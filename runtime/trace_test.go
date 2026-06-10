package runtime

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

func TestTraceLine(t *testing.T) {
	reason := "stop"
	if got := traceLine("m", "Stop", "loop", "loop", 4, 3, Output{Block: &reason}); !strings.Contains(got, "block") || !strings.Contains(got, "fuel 4->3") {
		t.Errorf("block trace wrong: %q", got)
	}
	if got := traceLine("m", "", "a", "b", 1, 1, Output{}); !strings.Contains(got, "a->b") || !strings.Contains(got, "(no hook_event_name)") {
		t.Errorf("noop/empty-event trace wrong: %q", got)
	}
	if got := traceLine("m", "Stop", "s", "s", 2, 1, Output{Vars: []string{"pending=1"}}); !strings.Contains(got, "vars:pending=1") {
		t.Errorf("setvar trace should name the var: %q", got)
	}
}

// The whole safety claim for the trace: it is observability only. Enabling the
// Tracer must NOT change the exit code or a single byte of the stdout hook
// protocol — only add a stderr breadcrumb. Drive RunHook both ways and compare.
func TestTracerCannotChangeBehavior(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // contain state/calibration writes

	m := spec.Machine{
		Name: "trace-inv", Fuel: 2, Initial: "a",
		States: []spec.State{
			{Name: "a", On: []spec.Transition{{On: spec.Stop, To: "a"}}}, // Stop fires (nil guard) with no effect: empty stdout, exit 0
		},
	}
	run := func(input string) (int, string) {
		inR, inW, _ := os.Pipe()
		outR, outW, _ := os.Pipe()
		oldIn, oldOut := os.Stdin, os.Stdout
		os.Stdin, os.Stdout = inR, outW
		go func() { _, _ = inW.WriteString(input); _ = inW.Close() }()
		code := RunHook(m, noModel)
		_ = outW.Close()
		os.Stdin, os.Stdout = oldIn, oldOut
		b, _ := io.ReadAll(outR)
		return code, string(b)
	}

	const event = `{"hook_event_name":"Stop"}`
	Tracer = nil
	code0, out0 := run(event)

	var captured []string
	Tracer = func(l string) { captured = append(captured, l) }
	code1, out1 := run(event)
	Tracer = nil

	if code0 != code1 {
		t.Errorf("tracer changed exit code: %d vs %d", code0, code1)
	}
	if out0 != out1 {
		t.Errorf("tracer changed stdout: %q vs %q", out0, out1)
	}
	if len(captured) != 1 {
		t.Fatalf("tracer should receive exactly one breadcrumb, got %d", len(captured))
	}
	if !strings.Contains(captured[0], "trace-inv") {
		t.Errorf("breadcrumb should name the machine: %q", captured[0])
	}
}
