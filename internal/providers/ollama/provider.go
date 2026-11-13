// Package ollama implements core.Provider for a local Ollama server
// using the native /api/chat (NDJSON stream) and /api/tags endpoints.
//
// This replaces the previous "ollama via openailike" workaround.
// The native protocol is simpler: no SSE, just newline-delimited JSON objects
// streamed from /api/chat with stream:true.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
)

// Provider implements core.Provider for a local Ollama server.
type Provider struct {
	name    string
	baseURL string // e.g. "http://localhost:11434" (no trailing /v1; ollama native)
	kind    core.Kind
	client  *http.Client
	timeout time.Duration

	modelsMu sync.RWMutex
	models   []core.Model
}

var _ core.Provider = (*Provider)(nil)

// New creates an Ollama provider from config.
// cfg.BaseURL should be the Ollama host (e.g. http://localhost:11434).
// If it ends with /v1 or /api, it is stripped to get the native base.
func New(p config.Provider, timeout time.Duration) *Provider {
	if timeout <= 0 {
		timeout = 300 * time.Second // local models can be slow
	}
	base := strings.TrimRight(p.BaseURL, "/")
	// Ollama native API is at the root; /v1 is the OpenAI-compat shim.
	// Strip common suffixes to get the native endpoint.
	base = strings.TrimSuffix(base, "/v1")
	base = strings.TrimSuffix(base, "/api")

	return &Provider{
		name:    p.Name,
		baseURL: base,
		kind:    core.KindLocal,
		client: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
	}
}

// Name returns the provider identifier.
func (p *Provider) Name() string { return p.name }

// Kinds returns the transport kind.
func (p *Provider) Kinds() []core.Kind { return []core.Kind{core.KindLocal} }

// Available reports whether the Ollama server is reachable.
func (p *Provider) Available(ctx context.Context) *core.Availability {
	start := time.Now()
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := p.client.Get(baseURL(ctx2, p.baseURL, "/api/tags"))
	if err != nil {
		return &core.Availability{
			OK:     false,
			Reason: "ollama not reachable: " + err.Error(),
		}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	latency := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		return &core.Availability{
			OK:      false,
			Reason:  fmt.Sprintf("ollama /api/tags returned HTTP %d", resp.StatusCode),
			Latency: latency,
		}
	}
	return &core.Availability{
		OK:      true,
		Reason:  "ollama reachable",
		Up:      true,
		Latency: latency,
	}
}

// Models returns the list of models available on the local Ollama instance.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	p.modelsMu.RLock()
	if len(p.models) > 0 {
		defer p.modelsMu.RUnlock()
		return p.models, nil
	}
	p.modelsMu.RUnlock()

	ctx2, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	resp, err := p.client.Get(baseURL(ctx2, p.baseURL, "/api/tags"))
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, "", err.Error(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, core.NewUpstreamErr(resp.StatusCode, p.name, "", strings.TrimSpace(string(body)), nil)
	}

	var payload struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Digest  string `json:"digest"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
				Family        string `json:"family"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("ollama[%s]: decode tags: %w", p.name, err)
	}

	models := make([]core.Model, 0, len(payload.Models))
	for _, m := range payload.Models {
		models = append(models, core.Model{
			Provider: p.name,
			ID:       m.Name,
			Name:     m.Name,
			Kinds:    []core.Kind{core.KindLocal},
			Tier: core.TierInfo{
				Caps:  m.Details.ParameterSize,
				Notes: "Local inference via Ollama",
			},
		})
	}

	p.modelsMu.Lock()
	p.models = models
	p.modelsMu.Unlock()
	return models, nil
}

// Stream sends a chat request to /api/chat and consumes the NDJSON stream.
func (p *Provider) Stream(ctx context.Context, req *core.ChatRequest, out *core.StreamOut) (*core.StreamDone, error) {
	body, err := p.buildRequest(req, out)
	if err != nil {
		return nil, err
	}

	url := baseURL(ctx, p.baseURL, "/api/chat")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, req.Model, err.Error(), err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.name, req.Model, err.Error(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		msg := strings.TrimSpace(string(errBody))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		return nil, core.NewUpstreamErr(resp.StatusCode, p.name, req.Model, msg, nil)
	}

	// Ollama /api/chat always streams NDJSON (even non-stream mode sends one
	// line per "chunk" of the generated response, plus a final "done" line).
	return p.consumeNDJSON(ctx, resp.Body, out, req.Model)
}

// ---------------------------------------------------------------------------
// NDJSON stream consumption
// ---------------------------------------------------------------------------

// apiChatLine is one NDJSON line from Ollama's /api/chat endpoint.
type apiChatLine struct {
	Model   string `json:"model"`
	Message struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string `json:"name"`
				Arguments any    `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	Done          bool   `json:"done"`
	DoneReason    string `json:"done_reason"`
	PromptTokens  int    `json:"prompt_eval_count"`
	CompletionTok int    `json:"eval_count"`
}

func (p *Provider) consumeNDJSON(ctx context.Context, body io.Reader, out *core.StreamOut, model string) (*core.StreamDone, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // up to 1 MB lines

	done := &core.StreamDone{
		FinishReason:     "stop",
		PromptTokens:     -1,
		CompletionTokens: -1,
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, core.NewUpstreamErr(0, p.name, model, "context cancelled during stream", ctx.Err())
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk apiChatLine
		if err := json.Unmarshal(line, &chunk); err != nil {
			// Skip unparseable lines (may be warnings from the server).
			continue
		}

		// Content text.
		if chunk.Message.Content != "" && out != nil && out.TextSink != nil {
			out.TextSink(chunk.Message.Content)
		}

		// Tool calls.
		if out != nil && out.ToolSink != nil {
			for _, tc := range chunk.Message.ToolCalls {
				args := ""
				if tc.Function.Arguments != nil {
					if s, ok := tc.Function.Arguments.(string); ok {
						args = s
					} else {
						b, _ := json.Marshal(tc.Function.Arguments)
						args = string(b)
					}
				}
				out.ToolSink(core.ToolCall{
					ID:   "tool_call_0",
					Type: "function",
					Function: core.ToolFunction{
						Name:      tc.Function.Name,
						Arguments: args,
					},
				})
			}
		}

		// Final: done=true line carries token counts.
		if chunk.Done {
			if chunk.PromptTokens > 0 {
				done.PromptTokens = chunk.PromptTokens
			}
			if chunk.CompletionTok > 0 {
				done.CompletionTokens = chunk.CompletionTok
			}
			if chunk.DoneReason != "" {
				done.FinishReason = chunk.DoneReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, core.NewUpstreamErr(0, p.name, model, "stream cancelled", ctx.Err())
		}
		return nil, core.NewUpstreamErr(0, p.name, model, "read stream: "+err.Error(), err)
	}

	return done, nil
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

type ollamaChatReq struct {
	Model    string       `json:"model"`
	Messages []ollamaMsg  `json:"messages"`
	Stream   bool         `json:"stream"`
	Options  *ollamaOpts  `json:"options,omitempty"`
	Tools    []ollamaTool `json:"tools,omitempty"`
}

type ollamaMsg struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

type ollamaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  any    `json:"parameters,omitempty"`
	} `json:"function"`
}

type ollamaOpts struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"` // equivalent to max_tokens
}

func (p *Provider) buildRequest(req *core.ChatRequest, out *core.StreamOut) ([]byte, error) {
	stream := req.Stream || (out != nil && out.TextSink != nil)

	ocr := ollamaChatReq{
		Model:  req.Model,
		Stream: stream,
	}

	for _, m := range req.Messages {
		om := ollamaMsg{
			Role:    string(m.Role),
			Content: m.Content,
		}
		for _, tc := range m.ToolCalls {
			var omToolCall ollamaToolCall
			omToolCall.Function.Name = tc.Function.Name
			// Ollama expects arguments as a parsed JSON object (or empty object).
			var args any
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			if args == nil {
				args = map[string]any{}
			}
			omToolCall.Function.Arguments = args
			om.ToolCalls = append(om.ToolCalls, omToolCall)
		}
		ocr.Messages = append(ocr.Messages, om)
	}

	if req.MaxTokens > 0 || req.Temperature != nil || req.TopP != nil {
		ocr.Options = &ollamaOpts{}
		if req.MaxTokens > 0 {
			np := req.MaxTokens
			ocr.Options.NumPredict = np
		}
		if req.Temperature != nil {
			t := *req.Temperature
			ocr.Options.Temperature = &t
		}
		if req.TopP != nil {
			tp := *req.TopP
			ocr.Options.TopP = &tp
		}
	}

	for _, t := range req.Tools {
		var ot ollamaTool
		ot.Type = "function"
		ot.Function.Name = t.Function.Name
		ot.Function.Description = t.Function.Description
		ot.Function.Parameters = t.Function.Parameters
		ocr.Tools = append(ocr.Tools, ot)
	}

	data, err := json.Marshal(ocr)
	if err != nil {
		return nil, fmt.Errorf("ollama[%s]: marshal request: %w", p.name, err)
	}
	return data, nil
}

// baseURL assembles a full URL from the base + path.
func baseURL(_ context.Context, base, path string) string {
	if strings.HasSuffix(base, "/") {
		base = base[:len(base)-1]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
