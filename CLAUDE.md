# stull — context for Claude

`stull` is a guarded-statechart DSL that compiles to a Claude Code hook mesh.
You declare a machine (states, transitions, deterministic guards, fenced LLM
"cells"); a static checker proves it sound; a compiler emits the `settings.json`
hooks; one generic runtime dispatcher interprets it, fail-open and total.

## Start here

1. `README.md` — mechanics, usage, the invariants table.
2. `docs/DESIGN.md` — the rationale (formal oracle, why total-not-Turing-complete,
   Russell / reflective-oracle readings).

## State

- Self-contained Go module: `github.com/justinstimatze/stull`, Go 1.24, **stdlib
  only** (no third-party deps — keep it that way unless there's a strong reason).
- `go test ./...` and `go vet ./...` are green; keep them green.
- This is the canonical standalone repo. It was extracted from the
  `claude/hooks-message-bus-Z1Dw0` branch of a docs repo (`justinstimatze/hybrid`),
  which retains the pre-rename history as a recovery branch only.

## Status — no open build

`stull` is a working, live-verified module. The model call is bound
(`runtime.AnthropicModel`, stdlib only, fail-safe), and confined cells
(`spec.NewConfinedCell`) get
generation-time confinement via a forced strict tool call (see *Known stubs* for
the remaining edges). `stull` stays stdlib-only — no third-party dependencies.

## Invariants — do not weaken these

The whole point is that unsafe meshes are *inexpressible*. When changing code:

- A `Cell` is a **formal oracle**: it must keep both stages (`Grammar` =
  language membership, `Safety` = safe action). `NewCell` rejects either being
  nil. Never let an LLM output reach a guard unchecked.
- **Guards may only read `cells.X.{term,wellformed,safe}`, never `.raw`.** The
  checker enforces this (`E-ORACLE`); don't add an escape hatch.
- **Keep machines total:** every machine needs a positive `Fuel` bound (`E-FUEL`,
  the one hard floor) — the runtime must stop blocking once fuel hits 0. A
  reachable terminal is *not* required: a machine without one (a standing guard)
  compiles with a loud `W-HALT` warning, because fuel alone already guarantees
  totality. Loud-not-forbidden is deliberate: the real invariant is that a risky
  shape can never pass *silently*, not that it's rejected.
- **Hooks fail open:** any error/panic in the dispatcher yields exit 0 with no
  output (`runtime.SafeDispatch`). A broken hook must never brick a session.
- `Inject` only on triggers that can inject; `Block` only on triggers that can
  block (the checker enforces `E-INJECT` / `E-BLOCK`).

## Known stubs / gaps (the TODO surface)

- **Model call + confinement + context-passing are bound** (live-verified vs the
  real Messages API). `runtime.AnthropicModel` (stdlib only, fail-safe → `""`)
  sends the cell instruction as the **system** block (with a `cache_control`
  breakpoint that activates once the system prompt clears the model's min
  cacheable size) and the cell's declared hook context as the **user** turn.
  Cells declare needs with `.Reading(spec.NeedTranscript|NeedEvent|NeedPrompt)`;
  the runtime supplies them (transcript read from `transcript_path`, tail-bounded
  by `maxTranscriptBytes`). This closes a gap the live smoke exposed: a cell that
  says "read the transcript" used to get none and always fail-safe. Confinement:
  `spec.NewConfinedCell(..., schema, ...)` forces a strict tool call so the model
  *can only* emit `L`; `Grammar` is the parse-time backstop. Honest edges:
  unconfined cells rely on `Grammar` alone; `strict` schemas limited to the
  supported subset; tail-bounding forgoes transcript prefix caching; thinking is
  off (could be per-cell). Both stages stay mandatory — `NewConfinedCell` panics
  on a nil grammar/safety just like `NewCell`.
- **`Emit` delivers to a local inbox, not a live bus.** `runtime.WriteInbox`
  (called from `cmd/stull run`, fail-open, `$STULL_INBOX_DIR` or
  `~/.cache/stull/inbox`) writes each emission as an atomic JSON file under
  `{dir}/{target}/`, with `target` collapsed to one path segment. It's a
  stull-defined local artifact — *not* mcp-dispatch delivery. Forwarding onto a
  real bus is a separate adapter, deferred until that on-disk contract is
  verified (don't conflate the directory-of-files with the transport).
- **Calibration tap is a substrate, not a closed loop.** `runtime.Calibrate`
  (nil by default → no-op in sim/tests; `cmd/stull run` installs `FileCalibrator`,
  fail-open, local JSONL under `~/.cache/stull/<machine>/`) logs two joinable
  halves per session+machine, ordinal'd by `fuel` (cell `F` feeds step `F-1`):
  per-cell *prediction* (`term`/`wellformed`/`safe`/`schema_forced`) and per-step
  *outcome* (block/inject/fuel-halt — a **proxy** verdict, not correctness). Every
  record carries `v` (`SchemaVersion`) so the append-only log stays joinable as
  the shape evolves. `schema_forced` is a *config* fact (the cell was built with
  `NewConfinedCell`), deliberately **not** a runtime confinement-success signal —
  capturing the latter would need the Model seam to report tool-block success.
  Dropped writes are counted (`runtime.CalibDroppedWrites`), not silently
  swallowed, since a fail-open drop biases the sample toward stressed states. It
  makes the join *possible*; it deliberately does not close back onto the oracle
  (stull contains the stochastic part, doesn't steer it). Remaining: the
  correctness verdict needs an external join, and there's no analyzer over the
  log yet — both are out of stull's scope by design. The richer per-record
  `input_hash` calque suggested is the next add when this struct is next touched.
- **`Safety` is trivial in the example** (a classification is always safe). The
  stage earns its keep when a cell's term denotes an *action*; see
  `spec/spec_test.go`'s read/write command-language oracle for the real shape.
- **`run` reads/writes per-session state under `~/.cache/stull/`** — fine for a
  single host; revisit if you ever want cross-host runs.

## Verify

`make check` is the whole green bar — the exact gate CI enforces (gofmt, vet,
test). Run it after any change; don't assert "tests pass" from reading the
diff. `stull sim` exits non-zero on a lint, so a footgun the framework flags
fails the build, not just the eye.

```bash
make check                              # gofmt + vet + test — the CI bar, verbatim
go run ./cmd/stull sim review-loop      # converges / fail-safe / total
```

## Adding a machine (the whole recipe)

The authoring loop is *write → register → explain/check → sim → install*. There
is no fork of this repo; a real deployment registers its own machines (or builds
its own binary that imports `runtime.RunHook`).

1. **Write** `examples/<name>/<name>.go` defining `func Machine() spec.Machine`
   (and a `func Scenarios() []sim.Scenario` so `sim` can exercise it). Copy
   `examples/denyguard` (oracle-free) or `examples/reviewloop` (cell-gated) as
   the skeleton. The registry key MUST equal the machine's own `.Name` — a test
   (`registry.TestRegistryKeyMatchesMachineName`) enforces it.
2. **Register** it in `registry/registry.go`: add one line to the `machines`
   map — `"<name>": {Machine: <pkg>.Machine, Scenarios: <pkg>.Scenarios}`.
3. **Inspect** it: `go run ./cmd/stull explain <name>` (shape + soundness; add
   `--json` for the machine-readable form) and `stull check <name>`.
4. **Simulate**: `go run ./cmd/stull sim <name>` — must converge / fail-safe /
   stay total, and be lint-clean (a lint exits non-zero).
5. **Install**: `stull install <name>` merges the hooks into `settings.json`
   (dry-run by default; `--write` to apply; `--project` for `./.claude/`).

When debugging a live hook by hand, `STULL_TRACE=1 stull run --machine <name>`
narrates each step to stderr (it cannot change the stdout protocol or exit code).

<!-- defn:begin -->
## Code Navigation and Editing

**The database is authoritative. Files are an I/O projection.** This project
is indexed in defn. For **Go code**, use the `code` MCP tool — **not**
Read, Edit, Write, or Grep. Reserve those built-in tools for non-Go files
(YAML, JSON, Markdown, shell, `go.mod`).

```
code(op: "read", name: "handleEdit")           -- full source by name
code(op: "read", name: "server.go:272")        -- or by file:line
code(op: "impact", name: "Render")             -- blast radius + test coverage
code(op: "edit", name: "Foo", new_body: "...") -- edit, auto-emit + build
code(op: "search", pattern: "%Auth%")          -- name pattern (% wildcard)
code(op: "search", pattern: "authentication")  -- body text search
code(op: "test", name: "Render")               -- run affected tests only
```

All ops: read, search, impact, explain, untested, edit, create, delete, rename, move, test, apply, diff, history, find, sync, query, overview, patch.

### Why defn for Go, not Read/Edit/Grep

- `code(op:"read")` returns a whole definition by name — no line-number guessing, no reading a file to find one function.
- `code(op:"edit")` updates one definition, emits the file, and rebuilds the reference graph in one call. A raw file Edit leaves defn's graph stale until a `sync`.
- `code(op:"rename")` / `move` update every reference and import site across the repo in one call — many fragile Edits otherwise.
- `code(op:"impact")` gives callers + transitive blast radius + test coverage before you touch anything.

If you do edit a `.go` file with a built-in tool, call `code(op:"sync", file:"path")` afterward so the graph stays correct.

**Rule of thumb:** run `impact` before modifying an existing definition; skip it for brand-new ones.
<!-- defn:end -->
