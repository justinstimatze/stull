package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/justinstimatze/stull/spec"
)

// SchemaVersion is stamped on every record (`v`). An append-only log whose shape
// will evolve stays joinable: a later reader branches on the version instead of
// guessing which fields a given line predates.
const SchemaVersion = 1

// The calibration tap. It is a substrate, not a feedback loop: it records what
// the oracle predicted and what the machine then did, so the two can be joined
// offline. It deliberately does NOT close back onto the oracle's behavior —
// stull contains the stochastic part, it does not steer it.
//
// Two record kinds share one stream so a prediction can be joined to the machine
// outcome it fed, keyed by (session, machine) and ordered by Fuel:
//
//	kind "cell"  a formal-oracle prediction — term/wellformed/safe, knowable now.
//	kind "step"  the dispatch outcome it contributed to — a *proxy* verdict
//	             (did the loop converge, block, or hit the fuel bound). NOT
//	             correctness ground truth; that needs an external join.
//
// Honest about the gap: "wellformed && safe" is necessary, not sufficient — the
// log makes the calibration join *possible*, it does not perform it.
type CalibRecord struct {
	V       int    `json:"v"` // schema version (see SchemaVersion)
	TS      string `json:"ts"`
	Kind    string `json:"kind"` // "cell" | "step"
	Session string `json:"session"`
	Machine string `json:"machine"`

	// Fuel is the per-session step ordinal that makes the cell<->step join an
	// integer instead of timestamp archaeology. For a "cell" record it is the
	// budget available when the prediction ran (pre-decrement); for the "step"
	// it fed, the budget remaining after the step (post-decrement). They
	// interleave within a session as cell(F) -> step(F-1).
	Fuel int `json:"fuel"`

	// kind "cell"
	Cell       string `json:"cell,omitempty"`
	Model      string `json:"model,omitempty"`
	Term       string `json:"term,omitempty"`
	WellFormed *bool  `json:"wellformed,omitempty"`
	Safe       *bool  `json:"safe,omitempty"`
	// SchemaForced records a *config* fact: the cell was built with
	// NewConfinedCell, so generation is constrained by a forced strict tool call
	// (Grammar is the parse-time backstop). It is deliberately NOT a runtime
	// signal — a forced call that degrades still surfaces as wellformed=false,
	// and capturing *that* would need the Model seam to report whether the
	// tool_use block was actually found (a deeper change to the Model signature).
	SchemaForced bool `json:"schema_forced,omitempty"`

	// kind "step"
	Trigger string `json:"trigger,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Outcome string `json:"outcome,omitempty"` // block | inject | budget | noop
}

// Calibrate is the calibration sink. It is nil by default, so the tap is a no-op
// everywhere (sim, tests, and `run` without a configured log directory) and adds
// nothing to the hot path. cmd/stull installs FileCalibrator for `run`; tests
// install a capturing sink. It must never affect dispatch — any panic here is
// already caught by SafeDispatch, and FileCalibrator swallows all errors.
var Calibrate func(CalibRecord)

func emitCell(machine string, ctx *spec.Context, c spec.Cell, r *spec.CellResult) {
	if Calibrate == nil {
		return
	}
	wf, sf := r.WellFormed, r.Safe
	Calibrate(CalibRecord{
		V: SchemaVersion, TS: now(), Kind: "cell",
		Session: sessionOf(ctx), Machine: machine, Fuel: ctx.Fuel,
		Cell: c.Name, Model: c.Model, Term: r.Term,
		WellFormed: &wf, Safe: &sf, SchemaForced: c.Schema != nil,
	})
}

func emitStep(machine string, ctx *spec.Context, trigger spec.Trigger, from, to string, out Output) {
	if Calibrate == nil {
		return
	}
	Calibrate(CalibRecord{
		V: SchemaVersion, TS: now(), Kind: "step",
		Session: sessionOf(ctx), Machine: machine, Fuel: ctx.Fuel,
		Trigger: string(trigger), From: from, To: to,
		Outcome: out.Kind(),
	})
}

func sessionOf(ctx *spec.Context) string {
	if s := asString(ctx.Event["session_id"]); s != "" {
		return s
	}
	return "default"
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// calibDropped counts records FileCalibrator could not write. Drops are not
// random: they cluster under disk pressure / contention — exactly when the
// failures worth calibrating happen — so a fail-open write that loses a row is a
// sampling bias, not just a lost line. The count makes that bias visible instead
// of silent (the house rule: no silent caps).
var calibDropped atomic.Uint64

// CalibDroppedWrites reports how many calibration records FileCalibrator has
// dropped this process. Nonzero means the log under-samples stressed states.
func CalibDroppedWrites() uint64 { return calibDropped.Load() }

// FileCalibrator returns a fail-open sink that appends one JSON line per record
// to {dir}/calibration.jsonl. Every error is swallowed (a calibration write must
// never disturb a hook) but counted via CalibDroppedWrites. Records are
// local-only; nothing leaves the host.
func FileCalibrator(dir string) func(CalibRecord) {
	return func(rec CalibRecord) {
		if os.MkdirAll(dir, 0o700) != nil {
			calibDropped.Add(1)
			return
		}
		f, err := os.OpenFile(filepath.Join(dir, "calibration.jsonl"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			calibDropped.Add(1)
			return
		}
		defer f.Close()
		line, err := json.Marshal(rec)
		if err != nil {
			calibDropped.Add(1)
			return
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			calibDropped.Add(1)
		}
	}
}
