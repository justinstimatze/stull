package registry

import (
	"os"
	"regexp"
	"testing"

	"github.com/justinstimatze/stull/check"
	"github.com/justinstimatze/stull/sim"
)

// The CLI resolves a machine by its registry key, but the runtime persists and
// calibrates under the machine's own Name (statePath uses m.Name). If the two
// drift, `stull run --machine <key>` loads a machine that reads and writes its
// session state under a different identity — a silent footgun. Lock them equal.
func TestRegistryKeyMatchesMachineName(t *testing.T) {
	for _, name := range Names() {
		e, _ := Get(name)
		if got := e.Machine().Name; got != name {
			t.Errorf("registry key %q != machine Name %q — the CLI name and the persisted identity must match", name, got)
		}
	}
}

// Every machine the CLI can run must be statically sound (no hard errors).
// Warnings (e.g. W-HALT for a standing guard) are allowed by design.
func TestEveryRegisteredMachineIsSound(t *testing.T) {
	for _, name := range Names() {
		e, _ := Get(name)
		if rep := check.Inspect(e.Machine()); !rep.Sound() {
			t.Errorf("registered machine %q is unsound: %v", name, rep.Errors)
		}
	}
}

// Every registered machine's demo scenarios must run to completion and be
// lint-clean — a registered example is also documentation, so it cannot ship a
// footgun the framework itself flags (undeclared cell read, impure guard, …).
func TestEveryRegisteredScenarioRunsCleanly(t *testing.T) {
	for _, name := range Names() {
		e, _ := Get(name)
		m := e.Machine()
		for _, sc := range e.Scenarios() {
			steps, _ := sim.Run(m, sc)
			if len(steps) == 0 {
				t.Errorf("%s/%q produced no steps", name, sc.Name)
			}
			if lint := sim.Lint(steps); len(lint) != 0 {
				t.Errorf("%s/%q is not lint-clean: %v", name, sc.Name, lint)
			}
		}
	}
}

// Documentation drift guard: every machine name the README invokes in a
// `stull <verb> <name>` command must resolve in the registry. This is the
// mechanism behind the prose fix — it directly catches the README referencing a
// machine name the CLI does not have.
func TestREADMECommandsReferenceRealMachines(t *testing.T) {
	data, err := os.ReadFile("../README.md")
	if err != nil {
		t.Skipf("README not readable from test cwd: %v", err)
	}
	cmd := regexp.MustCompile(`stull\s+(?:check|compile|sim|explain|install|run --machine)\s+([a-z][a-z0-9-]*)`)
	for _, mt := range cmd.FindAllStringSubmatch(string(data), -1) {
		name := mt[1]
		if _, ok := Get(name); !ok {
			t.Errorf("README invokes `stull ... %s` but no such machine is registered (have %v)", name, Names())
		}
	}
}
