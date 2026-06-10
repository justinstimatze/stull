package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// A one-cell Stop-loop: assess says complete -> done (inject), else -> loop
// (block). Mirrors the worked example, minimal here.
func calibMachine() spec.Machine {
	assess := spec.NewCell("assess", "m", "complete or incomplete",
		func(raw string) (string, bool) {
			v := strings.TrimSpace(raw)
			return v, v == "complete" || v == "incomplete"
		},
		func(string) bool { return true })
	loop := spec.State{Name: "loop", On: []spec.Transition{
		{On: spec.Stop, To: "done",
			Guard: &spec.Guard{Reads: []string{"cells.assess.term"},
				When: func(c *spec.Context) bool { return c.Cell("assess").WellFormed && c.Cell("assess").Term == "complete" }},
			Do: []spec.Effect{spec.Inject{Text: spec.S("done")}}},
		{On: spec.Stop, To: "loop",
			Guard: &spec.Guard{Reads: []string{"cells.assess.term"},
				When: func(c *spec.Context) bool {
					return c.Cell("assess").WellFormed && c.Cell("assess").Term == "incomplete"
				}},
			Do: []spec.Effect{spec.Block{Reason: spec.S("keep going")}}},
	}}
	return spec.Machine{Name: "calib-test", Fuel: 3, Initial: "loop",
		States: []spec.State{loop, {Name: "done", Terminal: true}},
		Cells:  []spec.Cell{assess}}
}

func TestCalibrationTapCapturesBothHalves(t *testing.T) {
	var got []CalibRecord
	Calibrate = func(r CalibRecord) { got = append(got, r) }
	t.Cleanup(func() { Calibrate = nil })

	m := calibMachine()
	ctx := &spec.Context{State: m.Initial, Fuel: m.Fuel,
		Vars: map[string]any{}, Cells: map[string]*spec.CellResult{},
		Event: map[string]any{"hook_event_name": "Stop", "session_id": "s1"}}
	model := func(spec.Cell, *spec.Context) string { return "incomplete" }

	out := SafeDispatch(m, ctx.Event, ctx, model)
	if out.Block == nil {
		t.Fatalf("expected a block on 'incomplete', got %+v", out)
	}

	var cell, step *CalibRecord
	for i := range got {
		switch got[i].Kind {
		case "cell":
			cell = &got[i]
		case "step":
			step = &got[i]
		}
	}
	if cell == nil || step == nil {
		t.Fatalf("want both a cell and a step record, got %+v", got)
	}
	// The prediction half.
	if cell.Cell != "assess" || cell.Term != "incomplete" || cell.WellFormed == nil || !*cell.WellFormed {
		t.Fatalf("cell record wrong: %+v", cell)
	}
	if cell.Session != "s1" || cell.Machine != "calib-test" {
		t.Fatalf("cell record not keyed for the join: %+v", cell)
	}
	// Both halves carry the schema version so a later reader can branch on it.
	if cell.V != SchemaVersion || step.V != SchemaVersion {
		t.Fatalf("records must stamp the schema version: cell=%+v step=%+v", cell, step)
	}
	// The outcome half — joinable to the prediction by (session, machine).
	if step.From != "loop" || step.To != "loop" || step.Outcome != "block" {
		t.Fatalf("step record wrong: %+v", step)
	}
	if step.Session != cell.Session || step.Machine != cell.Machine {
		t.Fatalf("halves not joinable: cell=%+v step=%+v", cell, step)
	}
	// Fuel is the ordinal that makes the join an integer, not a timestamp: the
	// cell ran at the pre-decrement budget, the step it fed at one less.
	if cell.Fuel != m.Fuel {
		t.Fatalf("cell record should carry the pre-decrement fuel %d, got %d", m.Fuel, cell.Fuel)
	}
	if cell.Fuel != step.Fuel+1 {
		t.Fatalf("cell(F) should feed step(F-1): cell.Fuel=%d step.Fuel=%d", cell.Fuel, step.Fuel)
	}
}

// FileCalibrator is fail-open, but a dropped write is a sampling bias, not a
// no-op — so it must be counted, not silently swallowed.
func TestFileCalibratorCountsDroppedWrites(t *testing.T) {
	before := CalibDroppedWrites()
	// A path whose parent is a regular file can't be MkdirAll'd -> the write
	// fails -> the drop must be counted.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := FileCalibrator(filepath.Join(blocker, "sub"))
	sink(CalibRecord{V: SchemaVersion, Kind: "cell"})
	if CalibDroppedWrites() != before+1 {
		t.Fatalf("dropped write not counted: before=%d after=%d", before, CalibDroppedWrites())
	}
}

// With no sink installed the tap is inert — sim and other tests must emit nothing.
func TestCalibrationTapNoopWhenUnset(t *testing.T) {
	Calibrate = nil
	m := calibMachine()
	ctx := &spec.Context{State: m.Initial, Fuel: m.Fuel,
		Vars: map[string]any{}, Cells: map[string]*spec.CellResult{},
		Event: map[string]any{"hook_event_name": "Stop", "session_id": "s1"}}
	// Must not panic and must not require a sink.
	_ = SafeDispatch(m, ctx.Event, ctx, func(spec.Cell, *spec.Context) string { return "incomplete" })
}
