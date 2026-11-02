package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// ---------------------------------------------------------------------------
// OpenAI-wire request types (what clients send us)
// ---------------------------------------------------------------------------

type oaMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []oaToolCallIn  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type oaToolCallIn struct {
	ID       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Index    *int     `json:"index,omitempty"`
	Function oaFuncIn `json:"function"`
}

type oaFuncIn struct {
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type oaToolDef struct {
	Type     string    `json:"type"`
	Function oaFuncDef `json:"function"`
}

type oaFuncDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type oaChatRequest struct {
	Model       string            `json:"model"`
	Messages    []oaMessage       `json:"messages"`
	Stream      bool              `json:"stream"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
	TopP        *float64          `json:"top_p,omitempty"`
	Tools       []oaToolDef       `json:"tools,omitempty"`
	ToolChoice  json.RawMessage   `json:"tool_choice,omitempty"`
	User        string            `json:"user,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// contentString normalizes message content (string | array | null) to a string.
func (m oaMessage) contentString() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	b := []byte(m.Content)
	// arrays/objects: keep JSON for pass-through
	if len(b) > 0 && b[0] == '[' {
		return string(b)
	}
	var v any
	if err := json.Unmarshal(m.Content, &v); err == nil {
		out, _ := json.Marshal(v)
		return string(out)
	}
	return ""
}

func (m oaMessage) toolCallArguments(tc oaToolCallIn) string {
	if len(tc.Function.Arguments) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(tc.Function.Arguments, &s); err == nil {
		return s
	}
	out := make([]byte, 0, len(tc.Function.Arguments))
	out = append(out, tc.Function.Arguments...)
	return string(out)
}

// ---------------------------------------------------------------------------
// OpenAI-wire response types (what we send back)
// ---------------------------------------------------------------------------

// writeJSON encodes v as pretty-adjacent application/json.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAError writes the OpenAI error envelope:
//
//	{ "error": { "message": …, "type": …, "code": …, "param": … } }
func writeOAError(w http.ResponseWriter, status int, msg, typ, param string) {
	errObj := map[string]any{
		"message": msg,
		"type":    typ,
		"code":    nil,
	}
	if param != "" {
		errObj["param"] = param
	} else {
		delete(errObj, "param")
	}
	if status == http.StatusTooManyRequests {
		errObj["code"] = "rate_limit_exceeded"
	}
	writeJSON(w, status, map[string]any{"error": errObj})
}

// writeChunk emits one SSE `data: {…chat.completion.chunk…}` line.
func writeChunk(w http.ResponseWriter, chunk any, flush func()) {
	b, _ := json.Marshal(chunk)
	line := append([]byte("data: "), b...)
	line = append(line, '\n', '\n')
	_, _ = w.Write(line)
	if flush != nil {
		flush()
	}
}

// writeSSEDone emits the terminal `data: [DONE]` line.
func writeSSEDone(w http.ResponseWriter, flush func()) {
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flush != nil {
		flush()
	}
}

// flushFn returns the Flush function if the writer supports it.
func flushFn(w http.ResponseWriter) func() {
	if f, ok := w.(interface{ Flush() }); ok {
		return f.Flush
	}
	return nil
}

// newChunkID builds a stable chatcmpl id for the response session.
func newChunkID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}
