package runtime

import (
	"regexp"
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// noModel is a Model that should never be called: the deterministic paths here
// (transcript guard, var guard, SetVar) involve no oracle.
func noModel(spec.Cell, *spec.Context) string { return "" }

// A transcript guard fires deterministically off ctx.Transcript, a SetVar
// accumulates into Vars, and a later VarSet guard reads it back — the whole
// set-on-detect / gate-on-memory / clear-on-consume accumulation, no model call.
func TestTranscriptGuardAndVarAccumulation(t *testing.T) {
	hedge := regexp.MustCompile(`(?i)\bprobably\b`)
	m := spec.Machine{
		Name: "acc", Fuel: 5, Initial: "watch",
		States: []spec.State{{Name: "watch", On: []spec.Transition{
			{On: spec.Stop, To: "watch",
				Guard: spec.TranscriptMatches(hedge),
				Do:    []spec.Effect{spec.SetVar{Key: "pending", Value: spec.S("1")}}},
			{On: spec.UserPromptSubmit, To: "watch",
				Guard: spec.VarSet("pending"),
				Do:    []spec.Effect{spec.Inject{Text: spec.S("verify it")}, spec.ClearVar("pending")}},
		}}},
	}

	ctx := &spec.Context{State: "watch", Fuel: 5, Vars: map[string]any{}}

	// Stop with a hedging transcript -> records pending.
	ctx.Transcript = "this probably works"
	out := Dispatch(m, map[string]any{"hook_event_name": "Stop"}, ctx, noModel)
	if ctx.Var("pending") != "1" {
		t.Fatalf("expected pending=1 after a hedging Stop, got %q", ctx.Var("pending"))
	}
	if len(out.Vars) != 1 || !strings.HasPrefix(out.Vars[0], "pending=") {
		t.Fatalf("expected SetVar in Output.Vars, got %v", out.Vars)
	}

	// Next prompt -> nudge fires off memory, then clears it.
	out = Dispatch(m, map[string]any{"hook_event_name": "UserPromptSubmit"}, ctx, noModel)
	if len(out.Inject) != 1 {
		t.Fatalf("expected a nudge injected while pending, got %v", out.Inject)
	}
	if ctx.Var("pending") != "" {
		t.Fatalf("expected pending cleared after the nudge, got %q", ctx.Var("pending"))
	}

	// A clean Stop (no hedge) records nothing and spends no fuel.
	ctx.Transcript = "I read the file and confirmed it"
	fuelBefore := ctx.Fuel
	Dispatch(m, map[string]any{"hook_event_name": "Stop"}, ctx, noModel)
	if ctx.Var("pending") != "" || ctx.Fuel != fuelBefore {
		t.Fatalf("a clean turn must not fire (pending %q, fuel %d->%d)", ctx.Var("pending"), fuelBefore, ctx.Fuel)
	}
}

// The 1h cache TTL keeps the frozen instruction (and a confined cell's schema)
// warm across hook fires minutes apart, instead of re-billing every fire.
func TestBuildRequestWarmCache(t *testing.T) {
	c := spec.NewConfinedCell("assess", "claude-sonnet-4-6", "judge it",
		map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"v": map[string]any{"type": "string"}}},
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true })
	body, err := buildRequest(c, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Count(s, `"ttl":"1h"`) != 2 {
		t.Fatalf("expected 1h cache_control on both the system block and the tool schema, got: %s", s)
	}
}
