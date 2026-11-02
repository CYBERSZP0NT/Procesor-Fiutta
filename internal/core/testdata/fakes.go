// Package testdata provides reusable httptest fakes for openailike provider
// and router integration tests.
package testdata

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
)

// FakeUpstream is a controllable OpenAI-compatible mock server.
type FakeUpstream struct {
	*httptest.Server

	// Behavior controls (set before starting or atomically at runtime).
	StatusCode int    // non-200 → error response
	ErrMsg     string // body for error responses
	Stream     bool   // if true, respond with SSE

	// Response data.
	Content   string            // text content to return
	Finish    string            // finish_reason
	ToolCalls []ToolCallFixture // tool calls in response
	PromptTok int
	CompTok   int

	// Counters.
	CallCount     int64        // total stream calls made
	HeaderSeen    atomic.Value // last Authorization header seen
	CustomHdrSet  atomic.Value // last custom auth header seen (e.g. x-goog-api-key)
	CustomHdrName string       // name to check for custom header

	// Behavior on retry: set a list of status codes to return in order.
	StatusQueue []int
	StatusIdx   int32 // atomic index into StatusQueue
	StatusMu    sync.Mutex

	Mu     sync.Mutex
	status int
	msg    string
}

// ToolCallFixture holds a simplified tool call to return.
type ToolCallFixture struct {
	ID        string
	Name      string
	Arguments string
}

// New creates a FakeUpstream with default behavior (200, "Hello!", non-streaming).
func New() *FakeUpstream {
	f := &FakeUpstream{
		StatusCode: 200,
		Content:    "Hello!",
		Finish:     "stop",
		Stream:     false,
		PromptTok:  10,
		CompTok:    5,
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

// NewStream creates a streaming (SSE) fake upstream.
func NewStream() *FakeUpstream {
	f := New()
	f.Stream = true
	f.Content = "Hi there"
	return f
}

// SetStatus changes the response status.
func (f *FakeUpstream) SetStatus(code int) {
	f.Mu.Lock()
	f.status = code
	f.Mu.Unlock()
}

// SetContent sets the text content.
func (f *FakeUpstream) SetContent(s string) {
	f.Mu.Lock()
	f.Content = s
	f.Mu.Unlock()
}

// SetStatusQueue makes the fake return status codes in sequence, one per call.
func (f *FakeUpstream) SetStatusQueue(codes ...int) {
	f.StatusQueue = codes
	f.atomicStoreInt32(&f.StatusIdx, 0)
}

func (f *FakeUpstream) atomicStoreInt32(p *int32, v int32) {
	for {
		old := atomic.LoadInt32(p)
		if atomic.CompareAndSwapInt32(p, old, v) {
			return
		}
	}
}

func (f *FakeUpstream) CallCountVal() int64 {
	return atomic.LoadInt64(&f.CallCount)
}

func (f *FakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&f.CallCount, 1)

	// Record auth header.
	if auth := r.Header.Get("Authorization"); auth != "" {
		f.HeaderSeen.Store(auth)
	}
	if f.CustomHdrName != "" {
		if v := r.Header.Get(f.CustomHdrName); v != "" {
			f.CustomHdrSet.Store(v)
		}
	}

	switch r.URL.Path {
	case "/models", "/v1/models":
		f.handleModels(w)
		return
	case "/chat/completions", "/v1/chat/completions":
	default:
		http.NotFound(w, r)
		return
	}

	// Determine status.
	status := http.StatusOK
	f.Mu.Lock()
	if f.status != 0 {
		status = f.status
		f.status = 0
	}
	f.Mu.Unlock()

	// Queue: pop next status if available.
	if len(f.StatusQueue) > 0 {
		idx := int(atomic.AddInt32(&f.StatusIdx, 1) - 1)
		if idx < len(f.StatusQueue) {
			status = f.StatusQueue[idx]
		}
	}

	if status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		msg := f.ErrMsg
		if msg == "" {
			msg = "simulated error"
		}
		fmt.Fprintf(w, `{"error":{"message":"%s","type":"server_error"}}`, msg)
		return
	}

	// Get request content (not strictly needed for response).
	body, _ := io.ReadAll(r.Body)
	_ = body

	// Build response.
	content, finish, toolCalls, ptok, ctok := f.getResponseData()

	if f.Stream {
		f.writeSSE(w, content, finish, toolCalls, ptok, ctok)
	} else {
		f.writeJSON(w, content, finish, toolCalls, ptok, ctok)
	}
}

func (f *FakeUpstream) getResponseData() (string, string, []ToolCallFixture, int, int) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Content, f.Finish, f.ToolCalls, f.PromptTok, f.CompTok
}

func (f *FakeUpstream) handleModels(w http.ResponseWriter) {
	f.Mu.Lock()
	content := f.Content
	f.Mu.Unlock()
	_ = content

	w.Header().Set("Content-Type", "application/json")
	// Return a minimal model list based on the Content field or default.
	modelID := "test-model"
	fmt.Fprintf(w, `{"data":[{"id":"%s","name":"Test Model","object":"model"}]}`, modelID)
}

func (f *FakeUpstream) writeJSON(w http.ResponseWriter, content, finish string, toolCalls []ToolCallFixture, ptok, ctok int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)

	resp := map[string]any{
		"id":     "chatcmpl-fake",
		"object": "chat.completion",
		"model":  "test-model",
		"choices": []map[string]any{
			{
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finish,
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     ptok,
			"completion_tokens": ctok,
			"total_tokens":      ptok + ctok,
		},
	}

	if len(toolCalls) > 0 {
		tcs := make([]map[string]any, 0, len(toolCalls))
		for _, tc := range toolCalls {
			tcs = append(tcs, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		resp["choices"].([]map[string]any)[0]["message"].(map[string]any)["tool_calls"] = tcs
	}

	json.NewEncoder(w).Encode(resp)
}

func (f *FakeUpstream) writeSSE(w http.ResponseWriter, content, finish string, toolCalls []ToolCallFixture, ptok, ctok int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

	flusher, _ := w.(http.Flusher)

	// Write content as 2 chunks for realism.
	if len(content) > 2 {
		half := len(content) / 2
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fake\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"%s\"},\"finish_reason\":null}]}\n\n",
			jsonEscape(content[:half]))
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fake\",\"choices\":[{\"delta\":{\"content\":\"%s\"},\"finish_reason\":null}]}\n\n",
			jsonEscape(content[half:]))
		if flusher != nil {
			flusher.Flush()
		}
	} else if content != "" {
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fake\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"%s\"},\"finish_reason\":null}]}\n\n",
			jsonEscape(content))
		if flusher != nil {
			flusher.Flush()
		}
	}

	// Write tool calls if any.
	for _, tc := range toolCalls {
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fake\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"%s\",\"type\":\"function\",\"function\":{\"name\":\"%s\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n",
			tc.ID, tc.Name)
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fake\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"%s\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n",
			jsonEscape(tc.Arguments))
		if flusher != nil {
			flusher.Flush()
		}
	}

	// Final chunk with usage.
	if finish == "" {
		finish = "stop"
	}
	endReason := finish
	if len(toolCalls) > 0 {
		endReason = "tool_calls"
	}
	fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fake\",\"choices\":[{\"delta\":{},\"finish_reason\":\"%s\"}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n",
		endReason, ptok, ctok, ptok+ctok)

	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), "\"")
}
