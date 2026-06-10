package runtime

import (
	"os"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// withStdin redirects os.Stdin to deliver content for the duration of a test.
func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	go func() { _, _ = w.WriteString(content); w.Close() }()
}

// The load-bearing property: a malformed (or absent) hook event must never
// brick the session — RunHook returns 0 with no action.
func TestRunHookFailsOpenOnBadStdin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { Calibrate = nil })
	withStdin(t, "this is not json{")

	m := spec.Machine{Name: "x", Fuel: 1, Initial: "a",
		States: []spec.State{{Name: "a", Terminal: true}}}
	if code := RunHook(m, func(spec.Cell, *spec.Context) string { return "" }); code != 0 {
		t.Fatalf("bad stdin must fail open with exit 0, got %d", code)
	}
}

// The happy path: a real event advances the machine and writes the hook
// protocol. With HOME isolated, state + calibration land in a temp dir.
func TestRunHookDispatchesAnEvent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { Calibrate = nil })
	withStdin(t, `{"hook_event_name":"Stop","session_id":"s1"}`)

	// loop --Stop--> done, injecting on the way (Stop can inject).
	loop := spec.State{Name: "loop", On: []spec.Transition{{
		On: spec.Stop, To: "done",
		Do: []spec.Effect{spec.Inject{Text: spec.S("released")}},
	}}}
	m := spec.Machine{Name: "run-test", Fuel: 1, Initial: "loop",
		States: []spec.State{loop, {Name: "done", Terminal: true}}}

	if code := RunHook(m, func(spec.Cell, *spec.Context) string { return "" }); code != 0 {
		t.Fatalf("an inject path returns exit 0, got %d", code)
	}
	// State was persisted under the isolated HOME.
	if got := LoadContext(m, map[string]any{"session_id": "s1"}).State; got != "done" {
		t.Fatalf("RunHook should have advanced + persisted state to done, got %q", got)
	}
}
