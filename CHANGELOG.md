# Changelog

All notable changes to `stull` are recorded here. The version string itself
comes from the git tag (see the Makefile); this file is the prose that tags
can't carry.

The format is loosely [Keep a Changelog](https://keepachangelog.com/); this
project has not yet cut a tagged release, so everything lives under
*Unreleased*.

## [Unreleased]

### Added
- **Standalone `Cell` — stull's second entry point.** A `spec.Cell` is usable as
  a fenced-oracle library *without* a machine or the runtime: `NewConfinedCell`,
  drive the model yourself, hand the completion to `Cell.Check`, trust only a
  `WellFormed && Safe` term. Blessed and documented as a stable surface
  (`NewConfinedCell` / `Cell.Schema` / `Cell.Check` → `CellResult`), with a
  runnable `ExampleCell_standaloneFence`. `spec` has no sibling-package imports,
  so the fence carries no runtime dependency. (basanite is the flagship consumer.)
- **Agent devex: `explain`, `install`, and a run trace.** `stull explain
  <machine> [--json]` (backed by `check.Describe`) dumps a machine's whole
  observable shape — triggers, per-transition reads/effects, cells, soundness —
  so a reader (often another agent) understands it without reading Go. `stull
  install <machine>` merges the machine's hooks into `settings.json` via the
  pure, idempotent `compile.MergeHooks` — dry-run by default, `--write` to apply
  with a `.bak` backup, `--project` for repo-local — closing the "where does the
  fragment go?" gap with a command instead of hand-edited JSON. `STULL_TRACE=1
  stull run` narrates each step to stderr through `runtime.Tracer`; a missing or
  unknown `--machine` now prints a stderr hint instead of silent exit 0. All of
  it is observability-only: a test asserts the tracer changes neither the stdout
  hook protocol nor the exit code, so the live path stays fail-open.
- **Within-machine accumulation.** `spec.SetVar{Key, Reads, Value}` writes the
  machine's persisted `Vars` — the first effect that gives a machine cross-turn
  memory — with `spec.ClearVar` for the consume half. `Context.Var` reads it
  back. The checker enforces the same `E-ORACLE` contract over `SetVar.Reads` as
  over guards, so memory cannot launder a cell's raw output into a later branch.
- **Deterministic transcript guards.** `spec.TranscriptMatches` (regexp over the
  transcript tail) and `spec.VarSet` (fire on a non-empty var), both returning a
  complete `*Guard` with `Reads` and `When` coupled, like `spec.TermIs`.
  `Context.Transcript` carries the tail-bounded transcript to those guards; the
  runtime loads it only when a machine declares the read.
- **`examples/hedgeverify`** — a single-machine rebuild of the `likely` hook
  (github.com/terpjwu1/likely). One wiring, two lenses: `Machine` detects hedging
  with a regexp (no model call), `SmartMachine` swaps in a fenced cell on the
  same substrate/gate/action via a one-argument change.
- **Tier-A prompt caching.** `buildRequest` sets a 1h-TTL `cache_control`
  breakpoint on the frozen instruction and, for confined cells, the tool schema,
  so per-session-stable blocks stay warm across hooks that fire minutes apart.
- `Output.Vars` so a `sim` trace narrates each memory write.

### Changed (footguns made inexpressible)
- **Raw oracle output is no longer reachable from control flow.** `CellResult`
  no longer retains the model's raw completion (Grammar consumes it and it is
  dropped), so a guard or `Text`/`SetVar` closure cannot launder unvalidated
  output into a decision or an injection — it won't compile. This closes the
  closure-could-cheat gap the accumulation spec had only mitigated by convention;
  with E-ORACLE rejecting a `cells.X.raw` declaration, both axes are now closed.

### Fixed (footguns made loud)
- **W-SHADOW**: the checker now warns when an unconditional transition makes a
  later same-trigger transition statically unreachable (dead code).
- **Impure-guard lint**: a guard whose `When` mutates `Vars` (silent state
  corruption) is caught at evaluation time and surfaced as a `sim` lint.
- **Undeclared-cell-read lint**: a guard whose `When` reads a cell its `Reads`
  never declares is silently always-false (the cell never runs). The context is
  instrumented and `Dispatch` flags the mismatch via `Output.Lint`; `sim` renders
  it and `stull sim` exits non-zero, so the footgun can't ship silently. The
  coupled guard builders (`TermIs`/`VarSet`/`TranscriptMatches`) remain the clean
  path; the lint backstops hand-written guards.

### Project
- Added `LICENSE` (MIT), CI, `Makefile`, and git-tag-derived version reporting
  (`stull version`).
