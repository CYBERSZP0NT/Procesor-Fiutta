package live

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "fla_test_0123456789abcdef"

// fixtureCatalog uses FreeLLMAPI platform ids, including the renamed ones.
var fixtureCatalog = Catalog{
	Version: "2026.11.12", GeneratedAt: "2026-11-12T00:00:00Z", Tier: "live",
	Models: []Model{
		{Platform: "google", ModelID: "gemini-3-pro", DisplayName: "Gemini 3 Pro", IntelligenceRank: 1, SpeedRank: 6, SizeLabel: "Frontier", ContextWindow: 1048576, Enabled: true, SupportsTools: true, Limits: Limits{RPD: 50}},
		{Platform: "google", ModelID: "gemini-3-flash", IntelligenceRank: 2, SpeedRank: 2, SizeLabel: "Large", ContextWindow: 1048576, Enabled: true, SupportsTools: true, MonthlyTokenBudget: "30M"},
		{Platform: "groq", ModelID: "llama-4-maverick", IntelligenceRank: 1, SpeedRank: 1, SizeLabel: "Large", ContextWindow: 131072, Enabled: true, Limits: Limits{TPD: 500000}},
		{Platform: "zhipu", ModelID: "glm-4.6", IntelligenceRank: 1, SizeLabel: "Frontier", ContextWindow: 200000, Enabled: true},
		{Platform: "github", ModelID: "gpt-5", IntelligenceRank: 1, SizeLabel: "Frontier", ContextWindow: 128000, Enabled: true},
		{Platform: "ollama", ModelID: "kimi-k2", IntelligenceRank: 1, SizeLabel: "Frontier", ContextWindow: 256000, Enabled: true},
		{Platform: "cerebras", ModelID: "qwen-3-32b", SizeLabel: "Medium", ContextWindow: 65536, Enabled: true},
		{Platform: "groq", ModelID: "dead-model", SizeLabel: "Large", Enabled: false},
		{Platform: "cloudflare", ModelID: "flux-1", SizeLabel: "Large", Enabled: true, Modality: "image"},
		{Platform: "nowhere", ModelID: "x", SizeLabel: "Frontier", Enabled: true},
	},
}

type fake struct {
	t       *testing.T
	priv    ed25519.PrivateKey
	pubPEM  string
	srv     *httptest.Server
	checks  atomic.Int32
	fetches atomic.Int32
	// knobs
	valid  bool
	tier   string
	tamper bool
	since  atomic.Value // last ?since=
}

func newFake(t *testing.T) *fake {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(pub)
	f := &fake{t: t, priv: priv, valid: true, tier: "live",
		pubPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/license/check", func(w http.ResponseWriter, r *http.Request) {
		f.checks.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+testKey || !f.valid {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"valid": false, "reason": "unknown_key"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "plan": "lifetime", "status": "active"})
	})
	mux.HandleFunc("GET /v1/latest", func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		f.since.Store(r.URL.Query().Get("since"))
		if s := r.URL.Query().Get("since"); s == fixtureCatalog.Version {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		cat := fixtureCatalog
		cat.Tier = f.tier
		body, _ := json.Marshal(cat)
		sig := ed25519.Sign(f.priv, body)
		if f.tamper {
			body = append([]byte(nil), body...)
			body[10] ^= 1
		}
		w.Header().Set("X-Catalog-Signature", base64.StdEncoding.EncodeToString(sig))
		_, _ = w.Write(body)
	})
	mux.HandleFunc("POST /v1/license/activate", func(w http.ResponseWriter, r *http.Request) {
		t.Error("activate must never be called")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) opts(dir string, env map[string]string) Options {
	return Options{
		Getenv:  func(k string) string { return env[k] },
		BaseURL: f.srv.URL, PubKey: f.pubPEM, CacheDir: dir,
		SidecarURL: "http://127.0.0.1:1", Timeout: 2 * time.Second,
	}
}

func TestClassifyAndMask(t *testing.T) {
	cases := map[string]Kind{"fla_abc": KindLicense, "freellmapi-xyz": KindUnified, "sk-123": KindUnknown, "": KindUnknown}
	for v, want := range cases {
		if got := Classify(v); got != want {
			t.Errorf("Classify(%q) = %s, want %s", v, got, want)
		}
	}
	if m := Mask(testKey); strings.Contains(m, "0123456789") || !strings.HasPrefix(m, "fla_") || !strings.HasSuffix(m, "cdef") {
		t.Errorf("Mask leaks or loses shape: %q", m)
	}
	if m := Mask("freellmapi-0123456789abcdef"); !strings.HasPrefix(m, "freellmapi-…") {
		t.Errorf("unified mask: %q", m)
	}
}

func TestDiscover(t *testing.T) {
	env := map[string]string{
		"FREELLM_API_KEY":     testKey,
		"FREELLMAPI_API_KEY":  "custom-sidecar-key", // no prefix: a sidecar key by its name
		"FREELLMAPI_KEY":      "router-own-key",     // listed as the router's own key below
		"FREEAPI_CONFIG_JSON": `{"license":"fla_from_declarative_config"}`,
	}
	keys := Discover(func(k string) string { return env[k] }, "", []string{"router-own-key"})
	got := map[string]Kind{}
	for _, k := range keys {
		got[k.From] = k.Kind
	}
	want := map[string]Kind{"FREELLM_API_KEY": KindLicense, "FREELLMAPI_API_KEY": KindUnified, "FREEAPI_CONFIG.license": KindLicense}
	if len(got) != len(want) {
		t.Fatalf("Discover = %+v, want %+v", got, want)
	}
	for f, k := range want {
		if got[f] != k {
			t.Errorf("%s: %s, want %s", f, got[f], k)
		}
	}
	if keys[len(keys)-1].Kind != KindUnified {
		t.Error("licenses must come before sidecar keys")
	}
	// A plain value in FREELLM_API_KEY is still tried as a license.
	keys = Discover(func(k string) string { return map[string]string{"FREELLM_API_KEY": "abc123"}[k] }, "", nil)
	if len(keys) != 1 || keys[0].Kind != KindLicense {
		t.Errorf("plain FREELLM_API_KEY: %+v", keys)
	}
}

func TestResolveNoKeyNoNetwork(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	r := Resolve(context.Background(), f.opts(dir, nil))
	if r.Active || f.checks.Load() != 0 || f.fetches.Load() != 0 {
		t.Fatalf("no key must mean no network: active=%v checks=%d fetches=%d", r.Active, f.checks.Load(), f.fetches.Load())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("no key must not write the cache: %v", ents)
	}
}

func TestResolveLicenseLiveAndCache(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	o := f.opts(dir, map[string]string{"FREELLM_API_KEY": testKey})
	r := Resolve(context.Background(), o)
	if !r.Active || r.Kind != KindLicense || r.Data == nil || r.State.Catalog.Tier != "live" {
		t.Fatalf("want active live license, got %+v", r.State)
	}
	if r.License.Plan != "lifetime" || r.Source != "network" {
		t.Errorf("license: %+v source %s", r.License, r.Source)
	}
	if strings.Contains(r.Key, "0123456789") {
		t.Errorf("state leaks the key: %s", r.Key)
	}
	for _, n := range []string{stateName, catalogName} {
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", n, st.Mode().Perm())
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, stateName))
	if strings.Contains(string(b), testKey) {
		t.Error("cache holds the raw key")
	}

	// Second resolve inside the TTLs: cache only, still verified.
	r2 := Resolve(context.Background(), o)
	if !r2.Active || r2.Source != "cache" || r2.State.Catalog.Source != "cache" {
		t.Fatalf("want cached verdict, got %+v", r2.State)
	}
	if f.checks.Load() != 1 || f.fetches.Load() != 1 {
		t.Errorf("cache not used: checks=%d fetches=%d", f.checks.Load(), f.fetches.Load())
	}

	// Catalog stale: a conditional fetch, 304 keeps the cached catalog.
	o.Now = func() time.Time { return time.Now().Add(13 * time.Hour) }
	o.LicenseTTL = 24 * time.Hour
	r3 := Resolve(context.Background(), o)
	if !r3.Active || r3.Data == nil || f.since.Load() != fixtureCatalog.Version {
		t.Fatalf("want 304 path with since=%s, got since=%v state %+v", fixtureCatalog.Version, f.since.Load(), r3.State)
	}
}

func TestResolveOutageKeepsCachedVerdict(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	o := f.opts(dir, map[string]string{"FREELLM_API_KEY": testKey})
	if r := Resolve(context.Background(), o); !r.Active {
		t.Fatalf("setup: %+v", r.State)
	}
	f.srv.Close()
	o.Force, o.ForceCatalog = true, true
	r := Resolve(context.Background(), o)
	if !r.Active || len(r.Warnings) == 0 || r.Data == nil {
		t.Fatalf("an outage must not flip live off: %+v", r.State)
	}
	// Beyond the grace period the cached verdict no longer counts.
	o.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if r := Resolve(context.Background(), o); r.Active {
		t.Errorf("verdict older than grace kept: %+v", r.State)
	}
}

func TestResolveRejections(t *testing.T) {
	t.Run("invalid key", func(t *testing.T) {
		f := newFake(t)
		f.valid = false
		r := Resolve(context.Background(), f.opts(t.TempDir(), map[string]string{"FREELLM_API_KEY": testKey}))
		if r.Active || !strings.Contains(r.Reason, "unknown_key") || f.fetches.Load() != 0 {
			t.Errorf("want inactive unknown_key without a catalog fetch, got %+v (fetches %d)", r.State, f.fetches.Load())
		}
	})
	t.Run("monthly tier", func(t *testing.T) {
		f := newFake(t)
		f.tier = "monthly"
		r := Resolve(context.Background(), f.opts(t.TempDir(), map[string]string{"FREELLM_API_KEY": testKey}))
		if r.Active || !strings.Contains(r.Reason, "monthly") {
			t.Errorf("monthly tier must not count as live: %+v", r.State)
		}
	})
	t.Run("tampered signature", func(t *testing.T) {
		f := newFake(t)
		f.tamper = true
		r := Resolve(context.Background(), f.opts(t.TempDir(), map[string]string{"FREELLM_API_KEY": testKey}))
		if r.Data != nil {
			t.Fatal("a tampered catalog was accepted")
		}
		if len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, " "), "FAILED") {
			t.Errorf("want a signature warning, got %v", r.Warnings)
		}
	})
	t.Run("wrong pinned key", func(t *testing.T) {
		f := newFake(t)
		o := f.opts(t.TempDir(), map[string]string{"FREELLM_API_KEY": testKey})
		o.PubKey = "" // production pin: the fake's signature must not verify
		if r := Resolve(context.Background(), o); r.Data != nil {
			t.Fatal("catalog verified against the wrong key")
		}
	})
}

func TestProbeSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer freellmapi-good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"auto"},{"id":"gemini-3-pro"}]}`))
	}))
	defer srv.Close()
	f := newFake(t)
	o := f.opts(t.TempDir(), map[string]string{"FREELLM_API_KEY": "freellmapi-good"})
	o.SidecarURL = srv.URL + "/v1" // provider base_url shape
	r := Resolve(context.Background(), o)
	if !r.Active || r.Kind != KindUnified || r.Sidecar.Models != 2 || f.checks.Load() != 0 {
		t.Fatalf("sidecar key: %+v (license checks %d — a unified key must never go to the catalog service)", r.State, f.checks.Load())
	}
	o.Getenv = func(k string) string { return map[string]string{"FREELLM_API_KEY": "freellmapi-bad"}[k] }
	if r := Resolve(context.Background(), o); r.Active {
		t.Errorf("rejected sidecar key active: %+v", r.State)
	}
}

func TestEntries(t *testing.T) {
	known := map[string]bool{"google": true, "groq": true, "zai": true, "github_models": true, "ollama_cloud": true, "cerebras": true, "cloudflare": true, "freellmapi": true, "ollama": true}
	cat := fixtureCatalog
	es := Entries(&cat, nil, known, map[string]bool{"cerebras/qwen-3-32b": true}, 0)
	got := map[string]bool{}
	for _, e := range es {
		got[e.Model] = true
	}
	for _, m := range []string{"google/gemini-3-pro", "zai/glm-4.6", "github_models/gpt-5", "ollama_cloud/kimi-k2", "groq/llama-4-maverick"} {
		if !got[m] {
			t.Errorf("missing %s (platform mapping)", m)
		}
	}
	for _, m := range []string{"groq/dead-model", "cloudflare/flux-1", "nowhere/x", "ollama/kimi-k2", "cerebras/qwen-3-32b"} {
		if got[m] {
			t.Errorf("%s must be dropped", m)
		}
	}
	if len(es) != 6 {
		t.Errorf("want 6 entries after filtering, got %d", len(es))
	}
	// Richest first: Frontier with the biggest context leads.
	if es[0].Model != "google/gemini-3-pro" || es[0].Tier != 5 {
		t.Errorf("richest first: got %s tier %d", es[0].Model, es[0].Tier)
	}
	if q := es[len(es)-1]; q.Tier != 4 {
		t.Errorf("Large models after Frontier, last is %s tier %d", q.Model, q.Tier)
	}
	if es := Entries(&cat, nil, known, nil, 2); len(es) != 2 {
		t.Errorf("max not applied: %d", len(es))
	}
	es = Entries(nil, []string{"auto"}, known, nil, 0)
	if len(es) != 2 || es[0].Model != "freellmapi/auto:smart" {
		t.Errorf("sidecar entries: %+v", es)
	}
	if DailyTokens(Model{MonthlyTokenBudget: "30M"}) != 1_000_000 || DailyTokens(Model{MonthlyTokenBudget: "2x"}) != 0 {
		t.Error("DailyTokens")
	}
}
