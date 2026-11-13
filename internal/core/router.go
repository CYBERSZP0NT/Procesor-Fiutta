package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Router orchestrates the fallback chain: resolve → filter → stream → retry.
// It is the only component that touches Provider.Stream.
type Router struct {
	providers map[string]Provider // provider name → instance
	policy    RoutePolicy
	cooldown  CooldownStore
	backoff   *BackoffPolicy // per status class, jittered (STEP 7)
	retrySame bool           // retry same provider once before moving on
	maxAtt    int            // max total stream attempts
	onErr     func(via string, err error)
	onAttempt func(ctx context.Context, a AttemptInfo)
}

// RouterOpts configures a Router.
type RouterOpts struct {
	Providers   map[string]Provider
	RoutePolicy RoutePolicy
	Cooldown    CooldownStore
	Short       time.Duration
	Long        time.Duration
	RetrySame   bool
	MaxAtt      int
	// Backoff optionally overrides the derived policy (per status class,
	// jittered). When nil it is derived from Short/Long with no jitter.
	Backoff *BackoffPolicy
}

// NewRouter creates a Router with sensible defaults where opts are zero.
func NewRouter(opts RouterOpts) *Router {
	if opts.Cooldown == nil {
		opts.Cooldown = NewInMemCooldown()
	}
	if opts.Short <= 0 {
		opts.Short = 30 * time.Second
	}
	if opts.Long <= 0 {
		opts.Long = time.Hour
	}
	if opts.MaxAtt <= 0 {
		opts.MaxAtt = 8
	}
	if opts.Backoff == nil {
		opts.Backoff = NewBackoffPolicy(opts.Short, opts.Long, nil)
	}
	return &Router{
		providers: opts.Providers,
		policy:    opts.RoutePolicy,
		cooldown:  opts.Cooldown,
		backoff:   opts.Backoff,
		retrySame: opts.RetrySame,
		maxAtt:    opts.MaxAtt,
	}
}

// RouteResult is the outcome of a successful Route call.
type RouteResult struct {
	Via      string      // "provider/model" label of the provider that served the request
	Done     *StreamDone // finish reason + token counts
	FullText string      // full text (accumulated from TextSink)
	Attempts int         // upstream calls made (including failed ones)
	Trail    []string    // entries skipped or failed before Via served
}

// Route resolves the chain and walks it in order, returning the first
// successful response. On all-fail it returns the last *UpstreamErr wrapped
// in a descriptive error.
func (r *Router) Route(ctx context.Context, sel ModelSelector, req *ChatRequest, out *StreamOut) (*RouteResult, error) {
	// 1. Resolve the chain: a pre-resolved chain (agentic task router) wins;
	// otherwise ask the policy. The live model listing is only gathered for
	// policies that declare they read it — it costs one /models request per
	// provider, so doing it on every request was pure latency.
	var chain []Model
	if len(sel.Chain) > 0 {
		chain = sel.Chain
	} else {
		var known map[string]Model
		if kp, ok := r.policy.(KnownModelsPolicy); ok && kp.UsesKnownModels() {
			known = r.knownModels(ctx)
		}
		chain = r.policy.Resolve(ctx, sel, known)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("router: no chain resolved for selector %q", sel.Name)
	}

	// Deduplicate (same provider+model more than once).
	seen := make(map[string]bool, len(chain))
	deduped := make([]Model, 0, len(chain))
	for _, m := range chain {
		key := m.Provider + "/" + m.ID
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, m)
		}
	}
	chain = deduped

	// 2. Walk through providers with retry/cooldown logic.
	opts := CallOptionsFrom(ctx)
	if opts.MaxEntries > 0 && len(chain) > opts.MaxEntries {
		chain = chain[:opts.MaxEntries]
	}
	maxAtt := r.maxAtt
	if opts.MaxAttempts > 0 {
		maxAtt = opts.MaxAttempts
	}
	attempts := 0
	var lastErr error
	var lastErrs []string
	var soonest time.Duration
	noteCooldown := func(d time.Duration) {
		if d > 0 && (soonest == 0 || d < soonest) {
			soonest = d
		}
	}

	for i := 0; i < len(chain); i++ {
		entry := chain[i]
		provName := entry.Provider
		modelID := entry.ID
		via := provName + "/" + modelID

		// Look up provider instance.
		prov, exists := r.providers[provName]
		if !exists {
			lastErr = NewUpstreamErr(0, provName, modelID, "provider not registered", nil)
			lastErrs = append(lastErrs, via+": not registered")
			continue
		}

		// Operator gate (disabled providers, allow-lists, quota budgets).
		if opts.Gate != nil {
			if ok, why := opts.Gate(ctx, provName, modelID); !ok {
				if why == "" {
					why = "blocked by gate"
				}
				lastErr = NewUpstreamErr(0, provName, modelID, why, nil)
				lastErrs = append(lastErrs, via+": "+why)
				continue
			}
		}

		// Availability check.
		avail := prov.Available(ctx)
		if avail != nil && !avail.OK {
			availReason := avail.Reason
			if availReason == "" {
				availReason = "not available"
			}
			lastErr = NewUpstreamErr(0, provName, modelID, availReason, nil)
			lastErrs = append(lastErrs, via+": "+availReason)
			continue
		}

		// Cooldown check.
		if r.cooldown.Has(ctx, via) {
			remaining := r.cooldown.Remaining(ctx, via)
			noteCooldown(remaining)
			lastErr = NewUpstreamErr(429, provName, modelID, "in cooldown ("+remaining.String()+")", nil)
			lastErrs = append(lastErrs, via+": cooldown ("+remaining.String()+")")
			continue
		}

		// Try stream (potentially retry-once on same provider).
		maxTry := 1
		if r.retrySame {
			maxTry = 2
		}

		for attempt := 1; attempt <= maxTry; attempt++ {
			if attempts >= maxAtt {
				lastErrs = append(lastErrs, "max attempts reached")
				break
			}
			attempts++

			// Build per-call StreamOut.
			var callOut *StreamOut
			var textBuf strings.Builder
			var hasOut bool

			if out != nil {
				callOut = r.wrapOutHooks(out, via)
			} else {
				hasOut = true
				callOut = &StreamOut{
					TextSink: func(s string) { textBuf.WriteString(s) },
				}
			}
			callOut, seen := observeOut(callOut)

			// Per-entry concrete model (contract: chain entries are
			// provider/model and bind the model the provider actually offers).
			// If the caller selected a route alias / provider name / empty
			// instead of a concrete model id, use the entry's model for THIS
			// provider. A concrete model id given by the caller is honored as-is
			// (rule 4: bare model routed to every provider that lists it).
			callReq := req
			if req.Model == "" || req.Model == sel.Name ||
				strings.EqualFold(strings.TrimSpace(req.Model), sel.Name) {
				c := *req
				c.Model = modelID
				callReq = &c
			}
			t0 := time.Now()
			done, err := prov.Stream(ctx, callReq, callOut)
			info := AttemptInfo{
				Via: via, Provider: provName, Model: modelID, N: attempts,
				Latency: time.Since(t0), RateLimit: seen.rl, StartedOut: seen.started,
			}

			if err == nil {
				// Success!
				r.cooldown.Forget(ctx, via)
				fullText := ""
				if hasOut {
					fullText = textBuf.String()
				}
				if done == nil {
					done = &StreamDone{FinishReason: "stop"}
				}
				info.Status, info.Done = 200, done
				r.observe(ctx, info)
				return &RouteResult{Via: via, Done: done, FullText: fullText, Attempts: attempts, Trail: lastErrs}, nil
			}

			// Classify the error.
			ue, isUp := IsUpstreamErr(err)
			if isUp {
				lastErr = ue
				info.Status = ue.Status
				lastErrs = append(lastErrs, fmt.Sprintf("%s: HTTP %d %s", via, ue.Status, ue.Msg))
			} else {
				lastErr = err
				info.Status = seen.status
				lastErrs = append(lastErrs, via+": "+err.Error())
			}

			if r.onErr != nil {
				r.onErr(via, err)
			}

			// Apply cooldown.
			cd := r.cooldownFor(err)
			r.cooldown.Set(ctx, via, cd)
			noteCooldown(cd)
			info.Err, info.Cooldown = err, cd
			r.observe(ctx, info)

			// 4xx (non-429/408): don't retry same provider.
			if isUp && ue.Status >= 400 && ue.Status < 500 && ue.Status != 429 && ue.Status != 408 {
				break
			}
		}
	}

	return nil, &ChainError{Entries: len(chain), Attempts: attempts, Trail: lastErrs, Last: lastErr, Soonest: soonest}
}

func (r *Router) observe(ctx context.Context, a AttemptInfo) {
	if r.onAttempt != nil {
		r.onAttempt(ctx, a)
	}
}

// outSeen collects what one attempt's StreamOut observed.
type outSeen struct {
	status  int
	rl      *RateLimit
	started bool
}

// observeOut wraps o so the router learns whether the attempt produced
// output and which quota headers the provider reported, without changing
// what the caller's sinks receive.
func observeOut(o *StreamOut) (*StreamOut, *outSeen) {
	seen := &outSeen{}
	wo := *o
	origMeta := o.Meta
	wo.Meta = func(status int, rl *RateLimit) {
		seen.status = status
		if rl != nil {
			seen.rl = rl
		}
		if origMeta != nil {
			origMeta(status, rl)
		}
	}
	if o.TextSink != nil {
		orig := o.TextSink
		wo.TextSink = func(t string) { seen.started = true; orig(t) }
	}
	if o.ToolSink != nil {
		orig := o.ToolSink
		wo.ToolSink = func(tc ToolCall) { seen.started = true; orig(tc) }
	}
	return &wo, seen
}

// cooldownFor decides the cooldown duration based on the error, via the
// status-class backoff policy (jitter applied only when configured).
func (r *Router) cooldownFor(err error) time.Duration {
	return r.backoff.For(err)
}

// Backoff returns the router's backoff policy (tests / embeds).
func (r *Router) Backoff() *BackoffPolicy { return r.backoff }

// Provider returns the registered Provider instance for name.
func (r *Router) Provider(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Cooldown returns the router's cooldown store.
func (r *Router) Cooldown() CooldownStore { return r.cooldown }

// SetOnUpstreamError sets an optional observer invoked for every failed
// provider attempt (before cooldown bookkeeping). Used by the server for
// metrics and last-error tracking. The hook must be safe for concurrent use.
func (r *Router) SetOnUpstreamError(fn func(via string, err error)) { r.onErr = fn }

// wrapOutHooks wraps the caller's StreamOut so that the first delta
// delivered by THIS attempt triggers out.Serving(via) exactly once.
func (r *Router) wrapOutHooks(o *StreamOut, via string) *StreamOut {
	if o == nil || (o.TextSink == nil && o.ToolSink == nil) || o.Serving == nil {
		return o
	}
	wo := *o
	fired := false
	fire := func() {
		if !fired {
			fired = true
			o.Serving(via)
		}
	}
	if wo.TextSink != nil {
		orig := wo.TextSink
		wo.TextSink = func(t string) { fire(); orig(t) }
	}
	if wo.ToolSink != nil {
		orig := wo.ToolSink
		wo.ToolSink = func(tc ToolCall) { fire(); orig(tc) }
	}
	return &wo
}

// knownModels builds a lookup map "provider/model" → Model for the RoutePolicy.
func (r *Router) knownModels(ctx context.Context) map[string]Model {
	m := make(map[string]Model)
	for name, prov := range r.providers {
		models, _ := prov.Models(ctx)
		for _, mol := range models {
			if mol.Provider == "" {
				mol.Provider = name
			}
			m[name+"/"+mol.ID] = mol
		}
	}
	return m
}

// ChainNames returns the sorted provider names (for debugging/logging).
func (r *Router) ChainNames() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
