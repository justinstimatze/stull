package runtime

import (
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// The under-declared-Reads footgun: a guard whose When reads a cell its Reads
// does not declare is silently always-false (the cell never runs). It cannot be
// caught statically, so Dispatch flags it at evaluation time via Output.Lint.
func TestUndeclaredCellReadIsLinted(t *testing.T) {
	cell := spec.NewCell("assess", "m", "i",
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true })

	bad := &spec.Guard{
		Reads: nil, // forgot "cells.assess.term"
		When:  func(c *spec.Context) bool { return c.Cell("assess").Term == "yes" },
	}
	m := spec.Machine{
		Name: "fg", Fuel: 3, Initial: "a", Cells: []spec.Cell{cell},
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "b", Guard: bad,
				Do: []spec.Effect{spec.Block{Reason: spec.S("blocked")}}}}},
			{Name: "b", Terminal: true},
		},
	}

	model := func(spec.Cell, *spec.Context) string { return "yes" }
	ctx := &spec.Context{State: "a", Fuel: 3, Vars: map[string]any{}}
	out := Dispatch(m, map[string]any{"hook_event_name": "Stop"}, ctx, model)

	if len(out.Lint) == 0 {
		t.Fatal("expected a lint warning for the undeclared cell read")
	}
	if !strings.Contains(out.Lint[0], "cells.assess") || !strings.Contains(out.Lint[0], "silently always-false") {
		t.Fatalf("lint message should name the cell and the hazard, got: %q", out.Lint[0])
	}
}

// An impure guard — a When that mutates Vars — is silent state corruption. The
// runtime catches it and surfaces it as a lint.
func TestImpureGuardIsLinted(t *testing.T) {
	impure := &spec.Guard{
		When: func(c *spec.Context) bool {
			c.Vars["sneaky"] = "1" // a guard must not write state
			return false
		},
	}
	m := spec.Machine{
		Name: "impure", Fuel: 3, Initial: "a",
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "b", Guard: impure}}},
			{Name: "b", Terminal: true},
		},
	}
	ctx := &spec.Context{State: "a", Fuel: 3, Vars: map[string]any{}}
	out := Dispatch(m, map[string]any{"hook_event_name": "Stop"}, ctx, noModel)
	found := false
	for _, l := range out.Lint {
		if strings.Contains(l, "must be pure") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an impure-guard lint, got: %v", out.Lint)
	}
}

// A correctly-declared cell guard (spec.TermIs) must NOT trip the lint.
func TestDeclaredCellReadIsClean(t *testing.T) {
	cell := spec.NewCell("assess", "m", "i",
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true })
	m := spec.Machine{
		Name: "ok", Fuel: 3, Initial: "a", Cells: []spec.Cell{cell},
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "b",
				Guard: spec.TermIs("assess", "yes"),
				Do:    []spec.Effect{spec.Block{Reason: spec.S("blocked")}}}}},
			{Name: "b", Terminal: true},
		},
	}
	model := func(spec.Cell, *spec.Context) string { return "yes" }
	ctx := &spec.Context{State: "a", Fuel: 3, Vars: map[string]any{}}
	out := Dispatch(m, map[string]any{"hook_event_name": "Stop"}, ctx, model)
	if len(out.Lint) != 0 {
		t.Fatalf("a properly-declared TermIs guard must not lint, got: %v", out.Lint)
	}
	if out.Block == nil {
		t.Fatal("the declared guard should have fired")
	}
}
