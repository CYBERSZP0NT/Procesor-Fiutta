package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/silverx/freellm-router/internal/config"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// writeTempConfig writes a config YAML to a temp file and returns its path.
func writeTempConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeTestConfig builds a minimal config pointing at a fake server.
func makeTestConfig(fakeBaseURL string) *config.Config {
	return &config.Config{
		Routes: map[string][]string{
			"auto": {"fake/test-model"},
			"free": {"fake/test-model"},
		},
		Providers: []config.Provider{
			{Name: "fake", Impl: "openailike", BaseURL: fakeBaseURL, Kind: "keyless", AllowInsecureUpstream: true},
		},
		Server:   config.ServerConfig{Host: "127.0.0.1", Port: 0, Auth: config.AuthConfig{Mode: "none"}},
		Policies: config.PolicyConfig{CooldownShortSec: 5, CooldownLongSec: 60, MaxChainAttempts: 3},
		Logging:  config.LoggingConfig{Level: "info", Format: "json"},
	}
}

// newFakeUpstream returns a minimal OpenAI-compatible fake for tests.
func newFakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{
				"id":"chatcmpl-test",
				"object":"chat.completion",
				"model":"test-model",
				"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
			}`)
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":"test-model","name":"Test Model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureOutput redirects os.Stdout to a bytes.Buffer during fn and returns the captured output.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	_, _ = io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// helper to reset all globals between tests.
func resetGlobals() {
	pCfgPath = ""
	pProvider = ""
	pModel = ""
	pTimeoutSec = 3
	pJSON = false
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestChatAgainstFake(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true
	pModel = "auto"

	code := Execute([]string{"chat", "hi"})
	if code != 0 {
		t.Fatalf("chat exited with code %d", code)
	}
}

func TestChatStreaming(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = false
	pModel = "auto"

	code := Execute([]string{"chat", "hello", "--no-stream"})
	if code != 0 {
		t.Fatalf("chat --no-stream exited with code %d", code)
	}
}

func TestChatVia(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = false
	pModel = "free"

	out := captureOutput(t, func() {
		code := Execute([]string{"chat", "hi", "--no-stream", "--via"})
		if code != 0 {
			t.Fatalf("chat --via exited with code %d", code)
		}
	})
	// The "via" info goes to stderr; the response goes to stdout.
	_ = out
}

func TestChatSystemMsg(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true
	pModel = "auto"

	code := Execute([]string{"chat", "hi", "--system", "You are a test bot.", "--no-stream"})
	if code != 0 {
		t.Fatalf("chat --system exited with code %d", code)
	}
}

func TestModelsJSON(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true

	out := captureOutput(t, func() {
		code := Execute([]string{"models"})
		if code != 0 {
			t.Fatalf("models --json exited with code %d", code)
		}
	})

	// Verify it's valid JSON with the expected shape.
	var mj struct {
		Routes []struct {
			Name  string   `json:"name"`
			Chain []string `json:"chain"`
		} `json:"routes"`
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &mj); err != nil {
		t.Fatalf("models --json output is not valid JSON: %v", err)
	}
	// Must have "auto" route.
	foundAuto := false
	for _, r := range mj.Routes {
		if r.Name == "auto" {
			foundAuto = true
			break
		}
	}
	if !foundAuto {
		t.Error("models --json output: expected 'auto' route in routes list")
	}
	if len(mj.Providers) == 0 {
		t.Error("models --json output: expected at least 1 provider")
	}
}

func TestModelsJSONTier(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfg.Providers[0].Notes = "Test free tier: 100 req/day"
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true

	out := captureOutput(t, func() {
		code := Execute([]string{"models", "--tier"})
		if code != 0 {
			t.Fatalf("models --tier exited with code %d", code)
		}
	})

	if !strings.Contains(out, "Test free tier: 100 req/day") {
		t.Error("models --tier output: expected tier notes to be present")
	}
}

func TestModelsProviderFilter(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pProvider = "fake"
	pJSON = true

	out := captureOutput(t, func() {
		code := Execute([]string{"models"})
		if code != 0 {
			t.Fatalf("models --provider fake exited with code %d", code)
		}
	})

	var mj struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &mj); err != nil {
		t.Fatal(err)
	}
	if len(mj.Providers) != 1 || mj.Providers[0].Name != "fake" {
		t.Errorf("expected exactly provider 'fake', got %v", mj.Providers)
	}
}

func TestModelsKindFilter(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true

	out := captureOutput(t, func() {
		code := Execute([]string{"models", "--kind", "keyless"})
		if code != 0 {
			t.Fatalf("models --kind keyless exited with code %d", code)
		}
	})

	var mj struct {
		Providers []struct {
			Kind string `json:"kind"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &mj); err != nil {
		t.Fatal(err)
	}
	for _, p := range mj.Providers {
		if p.Kind != "keyless" {
			t.Errorf("expected all providers to be kind=keyless, got %q", p.Kind)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath

	out := captureOutput(t, func() {
		code := Execute([]string{"config", "validate"})
		if code != 0 {
			t.Fatalf("config validate exited with code %d", code)
		}
	})
	if !strings.Contains(out, "Config OK") {
		t.Error("config validate: expected 'Config OK' in output, got:", out)
	}
}

func TestConfigPath(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath

	out := captureOutput(t, func() {
		code := Execute([]string{"config", "path"})
		if code != 0 {
			t.Fatalf("config path exited with code %d", code)
		}
	})
	if !strings.Contains(out, "config.yaml") {
		t.Error("config path: expected 'config.yaml' in output")
	}
}

func TestServeHelp(t *testing.T) {
	resetGlobals()
	pCfgPath = ""

	out := captureOutput(t, func() {
		code := Execute([]string{"serve", "--help"})
		if code != 0 {
			t.Fatalf("serve --help exited with code %d", code)
		}
	})
	for _, want := range []string{"OpenAI-compatible", "POST /v1/chat/completions", "--once", "--host", "--port"} {
		if !strings.Contains(out, want) {
			t.Errorf("serve --help: expected %q in output", want)
		}
	}
}

func TestProvidersList(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true

	out := captureOutput(t, func() {
		code := Execute([]string{"providers", "list"})
		if code != 0 {
			t.Fatalf("providers list exited with code %d", code)
		}
	})

	var pl []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &pl); err != nil {
		t.Fatal("providers list --json: invalid JSON:", err)
	}
	foundFake := false
	for _, p := range pl {
		if p.Name == "fake" {
			foundFake = true
		}
	}
	if !foundFake {
		t.Error("providers list: expected 'fake' provider in output")
	}
}

func TestProvidersAdd(t *testing.T) {
	// Use a temp config file that we can modify.
	tmpdir := t.TempDir()
	cfgPath := filepath.Join(tmpdir, "config.yaml")

	// Start with a minimal valid config.
	cfg := makeTestConfig("http://localhost:9999")
	data, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true

	out := captureOutput(t, func() {
		code := Execute([]string{
			"providers", "add",
			"--name", "newprov",
			"--base-url", "https://api.newprov.com/v1",
			"--kind", "keyless",
		})
		if code != 0 {
			t.Fatalf("providers add exited with code %d", code)
		}
	})
	if !strings.Contains(out, "added") {
		t.Error("providers add: expected 'added' in output:", out)
	}
}

func TestNotes(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfg.Providers[0].Notes = "test free tier note"
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath

	out := captureOutput(t, func() {
		code := Execute([]string{"notes"})
		if code != 0 {
			t.Fatalf("notes exited with code %d", code)
		}
	})
	if !strings.Contains(out, "test free tier note") {
		t.Error("notes: expected 'test free tier note' in output")
	}
}

func TestProbeJSON(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true
	pModel = "auto"

	out := captureOutput(t, func() {
		code := Execute([]string{"probe", "--pro-timeout", "5"})
		if code != 0 {
			t.Fatalf("probe exited with code %d", code)
		}
	})

	// Verify it's a JSON array.
	var results []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		OK       bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal("probe --json: invalid JSON:", err)
	}
	if len(results) == 0 {
		t.Error("probe: expected at least 1 result")
	}
	for _, r := range results {
		if r.Provider != "fake" {
			t.Errorf("probe: expected provider 'fake', got %q", r.Provider)
		}
	}
}

func TestProbeProviderFilter(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = true
	pModel = "auto"
	pProvider = "fake"

	out := captureOutput(t, func() {
		code := Execute([]string{"probe", "--pro-timeout", "5"})
		if code != 0 {
			t.Fatalf("probe --provider fake exited with code %d", code)
		}
	})

	var results []struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Provider != "fake" {
			t.Errorf("probe --provider fake: expected only 'fake', got %q", r.Provider)
		}
	}
}

// TestChatErrorHandling verifies that a connection failure produces a
// non-zero exit code (error path).
func TestChatErrorHandling(t *testing.T) {
	// Point at a closed port.
	cfg := makeTestConfig("http://127.0.0.1:19999/v1")
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pJSON = false
	pModel = "auto"
	pTimeoutSec = 2

	code := Execute([]string{"chat", "hi", "--no-stream"})
	if code != 1 {
		t.Fatalf("expected exit code 1 for connection failure, got %d", code)
	}
}

// TestProvidersTestCommand tests `providers test` against the fake.
func TestProvidersTestCommand(t *testing.T) {
	fake := newFakeUpstream(t)
	cfg := makeTestConfig(fake.URL)
	cfgPath := writeTempConfig(t, cfg)

	resetGlobals()
	pCfgPath = cfgPath
	pTimeoutSec = 5

	out := captureOutput(t, func() {
		code := Execute([]string{"providers", "test", "fake"})
		if code != 0 {
			t.Fatalf("providers test fake exited with code %d", code)
		}
	})
	if !strings.Contains(out, "OK") {
		t.Error("providers test fake: expected 'OK' in output")
	}
}

// TestVersionFlag verifies --version prints something.
func TestVersionFlag(t *testing.T) {
	resetGlobals()
	SetVersionInfo("1.0.0-test", "abc1234", "2025-01-01")

	out := captureOutput(t, func() {
		code := Execute([]string{"--version"})
		if code != 0 {
			t.Fatalf("--version exited with code %d", code)
		}
	})
	if !strings.Contains(out, "1.0.0-test") {
		t.Error("--version: expected version string in output")
	}
}
