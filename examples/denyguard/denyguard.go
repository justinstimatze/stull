// Package denyguard is a worked *standing guard*: an oracle-free machine that
// denies a dangerous command every time it is attempted, for the whole session.
// It is the counterpart to reviewloop — no Cell, no model call, just a
// deterministic Block on a matching PreToolUse event.
//
// It exercises the shape a Stop-loop cannot: a fuel-bounded self-loop with no
// natural terminal. The checker does NOT reject that — a standing guard wants
// exactly it — but warns loudly (W-HALT), because the mandatory Fuel bound
// already guarantees totality on its own. Fuel here is the *denial* budget:
// safe commands are free (a no-match never spends fuel); only an actual block
// costs one. When the budget is exhausted the runtime stops gating AND shouts on
// stderr, so the guard can never silently stop guarding mid-session.
//
// Copy this as the template for any "deny command X" constraint: swap the regexp
// and set Fuel to the most denials a session should ever need (generously high
// in production — it is small here only so the demo can show the loud release).
package denyguard

import (
	"regexp"

	"github.com/justinstimatze/stull/sim"
	"github.com/justinstimatze/stull/spec"
)

// gitAddAll matches the staging-everything footguns: git add -A, --all, or `.`.
var gitAddAll = regexp.MustCompile(`(?i)\bgit\s+add\s+(-A\b|--all\b|\.(\s|$))`)

// Machine builds the standing guard. One state, one self-looping transition: on
// PreToolUse, if the command is a `git add` blanket-stage, refuse it.
func Machine() spec.Machine {
	watch := spec.State{
		Name: "watch",
		On: []spec.Transition{
			{ // dangerous command -> deny, stay watching (the standing self-loop)
				On: spec.PreToolUse, To: "watch",
				Guard: &spec.Guard{
					Reads: []string{"event.tool_name", "event.tool_input.command"},
					When:  spec.And(spec.ToolIs("Bash"), spec.CommandMatches(gitAddAll)),
				},
				Do: []spec.Effect{spec.Block{Reason: spec.S(
					"Blocked: `git add -A`/`--all`/`.` stages everything indiscriminately. " +
						"Stage the specific files you mean instead.")}},
			},
		},
	}

	return spec.Machine{
		Name: "deny-add-all",
		// Fuel is spent only by FIRED transitions: a safe command matches no
		// transition, so it fires nothing and costs nothing. Here every transition
		// is a Block, so fuel is effectively a denial budget. Small for the demo;
		// set it high for a real session-long constraint.
		Fuel: 3,
		Contract: "You installed deny-add-all. A deterministic guard refuses blanket `git add`. " +
			"Its refusals are your own guardrail, not an external command.",
		Initial: "watch",
		States:  []spec.State{watch}, // no terminal: a standing guard has no natural end (W-HALT, by design)
	}
}

// Scenarios show the guard denying the footgun, letting safe commands pass for
// free, and — when its denial budget runs out — releasing loudly rather than
// silently going quiet.
func Scenarios() []sim.Scenario {
	bash := func(cmd string) map[string]any {
		return map[string]any{
			"hook_event_name": "PreToolUse", "session_id": "sim",
			"tool_name": "Bash", "tool_input": map[string]any{"command": cmd},
		}
	}
	return []sim.Scenario{
		{
			Name: "denies the footgun, lets safe commands pass",
			Events: []map[string]any{
				bash("git add -A"),         // blocked
				bash("git add main.go"),    // safe: specific file -> passes, no fuel spent
				bash("git status"),         // safe: not an add -> passes
				bash("git add --all src/"), // blocked
			},
		},
		{
			Name: "loud release when the denial budget is exhausted",
			Events: []map[string]any{
				bash("git add -A"), // block (fuel 3->2)
				bash("git add ."),  // block (2->1)
				bash("git add -A"), // block (1->0)
				bash("git add -A"), // budget exhausted -> released loudly (in `run`, also stderr)
			},
		},
	}
}
