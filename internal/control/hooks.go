package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CallInfo is what a pre-call hook sees on stdin.
type CallInfo struct {
	RequestID string            `json:"request_id,omitempty"`
	Session   string            `json:"session,omitempty"`
	Source    string            `json:"source,omitempty"`
	Model     string            `json:"model"`
	Profile   string            `json:"profile"`
	Messages  int               `json:"messages"`
	Prompt    string            `json:"prompt,omitempty"` // last user message (clipped)
	Tools     bool              `json:"tools,omitempty"`
	Stream    bool              `json:"stream,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// Outcome is what a post-call hook sees on stdin.
type Outcome struct {
	CallInfo
	OK               bool     `json:"ok"`
	Via              string   `json:"via,omitempty"`
	Error            string   `json:"error,omitempty"`
	Attempts         int      `json:"attempts,omitempty"`
	Rounds           int      `json:"rounds,omitempty"`
	WaitedSec        float64  `json:"waited_s,omitempty"`
	LatencyMS        int64    `json:"latency_ms"`
	PromptTokens     int      `json:"prompt_tokens,omitempty"`
	CompletionTokens int      `json:"completion_tokens,omitempty"`
	Trail            []string `json:"trail,omitempty"`
}

// Verdict is a pre-call hook's decision.
type Verdict struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
	Model  string `json:"model,omitempty"` // non-empty: route this instead
}

// VetoError is returned when a pre-call hook (or a pause) refuses a call.
type VetoError struct{ Reason string }

func (e *VetoError) Error() string { return "vetoed: " + e.Reason }

func (h Hooks) timeout() time.Duration {
	if h.TimeoutSec > 0 {
		return time.Duration(h.TimeoutSec) * time.Second
	}
	return 10 * time.Second
}

// RunPre runs the pre-call hook (if any) and returns its verdict. No hook
// means allow. Failing to run the hook vetoes the call (fail closed: a
// guard that silently stops guarding is worse than a refused call).
func (h Hooks) RunPre(ctx context.Context, info CallInfo) Verdict {
	if len(h.PreCall) == 0 {
		return Verdict{Allow: true}
	}
	stdout, stderr, err := runHook(ctx, h.PreCall, info, h.timeout())
	var v Verdict
	if out := bytes.TrimSpace(stdout); len(out) > 0 && out[0] == '{' {
		if jerr := json.Unmarshal(out, &v); jerr == nil {
			if err != nil { // a non-zero exit always vetoes
				v.Allow = false
			}
			if !v.Allow && v.Reason == "" {
				v.Reason = hookReason(err, stderr)
			}
			return v
		}
	}
	if err != nil {
		return Verdict{Allow: false, Reason: hookReason(err, append(stderr, stdout...))}
	}
	return Verdict{Allow: true}
}

// RunPost runs the post-call hook in the background.
func (h Hooks) RunPost(o Outcome) {
	if len(h.PostCall) == 0 {
		return
	}
	go func() { _, _, _ = runHook(context.Background(), h.PostCall, o, h.timeout()) }()
}

func runHook(ctx context.Context, argv []string, payload any, timeout time.Duration) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in, _ := json.Marshal(payload)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(append(in, '\n'))
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("hook timed out after %s", timeout)
	}
	return o.Bytes(), e.Bytes(), err
}

func hookReason(err error, out []byte) string {
	msg := strings.TrimSpace(string(out))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	switch {
	case msg != "":
		return "pre-call hook: " + msg
	case err != nil:
		return "pre-call hook: " + err.Error()
	}
	return "pre-call hook refused"
}
