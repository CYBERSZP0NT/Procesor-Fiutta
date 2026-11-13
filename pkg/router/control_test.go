package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/silverx/szmelc-router/internal/control"
	"github.com/silverx/szmelc-router/internal/core"
	"github.com/silverx/szmelc-router/internal/ledger"
)

// flaky answers 429 for the first `fail` calls, then succeeds.
func flaky(t *testing.T, fail int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= fail {
			w.Header().Set("x-ratelimit-remaining-requests", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-ratelimit-remaining-requests", "41")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func newTestRouter(t *testing.T, urls ...string) *Router {
	t.Helper()
	var pcs []ProviderConfig
	var chain []string
	for i, u := range urls {
		name := string(rune('a' + i))
		pcs = append(pcs, ProviderConfig{Name: name, Impl: "openailike", BaseURL: u, Kind: "keyless"})
		chain = append(chain, name+"/m")
	}
	r, err := New(Config{Providers: pcs, Routes: map[string][]string{"r": chain}, CooldownShort: 50 * time.Millisecond, CooldownLong: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func msgs() []Message { return []Message{{Role: "user", Content: "hi"}} }

func TestForeverWaitsOutCooldowns(t *testing.T) {
	srv, n := flaky(t, 2)
	r := newTestRouter(t, srv.URL)
	r.SetControl(control.New(control.Settings{Forever: control.Bool(true), Escalate: control.String("")}))
	old := foreverMinWait
	foreverMinWait = 10 * time.Millisecond
	defer func() { foreverMinWait = old }()

	var attempts []core.AttemptInfo
	r.SetOnAttempt(func(_ context.Context, a core.AttemptInfo) { attempts = append(attempts, a) })
	ev := &ledger.Event{}
	ctx := ledger.WithCall(context.Background(), &ledger.Call{ID: "x", Event: ev})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	via, done, err := r.Route(ctx, "r", msgs(), "", nil)
	if err != nil || via != "a/m" || done.Text != "ok" {
		t.Fatalf("via=%q err=%v", via, err)
	}
	if n.Load() != 3 || ev.Rounds != 3 || ev.Attempts != 3 || ev.Waited <= 0 {
		t.Fatalf("calls=%d rounds=%d attempts=%d waited=%v", n.Load(), ev.Rounds, ev.Attempts, ev.Waited)
	}
	if len(attempts) != 3 || attempts[0].Status != 429 || attempts[2].RateLimit == nil || *attempts[2].RateLimit.RemainingRequests != 41 {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestForeverRespectsMaxWait(t *testing.T) {
	srv, _ := flaky(t, 1000)
	r := newTestRouter(t, srv.URL)
	r.SetControl(control.New(control.Settings{Profile: "auto", MaxWaitSec: 1, Escalate: control.String("")}))
	t0 := time.Now()
	_, _, err := r.Route(context.Background(), "r", msgs(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "forever mode gave up") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestControlProfileNoFallback(t *testing.T) {
	bad, _ := flaky(t, 1000)
	good, gn := flaky(t, 0)
	r := newTestRouter(t, bad.URL, good.URL)
	r.SetControl(control.New(control.Settings{}))
	if via, _, err := r.Route(context.Background(), "r", msgs(), "", nil); err != nil || via != "b/m" {
		t.Fatalf("balanced: via=%q err=%v", via, err)
	}
	r.ClearCooldowns("")
	ctx := WithOverride(context.Background(), Override{Profile: "control"})
	_, _, err := r.Route(ctx, "r", msgs(), "", nil)
	if err == nil || gn.Load() != 1 {
		t.Fatalf("control must not fall back: err=%v good calls=%d", err, gn.Load())
	}
}

func TestPauseDisableAndLimiter(t *testing.T) {
	a, _ := flaky(t, 0)
	b, bn := flaky(t, 0)
	r := newTestRouter(t, a.URL, b.URL)
	ctrl := control.New(control.Settings{Paused: true})
	r.SetControl(ctrl)
	_, _, err := r.Route(context.Background(), "r", msgs(), "", nil)
	var ve *VetoError
	if !errors.As(err, &ve) {
		t.Fatalf("paused: %v", err)
	}
	_ = ctrl.Replace(control.Settings{Disabled: []string{"a"}})
	if via, _, err := r.Route(context.Background(), "r", msgs(), "", nil); err != nil || via != "b/m" {
		t.Fatalf("disabled a: via=%q err=%v", via, err)
	}
	_ = ctrl.Replace(control.Settings{EnforceLimits: control.Bool(true)})
	r.SetLimiter(limiterFunc(func(p string) (bool, string) { return p != "a", "budget" }))
	if via, _, err := r.Route(context.Background(), "r", msgs(), "", nil); err != nil || via != "b/m" || bn.Load() != 2 {
		t.Fatalf("limited a: via=%q err=%v", via, err)
	}
}

type limiterFunc func(string) (bool, string)

func (f limiterFunc) Allow(p string) (bool, string) { return f(p) }
