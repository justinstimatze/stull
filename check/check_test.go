package check_test

import (
	"strings"
	"testing"

	"github.com/justinstimatze/stull/check"
	"github.com/justinstimatze/stull/examples/reviewloop"
	"github.com/justinstimatze/stull/spec"
)

func hasCode(errs []string, code string) bool {
	for _, e := range errs {
		if strings.HasPrefix(e, code) {
			return true
		}
	}
	return false
}

func TestExampleIsSound(t *testing.T) {
	if errs := check.Check(reviewloop.Machine()); len(errs) != 0 {
		t.Fatalf("review-loop should be sound, got: %v", errs)
	}
}

func TestUngatedOracleIsInexpressible(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewCell with a nil grammar/safety should panic")
		}
	}()
	_ = spec.NewCell("x", "m", "i", nil, nil)
}

func TestGuardOnRawOutputRejected(t *testing.T) {
	c := spec.NewCell("c", "m", "i",
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true })
	m := spec.Machine{
		Name: "bad", Fuel: 2, Initial: "a", Cells: []spec.Cell{c},
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{
				On: spec.Stop, To: "b",
				Guard: &spec.Guard{Reads: []string{"cells.c.raw"}, When: func(*spec.Context) bool { return true }},
			}}},
			{Name: "b", Terminal: true},
		},
	}
	if errs := check.Check(m); !hasCode(errs, "E-ORACLE") {
		t.Fatalf("expected E-ORACLE for a guard reading cells.c.raw, got: %v", errs)
	}
}

func TestSetVarLaunderingRejected(t *testing.T) {
	// A SetVar that stashes a cell's raw output into memory is the laundering
	// path E-ORACLE must close: a later guard could branch on that var and thus
	// gate on unvalidated oracle output. The same rule that guards a guard.
	c := spec.NewCell("c", "m", "i",
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true })
	m := spec.Machine{
		Name: "bad", Fuel: 2, Initial: "a", Cells: []spec.Cell{c},
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{
				On: spec.Stop, To: "b",
				Do: []spec.Effect{spec.SetVar{Key: "k", Reads: []string{"cells.c.raw"}, Value: spec.S("x")}},
			}}},
			{Name: "b", Terminal: true},
		},
	}
	if errs := check.Check(m); !hasCode(errs, "E-ORACLE") {
		t.Fatalf("expected E-ORACLE for a SetVar reading cells.c.raw, got: %v", errs)
	}
}

func TestSetVarValidatedReadAccepted(t *testing.T) {
	// Accumulating a cell's *validated* term is exactly what SetVar is for; it
	// must not trip E-ORACLE (only .raw does).
	c := spec.NewCell("c", "m", "i",
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true })
	m := spec.Machine{
		Name: "ok", Fuel: 2, Initial: "a", Cells: []spec.Cell{c},
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{
				On: spec.Stop, To: "b",
				Do: []spec.Effect{spec.SetVar{Key: "k", Reads: []string{"cells.c.term", "vars.k", "transcript"}, Value: spec.S("x")}},
			}}},
			{Name: "b", Terminal: true},
		},
	}
	if errs := check.Check(m); hasCode(errs, "E-ORACLE") || hasCode(errs, "E-CELL") {
		t.Fatalf("a SetVar reading validated term/vars/transcript should be sound, got: %v", errs)
	}
}

func TestUnconditionalTransitionShadowsLaterOne(t *testing.T) {
	// An unconditional transition makes any later same-trigger transition dead
	// code; the checker must warn (W-SHADOW), not pass it silently.
	m := spec.Machine{
		Name: "shadow", Fuel: 3, Initial: "a",
		States: []spec.State{
			{Name: "a", On: []spec.Transition{
				{On: spec.Stop, To: "b"}, // unconditional: always fires first
				{On: spec.Stop, To: "c", // unreachable
					Guard: &spec.Guard{When: func(*spec.Context) bool { return true }}},
			}},
			{Name: "b", Terminal: true},
			{Name: "c", Terminal: true},
		},
	}
	rep := check.Inspect(m)
	if len(rep.Errors) != 0 {
		t.Fatalf("shadowing is a warning, not an error: %v", rep.Errors)
	}
	if !hasCode(rep.Warnings, "W-SHADOW") {
		t.Fatalf("expected W-SHADOW for the unreachable transition, got: %v", rep.Warnings)
	}
}

func TestInjectChannelMismatch(t *testing.T) {
	// Inject on PreToolUse: that trigger cannot inject.
	m := spec.Machine{
		Name: "bad", Fuel: 2, Initial: "a",
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{
				On: spec.PreToolUse, To: "b",
				Do: []spec.Effect{spec.Inject{Text: spec.S("hi")}},
			}}},
			{Name: "b", Terminal: true},
		},
	}
	if errs := check.Check(m); !hasCode(errs, "E-INJECT") {
		t.Fatalf("expected E-INJECT, got: %v", errs)
	}
}

func TestBlockChannelMismatch(t *testing.T) {
	// Block on PostToolUse: that trigger cannot halt control flow.
	m := spec.Machine{
		Name: "bad", Fuel: 2, Initial: "a",
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{
				On: spec.PostToolUse, To: "b",
				Do: []spec.Effect{spec.Block{Reason: spec.S("no")}},
			}}},
			{Name: "b", Terminal: true},
		},
	}
	if errs := check.Check(m); !hasCode(errs, "E-BLOCK") {
		t.Fatalf("expected E-BLOCK, got: %v", errs)
	}
}

func TestStructuralProblems(t *testing.T) {
	cases := []struct {
		name string
		m    spec.Machine
		code string
	}{
		{"zero fuel", spec.Machine{Name: "m", Fuel: 0, Initial: "a",
			States: []spec.State{{Name: "a", Terminal: true}}}, "E-FUEL"},
		{"bad target", spec.Machine{Name: "m", Fuel: 1, Initial: "a",
			States: []spec.State{{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "nope"}}}}}, "E-TARGET"},
		{"orphan", spec.Machine{Name: "m", Fuel: 1, Initial: "a",
			States: []spec.State{{Name: "a", Terminal: true}, {Name: "b", Terminal: true}}}, "E-ORPHAN"},
		{"undefined initial", spec.Machine{Name: "m", Fuel: 1, Initial: "z",
			States: []spec.State{{Name: "a", Terminal: true}}}, "E-INITIAL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errs := check.Check(tc.m); !hasCode(errs, tc.code) {
				t.Fatalf("expected %s, got: %v", tc.code, errs)
			}
		})
	}
}

// A standing guard — a fuel-bounded self-loop with no terminal — must COMPILE
// (totality still holds via the E-FUEL floor) but WARN loudly (W-HALT), so the
// shape can't pass silently. This is the crystal use case: block a bad command
// every fire until the session/fuel ends.
func TestStandingGuardCompilesButWarns(t *testing.T) {
	standing := spec.Machine{Name: "guard", Fuel: 100, Initial: "watch",
		States: []spec.State{{Name: "watch", On: []spec.Transition{{On: spec.Stop, To: "watch"}}}}}
	rep := check.Inspect(standing)
	if !rep.Sound() {
		t.Fatalf("a fuel-bounded standing guard must compile, got errors: %v", rep.Errors)
	}
	if !hasCode(rep.Warnings, "W-HALT") {
		t.Fatalf("a no-terminal machine must warn W-HALT, got warnings: %v", rep.Warnings)
	}
	// And the old hard error is gone: no E-HALT in errors.
	if hasCode(rep.Errors, "E-HALT") {
		t.Fatalf("W-HALT must be a warning, not an error: %v", rep.Errors)
	}
}
