package check

import (
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

func TestDescribe(t *testing.T) {
	cell := spec.NewCell("assess", "claude-sonnet-4-6", "i",
		func(r string) (string, bool) { return r, true },
		func(string) bool { return true }).Reading(spec.NeedTranscript)

	m := spec.Machine{
		Name: "d", Fuel: 3, Initial: "a", Cells: []spec.Cell{cell},
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "b",
				Guard: spec.TermIs("assess", "complete"),
				Do:    []spec.Effect{spec.Inject{Text: spec.S("ok")}}}}},
			{Name: "b", Terminal: true},
		},
	}

	d := Describe(m)
	if d.Name != "d" || d.Fuel != 3 || d.Initial != "a" || !d.Sound {
		t.Fatalf("header wrong: %+v", d)
	}
	if len(d.Triggers) != 1 || d.Triggers[0] != "Stop" {
		t.Errorf("triggers = %v, want [Stop]", d.Triggers)
	}
	if len(d.Cells) != 1 || d.Cells[0].Name != "assess" || d.Cells[0].Confined {
		t.Errorf("cell info wrong: %+v", d.Cells)
	}
	if len(d.Cells[0].Needs) != 1 || d.Cells[0].Needs[0] != "transcript" {
		t.Errorf("cell needs = %v, want [transcript]", d.Cells[0].Needs)
	}
	if len(d.States) != 2 || !d.States[1].Terminal {
		t.Fatalf("states wrong: %+v", d.States)
	}
	tr := d.States[0].Transitions
	if len(tr) != 1 || tr[0].On != "Stop" || tr[0].To != "b" || tr[0].Unconditional {
		t.Fatalf("transition wrong: %+v", tr)
	}
	if !strings.Contains(strings.Join(tr[0].GuardReads, ","), "cells.assess.term") {
		t.Errorf("guard reads = %v, want cells.assess.term", tr[0].GuardReads)
	}
	if len(tr[0].Effects) != 1 || tr[0].Effects[0] != "inject" {
		t.Errorf("effects = %v, want [inject]", tr[0].Effects)
	}
}
