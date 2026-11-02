package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func tSetEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// Test: embedded defaults load without error
// ---------------------------------------------------------------------------

func TestLoad_EmbeddedDefaults(t *testing.T) {
	cfg, err := Load("") // no explicit path, no FREELLM_CONFIG → pure defaults
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Providers) == 0 {
		t.Error("expected at least one provider from defaults")
	}
	// Check critical providers exist
	mustExist := []string{"groq", "google", "pollinations", "ollama", "deepseek"}
	for _, name := range mustExist {
		if _, ok := cfg.ProviderByName(name); !ok {
			t.Errorf("expected provider %q in defaults, not found", name)
		}
	}
	// Check keyless providers have no key_env
	if p, ok := cfg.ProviderByName("pollinations"); ok {
		if !keylessProvider(p) {
			t.Error("pollinations should be kind=keyless")
		}
	}
	// Check keyed providers have key_env
	if p, ok := cfg.ProviderByName("groq"); ok {
		if p.KeyEnv == "" {
			t.Error("groq should have key_env set in defaults")
		}
	}
	// Check routes exist
	for _, route := range []string{"auto", "free", "fast", "code", "local", "smart"} {
		if len(cfg.RouteChain(route)) == 0 {
			t.Errorf("expected default route %q, not found", route)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: user config file overrides defaults
// ---------------------------------------------------------------------------

func TestLoad_UserConfigOverride(t *testing.T) {
	userYAML := `
routes:
  auto:
    - custom_provider/my-model
providers:
  - name: custom_provider
    impl: openailike
    base_url: "http://localhost:9999/v1"
    key_env: "CUSTOM_KEY"
    kind: keyed
    weight: 1
`
	path := writeConfig(t, userYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	p, ok := cfg.ProviderByName("custom_provider")
	if !ok {
		t.Fatal("custom_provider not found in merged config")
	}
	if p.BaseURL != "http://localhost:9999/v1" {
		t.Errorf("BaseURL = %q, want http://localhost:9999/v1", p.BaseURL)
	}
	if p.KeyEnv != "CUSTOM_KEY" {
		t.Errorf("KeyEnv = %q, want CUSTOM_KEY", p.KeyEnv)
	}
	// auto route should be overridden
	auto := cfg.RouteChain("auto")
	if len(auto) != 1 || auto[0] != "custom_provider/my-model" {
		t.Errorf("auto route = %v, want [custom_provider/my-model]", auto)
	}
	// other providers (groq etc.) should still exist
	if _, ok := cfg.ProviderByName("groq"); !ok {
		t.Error("groq should still exist after user config merge")
	}
}

// ---------------------------------------------------------------------------
// Test: user config can ADD providers
// ---------------------------------------------------------------------------

func TestLoad_UserConfigAddsProviders(t *testing.T) {
	userYAML := `
providers:
  - name: mysidecar
    impl: openailike
    base_url: "http://localhost:4242/v1"
    kind: sidecar
    weight: 5
`
	path := writeConfig(t, userYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.ProviderByName("mysidecar"); !ok {
		t.Error("mysidecar provider not found")
	}
}

// ---------------------------------------------------------------------------
// Test: env-var overlay
// ---------------------------------------------------------------------------

func TestLoad_EnvOverlay(t *testing.T) {
	tSetEnv(t, map[string]string{
		"FREELLM_PORT":      "9999",
		"FREELLM_HOST":      "127.0.0.1",
		"FREELLM_LOG_LEVEL": "debug",
	})

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("Port = %d, want 9999", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want 127.0.0.1", cfg.Server.Host)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.Logging.Level)
	}
}

// ---------------------------------------------------------------------------
// Test: ${VAR:-default} expansion
// ---------------------------------------------------------------------------

func TestExpandString(t *testing.T) {
	tSetEnv(t, map[string]string{
		"MY_TEST_VAR": "hello",
	})

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "no expansion", "no expansion"},
		{"set env", "val=${MY_TEST_VAR}", "val=hello"},
		{"unset env no default", "val=${UNSET_VAR_XYZ}", "val="},
		{"unset env with default", "val=${UNSET_VAR_XYZ:-fallback}", "val=fallback"},
		{"set env ignores default", "val=${MY_TEST_VAR:-default}", "val=hello"},
		{"empty string expansion", "http://${EMPTY_XYZ:-localhost:11434}/v1", "http://localhost:11434/v1"},
		{"url default", "http://${UNSET:-api.example.com:8080}/v1", "http://api.example.com:8080/v1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expandString(c.input)
			if got != c.want {
				t.Errorf("expandString(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test: expansion is actually applied to config
// ---------------------------------------------------------------------------

func TestLoad_ExpansionApplied(t *testing.T) {
	// Set an env var and make sure it appears in the config.
	tSetEnv(t, map[string]string{"TEST_BASE_URL": "http://test-host:1234"})

	userYAML := `
providers:
  - name: testprov
    impl: openailike
    base_url: "${TEST_BASE_URL:-http://default:80}"
    kind: keyless
    weight: 1
`
	path := writeConfig(t, userYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.ProviderByName("testprov")
	if !ok {
		t.Fatal("testprov not found")
	}
	if p.BaseURL != "http://test-host:1234" {
		t.Errorf("BaseURL = %q, want http://test-host:1234 (env var should win)", p.BaseURL)
	}
}

func TestLoad_ExpansionDefaultUsed(t *testing.T) {
	t.Setenv("TEST_BASE_URL", "")
	// Env is set but empty → should use default.
	userYAML := `
providers:
  - name: testprov2
    impl: openailike
    base_url: "${TEST_BASE_URL:-http://fallback-host:9999}"
    kind: keyless
    weight: 1
`
	path := writeConfig(t, userYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.ProviderByName("testprov2")
	if !ok {
		t.Fatal("testprov2 not found")
	}
	if p.BaseURL != "http://fallback-host:9999" {
		t.Errorf("BaseURL = %q, want http://fallback-host:9999 (default)", p.BaseURL)
	}
}

// ---------------------------------------------------------------------------
// Test: Validate with known-impl
// ---------------------------------------------------------------------------

func TestValidate_UnknownImpl(t *testing.T) {
	userYAML := `
providers:
  - name: badimpl
    impl: nonexistent_impl
    base_url: "http://x:1/v1"
    kind: keyed
    key_env: "BAD_KEY"
`
	path := writeConfig(t, userYAML)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for unknown impl")
	}
	if !containsStr(err.Error(), "unknown impl") {
		t.Errorf("error should mention 'unknown impl', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Validate with bad port
// ---------------------------------------------------------------------------

func TestValidate_BadPort(t *testing.T) {
	userYAML := `
server:
  host: "0.0.0.0"
  port: 99999
  auth:
    mode: none
`
	path := writeConfig(t, userYAML)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for port 99999")
	}
	if !containsStr(err.Error(), "port") {
		t.Errorf("error should mention 'port', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Validate with unknown kind
// ---------------------------------------------------------------------------

func TestValidate_UnknownKind(t *testing.T) {
	userYAML := `
providers:
  - name: badkind
    impl: openailike
    base_url: "http://x:1/v1"
    kind: notakind
    key_env: "BADKEY"
`
	path := writeConfig(t, userYAML)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for unknown kind")
	}
	if !containsStr(err.Error(), "unknown kind") {
		t.Errorf("error should mention 'unknown kind', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Validate duplicate provider names
// ---------------------------------------------------------------------------

func TestValidate_DuplicateProvider(t *testing.T) {
	userYAML := `
providers:
  - name: dupprov
    impl: openailike
    base_url: "http://a:1/v1"
    kind: keyless
  - name: dupprov
    impl: openailike
    base_url: "http://b:1/v1"
    kind: keyless
`
	path := writeConfig(t, userYAML)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for duplicate provider names")
	}
	if !containsStr(err.Error(), "duplicate") {
		t.Errorf("error should mention 'duplicate', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Validate — route references unknown provider
// ---------------------------------------------------------------------------

func TestValidate_RouteRefsUnknownProvider(t *testing.T) {
	userYAML := `
routes:
  custom:
    - nosuchprovider/model-x
providers:
  - name: nosuchprovider
    impl: openailike
    base_url: "http://x:1/v1"
    kind: keyed
    key_env: "TEST_KEY"
`
	// Remove nosuchprovider so route references an unknown provider
	userYAML2 := `
routes:
  custom:
    - totally-unknown/model-x
`
	path := writeConfig(t, userYAML2)
	_ = userYAML
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for route referencing unknown provider")
	}
	if !containsStr(err.Error(), "unknown provider") {
		t.Errorf("error should mention 'unknown provider', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: 3-layer merge — env beats file, file beats defaults
// ---------------------------------------------------------------------------

func TestLoad_ThreeLayerMerge(t *testing.T) {
	// Layer 1: defaults (already embedded)
	// Layer 2: user file sets port=7777
	userYAML := `
server:
  host: "0.0.0.0"
  port: 7777
  auth:
    mode: none
`
	path := writeConfig(t, userYAML)

	// Layer 3: env overrides port to 8888
	t.Setenv("FREELLM_PORT", "8888")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 8888 {
		t.Errorf("Port = %d, want 8888 (env should win over file, file should win over defaults)", cfg.Server.Port)
	}
}

// ---------------------------------------------------------------------------
// Test: KeyEnvMap returns correct mapping
// ---------------------------------------------------------------------------

func TestKeyEnvMap(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.KeyEnvMap()
	if m["groq"] == "" {
		t.Error("groq should appear in KeyEnvMap with non-empty value")
	}
	if _, ok := m["pollinations"]; ok {
		t.Error("pollinations is keyless; should not have empty key_env in map")
	}
}

// ---------------------------------------------------------------------------
// Test: RouteNames returns sorted
// ---------------------------------------------------------------------------

func TestRouteNames_Sorted(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := cfg.RouteNames()
	if len(names) == 0 {
		t.Fatal("expected route names")
	}
	// Check sorted
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("RouteNames not sorted at index %d: %q < %q", i, names[i], names[i-1])
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Test: ProviderByName case insensitive
// ---------------------------------------------------------------------------

func TestProviderByName_CaseInsensitive(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.ProviderByName("GROQ")
	if !ok {
		t.Fatal("GROQ (uppercase) should be found")
	}
	if p.Name != "groq" {
		t.Errorf("provider name = %q, want groq", p.Name)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func keylessProvider(p Provider) bool {
	return p.Kind == "keyless" && p.KeyEnv == ""
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

