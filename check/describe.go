package check

import (
	"sort"

	"github.com/justinstimatze/stull/spec"
)

// Description is a machine's whole observable shape — structure plus soundness —
// in one JSON-able value. It is what `stull explain --json` emits so a reader
// (often another agent) can understand a machine without reading its Go source:
// which triggers it binds, what each transition reads and does, which cells it
// calls, and whether it is sound.
type Description struct {
	Name     string      `json:"name"`
	Fuel     int         `json:"fuel"`
	Initial  string      `json:"initial"`
	Sound    bool        `json:"sound"`
	Errors   []string    `json:"errors,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
	Triggers []string    `json:"triggers"`
	Cells    []CellInfo  `json:"cells,omitempty"`
	States   []StateInfo `json:"states"`
}

type CellInfo struct {
	Name     string   `json:"name"`
	Model    string   `json:"model"`
	Confined bool     `json:"confined"` // built with NewConfinedCell (generation-time confinement)
	Needs    []string `json:"needs,omitempty"`
}

type StateInfo struct {
	Name        string           `json:"name"`
	Terminal    bool             `json:"terminal,omitempty"`
	Transitions []TransitionInfo `json:"transitions,omitempty"`
}

type TransitionInfo struct {
	On            string   `json:"on"`
	To            string   `json:"to"`
	GuardReads    []string `json:"guard_reads,omitempty"`
	Unconditional bool     `json:"unconditional,omitempty"`
	Effects       []string `json:"effects,omitempty"`
}

// Describe builds a Description for m. It is pure and never panics on a
// malformed machine — it reports whatever structure is present and folds the
// soundness verdict in from Inspect.
func Describe(m spec.Machine) Description {
	rep := Inspect(m)
	d := Description{
		Name: m.Name, Fuel: m.Fuel, Initial: m.Initial,
		Sound: rep.Sound(), Errors: rep.Errors, Warnings: rep.Warnings,
	}

	used := map[string]bool{}
	for _, s := range m.States {
		si := StateInfo{Name: s.Name, Terminal: s.Terminal}
		for _, t := range s.On {
			used[string(t.On)] = true
			ti := TransitionInfo{On: string(t.On), To: t.To}
			if t.Guard == nil {
				ti.Unconditional = true
			} else {
				ti.GuardReads = t.Guard.Reads
			}
			for _, eff := range t.Do {
				ti.Effects = append(ti.Effects, effectName(eff))
			}
			si.Transitions = append(si.Transitions, ti)
		}
		d.States = append(d.States, si)
	}
	for t := range used {
		d.Triggers = append(d.Triggers, t)
	}
	sort.Strings(d.Triggers)

	for _, c := range m.Cells {
		ci := CellInfo{Name: c.Name, Model: c.Model, Confined: c.Schema != nil}
		for _, n := range c.Context {
			ci.Needs = append(ci.Needs, string(n))
		}
		d.Cells = append(d.Cells, ci)
	}
	return d
}

// effectName is the stable string label for an effect kind, used in the
// description and the run trace.
func effectName(e spec.Effect) string {
	switch ev := e.(type) {
	case spec.Inject:
		return "inject"
	case spec.Block:
		return "block"
	case spec.Run:
		return "run:" + ev.Cell.Name
	case spec.Emit:
		return "emit:" + ev.Target
	case spec.SetVar:
		// ClearVar is a SetVar with an empty value, indistinguishable here from a
		// write of "" — report both as setvar:<key>, honestly.
		return "setvar:" + ev.Key
	default:
		return "unknown"
	}
}
