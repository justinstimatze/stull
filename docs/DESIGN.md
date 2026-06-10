# stull — design notes: the formal oracle

Background notes for `stull`. The README covers mechanics; this captures *why*
the shape is what it is.

## 1. The formal oracle

A **formal oracle** (Stuart Russell's sense): an LLM whose output is confined to
a formal language `L` that a decision procedure can check for safe actions. The
constraint is on the *output channel*, not the model — the oracle can only ever
emit in `L`, and an action is taken only if the checker certifies it.

`stull`'s `Cell` is exactly this, with the contract split into two decidable
stages (both mandatory; an incomplete oracle is inexpressible):

- **Grammar** — formal-language membership: `raw → (term, wellFormed)`. Is the
  output in `L` at all?
- **Safety** — over a well-formed term, is the denoted action safe?

A term reaches control flow only if `WellFormed && Safe`; otherwise the machine
takes its fail-safe path. Generation-time confinement (constrained decoding /
structured output, so the model *can only* emit `L`) is the runtime's job; the
Grammar is the parse-time backstop.

In the code the formal oracle is the `Cell`; the runtime calls the model through
the `Model` seam (`runtime.AnthropicModel` binds it to the Messages API, stdlib
only, fail-safe on every error). "Oracle" is kept here for the concept and its
lineage; the runtime symbol is `Model`. Generation-time confinement is real for
cells built with `spec.NewConfinedCell(..., schema, ...)`: the runtime forces a
strict tool call over the schema, so the model *can only* emit `L`, and `Grammar`
parses the tool input as the backstop. For a cell *without* generation-confinement
(`NewCell`, no schema) the model still sends plain text and `Grammar` is the sole
guarantee. Either way both stages are mandatory — the schema is a generation-time
aid, never a substitute for the membership/safety check.

Two senses of "confined" to keep straight: *every* cell is confined to a language
`L` (Grammar is mandatory — the universal Russell property, which the README calls
*fenced*); *generation-*confinement (`NewConfinedCell`) is the optional, stronger
mechanism that also pins the model at decode time. A plain `NewCell` is fully a
formal oracle; it just relies on the parse-time check rather than decode-time
constraint.

Why two stages, not one: membership and safety are independent. `read foo` and
`write /etc/passwd` can both be well-formed yet differ in safety; `rm -rf /` is
outside the language entirely. Collapsing them into one opaque validator (the
original design) hides that an action was both *parsed* and *judged safe*.

## 2. Why a total guarded statechart, not a Turing-complete mesh

A hook mesh is trivially Turing complete (arbitrary programs as nodes + a `Stop`
hook that refuses to halt = an unbounded loop), but TC brings the halting problem,
so an LLM-in-the-loop mesh that is TC cannot be formally guaranteed to behave
(Rice's theorem). `stull` targets the class one rung down: a **total**
(always-terminating) guarded statechart, where

- the control-flow skeleton is deterministic and analyzable, and
- the LLM is a fenced oracle that can never move the machine into an unverified
  state.

You deliberately give up Turing-completeness (bound the fuel) to *gain* the
guarantees: reachability, **termination** (the mandatory fuel bound — the hard
floor), and the safety property "no transition is gated on unvalidated oracle
output" are all decidable on the spec, before a turn runs. A *reachable terminal*
is checked too, but as a loud `W-HALT` warning, not a hard guarantee: fuel alone
already makes the machine total, and a standing guard legitimately has no natural
terminal. What is **not** guaranteed — and cannot be — is the correctness of an
individual cell's output. That is the irreducible stochastic part; the stull
contains it, it does not eliminate it.

## 3. Two control-theory readings of the same architecture

- **Russell — Oracle AI + assistance games (safety axis).** The cell is a
  *confined* oracle: it answers, it never acts; only deterministic code acts. The
  validator is the *deference / uncertain-about-the-objective* mechanism — the
  system never treats oracle output as authoritative, and on a failed check it
  defers (fails safe). The fuel bound is the *off-switch / corrigibility*
  guarantee: the loop always halts and yields control.
- **Reflective oracles (self-reference axis).** The bus (an agent's output
  becomes its own or a sibling's input), the calibration loop (the system
  measures its own past predictions), and the dev-time loop (a critic rewrites
  the runtime from transcripts of the runtime) all put the oracle in a position
  to reason about a system that contains it. Reflective oracles are the formal
  fix for that self-reference — but they are uncomputable idealizations whose
  consistency is *internal*. `stull` is the computable approximation where the
  fixed point is *externalized* into the deterministic verifier: the gate does,
  from outside and by force, the consistency work the idealized oracle does from
  inside by definition. The fuel bound tames unbounded self-reference by bounding
  the recursion rather than via a probabilistic fixed point.

Synthesis: `stull` is a *confined* oracle (Russell) embedded in a
*self-referential* mesh (reflective oracles), made computable by externalizing
both the fixed point and the deference into deterministic code.

## Prior art these notes lean on

- mcp-dispatch (the bus / piggyback + Stop-hook fallback), slimemold (the
  read-once contract vs data-only injection; the maintenance detectors),
  hindcast (fail-open `Guard`, calibration), Ralph Wiggum (the Stop-loop clock),
  Claude Code's own hook model / Windsurf Cascade (typed guard/effect split),
  disler multi-agent-observability (the star-topology event tap).
- Russell, *Human Compatible* (Oracle AI, assistance games, corrigibility).
- Reflective oracles (Fallenstein, Taylor, Christiano; Critch-adjacent at CHAI).
