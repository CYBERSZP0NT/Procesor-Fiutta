package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
)

// fakeOllama creates an httptest server that mimics the Ollama native API.
type fakeOllama struct {
	Server     *httptest.Server
	ModelsList []string
	StreamText string
}

func newFakeOllama(models ...string) *fakeOllama {
	f := &fakeOllama{
		ModelsList: models,
		StreamText: "Hello from fake Ollama!",
	}

	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/tags" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{
				"models": mapModels(models),
			})
		case r.URL.Path == "/api/chat" && r.Method == http.MethodPost:
			// Read and ignore the request body.
			var body struct {
				Stream bool `json:"stream"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/x-ndjson")
			fl, _ := w.(http.Flusher)
			if f.StreamText == "" {
				f.StreamText = "Hello from fake Ollama!"
			}
			// Stream two chunks then done.
			chunks := [][2]string{
				{f.StreamText[:len(f.StreamText)/2], "false"},
				{f.StreamText[len(f.StreamText)/2:], "false"},
			}
			for _, c := range chunks {
				line, _ := json.Marshal(map[string]any{
					"model":   "llama3.2:3b",
					"message": map[string]string{"role": "assistant", "content": c[0]},
					"done":    false,
				})
				fmt.Fprintf(w, "%s\n", line)
				if fl != nil {
					fl.Flush()
				}
			}
			// Done line
			done, _ := json.Marshal(map[string]any{
				"model":             "llama3.2:3b",
				"message":           map[string]string{"role": "assistant", "content": ""},
				"done":              true,
				"done_reason":       "stop",
				"prompt_eval_count": 5,
				"eval_count":        10,
			})
			fmt.Fprintf(w, "%s\n", done)
			if fl != nil {
				fl.Flush()
			}

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	return f
}

func mapModels(names []string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{
			"name":   n,
			"size":   int64(1073741824),
			"digest": "abc123def456",
			"details": map[string]any{
				"parameter_size": "3B",
				"family":         "llama",
			},
		})
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func testProvider(serverURL string) *Provider {
	cfg := config.Provider{
		Name:    "test_ollama",
		Impl:    "ollama",
		BaseURL: serverURL,
		Kind:    "local",
	}
	return New(cfg, 10*time.Second)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestOllamaName(t *testing.T) {
	p := testProvider("http://localhost:9999")
	if p.Name() != "test_ollama" {
		t.Errorf("Name() = %q, want test_ollama", p.Name())
	}
}

func TestOllamaKinds(t *testing.T) {
	p := testProvider("http://localhost:9999")
	kinds := p.Kinds()
	if len(kinds) != 1 || kinds[0] != core.KindLocal {
		t.Errorf("Kinds() = %v, want [local]", kinds)
	}
}

func TestOllamaAvailableReachable(t *testing.T) {
	f := newFakeOllama("llama3.2:3b", "qwen:7b")
	defer f.Server.Close()

	p := testProvider(f.Server.URL)
	ctx := context.Background()

	av := p.Available(ctx)
	if !av.OK {
		t.Errorf("Available should be OK for reachable server, got: %s", av.Reason)
	}
	if !av.Up {
		t.Error("Available.Up should be true for reachable server")
	}
}

func TestOllamaAvailableUnreachable(t *testing.T) {
	p := testProvider("http://localhost:19999") // almost certainly not listening
	ctx := context.Background()

	av := p.Available(ctx)
	if av.OK {
		t.Error("Available should be false for unreachable server")
	}
	if av.Reason == "" {
		t.Error("Available.Reason should be non-empty")
	}
}

func TestOllamaModels(t *testing.T) {
	f := newFakeOllama("llama3.2:3b", "qwen2.5:7b", "mistral:7b")
	defer f.Server.Close()

	p := testProvider(f.Server.URL)
	ctx := context.Background()

	models, err := p.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(models))
	}
	if models[0].ID != "llama3.2:3b" {
		t.Errorf("models[0].ID = %q, want llama3.2:3b", models[0].ID)
	}
	if models[0].Provider != "test_ollama" {
		t.Errorf("models[0].Provider = %q, want test_ollama", models[0].Provider)
	}
	// Second call should use cache.
	models2, _ := p.Models(ctx)
	if len(models2) != 3 {
		t.Errorf("cached len(models) = %d, want 3", len(models2))
	}
}

func TestOllamaStream(t *testing.T) {
	f := newFakeOllama("llama3.2:3b")
	defer f.Server.Close()

	p := testProvider(f.Server.URL)
	ctx := context.Background()

	var got strings.Builder
	out := &core.StreamOut{
		TextSink: func(s string) { got.WriteString(s) },
	}

	req := &core.ChatRequest{
		Model:    "llama3.2:3b",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Stream:   true,
	}

	done, err := p.Stream(ctx, req, out)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if done.FinishReason != "stop" {
		t.Errorf("done.FinishReason = %q, want stop", done.FinishReason)
	}
	if done.PromptTokens != 5 {
		t.Errorf("done.PromptTokens = %d, want 5", done.PromptTokens)
	}
	if done.CompletionTokens != 10 {
		t.Errorf("done.CompletionTokens = %d, want 10", done.CompletionTokens)
	}
	want := f.StreamText
	if got.String() != want {
		t.Errorf("streamed text = %q, want %q", got.String(), want)
	}
}

func TestOllamaStreamMultiple(t *testing.T) {
	f := newFakeOllama("llama3.2:3b")
	defer f.Server.Close()

	p := testProvider(f.Server.URL)
	ctx := context.Background()

	// Call twice: second should use the cached models.
	for i := 0; i < 2; i++ {
		var got strings.Builder
		out := &core.StreamOut{TextSink: func(s string) { got.WriteString(s) }}
		req := &core.ChatRequest{
			Model:    "llama3.2:3b",
			Messages: []core.Message{{Role: core.RoleUser, Content: fmt.Sprintf("call %d", i)}},
			Stream:   true,
		}
		if _, err := p.Stream(ctx, req, out); err != nil {
			t.Fatalf("Stream[%d]: %v", i, err)
		}
		if got.String() != f.StreamText {
			t.Errorf("Stream[%d] text = %q, want %q", i, got.String(), f.StreamText)
		}
	}
}

func TestOllamaStreamNoSink(t *testing.T) {
	f := newFakeOllama("llama3.2:3b")
	defer f.Server.Close()

	p := testProvider(f.Server.URL)
	ctx := context.Background()

	req := &core.ChatRequest{
		Model:    "llama3.2:3b",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hello"}},
		// stream=false, no sink → non-streaming call
	}
	done, err := p.Stream(ctx, req, nil)
	if err != nil {
		t.Fatalf("Stream(nil): %v", err)
	}
	if done.FinishReason != "stop" {
		t.Errorf("done.FinishReason = %q, want stop", done.FinishReason)
	}
}

func TestOllamaStreamToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl, _ := w.(http.Flusher)
		// Emit a tool-call chunk
		line, _ := json.Marshal(map[string]any{
			"model": "tool-model",
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{{
					"function": map[string]any{
						"name":      "get_weather",
						"arguments": map[string]any{"city": "Paris"},
					},
				}},
			},
			"done":        false,
			"done_reason": "tool_calls",
		})
		fmt.Fprintf(w, "%s\n", line)
		fl.Flush()
		// Done line
		done, _ := json.Marshal(map[string]any{
			"model":       "tool-model",
			"message":     map[string]string{"role": "assistant", "content": ""},
			"done":        true,
			"done_reason": "tool_calls",
		})
		fmt.Fprintf(w, "%s\n", done)
		fl.Flush()
	}))
	defer srv.Close()

	p := testProvider(srv.URL)
	ctx := context.Background()

	var toolCalls []core.ToolCall
	out := &core.StreamOut{
		TextSink: func(s string) {},
		ToolSink: func(tc core.ToolCall) { toolCalls = append(toolCalls, tc) },
	}
	req := &core.ChatRequest{
		Model:    "tool-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather in Paris?"}},
		Stream:   true,
	}
	done, err := p.Stream(ctx, req, out)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(toolCalls) < 1 {
		t.Fatal("expected at least one tool call")
	}
	if toolCalls[0].Function.Name != "get_weather" {
		t.Errorf("toolCalls[0].Function.Name = %q, want get_weather", toolCalls[0].Function.Name)
	}
	if done.FinishReason != "tool_calls" {
		t.Errorf("done.FinishReason = %q, want tool_calls", done.FinishReason)
	}
}

func TestOllamaUnavailableWhenServerDown(t *testing.T) {
	p := testProvider("http://127.0.0.1:1") // port 1 = almost certainly closed
	ctx := context.Background()
	av := p.Available(ctx)
	if av.OK {
		t.Error("expected OK=false for unreachable server")
	}
}

func TestOllamaStripBaseURLSuffixes(t *testing.T) {
	// Even if the config has /v1 or /api, we should strip them.
	cfg := config.Provider{Name: "test", Impl: "ollama", BaseURL: "http://localhost:11434/v1", Kind: "local"}
	p := New(cfg, 10*time.Second)
	if strings.HasSuffix(p.baseURL, "/v1") {
		t.Errorf("baseURL still has /v1: %q", p.baseURL)
	}
	if strings.HasSuffix(p.baseURL, "/api") {
		t.Errorf("baseURL still has /api: %q", p.baseURL)
	}
}
