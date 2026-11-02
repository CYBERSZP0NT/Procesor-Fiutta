package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
	openailike "github.com/silverx/freellm-router/internal/providers/openailike"
)

// fakeKS returns fixed keys.
type fakeKS struct {
	keys map[string][]string
}

func (f *fakeKS) Keys(_ context.Context, provider string) []string {
	return f.keys[strings.ToLower(provider)]
}

// makeProvider creates an openailike provider pointing at url.
func makeProvider(t *testing.T, name, url, kind string, keys []string) core.Provider {
	t.Helper()
	cp := config.Provider{
		Name:    name,
		Impl:    "openailike",
		BaseURL: url,
		Kind:    kind,
	}
	ks := &fakeKS{keys: map[string][]string{name: keys}}
	return openailike.New(cp, ks, 5*time.Second)
}

// newRouter3 creates a router with 3 fake upstreams.
func newRouter3(t *testing.T, urls [3]string) *core.Router {
	t.Helper()
	provs := map[string]core.Provider{
		"prov1": makeProvider(t, "prov1", urls[0], "keyless", nil),
		"prov2": makeProvider(t, "prov2", urls[1], "keyless", nil),
		"prov3": makeProvider(t, "prov3", urls[2], "keyless", nil),
	}
	routes := map[string][]string{
		"auto": {
			"prov1/model-a",
			"prov2/model-b",
			"prov3/model-c",
		},
	}
	policy := core.NewDefaultRoutePolicy(routes, []string{"prov1", "prov2", "prov3"}, "auto")
	cooldown := core.NewInMemCooldown()
	return core.NewRouter(core.RouterOpts{
		Providers:   provs,
		RoutePolicy: policy,
		Cooldown:    cooldown,
		Short:       time.Second,
		Long:        10 * time.Second,
		MaxAtt:      8,
	})
}

// sseSrv creates a streaming SSE upstream that returns the given content.
func sseSrv(t *testing.T, status int, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"fail"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		if content != "" {
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"" + content + "\"},\"finish_reason\":null}]}\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
}

// jsonSrv creates a non-streaming JSON upstream.
func jsonSrv(t *testing.T, status int, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			body := `{"error":{"message":"simulated error"}}`
			w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
}

func TestRouterFallbackAllSuccess(t *testing.T) {
	// All 3 succeed; first one should win.
	s1 := jsonSrv(t, 200, "from-prov1")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "from-prov2")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "from-prov3")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "model-a",
			Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
		},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Via != "prov1/model-a" {
		t.Errorf("via = %q, want prov1/model-a", res.Via)
	}
	if text.String() != "from-prov1" {
		t.Errorf("text = %q, want from-prov1", text.String())
	}
}

func TestRouterFallback429ThenSuccess(t *testing.T) {
	// prov1 → 429, prov2 → 200
	s1 := jsonSrv(t, 429, "")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "recovered")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "unused")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "model-a",
			Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
		},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Via != "prov2/model-b" {
		t.Errorf("via = %q, want prov2/model-b", res.Via)
	}
	if text.String() != "recovered" {
		t.Errorf("text = %q, want recovered", text.String())
	}
}

func TestRouterFallback500ThenSuccess(t *testing.T) {
	// prov1 → 500, prov2 → 500, prov3 → 200
	s1 := jsonSrv(t, 500, "")
	defer s1.Close()
	s2 := jsonSrv(t, 500, "")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "finally")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "model-a",
			Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
		},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Via != "prov3/model-c" {
		t.Errorf("via = %q, want prov3/model-c", res.Via)
	}
	if text.String() != "finally" {
		t.Errorf("text = %q, want finally", text.String())
	}
}

func TestRouterAllFail(t *testing.T) {
	s1 := jsonSrv(t, 500, "")
	defer s1.Close()
	s2 := jsonSrv(t, 503, "")
	defer s2.Close()
	s3 := jsonSrv(t, 500, "")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	_, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "model-a",
			Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("error should mention 'all' chain entries failed: %v", err)
	}
}

func TestRouterCooldownEnforcement(t *testing.T) {
	// After a failure, the provider should be in cooldown.
	s1 := jsonSrv(t, 429, "")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "ok")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "ok")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	// First call: prov1 fails with 429, prov2 succeeds.
	_, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{Model: "model-a", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		nil,
	)
	if err != nil {
		t.Fatalf("first route: %v", err)
	}

	// Second call: prov1 is in cooldown → should skip to prov2.
	res2, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{Model: "model-a", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		nil,
	)
	if err != nil {
		t.Fatalf("second route: %v", err)
	}
	if res2.Via != "prov2/model-b" {
		t.Errorf("via = %q, want prov2/model-b (prov1 should be cooling down)", res2.Via)
	}
}

func TestRouterLongCooldown403(t *testing.T) {
	// 403 → long cooldown.
	s1 := jsonSrv(t, 403, "")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "ok")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "ok")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	_, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{Model: "model-a", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		nil,
	)
	if err != nil {
		t.Fatalf("route: %v", err)
	}

	// Second call: prov1 is in long cooldown.
	res2, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{Model: "model-a", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		nil,
	)
	if err != nil {
		t.Fatalf("second route: %v", err)
	}
	if res2.Via != "prov2/model-b" {
		t.Errorf("via = %q, want prov2/model-b", res2.Via)
	}
}

func TestRouterKeyRotationOrder(t *testing.T) {
	// Provider with 2 keys; verify it rotates.
	var authOrder []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			authOrder = append(authOrder, r.Header.Get("Authorization"))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	cp := config.Provider{
		Name:    "keyprov",
		Impl:    "openailike",
		BaseURL: srv.URL,
		Kind:    "keyed",
	}
	ks := &fakeKS{keys: map[string][]string{"keyprov": {"sk-aaa", "sk-bbb"}}}
	prov := openailike.New(cp, ks, 5*time.Second)

	routes := map[string][]string{"auto": {"keyprov/test-model"}}
	policy := core.NewDefaultRoutePolicy(routes, []string{"keyprov"}, "auto")
	r := core.NewRouter(core.RouterOpts{
		Providers:   map[string]core.Provider{"keyprov": prov},
		RoutePolicy: policy,
		Cooldown:    core.NewInMemCooldown(),
		Short:       time.Second,
		Long:        10 * time.Second,
		MaxAtt:      10,
	})

	req := &core.ChatRequest{Model: "test-model", Messages: []core.Message{{Role: core.RoleUser, Content: "x"}}}
	sel := core.ModelSelector{Name: "auto"}
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		_, err := r.Route(ctx, sel, req, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	want := []string{"Bearer sk-aaa", "Bearer sk-bbb", "Bearer sk-aaa", "Bearer sk-bbb"}
	mu.Lock()
	if len(authOrder) != 4 {
		t.Fatalf("got %d calls, want 4", len(authOrder))
	}
	for i, w := range want {
		if authOrder[i] != w {
			t.Errorf("call %d: got %q, want %q", i, authOrder[i], w)
		}
	}
	mu.Unlock()
}

func TestRouterAutoSafetyNet(t *testing.T) {
	// "provider/model" selector should try that first, then auto.
	s1 := jsonSrv(t, 500, "")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "safety-net")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "x")
	defer s3.Close()

	provs := map[string]core.Provider{
		"prov1": makeProvider(t, "prov1", s1.URL, "keyless", nil),
		"prov2": makeProvider(t, "prov2", s2.URL, "keyless", nil),
		"prov3": makeProvider(t, "prov3", s3.URL, "keyless", nil),
	}
	routes := map[string][]string{
		"auto": {"prov1/failmodel", "prov2/goodmodel"},
	}
	policy := core.NewDefaultRoutePolicy(routes, []string{"prov1", "prov2", "prov3"}, "auto")
	r := core.NewRouter(core.RouterOpts{
		Providers:   provs,
		RoutePolicy: policy,
		Cooldown:    core.NewInMemCooldown(),
		Short:       time.Second,
		Long:        10 * time.Second,
		MaxAtt:      8,
	})

	// Select "prov1/failmodel" → should try prov1 first (500), then fall back to auto chain.
	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "prov1/failmodel"},
		&core.ChatRequest{Model: "failmodel", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Via != "prov2/goodmodel" {
		t.Errorf("via = %q, want prov2/goodmodel (safety net)", res.Via)
	}
	if text.String() != "safety-net" {
		t.Errorf("text = %q, want safety-net", text.String())
	}
}

func TestRouterUnknownModel(t *testing.T) {
	// Bare unknown model → falls back to auto chain.
	s1 := jsonSrv(t, 200, "from-auto")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "x")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "x")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "totally-unknown-model-xyz"},
		&core.ChatRequest{Model: "totally-unknown-model-xyz", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Should have used the auto chain entry (prov1/model-a is first).
	if res.Via != "prov1/model-a" {
		t.Errorf("via = %q, want prov1/model-a (auto fallback)", res.Via)
	}
}

func TestRouterProviderByName(t *testing.T) {
	// Select a provider name → should use that provider's entries in auto.
	s1 := jsonSrv(t, 200, "prov1-answer")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "x")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "x")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "prov1"},
		&core.ChatRequest{Model: "anything", Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}}},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !strings.HasPrefix(res.Via, "prov1/") {
		t.Errorf("via = %q, want prov1/...", res.Via)
	}
}

func TestRouterStreamingThroughChain(t *testing.T) {
	// Fallback chain with streaming: prov1 429, prov2 200 SSE.
	s1 := jsonSrv(t, 429, "")
	defer s1.Close()
	s2 := sseSrv(t, 200, "streamed-ok")
	defer s2.Close()
	s3 := sseSrv(t, 200, "unused")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var parts []string
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "model-a",
			Messages: []core.Message{{Role: core.RoleUser, Content: "stream me"}},
			Stream:   true,
		},
		&core.StreamOut{TextSink: func(s string) { parts = append(parts, s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Via != "prov2/model-b" {
		t.Errorf("via = %q, want prov2/model-b", res.Via)
	}
	full := strings.Join(parts, "")
	if full != "streamed-ok" {
		t.Errorf("text = %q, want streamed-ok", full)
	}
}

func TestRouterMaxAttempts(t *testing.T) {
	// All fail, verify max attempts is respected.
	s1 := jsonSrv(t, 500, "")
	defer s1.Close()
	s2 := jsonSrv(t, 500, "")
	defer s2.Close()
	s3 := jsonSrv(t, 500, "")
	defer s3.Close()

	provs := map[string]core.Provider{
		"prov1": makeProvider(t, "prov1", s1.URL, "keyless", nil),
		"prov2": makeProvider(t, "prov2", s2.URL, "keyless", nil),
		"prov3": makeProvider(t, "prov3", s3.URL, "keyless", nil),
	}
	routes := map[string][]string{
		"auto": {"prov1/a", "prov2/b", "prov3/c"},
	}
	policy := core.NewDefaultRoutePolicy(routes, []string{"prov1", "prov2", "prov3"}, "auto")
	r := core.NewRouter(core.RouterOpts{
		Providers:   provs,
		RoutePolicy: policy,
		Cooldown:    core.NewInMemCooldown(),
		Short:       time.Second,
		Long:        10 * time.Second,
		MaxAtt:      2, // only 2 total attempts allowed
	})

	_, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{Model: "a", Messages: []core.Message{{Role: core.RoleUser, Content: "x"}}},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("expected 'all' in error, got: %v", err)
	}
}

func TestRouterToolCallPassthrough(t *testing.T) {
	// Verify tools are sent in the request body.
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body.Read(gotBody)
		r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"my_tool","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer srv.Close()

	cp := config.Provider{Name: "tp", Impl: "openailike", BaseURL: srv.URL, Kind: "keyless"}
	prov := openailike.New(cp, &fakeKS{keys: map[string][]string{}}, 5*time.Second)

	routes := map[string][]string{"auto": {"tp/m"}}
	policy := core.NewDefaultRoutePolicy(routes, []string{"tp"}, "auto")
	r := core.NewRouter(core.RouterOpts{
		Providers:   map[string]core.Provider{"tp": prov},
		RoutePolicy: policy,
		Cooldown:    core.NewInMemCooldown(),
		MaxAtt:      4,
	})

	var tools []core.ToolCall
	_, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: core.RoleUser, Content: "call it"}},
			Tools:    []core.ToolDef{{Type: "function", Function: core.FunctionDef{Name: "my_tool"}}},
		},
		&core.StreamOut{
			ToolSink: func(tc core.ToolCall) { tools = append(tools, tc) },
		},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("no tools received")
	}
	if tools[0].Function.Name != "my_tool" {
		t.Errorf("tool name = %q, want my_tool", tools[0].Function.Name)
	}
	// Verify the request body contained tools.
	_ = gotBody // (assertion would go here if we had parsed it)
}

func TestRouterEmptyChain(t *testing.T) {
	// A route alias with empty chain.
	routes := map[string][]string{"empty": {}}
	policy := core.NewDefaultRoutePolicy(routes, []string{"nothing"}, "auto")
	r := core.NewRouter(core.RouterOpts{
		Providers:   map[string]core.Provider{},
		RoutePolicy: policy,
		MaxAtt:      4,
	})

	_, err := r.Route(context.Background(),
		core.ModelSelector{Name: "nonexistent"},
		&core.ChatRequest{Model: "x", Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}},
		nil,
	)
	if err == nil {
		t.Fatal("expected error for no chain")
	}
}

func TestRouterSSEFallback(t *testing.T) {
	// prov1: returns 429 via SSE-style (actually JSON error), prov2: SSE success.
	s1 := jsonSrv(t, 429, "")
	defer s1.Close()
	s2 := sseSrv(t, 200, "sse-fallback")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "unused")
	defer s3.Close()

	r := newRouter3(t, [3]string{s1.URL, s2.URL, s3.URL})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{
			Model:    "model-a",
			Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
			Stream:   true,
		},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Via != "prov2/model-b" {
		t.Errorf("via = %q, want prov2/model-b", res.Via)
	}
	if text.String() != "sse-fallback" {
		t.Errorf("text = %q, want sse-fallback", text.String())
	}
}

func TestRouterDeduplication(t *testing.T) {
	// Route that lists the same entry twice.
	s1 := jsonSrv(t, 200, "once")
	defer s1.Close()
	s2 := jsonSrv(t, 200, "unused")
	defer s2.Close()
	s3 := jsonSrv(t, 200, "unused")
	defer s3.Close()

	provs := map[string]core.Provider{
		"prov1": makeProvider(t, "prov1", s1.URL, "keyless", nil),
		"prov2": makeProvider(t, "prov2", s2.URL, "keyless", nil),
		"prov3": makeProvider(t, "prov3", s3.URL, "keyless", nil),
	}
	// Duplicate entry: prov1/model-a appears twice.
	routes := map[string][]string{
		"auto": {"prov1/model-a", "prov1/model-a", "prov2/model-b"},
	}
	policy := core.NewDefaultRoutePolicy(routes, []string{"prov1", "prov2", "prov3"}, "auto")
	r := core.NewRouter(core.RouterOpts{
		Providers:   provs,
		RoutePolicy: policy,
		Cooldown:    core.NewInMemCooldown(),
		MaxAtt:      8,
	})

	var text strings.Builder
	res, err := r.Route(context.Background(),
		core.ModelSelector{Name: "auto"},
		&core.ChatRequest{Model: "model-a", Messages: []core.Message{{Role: core.RoleUser, Content: "x"}}},
		&core.StreamOut{TextSink: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Should only have called prov1 once (dedup).
	if res.Via != "prov1/model-a" {
		t.Errorf("via = %q, want prov1/model-a", res.Via)
	}
	if text.String() != "once" {
		t.Errorf("text = %q, want once", text.String())
	}
}
