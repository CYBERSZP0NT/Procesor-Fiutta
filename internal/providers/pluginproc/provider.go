// Package pluginproc implements core.Provider for external plugin subprocesses
// that speak JSON-RPC 2.0 over stdio (LSP-style).
//
// The client:
//
//	– spawns the plugin process
//	– performs the initialize handshake (with timeout)
//	– routes Models/Available/Stream calls as JSON-RPC requests
//	– handles stream notifications (text_delta, tool_delta) as they arrive
//	– detects crashes → marks provider unavailable + optionally restarts
//	– captures stderr → slog (for observability)
//
// See docs/plugin-protocol.md for the full wire protocol.
package pluginproc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
)

// ---------------------------------------------------------------------------
// Provider — implements core.Provider
// ---------------------------------------------------------------------------

// Provider manages a plugin subprocess and provides core.Provider.
type Provider struct {
	cfg  procConfig
	kind core.Kind

	mu           sync.Mutex
	ph           *procHandle
	initialized  bool // session handshake succeeded (persists across process restarts)
	restarts     atomic.Int32
	cachedModels []core.Model
	catalogMu    sync.RWMutex
}

var _ core.Provider = (*Provider)(nil)

// CrashError indicates the plugin process died unexpectedly.
type CrashError struct{ Msg string }

func (e *CrashError) Error() string { return "plugin crashed: " + e.Msg }

type procConfig struct {
	Name             string
	Command          []string
	Env              map[string]string
	HandshakeTimeout time.Duration
	RestartPolicy    string // "never" | "on-crash"
	RestartMax       int    // max restarts before giving up (-1 = infinite)
	Logger           *slog.Logger
}

// New creates a plugin Provider from a config entry.
func New(p config.Provider, logger *slog.Logger) *Provider {
	handshake := 10 * time.Second
	if p.HandshakeTimeout > 0 {
		handshake = time.Duration(p.HandshakeTimeout) * time.Second
	}
	restartMax := -1
	if p.RestartMax > 0 {
		restartMax = p.RestartMax
	}
	restart := "on-crash"
	if p.RestartPolicy != "" {
		restart = p.RestartPolicy
	}
	return &Provider{
		cfg: procConfig{
			Name:             p.Name,
			Command:          p.Command,
			Env:              p.Env,
			HandshakeTimeout: handshake,
			RestartPolicy:    restart,
			RestartMax:       restartMax,
			Logger:           logger,
		},
		kind: core.KindPlugin,
	}
}

// Name returns the provider identifier.
func (p *Provider) Name() string { return p.cfg.Name }

// Kinds returns the transport kind.
func (p *Provider) Kinds() []core.Kind { return []core.Kind{core.KindPlugin} }

// Available reports whether the plugin process is alive and ready.
func (p *Provider) Available(ctx context.Context) *core.Availability {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ph == nil || p.ph.dead() {
		return &core.Availability{OK: false, Reason: "plugin process not running"}
	}
	return &core.Availability{OK: true, Reason: "plugin process running", Up: true}
}

// Models returns the plugin's model catalog via JSON-RPC "models" call.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	p.catalogMu.RLock()
	if len(p.cachedModels) > 0 {
		defer p.catalogMu.RUnlock()
		return p.cachedModels, nil
	}
	p.catalogMu.RUnlock()

	result, err := p.call(ctx, "models", map[string]any{})
	if err != nil {
		return nil, core.NewUpstreamErr(0, p.cfg.Name, "", err.Error(), err)
	}

	var models []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(result, &models); err != nil {
		return nil, fmt.Errorf("pluginproc[%s]: decode models: %w", p.cfg.Name, err)
	}

	coreModels := make([]core.Model, 0, len(models))
	for _, m := range models {
		coreModels = append(coreModels, core.Model{
			Provider: p.cfg.Name,
			ID:       m.ID,
			Name:     m.Name,
			Kinds:    []core.Kind{core.KindPlugin},
		})
	}

	p.catalogMu.Lock()
	p.cachedModels = coreModels
	p.catalogMu.Unlock()
	return coreModels, nil
}

// Stream sends a chat request to the plugin and consumes the response,
// including any streaming notifications (text_delta, tool_delta).
func (p *Provider) Stream(ctx context.Context, req *core.ChatRequest, out *core.StreamOut) (*core.StreamDone, error) {
	if out == nil {
		out = &core.StreamOut{}
	}
	if out.TextSink == nil {
		var buf strings.Builder
		out.TextSink = func(s string) { buf.WriteString(s) }
	}

	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{
			"role":    string(m.Role),
			"content": m.Content,
		})
	}
	params := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	if req.MaxTokens > 0 {
		params["max_tokens"] = req.MaxTokens
	}

	resultRaw, err := p.callStream(ctx, params, out)
	if err != nil {
		var crashErr *CrashError
		if errors.As(err, &crashErr) {
			p.handleCrash()
		}
		return nil, core.NewUpstreamErr(503, p.cfg.Name, req.Model, err.Error(), err)
	}

	var result struct {
		FinishReason     string `json:"finish_reason"`
		PromptTokens     int    `json:"prompt_tokens"`
		CompletionTokens int    `json:"completion_tokens"`
	}
	if len(resultRaw) > 0 {
		if err := json.Unmarshal(resultRaw, &result); err != nil {
			return nil, fmt.Errorf("pluginproc[%s]: decode stream result: %w", p.cfg.Name, err)
		}
	}
	if result.FinishReason == "" {
		result.FinishReason = "stop"
	}
	if result.PromptTokens == 0 {
		result.PromptTokens = -1
	}
	if result.CompletionTokens == 0 {
		result.CompletionTokens = -1
	}

	return &core.StreamDone{
		FinishReason:     result.FinishReason,
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
	}, nil
}

// Close shuts down the plugin process.
func (p *Provider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ph != nil {
		p.ph.kill()
		p.ph = nil
	}
}

// ---------------------------------------------------------------------------
// Call dispatch
// ---------------------------------------------------------------------------

// call makes a simple JSON-RPC request/response. If the plugin process
// dies mid-call and restart is allowed, the call is transparently retried
// once on a freshly (re)started process.
func (p *Provider) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ph, err := p.ensureProc(ctx)
		if err != nil {
			return nil, err
		}
		result, err := ph.doCall(ctx, method, params)
		if err == nil {
			return result, nil
		}
		if !ph.crashed(err) {
			return result, err
		}
		lastErr = &CrashError{Msg: "plugin process exited"}
		if !p.canRestart() {
			return nil, lastErr
		}
		p.dropProcess()
		continue // retry once on a fresh process
	}
	return nil, lastErr
}

// callStream makes a "stream" call that may produce notifications.
// Like call, a dead plugin is restarted (if allowed) and the stream re-sent
// exactly once.
func (p *Provider) callStream(ctx context.Context, params any, out *core.StreamOut) (json.RawMessage, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ph, err := p.ensureProc(ctx)
		if err != nil {
			return nil, err
		}
		result, err := ph.doCallWithNotifications(ctx, "stream", params, out, p.cfg.Name)
		if err == nil {
			return result, nil
		}
		if !ph.crashed(err) {
			return result, err
		}
		lastErr = &CrashError{Msg: "plugin process exited during stream"}
		if !p.canRestart() {
			return nil, lastErr
		}
		p.dropProcess()
		continue // retry once on a fresh process
	}
	return nil, lastErr
}

// canRestart reports whether a crashed plugin may be (re)started.
func (p *Provider) canRestart() bool {
	if p.cfg.RestartPolicy == "never" {
		return false
	}
	if p.cfg.RestartMax >= 0 {
		n := p.restarts.Add(1)
		if int(n) > p.cfg.RestartMax {
			return false
		}
	}
	return true
}

// dropProcess kills (if still running) and detaches the current process.
func (p *Provider) dropProcess() {
	p.mu.Lock()
	if p.ph != nil {
		p.ph.kill()
		p.ph = nil
	}
	p.mu.Unlock()
	// Brief pause before next start (no lock held).
	time.Sleep(50 * time.Millisecond)
}

// handleCrash is kept for stream paths that detect a CrashError.
func (p *Provider) handleCrash() {
	if !p.canRestart() {
		return
	}
	p.dropProcess()
}

// ensureProc starts the plugin process if not already running.
// Caller must NOT hold p.mu.
func (p *Provider) ensureProc(ctx context.Context) (*procHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ph != nil && !p.ph.dead() {
		return p.ph, nil
	}

	logger := p.cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Build the command.
	cmd := exec.Command(p.cfg.Command[0], p.cfg.Command[1:]...)
	if p.cfg.Env != nil {
		for k, v := range p.cfg.Env {
			cmd.Env = append(cmd.Environ(), k+"="+v)
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginproc[%s]: stdin: %w", p.cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginproc[%s]: stdout: %w", p.cfg.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginproc[%s]: stderr: %w", p.cfg.Name, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pluginproc[%s]: start: %w", p.cfg.Name, err)
	}

	ph := &procHandle{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 256*1024),
		// JSON-RPC request IDs must be non-zero; the read loop treats
		// ID == 0 as a server notification, so requests must start at 1.
		nextID: 1,
	}

	// Capture stderr in background.
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				logger.Warn("plugin-stderr", "provider", p.cfg.Name, "line", line)
			}
		}
	}()

	// Track process exit. Use Process.Wait (raw wait4) rather than cmd.Wait:
	// cmd.Wait additionally waits on internal pipe-copy goroutines, which can
	// stay blocked on the stdin pipe we keep open, delaying death detection.
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
		_ = stderr.Close()
	}()
	ph.exitCh = done

	// Handshake: initialize. The handshake is a *session*-level exchange —
	// once it succeeded for this provider, a restarted process (crash
	// recovery) is re-used without re-handshaking, so the re-sent request
	// is the first line the fresh process sees.
	if !p.initialized {
		hctx, cancel := context.WithTimeout(ctx, p.cfg.HandshakeTimeout)
		defer cancel()

		handshakeResult, err := ph.doCall(hctx, "initialize", map[string]any{})
		if err != nil {
			ph.kill()
			return nil, fmt.Errorf("pluginproc[%s]: handshake: %w", p.cfg.Name, err)
		}
		p.initialized = true
		logger.Debug("plugin initialized", "provider", p.cfg.Name, "resp", string(handshakeResult))
	}

	p.ph = ph
	logger.Info("plugin started", "provider", p.cfg.Name, "cmd", strings.Join(p.cfg.Command, " "))
	return ph, nil
}

// ---------------------------------------------------------------------------
// procHandle — subprocess I/O
// ---------------------------------------------------------------------------

type procHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	ioMu   sync.Mutex // serializes the write→read cycle
	nextID int64
	exitCh chan struct{}
}

func (h *procHandle) dead() bool {
	select {
	case <-h.exitCh:
		return true
	default:
		return false
	}
}

func (h *procHandle) kill() {
	if h.stdin != nil {
		_ = h.stdin.Close()
	}
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
}

// doCall makes a simple request/response call.
func (h *procHandle) doCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return h.doCallWithNotifications(ctx, method, params, nil, "")
}

// doCallWithNotifications handles a request that may be interleaved with
// notification messages (for streaming).
func (h *procHandle) doCallWithNotifications(
	ctx context.Context,
	method string,
	params any,
	out *core.StreamOut,
	provName string,
) (json.RawMessage, error) {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()

	id := h.nextID
	h.nextID++

	// Write the request.
	req := wireMsg{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  toRawJSON(params),
	}
	line, _ := json.Marshal(req)
	line = append(line, '\n')
	if _, err := h.stdin.Write(line); err != nil {
		return nil, fmt.Errorf("pluginproc[%s]: write: %w", provName, err)
	}

	// Run the read loop in a goroutine so we can watch ctx.Done().
	type readResult struct {
		data json.RawMessage
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		for {
			if h.dead() {
				ch <- readResult{err: fmt.Errorf("pluginproc[%s]: process exited", provName)}
				return
			}
			data, err := h.stdout.ReadBytes('\n')
			if err != nil {
				ch <- readResult{err: fmt.Errorf("pluginproc[%s]: read: %w", provName, err)}
				return
			}
			if len(data) > 0 && data[len(data)-1] == '\n' {
				data = data[:len(data)-1]
			}
			if len(data) == 0 {
				continue
			}
			var msg wireMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.ID == 0 {
				if out != nil {
					h.dispatchNotif(&msg, out, provName)
				}
				continue
			}
			if int64(msg.ID) != id {
				continue
			}
			if msg.Error != nil {
				ch <- readResult{err: fmt.Errorf("pluginproc[%s]: rpc error %d: %s", provName, msg.Error.Code, msg.Error.Message)}
				return
			}
			ch <- readResult{data: msg.Result}
			return
		}
	}()

	select {
	case res := <-ch:
		return res.data, res.err
	case <-ctx.Done():
		return nil, fmt.Errorf("pluginproc[%s]: context cancelled: %w", provName, ctx.Err())
	}
}

func (h *procHandle) dispatchNotif(msg *wireMsg, out *core.StreamOut, provName string) {
	switch msg.Method {
	case "notification/streams/text_delta":
		var p struct{ Text string }
		if msg.Params != nil {
			_ = json.Unmarshal(msg.Params, &p)
		}
		if out.TextSink != nil {
			out.TextSink(p.Text)
		}
		if out.Serving != nil {
			out.Serving(provName)
		}
	case "notification/streams/tool_delta":
		var p struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if msg.Params != nil {
			_ = json.Unmarshal(msg.Params, &p)
		}
		if out.ToolSink != nil {
			out.ToolSink(core.ToolCall{
				ID:       p.ID,
				Type:     "function",
				Function: core.ToolFunction{Name: p.Name, Arguments: p.Arguments},
			})
		}
	}
}

// crashed reports whether a failed call should be treated as a plugin crash
// (process gone). An io.EOF on the plugin's stdout is definitive: the write
// end closed only when the process exited, even if the reaper (exitCh) has
// not fired yet.
func (h *procHandle) crashed(err error) bool {
	if h.dead() {
		return true
	}
	if errors.Is(err, io.EOF) {
		select {
		case <-h.exitCh:
		case <-time.After(100 * time.Millisecond):
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Wire types (JSON-RPC 2.0)
// ---------------------------------------------------------------------------

type wireMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireErr        `json:"error,omitempty"`
}

func (m wireMsg) String() string {
	b, _ := json.Marshal(m)
	return string(b)
}

type wireErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func toRawJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}
