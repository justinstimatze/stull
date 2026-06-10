# Spec: within-machine accumulation (`SetVar`), caching, and Anthropic-feature use

Status: **built** (steps 1–4 + Tier-A caching shipped; Tier-B delta read and
`output_config.format` remain deferred — see §7). Scope chosen with the owner:
within-machine accumulation only — the cross-machine inbox/mesh is deferred as
icing. The worked demo is `examples/hedgeverify` (machines `hedge-verify` and
`hedge-verify-smart`), a single-machine rebuild of the `likely` hook
(github.com/terpjwu1/likely) that exercises every piece below.

## 1. Why

stull has almost no cross-event memory. The only state that persists between hook
fires is `State`, `Fuel`, and `Vars` (`runtime.go` `persisted`) — and **nothing
writes `Vars`**: the effect set is `Inject`/`Block`/`Run`/`Emit`, none of which
mutate it. Cells are ephemeral (re-read fresh each fire). So a machine cannot
remember "I noticed X three turns ago." Its whole memory is *which state it is in*.

Most genuinely-useful ambient hooks — including a faithful slimemold rebuild —
need to **accumulate a structure across turns** and decide from it. slimemold
maintains one evolving claim graph and injects from its topology. That is a
single-evolving-datastructure need: a `SetVar` primitive, not the message-pile
inbox.

## 2. The `SetVar` effect

```go
// SetVar writes a value into the machine's persisted Vars, the only durable
// per-session memory besides State and Fuel. Value is evaluated like Inject's
// Text — deterministic Go that may read the validated cell terms and the current
// Vars. Reads declares its dependencies so the checker can keep unvalidated
// oracle output out of memory (see §3).
type SetVar struct {
    Key   string
    Reads []string // same grammar as Guard.Reads: "cells.X.term|wellformed|safe", "vars.K"
    Value Text     // func(*Context) string — the merged/updated value
}
func (SetVar) isEffect() {}
```

- **Runtime**: one new case in `Dispatch`'s effect switch — `ctx.Vars[e.Key] =
  spec.Resolve(e.Value, ctx)`. `Vars` already round-trips through
  `SaveContext`/`LoadContext`, so accumulation is "`SetVar` each fire, building
  `Vars["graph"]`".
- **Value is a string** (not `any`): `Vars` persists as JSON and must stay
  deterministic and serializable. A graph is a serialized JSON string the guard
  parses — same discipline as a cell's `Term`.
- **Reading**: guards already receive `*Context` and can read `ctx.Vars`. Add an
  ergonomic `func (c *Context) Var(key string) string` and let `Reads` reference
  `vars.K` so the checker can see var dependencies too.

## 3. Soundness: no laundering raw oracle output through memory

The core invariant is *no unvalidated oracle output may gate a transition*
(`E-ORACLE`). A `SetVar` whose `Value` stashed `c.Cell("x").Raw`, with a later
guard branching on that var, would smuggle raw model output into a decision past
`E-ORACLE`.

**Decision (built):** give `SetVar` a `Reads` field and extend `E-ORACLE` (and
`E-CELL`) to cover `SetVar.Reads`, identically to guards. A `SetVar` that declares
a read of `cells.X.raw` fails the check; one that declares only
`term`/`wellformed`/`safe`/`vars.*` passes.

The closure-could-cheat gap this section worried about — `Guard.When`/`Value` are
opaque Go closures that *could* read raw output without declaring it — **is now
closed structurally**, not just by convention: `CellResult.raw` is unexported, so
a closure in any other package cannot reach it. The strong "sanitized view" option
this spec deferred is realized in its simplest form (hide the field, not wrap the
context). Laundering is now inexpressible on *both* axes — you cannot declare
`cells.X.raw` (E-ORACLE rejects it) and you cannot read it (unexported). The same
move closes injecting raw oracle output (a `Text` closure can't reach raw either).

## 4. The caching payoff falls out of accumulation

This is the part the owner flagged ("optimal caching"). Accumulation isn't only a
memory feature — it's the caching win.

**Today:** each fire, a cell's input is the full tail-bounded transcript (up to
`maxTranscriptBytes = 60_000`), re-sent every fire. Because the window is a moving
*tail*, there is no stable prefix to cache — `model.go` already notes this forgoes
transcript-prefix caching. Cost is O(transcript) per fire and grows.

**With accumulation:** the distilled state lives in `Vars` (the graph). The cell
no longer needs the whole transcript — it needs *the accumulated state + only what
is new since last fire*. Input becomes O(graph + delta) ≈ O(1) per fire. That is a
cost, latency, **and** cacheability win at once, and it is how slimemold actually
works (incremental graph update, not re-reading everything).

Two tiers, smallest-first:

- **Tier A — cheap, do alongside `SetVar` (no new context machinery):**
  - Bump the system-block `cache_control` to the **1h TTL** (`{"type":
    "ephemeral", "ttl": "1h"}`). Hooks fire minutes apart; the default 5-min
    ephemeral expires *between* fires, so the instruction is re-billed every time.
    1h keeps the (frozen) cell instruction warm across a session. (Caveat: 1h
    cache has different pricing; worth it for a per-session-stable block.)
  - Add `cache_control` to the **tool/schema block** for confined cells — the
    schema is identical every fire; cache it once per session.
  - These alone cut the per-fire billed-input to roughly the transcript.

- **Tier B — the real prize, medium effort:** feed the cell *only the delta*.
  - Store a watermark in the accumulated value (e.g. `graph.seen_bytes` or a turn
    count). Add a context read that supplies "transcript since watermark" instead
    of the tail — e.g. `NeedTranscriptDelta`, the runtime slicing from the
    watermark. The cell's user turn becomes `<accumulated-summary> + <new since
    last fire>`, both small.
  - Now the only freshly-billed input per fire is the small delta; the instruction
    (and optionally a stable summary prefix) are cache hits.

Recommendation: ship Tier A with `SetVar`; gate Tier B on whether the token bill
actually warrants the watermark machinery (measure first — the calibration tap can
tell us per-cell input sizes).

## 5. Latest Anthropic-feature audit (`runtime/model.go`)

Current: forced **strict tool call** for confinement; `cache_control: ephemeral`
(5-min) on system only; no schema caching; `max_tokens 1024`; no
temperature/thinking (correct for a hot-path classifier). Model is per-cell
(examples use `claude-sonnet-4-6`).

Proposed, in priority order:

1. **1h cache TTL on the instruction + cache the tool schema** (Tier A above).
   Biggest, cheapest win; pure `model.go` change.
2. **Structured output via `output_config.format`** as an alternative to the
   forced-tool-call hack. The current Messages API constrains the *response* to a
   JSON schema directly (`output_config: {format: {...}}`) rather than coercing a
   tool call — cleaner for "emit exactly this shape," and confinement is then
   first-class rather than a tool-choice trick. **Verify the exact wire schema
   against current docs at implementation time** (stdlib raw HTTP; no SDK). Keep
   the tool-call path as a fallback for models/cases that need it.
3. **Model tier per cell**: cells are cheap classifiers — `sonnet-4-6` or
   `haiku-4-5` is right; do *not* default a hot-path hook to `opus`. Already
   supported (per-cell `Model`); just document the guidance.
4. Leave thinking/effort off for classifier cells; expose per-cell later only if a
   cell genuinely needs reasoning (the audit/topology cell does not — the topology
   reasoning is the deterministic guard, not the cell).

All of this stays **stdlib-only** (raw `net/http`), preserving the no-dependencies
invariant.

## 6. slimemold-as-stull, built on this (the demo)

The whole point. Maps onto hybrid's canonical 5-role loop:

- **lens** → `cell extract` (confined): input is `Vars["graph"]` + (Tier A) the
  transcript or (Tier B) the delta; output is new claims + epistemic basis.
- **substrate** → `Vars["graph"]`: the accumulated claim graph, persisted.
- **gate** → a **deterministic guard** `hasFragility`: reads `Vars["graph"]`, folds
  in the new claims, runs topology detection (load-bearing-on-weak-basis,
  unchallenged chain, bottleneck) — *plain Go, the faithful part*; returns bool.
- **action** → `SetVar{"graph", …merged…}` to persist + `Inject{…rendered
  finding…}`. `Safety` on `extract` (or a render check) enforces
  *factual-in-register, not imperative* — the stage the stop-gate left trivial.
- standing guard (`W-HALT`), `fuel` as the audit budget, fail-open.

Timing note: the merge-and-detect runs *in the guard* (it runs the cell via
`Reads`, reads the old graph, merges transiently, decides); the `SetVar` effect
then persists the merged graph for next fire. Both are pure functions of
`(old Vars + validated cell term)`, so it is clean, just computed twice.

## 7. Build order

1. ✅ `spec.SetVar{Key, Reads, Value}` + `isEffect`; `Dispatch` effect case;
   `Context.Var` helper. Plus `spec.ClearVar`, and `Output.Vars` so a trace
   narrates each write.
2. ✅ Checker: `E-ORACLE`/`E-CELL` extended over `SetVar.Reads`
   (`check.Inspect`), with tests (`TestSetVarLaunderingRejected`,
   `TestSetVarValidatedReadAccepted`).
3. ✅ `model.go`: 1h-TTL `cache_control` on the instruction **and** the tool
   schema (`buildRequest`); `transcriptTail` extracted and shared.
4. ✅ Demo: **`examples/hedgeverify`** instead of the synthetic `reasonaudit` —
   a real shipped project (`likely`) rebuilt as one machine, with a deterministic
   transcript-guard lens (`Machine`) and a one-line cell-lens upgrade
   (`SmartMachine`). Plus the supporting **deterministic transcript-predicate
   guard** the `likely` rebuild required: `spec.TranscriptMatches` +
   `spec.VarSet`, `Context.Transcript`, runtime-side `machineReadsTranscript`,
   sim inline-transcript support. Sim scenarios cover clean→silent,
   load-bearing→nudge, genuine→silent, off-language→fail-safe.
5. *Optional, measure first (unchanged):* Tier B incremental delta read;
   `output_config.format` confinement. (medium each)
6. Docs: README example mention; this file → fold into DESIGN. *(pending)*

## 8. Decisions (settled with the owner)

- **`Vars` value type:** string-only (JSON blobs). `Vars` stays `map[string]any`
  for JSON round-trip, but `SetVar.Value` is a `Text` (string) and `Context.Var`
  returns a string, so the authoring surface is string-only by construction.
- **Tier B:** deferred — Tier A shipped; measure the bill before adding the
  watermark machinery.
- **`output_config.format`:** deferred — kept the working forced-tool-call
  confinement; structured output is a clean follow-up.
- **Laundering guard:** the `Reads`+`E-ORACLE` reuse (the careful-but-light
  option); the sanitized-context view remains a noted future option, not built.
