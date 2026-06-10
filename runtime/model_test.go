package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/stull/spec"
)

// AnthropicModel must fail safe without ever reaching the network when it has
// nothing to call with. These paths return "" so the cell falls outside its
// language and the machine takes its fail-safe path.
func TestAnthropicModelFailsSafe(t *testing.T) {
	cell := spec.NewCell("assess", "claude-sonnet-4-6", "Output 'complete' or 'incomplete'.",
		func(raw string) (string, bool) { return raw, raw != "" },
		func(string) bool { return true })

	t.Run("no api key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		if got := AnthropicModel(cell, nil); got != "" {
			t.Fatalf("want empty on missing key, got %q", got)
		}
	})

	t.Run("no model on cell", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-not-used")
		modelless := spec.NewCell("x", "", "instr",
			func(raw string) (string, bool) { return raw, true },
			func(string) bool { return true })
		if got := AnthropicModel(modelless, nil); got != "" {
			t.Fatalf("want empty on cell without model, got %q", got)
		}
	})
}

func grammarSafe() (spec.Grammar, spec.Safety) {
	return func(raw string) (string, bool) { return raw, raw != "" },
		func(string) bool { return true }
}

// An unconfined cell renders a plain completion; a confined cell renders a
// forced, strict tool call over its schema — that is generation-time confinement
// on the wire. The instruction goes in the (cache-breakpointed) system block and
// the context in the user turn.
func TestBuildRequestConfinement(t *testing.T) {
	g, s := grammarSafe()
	schema := map[string]any{"type": "object", "additionalProperties": false}

	plain, err := buildRequest(spec.NewCell("assess", "claude-sonnet-4-6", "do it", g, s), "the context")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(plain, &req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "tool_choice") {
		t.Fatalf("unconfined cell must not force a tool call: %s", plain)
	}
	// Instruction is the system block, with a cache breakpoint.
	sys, ok := req["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("instruction should be a system block: %s", plain)
	}
	sb := sys[0].(map[string]any)
	if sb["text"] != "do it" || sb["cache_control"] == nil {
		t.Fatalf("system block missing instruction or cache breakpoint: %s", plain)
	}
	// Context is the user turn.
	msgs := req["messages"].([]any)
	if m := msgs[0].(map[string]any); m["content"] != "the context" {
		t.Fatalf("context should be the user turn: %s", plain)
	}

	confined, err := buildRequest(spec.NewConfinedCell("act", "claude-sonnet-4-6", "do it", schema, g, s), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(confined, &req); err != nil {
		t.Fatal(err)
	}
	tc, ok := req["tool_choice"].(map[string]any)
	if !ok || tc["type"] != "tool" || tc["name"] != "act" {
		t.Fatalf("confined cell must force its tool: %s", confined)
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("confined cell must declare exactly one tool: %s", confined)
	}
	if tool := tools[0].(map[string]any); tool["strict"] != true {
		t.Fatalf("confinement requires strict tool use: %s", confined)
	}
}

// A cell sees exactly the hook context it declared, bounded — and the transcript
// is read from transcript_path, the gap the live smoke exposed.
func TestAssembleContext(t *testing.T) {
	if got := assembleContext(nil, map[string]any{"prompt": "x"}); got != "" {
		t.Fatalf("a cell that declares nothing gets nothing, got %q", got)
	}

	tf := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tf, []byte(`{"role":"user","content":"ship the parser"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := map[string]any{"transcript_path": tf, "prompt": "ignored unless declared"}

	got := assembleContext([]spec.ContextNeed{spec.NeedTranscript}, ev)
	if !strings.Contains(got, "ship the parser") || !strings.Contains(got, "<transcript>") {
		t.Fatalf("transcript not injected: %q", got)
	}
	if strings.Contains(got, "ignored unless declared") {
		t.Fatalf("undeclared context leaked in: %q", got)
	}

	// Oversized transcripts are tail-bounded.
	big := filepath.Join(t.TempDir(), "big.jsonl")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), maxTranscriptBytes*2), 0o600); err != nil {
		t.Fatal(err)
	}
	got = assembleContext([]spec.ContextNeed{spec.NeedTranscript}, map[string]any{"transcript_path": big})
	if len(got) > maxTranscriptBytes+200 || !strings.Contains(got, "truncated") {
		t.Fatalf("transcript not bounded: len=%d", len(got))
	}
}

// extractTerm returns the forced tool call's input (as JSON) for a confined
// cell, concatenated text otherwise, and "" on a refusal or shape mismatch.
func TestExtractTerm(t *testing.T) {
	toolResp := `{"content":[{"type":"tool_use","name":"act","input":{"action":"write","path":"/etc/passwd"}}]}`
	if got := extractTerm([]byte(toolResp), true, "act"); got != `{"action":"write","path":"/etc/passwd"}` {
		t.Fatalf("confined extract = %q", got)
	}

	textResp := `{"content":[{"type":"text","text":"complete"}]}`
	if got := extractTerm([]byte(textResp), false, "x"); got != "complete" {
		t.Fatalf("text extract = %q", got)
	}

	// A refusal carries no usable block -> "" -> machine fails safe.
	refusal := `{"content":[{"type":"text","text":""}],"stop_reason":"refusal"}`
	if got := extractTerm([]byte(refusal), true, "act"); got != "" {
		t.Fatalf("refusal must yield empty, got %q", got)
	}
	// Wrong tool name -> "".
	if got := extractTerm([]byte(toolResp), true, "other"); got != "" {
		t.Fatalf("mismatched tool name must yield empty, got %q", got)
	}
}
