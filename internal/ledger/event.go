// Package ledger is the single event log of everything freellm-router does:
// client requests, every upstream call (attempt), task-router decisions,
// sessions and operator control changes. It is one append-only JSONL file
// (the usage journal, rotated by the server) that every process — server,
// `chat`, `exec`, `wrap`, `tui` — appends to, and that `history`, `usage`,
// `stats` and the TUI read back.
//
// Lines written before the ledger existed (the plain usage journal) have no
// "kind"; they are read as requests, so old history keeps working.
package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/silverx/freellm-router/internal/core"
)

// Event kinds.
const (
	KindRequest = "request" // one client call (server request, CLI chat/exec)
	KindAttempt = "attempt" // one upstream call made while serving a request
	KindTask    = "task"    // a task-router decision (model "agent")
	KindSession = "session" // a session started or ended (wrap, tui, exec)
	KindControl = "control" // an operator changed the control settings
	KindHTTP    = "http"    // a non-chat HTTP request (models, health, …)
)

// Kinds lists every kind, in display order.
var Kinds = []string{KindRequest, KindAttempt, KindTask, KindSession, KindControl, KindHTTP}

// Event is one ledger line. Field names of the original usage journal are
// kept (t, path, status, key, latency_ms, model, via, route, stream, tokens,
// error) so old files and the `stats` command read the same data.
type Event struct {
	T         time.Time `json:"t"`
	Kind      string    `json:"kind,omitempty"`
	ID        string    `json:"request_id,omitempty"` // request id (attempts/task share their request's)
	Session   string    `json:"session,omitempty"`
	Source    string    `json:"source,omitempty"` // server | cli | exec | wrap | tui
	Path      string    `json:"path,omitempty"`
	Method    string    `json:"method,omitempty"`
	Status    int       `json:"status,omitempty"`
	Key       string    `json:"key,omitempty"` // redacted client key
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Client    string    `json:"client,omitempty"` // user agent

	Model    string  `json:"model,omitempty"` // what the client asked for
	Route    string  `json:"route,omitempty"` // selector used for routing
	Via      string  `json:"via,omitempty"`   // provider/model that served (or was tried)
	Provider string  `json:"provider,omitempty"`
	Stream   bool    `json:"stream,omitempty"`
	Tokens   *Tokens `json:"tokens,omitempty"`
	Err      string  `json:"error,omitempty"`

	Attempt   int             `json:"attempt,omitempty"`  // attempt: 1-based number
	Attempts  int             `json:"attempts,omitempty"` // request: upstream calls made
	Trail     []string        `json:"trail,omitempty"`    // request: entries skipped/failed
	RateLimit *core.RateLimit `json:"ratelimit,omitempty"`
	CooldownS float64         `json:"cooldown_s,omitempty"` // attempt: cooldown applied
	Task      *TaskInfo       `json:"task,omitempty"`
	Profile   string          `json:"profile,omitempty"`  // control profile in effect
	Waited    float64         `json:"waited_s,omitempty"` // request: time spent waiting for cooldowns
	Rounds    int             `json:"rounds,omitempty"`   // request: chain walks (forever mode)

	Action string            `json:"action,omitempty"` // session: start|end; control: what changed
	Note   string            `json:"note,omitempty"`
	Tags   map[string]string `json:"tags,omitempty"`

	Prompt string `json:"prompt,omitempty"` // last user message (per ledger.content)
	Answer string `json:"answer,omitempty"` // answer text (per ledger.content)
}

// Tokens holds per-call token counts (-1 = upstream did not report).
type Tokens struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
}

// Total returns prompt+completion, counting unreported (negative) as 0.
func (t *Tokens) Total() int {
	if t == nil {
		return 0
	}
	return max(t.Prompt, 0) + max(t.Completion, 0)
}

// TaskInfo is the part of a task-router decision worth keeping.
type TaskInfo struct {
	Mode     string   `json:"mode,omitempty"`
	Role     string   `json:"role,omitempty"`
	Tier     int      `json:"tier,omitempty"`
	Escalate int      `json:"escalation,omitempty"`
	Chain    []string `json:"chain,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// EffectiveKind returns Kind, or "request" for legacy lines without one.
func (e *Event) EffectiveKind() string {
	if e.Kind == "" {
		if e.Path != "" && !strings.Contains(e.Path, "chat/completions") {
			return KindHTTP
		}
		return KindRequest
	}
	return e.Kind
}

// ProviderName returns Provider, or the provider part of Via.
func (e *Event) ProviderName() string {
	if e.Provider != "" {
		return e.Provider
	}
	if i := strings.IndexByte(e.Via, '/'); i > 0 {
		return e.Via[:i]
	}
	return e.Via
}

// Failed reports whether the event records a failure.
func (e *Event) Failed() bool { return e.Status >= 400 || e.Err != "" }

// NewID returns a short random id (request/session ids).
func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// DefaultPath is where the ledger lives unless configured otherwise:
// $XDG_DATA_HOME/freellm/usage.jsonl (~/.local/share/freellm/usage.jsonl).
// The server, the CLI and `stats` all use this one helper.
func DefaultPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "freellm", "usage.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".local", "share", "freellm", "usage.jsonl")
	}
	return filepath.Join(home, ".local", "share", "freellm", "usage.jsonl")
}

// Clip shortens s to n runes (adding "…") for previews.
func Clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------------------
// Request-scoped metadata
// ---------------------------------------------------------------------------

// Call is what one request carries through the layers below it: who it
// is (id, session, source) and the request event being filled in.
type Call struct {
	ID      string
	Session string
	Source  string
	Profile string
	Tags    map[string]string
	// Event is the request's own record; handlers fill model/via/tokens/
	// error into it and whoever created it writes it once at the end.
	Event *Event
}

type callKey struct{}

// WithCall returns ctx carrying c.
func WithCall(ctx context.Context, c *Call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

// CallFrom returns the call stored in ctx, or nil.
func CallFrom(ctx context.Context) *Call {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}
