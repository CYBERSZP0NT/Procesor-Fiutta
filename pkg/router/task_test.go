package router_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/silverx/freellm-router/pkg/router"
)

// fakeUpstream answers with the requested model name, except models listed
// in empty, which answer with an empty message. It records requested models.
func fakeUpstream(t *testing.T, empty ...string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		mu.Lock()
		seen = append(seen, q.Model)
		mu.Unlock()
		content := "answer from " + q.Model
		for _, e := range empty {
			if e == q.Model {
				content = ""
			}
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

// taskRouter builds a 3-entry task router pinned to Jako Tako (the tests
// below are about tiers); taskRouterMode keeps any mode ("" = default).
func taskRouter(t *testing.T, url string, routes map[string][]string) *router.Router {
	t.Helper()
	return taskRouterMode(t, url, routes, "jako-tako")
}

func taskRouterMode(t *testing.T, url string, routes map[string][]string, mode string) *router.Router {
	t.Helper()
	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{{Name: "fake", Impl: "openailike", BaseURL: url, Kind: "keyless"}},
		Routes:    routes,
		Timeout:   5 * time.Second,
		Agentic: &router.TaskSpec{Mode: mode, Catalog: []router.TaskEntry{
			{ID: "tiny", Model: "fake/tiny", Tier: 1, Speed: 3},
			{ID: "mid", Model: "fake/mid", Tier: 3, Speed: 2},
			{ID: "big", Model: "fake/big", Tier: 5, Speed: 1, GoodAt: []string{"plan"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTaskRouterPicksJustEnough(t *testing.T) {
	srv, seen := fakeUpstream(t)
	r := taskRouter(t, srv.URL, map[string][]string{"auto": {"fake/mid"}})

	var last *router.TaskDecision
	via, done, err := r.RouteRaw(context.Background(), "agent",
		[]router.Message{{Role: "user", Content: "hi"}}, "agent", false, nil,
		router.Sinks{Text: func(string) {}, Task: func(d *router.TaskDecision) { last = d }})
	if err != nil {
		t.Fatal(err)
	}
	if via != "fake/tiny" || seen()[0] != "tiny" {
		t.Fatalf("via %s, upstream saw %v; want the tier-1 model", via, seen())
	}
	if done.FinishReason != "stop" || last == nil || last.Via != "fake/tiny" || last.Required != 1 {
		t.Fatalf("decision not reported: %+v", last)
	}
	if !strings.Contains(last.Header(), "pick=fake/tiny") {
		t.Fatalf("header: %s", last.Header())
	}
}

func TestTaskRouterModelSuffixOverrides(t *testing.T) {
	srv, _ := fakeUpstream(t)
	r := taskRouter(t, srv.URL, nil)
	d, err := r.PlanTask(context.Background(), "agent:plan:5",
		[]router.Message{{Role: "user", Content: "hi"}}, false,
		&router.TaskHints{Override: "chat:1", Metadata: map[string]string{"task": "code:2"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Need.Role != "plan" || d.Need.Complexity != 5 || d.Need.Source != "model" || d.Winner.ID != "big" {
		t.Fatalf("model suffix must win: %+v / %s", d.Need, d.Reason)
	}
	// Header beats metadata.
	d, _ = r.PlanTask(context.Background(), "agent", []router.Message{{Role: "user", Content: "hi"}}, false,
		&router.TaskHints{Override: "write", Metadata: map[string]string{"task": "code:2"}})
	if d.Need.Role != "write" || d.Need.Complexity != 2 || d.Need.Source != "header" {
		t.Fatalf("header must beat metadata (role), metadata keeps complexity: %+v", d.Need)
	}
}

func TestTaskRouterEscalatesEmptyAnswer(t *testing.T) {
	srv, seen := fakeUpstream(t, "tiny")
	r := taskRouter(t, srv.URL, nil)

	var text strings.Builder
	var decisions []router.TaskDecision
	via, _, err := r.RouteRaw(context.Background(), "agent",
		[]router.Message{{Role: "user", Content: "hi"}}, "agent", false, nil,
		router.Sinks{Text: func(s string) { text.WriteString(s) }, Task: func(d *router.TaskDecision) { decisions = append(decisions, *d) }})
	if err != nil {
		t.Fatal(err)
	}
	if via == "fake/tiny" || !strings.HasPrefix(text.String(), "answer from ") {
		t.Fatalf("empty answer was not escalated: via %s, text %q, upstream %v", via, text.String(), seen())
	}
	final := decisions[len(decisions)-1]
	if final.Attempt != 1 || final.Required != 2 || final.Via != via {
		t.Fatalf("final decision %+v", final)
	}
	for _, m := range final.Chain {
		if m == "fake/tiny" {
			t.Fatalf("rejected model still in chain: %v", final.Chain)
		}
	}
}

func TestTaskRouterStreamIsNotEscalated(t *testing.T) {
	srv, _ := fakeUpstream(t, "tiny")
	r := taskRouter(t, srv.URL, nil)
	via, _, err := r.RouteRaw(context.Background(), "agent",
		[]router.Message{{Role: "user", Content: "hi"}}, "agent", true, nil, router.Sinks{Text: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if via != "fake/tiny" {
		t.Fatalf("stream must not be re-routed after the fact, via %s", via)
	}
}

func TestConfiguredRouteWinsOverTaskAlias(t *testing.T) {
	srv, seen := fakeUpstream(t)
	r := taskRouter(t, srv.URL, map[string][]string{"agent": {"fake/mid"}})
	if r.IsTaskAlias("agent") || r.IsTaskAlias("agent:code:4") || !r.IsTaskAlias("task") {
		t.Fatal("a route named agent must shadow the alias, task must stay")
	}
	via, _, err := r.Route(context.Background(), "agent", []router.Message{{Role: "user", Content: "hi"}}, "", nil)
	if err != nil || via != "fake/mid" || seen()[0] != "mid" {
		t.Fatalf("via %s err %v upstream %v", via, err, seen())
	}
	if got := r.TaskAliases(); len(got) != 1 || got[0] != "task" {
		t.Fatalf("aliases %v", got)
	}
}

func TestTaskSpecDropsUnconfiguredProviders(t *testing.T) {
	srv, _ := fakeUpstream(t)
	r := taskRouter(t, srv.URL, nil)
	for _, e := range r.TaskSpec().Catalog {
		if e.Provider() != "fake" {
			t.Fatalf("entry %s for unconfigured provider kept", e.ID)
		}
		if e.Where != "keyless" {
			t.Fatalf("kind not filled: %+v", e)
		}
	}
	if a := r.TaskAvailability(context.Background()); a["tiny"] != "ok" {
		t.Fatalf("availability %v", a)
	}
}

func TestTaskRouterDefaultsToRozkladJazdy(t *testing.T) {
	srv, seen := fakeUpstream(t)
	r := taskRouterMode(t, srv.URL, nil, "")
	if r.TaskMode() != "rozklad-jazdy" || router.TaskModes()[0].Name != "[Rozkład Jazdy]" {
		t.Fatalf("default mode %q", r.TaskMode())
	}
	var last *router.TaskDecision
	via, _, err := r.RouteRaw(context.Background(), "agent", []router.Message{{Role: "user", Content: "hi"}}, "agent", false, nil,
		router.Sinks{Text: func(string) {}, Task: func(d *router.TaskDecision) { last = d }})
	if err != nil || via != "fake/big" || seen()[0] != "big" {
		t.Fatalf("[Rozkład Jazdy] must go straight to the strongest: via %s err %v", via, err)
	}
	if last.Mode != "rozklad-jazdy" || !strings.Contains(last.Header(), "mode=rozklad-jazdy") {
		t.Fatalf("decision %+v", last)
	}
	// Empty answer from the strongest: retry goes to the next strongest.
	srv2, seen2 := fakeUpstream(t, "big")
	r2 := taskRouterMode(t, srv2.URL, nil, "")
	via, _, err = r2.RouteRaw(context.Background(), "agent", []router.Message{{Role: "user", Content: "hi"}}, "agent", false, nil, router.Sinks{Text: func(string) {}})
	if err != nil || via != "fake/mid" {
		t.Fatalf("retry after empty answer: via %s err %v upstream %v", via, err, seen2())
	}
}

func TestTaskModeSwitchAndPerRequest(t *testing.T) {
	srv, _ := fakeUpstream(t)
	r := taskRouterMode(t, srv.URL, nil, "")
	hi := []router.Message{{Role: "user", Content: "hi"}}

	if _, err := r.SetTaskMode("turbo"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if id, err := r.SetTaskMode("Jako Tako"); err != nil || id != "jako-tako" || r.TaskMode() != "jako-tako" {
		t.Fatalf("switch: %q %v", id, err)
	}
	if d, _ := r.PlanTask(context.Background(), "agent", hi, false, nil); d.Winner.ID != "tiny" {
		t.Fatalf("Jako Tako after switch: %s", d.Reason)
	}
	// Per request back to [Rozkład Jazdy]: header, metadata, model suffix.
	for _, c := range []struct {
		sel string
		h   *router.TaskHints
	}{
		{"agent", &router.TaskHints{Mode: "[Rozkład Jazdy]"}},
		{"agent", &router.TaskHints{Metadata: map[string]string{"task_mode": "rozklad-jazdy"}}},
		{"agent:rozklad-jazdy", nil},
		{"agent", &router.TaskHints{Override: "rozklad jazdy"}},
	} {
		d, err := r.PlanTask(context.Background(), c.sel, hi, false, c.h)
		if err != nil || d.Mode != "rozklad-jazdy" || d.Winner.ID != "big" {
			t.Fatalf("%s %+v: %v %s", c.sel, c.h, err, d.Reason)
		}
	}
	// Model suffix beats the header.
	d, _ := r.PlanTask(context.Background(), "agent:jako-tako", hi, false, &router.TaskHints{Mode: "rozklad-jazdy"})
	if d.Mode != "jako-tako" {
		t.Fatalf("suffix must beat header: %s", d.Mode)
	}
}
