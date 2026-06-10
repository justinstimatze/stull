// Package registry maps machine names to the compiled-in machines the CLI can
// check, compile, simulate, and run. A real deployment registers its own
// machines here (or builds its own binary that imports this package).
package registry

import (
	"sort"

	"github.com/justinstimatze/stull/examples/denyguard"
	"github.com/justinstimatze/stull/examples/hedgeverify"
	"github.com/justinstimatze/stull/examples/reviewloop"
	"github.com/justinstimatze/stull/sim"
	"github.com/justinstimatze/stull/spec"
)

type Entry struct {
	Machine   func() spec.Machine
	Scenarios func() []sim.Scenario
}

var machines = map[string]Entry{
	"review-loop":        {Machine: reviewloop.Machine, Scenarios: reviewloop.Scenarios},
	"deny-add-all":       {Machine: denyguard.Machine, Scenarios: denyguard.Scenarios},
	"hedge-verify":       {Machine: hedgeverify.Machine, Scenarios: hedgeverify.Scenarios},
	"hedge-verify-smart": {Machine: hedgeverify.SmartMachine, Scenarios: hedgeverify.SmartScenarios},
}

func Get(name string) (Entry, bool) {
	e, ok := machines[name]
	return e, ok
}

func Names() []string {
	ns := make([]string, 0, len(machines))
	for n := range machines {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}
