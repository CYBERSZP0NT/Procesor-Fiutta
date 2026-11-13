// Package obs wires observability and control into a router: every
// process (server, chat, exec, wrap, tui) builds one Runtime, so they all
// log the same events to the same ledger, account quota the same way and
// obey the same control settings.
package obs

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/control"
	"github.com/silverx/szmelc-router/internal/core"
	"github.com/silverx/szmelc-router/internal/ledger"
	"github.com/silverx/szmelc-router/internal/usage"
	"github.com/silverx/szmelc-router/pkg/router"
)

// Runtime is one process's ledger + quota tracker + controller.
type Runtime struct {
	Hub     *ledger.Hub
	Tracker *usage.Tracker
	Ctrl    *control.Controller
	Path    string // ledger file ("" = not persisted)
	Keep    int
	Source  string
	content string
	cfg     *config.Config
}

// Options tune Open.
type Options struct {
	Source string // server | cli | exec | wrap | tui
	// Rotate selects the rotating writer (only the long-running server
	// should rotate; others append).
	Rotate bool
	// Seed preloads the in-memory ring and the quota tracker from the
	// ledger file (servers and long-lived clients).
	Seed bool
	// DefaultPath is the ledger file used when the config names none ("" =
	// in-memory only). The CLI passes ledger.DefaultPath(); server.New
	// passes "" so tests and library users never write to $HOME.
	DefaultPath string
}

// Open builds a Runtime from cfg. A ledger that cannot be opened degrades
// to in-memory only; it never stops the router from working.
func Open(cfg *config.Config, o Options) *Runtime {
	rt := &Runtime{
		Path:    cfg.LedgerPath(o.DefaultPath),
		Keep:    cfg.Ledger.Keep,
		Source:  o.Source,
		content: strings.ToLower(cfg.Ledger.Content),
		cfg:     cfg,
		Ctrl:    control.New(cfg.Control),
		Tracker: usage.NewTracker(cfg.LimitsFor),
	}
	var sink ledger.Sink = ledger.Discard{}
	if rt.Path != "" {
		var err error
		if o.Rotate {
			sink, err = ledger.OpenRotating(rt.Path, cfg.Ledger.MaxBytes, cfg.Ledger.Keep)
		} else {
			sink, err = ledger.OpenAppend(rt.Path)
		}
		if err != nil {
			sink, rt.Path = ledger.Discard{}, ""
		}
	}
	rt.Hub = ledger.NewHub(5000, sink)
	if o.Seed && rt.Path != "" {
		if evs, err := ledger.ReadFile(rt.Path, rt.Keep); err == nil {
			cut := time.Now().Add(-24 * time.Hour)
			recent := evs
			for i := range evs {
				if !evs[i].T.Before(cut) {
					recent = evs[i:]
					break
				}
			}
			rt.Hub.Seed(recent)
			rt.Tracker.Seed(recent)
		}
	}
	return rt
}

// Close flushes the ledger.
func (rt *Runtime) Close() error { return rt.Hub.Close() }

// Attach makes r log attempts/task decisions and obey the controller and
// quota tracker.
func (rt *Runtime) Attach(r *router.Router) {
	r.SetControl(rt.Ctrl)
	r.SetLimiter(rt.Tracker)
	r.SetOnAttempt(func(ctx context.Context, a core.AttemptInfo) {
		rt.Tracker.Observe(a)
		e := ledger.Event{
			Kind: ledger.KindAttempt, Source: rt.Source, Via: a.Via, Provider: a.Provider,
			Model: a.Model, Attempt: a.N, Status: a.Status, LatencyMS: a.Latency.Milliseconds(),
			RateLimit: a.RateLimit, CooldownS: a.Cooldown.Seconds(),
		}
		if a.Err != nil {
			e.Err = ledger.Clip(a.Err.Error(), 500)
		}
		if a.Done != nil {
			e.Tokens = &ledger.Tokens{Prompt: a.Done.PromptTokens, Completion: a.Done.CompletionTokens}
		}
		stamp(ctx, &e)
		_ = rt.Hub.Write(e)
	})
	r.SetOnTask(func(ctx context.Context, d *router.TaskDecision) {
		if d.Via == "" { // the pick; the final call repeats it with Via set
			e := ledger.Event{Kind: ledger.KindTask, Source: rt.Source, Model: d.Need.Role,
				Task: &ledger.TaskInfo{Mode: d.Mode, Role: d.Need.Role, Tier: d.Required,
					Escalate: d.Attempt, Chain: d.Chain, Reason: d.Reason}}
			stamp(ctx, &e)
			_ = rt.Hub.Write(e)
		}
	})
	rt.Ctrl.OnChange(func(_, s control.Settings) {
		_ = rt.Hub.Write(ledger.Event{Kind: ledger.KindControl, Source: rt.Source, Action: "update",
			Profile: s.Profile, Note: describe(s)})
	})
}

// stamp copies request identity from ctx into e.
func stamp(ctx context.Context, e *ledger.Event) {
	if c := ledger.CallFrom(ctx); c != nil {
		e.ID, e.Session, e.Profile = c.ID, c.Session, c.Profile
		if c.Source != "" {
			e.Source = c.Source
		}
	}
}

// Capture applies ledger.content to a prompt/answer pair.
func (rt *Runtime) Capture(s string) string {
	switch rt.content {
	case "off", "none":
		return ""
	case "full":
		return s
	}
	return ledger.Clip(s, 200)
}

// Begin starts a client-side request (CLI chat/exec): it returns a ctx
// carrying the call and the call itself; finish it with End.
func (rt *Runtime) Begin(ctx context.Context, session, model, prompt string, stream bool) (context.Context, *ledger.Call) {
	c := &ledger.Call{ID: ledger.NewID(), Session: session, Source: rt.Source}
	c.Event = &ledger.Event{T: time.Now(), Kind: ledger.KindRequest, ID: c.ID, Session: session,
		Source: rt.Source, Model: model, Route: model, Stream: stream, Prompt: rt.Capture(prompt)}
	return ledger.WithCall(ctx, c), c
}

// End completes and writes the request event.
func (rt *Runtime) End(c *ledger.Call, via string, prompt, completion int, answer string, err error) {
	e := c.Event
	e.LatencyMS = time.Since(e.T).Milliseconds()
	e.Via = via
	e.Provider = ""
	if via != "" {
		e.Provider = strings.SplitN(via, "/", 2)[0]
	}
	e.Profile = c.Profile
	if err != nil {
		e.Status, e.Err = statusOf(err), ledger.Clip(err.Error(), 1000)
	} else {
		e.Status = 200
		e.Tokens = &ledger.Tokens{Prompt: prompt, Completion: completion}
		e.Answer = rt.Capture(answer)
	}
	_ = rt.Hub.Write(*e)
}

// Session writes a session start/end marker.
func (rt *Runtime) Session(id, action, note string) {
	_ = rt.Hub.Write(ledger.Event{Kind: ledger.KindSession, Session: id, Source: rt.Source, Action: action, Note: note})
}

func statusOf(err error) int {
	if _, ok := err.(*control.VetoError); ok {
		return 403
	}
	if ue, ok := core.IsUpstreamErr(err); ok && ue.Status >= 400 {
		return ue.Status
	}
	return 502
}

func describe(s control.Settings) string {
	var parts []string
	if s.Profile != "" {
		parts = append(parts, "profile="+s.Profile)
	}
	if s.Paused {
		parts = append(parts, "paused")
	}
	if len(s.Disabled) > 0 {
		parts = append(parts, "disabled="+strings.Join(s.Disabled, ","))
	}
	if len(s.Allow) > 0 {
		parts = append(parts, "allow="+strings.Join(s.Allow, ","))
	}
	b := func(name string, v *bool) {
		if v != nil {
			if *v {
				parts = append(parts, name+"=on")
			} else {
				parts = append(parts, name+"=off")
			}
		}
	}
	b("fallback", s.Fallback)
	b("forever", s.Forever)
	b("enforce_limits", s.EnforceLimits)
	if s.MaxAttempts > 0 {
		parts = append(parts, "max_attempts="+strconv.Itoa(s.MaxAttempts))
	}
	if s.MaxWaitSec > 0 {
		parts = append(parts, "max_wait="+strconv.Itoa(s.MaxWaitSec)+"s")
	}
	if s.Escalate != nil {
		parts = append(parts, "escalate="+*s.Escalate)
	}
	if len(s.Hooks.PreCall) > 0 {
		parts = append(parts, "pre_call="+strings.Join(s.Hooks.PreCall, " "))
	}
	if len(s.Hooks.PostCall) > 0 {
		parts = append(parts, "post_call="+strings.Join(s.Hooks.PostCall, " "))
	}
	if len(parts) == 0 {
		return "defaults"
	}
	return strings.Join(parts, " ")
}

// UsageReport builds the usage report from this runtime's live state.
func (rt *Runtime) UsageReport(r *router.Router, events []ledger.Event, live *usage.Live) *usage.Report {
	keys := func(p string) int {
		if r == nil {
			return -1
		}
		return r.KeyCount(p)
	}
	rep := usage.Build(time.Now(), usage.ProvidersFromConfig(rt.cfg, keys), events, live)
	rep.Source = rt.Path
	return rep
}
