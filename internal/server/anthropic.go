package server

// Anthropic-compatible /v1/messages endpoint.
//
// Translates the Anthropic Messages API (what Claude Code and other
// Anthropic-protocol clients speak) into the router's core request shape and
// translates the route result back, streaming (SSE) or non-streaming. Tool
// history survives translation: assistant tool_use blocks become
// core.Message ToolCalls and user tool_result blocks become RoleTool
// messages with ToolCallID, so agentic multi-turn tool use works through
// free/local providers.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/control"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

// ---------------------------------------------------------------------------
// request types
// ---------------------------------------------------------------------------

type anthRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Messages    []anthMessage   `json:"messages"`
	System      json.RawMessage `json:"system,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []anthTool      `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
}

type anthMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or block array
}

type anthContentBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// tool_use / tool_result
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"` // tool_use arguments
	// tool_result
	Content   json.RawMessage `json:"content,omitempty"` // string or blocks
	IsError   bool            `json:"is_error,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ---------------------------------------------------------------------------
// response types (non-stream)
// ---------------------------------------------------------------------------

type anthResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"` // "message"
	Role       string             `json:"role"` // "assistant"
	Model      string             `json:"model"`
	Content    []anthContentBlock `json:"content"`
	StopReason string             `json:"stop_reason,omitempty"`
	StopSeq    any                `json:"stop_sequence,omitempty"`
	Usage      anthUsage          `json:"usage"`
}

type anthUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAnthError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}

	start := time.Now()
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.onceFail("body read failed")
		writeAnthError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
		return
	}

	var q anthRequest
	if err := json.Unmarshal(body, &q); err != nil {
		s.onceFail("invalid json: " + err.Error())
		writeAnthError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	if q.Model == "" {
		s.onceFail("missing model")
		writeAnthError(w, http.StatusBadRequest, "invalid_request_error", "'model' is required")
		return
	}
	if q.MaxTokens <= 0 {
		// Anthropic requires max_tokens; default generously instead of failing.
		q.MaxTokens = 8192
	}
	if len(q.Messages) == 0 {
		s.onceFail("no messages")
		writeAnthError(w, http.StatusBadRequest, "invalid_request_error", "'messages' must be a non-empty array")
		return
	}

	msgs, terr := anthToCore(&q)
	if terr != nil {
		s.onceFail(terr.Error())
		writeAnthError(w, http.StatusBadRequest, "invalid_request_error", terr.Error())
		return
	}

	opts := &router.RouteOpts{}
	if q.MaxTokens > 0 {
		mt := q.MaxTokens
		opts.MaxTokens = &mt
	}
	opts.Temperature = q.Temperature
	opts.TopP = q.TopP
	for _, t := range q.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		opts.Tools = append(opts.Tools, router.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
		})
	}
	opts.ToolChoice = anthToolChoiceString(q.ToolChoice)

	// Ledger/control context. The Anthropic protocol carries session hints in
	// metadata.user_id; the X-Freellm-* headers work as on the OpenAI path.
	ctx := s.beginAnth(w, r, &q, msgs)
	r = r.WithContext(ctx)

	var (
		textBuf     strings.Builder
		toolBuf     []router.ToolChunk
		via         string
		streamState anthStreamState
	)
	if q.Stream {
		streamState.init(w)
	}
	sinks := router.Sinks{
		Text: func(delta string) {
			textBuf.WriteString(delta)
			if q.Stream {
				streamState.textDelta(w, delta)
			}
		},
		Tool: func(tc router.ToolChunk) {
			toolBuf = append(toolBuf, tc)
			if q.Stream {
				streamState.toolDelta(w, tc)
			}
		},
		Serving: func(v string) { via = v },
	}

	v, done, rerr := s.router.RouteRawCore(r.Context(), q.Model, msgs, q.Model, q.Stream, opts, sinks)
	via = v
	if rerr != nil {
		s.onceFail(q.Model + " failed: " + rerr.Error())
		if !q.Stream || !streamState.started {
			status, typ := statusFromError(rerr)
			if _, ok := rerr.(*router.VetoError); ok {
				writeAnthError(w, http.StatusForbidden, "permission_error", "vetoed: "+rerr.Error())
				s.metrics.RecordRequest(time.Since(start))
				return
			}
			writeAnthError(w, status, typ, "all providers failed for route "+q.Model+": "+errToMsg(rerr))
			s.metrics.RecordRequest(time.Since(start))
			return
		}
		// Stream already committed: emit an error event so clients close cleanly.
		streamState.error(w, "all providers failed for route "+q.Model+": "+errToMsg(rerr))
		s.metrics.RecordRequest(time.Since(start))
		return
	}

	stopReason := stopReasonFromDone(done.FinishReason)

	if q.Stream {
		streamState.finish(w, stopReason, done)
	} else {
		if via != "" {
			w.Header().Set("X-Freellm-Routed-Via", via)
		}
		content := make([]anthContentBlock, 0, 1+len(toolBuf))
		if txt := textBuf.String(); txt != "" {
			content = append(content, anthContentBlock{Type: "text", Text: txt})
		}
		for _, tc := range toolBuf {
			input := parseJSONOrObject(tc.Arguments)
			content = append(content, anthContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		if len(content) == 0 {
			content = append(content, anthContentBlock{Type: "text", Text: ""})
		}
		resp := anthResponse{
			ID:         "msg_" + newChunkID(),
			Type:       "message",
			Role:       "assistant",
			Model:      q.Model,
			Content:    content,
			StopReason: stopReason,
			Usage: anthUsage{
				InputTokens:  done.PromptTokens,
				OutputTokens: done.CompletionTokens,
			},
		}
		writeJSON(w, http.StatusOK, resp)
	}

	s.metrics.RecordRoute(labelFor(q.Model))
	s.metrics.RecordRequest(time.Since(start))
	s.onceOK()
}

// beginAnth mirrors beginChat for the Anthropic protocol: ledger call
// context, control-plane override headers and last-user prompt capture.
func (s *Server) beginAnth(w http.ResponseWriter, r *http.Request, q *anthRequest, msgs []core.Message) (ctx context.Context) {
	ctx = r.Context()
	c := ledger.CallFrom(ctx)
	if c == nil {
		c = &ledger.Call{ID: RequestID(ctx), Source: "server", Event: &UsageRecord{T: time.Now(), Source: "server"}}
		ctx = ledger.WithCall(ctx, c)
	}
	c.Tags = parseTags(r.Header.Get("X-Freellm-Tags"))
	ev := c.Event
	ev.Kind = ledger.KindRequest
	ev.Model, ev.Route, ev.Stream, ev.Session, ev.Tags = q.Model, q.Model, q.Stream, c.Session, c.Tags
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == core.RoleUser {
			ev.Prompt = s.rt.Capture(msgs[i].Content)
			break
		}
	}
	if c.ID != "" {
		w.Header().Set("X-Freellm-Request-Id", c.ID)
	}
	if c.Session != "" {
		w.Header().Set("X-Freellm-Session", c.Session)
	}

	ov := control.Override{
		Profile:  firstNonEmpty(r.Header.Get("X-Freellm-Profile")),
		Fallback: parseBool(r.Header.Get("X-Freellm-Fallback")),
		Forever:  parseBool(r.Header.Get("X-Freellm-Forever")),
	}
	if n, err := strconv.Atoi(r.Header.Get("X-Freellm-Max-Attempts")); err == nil && n > 0 {
		ov.MaxAttempts = n
	}
	if !ov.IsZero() {
		ctx = router.WithOverride(ctx, ov)
	}
	return ctx
}

// ---------------------------------------------------------------------------
// request translation
// ---------------------------------------------------------------------------

// anthToCore converts an Anthropic request to core messages. Text parts of a
// block array are joined; tool_use blocks become assistant ToolCalls;
// tool_result blocks become separate RoleTool messages (Anthropic puts them
// in user messages; OpenAI wants standalone tool messages).
func anthToCore(q *anthRequest) ([]core.Message, error) {
	msgs := make([]core.Message, 0, len(q.Messages)+4)

	if sys := systemString(q.System); sys != "" {
		msgs = append(msgs, core.Message{Role: core.RoleSystem, Content: sys})
	}

	for _, m := range q.Messages {
		var blocks []anthContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			// plain string content
			var s string
			if err2 := json.Unmarshal(m.Content, &s); err2 != nil {
				return nil, err2
			}
			msgs = append(msgs, core.Message{Role: core.Role(m.Role), Content: s})
			continue
		}

		var textParts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				textParts = append(textParts, b.Text)
			case "tool_use":
				msgs = append(msgs, core.Message{
					Role:    core.RoleAssistant,
					Content: strings.Join(textParts, "\n"),
					ToolCalls: []core.ToolCall{{
						ID:   b.ID,
						Type: "function",
						Function: core.ToolFunction{
							Name:      b.Name,
							Arguments: string(b.Input),
						},
					}},
				})
				textParts = nil
			case "tool_result":
				msgs = append(msgs, core.Message{
					Role:       core.RoleTool,
					ToolCallID: b.ToolUseID,
					Content:    toolResultString(b.Content),
				})
			case "image", "document":
				// Not supported by the text-only router core; keep a marker so
				// the conversation shape stays honest.
				textParts = append(textParts, "["+b.Type+" omitted]")
			}
		}
		if txt := strings.Join(textParts, "\n"); txt != "" {
			msgs = append(msgs, core.Message{Role: core.Role(m.Role), Content: txt})
		}
	}
	return msgs, nil
}

func systemString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func toolResultString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			default:
				parts = append(parts, "["+b.Type+" omitted]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// anthToolChoiceString maps Anthropic tool_choice to the router's string
// form: {"type":"auto"} → "auto", {"type":"any"} → "required",
// {"type":"none"} → "none", {"type":"tool","name":N} → "function:N".
func anthToolChoiceString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return ""
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "none":
		return "none"
	case "any", "required":
		return "required"
	case "tool":
		if tc.Name != "" {
			return "function:" + tc.Name
		}
		return "required"
	}
	return ""
}

func stopReasonFromDone(finish string) string {
	switch finish {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop", "":
		return "end_turn"
	default:
		return finish
	}
}

func parseJSONOrObject(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	var v any
	if json.Unmarshal([]byte(trimmed), &v) == nil {
		return json.RawMessage(trimmed)
	}
	// Not valid JSON (e.g. upstream emitted prose): wrap as a single field so
	// clients expecting an object schema still parse the message.
	escaped, _ := json.Marshal(map[string]string{"raw": trimmed})
	return escaped
}

// ---------------------------------------------------------------------------
// SSE translation
// ---------------------------------------------------------------------------

type anthStreamState struct {
	started   bool
	blockIdx  int
	blockOpen bool
}

func (st *anthStreamState) init(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
}

// commit writes the headers+message_start on the first delta (Anthropic SSE
// responses must begin with message_start before any content event).
func (st *anthStreamState) commit(w http.ResponseWriter, model string) {
	if st.started {
		return
	}
	st.started = true
	writeSSE(w, map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":      "msg_" + newChunkID(),
			"type":    "message",
			"role":    "assistant",
			"model":   model,
			"content": []any{},
			"usage":   map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (st *anthStreamState) textDelta(w http.ResponseWriter, delta string) {
	st.commit(w, "")
	if !st.blockOpen {
		writeSSE(w, map[string]any{
			"type":          "content_block_start",
			"index":         st.blockIdx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		st.blockOpen = true
	}
	writeSSE(w, map[string]any{
		"type":  "content_block_delta",
		"index": st.blockIdx,
		"delta": map[string]any{"type": "text_delta", "text": delta},
	})
}

func (st *anthStreamState) closeBlock(w http.ResponseWriter) {
	if st.blockOpen {
		writeSSE(w, map[string]any{"type": "content_block_stop", "index": st.blockIdx})
		st.blockIdx++
		st.blockOpen = false
	}
}

func (st *anthStreamState) toolDelta(w http.ResponseWriter, tc router.ToolChunk) {
	st.commit(w, "")
	st.closeBlock(w) // tool calls start a new block after any text block
	writeSSE(w, map[string]any{
		"type":          "content_block_start",
		"index":         st.blockIdx,
		"content_block": map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": map[string]any{}},
	})
	// The router may deliver the arguments in fragments; Anthropic wants
	// partial_json deltas inside an open input_json_delta block.
	writeSSE(w, map[string]any{
		"type":  "content_block_delta",
		"index": st.blockIdx,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Arguments},
	})
	writeSSE(w, map[string]any{"type": "content_block_stop", "index": st.blockIdx})
	st.blockIdx++
}

func (st *anthStreamState) finish(w http.ResponseWriter, stopReason string, done *router.Done) {
	st.commit(w, "")
	st.closeBlock(w)
	delta := map[string]any{
		"stop_reason":   stopReason,
		"stop_sequence": nil,
	}
	usage := map[string]int{"output_tokens": done.CompletionTokens}
	if done.PromptTokens > 0 {
		usage["input_tokens"] = done.PromptTokens
	}
	writeSSE(w, map[string]any{
		"type":  "message_delta",
		"delta": delta,
		"usage": usage,
	})
	writeSSE(w, map[string]any{"type": "message_stop"})
}

func (st *anthStreamState) error(w http.ResponseWriter, msg string) {
	writeSSE(w, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": msg},
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeSSE(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	w.Write([]byte("event: "))
	w.Write([]byte(v.(map[string]any)["type"].(string)))
	w.Write([]byte("\ndata: "))
	w.Write(b)
	w.Write([]byte("\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeAnthError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	})
}
