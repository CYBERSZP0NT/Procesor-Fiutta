// Package tui is the interactive terminal UI (`szmelc-router tui`):
// dashboard, usage & limits, full history, sessions, chat and the control
// plane, live against a running server or locally against the ledger.
package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/control"
	"github.com/silverx/szmelc-router/internal/core"
	"github.com/silverx/szmelc-router/internal/ledger"
	"github.com/silverx/szmelc-router/internal/obs"
	"github.com/silverx/szmelc-router/internal/usage"
	"github.com/silverx/szmelc-router/pkg/router"
)

// ControlState is the control tab's data.
type ControlState struct {
	Settings  control.Settings `json:"settings"`
	Effective control.Resolved `json:"effective"`
	TaskMode  string           `json:"task_mode"`
	Cooldowns map[string]int   `json:"cooldowns_s"`
}

// Backend is where the TUI gets its data and sends its commands.
type Backend interface {
	Describe() string
	Live() bool
	Usage() (*usage.Report, error)
	History(limit int) ([]ledger.Event, error)
	Control() (*ControlState, error)
	SetControl(s control.Settings) error
	// SaveControl persists s to the config file.
	SaveControl(s control.Settings) (string, error)
	ClearCooldowns(via string) (int, error)
	// Chat streams one turn; onDelta runs on the calling goroutine.
	Chat(ctx context.Context, model, session string, msgs []router.Message, onDelta func(string)) (via string, err error)
	Close() error
}

// ---------------------------------------------------------------------------
// live: a running server
// ---------------------------------------------------------------------------

type liveBackend struct {
	base    string
	key     string
	hc      *http.Client
	cfgPath string
}

// NewLive returns a backend for the server at base (http://host:port).
func NewLive(base, key, cfgPath string) Backend {
	return &liveBackend{base: strings.TrimRight(base, "/"), key: key, cfgPath: cfgPath, hc: &http.Client{Timeout: 10 * time.Second}}
}

// Ping reports whether a server answers at base.
func Ping(base, key string) bool {
	b := &liveBackend{base: strings.TrimRight(base, "/"), key: key, hc: &http.Client{Timeout: 800 * time.Millisecond}}
	var out map[string]any
	return b.do(context.Background(), http.MethodGet, "/v1/ping", nil, &out) == nil
}

func (b *liveBackend) Describe() string { return "live · " + b.base }
func (b *liveBackend) Live() bool       { return true }
func (b *liveBackend) Close() error     { return nil }

func (b *liveBackend) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.key != "" {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, ledger.Clip(string(data), 200))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (b *liveBackend) Usage() (*usage.Report, error) {
	var rep usage.Report
	return &rep, b.do(context.Background(), http.MethodGet, "/v1/usage", nil, &rep)
}

func (b *liveBackend) History(limit int) ([]ledger.Event, error) {
	var out struct {
		Data []ledger.Event `json:"data"`
	}
	err := b.do(context.Background(), http.MethodGet, fmt.Sprintf("/v1/history?limit=%d", limit), nil, &out)
	return out.Data, err
}

func (b *liveBackend) Control() (*ControlState, error) {
	var st ControlState
	return &st, b.do(context.Background(), http.MethodGet, "/v1/control", nil, &st)
}

func (b *liveBackend) SetControl(s control.Settings) error {
	return b.do(context.Background(), http.MethodPut, "/v1/control", s, nil)
}

func (b *liveBackend) SaveControl(s control.Settings) (string, error) {
	if err := b.SetControl(s); err != nil {
		return "", err
	}
	return b.cfgPath, config.SetUserAny(b.cfgPath, []string{"control"}, s)
}

func (b *liveBackend) ClearCooldowns(via string) (int, error) {
	var out struct {
		Cleared int `json:"cleared"`
	}
	path := "/v1/cooldowns"
	if via != "" {
		path += "?via=" + via
	}
	err := b.do(context.Background(), http.MethodDelete, path, nil, &out)
	return out.Cleared, err
}

func (b *liveBackend) Chat(ctx context.Context, model, session string, msgs []router.Message, onDelta func(string)) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	path := "/v1/chat/completions"
	if session != "" {
		path = "/s/" + session + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.key != "" {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	resp, err := (&http.Client{}).Do(req) // no client timeout: streams and forever mode
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	via := resp.Header.Get("X-Freellm-Routed-Via")
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return via, fmt.Errorf("%s", e.Error.Message)
		}
		return via, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				onDelta(c.Delta.Content)
			}
			if c.FinishReason == "error" {
				return via, fmt.Errorf("stream ended with an upstream error")
			}
		}
	}
	return via, sc.Err()
}

// ---------------------------------------------------------------------------
// local: the ledger file + an in-process router
// ---------------------------------------------------------------------------

type localBackend struct {
	cfg     *config.Config
	cfgPath string
	r       *router.Router
	rt      *obs.Runtime
	keys    func(string) int
	cache   *ledger.CachedReader
}

// NewLocal returns a backend that reads the ledger and routes in-process.
// Control changes apply to this process and are saved to the config.
func NewLocal(cfg *config.Config, cfgPath string, timeout time.Duration) (Backend, error) {
	r, err := router.NewFromConfig(cfg, timeout)
	if err != nil {
		return nil, err
	}
	rt := obs.Open(cfg, obs.Options{Source: "tui", Seed: true, DefaultPath: ledger.DefaultPath()})
	rt.Attach(r)
	src := core.NewEnvKeySource(cfg.KeyEnvMap())
	keys := func(p string) int { return len(src.Keys(context.Background(), strings.ToLower(p))) }
	return &localBackend{cfg: cfg, cfgPath: cfgPath, r: r, rt: rt, keys: keys,
		cache: &ledger.CachedReader{Path: rt.Path, Keep: cfg.Ledger.Keep}}, nil
}

func (b *localBackend) Describe() string {
	if b.rt.Path == "" {
		return "local · ledger disabled"
	}
	return "local · " + b.rt.Path
}
func (b *localBackend) Live() bool   { return false }
func (b *localBackend) Close() error { return b.rt.Close() }

func (b *localBackend) events() []ledger.Event {
	if b.rt.Path != "" {
		if evs, err := b.cache.Read(); err == nil {
			return evs
		}
	}
	return b.rt.Hub.Recent()
}

func (b *localBackend) Usage() (*usage.Report, error) {
	live := &usage.Live{Cooldowns: b.r.Cooldowns()}
	rep := usage.Build(time.Now(), usage.ProvidersFromConfig(b.cfg, b.keys), b.events(), live)
	rep.Live, rep.Source = false, b.rt.Path
	return rep, nil
}

func (b *localBackend) History(limit int) ([]ledger.Event, error) {
	evs := b.events()
	if limit > 0 && len(evs) > limit {
		evs = evs[len(evs)-limit:]
	}
	return evs, nil
}

func (b *localBackend) Control() (*ControlState, error) {
	s := b.rt.Ctrl.Settings()
	cds := map[string]int{}
	for via, d := range b.r.Cooldowns() {
		cds[via] = int(d.Seconds())
	}
	return &ControlState{Settings: s, Effective: s.Resolve(control.Override{}), TaskMode: b.r.TaskMode(), Cooldowns: cds}, nil
}

func (b *localBackend) SetControl(s control.Settings) error { return b.rt.Ctrl.Replace(s) }

func (b *localBackend) SaveControl(s control.Settings) (string, error) {
	if err := b.SetControl(s); err != nil {
		return "", err
	}
	return b.cfgPath, config.SetUserAny(b.cfgPath, []string{"control"}, s)
}

func (b *localBackend) ClearCooldowns(via string) (int, error) { return b.r.ClearCooldowns(via), nil }

func (b *localBackend) Chat(ctx context.Context, model, session string, msgs []router.Message, onDelta func(string)) (string, error) {
	var answer strings.Builder
	last := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			last = msgs[i].Content
			break
		}
	}
	ctx, call := b.rt.Begin(ctx, session, model, last, true)
	via, done, err := b.r.RouteWithOptions(ctx, model, msgs, "", nil, func(s string) {
		answer.WriteString(s)
		onDelta(s)
	})
	p, c := 0, 0
	if done != nil {
		p, c = done.PromptTokens, done.CompletionTokens
	}
	b.rt.End(call, via, p, c, answer.String(), err)
	return via, err
}
