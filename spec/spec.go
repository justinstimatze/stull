// Package spec is the statechart language that compiles to a Claude Code hook
// mesh. A Machine is a finite set of States; Transitions fire on hook Triggers,
// are selected by deterministic Guards, and run Effects.
//
// The one stochastic element — an LLM Cell — is fenced two ways:
//
//   - A Cell cannot be built without a validator (NewCell rejects a nil one, and
//     the validator field is unexported, so a Cell literal without one will not
//     compile in another package). An ungated oracle is therefore inexpressible.
//   - A Guard may only read a cell's *validated* output (see package check),
//     never its raw output, so an unvalidated oracle result can never gate
//     control flow.
//
// Nothing here calls a model or touches the filesystem; it is pure data plus
// deterministic predicates.
package spec

import "sort"

// Trigger is a hook event a transition can fire on (the reliable subset).
type Trigger string

const (
	SessionStart     Trigger = "SessionStart"
	UserPromptSubmit Trigger = "UserPromptSubmit"
	PreToolUse       Trigger = "PreToolUse"
	PostToolUse      Trigger = "PostToolUse"
	Stop             Trigger = "Stop"
	SubagentStop     Trigger = "SubagentStop"
)

// injecting: triggers whose hook output can carry text back into Claude's
// context. gating: triggers whose hook can halt/redirect control flow (exit 2).
// These mirror real Claude Code mechanics, so an Inject on PreToolUse — which
// cannot inject — is a compile-time check failure, not a runtime surprise.
var injecting = map[Trigger]bool{
	SessionStart: true, UserPromptSubmit: true, PostToolUse: true,
	Stop: true, SubagentStop: true,
}
var gating = map[Trigger]bool{PreToolUse: true, UserPromptSubmit: true, Stop: true}

func Injecting(t Trigger) bool { return injecting[t] }
func Gating(t Trigger) bool    { return gating[t] }

// A Cell is a formal oracle: an LLM whose output is confined to a formal
// language and checked for safe actions before it can influence anything.
// Its contract has two decidable stages, both mandatory:
//
//	Grammar  formal-language membership — parse raw output into a well-formed
//	         term, reporting whether it is in the language L at all.
//	Safety   over a well-formed term, decide whether the action it denotes is safe.
//
// A term reaches control flow only if it is WellFormed AND Safe. Generation-time
// confinement (constrained decoding / structured output so the model *can only*
// emit L) is the runtime's job; Grammar is the parse-time backstop.
type Grammar func(raw string) (term string, wellFormed bool)

// Safety decides whether a well-formed term denotes a safe action.
type Safety func(term string) bool

// Cell is a single formal-oracle call. Grammar and Safety are mandatory and
// unexported: a Cell whose output could reach a guard unchecked is inexpressible.
//
// Schema is optional and additive: when non-nil it is a JSON Schema the runtime
// uses for generation-time confinement (a forced, strict tool call), so the
// model *can only* emit a value in the language L. It does not replace Grammar —
// Grammar still parses the tool input as the parse-time backstop, and Safety
// still judges the term. A nil Schema means the cell is unconfined (plain text);
// either way both stages run.
type Cell struct {
	Name         string
	Model        string
	Instructions string
	Schema       map[string]any // nil = unconfined (plain text); non-nil = forced tool-call schema
	Context      []ContextNeed  // hook context the runtime injects; nil = instruction only
	grammar      Grammar
	safe         Safety
}

// NewCell builds a formal-oracle Cell with parse-time confinement only: its
// Grammar confines the output to a formal language L (that is the universal
// "fenced" property — every Cell has it), but the model is not constrained at
// generation time. For the stronger generation-time confinement, see
// NewConfinedCell. It panics if either stage is missing: there is no valid Cell
// that emits unconstrained or unsafety-checked output.
func NewCell(name, model, instructions string, g Grammar, s Safety) Cell {
	if g == nil || s == nil {
		panic("stull/spec: a formal-oracle Cell requires both a grammar (formal language) and a safety check")
	}
	return Cell{Name: name, Model: model, Instructions: instructions, grammar: g, safe: s}
}

// ContextNeed names a piece of hook context a cell's instruction depends on.
// Declaring it is the fix for a silent mismatch we hit live: a cell whose
// instruction says "read the transcript" gets no transcript unless it asks for
// one, so it hedges and always fails safe. spec declares WHAT context a cell
// needs; the runtime supplies it (reading files, bounding size).
type ContextNeed string

const (
	NeedEvent      ContextNeed = "event"      // the hook event metadata (JSON)
	NeedTranscript ContextNeed = "transcript" // contents of transcript_path, bounded
	NeedPrompt     ContextNeed = "prompt"     // the submitted user prompt
)

// Reading declares the hook context this cell's instruction depends on, e.g.
// spec.NewCell(...).Reading(spec.NeedTranscript). Returns the cell for chaining.
func (c Cell) Reading(needs ...ContextNeed) Cell {
	c.Context = needs
	return c
}

// NewConfinedCell builds a Cell whose output is confined at generation time: the
// runtime forces the model to emit JSON matching schema, so it *cannot* produce
// output outside the language. Both stages remain mandatory — confinement is
// generation-time, Grammar is still the parse-time backstop over the tool input,
// and Safety still judges the term. schema must be within the strict-tool-use
// supported subset (objects with `additionalProperties: false`; no min/max,
// minLength, recursion). The raw handed to Grammar is the tool input as JSON.
//
// Keep schema and Grammar in agreement: the schema constrains generation, Grammar
// parses what comes back. If they diverge — the schema permits a shape Grammar
// rejects — the cell silently fails safe (not WellFormed) rather than erroring,
// so a mismatch reads as "the oracle keeps declining." Derive one from the other
// where you can, and exercise the cell in sim so a divergence shows up there.
func NewConfinedCell(name, model, instructions string, schema map[string]any, g Grammar, s Safety) Cell {
	c := NewCell(name, model, instructions, g, s) // keeps the both-stages-mandatory panic
	c.Schema = schema
	return c
}

// Check runs the formal pipeline over a raw completion: membership, then safety.
func (c Cell) Check(raw string) CellResult {
	term, ok := c.grammar(raw)
	if !ok {
		return CellResult{Ran: true} // not in the language; raw is consumed, not retained
	}
	return CellResult{Term: term, WellFormed: true, Safe: c.safe(term), Ran: true}
}

// Text is a string field that may depend on the live context. Use S for a
// constant.
type Text func(*Context) string

// S lifts a constant string into a Text.
func S(s string) Text { return func(*Context) string { return s } }

// Resolve evaluates a Text against a context (nil Text -> empty string).
func Resolve(t Text, c *Context) string {
	if t == nil {
		return ""
	}
	return t(c)
}

// Effect is the action side of a transition.
type Effect interface{ isEffect() }

// Inject surfaces text into Claude's context (additionalContext).
type Inject struct{ Text Text }

// Block halts the triggering action and feeds Reason back to Claude (exit 2).
// On a Stop trigger this is the "keep going" primitive — it refuses the stop.
type Block struct{ Reason Text }

// Run runs a cell for its side effect (result cached on the context).
type Run struct{ Cell Cell }

// Emit writes a message onto the bus (an inbox file) for another agent.
type Emit struct {
	Target  string
	Message Text
}

// SetVar writes a value into the machine's persisted Vars — the only durable
// per-session memory besides State and Fuel — so a machine can accumulate a
// structure across hook fires and decide from it. Value is evaluated like
// Inject's Text: deterministic Go that may read the validated cell terms and the
// current Vars (via the context), returning the merged/updated value.
//
// Reads declares its dependencies in the same grammar as Guard.Reads, and the
// checker enforces the same E-ORACLE contract over it: a SetVar may accumulate a
// cell's validated output (cells.X.{term,wellformed,safe}) but never its raw
// completion, so memory cannot launder unvalidated oracle output into a later
// guard. "vars.K" and "transcript" reads are fine — they are not oracle output.
//
// Timing: a SetVar's write lands in Vars for the NEXT hook event, not the
// current one. Each event fires at most one transition, and its effects run
// after that transition's guard has already been evaluated — so no guard ever
// observes a write made during the same dispatch. Accumulation is therefore
// always read-the-old-value, then write-the-new; design for that, not for
// within-event read-after-write.
type SetVar struct {
	Key   string
	Reads []string // "cells.X.{term,wellformed,safe}", "vars.K", "transcript" — never cells.X.raw
	Value Text
}

func (Inject) isEffect() {}
func (Block) isEffect()  {}
func (Run) isEffect()    {}
func (Emit) isEffect()   {}
func (SetVar) isEffect() {}

// ClearVar returns a SetVar that empties key — the consume half of a
// set-on-detect / clear-on-consume accumulation. It reads nothing.
func ClearVar(key string) SetVar { return SetVar{Key: key, Value: S("")} }

// Guard is a deterministic transition predicate. Reads declares the context
// paths it depends on so the checker can prove it never gates on unvalidated
// oracle output. Paths look like "cells.<name>.{term,wellformed,safe}",
// "vars.<key>", or "transcript".
//
// When must be a PURE predicate: read the context, return a bool, no side
// effects. It must not mutate Vars or any other context state — write state with
// a SetVar effect instead. (A guard that mutates Vars during evaluation is caught
// by the runtime and surfaced as a sim lint.) A When that panics is recovered by
// SafeDispatch and the whole dispatch fails open to a no-op, so a guard can never
// brick a session — but it also never fires, so keep it total.
//
// Prefer the coupled builders (TermIs, VarSet, TranscriptMatches, …) over a raw
// literal: they declare Reads for you, so the guard can't read a cell it forgot
// to declare (which would never run, making the guard silently always-false —
// the runtime lints this too, but the builders make it unrepresentable).
type Guard struct {
	Reads []string
	When  func(*Context) bool
}

// Transition is one edge. The runtime evaluates a state's transitions in
// DECLARATION ORDER and fires the first whose trigger matches and whose guard
// holds (or is nil); the rest are not considered. Order them specific-first,
// catch-all last — an earlier unconditional (nil-guard) transition shadows every
// later one on the same trigger, which the checker flags as W-SHADOW.
type Transition struct {
	On    Trigger
	To    string
	Guard *Guard // nil == unconditional (always holds; place last for its trigger)
	Do    []Effect
}

type State struct {
	Name     string
	On       []Transition
	Terminal bool
}

type Machine struct {
	Name     string
	Fuel     int    // step budget — the bound that makes the machine total
	Contract string // read-once legitimacy text (the slimemold plane)
	Initial  string
	States   []State
	Cells    []Cell // registry every guard.Reads must resolve against
}

// --- runtime context (carried across steps, persisted between hook fires) ---

// CellResult is the outcome of a formal-oracle Check: exactly the *validated*
// surface a guard or effect may read. A term influences control flow only when
// WellFormed && Safe; otherwise the machine takes its fail-safe.
//
// The model's raw completion is deliberately NOT retained here. Grammar consumes
// it as a parameter and it is dropped, so there is nothing for a guard or a Text
// closure to launder — laundering unvalidated oracle output into a decision or an
// injection is structurally inexpressible, not merely rejected at declaration
// time (the E-ORACLE check also rejects a "cells.X.raw" Reads, closing both
// axes). If a future consumer (e.g. richer calibration) needs the raw text, add
// it back through a deliberate channel, not a guard-readable field.
type CellResult struct {
	Term       string // the parsed, well-formed term (empty if not in the language)
	WellFormed bool   // the raw output was a member of the formal language
	Safe       bool   // the term denotes a safe action
	Ran        bool
}

type Context struct {
	Event      map[string]any
	State      string
	Fuel       int
	Vars       map[string]any
	Cells      map[string]*CellResult
	Transcript string // tail-bounded transcript text, supplied when a guard or effect declares a "transcript" read

	touched map[string]bool // lint instrumentation: cells read since the last ClearTouched
}

// Cell returns the cached result for a cell, or a zero result if it has not run.
// Reading a cell records the access (ClearTouched/TouchedCells) so the runtime
// can catch a guard that reads a cell its Reads never declared — that cell never
// runs, so the guard would be silently always-false.
func (c *Context) Cell(name string) *CellResult {
	if c.touched == nil {
		c.touched = map[string]bool{}
	}
	c.touched[name] = true
	if r, ok := c.Cells[name]; ok {
		return r
	}
	return &CellResult{}
}

// ClearTouched resets the cell-access record. The runtime calls it before each
// guard so TouchedCells reflects exactly that guard's reads.
func (c *Context) ClearTouched() { c.touched = nil }

// TouchedCells lists the cells read since the last ClearTouched, sorted.
func (c *Context) TouchedCells() []string {
	out := make([]string, 0, len(c.touched))
	for n := range c.touched {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Var returns a persisted var as a string ("" if unset or non-string). Vars is
// the only cross-fire memory a SetVar writes; guards and Text closures read it
// here. String-only by construction: Vars round-trips through JSON, so an
// accumulated structure is a serialized blob the next fire parses.
func (c *Context) Var(key string) string {
	if c == nil || c.Vars == nil {
		return ""
	}
	if s, ok := c.Vars[key].(string); ok {
		return s
	}
	return ""
}
