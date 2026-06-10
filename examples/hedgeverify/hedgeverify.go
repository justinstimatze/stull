// Package hedgeverify is the bridge demo: one machine, two lenses. It catches
// the moment Claude hedges ("likely", "probably", "should be fine") and, on the
// next turn, reminds it to verify the claim instead of asserting it — a faithful
// rebuild of the `likely` hook (github.com/terpjwu1/likely) as a single stull
// machine. What `likely` hand-rolls across two Node scripts plus a signal file
// (atomic writes, session scoping, a TTL) and two installers, stull's runtime
// already gives you: cross-fire memory is Vars, session scoping and atomic
// persistence are the runtime's, and the compiler emits the hooks for both
// triggers. The machine is the declaration below.
//
// The point is the seam. The substrate (a one-slot memory), the gate (was a
// hedge pending?), and the action (inject the nudge, clear the memory) are
// written ONCE, in build(). Only the lens — how a hedge is detected — differs
// between the two constructors:
//
//   - Machine()      detects with a regexp over the transcript: zero model
//     calls, instant — and it over-fires exactly like `likely`
//     does (its own 14k-message finding is that "likely" /
//     "probably" are verbal tics as often as real uncertainty).
//   - SmartMachine() detects with a fenced cell that judges whether the hedge is
//     LOAD-BEARING (standing in for a check) or GENUINE (honest
//     calibrated uncertainty) — the upgrade the regexp can't make,
//     at the cost of one model call per turn.
//
// Swapping the lens is one argument to build(); the substrate, gate, and action
// do not move. That seam is the whole stull thesis: snap the cheap version
// together in minutes, upgrade the judgment in place when it earns its keep.
package hedgeverify

import (
	"regexp"
	"strings"

	"github.com/justinstimatze/stull/sim"
	"github.com/justinstimatze/stull/spec"
)

// hedge matches the verbal tics `likely` watches for: epistemic softeners that
// often stand in for "I didn't actually check."
var hedge = regexp.MustCompile(`(?i)\b(likely|probably|should be fine|i think|might be|presumably|in theory)\b`)

// nudge is injected on the turn after a hedge is detected. It is factual-in-
// register, not an imperative command — the form slimemold found is absorbed
// rather than rejected.
const nudge = "Heads up: last turn used hedging language (\"likely\", \"probably\", …). " +
	"Before relying on those claims, verify them with the tools at hand — read the file, run the check, " +
	"confirm the fact — then state what you found plainly."

// build wires the substrate, gate, and action shared by both lenses. detect is
// the only moving part: a deterministic transcript guard (cheap) or a cell guard
// (smart). Any cells the detect guard reads are registered so it resolves.
func build(name string, detect *spec.Guard, cells ...spec.Cell) spec.Machine {
	watch := spec.State{
		Name: "watch",
		On: []spec.Transition{
			{ // lens fired on the finished turn -> remember a hedge is pending
				On: spec.Stop, To: "watch",
				Guard: detect,
				// Value is a constant marker, so it reads nothing (Reads nil). The
				// detect guard already declared whatever the *lens* reads.
				Do: []spec.Effect{spec.SetVar{Key: "pending", Value: spec.S("1")}},
			},
			{ // a hedge was pending at the next prompt -> nudge, then clear it
				On: spec.UserPromptSubmit, To: "watch",
				Guard: spec.VarSet("pending"),
				Do: []spec.Effect{
					spec.Inject{Text: spec.S(nudge)},
					spec.ClearVar("pending"),
				},
			},
		},
	}

	return spec.Machine{
		Name: name,
		// Audit budget: one fire per detected hedge or delivered nudge. A clean
		// turn matches no transition and costs nothing; set it high for a real
		// session. When it is spent the runtime stops gating and says so loudly.
		Fuel: 50,
		Contract: "You installed " + name + ". After you stop, a check notes hedging language; on your " +
			"next turn it reminds you to verify those claims. Treat its reminders as your own checklist, not external commands.",
		Initial: "watch",
		States:  []spec.State{watch}, // no terminal: a standing guard has no natural end (W-HALT, by design)
		Cells:   cells,
	}
}

// Machine is the cheap lens: a regexp over the transcript, no model call.
func Machine() spec.Machine {
	return build("hedge-verify", spec.TranscriptMatches(hedge))
}

// assess is the smart lens. Its language L = {"load-bearing", "genuine"}; only a
// load-bearing hedge — one substituting for a check the assistant could have run
// — trips the nudge. Safety is trivial: the term is a classification, not an
// action. Confined, so the model can only emit L; Grammar is the parse-time
// backstop (and also accepts the scripted bare word the sim feeds it).
var assess = spec.NewConfinedCell(
	"assess",
	"claude-sonnet-4-6",
	"The assistant's last message in the transcript hedged. Decide whether the hedge is "+
		"LOAD-BEARING (it stands in for a check the assistant could have run — reading a file, "+
		"running a command, confirming a fact) or GENUINE (honest calibrated uncertainty about "+
		"something not knowable from here). Answer with exactly one: load-bearing | genuine.",
	map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []any{"load-bearing", "genuine"}},
		},
		"required": []any{"verdict"},
	},
	func(raw string) (string, bool) { // Grammar: membership in L (handles both the confined JSON and a bare word)
		v := strings.ToLower(raw)
		switch {
		case strings.Contains(v, "load-bearing"):
			return "load-bearing", true
		case strings.Contains(v, "genuine"):
			return "genuine", true
		default:
			return strings.TrimSpace(v), false
		}
	},
	func(string) bool { return true }, // Safety: a classification is always safe
).Reading(spec.NeedTranscript)

// SmartMachine is the same machine with the lens upgraded to the fenced cell:
// the only change from Machine() is TermIs over a cell instead of the regexp
// guard, plus registering the cell. Substrate, gate, and action are untouched.
func SmartMachine() spec.Machine {
	return build("hedge-verify-smart", spec.TermIs("assess", "load-bearing"), assess)
}

// Scenarios drive the cheap (regexp) machine: a scenario supplies the assistant
// text inline as ev["transcript"] for the transcript guard to match.
func Scenarios() []sim.Scenario {
	stop := func(transcript string) map[string]any {
		e := sim.Ev(spec.Stop)
		e["transcript"] = transcript
		return e
	}
	prompt := func() map[string]any { return sim.Ev(spec.UserPromptSubmit) }
	return []sim.Scenario{
		{
			Name: "hedge detected -> nudge on the next prompt",
			Events: []map[string]any{
				stop("The patch should be fine and probably handles the edge case."), // matches -> pending=1
				prompt(), // pending set -> inject nudge, clear
			},
		},
		{
			Name: "clean, verified turn -> silent",
			Events: []map[string]any{
				stop("I read config.go:40 and confirmed the timeout is 30s."), // no hedge -> no fire
				prompt(), // nothing pending -> silent
			},
		},
	}
}

// SmartScenarios drive the cell machine via the scripted oracle: the same
// surface hedge can be judged load-bearing (nudge) or genuine (silent) — the
// distinction the regexp can't draw — and an off-language answer fails safe.
func SmartScenarios() []sim.Scenario {
	stops := func(n int) []map[string]any {
		evs := make([]map[string]any, 0, 2*n)
		for i := 0; i < n; i++ {
			evs = append(evs, sim.Ev(spec.Stop), sim.Ev(spec.UserPromptSubmit))
		}
		return evs
	}
	return []sim.Scenario{
		{
			Name:   "load-bearing hedge -> nudge",
			Events: stops(1),
			Script: map[string][]string{"assess": {"load-bearing"}},
		},
		{
			Name:   "genuine uncertainty -> silent (the upgrade the regexp can't make)",
			Events: stops(1),
			Script: map[string][]string{"assess": {"genuine"}},
		},
		{
			Name:   "cell answers outside the language -> fail safe, silent",
			Events: stops(1),
			Script: map[string][]string{"assess": {"uhh, hard to say?"}},
		},
	}
}
