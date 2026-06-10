package compile

import (
	"testing"

	"github.com/justinstimatze/stull/spec"
)

func tinyMachine() spec.Machine {
	return spec.Machine{
		Name: "tiny", Fuel: 1, Initial: "a",
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "b",
				Do: []spec.Effect{spec.Block{Reason: spec.S("x")}}}}},
			{Name: "b", Terminal: true},
		},
	}
}

func stopEntries(merged map[string]any) []any {
	hooks, _ := merged["hooks"].(map[string]any)
	e, _ := hooks["Stop"].([]any)
	return e
}

func TestMergeIntoEmpty(t *testing.T) {
	merged, added, err := MergeHooks(nil, tinyMachine(), "bin")
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if got := len(stopEntries(merged)); got != 1 {
		t.Fatalf("Stop entries = %d, want 1", got)
	}
}

func TestMergePreservesOtherKeysAndTriggers(t *testing.T) {
	existing := map[string]any{
		"model": "claude-opus-4-8",
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{}}},
		},
	}
	merged, added, err := MergeHooks(existing, tinyMachine(), "bin")
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if merged["model"] != "claude-opus-4-8" {
		t.Error("merge dropped an unrelated top-level key")
	}
	hooks := merged["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("merge dropped an unrelated trigger")
	}
	if _, ok := hooks["Stop"]; !ok {
		t.Error("merge did not add the machine's trigger")
	}
}

func TestMergeAppendsToSameTrigger(t *testing.T) {
	existing := map[string]any{"hooks": map[string]any{
		"Stop": []any{map[string]any{"matcher": "*", "hooks": []any{
			map[string]any{"type": "command", "command": "someone-else --x"},
		}}},
	}}
	merged, added, err := MergeHooks(existing, tinyMachine(), "bin")
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if got := len(stopEntries(merged)); got != 2 {
		t.Fatalf("Stop entries = %d, want 2 (existing kept + ours appended)", got)
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	m := tinyMachine()
	first, _, err := MergeHooks(nil, m, "bin")
	if err != nil {
		t.Fatal(err)
	}
	second, added, err := MergeHooks(first, m, "bin")
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Errorf("re-install added = %d, want 0 (idempotent)", added)
	}
	if got := len(stopEntries(second)); got != 1 {
		t.Errorf("re-install duplicated the entry: Stop entries = %d, want 1", got)
	}
}
