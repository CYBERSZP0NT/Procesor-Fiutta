// Package pluginhost provides a minimal library for building Go plugins
// that speak the freellm-router JSON-RPC 2.0 over stdio protocol.
//
// A plugin is a subprocess that reads JSON-RPC messages on stdin and writes
// responses on stdout. The host (freellm-router) manages the process lifecycle
// (start, handshake, restart on crash, log capture).
//
// To build a Go plugin with one import:
//
//	package main
//
//	import (
//	    "log"
//	    "github.com/silverx/freellm-router/pkg/pluginhost"
//	)
//
//	func main() {
//	    h := pluginhost.New("my-plugin", "1.0")
//	    h.Models(func() ([]pluginhost.Model, error) {
//	        return []pluginhost.Model{{ID: "echo-1", Name: "Echo Model"}}, nil
//	    })
//	    h.Available(func() bool { return true })
//	    h.Stream(func(p *pluginhost.StreamParams, sink pluginhost.Sink) (*pluginhost.StreamDone, error) {
//	        sink.Text("echo: " + p.Messages[0].Content)
//	        return &pluginhost.StreamDone{FinishReason: "stop"}, nil
//	    })
//	    if err := h.Run(); err != nil {
//	        log.Fatal(err)
//	    }
//	}
//
// Protocol spec: docs/plugin-protocol.md
package pluginhost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Types exposed to plugin authors
// ---------------------------------------------------------------------------

// Model is a catalog entry returned by the Models handler.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// StreamParams is what the host sends in the "stream" method params.
type StreamParams struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Stream    bool      `json:"stream"`
}

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamDone is returned from the Stream handler to signal completion.
type StreamDone struct {
	FinishReason     string `json:"finish_reason"`
	PromptTokens     int    `json:"prompt_tokens"`     // -1 = unknown
	CompletionTokens int    `json:"completion_tokens"` // -1 = unknown
}

// Sink allows a Stream handler to emit text deltas (notifications) as they
// are generated.
type Sink struct {
	textFn func(string)
	toolFn func(ToolChunk)
}

// ToolChunk is an incremental tool-call fragment sent to the host during
// streaming.
type ToolChunk struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded; may be a fragment
}

// Text sends a text delta notification to the host.
func (s Sink) Text(t string) {
	if s.textFn != nil {
		s.textFn(t)
	}
}

// Tool sends a tool-call chunk notification to the host.
func (s Sink) Tool(tc ToolChunk) {
	if s.toolFn != nil {
		s.toolFn(tc)
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 wire types (shared with the host-side parser)
// ---------------------------------------------------------------------------

// rpcRequest is an incoming JSON-RPC request (from the host to the plugin).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is a response (from plugin to host).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcNotification is a notification (no id; one-way). Used for stream deltas.
type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Host — the main type plugin authors use
// ---------------------------------------------------------------------------

// Host is the plugin server. Configure handlers, then call Run().
type Host struct {
	name    string
	version string

	modelsFn    func() ([]Model, error)
	availFn     func() bool
	availReason string
	streamFn    func(p *StreamParams, sink Sink) (*StreamDone, error)

	// I/O (overridable for tests).
	in  io.Reader
	out io.Writer

	mu sync.Mutex // serializes writes to out

	running bool
}

// New creates a Host with the plugin's name and version.
func New(name, version string) *Host {
	return &Host{
		name:    name,
		version: version,
		in:      os.Stdin,
		out:     os.Stdout,
	}
}

// Models sets the model catalog handler.
func (h *Host) Models(fn func() ([]Model, error)) { h.modelsFn = fn }

// Available sets the availability check handler. Return true/false.
func (h *Host) Available(fn func() bool) { h.availFn = fn }

// AvailableWith sets the availability handler with a reason string.
func (h *Host) AvailableWith(fn func() (bool, string)) {
	h.availFn = func() bool {
		ok, reason := fn()
		h.availReason = reason
		return ok
	}
}

// Stream sets the streaming handler.
func (h *Host) Stream(fn func(p *StreamParams, sink Sink) (*StreamDone, error)) {
	h.streamFn = fn
}

// In sets the input reader (for tests).
func (h *Host) In(r io.Reader) { h.in = r }

// Out sets the output writer (for tests).
func (h *Host) Out(w io.Writer) { h.out = w }

// ---------------------------------------------------------------------------
// Run loop
// ---------------------------------------------------------------------------

// Run reads JSON-RPC requests from stdin and dispatches to registered
// handlers. Blocks until EOF or error.
func (h *Host) Run() error {
	h.running = true
	scanner := bufio.NewScanner(h.in)
	scanner.Buffer(make([]byte, 256*1024), 1024*1024) // up to 1 MB messages

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue // skip blank lines and non-JSON
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			// Write a parse error response if there was an id.
			if idFromRaw(line) != nil {
				h.writeResp(idFromRaw(line), nil, &rpcError{Code: -32700, Message: "parse error: " + err.Error()})
			}
			continue
		}

		var result any
		var rpcErr *rpcError

		switch req.Method {
		case "initialize":
			result = map[string]string{
				"name":    h.name,
				"version": h.version,
			}
		case "models":
			if h.modelsFn == nil {
				rpcErr = &rpcError{Code: -32000, Message: "models handler not set"}
			} else {
				models, err := h.modelsFn()
				if err != nil {
					rpcErr = &rpcError{Code: -32001, Message: err.Error()}
				} else {
					result = models
				}
			}
		case "available":
			ok := false
			reason := "not ready"
			if h.availFn != nil {
				ok = h.availFn()
				reason = h.availReason
				if reason == "" {
					if ok {
						reason = "ready"
					}
				}
			}
			result = map[string]any{"ok": ok, "reason": reason}
		case "stream":
			result, rpcErr = h.dispatchStream(req.ID, req.Params)
		case "shutdown":
			// Graceful exit: write response then return.
			if req.ID != nil {
				h.writeResp(req.ID, nil, nil)
			}
			return nil
		default:
			if req.ID != nil {
				rpcErr = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
			}
		}

		if req.ID != nil {
			h.writeResp(req.ID, result, rpcErr)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("pluginhost: read error: %w", err)
	}
	h.running = false
	return nil
}

func (h *Host) dispatchStream(id any, paramsRaw json.RawMessage) (any, *rpcError) {
	if h.streamFn == nil {
		return nil, &rpcError{Code: -32000, Message: "stream handler not set"}
	}

	var params StreamParams
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "bad params: " + err.Error()}
		}
	}

	sink := Sink{
		textFn: func(text string) {
			h.writeNotification("notification/streams/text_delta", map[string]string{"text": text})
		},
		toolFn: func(tc ToolChunk) {
			h.writeNotification("notification/streams/tool_delta", map[string]any{
				"id":        tc.ID,
				"name":      tc.Name,
				"arguments": tc.Arguments,
			})
		},
	}

	done, err := h.streamFn(&params, sink)
	if err != nil {
		return nil, &rpcError{Code: -32002, Message: err.Error()}
	}
	if done == nil {
		done = &StreamDone{FinishReason: "stop", PromptTokens: -1, CompletionTokens: -1}
	}
	if done.PromptTokens == 0 {
		done.PromptTokens = -1
	}
	if done.CompletionTokens == 0 {
		done.CompletionTokens = -1
	}
	return done, nil
}

// ---------------------------------------------------------------------------
// JSON-RPC I/O helpers
// ---------------------------------------------------------------------------

func (h *Host) writeResp(id any, result any, rpcErr *rpcError) {
	resp := rpcResponse{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		b, _ := json.Marshal(result)
		resp.Result = b
	}
	b, _ := json.Marshal(resp)
	h.writeLine(b)
}

func (h *Host) writeNotification(method string, params any) {
	notif := rpcNotification{JSONRPC: "2.0", Method: method, Params: toJSONRaw(params)}
	b, _ := json.Marshal(notif)
	h.writeLine(b)
}

func (h *Host) writeLine(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.out.Write(b)
	_, _ = h.out.Write([]byte("\n"))
}

func idFromRaw(line []byte) any {
	var v rune
	_ = v
	// Simplest: decode the "id" field from the raw request.
	var r struct {
		ID any `json:"id"`
	}
	if err := json.Unmarshal(line, &r); err == nil && r.ID != nil {
		return r.ID
	}
	return nil
}

func toJSONRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

// String returns the plugin name+version (for debugging).
func (h *Host) String() string {
	return strings.Join([]string{h.name, h.version}, " ")
}
