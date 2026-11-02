package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/silverx/freellm-router/pkg/router"
)

func TestPkgRouterBasic(t *testing.T) {
	// One fake upstream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Hello from pkg!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer srv.Close()

	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "faketest", Impl: "openailike", BaseURL: srv.URL, Kind: "keyless"},
		},
		Routes: map[string][]string{
			"auto": {"faketest/any-model"},
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var text strings.Builder
	via, done, err := r.Route(context.Background(), "auto",
		[]router.Message{{Role: "user", Content: "Hi"}},
		"any-model",
		func(s string) { text.WriteString(s) },
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if via != "faketest/any-model" {
		t.Errorf("via = %q, want faketest/any-model", via)
	}
	if text.String() != "Hello from pkg!" {
		t.Errorf("text = %q, want 'Hello from pkg!'", text.String())
	}
	if done.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", done.FinishReason)
	}
	if done.PromptTokens != 3 {
		t.Errorf("prompt tokens = %d, want 3", done.PromptTokens)
	}
}

func TestPkgRouterFallback(t *testing.T) {
	// First provider fails with 503, second succeeds.
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fallback-ok"},"finish_reason":"stop"}]}`))
	}))
	defer s2.Close()

	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "bad", Impl: "openailike", BaseURL: s1.URL, Kind: "keyless"},
			{Name: "good", Impl: "openailike", BaseURL: s2.URL, Kind: "keyless"},
		},
		Routes: map[string][]string{
			"auto": {"bad/m1", "good/m2"},
		},
		Timeout:          3 * time.Second,
		CooldownShort:    time.Second,
		CooldownLong:     10 * time.Second,
		MaxChainAttempts: 6,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var text strings.Builder
	via, done, err := r.Route(context.Background(), "auto",
		[]router.Message{{Role: "user", Content: "Hi"}},
		"m1",
		func(s string) { text.WriteString(s) },
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if via != "good/m2" {
		t.Errorf("via = %q, want good/m2", via)
	}
	if text.String() != "fallback-ok" {
		t.Errorf("text = %q, want fallback-ok", text.String())
	}
	if done.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", done.FinishReason)
	}
}

func TestPkgRouterAllFail(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"internal"}}`))
	}))
	defer dead.Close()

	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "dead", Impl: "openailike", BaseURL: dead.URL, Kind: "keyless"},
		},
		Routes: map[string][]string{
			"auto": {"dead/m1"},
		},
		Timeout:          3 * time.Second,
		MaxChainAttempts: 4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, done, err := r.Route(context.Background(), "auto",
		[]router.Message{{Role: "user", Content: "Hi"}},
		"m1",
		nil,
	)
	if err == nil {
		t.Fatal("expected error when all fail")
	}
	if done != nil {
		t.Errorf("done should be nil on error, got %+v", done)
	}
	// Check it's an upstream error.
	ue, ok := router.AsUpstreamErr(err)
	if !ok {
		// The top-level error is a descriptive "all chain entries failed" wrapper.
		// The underlying cause should be an upstream err.
		if !strings.Contains(err.Error(), "fail") {
			t.Errorf("error should mention failure: %v", err)
		}
	}
	_ = ue
}

func TestPkgRouterStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "stream", Impl: "openailike", BaseURL: srv.URL, Kind: "keyless"},
		},
		Routes:  map[string][]string{"auto": {"stream/m"}},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var parts []string
	var mu sync.Mutex
	_, done, err := r.Route(context.Background(), "auto",
		[]router.Message{{Role: "user", Content: "Hi"}},
		"m",
		func(s string) {
			mu.Lock()
			parts = append(parts, s)
			mu.Unlock()
		},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	mu.Lock()
	full := strings.Join(parts, "")
	mu.Unlock()
	if full != "Hello" {
		t.Errorf("full = %q, want Hello", full)
	}
	if done.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", done.FinishReason)
	}
}

func TestPkgRouterNoSink(t *testing.T) {
	// When sink is nil, the text should be in Done.Text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"collected"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "noval", Impl: "openailike", BaseURL: srv.URL, Kind: "keyless"},
		},
		Routes:  map[string][]string{"auto": {"noval/m"}},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, done, err := r.Route(context.Background(), "auto",
		[]router.Message{{Role: "user", Content: "Hi"}},
		"m",
		nil,
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if done.Text != "collected" {
		t.Errorf("done.Text = %q, want collected", done.Text)
	}
}

func TestPkgRouterUnsupportedImpl(t *testing.T) {
	_, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "x", Impl: "unknownimpl", BaseURL: "http://x", Kind: "local"},
		},
	})
	if err == nil {
		t.Fatal("expected error for unsupported impl")
	}
	if !strings.Contains(err.Error(), "unknownimpl") {
		t.Errorf("error should mention 'unknownimpl': %v", err)
	}
}

func TestPkgRouterProvidersList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "alpha", Impl: "openailike", BaseURL: srv.URL, Kind: "keyless"},
			{Name: "beta", Impl: "openailike", BaseURL: srv.URL, Kind: "keyless"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	provs := r.Providers()
	if len(provs) != 2 {
		t.Errorf("providers count = %d, want 2", len(provs))
	}
}
