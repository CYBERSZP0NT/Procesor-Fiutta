package core

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Rate-limit snapshots (what the upstream told us about its quota)
// ---------------------------------------------------------------------------

// RateLimit is a snapshot of the quota headers one upstream response carried
// (x-ratelimit-*, retry-after). Nil pointer fields mean "not reported" — a
// reported 0 remaining is the most important value, so it must survive JSON.
type RateLimit struct {
	LimitRequests     *int64 `json:"limit_requests,omitempty"`
	RemainingRequests *int64 `json:"remaining_requests,omitempty"`
	ResetRequests     string `json:"reset_requests,omitempty"`
	LimitTokens       *int64 `json:"limit_tokens,omitempty"`
	RemainingTokens   *int64 `json:"remaining_tokens,omitempty"`
	ResetTokens       string `json:"reset_tokens,omitempty"`
	RetryAfter        string `json:"retry_after,omitempty"`
}

// Empty reports whether no quota header was present.
func (r *RateLimit) Empty() bool {
	return r == nil || (r.LimitRequests == nil && r.RemainingRequests == nil &&
		r.LimitTokens == nil && r.RemainingTokens == nil &&
		r.ResetRequests == "" && r.ResetTokens == "" && r.RetryAfter == "")
}

// ParseRateLimit extracts the common quota header families from h:
// OpenAI/Groq style (x-ratelimit-{limit,remaining,reset}-{requests,tokens}),
// the unsuffixed form (x-ratelimit-limit / -remaining / -reset, e.g.
// OpenRouter) and Retry-After. Returns nil when none is present.
func ParseRateLimit(h http.Header) *RateLimit {
	if h == nil {
		return nil
	}
	num := func(names ...string) *int64 {
		for _, n := range names {
			if v := strings.TrimSpace(h.Get(n)); v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					i := int64(f)
					return &i
				}
			}
		}
		return nil
	}
	str := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(h.Get(n)); v != "" {
				return v
			}
		}
		return ""
	}
	rl := &RateLimit{
		LimitRequests:     num("x-ratelimit-limit-requests", "x-ratelimit-limit", "ratelimit-limit"),
		RemainingRequests: num("x-ratelimit-remaining-requests", "x-ratelimit-remaining", "ratelimit-remaining"),
		ResetRequests:     str("x-ratelimit-reset-requests", "x-ratelimit-reset", "ratelimit-reset"),
		LimitTokens:       num("x-ratelimit-limit-tokens"),
		RemainingTokens:   num("x-ratelimit-remaining-tokens"),
		ResetTokens:       str("x-ratelimit-reset-tokens"),
		RetryAfter:        str("retry-after"),
	}
	if rl.Empty() {
		return nil
	}
	return rl
}

// ---------------------------------------------------------------------------
// Per-attempt observation
// ---------------------------------------------------------------------------

// AttemptInfo describes one upstream call the router made (success or not).
type AttemptInfo struct {
	Via        string        // provider/model
	Provider   string        // provider part of Via
	Model      string        // model part of Via
	N          int           // 1-based attempt number within the request
	Status     int           // HTTP status (200 on success, 0 = transport error)
	Latency    time.Duration // time spent in Provider.Stream
	Err        error         // nil on success
	Done       *StreamDone   // set on success
	RateLimit  *RateLimit    // last quota snapshot the provider reported, if any
	Cooldown   time.Duration // cooldown applied after a failure
	StartedOut bool          // the attempt delivered at least one delta
}

// SetOnAttempt registers an observer called after every upstream call. It
// runs on the request goroutine; keep it cheap (append to a log, bump a
// counter). ctx is the request context, so observers can read request
// metadata (request id, session) their own middleware stored there.
func (r *Router) SetOnAttempt(fn func(ctx context.Context, a AttemptInfo)) { r.onAttempt = fn }

// ---------------------------------------------------------------------------
// Per-call options (carried in the context so every layer above core —
// route policy, task router, HTTP server — can set them without new params)
// ---------------------------------------------------------------------------

// GateFunc decides whether a chain entry may be tried right now. It is the
// hook for operator control (disabled providers, allow-lists, client-side
// quota budgets). why explains a false and lands in the error trail.
type GateFunc func(ctx context.Context, provider, model string) (ok bool, why string)

// CallOptions tune a single Route call.
type CallOptions struct {
	// MaxEntries caps how many chain entries are tried (1 = no fallback).
	// 0 = the whole chain.
	MaxEntries int
	// MaxAttempts overrides the router's total attempt budget when > 0.
	MaxAttempts int
	// Gate, when set, is consulted for every entry before it is tried.
	Gate GateFunc
}

type callOptsKey struct{}

// WithCallOptions returns ctx carrying o (replacing any previous options).
func WithCallOptions(ctx context.Context, o CallOptions) context.Context {
	return context.WithValue(ctx, callOptsKey{}, o)
}

// CallOptionsFrom returns the options stored in ctx (zero value if none).
func CallOptionsFrom(ctx context.Context) CallOptions {
	if ctx == nil {
		return CallOptions{}
	}
	o, _ := ctx.Value(callOptsKey{}).(CallOptions)
	return o
}

// ---------------------------------------------------------------------------
// Chain failure
// ---------------------------------------------------------------------------

// ChainError is returned by Router.Route when every chain entry failed. Its
// text is the human-readable trail; Last is the final underlying error.
// (It deliberately does not Unwrap to Last: callers map "everything
// failed" to one status, not to whichever provider happened to be last.)
type ChainError struct {
	Entries  int
	Attempts int // upstream calls made
	Trail    []string
	Last     error
	// Soonest is the shortest remaining cooldown among the entries that were
	// skipped or failed (0 = unknown) — when to try again.
	Soonest time.Duration
}

func (e *ChainError) Error() string {
	return fmt.Sprintf("router: all %d chain entries failed:\n  %s\n%v",
		e.Entries, strings.Join(e.Trail, "\n  "), e.Last)
}

// AsChainError reports whether err (or an error it wraps) is a *ChainError.
func AsChainError(err error) (*ChainError, bool) {
	for err != nil {
		if ce, ok := err.(*ChainError); ok {
			return ce, true
		}
		uu, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		err = uu.Unwrap()
	}
	return nil, false
}
