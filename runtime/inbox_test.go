package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// The full relay chain: an Emit effect flows through Dispatch into Output.Emits,
// and WriteInbox delivers it. (No registered machine emits, so this exercises
// the chain directly rather than via the CLI.)
func TestEmitEffectReachesInbox(t *testing.T) {
	m := spec.Machine{Name: "emitter", Fuel: 2, Initial: "s", States: []spec.State{
		{Name: "s", On: []spec.Transition{
			{On: spec.Stop, To: "done", Do: []spec.Effect{
				spec.Emit{Target: "reviewer", Message: spec.S("ping")}}}},
		},
		{Name: "done", Terminal: true},
	}}
	ctx := &spec.Context{State: "s", Fuel: 2, Vars: map[string]any{},
		Cells: map[string]*spec.CellResult{},
		Event: map[string]any{"hook_event_name": "Stop", "session_id": "s1"}}

	out := SafeDispatch(m, ctx.Event, ctx, func(spec.Cell, *spec.Context) string { return "" })
	if len(out.Emits) != 1 || out.Emits[0].Target != "reviewer" || out.Emits[0].Message != "ping" {
		t.Fatalf("Emit effect did not reach Output.Emits: %+v", out.Emits)
	}

	dir := t.TempDir()
	for _, e := range out.Emits {
		if err := WriteInbox(dir, m.Name, e); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := filepath.Glob(filepath.Join(dir, "reviewer", "*.json")); len(got) != 1 {
		t.Fatalf("emitted message not delivered to inbox: %v", got)
	}
}

func TestWriteInboxRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteInbox(dir, "review-loop", Emission{Target: "reviewer", Message: "take a look"}); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "reviewer", "*.json"))
	if len(files) != 1 {
		t.Fatalf("want one message under reviewer/, got %v", files)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var msg inboxMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.From != "review-loop" || msg.Target != "reviewer" || msg.Message != "take a look" || msg.TS == "" {
		t.Fatalf("bad message: %+v", msg)
	}
	// No half-written .tmp left behind (publish is atomic).
	if tmps, _ := filepath.Glob(filepath.Join(dir, "reviewer", "*.tmp")); len(tmps) != 0 {
		t.Fatalf("leftover tmp files: %v", tmps)
	}
}

// An Emit target can never escape the inbox dir: it is collapsed to one segment,
// and bare traversal targets are rejected.
func TestWriteInboxConfinesTarget(t *testing.T) {
	dir := t.TempDir()

	if err := WriteInbox(dir, "m", Emission{Target: "..", Message: "x"}); err == nil {
		t.Fatal("expected rejection of a bare traversal target")
	}

	// "../../etc/passwd" collapses to the base segment "passwd" under dir.
	if err := WriteInbox(dir, "m", Emission{Target: "../../etc/passwd", Message: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := filepath.Glob(filepath.Join(dir, "passwd", "*.json")); len(got) != 1 {
		t.Fatalf("target not confined to dir: %v", got)
	}
	// Nothing escaped above dir.
	if escaped, _ := filepath.Glob(filepath.Join(dir, "..", "etc")); len(escaped) != 0 {
		t.Fatalf("target escaped the inbox dir: %v", escaped)
	}
}

// Two emissions in the same instant get distinct files (the sequence counter).
func TestWriteInboxNoCollision(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		if err := WriteInbox(dir, "m", Emission{Target: "t", Message: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "t", "*.json"))
	if len(files) != 50 {
		t.Fatalf("want 50 distinct messages, got %d", len(files))
	}
}
