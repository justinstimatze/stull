package spec

import (
	"regexp"
	"testing"
)

func ctxWith(ev map[string]any) *Context { return &Context{Event: ev} }

func TestToolIs(t *testing.T) {
	is := ToolIs("Bash")
	if !is(ctxWith(map[string]any{"tool_name": "Bash"})) {
		t.Fatal("Bash should match")
	}
	if is(ctxWith(map[string]any{"tool_name": "Edit"})) {
		t.Fatal("Edit should not match Bash")
	}
	// Nil-safe: missing event / missing key never matches, never panics.
	if is(ctxWith(nil)) || is(&Context{}) {
		t.Fatal("absent tool_name must not match")
	}
}

func TestCommandMatches(t *testing.T) {
	m := CommandMatches(regexp.MustCompile(`\bgit\s+add\s+-A\b`))
	hit := ctxWith(map[string]any{"tool_input": map[string]any{"command": "git add -A"}})
	miss := ctxWith(map[string]any{"tool_input": map[string]any{"command": "git add main.go"}})
	if !m(hit) {
		t.Fatal("git add -A should match")
	}
	if m(miss) {
		t.Fatal("git add main.go should not match")
	}
	// A missing/empty command must fail closed to no-match.
	if m(ctxWith(map[string]any{"tool_input": map[string]any{"command": ""}})) {
		t.Fatal("empty command must not match")
	}
	if m(ctxWith(map[string]any{})) || m(ctxWith(nil)) {
		t.Fatal("absent command must not match")
	}
}

func TestTermIs(t *testing.T) {
	g := TermIs("assess", "complete")
	// It must declare the cell read, or the runtime won't run the cell.
	if len(g.Reads) != 1 || g.Reads[0] != "cells.assess.term" {
		t.Fatalf("TermIs must declare its cell read, got %v", g.Reads)
	}
	mk := func(r *CellResult) *Context { return &Context{Cells: map[string]*CellResult{"assess": r}} }
	if !g.When(mk(&CellResult{WellFormed: true, Term: "complete"})) {
		t.Fatal("should fire on a well-formed 'complete'")
	}
	if g.When(mk(&CellResult{WellFormed: true, Term: "incomplete"})) {
		t.Fatal("a different term must not fire")
	}
	if g.When(mk(&CellResult{WellFormed: false, Term: "complete"})) {
		t.Fatal("a not-well-formed result must not fire even if the term matches")
	}
	// The footgun TermIs prevents: a cell that never ran is the zero result, so
	// the guard must read false, not panic.
	if g.When(&Context{Cells: map[string]*CellResult{}}) {
		t.Fatal("an absent cell must not fire")
	}
}

func TestAnd(t *testing.T) {
	both := And(ToolIs("Bash"), CommandMatches(regexp.MustCompile(`rm -rf`)))
	if !both(ctxWith(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /"}})) {
		t.Fatal("Bash + rm -rf should hold")
	}
	if both(ctxWith(map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"command": "rm -rf /"}})) {
		t.Fatal("wrong tool must fail the conjunction")
	}
	// Zero predicates is vacuously true.
	if !And()(ctxWith(nil)) {
		t.Fatal("And() with no predicates should be true")
	}
}
