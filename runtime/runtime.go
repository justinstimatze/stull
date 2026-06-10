// Package runtime is the generic hook dispatcher — the single command every
// registered hook runs. One invocation, per hook event, that:
//
//  1. loads the per-session context (state + fuel + cell cache),
//  2. selects the first transition whose trigger matches and whose guard holds,
//     running any cells the guard depends on (lazily, then validating them),
//  3. applies the transition's effects, decrements fuel, advances state, persists,
//  4. emits the hook protocol (additionalContext to inject, exit 2 to block).
//
// Two safety properties are enforced here, not hoped for:
//
//   - fail-open: SafeDispatch recovers from any panic and yields a no-op, so a
//     broken hook can never brick the session (hindcast's Guard discipline).
//   - totality: once fuel reaches 0 the loop stops firing transitions (it can no
//     longer Block), so a Stop-loop terminates regardless of what the oracle says.
//
// The model call is the Model func. AnthropicModel (model.go) binds it to the
// Messages API; sim drives the same seam with a scripted Model. Everything else
// here is deterministic.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/justinstimatze/stull/check"
	"github.com/justinstimatze/stull/spec"
)

// Model runs a cell and returns the raw model completion. It is the one
// non-deterministic seam: a confined oracle whose output is checked by the
// cell before it can reach control flow. A Model that errors must fail safe by
// returning a value outside the cell's language (the empty string works), never
// panic or block — see AnthropicModel in model.go.
type Model func(spec.Cell, *spec.Context) string

// Output is the result of one dispatch step, before it is rendered to the hook
// protocol.
type Output struct {
	Inject     []string
	Block      *string
	Emits      []Emission
	Vars       []string // "key=value" of each SetVar write this step (observability only; the value is already persisted on the context)
	Lint       []string // authoring-time warnings (e.g. a guard read an undeclared cell); surfaced by sim, ignored by the live hook
	BudgetHalt bool
}

type Emission struct{ Target, Message string }

func (o Output) Kind() string {
	switch {
	case o.Block != nil:
		return "block"
	case o.BudgetHalt:
		return "budget"
	case len(o.Inject) > 0 || len(o.Emits) > 0:
		return "inject"
	case len(o.Vars) > 0:
		return "setvar"
	default:
		return "noop"
	}
}

func runCell(machine string, c spec.Cell, ctx *spec.Context, model Model) *spec.CellResult {
	if r, ok := ctx.Cells[c.Name]; ok && r.Ran {
		return r
	}
	raw := model(c, ctx)
	res := c.Check(raw) // formal-language membership, then safety
	r := &res
	if ctx.Cells == nil {
		ctx.Cells = map[string]*spec.CellResult{}
	}
	ctx.Cells[c.Name] = r
	emitCell(machine, ctx, c, r) // calibration tap (no-op unless a sink is set)
	return r
}

// Dispatch advances the machine one step for event. It mutates ctx and is pure
// given model.
func Dispatch(m spec.Machine, event map[string]any, ctx *spec.Context, model Model) Output {
	byName := map[string]spec.State{}
	for _, s := range m.States {
		byName[s.Name] = s
	}
	cellMap := map[string]spec.Cell{}
	for _, c := range m.Cells {
		cellMap[c.Name] = c
	}

	// Cell results are ephemeral per hook event: the transcript changes every
	// turn, so a cell must re-run each event. Within a single dispatch the
	// result is cached (one model call even if several guards read it).
	ctx.Cells = map[string]*spec.CellResult{}

	state := byName[ctx.State]
	if state.Terminal {
		return Output{}
	}
	trigger := spec.Trigger(asString(event["hook_event_name"]))

	// Totality: out of fuel -> never block again; release the loop.
	if ctx.Fuel <= 0 {
		out := Output{BudgetHalt: true}
		if spec.Gating(trigger) {
			out.Inject = append(out.Inject, fmt.Sprintf("[%s] step budget (%d) exhausted; halting.", m.Name, m.Fuel))
		}
		emitStep(m.Name, ctx, trigger, state.Name, state.Name, out)
		return out
	}

	var lint []string
	for _, t := range state.On {
		if t.On != trigger {
			continue
		}
		if t.Guard != nil {
			for _, cr := range check.CellReads(t.Guard.Reads) {
				if c, ok := cellMap[cr.Cell]; ok {
					runCell(m.Name, c, ctx, model)
				}
			}
			ctx.ClearTouched()
			varsBefore := snapshotVars(ctx.Vars)
			held := t.Guard.When(ctx)
			lint = append(lint, undeclaredCellReads(t.Guard, ctx, state.Name)...)
			if !reflect.DeepEqual(varsBefore, ctx.Vars) {
				lint = append(lint, fmt.Sprintf("%s: guard mutated Vars during evaluation — a guard's When must be pure; write state with a SetVar effect instead", state.Name))
			}
			if !held {
				continue
			}
		}
		var out Output
		out.Lint = lint
		for _, eff := range t.Do {
			switch e := eff.(type) {
			case spec.Inject:
				out.Inject = append(out.Inject, spec.Resolve(e.Text, ctx))
			case spec.Block:
				r := spec.Resolve(e.Reason, ctx)
				out.Block = &r
			case spec.Run:
				runCell(m.Name, e.Cell, ctx, model)
			case spec.Emit:
				out.Emits = append(out.Emits, Emission{Target: e.Target, Message: spec.Resolve(e.Message, ctx)})
			case spec.SetVar:
				if ctx.Vars == nil {
					ctx.Vars = map[string]any{}
				}
				v := spec.Resolve(e.Value, ctx)
				ctx.Vars[e.Key] = v
				out.Vars = append(out.Vars, e.Key+"="+v)
			}
		}
		ctx.Fuel--
		ctx.State = t.To
		emitStep(m.Name, ctx, trigger, state.Name, t.To, out)
		return out
	}
	return Output{Lint: lint} // no matching transition: a no-op for this event
}

// snapshotVars shallow-copies Vars so the runtime can tell whether a guard's
// When mutated it (guards must be pure). Top-level writes are caught; by
// convention Vars values are immutable strings, so a shallow copy suffices.
func snapshotVars(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	cp := make(map[string]any, len(v))
	for k, val := range v {
		cp[k] = val
	}
	return cp
}

// undeclaredCellReads reports any cell a guard's When actually read that its
// Reads did not declare. Such a cell is never run by the pre-scan, so the guard
// sees a zero CellResult and is silently always-false — the under-declared-Reads
// footgun. It cannot be caught statically (the When is an opaque closure), so it
// is caught here at evaluation time and surfaced loudly by sim.
func undeclaredCellReads(g *spec.Guard, ctx *spec.Context, state string) []string {
	declared := map[string]bool{}
	for _, cr := range check.CellReads(g.Reads) {
		declared[cr.Cell] = true
	}
	var out []string
	for _, name := range ctx.TouchedCells() {
		if !declared[name] {
			out = append(out, fmt.Sprintf(
				"%s: guard read cells.%s but did not declare it in Reads — the cell never runs, so the guard is silently always-false (use spec.TermIs, or add %q to Reads)",
				state, name, "cells."+name+".term"))
		}
	}
	return out
}

// SafeDispatch wraps Dispatch with fail-open recovery.
func SafeDispatch(m spec.Machine, event map[string]any, ctx *spec.Context, model Model) (out Output) {
	defer func() {
		if r := recover(); r != nil {
			out = Output{}
		}
	}()
	return Dispatch(m, event, ctx, model)
}

// --- persistence + hook protocol --------------------------------------------

// persisted is the cross-turn state. Cell results are deliberately absent: they
// are ephemeral per event (see Dispatch). Only state, fuel, and machine vars
// survive between hook fires.
type persisted struct {
	State string         `json:"state"`
	Fuel  int            `json:"fuel"`
	Vars  map[string]any `json:"vars"`
}

func statePath(m spec.Machine, session string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "stull", m.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, session+".json"), nil
}

func LoadContext(m spec.Machine, event map[string]any) *spec.Context {
	session := asString(event["session_id"])
	if session == "" {
		session = "default"
	}
	ctx := &spec.Context{Event: event, State: m.Initial, Fuel: m.Fuel,
		Vars: map[string]any{}, Cells: map[string]*spec.CellResult{}}
	// Supply the transcript tail to deterministic transcript guards. Cells get
	// the transcript through their own declared Context (assembleContext); this
	// path is for guards/effects that match it without a model call. Loaded only
	// when the machine actually reads it, so a transcript-free machine pays
	// nothing. Set before the persisted-state load so every return path has it.
	if machineReadsTranscript(m) {
		ctx.Transcript = transcriptTail(event)
	}
	p, err := statePath(m, session)
	if err != nil {
		return ctx
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ctx
	}
	var ps persisted
	if json.Unmarshal(data, &ps) != nil {
		return ctx
	}
	ctx.State, ctx.Fuel = ps.State, ps.Fuel
	if ps.Vars != nil {
		ctx.Vars = ps.Vars
	}
	return ctx
}

// machineReadsTranscript reports whether any guard or SetVar in m declares a
// "transcript" read, so LoadContext only pays the transcript file read when a
// deterministic guard/effect actually needs it.
func machineReadsTranscript(m spec.Machine) bool {
	for _, s := range m.States {
		for _, t := range s.On {
			if t.Guard != nil && hasTranscriptRead(t.Guard.Reads) {
				return true
			}
			for _, eff := range t.Do {
				if sv, ok := eff.(spec.SetVar); ok && hasTranscriptRead(sv.Reads) {
					return true
				}
			}
		}
	}
	return false
}

func hasTranscriptRead(reads []string) bool {
	for _, r := range reads {
		if r == "transcript" {
			return true
		}
	}
	return false
}

func SaveContext(m spec.Machine, ctx *spec.Context) error {
	session := asString(ctx.Event["session_id"])
	if session == "" {
		session = "default"
	}
	p, err := statePath(m, session)
	if err != nil {
		return err
	}
	data, err := json.Marshal(persisted{State: ctx.State, Fuel: ctx.Fuel, Vars: ctx.Vars})
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p) // atomic
}

// Emit writes the Claude Code hook protocol for out and returns the exit code.
func Emit(trigger spec.Trigger, out Output) int {
	// Loud to the human: a machine that exhausted its fuel has stopped gating.
	// For a standing guard that means it has STOPPED guarding this session — a
	// silent release would be the one failure mode the design refuses to allow.
	if out.BudgetHalt {
		fmt.Fprintln(os.Stderr, "stull: step budget exhausted — machine is no longer gating; if this is a standing guard it has STOPPED guarding this session.")
	}
	if out.Block != nil && spec.Gating(trigger) {
		fmt.Fprintln(os.Stderr, *out.Block)
		return 2
	}
	if len(out.Inject) > 0 {
		payload := map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":     string(trigger),
			"additionalContext": strings.Join(out.Inject, "\n"),
		}}
		b, _ := json.Marshal(payload)
		fmt.Println(string(b))
	}
	return 0
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
