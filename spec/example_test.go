package spec_test

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/justinstimatze/stull/check"
	"github.com/justinstimatze/stull/spec"
)

// A complete machine is a few States with Transitions that fire on a hook
// Trigger, are selected by a deterministic Guard, and run Effects. This one is a
// standing guard with no LLM at all: block `rm -rf` every time it is attempted.
// A standing guard has no terminal state, so it is *sound* but carries the loud
// W-HALT warning — fuel alone guarantees it halts.
func ExampleMachine() {
	rmrf := regexp.MustCompile(`(?i)rm\s+-rf`)
	m := spec.Machine{
		Name:    "deny-rm-rf",
		Fuel:    100, // step budget: the bound that makes the machine total
		Initial: "watch",
		States: []spec.State{{
			Name: "watch",
			On: []spec.Transition{{
				On: spec.PreToolUse, To: "watch",
				Guard: &spec.Guard{
					Reads: []string{"event.tool_input.command"},
					When:  spec.CommandMatches(rmrf),
				},
				Do: []spec.Effect{spec.Block{Reason: spec.S("refused: rm -rf")}},
			}},
		}},
	}

	rep := check.Inspect(m)
	fmt.Println("sound:", rep.Sound())
	fmt.Println("warnings:", len(rep.Warnings))
	// Output:
	// sound: true
	// warnings: 1
}

// TermIs is the most common cell guard: fire when the oracle's *validated* term
// equals a value. It returns a whole Guard so its Reads and When stay coupled —
// the runtime uses Reads to know which cell to run before the guard evaluates.
func ExampleTermIs() {
	g := spec.TermIs("assess", "complete")

	// What the runtime hands the guard after the cell ran and passed both stages.
	ctx := &spec.Context{Cells: map[string]*spec.CellResult{
		"assess": {Term: "complete", WellFormed: true, Safe: true, Ran: true},
	}}

	fmt.Println("reads:", g.Reads)
	fmt.Println("fires:", g.When(ctx))
	// Output:
	// reads: [cells.assess.term]
	// fires: true
}

// A Cell is useful on its own — you do not need a machine to get the fence. This
// is stull's second entry point: drive the model yourself, hand the completion to
// Cell.Check, and trust only a WellFormed && Safe term. The Schema confines
// generation to a vetted set; Grammar + Safety are the parse-time backstop, so an
// answer the model invented outside that set is WellFormed-but-not-Safe and fails
// safe instead of being acted on. (This is exactly how basanite uses spec.Cell:
// a deterministic step manufactures a vetted ladder, the cell fences an LLM to
// SELECT from it, never invent.)
func ExampleCell_standaloneFence() {
	// A deterministic step produced this word's vetted ladder (the only rungs an
	// LLM may demote it to), plus "none".
	ladder := map[string]bool{"base": true, "foundation": true, "none": true}

	cell := spec.NewConfinedCell(
		"demote", "claude-sonnet-4-6",
		`Pick the rung this word should be demoted to, or "none".`,
		map[string]any{ // Schema: confines generation to the vetted ladder
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"demote_to": map[string]any{"type": "string", "enum": []any{"base", "foundation", "none"}},
			},
			"required": []any{"demote_to"},
		},
		func(raw string) (string, bool) { // Grammar: parse the tool JSON into a term
			var v struct {
				DemoteTo string `json:"demote_to"`
			}
			if json.Unmarshal([]byte(raw), &v) != nil || v.DemoteTo == "" {
				return "", false
			}
			return v.DemoteTo, true
		},
		func(term string) bool { return ladder[term] }, // Safety: only a vetted rung may pass
	)

	// You drive the model yourself; hand each completion to Check.
	good := cell.Check(`{"demote_to":"base"}`)        // selected a real rung
	invented := cell.Check(`{"demote_to":"bedrock"}`) // invented one off the ladder

	fmt.Printf("good:     wellformed=%v safe=%v term=%q\n", good.WellFormed, good.Safe, good.Term)
	fmt.Printf("invented: wellformed=%v safe=%v -> fail safe\n", invented.WellFormed, invented.Safe)
	// Output:
	// good:     wellformed=true safe=true term="base"
	// invented: wellformed=true safe=false -> fail safe
}
