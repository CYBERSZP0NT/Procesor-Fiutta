package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
)

// serveFile spins up an httptest server returning the given file with
// application/json (path-independent).
func serveFile(t *testing.T, path string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func mustFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s missing: %v", p, err)
	}
	srv := serveFile(t, p)
	t.Cleanup(srv.Close)
	return srv.URL
}

// ---------------------------------------------------------------------------
// freellmapi
// ---------------------------------------------------------------------------

func TestFreellmapiParseFixture(t *testing.T) {
	s := FreellmapiSource()
	s.URL = mustFile(t, "freellmapi.json")

	notes, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("no notes parsed")
	}

	// Free entries carry caps "free"; paid entries "paid".
	var free, paid int
	for _, n := range notes {
		switch n.Tier.Caps {
		case "free":
			free++
		case "paid":
			paid++
		}
		if n.Provider == "" {
			t.Errorf("note without provider: %+v", n)
		}
	}
	if free < 3 {
		t.Errorf("expected >=3 free model notes, got %d (%+v)", free, notes)
	}
	if paid != 1 {
		t.Errorf("expected exactly 1 paid note, got %d (%+v)", paid, notes)
	}

	provs, err := s.ProviderList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) == 0 {
		t.Fatal("no provider specs")
	}
	found := false
	for _, p := range provs {
		if p.Name == "freellmapi" && p.BaseURL == "https://freellmapi.co/v1" {
			found = true
			if len(p.Models) < 2 {
				t.Errorf("expected freellmapi spec to carry models, got %+v", p)
			}
		}
	}
	if !found {
		t.Errorf("freellmapi provider spec missing: %+v", provs)
	}
}

// ---------------------------------------------------------------------------
// awesome-llm-apis
// ---------------------------------------------------------------------------

func TestAwesomeParseFixture(t *testing.T) {
	s := AwesomeSource()
	s.URL = mustFile(t, "awesome-llm-apis.json")

	notes, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	provs, err := s.ProviderList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]core.ProviderSpec{}
	for _, p := range provs {
		byName[p.Name] = p
	}

	// keyless entry
	if p := byName["pollinations"]; p.Kind != "keyless" {
		t.Errorf("pollinations kind = %q, want keyless", p.Kind)
	}
	// explicit key entry
	if p := byName["deepseek"]; p.Kind != "keyed" {
		t.Errorf("deepseek kind = %q, want keyed", p.Kind)
	}
	// CSV models string must be split
	if p := byName["deepseek"]; len(p.Models) != 2 || p.Models[0] != "deepseek-chat" {
		t.Errorf("deepseek models = %v, want [deepseek-chat deepseek-reasoner]", p.Models)
	}
	// "auth":"no" → keyless
	if p := byName["hf-inference"]; p.Kind != "keyless" {
		t.Errorf("hf-inference kind = %q, want keyless", p.Kind)
	}
	// entries without name+url are dropped
	if _, ok := byName[""]; ok {
		t.Error("empty-named entry should be dropped")
	}

	if len(notes) < 4 {
		t.Errorf("expected >=4 model notes, got %d (%+v)", len(notes), notes)
	}
}

// ---------------------------------------------------------------------------
// free-ai-models
// ---------------------------------------------------------------------------

func TestFreeAIModelsParseFixture(t *testing.T) {
	s := FreeAIModelsSource()
	s.URL = mustFile(t, "free-ai-models.json")

	notes, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 4 {
		t.Fatalf("expected 4 notes, got %d (%+v)", len(notes), notes)
	}
	byKey := map[string]core.ModelNote{}
	for _, n := range notes {
		byKey[n.Provider+"/"+n.Model] = n
	}
	if n := byKey["freellmapi/gpt-4o-mini"]; n.Tier.Caps != "up" {
		t.Errorf("gpt-4o-mini caps = %q, want up", n.Tier.Caps)
	}
	if n := byKey["groq/llama-3.3-70b-versatile"]; n.Tier.Caps != "down" {
		t.Errorf("llama-3.3 caps = %q, want down", n.Tier.Caps)
	}
	if n := byKey["google/gemini-2.5-flash"]; n.Tier.Caps != "up" {
		t.Errorf("gemini caps = %q, want up (numeric status)", n.Tier.Caps)
	}
	if n := byKey["openrouter/o3"]; n.Tier.Caps != "down" {
		t.Errorf("o3 caps = %q, want down (bool status)", n.Tier.Caps)
	}
}

// ---------------------------------------------------------------------------
// Tolerance: network errors must be clean errors, not panics
// ---------------------------------------------------------------------------

func TestAdapterUnavailableIsCleanError(t *testing.T) {
	closeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer closeSrv.Close()

	s := AwesomeSource()
	s.URL = closeSrv.URL
	_, err := s.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for HTTP 404 feed")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestAdapterContextTimeout(t *testing.T) {
	start := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-start
	}))
	defer srv.Close()
	defer close(start)

	s := FreellmapiSource()
	s.URL = srv.URL
	s.Timeout = 50 * time.Millisecond
	_, err := s.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// Garbage (non-JSON) feed: adapters must return a parse error, not crash.
func TestAdapterGarbageBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	for name, s := range map[string]*RemoteSource{
		"freellmapi":       FreellmapiSource(),
		"awesome-llm-apis": AwesomeSource(),
		"free-ai-models":   FreeAIModelsSource(),
	} {
		s.URL = srv.URL
		if _, err := s.Fetch(context.Background()); err == nil {
			t.Errorf("%s: expected parse error on non-JSON body", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry(t *testing.T) {
	for _, n := range []string{"freellmapi", "awesome-llm-apis", "free-ai-models"} {
		s, ok := Get(n)
		if !ok || s == nil {
			t.Fatalf("registry missing %q", n)
		}
		if s.URL == "" {
			t.Errorf("%s: no default URL", n)
		}
	}
	if _, ok := Get("nope-no-such-feed"); ok {
		t.Error("unknown name should not resolve")
	}
	if len(Names()) != 3 {
		t.Errorf("Names() = %v", Names())
	}
}

// ---------------------------------------------------------------------------
// Local / embedded sources (offline sources the CLI relies on)
// ---------------------------------------------------------------------------

func TestEmbeddedSourceOffline(t *testing.T) {
	src := EmbeddedSource()
	notes, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) < 10 {
		t.Errorf("embedded source should carry the builtin provider notes (%d)", len(notes))
	}
	for _, n := range notes {
		if n.Tier.Source != core.SourceEmbedded {
			t.Errorf("embedded note source = %q, want %q", n.Tier.Source, core.SourceEmbedded)
		}
	}
	provs, err := src.ProviderList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) < 10 {
		t.Errorf("embedded provider specs: %d", len(provs))
	}
}
