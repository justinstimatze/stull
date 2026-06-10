// Package check is the static checker. It makes the genuinely unsafe mesh shapes
// inexpressible (hard errors) and shouts about the merely risky ones (warnings),
// because stull's job is to let you snap hook+Claude workflows together fast and
// trust they'll *probably* behave — not to prove they always halt. So it splits
// its findings: Errors block compilation; Warnings never do, they just make sure
// you can't build a risky shape *quietly*.
//
// Hard errors (Errors) — the shape is unsound and will not compile:
//
//	E-FUEL      fuel is not a positive step budget (totality's one hard floor)
//	E-DUP       duplicate state name
//	E-INITIAL   initial state undefined
//	E-TARGET    transition targets an undefined state
//	E-TERMINAL  a terminal state has outgoing transitions
//	E-ORPHAN    a state is unreachable from initial
//	E-CELL      a guard/Run references a cell not in the machine registry
//	E-ORACLE    a guard gates control flow on a cell's *raw* output
//	E-INJECT    an Inject effect sits on a trigger that cannot inject
//	E-BLOCK     a Block effect sits on a trigger that cannot block
//
// Warnings (Warnings) — the machine compiles, but loudly:
//
//	W-HALT      no terminal state is reachable, so the machine only stops when
//	            its fuel budget is exhausted. That is exactly what a standing
//	            guard wants; for anything else it is probably a missing terminal.
//	            Totality still holds (fuel is a hard E-FUEL floor), so this is a
//	            heads-up, not a defect.
//	W-SHADOW    a transition is statically unreachable because an earlier
//	            unconditional transition on the same trigger always fires first.
//	            Dead code — almost always an ordering mistake.
//
// One footgun is undecidable statically and so is caught at *sim* time instead:
// a guard whose When reads a cell its Reads does not declare. The cell never
// runs, so the guard is silently always-false. sim instruments the context and
// flags the mismatch (and exits non-zero), turning a silent bug into a loud one.
//
// Cell-has-a-validator is not checked here: it is enforced one level down, by
// construction (spec.NewCell rejects a nil validator and the field is
// unexported), so an ungated oracle cannot be built in the first place.
package check

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/justinstimatze/stull/spec"
)

var cellPath = regexp.MustCompile(`^cells\.(\w+)\.(\w+)$`)

// Report is the full result of inspecting a machine. Errors block compilation
// (the shape is unsound); Warnings do not (the shape is sound but risky and must
// be surfaced loudly). A machine with only Warnings still compiles and runs.
type Report struct {
	Errors   []string
	Warnings []string
}

// Sound reports whether the machine has no hard errors. Warnings do not affect
// soundness — a warned machine is still buildable.
func (r Report) Sound() bool { return len(r.Errors) == 0 }

// CellRead is a parsed "cells.<Cell>.<Field>" guard dependency.
type CellRead struct{ Cell, Field string }

// CellReads extracts the cell dependencies declared in a guard's Reads.
func CellReads(reads []string) []CellRead {
	var out []CellRead
	for _, r := range reads {
		if m := cellPath.FindStringSubmatch(r); m != nil {
			out = append(out, CellRead{Cell: m[1], Field: m[2]})
		}
	}
	return out
}

// Inspect returns every finding about the machine, split into Errors (block
// compilation) and Warnings (compile, but loudly).
func Inspect(m spec.Machine) Report {
	var r Report
	byName := map[string]spec.State{}
	for _, s := range m.States {
		byName[s.Name] = s
	}
	cellNames := map[string]bool{}
	for _, c := range m.Cells {
		cellNames[c.Name] = true
	}

	// --- well-formedness ---
	if m.Fuel <= 0 {
		r.Errors = append(r.Errors, fmt.Sprintf("E-FUEL: fuel must be a positive step budget, got %d", m.Fuel))
	}
	seenName := map[string]int{}
	for _, s := range m.States {
		seenName[s.Name]++
	}
	for _, name := range sortedKeys(seenName) {
		if seenName[name] > 1 {
			r.Errors = append(r.Errors, fmt.Sprintf("E-DUP: duplicate state name %q", name))
		}
	}
	if _, ok := byName[m.Initial]; !ok {
		r.Errors = append(r.Errors, fmt.Sprintf("E-INITIAL: initial state %q is not defined", m.Initial))
	}
	for _, s := range m.States {
		for _, t := range s.On {
			if _, ok := byName[t.To]; !ok {
				r.Errors = append(r.Errors, fmt.Sprintf("E-TARGET: %s --%s--> undefined state %q", s.Name, t.On, t.To))
			}
		}
		if s.Terminal && len(s.On) > 0 {
			r.Errors = append(r.Errors, fmt.Sprintf("E-TERMINAL: terminal state %q has outgoing transitions", s.Name))
		}
	}

	// --- reachability + a reachable halt ---
	if _, ok := byName[m.Initial]; ok {
		seen := map[string]bool{}
		stack := []string{m.Initial}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[cur] {
				continue
			}
			seen[cur] = true
			for _, t := range byName[cur].On {
				if _, ok := byName[t.To]; ok {
					stack = append(stack, t.To)
				}
			}
		}
		for _, s := range m.States {
			if !seen[s.Name] {
				r.Errors = append(r.Errors, fmt.Sprintf("E-ORPHAN: state %q is unreachable from initial", s.Name))
			}
		}
		halt := false
		for name := range seen {
			if byName[name].Terminal {
				halt = true
				break
			}
		}
		if !halt {
			// Not an error: fuel (a hard E-FUEL floor) still guarantees the
			// machine stops. A standing guard *wants* this shape. Warn loudly so
			// an accidental missing-terminal can't slip by silently.
			r.Warnings = append(r.Warnings, fmt.Sprintf("W-HALT: %q has no reachable terminal state; it stops only when its fuel budget (%d) is exhausted. Intended for a standing guard; otherwise add a reachable terminal.", m.Name, m.Fuel))
		}
	}

	// --- transition shadowing (statically-unreachable transitions) ---
	// The runtime fires the first transition whose trigger matches and whose
	// guard holds. An unconditional (nil-guard) transition therefore always wins
	// for its trigger, making any later same-trigger transition dead code. That
	// is a footgun — the author wrote a branch that can never run — so warn
	// loudly. (Guarded shadowing, where two guards overlap, is undecidable here
	// and left to sim.)
	for _, s := range m.States {
		sawUnconditional := map[spec.Trigger]bool{}
		for _, t := range s.On {
			if sawUnconditional[t.On] {
				r.Warnings = append(r.Warnings, fmt.Sprintf("W-SHADOW: %s has a transition on %s that is unreachable — an earlier unconditional (guard-less) transition on %s always fires first", s.Name, t.On, t.On))
			}
			if t.Guard == nil {
				sawUnconditional[t.On] = true
			}
		}
	}

	// --- per-transition invariants ---
	for _, s := range m.States {
		for _, t := range s.On {
			if t.Guard != nil {
				for _, cr := range CellReads(t.Guard.Reads) {
					if !cellNames[cr.Cell] {
						r.Errors = append(r.Errors, fmt.Sprintf("E-CELL: %s guard reads unknown cell %q (not in machine.Cells)", s.Name, cr.Cell))
					}
					switch cr.Field {
					case "term", "wellformed", "safe":
					default:
						r.Errors = append(r.Errors, fmt.Sprintf("E-ORACLE: %s guard reads cells.%s.%s — control flow may only depend on a cell's formal-language output (term/wellformed/safe), never its raw output", s.Name, cr.Cell, cr.Field))
					}
				}
			}
			for _, eff := range t.Do {
				switch e := eff.(type) {
				case spec.Inject:
					if !spec.Injecting(t.On) {
						r.Errors = append(r.Errors, fmt.Sprintf("E-INJECT: Inject on a %s transition (%s); that trigger cannot inject context", t.On, s.Name))
					}
				case spec.Block:
					if !spec.Gating(t.On) {
						r.Errors = append(r.Errors, fmt.Sprintf("E-BLOCK: Block on a %s transition (%s); that trigger cannot halt control flow", t.On, s.Name))
					}
				case spec.Run:
					if !cellNames[e.Cell.Name] {
						r.Errors = append(r.Errors, fmt.Sprintf("E-CELL: %s runs cell %q not in machine.Cells", s.Name, e.Cell.Name))
					}
				case spec.SetVar:
					// A SetVar accumulates into memory; a later guard may branch on
					// that memory. So its Reads carry the same E-ORACLE contract as a
					// guard's — it may read a cell's validated output but never its
					// raw completion, or memory becomes a laundering channel.
					for _, cr := range CellReads(e.Reads) {
						if !cellNames[cr.Cell] {
							r.Errors = append(r.Errors, fmt.Sprintf("E-CELL: %s SetVar %q reads unknown cell %q (not in machine.Cells)", s.Name, e.Key, cr.Cell))
						}
						switch cr.Field {
						case "term", "wellformed", "safe":
						default:
							r.Errors = append(r.Errors, fmt.Sprintf("E-ORACLE: %s SetVar %q reads cells.%s.%s — memory may only accumulate a cell's formal-language output (term/wellformed/safe), never its raw output", s.Name, e.Key, cr.Cell, cr.Field))
						}
					}
				}
			}
		}
	}

	return r
}

// Check returns every hard error with the machine (empty == compiles). Warnings
// are deliberately excluded — use Inspect to surface them loudly.
func Check(m spec.Machine) []string { return Inspect(m).Errors }

// Validate returns an error listing every hard error, or nil if the machine
// compiles. Warnings do not make Validate fail (a warned machine still builds);
// callers that want them call Inspect.
func Validate(m spec.Machine) error {
	errs := Check(m)
	if len(errs) == 0 {
		return nil
	}
	out := fmt.Sprintf("machine %q is not sound:", m.Name)
	for _, e := range errs {
		out += "\n  " + e
	}
	return fmt.Errorf("%s", out)
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
