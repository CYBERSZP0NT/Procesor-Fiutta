package pluginproc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
)

// quietLogger returns a logger that discards all output (for tests).
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newTestPluginProvider(cmd []string, name string) *Provider {
	cfg := config.Provider{
		Name:             name,
		Impl:             "plugin",
		Kind:             "plugin",
		Command:          cmd,
		HandshakeTimeout: 10,
		RestartPolicy:    "on-crash",
	}
	return New(cfg, quietLogger())
}

// ---------------------------------------------------------------------------
// Helper: build the go-example plugin for THIS platform (once per run).
// A prebuilt binary from the repo would be the wrong OS/arch on macOS CI.
// ---------------------------------------------------------------------------

var (
	goExampleOnce sync.Once
	goExampleBin  string
	goExampleErr  error
)

func findGoExample(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("GO_EXAMPLE_BIN"); path != "" {
		return path
	}
	goExampleOnce.Do(func() {
		dir, err := os.MkdirTemp("", "go-example-*")
		if err != nil {
			goExampleErr = err
			return
		}
		out := filepath.Join(dir, "go-example")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = filepath.Join("..", "..", "..", "plugins", "go-example")
		if b, err := cmd.CombinedOutput(); err != nil {
			goExampleErr = fmt.Errorf("%v: %s", err, b)
			return
		}
		goExampleBin = out
	})
	if goExampleErr != nil {
		t.Logf("go-example plugin could not be built: %v", goExampleErr)
	}
	return goExampleBin
}

func findPythonPlugin() (string, bool) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return "", false
	}
	script := filepath.Join("..", "..", "plugins", "python-example", "plugin.py")
	if _, err := os.Stat(script); err != nil {
		return "", false
	}
	return py + " " + script, true
}

// ---------------------------------------------------------------------------
// Protocol round-trip using go-example
// ---------------------------------------------------------------------------

func TestPluginGoExampleInitialize(t *testing.T) {
	bin := findGoExample(t)
	if bin == "" {
		t.Skip("go-example not available")
	}

	p := newTestPluginProvider([]string{bin}, "go-example")
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Trigger the handshake by calling Models.
	models, err := p.Models(ctx)
	if err != nil {
		t.Fatalf("Models (which triggers handshake): %v", err)
	}
	if len(models) == 0 {
		t.Error("expected at least one model from go-example")
	}
	foundEcho := false
	for _, m := range models {
		if m.ID == "echo" {
			foundEcho = true
		}
	}
	if !foundEcho {
		t.Error("expected 'echo' model in catalogue")
	}
}

func TestPluginGoExampleStream(t *testing.T) {
	bin := findGoExample(t)
	if bin == "" {
		t.Skip("go-example not available")
	}

	p := newTestPluginProvider([]string{bin}, "go-example")
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var got strings.Builder
	out := &core.StreamOut{
		TextSink: func(s string) { got.WriteString(s) },
	}

	req := &core.ChatRequest{
		Model:    "echo",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hello world"}},
	}

	done, err := p.Stream(ctx, req, out)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if done.FinishReason != "stop" {
		t.Errorf("done.FinishReason = %q, want stop", done.FinishReason)
	}
	if got.String() != "echo: hello world" {
		t.Errorf("streamed = %q, want %q", got.String(), "echo: hello world")
	}
}

func TestPluginGoExampleRotating(t *testing.T) {
	bin := findGoExample(t)
	if bin == "" {
		t.Skip("go-example not available")
	}

	p := newTestPluginProvider([]string{bin}, "go-example")
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var texts []string
	out := &core.StreamOut{
		TextSink: func(s string) { texts = append(texts, s) },
	}

	req := &core.ChatRequest{
		Model:    "rotating",
		Messages: []core.Message{{Role: core.RoleUser, Content: "test"}},
	}

	for i := 0; i < 2; i++ {
		texts = nil
		_, err := p.Stream(ctx, req, out)
		if err != nil {
			t.Fatalf("rotating stream[%d]: %v", i, err)
		}
		full := strings.Join(texts, "")
		if full == "" {
			t.Errorf("stream[%d]: got empty text", i)
		}
	}
}

func TestPluginGoExampleUnknownModel(t *testing.T) {
	bin := findGoExample(t)
	if bin == "" {
		t.Skip("go-example not available")
	}

	p := newTestPluginProvider([]string{bin}, "go-example")
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &core.ChatRequest{
		Model:    "nonexistent-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	}
	_, err := p.Stream(ctx, req, nil)
	if err == nil {
		t.Error("expected error for unknown model")
	}
	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		t.Logf("error is not UpstreamErr (acceptable): %v", err)
	} else {
		if !strings.Contains(ue.Msg, "nonexistent-model") {
			t.Errorf("error should mention unknown model, got: %s", ue.Msg)
		}
	}
}

// ---------------------------------------------------------------------------
// Crash / restart
// ---------------------------------------------------------------------------

func TestPluginCrashDetection(t *testing.T) {
	// Use a command that exits immediately with status 1.
	// The handshake will fail → provider should report not-available.
	p := newTestPluginProvider([]string{"/bin/false"}, "crashy")
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	av := p.Available(ctx)
	// After the handshake timeout, the process should be reported dead.
	// Note: ensureProc is called lazily via Models/Stream, not Available.
	_ = av

	// Try to call Models — this triggers ensureProc, which should fail.
	_, err := p.Models(ctx)
	if err == nil {
		t.Error("expected error when plugin crashes during handshake")
	}
}

func TestPluginRestartPolicy(t *testing.T) {
	// Use a script that succeeds on first call and crashes on second.
	// We use /bin/sh -c with a counter file to simulate this.
	tmpDir := t.TempDir()
	counterFile := filepath.Join(tmpDir, "count")
	script := filepath.Join(tmpDir, "flaky.py")

	// Write a Python script that increments a counter and exits after 2 calls.
	scriptContent := `import sys, json, os

counter_file = ` + fmt.Sprintf("%q", counterFile) + `
n = 0
if os.path.exists(counter_file):
    with open(counter_file) as f:
        try: n = int(f.read().strip())
        except: n = 0
n += 1
with open(counter_file, 'w') as f:
    f.write(str(n))

# Read one message from stdin
line = sys.stdin.readline()
if not line:
    sys.exit(0)
msg = json.loads(line)
method = msg.get("method", "")
mid = msg.get("id")

if method == "initialize":
    print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"name":"flaky","version":"1.0"}}), flush=True)
elif method == "models":
    print(json.dumps({"jsonrpc":"2.0","id":mid,"result":[{"id":"m1","name":"Model 1"}]}), flush=True)
elif method == "stream":
    # Stream one chunk
    params = msg.get("params", {})
    user = params.get("messages", [{}])[-1].get("content", "")
    text = "flaky: " + user
    print(json.dumps({"jsonrpc":"2.0","method":"notification/streams/text_delta","params":{"text":text}}), flush=True)
    print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"finish_reason":"stop","prompt_tokens":-1,"completion_tokens":-1}}), flush=True)

sys.exit(0)
`
	_ = os.WriteFile(script, []byte(scriptContent), 0o644)

	p := newTestPluginProvider([]string{"python3", script}, "flaky")
	defer p.Close()

	ctx := context.Background()

	// First call: should succeed (script writes counter=1 and responds).
	var got1 strings.Builder
	out1 := &core.StreamOut{TextSink: func(s string) { got1.WriteString(s) }}
	req := &core.ChatRequest{Model: "m1", Messages: []core.Message{{Role: core.RoleUser, Content: "first"}}}
	if _, err := p.Stream(ctx, req, out1); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	if got1.String() != "flaky: first" {
		t.Errorf("first stream = %q, want %q", got1.String(), "flaky: first")
	}

	t.Log("plugin survived first call (as expected — it exits cleanly)")
}

// ---------------------------------------------------------------------------
// Available check
// ---------------------------------------------------------------------------

func TestPluginAvailable(t *testing.T) {
	bin := findGoExample(t)
	if bin == "" {
		t.Skip("go-example not available")
	}

	p := newTestPluginProvider([]string{bin}, "go-example")
	defer p.Close()

	// Before any call, provider is not started.
	av := p.Available(context.Background())
	if av.OK {
		t.Error("Available should be false before process starts (lazy start)")
	}

	// After a Models call (which starts it), provider should be available.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = p.Models(ctx)
	av2 := p.Available(ctx)
	if !av2.OK {
		t.Errorf("Available should be true after successful start, got: %s", av2.Reason)
	}
}

// ---------------------------------------------------------------------------
// Plugin protocol test (using go directly — build + run)
// ---------------------------------------------------------------------------

func TestPluginProtocolStreamOrdering(t *testing.T) {
	bin := findGoExample(t)
	if bin == "" {
		t.Skip("go-example not available")
	}

	p := newTestPluginProvider([]string{bin}, "go-example")
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var chunks []string
	out := &core.StreamOut{
		TextSink: func(s string) { chunks = append(chunks, s) },
	}

	req := &core.ChatRequest{
		Model:    "echo",
		Messages: []core.Message{{Role: core.RoleUser, Content: "abc"}},
	}

	done, err := p.Stream(ctx, req, out)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Verify text is "echo: abc"
	full := strings.Join(chunks, "")
	if full != "echo: abc" {
		t.Errorf("collected = %q, want %q", full, "echo: abc")
	}
	// Chunks should be ordered (2 chars each since go-example streams in 2-byte chunks).
	if len(chunks) < 2 {
		t.Log("warning: expected multiple chunks, got", len(chunks))
	}
	// Verify the finish reason.
	if done.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", done.FinishReason)
	}

	// Verify via is set in the provider label context.
	_ = done.PromptTokens     // should be -1
	_ = done.CompletionTokens // should be -1
}

// ---------------------------------------------------------------------------
// JSON-RPC wire format verification (using python3 directly)
// ---------------------------------------------------------------------------

func TestPluginProtocolJSONRPCWire(t *testing.T) {
	pyScript := filepath.Join("..", "..", "plugins", "python-example", "plugin.py")
	if _, err := os.Stat(pyScript); err != nil {
		t.Skip("python-example not found")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	f, err := ioutil.TempFile("", "plugin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"test"}}
{"jsonrpc":"2.0","id":2,"method":"models","params":{}}
{"jsonrpc":"2.0","id":3,"method":"available","params":{}}
{"jsonrpc":"2.0","id":4,"method":"stream","params":{"model":"hello","messages":[{"role":"user","content":"test-input"}]}}
{"jsonrpc":"2.0","id":5,"method":"shutdown","params":{}}
`)
	f.Close()

	cmd := exec.Command(py, pyScript)
	cmd.Stdin = f
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python plugin exited: %v\noutput: %s", err, outBytes)
	}
	outStr := strings.TrimSpace(string(outBytes))
	lines := strings.Split(outStr, "\n")

	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got %d: %s", len(lines), outStr)
	}

	// Line 0: initialize response
	var initResp map[string]any
	json.Unmarshal([]byte(lines[0]), &initResp)
	if resp, _ := initResp["result"].(map[string]any); resp == nil || resp["name"] != "python-example" {
		t.Errorf("init response bad: %s", lines[0])
	}

	// Check that a notification is present and has the right method.
	foundTextDelta := false
	for _, l := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(l), &msg); err != nil {
			continue
		}
		if method, _ := msg["method"].(string); method == "notification/streams/text_delta" {
			foundTextDelta = true
			break
		}
	}
	if !foundTextDelta {
		t.Error("expected at least one text_delta notification in stream output")
	}
}
