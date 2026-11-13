package server

// Tests for the Anthropic-compatible /v1/messages endpoint.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
)

// newAnthServer mirrors newObsServer with a fake OpenAI-like upstream that
// can also emit a tool call.
func newAnthServer(t *testing.T) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Model    string `json:"model"`
			Messages []struct {
				Role       string `json:"role"`
				Content    any    `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		w.Header().Set("Content-Type", "application/json")

		// Echo the number of tool-history messages so the test can assert
		// tool_use/tool_result translation reaches the upstream.
		toolMsgs := 0
		for _, m := range q.Messages {
			if m.ToolCallID != "" || m.Role == "assistant" {
				toolMsgs++
			}
		}
		if q.Model == "tool-model" {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\": \"Gdansk\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"anthropic hello (tool history: `+itoaSimple(toolMsgs)+`)"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	file := filepath.Join(t.TempDir(), "ledger.jsonl")
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "fake", Impl: "openailike", BaseURL: up.URL, Kind: "keyless"}},
		Routes:    map[string][]string{"auto": {"fake/good"}, "toolcall": {"fake/tool-model"}},
		Policies:  config.PolicyConfig{CooldownShortSec: 1, CooldownLongSec: 1, MaxChainAttempts: 4},
		Limits:    map[string]config.Limits{"fake": {RPM: 50}},
		Ledger:    config.LedgerConfig{File: file, Content: "none"},
		Logging:   config.LoggingConfig{Level: "error"},
	}
	s, err := New(cfg, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); _ = s.rt.Close() })
	return ts.URL
}

func itoaSimple(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestMessagesNonStream(t *testing.T) {
	url := newAnthServer(t)
	body := `{
		"model": "auto",
		"max_tokens": 100,
		"system": "be brief",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`
	req, _ := http.NewRequest("POST", url+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Fatalf("bad envelope: %+v", out)
	}
	if len(out.Content) == 0 || out.Content[0].Type != "text" || out.Content[0].Text == "" {
		t.Fatalf("no text block: %+v", out.Content)
	}
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q", out.StopReason)
	}
}

func TestMessagesSystemAndToolHistoryReachUpstream(t *testing.T) {
	url := newAnthServer(t)
	// assistant tool_use + user tool_result must survive translation as a
	// tool-capable history (the fake upstream counts assistant/tool msgs).
	body := `{
		"model": "auto",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "what is the weather?"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "checking"},
				{"type": "tool_use", "id": "call_1", "name": "get_weather", "input": {"city": "Gdansk"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": "sunny, 12C"}
			]}
		]
	}`
	req, _ := http.NewRequest("POST", url+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// assistant tool_use message (1) + tool result message (RoleTool maps to
	// assistant-count check in the fake: RoleTool has ToolCallID set) = 2
	if len(out.Content) == 0 || !strings.Contains(out.Content[0].Text, "tool history: 2") {
		t.Fatalf("tool history lost upstream: %+v", out.Content)
	}
}

func TestMessagesToolCallResponse(t *testing.T) {
	url := newAnthServer(t)
	body := `{"model":"toolcall","max_tokens":100,"messages":[{"role":"user","content":"weather?"}]}`
	req, _ := http.NewRequest("POST", url+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", out.StopReason)
	}
	var tu *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	for i := range out.Content {
		if out.Content[i].Type == "tool_use" {
			tu = &struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}{out.Content[i].ID, out.Content[i].Name}
		}
	}
	if tu == nil {
		t.Fatalf("no tool_use block: %+v", out.Content)
	}
	if tu.Name != "get_weather" || tu.ID != "call_1" {
		t.Fatalf("bad tool_use: %+v", tu)
	}
}

func TestMessagesStreamShape(t *testing.T) {
	url := newAnthServer(t)
	body := `{"model":"auto","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", url+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
		`"stop_reason":"end_turn"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("stream missing %q; body:\n%s", want, s)
		}
	}
}

func TestMessagesToolChoiceMapping(t *testing.T) {
	if got := anthToolChoiceString(json.RawMessage(`{"type":"any"}`)); got != "required" {
		t.Fatalf("any -> %q", got)
	}
	if got := anthToolChoiceString(json.RawMessage(`{"type":"tool","name":"read"}`)); got != "function:read" {
		t.Fatalf("tool -> %q", got)
	}
	if got := anthToolChoiceString(json.RawMessage(`{"type":"auto"}`)); got != "auto" {
		t.Fatalf("auto -> %q", got)
	}
	if got := anthToolChoiceString(json.RawMessage(`{"type":"none"}`)); got != "none" {
		t.Fatalf("none -> %q", got)
	}
}

func TestMessagesInvalidRequests(t *testing.T) {
	url := newAnthServer(t)
	cases := []struct {
		body string
		code int
	}{
		{`{"max_tokens":1,"messages":[{"role":"user","content":"x"}]}`, 400}, // no model
		{`{"model":"auto","max_tokens":1,"messages":[]}`, 400},               // no messages
		{`not json`, 400},                        // bad json
		{`{"model":"auto","max_tokens":1}`, 400}, // missing messages field
	}
	for _, c := range cases {
		req, _ := http.NewRequest("POST", url+"/v1/messages", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Fatalf("body %q: status %d (want %d): %s", c.body, resp.StatusCode, c.code, b)
		}
		var e struct {
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatalf("error body not JSON: %s", b)
		}
		if e.Type != "error" || e.Error.Type == "" {
			t.Fatalf("bad error shape: %s", b)
		}
	}
}
