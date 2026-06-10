package compile_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/justinstimatze/stull/compile"
	"github.com/justinstimatze/stull/spec"
)

// sound is a minimal compilable machine using the given triggers out of its
// initial state (each to a terminal, so it passes check).
func sound(name string, triggers ...spec.Trigger) spec.Machine {
	var ts []spec.Transition
	for _, t := range triggers {
		ts = append(ts, spec.Transition{On: t, To: "done"})
	}
	return spec.Machine{
		Name: name, Fuel: 2, Initial: "a",
		States: []spec.State{
			{Name: "a", On: ts},
			{Name: "done", Terminal: true},
		},
	}
}

func TestFragmentEmitsExactlyTheUsedTriggers(t *testing.T) {
	m := sound("two", spec.Stop, spec.UserPromptSubmit)
	frag, err := compile.SettingsFragment(m, "stull")
	if err != nil {
		t.Fatalf("sound machine should compile: %v", err)
	}
	hooks, ok := frag["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("fragment missing hooks block: %v", frag)
	}
	if len(hooks) != 2 || hooks["Stop"] == nil || hooks["UserPromptSubmit"] == nil {
		t.Fatalf("want exactly Stop+UserPromptSubmit hooks, got keys %v", keys(hooks))
	}
	// A trigger the machine does not use must not appear.
	if _, leaked := hooks["PreToolUse"]; leaked {
		t.Fatalf("emitted a hook for an unused trigger: %v", keys(hooks))
	}
}

func TestOnlyUsedTrigger(t *testing.T) {
	m := sound("one", spec.PreToolUse)
	frag, _ := compile.SettingsFragment(m, "stull")
	hooks := frag["hooks"].(map[string]any)
	if len(hooks) != 1 || hooks["PreToolUse"] == nil {
		t.Fatalf("want exactly one PreToolUse hook, got %v", keys(hooks))
	}
}

func TestUnsoundMachineDoesNotCompile(t *testing.T) {
	m := sound("bad", spec.Stop)
	m.Fuel = 0 // E-FUEL: no positive step budget
	if _, err := compile.SettingsFragment(m, "stull"); err == nil {
		t.Fatal("a machine that fails check must not compile to hooks")
	}
}

func TestJSONShapeAndBinaryWiring(t *testing.T) {
	m := sound("wire", spec.Stop)
	out, err := compile.SettingsJSON(m, "/usr/local/bin/stull")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("output is not valid JSON:\n%s", out)
	}
	// The emitted command must invoke the named binary on this machine.
	if !strings.Contains(out, "/usr/local/bin/stull run --machine wire") {
		t.Fatalf("command not wired to the binary+machine:\n%s", out)
	}
	if !strings.Contains(out, `"type": "command"`) {
		t.Fatalf("hook is not a command hook:\n%s", out)
	}
}

func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
