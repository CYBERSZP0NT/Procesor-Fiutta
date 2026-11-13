package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CYBERSZP0NT/szmelc-router/internal/agentic"
	"github.com/CYBERSZP0NT/szmelc-router/internal/live"
)

const goodKey = "fla_good_key_123456"

// liveEnv points Load at a fake catalog service that accepts goodKey.
func liveEnv(t *testing.T) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	cat, _ := json.Marshal(live.Catalog{Version: "2026.11.12", Tier: "live", Models: []live.Model{
		{Platform: "google", ModelID: "gemini-3-pro", SizeLabel: "Frontier", ContextWindow: 1048576, Enabled: true, SupportsTools: true},
		{Platform: "groq", ModelID: "kimi-k2-instruct", SizeLabel: "Frontier", ContextWindow: 262144, Enabled: true},
		{Platform: "zhipu", ModelID: "glm-4.6", SizeLabel: "Frontier", ContextWindow: 200000, Enabled: true},
		{Platform: "github", ModelID: "gpt-5", SizeLabel: "Frontier", ContextWindow: 128000, Enabled: true},
		{Platform: "cerebras", ModelID: "qwen-3-235b", SizeLabel: "Large", ContextWindow: 131072, Enabled: true},
	}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"valid":false,"reason":"unknown_key"}`))
			return
		}
		switch r.URL.Path {
		case "/v1/license/check":
			_, _ = w.Write([]byte(`{"valid":true,"plan":"annual","status":"active"}`))
		case "/v1/latest":
			w.Header().Set("X-Catalog-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, cat)))
			_, _ = w.Write(cat)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FREELLM_LIVE", "auto")
	t.Setenv("CATALOG_BASE_URL", srv.URL)
	t.Setenv("CATALOG_PUBKEY", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	t.Setenv("FREELLM_LIVE_CACHE", t.TempDir())
	t.Setenv("FREELLM_TASK_MODE", "")
	t.Setenv("FREELLMAPI_API_KEY", "")
	t.Setenv("FREELLMAPI_URL", "http://127.0.0.1:1")
}

// loadNoUserFile loads with an empty user config (no ~/.config file).
func loadNoUserFile(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(writeConfig(t, "{}\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadWithWorkingKeyDefaultsToLive(t *testing.T) {
	liveEnv(t)
	t.Setenv("FREELLM_API_KEY", goodKey)
	cfg := loadNoUserFile(t)
	st := cfg.LiveState
	if st == nil || !st.Active || !st.DefaultMode {
		t.Fatalf("want live active as the default, got %+v", st)
	}
	if cfg.Agentic.Mode != agentic.ModeLive {
		t.Errorf("mode %q, want live", cfg.Agentic.Mode)
	}
	// Renamed platforms (zhipu→zai, github→github_models) must survive
	// the provider filter, or live quietly routes an empty catalog.
	ids := map[string]bool{}
	for _, e := range cfg.Agentic.Catalog {
		ids[e.Model] = true
	}
	for _, m := range []string{"google/gemini-3-pro", "groq/kimi-k2-instruct", "zai/glm-4.6", "github_models/gpt-5", "cerebras/qwen-3-235b"} {
		if !ids[m] {
			t.Errorf("live entry %s missing (have %d live entries)", m, st.Entries)
		}
	}
	if st.Entries != 5 {
		t.Errorf("entries %d, want 5", st.Entries)
	}
	if probs := cfg.Validate(); probs != nil {
		t.Errorf("config with live entries does not validate: %v", probs)
	}
}

func TestLoadLiveKeepsAnExplicitMode(t *testing.T) {
	liveEnv(t)
	t.Setenv("FREELLM_API_KEY", goodKey)
	t.Setenv("FREELLM_TASK_MODE", "jako-tako")
	cfg := loadNoUserFile(t)
	if !cfg.LiveState.Active || cfg.LiveState.DefaultMode || cfg.Agentic.Mode != "jako-tako" {
		t.Errorf("explicit mode overridden: mode %q state %+v", cfg.Agentic.Mode, cfg.LiveState)
	}
	if cfg.LiveState.Entries == 0 {
		t.Error("the live catalog should still feed the task router")
	}
}

func TestLoadWithBadKeyStaysOff(t *testing.T) {
	liveEnv(t)
	t.Setenv("FREELLM_API_KEY", "fla_wrong_key_99999999")
	cfg := loadNoUserFile(t)
	if cfg.LiveState.Active || cfg.Agentic.Mode != "" || !strings.Contains(cfg.LiveState.Reason, "unknown_key") {
		t.Errorf("bad key: mode %q state %+v", cfg.Agentic.Mode, cfg.LiveState)
	}
}

func TestLoadLiveOff(t *testing.T) {
	liveEnv(t)
	t.Setenv("FREELLM_API_KEY", goodKey)
	t.Setenv("FREELLM_LIVE", "off")
	cfg := loadNoUserFile(t)
	if cfg.LiveState == nil || cfg.LiveState.Active || cfg.Agentic.Mode != "" {
		t.Errorf("live.enabled off ignored: %+v", cfg.LiveState)
	}
}
