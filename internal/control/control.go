// Package control is the operator's control plane: how much the router is
// allowed to decide on its own.
//
// Two profiles sit at the ends of the dial:
//
//	auto     "infinite & full auto": fall back freely, wait out cooldowns and
//	         retry until something answers (bounded by the request context
//	         and max_wait_s), escalate to the "auto" route when the chain is
//	         exhausted, respect quota budgets.
//	control  "120% control": exactly the entry you asked for (no fallback),
//	         no silent retries, quota budgets enforced, pre/post hooks run on
//	         every call and may veto or rewrite it.
//
// The default ("" / "balanced") is the router's classic behaviour. Every
// knob can also be set on its own; explicit settings beat the profile
// preset. Settings live in the config (control:), can be changed live on a
// running server (PUT /v1/control, `freellm-router ctl`), and per request
// (X-Freellm-Profile / -Fallback / -Forever / -Max-Attempts headers).
package control

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Profile ids.
const (
	ProfileBalanced = "balanced"
	ProfileAuto     = "auto"
	ProfileControl  = "control"
)

// Profiles describes the profiles for help texts and UIs.
var Profiles = []struct{ ID, Summary string }{
	{ProfileAuto, "infinite & full auto: fallback, wait out cooldowns, retry until answered, escalate to the auto route"},
	{ProfileControl, "120% control: no fallback, no silent retries, budgets enforced, hooks may veto/rewrite"},
	{ProfileBalanced, "classic: walk the chain once with fallback, no waiting"},
}

// NormalizeProfile maps spellings to a profile id ("" → balanced).
func NormalizeProfile(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "balanced", "default", "classic":
		return ProfileBalanced, true
	case "auto", "infinite", "full-auto", "full_auto", "yolo":
		return ProfileAuto, true
	case "control", "manual", "strict", "120":
		return ProfileControl, true
	}
	return "", false
}

// Hooks are external commands run around every routed call.
type Hooks struct {
	// PreCall runs before routing with the call as JSON on stdin. Exit 0
	// allows; any other exit vetoes (stderr/stdout is the reason). Stdout
	// may be JSON {"allow": bool, "reason": "...", "model": "..."} to veto
	// or to rewrite the model/route.
	PreCall []string `yaml:"pre_call,omitempty" json:"pre_call,omitempty"`
	// PostCall runs after every call (fire-and-forget) with the outcome as
	// JSON on stdin.
	PostCall []string `yaml:"post_call,omitempty" json:"post_call,omitempty"`
	// TimeoutSec bounds each hook run (default 10). A pre-call hook that
	// fails to run or times out vetoes the call (fail closed).
	TimeoutSec int `yaml:"timeout_s,omitempty" json:"timeout_s,omitempty"`
}

// Settings is the stored control state (config `control:` / PUT /v1/control).
// Pointer fields distinguish "not set, use the profile" from false.
type Settings struct {
	Profile       string   `yaml:"profile,omitempty" json:"profile,omitempty"`
	Paused        bool     `yaml:"paused,omitempty" json:"paused,omitempty"`
	Disabled      []string `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Allow         []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	Fallback      *bool    `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	Forever       *bool    `yaml:"forever,omitempty" json:"forever,omitempty"`
	EnforceLimits *bool    `yaml:"enforce_limits,omitempty" json:"enforce_limits,omitempty"`
	MaxAttempts   int      `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	MaxWaitSec    int      `yaml:"max_wait_s,omitempty" json:"max_wait_s,omitempty"`
	Escalate      *string  `yaml:"escalate,omitempty" json:"escalate,omitempty"`
	Hooks         Hooks    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}

// Merge overlays o on s: set fields of o win, lists replace.
func (s Settings) Merge(o Settings) Settings {
	out := s
	if o.Profile != "" {
		out.Profile = o.Profile
	}
	if o.Paused {
		out.Paused = true
	}
	if o.Disabled != nil {
		out.Disabled = o.Disabled
	}
	if o.Allow != nil {
		out.Allow = o.Allow
	}
	if o.Fallback != nil {
		out.Fallback = o.Fallback
	}
	if o.Forever != nil {
		out.Forever = o.Forever
	}
	if o.EnforceLimits != nil {
		out.EnforceLimits = o.EnforceLimits
	}
	if o.MaxAttempts != 0 {
		out.MaxAttempts = o.MaxAttempts
	}
	if o.MaxWaitSec != 0 {
		out.MaxWaitSec = o.MaxWaitSec
	}
	if o.Escalate != nil {
		out.Escalate = o.Escalate
	}
	if o.Hooks.PreCall != nil {
		out.Hooks.PreCall = o.Hooks.PreCall
	}
	if o.Hooks.PostCall != nil {
		out.Hooks.PostCall = o.Hooks.PostCall
	}
	if o.Hooks.TimeoutSec != 0 {
		out.Hooks.TimeoutSec = o.Hooks.TimeoutSec
	}
	return out
}

// Override is a per-request adjustment (from headers / CLI flags).
type Override struct {
	Profile     string
	Fallback    *bool
	Forever     *bool
	MaxAttempts int
}

// IsZero reports whether o changes nothing.
func (o Override) IsZero() bool {
	return o.Profile == "" && o.Fallback == nil && o.Forever == nil && o.MaxAttempts == 0
}

// Resolved is the effective behaviour for one call.
type Resolved struct {
	Profile       string        `json:"profile"`
	Paused        bool          `json:"paused"`
	Disabled      []string      `json:"disabled,omitempty"`
	Allow         []string      `json:"allow,omitempty"`
	Fallback      bool          `json:"fallback"`
	Forever       bool          `json:"forever"`
	EnforceLimits bool          `json:"enforce_limits"`
	MaxAttempts   int           `json:"max_attempts,omitempty"`
	MaxWait       time.Duration `json:"-"`
	MaxWaitSec    int           `json:"max_wait_s,omitempty"`
	Escalate      string        `json:"escalate,omitempty"`
	Hooks         Hooks         `json:"hooks"`
}

// preset returns the profile's defaults.
func preset(profile string) Resolved {
	switch profile {
	case ProfileAuto:
		return Resolved{Profile: ProfileAuto, Fallback: true, Forever: true, EnforceLimits: true, Escalate: "auto"}
	case ProfileControl:
		return Resolved{Profile: ProfileControl, Fallback: false, Forever: false, EnforceLimits: true}
	}
	return Resolved{Profile: ProfileBalanced, Fallback: true}
}

// Resolve applies the profile preset, then the explicit settings, then the
// per-request override (whose profile replaces the stored one's preset but
// keeps operator-wide state: pause, disabled/allow lists, hooks, budgets).
func (s Settings) Resolve(o Override) Resolved {
	prof, ok := NormalizeProfile(s.Profile)
	if !ok {
		prof = ProfileBalanced
	}
	explicit := true
	if o.Profile != "" {
		if p, ok := NormalizeProfile(o.Profile); ok {
			prof, explicit = p, false
		}
	}
	r := preset(prof)
	if explicit {
		if s.Fallback != nil {
			r.Fallback = *s.Fallback
		}
		if s.Forever != nil {
			r.Forever = *s.Forever
		}
		if s.Escalate != nil {
			r.Escalate = *s.Escalate
		}
	}
	if s.EnforceLimits != nil {
		r.EnforceLimits = *s.EnforceLimits
	}
	r.Paused = s.Paused
	r.Disabled = s.Disabled
	r.Allow = s.Allow
	r.MaxAttempts = s.MaxAttempts
	r.MaxWaitSec = s.MaxWaitSec
	r.MaxWait = time.Duration(s.MaxWaitSec) * time.Second
	r.Hooks = s.Hooks
	if o.Fallback != nil {
		r.Fallback = *o.Fallback
	}
	if o.Forever != nil {
		r.Forever = *o.Forever
	}
	if o.MaxAttempts > 0 {
		r.MaxAttempts = o.MaxAttempts
	}
	return r
}

// IsDisabled reports whether provider is switched off.
func (r Resolved) IsDisabled(provider string) bool {
	return slices.ContainsFunc(r.Disabled, func(d string) bool { return strings.EqualFold(d, provider) })
}

// Allowed reports whether provider passes the allow-list (empty = all).
func (r Resolved) Allowed(provider string) bool {
	return len(r.Allow) == 0 || slices.ContainsFunc(r.Allow, func(a string) bool { return strings.EqualFold(a, provider) })
}

// ---------------------------------------------------------------------------
// Controller: the live, concurrency-safe holder
// ---------------------------------------------------------------------------

// Controller holds the live settings of one process.
type Controller struct {
	mu       sync.RWMutex
	s        Settings
	onChange []func(old, new Settings)
}

// New returns a controller starting from s.
func New(s Settings) *Controller { return &Controller{s: s} }

// Settings returns a copy of the current settings.
func (c *Controller) Settings() Settings {
	if c == nil {
		return Settings{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.s
}

// Resolve returns the effective behaviour for a call with override o.
func (c *Controller) Resolve(o Override) Resolved { return c.Settings().Resolve(o) }

// Replace swaps in s (after validation) and notifies listeners.
func (c *Controller) Replace(s Settings) error {
	if s.Profile != "" {
		p, ok := NormalizeProfile(s.Profile)
		if !ok {
			return fmt.Errorf("unknown profile %q (auto | control | balanced)", s.Profile)
		}
		s.Profile = p
	}
	if s.MaxAttempts < 0 || s.MaxWaitSec < 0 {
		return fmt.Errorf("max_attempts and max_wait_s must be >= 0")
	}
	c.mu.Lock()
	old := c.s
	c.s = s
	fns := append([]func(old, new Settings){}, c.onChange...)
	c.mu.Unlock()
	for _, fn := range fns {
		fn(old, s)
	}
	return nil
}

// Update applies fn to a copy of the settings and stores the result.
func (c *Controller) Update(fn func(*Settings)) (Settings, error) {
	s := c.Settings()
	fn(&s)
	if err := c.Replace(s); err != nil {
		return Settings{}, err
	}
	return c.Settings(), nil
}

// OnChange registers a listener called after every successful change.
func (c *Controller) OnChange(fn func(old, new Settings)) {
	c.mu.Lock()
	c.onChange = append(c.onChange, fn)
	c.mu.Unlock()
}

// Bool returns a pointer to b (for Settings literals).
func Bool(b bool) *bool { return &b }

// String returns a pointer to s.
func String(s string) *string { return &s }
