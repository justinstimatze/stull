package spec_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// A formal oracle over a tiny command language: L = {"read <x>", "write <x>"};
// the safety check forbids writes. Demonstrates the two independent stages —
// membership in the language, then safety of the denoted action.
func TestFormalOracleCheck(t *testing.T) {
	cell := spec.NewCell("cmd", "m", "emit `read X` or `write X`",
		func(raw string) (string, bool) { // Grammar: membership in L
			if len(raw) > 5 && (raw[:5] == "read " || raw[:6] == "write ") {
				return raw, true
			}
			return "", false
		},
		func(term string) bool { return term[:4] == "read" }, // Safety: reads only
	)

	cases := []struct {
		name             string
		raw              string
		wellFormed, safe bool
	}{
		{"in-language, safe", "read notes.md", true, true},
		{"in-language, unsafe", "write /etc/passwd", true, false},
		{"outside the language", "rm -rf /", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := cell.Check(tc.raw)
			if r.WellFormed != tc.wellFormed || r.Safe != tc.safe {
				t.Fatalf("Check(%q) = {WellFormed:%v Safe:%v}, want {%v %v}",
					tc.raw, r.WellFormed, r.Safe, tc.wellFormed, tc.safe)
			}
		})
	}
}

// A confined formal oracle over an action language: the runtime forces the model
// to emit {action, path} JSON (generation-time confinement), Grammar parses that
// tool input, and Safety forbids writes outside /tmp. This is where the two
// stages earn their keep — the term denotes an *action*, and membership (is it a
// well-formed command?) is genuinely independent of safety (is the write
// allowed?). The Schema is what the runtime sends as a strict tool schema; the
// Check pipeline below runs over the tool input as JSON.
func TestConfinedActionOracle(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"read", "write"}},
			"path":   map[string]any{"type": "string"},
		},
		"required":             []string{"action", "path"},
		"additionalProperties": false,
	}
	cell := spec.NewConfinedCell("fs", "m", "Choose a filesystem action.", schema,
		func(raw string) (string, bool) { // Grammar: parse the tool input
			var cmd struct{ Action, Path string }
			if json.Unmarshal([]byte(raw), &cmd) != nil {
				return "", false
			}
			if (cmd.Action != "read" && cmd.Action != "write") || cmd.Path == "" {
				return "", false
			}
			return cmd.Action + " " + cmd.Path, true
		},
		func(term string) bool { // Safety: reads anywhere; writes only under /tmp
			return strings.HasPrefix(term, "read ") || strings.HasPrefix(term, "write /tmp/")
		},
	)

	if cell.Schema == nil {
		t.Fatal("confined cell must carry a Schema for the runtime to force the tool call")
	}

	cases := []struct {
		name             string
		raw              string
		wellFormed, safe bool
	}{
		{"read is safe anywhere", `{"action":"read","path":"/etc/passwd"}`, true, true},
		{"write under /tmp is safe", `{"action":"write","path":"/tmp/scratch"}`, true, true},
		{"write outside /tmp is unsafe", `{"action":"write","path":"/etc/passwd"}`, true, false},
		{"not in the language", `{"action":"chmod","path":"/x"}`, false, false},
		{"not even json", `rm -rf /`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := cell.Check(tc.raw)
			if r.WellFormed != tc.wellFormed || r.Safe != tc.safe {
				t.Fatalf("Check(%q) = {WellFormed:%v Safe:%v}, want {%v %v}",
					tc.raw, r.WellFormed, r.Safe, tc.wellFormed, tc.safe)
			}
		})
	}
}

func TestNewCellRejectsMissingStages(t *testing.T) {
	g := func(r string) (string, bool) { return r, true }
	s := func(string) bool { return true }
	for _, tc := range []struct {
		name string
		g    spec.Grammar
		s    spec.Safety
	}{
		{"nil grammar", nil, s},
		{"nil safety", g, nil},
		{"both nil", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic for an incomplete formal oracle")
				}
			}()
			_ = spec.NewCell("x", "m", "i", tc.g, tc.s)
		})
	}
}

// Confinement does not buy a way around the two mandatory stages: a schema is
// not a substitute for a safety check.
func TestNewConfinedCellStillRequiresBothStages(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic: a confined cell still needs both stages")
		}
	}()
	_ = spec.NewConfinedCell("x", "m", "i", schema, func(r string) (string, bool) { return r, true }, nil)
}
