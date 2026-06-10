package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/stull/spec"
)

// Tracer, if non-nil, receives one human-readable breadcrumb per dispatched
// event: the event, the state move, the decision kind, and the fuel move. It is
// observability only — called after the hook protocol is already decided, it
// cannot touch stdout or the exit code, so enabling it can never change
// behavior. cmd/stull wires it to stderr when STULL_TRACE is set; a library
// embedder can point it anywhere. This is the cure for "I ran the hook and got
// silence, and couldn't tell success from misuse."
var Tracer func(line string)

// traceLine formats one observability breadcrumb. Pure — no IO, no globals — so
// it is unit-tested directly and can never be the thing that breaks fail-open.
func traceLine(machine, event, from, to string, fromFuel, toFuel int, out Output) string {
	if event == "" {
		event = "(no hook_event_name)"
	}
	move := from
	if to != from {
		move = from + "->" + to
	}
	line := fmt.Sprintf("stull[%s] %s: %s | %s (fuel %d->%d)", machine, event, move, out.Kind(), fromFuel, toFuel)
	if len(out.Vars) > 0 {
		line += " vars:" + strings.Join(out.Vars, ",")
	}
	return line
}

// RunHook is the entire live-hook path for one machine, fail-open end to end:
// read a hook event as JSON on stdin, advance the machine, persist the new
// state, deliver any Emit effects to the local inbox, and write the Claude Code
// hook protocol (exit 2 + stderr to block, additionalContext JSON to inject).
// Any problem — unreadable stdin, a panic in dispatch — yields exit 0 with no
// output, so a broken hook can never brick a session.
//
// This is the body `stull run` executes, exposed so a custom binary that imports
// stull is the same one line, no fork of this repo:
//
//	func main() { os.Exit(runtime.RunHook(mymachine.Machine(), runtime.AnthropicModel)) }
//
// model is the one seam: pass AnthropicModel for the live Messages API, or your
// own fail-safe Model. If no calibration sink is installed, RunHook installs the
// default local FileCalibrator under ~/.cache/stull/<machine>/.
func RunHook(m spec.Machine, model Model) int {
	var event map[string]any
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		return 0 // fail-open: no event, no action
	}
	ctx := LoadContext(m, event)
	if Calibrate == nil {
		if home, err := os.UserHomeDir(); err == nil {
			Calibrate = FileCalibrator(filepath.Join(home, ".cache", "stull", m.Name))
		}
	}
	from, fromFuel := ctx.State, ctx.Fuel
	out := SafeDispatch(m, event, ctx, model)
	_ = SaveContext(m, ctx)
	deliverInbox(m.Name, out.Emits)
	if Tracer != nil {
		Tracer(traceLine(m.Name, asString(event["hook_event_name"]), from, ctx.State, fromFuel, ctx.Fuel, out))
	}
	return Emit(spec.Trigger(asString(event["hook_event_name"])), out)
}

// deliverInbox writes each Emit to a stull-defined LOCAL inbox (not a live bus).
// Dir from $STULL_INBOX_DIR, else ~/.cache/stull/inbox. Fail-open: a delivery
// error never disturbs the hook. A separate adapter (not built here) forwards
// these onto a real bus once that on-disk contract is verified.
func deliverInbox(machine string, emits []Emission) {
	if len(emits) == 0 {
		return
	}
	dir := os.Getenv("STULL_INBOX_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(home, ".cache", "stull", "inbox")
	}
	for _, e := range emits {
		_ = WriteInbox(dir, machine, e)
	}
}
