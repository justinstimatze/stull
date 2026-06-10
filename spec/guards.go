package spec

import "regexp"

// Guard helpers for oracle-free machines. A standing guard (deny a command every
// time it is attempted) needs no Cell at all — its decision is a deterministic
// read of the hook event. These build the Guard.When predicate from the event
// JSON, so an emitter is a thin mapping instead of hand-written closures.
//
// They read spec.Context.Event, which mirrors the Claude Code hook payload
// (tool_name, tool_input.command, ...). All are nil-safe and fail closed to
// "no match", so a missing or oddly-shaped event simply does not fire the guard.

// ToolIs reports whether the event's tool_name equals name — e.g.
// ToolIs("Bash"). Pair it with CommandMatches to scope a deny to one tool.
func ToolIs(name string) func(*Context) bool {
	return func(c *Context) bool { return eventString(c, "tool_name") == name }
}

// CommandMatches reports whether the event's tool_input.command matches re — the
// shape behind a "deny this command" PreToolUse guard. An absent or empty
// command never matches.
func CommandMatches(re *regexp.Regexp) func(*Context) bool {
	return func(c *Context) bool {
		cmd := eventCommand(c)
		return cmd != "" && re.MatchString(cmd)
	}
}

// And combines predicates into one that holds iff all hold (zero preds == true).
// Lets an emitter compose ToolIs("Bash") with CommandMatches(...) without nesting.
func And(preds ...func(*Context) bool) func(*Context) bool {
	return func(c *Context) bool {
		for _, p := range preds {
			if !p(c) {
				return false
			}
		}
		return true
	}
}

// TermIs is the cell-guard counterpart: it returns a complete *Guard that fires
// when cell's validated term equals want — i.e. the model answered, its answer
// was in the language (WellFormed), and it was that value. It is the single most
// common cell guard ("the LLM said 'complete'").
//
// Unlike the event helpers above it returns a whole Guard, not just a When,
// because a cell guard MUST declare its Reads: the runtime uses Guard.Reads to
// know which cells to run before evaluating the guard. Hand-writing the When
// without the matching Reads is a silent footgun — the cell never runs, the
// result is the zero value (not WellFormed), and the guard is always false.
// TermIs couples the two so that can't happen.
func TermIs(cell, want string) *Guard {
	return &Guard{
		Reads: []string{"cells." + cell + ".term"},
		When: func(c *Context) bool {
			r := c.Cell(cell)
			return r.WellFormed && r.Term == want
		},
	}
}

// TranscriptMatches is the transcript counterpart to CommandMatches: a complete
// *Guard that fires when the (tail-bounded) transcript text matches re. This is
// the deterministic lens — pattern-detection over Claude's own output with no
// model call, the shape behind a "notice when the assistant did X" hook.
//
// Like TermIs it returns a whole Guard, not just a When, because the runtime
// must know from Reads to load the transcript before evaluating the guard.
// "transcript" is an event-family read — hook input matched deterministically,
// not a cells.* read — so it carries no E-ORACLE obligation: there is no oracle
// here to launder. Swap this for TermIs over a cell to upgrade a machine's lens
// from a regexp to LLM judgment without touching its substrate, gate, or action.
func TranscriptMatches(re *regexp.Regexp) *Guard {
	return &Guard{
		Reads: []string{"transcript"},
		When:  func(c *Context) bool { return c != nil && c.Transcript != "" && re.MatchString(c.Transcript) },
	}
}

// VarSet returns a complete *Guard that fires when the persisted var key is
// non-empty — the gate half of an accumulation ("we recorded something on an
// earlier fire; act on it now"). Reads declares the var dependency so the
// dependency is visible to the checker, mirroring TermIs.
func VarSet(key string) *Guard {
	return &Guard{
		Reads: []string{"vars." + key},
		When:  func(c *Context) bool { return c.Var(key) != "" },
	}
}

func eventString(c *Context, key string) string {
	if c == nil || c.Event == nil {
		return ""
	}
	if s, ok := c.Event[key].(string); ok {
		return s
	}
	return ""
}

func eventCommand(c *Context) string {
	if c == nil || c.Event == nil {
		return ""
	}
	ti, ok := c.Event["tool_input"].(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := ti["command"].(string); ok {
		return s
	}
	return ""
}
