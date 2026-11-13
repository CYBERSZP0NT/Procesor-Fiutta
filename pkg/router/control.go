package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/control"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
)

// Limiter is a client-side quota budget consulted before every upstream
// call when limits are enforced (usage.Tracker implements it).
type Limiter interface {
	Allow(provider string) (ok bool, why string)
}

// Override adjusts the control behaviour of one call (profile, fallback,
// forever, attempt budget) — e.g. from X-Freellm-* headers or CLI flags.
type Override = control.Override

// VetoError is returned when the router is paused or a pre-call hook
// refuses the call.
type VetoError = control.VetoError

type overrideKey struct{}

// WithOverride returns ctx carrying a per-call control override.
func WithOverride(ctx context.Context, o Override) context.Context {
	return context.WithValue(ctx, overrideKey{}, o)
}

// SetControl attaches the operator control plane (nil = classic behaviour).
func (r *Router) SetControl(c *control.Controller) { r.ctrl = c }

// Control returns the attached controller (may be nil).
func (r *Router) Control() *control.Controller { return r.ctrl }

// SetLimiter attaches a quota budget, enforced when the effective control
// settings say so.
func (r *Router) SetLimiter(l Limiter) { r.limiter = l }

// SetOnAttempt registers an observer for every upstream call.
func (r *Router) SetOnAttempt(fn func(ctx context.Context, a core.AttemptInfo)) {
	r.core.SetOnAttempt(fn)
}

// SetOnTask registers an observer for every task-router decision.
func (r *Router) SetOnTask(fn func(ctx context.Context, d *TaskDecision)) { r.onTask = fn }

// Resolve returns the effective control behaviour for ctx.
func (r *Router) Resolve(ctx context.Context) control.Resolved {
	ov, _ := ctx.Value(overrideKey{}).(Override)
	if r.ctrl == nil {
		return control.Settings{}.Resolve(ov)
	}
	return r.ctrl.Resolve(ov)
}

// gate builds the per-entry gate for res: operator switches first, then
// the quota budget.
func (r *Router) gate(res control.Resolved) core.GateFunc {
	return func(_ context.Context, provider, _ string) (bool, string) {
		if res.IsDisabled(provider) {
			return false, "disabled by operator"
		}
		if !res.Allowed(provider) {
			return false, "not in the allow-list"
		}
		if res.EnforceLimits && r.limiter != nil {
			if ok, why := r.limiter.Allow(provider); !ok {
				return false, why
			}
		}
		return true, ""
	}
}

// Forever-mode pacing: wait for the soonest cooldown, within these bounds.
var (
	foreverMinWait     = time.Second
	foreverMaxWait     = 60 * time.Second
	foreverDefaultWait = 5 * time.Second
)

// dispatch applies the control plane around dispatchOnce: pause, pre-call
// hook, fallback/attempt limits and gate, forever mode (wait out cooldowns
// and walk the chain again, escalating once), then the post-call hook.
func (r *Router) dispatch(ctx context.Context, selector string, req *core.ChatRequest, out *core.StreamOut, h *TaskHints, onDecision func(*TaskDecision)) (*core.RouteResult, error) {
	res := r.Resolve(ctx)
	call := ledger.CallFrom(ctx)
	ev := (*ledger.Event)(nil)
	if call != nil {
		call.Profile = res.Profile
		ev = call.Event
	}
	if ev != nil {
		ev.Profile = res.Profile
	}

	info := control.CallInfo{
		Model: selector, Profile: res.Profile, Messages: len(req.Messages),
		Tools: len(req.Tools) > 0, Stream: req.Stream,
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == core.RoleUser {
			info.Prompt = ledger.Clip(req.Messages[i].Content, 2000)
			break
		}
	}
	if call != nil {
		info.RequestID, info.Session, info.Source, info.Tags = call.ID, call.Session, call.Source, call.Tags
	}

	if res.Paused {
		return nil, &VetoError{Reason: "router paused by operator (szmelc-router ctl resume)"}
	}
	if v := res.Hooks.RunPre(ctx, info); !v.Allow {
		return nil, &VetoError{Reason: v.Reason}
	} else if v.Model != "" && v.Model != selector {
		req = retarget(req, selector, v.Model)
		selector = v.Model
		if ev != nil {
			ev.Route = selector
		}
	}

	opts := core.CallOptions{MaxAttempts: res.MaxAttempts, Gate: r.gate(res)}
	if !res.Fallback {
		opts.MaxEntries = 1
	}
	ctx = core.WithCallOptions(ctx, opts)

	started := false
	if out != nil {
		out = watchStarted(out, &started)
	}
	decide := func(d *TaskDecision) {
		if ev != nil {
			ev.Task = taskInfo(d)
		}
		if r.onTask != nil {
			r.onTask(ctx, d)
		}
		if onDecision != nil {
			onDecision(d)
		}
	}

	t0 := time.Now()
	var (
		result    *core.RouteResult
		err       error
		waited    time.Duration
		rounds    int
		attempts  int
		escalated bool
	)
	for {
		rounds++
		result, err = r.dispatchOnce(ctx, selector, req, out, h, decide)
		attempts += attemptsOf(result, err)
		if err == nil || !res.Forever || started || ctx.Err() != nil {
			break
		}
		if res.Escalate != "" && !escalated && !strings.EqualFold(res.Escalate, selector) {
			escalated = true
			result, err = r.dispatchOnce(ctx, res.Escalate, retarget(req, selector, res.Escalate), out, h, decide)
			attempts += attemptsOf(result, err)
			if err == nil || started || ctx.Err() != nil {
				break
			}
		}
		wait := foreverDefaultWait
		if ce, ok := core.AsChainError(err); ok && ce.Soonest > 0 {
			wait = ce.Soonest
		}
		wait = min(max(wait, foreverMinWait), foreverMaxWait)
		if res.MaxWait > 0 {
			left := res.MaxWait - waited
			if left <= 0 {
				break
			}
			wait = min(wait, left)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		waited += wait
		if ctx.Err() != nil {
			break
		}
	}
	if err != nil && rounds > 1 {
		err = fmt.Errorf("forever mode gave up after %d rounds (%s waiting): %w", rounds, waited.Round(time.Second), err)
	}

	if ev != nil {
		ev.Attempts, ev.Rounds, ev.Waited = attempts, rounds, waited.Seconds()
		if result != nil {
			ev.Trail = result.Trail
		} else if ce, ok := core.AsChainError(err); ok {
			ev.Trail = ce.Trail
		}
	}

	o := control.Outcome{CallInfo: info, OK: err == nil, Attempts: attempts, Rounds: rounds,
		WaitedSec: waited.Seconds(), LatencyMS: time.Since(t0).Milliseconds()}
	o.Model = selector
	if result != nil {
		o.Via, o.Trail = result.Via, result.Trail
		if result.Done != nil {
			o.PromptTokens, o.CompletionTokens = result.Done.PromptTokens, result.Done.CompletionTokens
		}
	}
	if err != nil {
		o.Error = err.Error()
	}
	res.Hooks.RunPost(o)
	return result, err
}

func attemptsOf(res *core.RouteResult, err error) int {
	if res != nil {
		return res.Attempts
	}
	if ce, ok := core.AsChainError(err); ok {
		return ce.Attempts
	}
	return 0
}

// retarget points req at a new selector when its model was the old one.
func retarget(req *core.ChatRequest, from, to string) *core.ChatRequest {
	if req.Model != "" && req.Model != from {
		return req
	}
	c := *req
	c.Model = to
	return &c
}

// watchStarted flips *started once any delta reaches the caller: a stream
// that has begun cannot be taken back, so it is never retried.
func watchStarted(o *core.StreamOut, started *bool) *core.StreamOut {
	wo := *o
	if o.TextSink != nil {
		orig := o.TextSink
		wo.TextSink = func(s string) { *started = true; orig(s) }
	}
	if o.ToolSink != nil {
		orig := o.ToolSink
		wo.ToolSink = func(tc core.ToolCall) { *started = true; orig(tc) }
	}
	return &wo
}

func taskInfo(d *TaskDecision) *ledger.TaskInfo {
	if d == nil {
		return nil
	}
	return &ledger.TaskInfo{
		Mode: d.Mode, Role: d.Need.Role, Tier: d.Required, Escalate: d.Attempt,
		Chain: d.Chain, Reason: d.Reason,
	}
}

// KeyCount returns how many API keys provider has configured.
func (r *Router) KeyCount(provider string) int {
	if r.keys == nil {
		return 0
	}
	return len(r.keys.Keys(context.Background(), strings.ToLower(provider)))
}

// Cooldowns returns every provider/model currently cooling down.
func (r *Router) Cooldowns() map[string]time.Duration {
	ctx := context.Background()
	cd := r.core.Cooldown()
	out := map[string]time.Duration{}
	for _, l := range cd.Labels() {
		if d := cd.Remaining(ctx, l); d > 0 {
			out[l] = d
		}
	}
	return out
}

// ClearCooldowns forgets the cooldown of via ("" = all); it returns how
// many entries were cooling down.
func (r *Router) ClearCooldowns(via string) int {
	ctx := context.Background()
	cd := r.core.Cooldown()
	n := 0
	for _, l := range cd.Labels() {
		if via == "" || strings.EqualFold(l, via) || strings.HasPrefix(strings.ToLower(l), strings.ToLower(via)+"/") {
			if cd.Has(ctx, l) {
				n++
			}
			cd.Forget(ctx, l)
		}
	}
	return n
}

// Unavailable asks every provider whether it can be called right now and
// returns provider → reason for those that cannot (no key, daemon down).
func (r *Router) Unavailable(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, name := range r.Providers() {
		p, ok := r.core.Provider(name)
		if !ok {
			continue
		}
		if a := p.Available(ctx); a != nil && !a.OK {
			why := a.Reason
			if why == "" {
				why = "not available"
			}
			out[strings.ToLower(name)] = why
		}
	}
	return out
}
