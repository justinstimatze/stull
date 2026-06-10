package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/justinstimatze/stull/spec"
)

// maxTranscriptBytes bounds how much of a transcript a cell sees. Stop-hook
// transcripts grow without limit; an unbounded read would blow the context
// window and the token bill. We keep the *tail* (recent turns matter most for
// "is the task done now?") — note this deliberately forgoes transcript-prefix
// prompt caching, which would need a stable head instead. See buildRequest.
const maxTranscriptBytes = 60_000

// AnthropicModel binds the Model seam to the Anthropic Messages API using only
// the standard library. The cell's instruction is the (frozen) system prompt;
// the hook context it declared via Reading(...) is assembled into the user turn;
// Grammar/Safety then decide whether the output may reach control flow.
//
// If the cell carries a Schema, the call is generation-time confined: a forced,
// strict tool call over that schema, so the model *can only* emit a value in L
// (the raw handed back is the tool input as JSON). Without a Schema it is plain
// text and Grammar is the sole guarantee.
//
// Total and fail-safe: every failure path — no API key, a network error, a
// non-200, a refusal, an unreadable transcript — yields "" (outside any
// non-trivial L), so the machine fails safe. Bounded by a client timeout so a
// hung API can't wedge the hook. Never panics, never blocks unbounded.
func AnthropicModel(c spec.Cell, rctx *spec.Context) string {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" || c.Model == "" {
		return ""
	}

	var ev map[string]any
	if rctx != nil {
		ev = rctx.Event
	}
	body, err := buildRequest(c, assembleContext(c.Context, ev))
	if err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return extractTerm(respBody, c.Schema != nil, c.Name)
}

// assembleContext renders the hook context a cell declared into the user turn.
// It reads files (transcript_path) and is fail-open: anything missing is simply
// omitted, which makes the cell fail safe rather than act on partial input.
func assembleContext(needs []spec.ContextNeed, ev map[string]any) string {
	if len(needs) == 0 || ev == nil {
		return ""
	}
	var b strings.Builder
	for _, n := range needs {
		switch n {
		case spec.NeedPrompt:
			if p := asString(ev["prompt"]); p != "" {
				fmt.Fprintf(&b, "<prompt>\n%s\n</prompt>\n", p)
			}
		case spec.NeedEvent:
			if j, err := json.Marshal(ev); err == nil {
				fmt.Fprintf(&b, "<event>\n%s\n</event>\n", j)
			}
		case spec.NeedTranscript:
			if s := transcriptTail(ev); s != "" {
				fmt.Fprintf(&b, "<transcript>\n%s\n</transcript>\n", s)
			}
		}
	}
	return b.String()
}

// transcriptTail reads transcript_path and returns its tail, bounded by
// maxTranscriptBytes. Fail-open: a missing path or unreadable file yields "", so
// a guard or cell that depends on the transcript simply fails safe. Shared by
// assembleContext (cell user-turn) and LoadContext (deterministic transcript
// guards), so both see exactly the same bounded text.
func transcriptTail(ev map[string]any) string {
	if ev == nil {
		return ""
	}
	tp := asString(ev["transcript_path"])
	if tp == "" {
		return ""
	}
	data, err := os.ReadFile(tp)
	if err != nil {
		return ""
	}
	s := string(data)
	if len(s) > maxTranscriptBytes {
		s = "…(earlier turns truncated)…\n" + s[len(s)-maxTranscriptBytes:]
	}
	return s
}

// buildRequest renders the Messages API request. The cell instruction is the
// system block (frozen across hook fires, role-defining); the assembled context
// is the user turn (what varies). A confined cell adds a forced, strict tool
// call so the model can only emit L.
//
// Both fixed blocks — the instruction and, for a confined cell, the tool schema
// — carry a 1h-TTL cache_control breakpoint. Hooks fire minutes apart, so the
// default 5-min ephemeral cache expires *between* fires and the instruction is
// re-billed every time; the 1h TTL keeps the per-session-stable blocks warm
// across a working session. It only actually caches once a block exceeds the
// model's minimum cacheable prefix (a one-line instruction won't, harmlessly),
// but these are the correct breakpoints for the win as instructions and schemas
// grow. Only the per-fire context (the user turn) is newly billed.
func buildRequest(c spec.Cell, userContext string) ([]byte, error) {
	if strings.TrimSpace(userContext) == "" {
		userContext = "(no additional context provided)"
	}
	warm := &cacheControl{Type: "ephemeral", TTL: "1h"}
	r := anthropicRequest{
		Model:     c.Model,
		MaxTokens: 1024,
		System: []systemBlock{{
			Type: "text", Text: c.Instructions,
			CacheControl: warm,
		}},
		Messages: []anthropicMessage{{Role: "user", Content: userContext}},
	}
	if c.Schema != nil {
		r.Tools = []anthropicTool{{
			Name:         c.Name,
			Description:  c.Instructions,
			InputSchema:  c.Schema,
			Strict:       true,
			CacheControl: warm,
		}}
		r.ToolChoice = &anthropicToolChoice{Type: "tool", Name: c.Name}
	}
	return json.Marshal(r)
}

// extractTerm pulls the raw completion from a Messages API response body. For a
// confined cell it returns the forced tool call's input as JSON (Grammar parses
// that); otherwise it concatenates text blocks. A refusal or shape mismatch
// yields "" so the cell falls outside its language and the machine fails safe.
func extractTerm(respBody []byte, confined bool, toolName string) string {
	var out anthropicResponse
	if json.Unmarshal(respBody, &out) != nil {
		return ""
	}
	if confined {
		for _, b := range out.Content {
			if b.Type == "tool_use" && b.Name == toolName && len(b.Input) > 0 {
				return string(b.Input)
			}
		}
		return ""
	}
	var text string
	for _, b := range out.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return text
}

// Anthropic Messages API wire types (the subset stull uses). No
// temperature/top_p/budget_tokens are sent — all 400 on Opus 4.8 — and cells are
// small constrained classifications, so thinking is left off.
type anthropicRequest struct {
	Model      string               `json:"model"`
	MaxTokens  int                  `json:"max_tokens"`
	System     []systemBlock        `json:"system,omitempty"`
	Messages   []anthropicMessage   `json:"messages"`
	Tools      []anthropicTool      `json:"tools,omitempty"`
	ToolChoice *anthropicToolChoice `json:"tool_choice,omitempty"`
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "" = default 5m; "1h" = extended (hooks fire minutes apart)
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema"`
	Strict       bool           `json:"strict,omitempty"`
	CacheControl *cacheControl  `json:"cache_control,omitempty"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type anthropicResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
}
