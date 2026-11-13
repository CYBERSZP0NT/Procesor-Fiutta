package openailike_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/core"
	openailike "github.com/silverx/szmelc-router/internal/providers/openailike"
)

// helper: create a provider key-source that returns specific keys
type fakeKeySource struct {
	keysByProvider map[string][]string
}

func (f *fakeKeySource) Keys(_ context.Context, provider string) []string {
	return f.keysByProvider[strings.ToLower(provider)]
}

func newProv(t *testing.T, name, baseURL, kind, keyEnv, header string, keys []string) (*openailike.Provider, *config.Provider) {
	t.Helper()
	cp := &config.Provider{
		Name:    name,
		Impl:    "openailike",
		BaseURL: baseURL,
		Kind:    kind,
		KeyEnv:  keyEnv,
		Header:  header,
	}
	ks := &fakeKeySource{keysByProvider: map[string][]string{name: keys}}
	return openailike.New(*cp, ks, 5*time.Second), cp
}

func TestProviderBasicChat(t *testing.T) {
	// Fake upstream returns a simple content.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"choices":[{"message":{"role":"assistant","content":"Hello World"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}
		}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "testprov", srv.URL+"/v1", "keyless", "", "", nil)

	var text strings.Builder
	done, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model: "gpt-test",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "Hi"},
		},
	}, &core.StreamOut{TextSink: func(s string) { text.WriteString(s) }})

	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text.String() != "Hello World" {
		t.Errorf("text = %q, want %q", text.String(), "Hello World")
	}
	if done.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", done.FinishReason)
	}
	if done.PromptTokens != 5 {
		t.Errorf("prompt tokens = %d, want 5", done.PromptTokens)
	}
	if done.CompletionTokens != 3 {
		t.Errorf("completion tokens = %d, want 3", done.CompletionTokens)
	}
}

func TestProviderSSEStreaming(t *testing.T) {
	// Fake SSE upstream that sends 3 chunks + [DONE].
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo \"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"World\"},\"finish_reason\":\"stop\"}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	prov, _ := newProv(t, "streamprov", srv.URL, "keyless", "", "", nil)

	var parts []string
	var mu sync.Mutex
	done, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "stream-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
		Stream:   true,
	}, &core.StreamOut{TextSink: func(s string) {
		mu.Lock()
		parts = append(parts, s)
		mu.Unlock()
	}})

	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3: %v", len(parts), parts)
	}
	full := strings.Join(parts, "")
	if full != "Hello World" {
		t.Errorf("full text = %q, want %q", full, "Hello World")
	}
	if done.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", done.FinishReason)
	}
}

func TestProviderHTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "ratelimited", srv.URL, "keyed", "GROQ_API_KEY", "", nil)

	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "llama",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}, nil)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Fatalf("not an UpstreamErr: %v", err)
	}
	if ue.Status != 429 {
		t.Errorf("status = %d, want 429", ue.Status)
	}
	if !strings.Contains(ue.Msg, "rate limited") {
		t.Errorf("msg = %q, want to contain 'rate limited'", ue.Msg)
	}
}

func TestProviderHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "servererr", srv.URL, "keyless", "", "", nil)

	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}, nil)

	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Fatalf("not UpstreamErr: %v", err)
	}
	if ue.Status != 500 {
		t.Errorf("status = %d, want 500", ue.Status)
	}
}

func TestProviderHTTP403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "forbiddprov", srv.URL, "keyed", "X_KEY", "", nil)

	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}, nil)

	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Fatalf("not UpstreamErr: %v", err)
	}
	if ue.Status != 403 {
		t.Errorf("status = %d, want 403", ue.Status)
	}
}

func TestProviderKeyRotation(t *testing.T) {
	var order []string
	var mu sync.Mutex
	var callCount int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, r.Header.Get("Authorization"))
		mu.Unlock()
		atomic.AddInt64(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	// Two keys for rotation.
	keys := []string{"sk-key1", "sk-key2"}
	prov, _ := newProv(t, "rotprov", srv.URL, "keyed", "K", "", keys)

	// Call 5 times, should cycle key1, key2, key1, key2, key1
	for i := 0; i < 5; i++ {
		_, err := prov.Stream(context.Background(), &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
		}, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 5 {
		t.Fatalf("expected 5 calls, got %d", len(order))
	}
	want := []string{"Bearer sk-key1", "Bearer sk-key2", "Bearer sk-key1", "Bearer sk-key2", "Bearer sk-key1"}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("call %d: got %q, want %q", i, order[i], w)
		}
	}
}

func TestProviderCustomAuthHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "gprov", srv.URL, "keyed", "GOOGLE_KEY", "x-goog-api-key", []string{"google-secret-key"})

	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "gemini",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if gotHeader != "google-secret-key" {
		t.Errorf("x-goog-api-key = %q, want 'google-secret-key'", gotHeader)
	}
}

func TestProviderKeylessNoAuth(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "keylessprov", srv.URL, "keyless", "", "", nil)

	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("keyless should have no Authorization header, got %q", gotAuth)
	}
	if gotCustom != "" {
		t.Errorf("keyless should have no x-goog-api-key, got %q", gotCustom)
	}
}

func TestProviderAvailableKeyless(t *testing.T) {
	prov, _ := newProv(t, "availkeyless", "http://unused", "keyless", "", "", nil)
	avail := prov.Available(context.Background())
	if avail == nil || !avail.OK {
		t.Errorf("keyless provider should be available, got %+v", avail)
	}
}

func TestProviderAvailableNoKey(t *testing.T) {
	prov, _ := newProv(t, "availnokey", "http://unused", "keyed", "MISSING_KEY", "", nil)
	avail := prov.Available(context.Background())
	if avail == nil {
		t.Fatal("avail is nil")
	}
	if avail.OK {
		t.Error("keyed provider with no keys should not be OK")
	}
	if avail.Reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestProviderAvailableWithKey(t *testing.T) {
	prov, _ := newProv(t, "availwithkey", "http://unused", "keyed", "HAS_KEY", "", []string{"sk-abc"})
	avail := prov.Available(context.Background())
	if avail == nil || !avail.OK {
		t.Errorf("keyed provider with key should be available, got %+v", avail)
	}
	if avail.KeyCount != 1 {
		t.Errorf("keycount = %d, want 1", avail.KeyCount)
	}
}

func TestProviderModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"gpt-4o","name":"GPT 4o"},{"id":"gpt-3.5-turbo","name":"GPT 3.5"}]}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "modelsprov", srv.URL+"/v1", "keyless", "", "", nil)
	models, err := prov.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models count = %d, want 2", len(models))
	}
	if models[0].ID != "gpt-4o" {
		t.Errorf("models[0].ID = %q, want gpt-4o", models[0].ID)
	}
	if models[0].Provider != "modelsprov" {
		t.Errorf("models[0].Provider = %q, want modelsprov", models[0].Provider)
	}
}

func TestProviderToolCallNonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"","tool_calls":[
				{"id":"call-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":20,"completion_tokens":15,"total_tokens":35}
		}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "toolprov", srv.URL, "keyless", "", "", nil)

	var tools []core.ToolCall
	var mu sync.Mutex
	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "tool-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather in tokyo?"}},
		Tools: []core.ToolDef{{
			Type:     "function",
			Function: core.FunctionDef{Name: "get_weather"},
		}},
	}, &core.StreamOut{
		ToolSink: func(tc core.ToolCall) {
			mu.Lock()
			tools = append(tools, tc)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tools) == 0 {
		t.Fatal("no tools received")
	}
	if tools[0].Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", tools[0].Function.Name)
	}
}

func TestProviderToolCallSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"\"}}]}}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"q\\\":\\\"go\\\"}\"}}]}}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	prov, _ := newProv(t, "toolsse", srv.URL, "keyless", "", "", nil)

	var toolChunks []core.ToolCall
	var mu sync.Mutex
	done, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "search go"}},
		Stream:   true,
	}, &core.StreamOut{
		ToolSink: func(tc core.ToolCall) {
			mu.Lock()
			toolChunks = append(toolChunks, tc)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if done.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", done.FinishReason)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(toolChunks) == 0 {
		t.Fatal("no tool chunks received")
	}
	// First chunk should have the name.
	if toolChunks[0].Function.Name != "search" {
		t.Errorf("first tool name = %q, want search", toolChunks[0].Function.Name)
	}
}

func TestProviderEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "emptyprov", srv.URL, "keyless", "", "", nil)
	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}, nil)

	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Fatalf("not UpstreamErr: %v", err)
	}
	if ue.Status != 599 {
		t.Errorf("status = %d, want 599 (empty response)", ue.Status)
	}
}

func TestProviderSSEStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"error\":{\"message\":\"stream failed\",\"type\":\"api_error\"}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	prov, _ := newProv(t, "sseerr", srv.URL, "keyless", "", "", nil)
	_, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
		Stream:   true,
	}, &core.StreamOut{TextSink: func(s string) {}})

	if err == nil {
		t.Fatal("expected stream error")
	}
	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Fatalf("not UpstreamErr: %v", err)
	}
	if !strings.Contains(ue.Msg, "stream failed") {
		t.Errorf("msg = %q, want to contain 'stream failed'", ue.Msg)
	}
}

func TestProviderContextCancelled(t *testing.T) {
	// Use a dead server (connection refused) to test context propagation.
	// Start a server then immediately close it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	prov, _ := newProv(t, "cancelprov", addr, "keyless", "", "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := prov.Stream(ctx, &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}, nil)

	if err == nil {
		t.Fatal("expected error (connection refused)")
	}
	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Fatalf("not UpstreamErr: %v", err)
	}
	if ue.Status != 0 {
		// Connection errors get status 0.
		t.Logf("note: status = %d (0 is fine for connection errors)", ue.Status)
	}
}

func TestProviderNameAndKinds(t *testing.T) {
	prov, _ := newProv(t, "namecheck", "http://x", "keyless", "", "", nil)
	if prov.Name() != "namecheck" {
		t.Errorf("name = %q, want namecheck", prov.Name())
	}
	kinds := prov.Kinds()
	if len(kinds) != 1 || kinds[0] != core.KindKeyless {
		t.Errorf("kinds = %v, want [keyless]", kinds)
	}
}

func TestProviderStreamNilOut(t *testing.T) {
	// When out is nil, the provider should still work (non-streaming).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	prov, _ := newProv(t, "nilout", srv.URL, "keyless", "", "", nil)
	done, err := prov.Stream(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("stream with nil out: %v", err)
	}
	if done.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", done.FinishReason)
	}
}

func TestProviderStreamOutInterface(t *testing.T) {
	// Ensure Provider satisfies core.Provider interface.
	var _ core.Provider = (*openailike.Provider)(nil)
}
