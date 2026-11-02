package core

import (
	"math/rand"
	"time"
)

// Backoff status classes. A failed upstream call is classified into exactly
// one of these; the policy maps each class to a (jittered) cooldown duration.
//
//	"conn"  – network layer / no HTTP status (status 0, timeouts, resets)
//	"429"   – upstream rate-limited
//	"408"   – upstream request timeout
//	"auth"  – 401/403 (key missing/invalid → long cooldown)
//	"4xx"   – other client errors (long cooldown)
//	"5xx"   – upstream server errors (short cooldown)
const (
	BackoffConn = "conn"
	Backoff429  = "429"
	Backoff408  = "408"
	Backoff4xx  = "4xx"
	BackoffAuth = "auth"
	Backoff5xx  = "5xx"
)

// BackoffSpec is one status class's tuning (config-driven, see AGENTS.md
// STEP 7 "Backoff policy object (per status class), jittered").
type BackoffSpec struct {
	// BaseSec is the base cooldown in seconds (0 = use the class default,
	// which derives from policies.cooldown_short_s / cooldown_long_s).
	BaseSec int `yaml:"base_s,omitempty"`
	// MaxSec caps the jittered result in seconds (0 = no cap beyond base).
	MaxSec int `yaml:"max_s,omitempty"`
	// Jitter is the maximum relative jitter (0..1): final duration is
	// base * (1 ± jitter) uniformly randomly. 0 = deterministic.
	Jitter float64 `yaml:"jitter,omitempty"`
}

// BackoffPolicy decides the cooldown duration for a failed upstream call,
// per status class, with optional jitter. Safe for concurrent use.
type BackoffPolicy struct {
	defaults map[string]time.Duration
	specs    map[string]BackoffSpec
}

// validBackoffClasses lists the accepted config keys for policies.backoff.
var validBackoffClasses = map[string]bool{
	BackoffConn: true, Backoff429: true, Backoff408: true,
	Backoff4xx: true, BackoffAuth: true, Backoff5xx: true,
}

// NewBackoffPolicy builds a policy. short/long are the class defaults for
// the "short" classes (conn, 429, 408, 5xx) and "long" classes (auth, 4xx).
// specs may be nil; unknown class keys in specs are ignored.
func NewBackoffPolicy(short, long time.Duration, specs map[string]BackoffSpec) *BackoffPolicy {
	if short <= 0 {
		short = 30 * time.Second
	}
	if long <= 0 {
		long = time.Hour
	}
	p := &BackoffPolicy{
		defaults: map[string]time.Duration{
			BackoffConn: short,
			Backoff429:  short,
			Backoff408:  short,
			Backoff5xx:  short,
			BackoffAuth: long,
			Backoff4xx:  long,
		},
		specs: make(map[string]BackoffSpec, len(specs)),
	}
	for k, v := range specs {
		if validBackoffClasses[k] {
			p.specs[k] = v
		}
	}
	return p
}

// ClassOf classifies an error into a backoff status class.
func (p *BackoffPolicy) ClassOf(err error) string {
	if ue, ok := IsUpstreamErr(err); ok {
		switch {
		case ue.Status == 429:
			return Backoff429
		case ue.Status == 408:
			return Backoff408
		case ue.Status == 401 || ue.Status == 403:
			return BackoffAuth
		case ue.Status >= 500:
			return Backoff5xx
		case ue.Status >= 400:
			return Backoff4xx
		}
	}
	return BackoffConn
}

// For returns the cooldown duration for a failed call.
// Deterministic when no jitter is configured (jitter = 0, the default).
func (p *BackoffPolicy) For(err error) time.Duration {
	class := p.ClassOf(err)
	base := p.defaults[class]

	spec, ok := p.specs[class]
	if ok && spec.BaseSec > 0 {
		base = time.Duration(spec.BaseSec) * time.Second
	}
	jitter := 0.0
	maxD := time.Duration(0)
	if ok {
		jitter = spec.Jitter
		if spec.MaxSec > 0 {
			maxD = time.Duration(spec.MaxSec) * time.Second
		}
	}

	d := base
	if jitter > 0 {
		// uniform in [1-jitter, 1+jitter]
		factor := 1 + (rand.Float64()*2-1)*jitter
		if factor < 1 {
			factor = 1 // never go below base*0 when jitter<=1
		}
		d = time.Duration(float64(base) * factor)
	}
	if d < 10*time.Millisecond {
		d = 10 * time.Millisecond
	}
	if maxD > 0 && d > maxD {
		d = maxD
	}
	return d
}

// DefaultBackoff returns the legacy (pre-STEP-7) policy: 30s short / 1h
// long, no jitter. Kept for tests and embeds that want the old behavior.
func DefaultBackoff() *BackoffPolicy {
	return NewBackoffPolicy(30*time.Second, time.Hour, nil)
}
