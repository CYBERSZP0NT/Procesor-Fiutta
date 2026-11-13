// Package openailike implements core.Provider for any OpenAI-compatible
// HTTP API endpoint. One provider instance covers google, groq, openrouter,
// pollinations, and every other "OpenAI-like" source.
//
// It supports:
//   - Full SSE streaming (chunked data: / [DONE])
//   - Non-streaming single-response
//   - Bearer, custom header (x-goog-api-key), or no auth
//   - tools / tool_choice pass-through
//   - max_tokens / temperature / top_p options
//   - Per-request timeout
//   - /models fetch
//   - Available() health check
//   - Error mapping → *core.UpstreamErr
package openailike

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
)

// Provider is one OpenAI-compatible upstream, configured by its config.Provider entry.
type Provider struct {
	name    string
	baseURL string // e.g. "https://api.groq.com/openai/v1"
	kind    core.Kind
	header  string // non-standard auth header name; "" = use Authorization: Bearer
	kindStr string // "keyless" | "keyed" | …

	keySrc core.KeySource // may be nil
	keyIdx int
	keyMu  sync.Mutex

	client  *http.Client
	timeout time.Duration

	// Cached /models response.
	modelsMu sync.RWMutex
	models   []core.Model
}

var _ core.Provider = (*Provider)(nil)

// New creates an openailike Provider from a config entry.
func New(p config.Provider, ks core.KeySource, timeout time.Duration) *Provider {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	kind := core.Kind(strings.ToLower(p.Kind))
	prov := &Provider{
		name:    p.Name,
		baseURL: strings.TrimRight(p.BaseURL, "/"),
		kind:    kind,
		header:  p.Header,
		kindStr: strings.ToLower(p.Kind),
		keySrc:  ks,
		client: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
	}
	return prov
}

// Name returns the provider identifier.
func (p *Provider) Name() string { return p.name }

// Kinds returns the transport kind(s).
func (p *Provider) Kinds() []core.Kind { return []core.Kind{p.kind} }

// Keys returns the current set of keys for this provider (for introspection).
func (p *Provider) Keys(ctx context.Context) []string {
	if p.keySrc == nil {
		return nil
	}
	return p.keySrc.Keys(ctx, p.name)
}

// nextKey returns the next key in round-robin order (advances index).
func (p *Provider) nextKey(ctx context.Context) string {
	if p.keySrc == nil {
		return ""
	}
	keys := p.keySrc.Keys(ctx, p.name)
	if len(keys) == 0 {
		return ""
	}
	p.keyMu.Lock()
	idx := p.keyIdx % len(keys)
	p.keyIdx++
	p.keyMu.Unlock()
	return keys[idx]
}

// currentKey returns the current key without advancing the rotation index.
// Used for informational calls like /models where we don't want to consume
// a rotation slot.
func (p *Provider) currentKey(ctx context.Context) string {
	if p.keySrc == nil {
		return ""
	}
	keys := p.keySrc.Keys(ctx, p.name)
	if len(keys) == 0 {
		return ""
	}
	p.keyMu.Lock()
	idx := p.keyIdx % len(keys)
	p.keyMu.Unlock()
	return keys[idx]
}

// Available reports whether this provider is callable right now.
func (p *Provider) Available(ctx context.Context) *core.Availability {
	// Keyless: always available (no key check).
	if p.kind == core.KindKeyless || p.kind == core.KindScraper || p.kind == core.KindLocal {
		return &core.Availability{OK: true, Reason: "keyless/local", KeyCount: 0}
	}
	if p.keySrc == nil {
		return &core.Availability{OK: false, Reason: "no key source configured"}
	}
	keys := p.keySrc.Keys(ctx, p.name)
	if len(keys) == 0 {
		return &core.Availability{OK: false, Reason: "no keys available (key_env not set)"}
	}
	return &core.Availability{OK: true, Reason: "key present", KeyCount: len(keys)}
}

// Models returns the model catalog from /models endpoint, or cached.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	p.modelsMu.RLock()
	if len(p.models) > 0 {
		defer p.modelsMu.RUnlock()
		return p.models, nil
	}
	p.modelsMu.RUnlock()

	// Fetch live.
	url := p.baseURL + "/models"
	ctx2, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("openailike[%s]: %w", p.name, err)
	}
	p.setAuthReadOnly(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, "", err.Error(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, core.NewUpstreamErr(resp.StatusCode, p.name, "", strings.TrimSpace(string(body)), nil)
	}

	var payload struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("openailike[%s]: decode models: %w", p.name, err)
	}

	models := make([]core.Model, 0, len(payload.Data))
	for _, m := range payload.Data {
		models = append(models, core.Model{
			Provider: p.name,
			ID:       m.ID,
			Name:     m.Name,
			Kinds:    []core.Kind{p.kind},
		})
	}

	p.modelsMu.Lock()
	p.models = models
	p.modelsMu.Unlock()
	return models, nil
}

// Stream sends a chat completion request and consumes the (SSE or JSON) response,
// calling the StreamOut sinks as data arrives.
func (p *Provider) Stream(ctx context.Context, chatReq *core.ChatRequest, out *core.StreamOut) (*core.StreamDone, error) {
	// Build request body.
	body, err := p.buildBody(chatReq, out)
	if err != nil {
		return nil, err
	}

	url := p.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, chatReq.Model, err.Error(), err)
	}
	req.Header.Set("Content-Type", "application/json")
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, chatReq.Model, err.Error(), err)
	}
	defer resp.Body.Close()

	// Report status + quota headers (x-ratelimit-*, retry-after) upward.
	if out != nil && out.Meta != nil {
		out.Meta(resp.StatusCode, core.ParseRateLimit(resp.Header))
	}

	// Map HTTP errors.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		msg := strings.TrimSpace(string(errBody))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		return nil, core.NewUpstreamErr(resp.StatusCode, p.name, chatReq.Model, msg, nil)
	}

	// Detect streaming vs non-streaming from response content-type.
	// We trust the response format over the request stream flag,
	// because some providers may return JSON even when stream=true was requested
	// (e.g. for errors, or non-standard implementations).
	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(contentType, "text/event-stream")

	if isSSE {
		return p.consumeSSE(ctx, resp.Body, out, chatReq.Model)
	}
	return p.consumeJSON(resp.Body, out, chatReq.Model)
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

type upstreamRequest struct {
	Model       string          `json:"model"`
	Messages    []upstreamMsg   `json:"messages"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream"`
	Tools       []upstreamTool  `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFmt json.RawMessage `json:"response_format,omitempty"`
}

type upstreamMsg struct {
	Role       string             `json:"role"`
	Content    any                `json:"content,omitempty"` // string or [] for tool msgs
	Name       string             `json:"name,omitempty"`
	ToolCalls  []upstreamToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

type upstreamToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function upstreamFunc `json:"function"`
}

type upstreamFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type upstreamTool struct {
	Type     string          `json:"type"`
	Function upstreamFuncDef `json:"function"`
}

type upstreamFuncDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

func (p *Provider) buildBody(req *core.ChatRequest, out *core.StreamOut) ([]byte, error) {
	stream := out != nil && (out.TextSink != nil || out.ToolSink != nil || out.ReasoningSink != nil)
	if req.Stream {
		stream = true
	}

	ur := upstreamRequest{
		Model:    req.Model,
		Stream:   stream,
		Messages: make([]upstreamMsg, 0, len(req.Messages)),
	}

	for _, m := range req.Messages {
		um := upstreamMsg{Role: string(m.Role)}
		if m.Content != "" {
			um.Content = m.Content
		}
		if m.Name != "" {
			um.Name = m.Name
		}
		if m.ToolCallID != "" {
			um.ToolCallID = m.ToolCallID
		}
		for _, tc := range m.ToolCalls {
			um.ToolCalls = append(um.ToolCalls, upstreamToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: upstreamFunc{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		ur.Messages = append(ur.Messages, um)
	}

	if req.MaxTokens > 0 {
		ur.MaxTokens = &req.MaxTokens
	}
	if req.Temperature != nil {
		ur.Temperature = req.Temperature
	}
	if req.TopP != nil {
		ur.TopP = req.TopP
	}
	for _, t := range req.Tools {
		ur.Tools = append(ur.Tools, upstreamTool{
			Type: t.Type,
			Function: upstreamFuncDef{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		})
	}
	if req.ToolChoice != nil {
		b, err := marshalToolChoice(*req.ToolChoice)
		if err == nil {
			ur.ToolChoice = b
		}
	}
	if req.ResponseFmt != "" {
		b, _ := json.Marshal(map[string]string{"type": req.ResponseFmt})
		ur.ResponseFmt = b
	}

	data, err := json.Marshal(ur)
	if err != nil {
		return nil, fmt.Errorf("openailike[%s]: marshal request: %w", p.name, err)
	}
	return data, nil
}

func marshalToolChoice(tc core.ToolChoice) ([]byte, error) {
	if tc.Function != nil {
		return json.Marshal(map[string]any{
			"type":     tc.Type,
			"function": map[string]string{"name": tc.Function.Name},
		})
	}
	return json.Marshal(map[string]string{"type": tc.Type})
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

func (p *Provider) setAuth(req *http.Request) {
	p.setAuthWith(req, true)
}

func (p *Provider) setAuthReadOnly(req *http.Request) {
	p.setAuthWith(req, false)
}

func (p *Provider) setAuthWith(req *http.Request, advance bool) {
	// Keyless / local / scraper: no auth.
	if p.kind == core.KindKeyless || p.kind == core.KindLocal || p.kind == core.KindScraper {
		return
	}
	var key string
	if advance {
		key = p.nextKey(req.Context())
	} else {
		key = p.currentKey(req.Context())
	}
	if key == "" {
		return
	}
	if p.header != "" {
		req.Header.Set(p.header, key)
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// ---------------------------------------------------------------------------
// SSE consumption
// ---------------------------------------------------------------------------

func (p *Provider) consumeSSE(ctx context.Context, body io.Reader, out *core.StreamOut, model string) (*core.StreamDone, error) {
	// Accumulate tool-call arguments per index.
	toolArgs := make(map[int]string)
	toolNames := make(map[int]string)
	toolIDs := make(map[int]string)

	down := &core.StreamDone{}

	decoder := NewSSEReader(body)
	for {
		data, err := decoder.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			// Read timeout or context cancel
			if ctx.Err() != nil {
				return nil, core.NewUpstreamErr(0, p.name, model, "stream context cancelled", ctx.Err())
			}
			return nil, core.NewUpstreamErr(0, p.name, model, "SSE read: "+err.Error(), err)
		}

		if strings.TrimSpace(data) == "[DONE]" {
			break
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Non-fatal: skip unparseable lines.
			continue
		}

		// Handle error in stream (some providers send {"error": ...} mid-stream).
		if chunk.Error != nil {
			msg := chunk.Error.Message
			if msg == "" {
				msg = "stream error"
			}
			return nil, core.NewUpstreamErr(400, p.name, model, msg, nil)
		}

		for _, ch := range chunk.Choices {
			// Content delta
			if ch.Delta.Content != "" {
				if out != nil && out.TextSink != nil {
					out.TextSink(ch.Delta.Content)
				}
			}
			// Reasoning delta (some providers)
			if ch.Delta.ReasoningContent != "" {
				if out != nil && out.ReasoningSink != nil {
					out.ReasoningSink(ch.Delta.ReasoningContent)
				}
			}
			// Tool calls delta
			for _, tc := range ch.Delta.ToolCalls {
				idx := tc.Index
				if tc.ID != "" {
					toolIDs[idx] = tc.ID
				}
				if tc.Function.Name != "" {
					toolNames[idx] = tc.Function.Name
				}
				toolArgs[idx] += tc.Function.Arguments
				// Forward any non-empty tool-call delta chunk (name or args),
				// so streaming tool-call fragments pass through intact.
				if out != nil && out.ToolSink != nil && (tc.Function.Name != "" || tc.Function.Arguments != "" || tc.ID != "") {
					out.ToolSink(core.ToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: core.ToolFunction{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					})
				}
			}
			// Finish reason
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				down.FinishReason = *ch.FinishReason
			}
		}

		// Usage (usually in final chunk)
		if chunk.Usage != nil {
			down.PromptTokens = chunk.Usage.PromptTokens
			down.CompletionTokens = chunk.Usage.CompletionTokens
		}
	}

	// Finalize: if there were tool calls collected but no finish_reason set,
	// default to "tool_calls".
	if down.FinishReason == "" {
		if len(toolArgs) > 0 {
			down.FinishReason = "tool_calls"
		} else {
			down.FinishReason = "stop"
		}
	}

	// Emit accumulated tool calls (full version) once at the end if not yet emitted as chunks.
	if out != nil && out.ToolSink != nil && down.FinishReason == "tool_calls" {
		// Already emitted incrementally above via the loop.
		// If tool sink was not called during streaming (no name chunk), emit final.
		_ = toolArgs
		_ = toolNames
		_ = toolIDs
	}

	down.PromptTokens = coalesce(down.PromptTokens)
	down.CompletionTokens = coalesce(down.CompletionTokens)

	return down, nil
}

func coalesce(v int) int {
	if v < 0 {
		return -1
	}
	return v
}

// argsToString converts an any (from JSON decode) to a string.
func argsToString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// ---------------------------------------------------------------------------
// Non-streaming JSON
// ---------------------------------------------------------------------------

func (p *Provider) consumeJSON(body io.Reader, out *core.StreamOut, model string) (*core.StreamDone, error) {
	data, err := io.ReadAll(io.LimitReader(body, 10<<20)) // max 10 MB
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, model, "read body: "+err.Error(), err)
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content   string             `json:"content"`
				ToolCalls []upstreamToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, core.NewUpstreamErr(200, p.name, model, "decode response: "+err.Error(), nil)
	}
	if resp.Error != nil {
		return nil, core.NewUpstreamErr(400, p.name, model, resp.Error.Message, nil)
	}
	if len(resp.Choices) == 0 {
		return nil, core.NewUpstreamErr(599, p.name, model, "empty response (no choices)", nil)
	}

	ch := resp.Choices[0]
	done := &core.StreamDone{
		FinishReason:     ch.FinishReason,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}

	if out != nil {
		if out.TextSink != nil && ch.Message.Content != "" {
			out.TextSink(ch.Message.Content)
		}
		if out.ToolSink != nil {
			for _, tc := range ch.Message.ToolCalls {
				out.ToolSink(core.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: core.ToolFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
	}

	if done.FinishReason == "" {
		if len(ch.Message.ToolCalls) > 0 {
			done.FinishReason = "tool_calls"
		} else {
			done.FinishReason = "stop"
		}
	}

	return done, nil
}
