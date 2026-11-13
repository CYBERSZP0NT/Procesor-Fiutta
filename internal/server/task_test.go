package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/silverx/szmelc-router/internal/agentic"
	"github.com/silverx/szmelc-router/internal/config"
)

// newTaskTestServer serves the router (3-entry task catalog over one fake
// upstream that echoes the requested model), pinned to Jako Tako, and
// returns its base URL. newTaskTestServerMode keeps any mode ("" = default).
func newTaskTestServer(t *testing.T) string {
	t.Helper()
	return newTaskTestServerMode(t, "jako-tako")
}

func newTaskTestServerMode(t *testing.T, mode string) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"choices":[{"delta":{"content":"from `+q.Model+`"}}]}`+"\n\n")
			io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from `+q.Model+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{
		Providers: []config.Provider{{Name: "fake", Impl: "openailike", BaseURL: up.URL, Kind: "keyless"}},
		Routes:    map[string][]string{"auto": {"fake/mid"}},
		Policies:  config.PolicyConfig{CooldownShortSec: 1, CooldownLongSec: 1, MaxChainAttempts: 8},
		Agentic: agentic.Spec{Mode: mode, Catalog: []agentic.Entry{
			{ID: "tiny", Model: "fake/tiny", Tier: 1, Speed: 3},
			{ID: "mid", Model: "fake/mid", Tier: 3, Speed: 2},
			{ID: "big", Model: "fake/big", Tier: 5, Speed: 1},
		}},
		Logging: config.LoggingConfig{Level: "error"},
	}
	s, err := New(cfg, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func post(t *testing.T, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestChatWithTaskAlias(t *testing.T) {
	base := newTaskTestServer(t)
	for _, stream := range []bool{false, true} {
		body := `{"model":"agent","stream":` + map[bool]string{false: "false", true: "true"}[stream] +
			`,"messages":[{"role":"user","content":"hi"}]}`
		resp, out := post(t, base+"/v1/chat/completions", body, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("stream=%v: %d %s", stream, resp.StatusCode, out)
		}
		if got := resp.Header.Get("X-Freellm-Routed-Via"); got != "fake/tiny" {
			t.Errorf("stream=%v: routed via %q", stream, got)
		}
		if h := resp.Header.Get("X-Freellm-Task"); !strings.Contains(h, "pick=fake/tiny") || !strings.Contains(h, "tier=1") {
			t.Errorf("stream=%v: X-Freellm-Task %q", stream, h)
		}
		if resp.Header.Get("X-Freellm-Task-Reason") == "" || !strings.Contains(out, "from tiny") {
			t.Errorf("stream=%v: reason header missing or body %q", stream, out)
		}
	}
}

func TestChatTaskHeaderOverride(t *testing.T) {
	base := newTaskTestServer(t)
	resp, out := post(t, base+"/v1/chat/completions",
		`{"model":"agent","messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Freellm-Task": "plan:5"})
	if resp.StatusCode != 200 || resp.Header.Get("X-Freellm-Routed-Via") != "fake/big" {
		t.Fatalf("%d via %q: %s", resp.StatusCode, resp.Header.Get("X-Freellm-Routed-Via"), out)
	}
}

func TestRoutePreviewAndCatalog(t *testing.T) {
	base := newTaskTestServer(t)
	resp, out := post(t, base+"/v1/route",
		`{"model":"agent:code:3","messages":[{"role":"user","content":"x"}]}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	var d struct {
		Required int `json:"required"`
		Winner   struct {
			ID string `json:"id"`
		} `json:"winner"`
		Need struct {
			Source string `json:"source"`
		} `json:"need"`
		Chain []string `json:"chain"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	if d.Required != 4 || d.Winner.ID != "big" || d.Need.Source != "model" || len(d.Chain) != 3 {
		t.Fatalf("preview %+v", d)
	}

	if resp, out := post(t, base+"/v1/route", `{"model":"auto","messages":[]}`, nil); resp.StatusCode != 400 {
		t.Fatalf("non-alias model: %d %s", resp.StatusCode, out)
	}

	r, err := http.Get(base + "/v1/route")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var cat struct {
		Aliases []string `json:"aliases"`
		Catalog []struct {
			ID        string `json:"id"`
			Where     string `json:"where"`
			Available string `json:"available"`
		} `json:"catalog"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cat); err != nil {
		t.Fatal(err)
	}
	if len(cat.Catalog) != 3 || cat.Catalog[0].Available != "ok" || cat.Catalog[0].Where != "keyless" || len(cat.Aliases) != 2 {
		t.Fatalf("catalog %+v", cat)
	}
}

func TestModelsListTaskAliases(t *testing.T) {
	base := newTaskTestServer(t)
	r, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), `"id":"agent"`) || !strings.Contains(string(b), "szmelc-router/agentic") {
		t.Fatalf("models: %s", b)
	}
}

func TestNonStreamBodyIsPlainJSON(t *testing.T) {
	base := newTaskTestServer(t)
	resp, out := post(t, base+"/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != 200 || strings.HasPrefix(out, "data:") || strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		t.Fatalf("non-stream response leaked SSE: %s %q", resp.Header.Get("Content-Type"), out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v: %q", err, out)
	}
}

func TestDefaultModeAndRuntimeSwitch(t *testing.T) {
	base := newTaskTestServerMode(t, "")
	hi := `{"model":"agent","messages":[{"role":"user","content":"hi"}]}`

	resp, _ := post(t, base+"/v1/chat/completions", hi, nil)
	if resp.Header.Get("X-Freellm-Routed-Via") != "fake/big" || !strings.Contains(resp.Header.Get("X-Freellm-Task"), "mode=rozklad-jazdy") {
		t.Fatalf("default must be [Rozkład Jazdy]: %v", resp.Header)
	}
	// Per request via header.
	resp, _ = post(t, base+"/v1/chat/completions", hi, map[string]string{"X-Freellm-Task-Mode": "Jako Tako"})
	if resp.Header.Get("X-Freellm-Routed-Via") != "fake/tiny" {
		t.Fatalf("X-Freellm-Task-Mode ignored: %v", resp.Header)
	}
	// Runtime switch.
	req, _ := http.NewRequest(http.MethodPut, base+"/v1/route/mode", strings.NewReader(`{"mode":"jako tako"}`))
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(b), `"mode":"jako-tako"`) {
		t.Fatalf("PUT /v1/route/mode: %d %s", r.StatusCode, b)
	}
	resp, _ = post(t, base+"/v1/chat/completions", hi, nil)
	if resp.Header.Get("X-Freellm-Routed-Via") != "fake/tiny" {
		t.Fatalf("runtime switch ignored: %v", resp.Header)
	}
	if resp, out := post(t, base+"/v1/route/mode", `{"mode":"turbo"}`, nil); resp.StatusCode != 400 {
		t.Fatalf("unknown mode: %d %s", resp.StatusCode, out)
	}
	g, err := http.Get(base + "/v1/route/mode")
	if err != nil {
		t.Fatal(err)
	}
	gb, _ := io.ReadAll(g.Body)
	g.Body.Close()
	if !strings.Contains(string(gb), `"mode":"jako-tako"`) || !strings.Contains(string(gb), "[Rozkład Jazdy]") {
		t.Fatalf("GET /v1/route/mode: %s", gb)
	}
}
