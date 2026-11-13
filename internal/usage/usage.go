// Package usage turns the ledger into "how much have I used, and how much
// is left" — per provider and per model, across rolling windows — and
// enforces declared quotas as a client-side budget.
//
// Every number says where it comes from: observed usage is counted from the
// ledger's upstream attempts; limits are what the user declared in the
// config; the x-ratelimit-* snapshot is what the provider itself last
// reported, with the time it was seen; cooldowns come from a live server.
package usage

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
	"github.com/silverx/freellm-router/internal/ledger"
)

// Window is a request/token/error count over a time span.
type Window struct {
	Requests int `json:"requests"`
	Tokens   int `json:"tokens"`
	Errors   int `json:"errors"`
}

// Remaining is what is left of each declared limit (nil = no limit declared).
type Remaining struct {
	RPM *int `json:"rpm,omitempty"`
	RPD *int `json:"rpd,omitempty"`
	TPM *int `json:"tpm,omitempty"`
	TPD *int `json:"tpd,omitempty"`
}

// Cooldown is one provider/model entry cooling down on a live server.
type Cooldown struct {
	Via        string  `json:"via"`
	RemainingS float64 `json:"remaining_s"`
}

// ModelRow aggregates one provider/model.
type ModelRow struct {
	Via          string    `json:"via"`
	Day          Window    `json:"last_24h"`
	Total        Window    `json:"total"`
	AvgLatencyMS int64     `json:"avg_latency_ms"`
	LastStatus   int       `json:"last_status,omitempty"`
	LastUsed     time.Time `json:"last_used,omitempty"`
	CooldownS    float64   `json:"cooldown_s,omitempty"`
}

// ProviderRow is one provider's line in the report.
type ProviderRow struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Keys   int    `json:"keys"` // configured API keys (-1 = not applicable)
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Notes  string `json:"notes,omitempty"`

	Limits    config.Limits `json:"limits"`
	Remaining Remaining     `json:"remaining"`
	Minute    Window        `json:"last_1m"`
	Hour      Window        `json:"last_1h"`
	Day       Window        `json:"last_24h"`
	Today     Window        `json:"today"`
	Total     Window        `json:"total"`

	RateLimit    *core.RateLimit `json:"ratelimit,omitempty"`
	RateLimitAt  time.Time       `json:"ratelimit_seen_at,omitempty"`
	RateLimitVia string          `json:"ratelimit_via,omitempty"`

	AvgLatencyMS int64      `json:"avg_latency_ms"`
	LastUsed     time.Time  `json:"last_used,omitempty"`
	LastStatus   int        `json:"last_status,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Cooldowns    []Cooldown `json:"cooldowns,omitempty"`
	Models       []ModelRow `json:"models,omitempty"`
}

// Report is the full usage & limits picture.
type Report struct {
	Generated time.Time     `json:"generated_at"`
	Live      bool          `json:"live"`             // built by a running server
	Source    string        `json:"source,omitempty"` // ledger path
	Since     time.Time     `json:"since,omitempty"`  // oldest event considered
	Started   time.Time     `json:"server_started,omitempty"`
	InFlight  int64         `json:"in_flight"`
	Totals    ProviderRow   `json:"totals"`
	Providers []ProviderRow `json:"providers"`
	Requests  struct {
		Day   Window `json:"last_24h"`
		Today Window `json:"today"`
		Total Window `json:"total"`
	} `json:"requests"`
}

// ProviderInput describes one configured provider.
type ProviderInput struct {
	Name   string
	Kind   string
	Keys   int // -1 = keyless/local
	Notes  string
	Limits config.Limits
}

// Live carries state only a running server knows.
type Live struct {
	Started     time.Time
	InFlight    int64
	Cooldowns   map[string]time.Duration // via → remaining
	Unavailable map[string]string        // provider → reason (no key, daemon down, …)
	Disabled    map[string]string        // provider → reason (operator, allow-list)
	Blocked     map[string]string        // provider → reason (quota budget exhausted)
}

// ProvidersFromConfig builds the provider inputs from a config.
func ProvidersFromConfig(cfg *config.Config, keyCount func(provider string) int) []ProviderInput {
	out := make([]ProviderInput, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		keys := -1
		if p.KeyEnv != "" && keyCount != nil {
			keys = keyCount(p.Name)
		}
		out = append(out, ProviderInput{Name: p.Name, Kind: p.Kind, Keys: keys, Notes: p.Notes, Limits: cfg.LimitsFor(p.Name)})
	}
	return out
}

// consumes reports whether an attempt used provider quota: a 429 or a
// transport error (status 0) did not reach the model.
func consumes(e *ledger.Event) bool { return e.Status != 0 && e.Status != 429 }

// Build computes the report at now. events are ledger events (any order);
// live may be nil (offline: ledger only).
func Build(now time.Time, providers []ProviderInput, events []ledger.Event, live *Live) *Report {
	rep := &Report{Generated: now, Live: live != nil}
	if live != nil {
		rep.Started, rep.InFlight = live.Started, live.InFlight
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// Upstream calls: attempts; a ledger without any (written before
	// attempts were logged) falls back to its request lines.
	hasAttempts := false
	for i := range events {
		if events[i].Kind == ledger.KindAttempt {
			hasAttempts = true
			break
		}
	}

	rows := map[string]*ProviderRow{}
	order := []string{}
	row := func(name string) *ProviderRow {
		key := strings.ToLower(name)
		if r, ok := rows[key]; ok {
			return r
		}
		r := &ProviderRow{Name: name, Keys: -1, Status: "ok"}
		rows[key] = r
		order = append(order, key)
		return r
	}
	for _, p := range providers {
		r := row(p.Name)
		r.Kind, r.Keys, r.Notes, r.Limits = p.Kind, p.Keys, p.Notes, p.Limits
	}

	models := map[string]*ModelRow{}
	latSum := map[string]int64{}
	latN := map[string]int64{}
	mlatSum := map[string]int64{}
	mlatN := map[string]int64{}

	add := func(w *Window, e *ledger.Event, quota bool) {
		if quota {
			w.Requests++
		}
		w.Tokens += e.Tokens.Total()
		if e.Failed() {
			w.Errors++
		}
	}

	for i := range events {
		e := &events[i]
		if rep.Since.IsZero() || e.T.Before(rep.Since) {
			rep.Since = e.T
		}
		kind := e.EffectiveKind()
		if kind == ledger.KindRequest {
			add(&rep.Requests.Total, e, true)
			if now.Sub(e.T) <= 24*time.Hour {
				add(&rep.Requests.Day, e, true)
			}
			if !e.T.Before(midnight) {
				add(&rep.Requests.Today, e, true)
			}
		}
		upstream := (hasAttempts && kind == ledger.KindAttempt) || (!hasAttempts && kind == ledger.KindRequest)
		if !upstream || e.ProviderName() == "" {
			continue
		}
		r := row(e.ProviderName())
		q := consumes(e) || !hasAttempts
		age := now.Sub(e.T)
		add(&r.Total, e, q)
		if age <= time.Minute {
			add(&r.Minute, e, q)
		}
		if age <= time.Hour {
			add(&r.Hour, e, q)
		}
		if age <= 24*time.Hour {
			add(&r.Day, e, q)
		}
		if !e.T.Before(midnight) {
			add(&r.Today, e, q)
		}
		if e.T.After(r.LastUsed) {
			r.LastUsed, r.LastStatus = e.T, e.Status
		}
		if e.Failed() && e.Err != "" {
			r.LastError = ledger.Clip(e.Err, 160)
		}
		if !e.RateLimit.Empty() && !e.T.Before(r.RateLimitAt) {
			r.RateLimit, r.RateLimitAt, r.RateLimitVia = e.RateLimit, e.T, e.Via
		}
		if e.LatencyMS > 0 {
			latSum[r.Name] += e.LatencyMS
			latN[r.Name]++
		}
		if e.Via != "" {
			m := models[e.Via]
			if m == nil {
				m = &ModelRow{Via: e.Via}
				models[e.Via] = m
			}
			add(&m.Total, e, q)
			if age <= 24*time.Hour {
				add(&m.Day, e, q)
			}
			if e.T.After(m.LastUsed) {
				m.LastUsed, m.LastStatus = e.T, e.Status
			}
			if e.LatencyMS > 0 {
				mlatSum[e.Via] += e.LatencyMS
				mlatN[e.Via]++
			}
		}
	}

	for via, m := range models {
		if n := mlatN[via]; n > 0 {
			m.AvgLatencyMS = mlatSum[via] / n
		}
		if live != nil {
			if d, ok := live.Cooldowns[via]; ok && d > 0 {
				m.CooldownS = d.Seconds()
			}
		}
		r := row(strings.SplitN(via, "/", 2)[0])
		r.Models = append(r.Models, *m)
	}

	for _, key := range order {
		r := rows[key]
		if n := latN[r.Name]; n > 0 {
			r.AvgLatencyMS = latSum[r.Name] / n
		}
		sort.Slice(r.Models, func(i, j int) bool { return r.Models[i].Total.Requests > r.Models[j].Total.Requests })
		r.Remaining = remaining(r.Limits, r.Minute, r.Day)
		if live != nil {
			for via, d := range live.Cooldowns {
				if d > 0 && strings.EqualFold(strings.SplitN(via, "/", 2)[0], r.Name) {
					r.Cooldowns = append(r.Cooldowns, Cooldown{Via: via, RemainingS: d.Seconds()})
				}
			}
			sort.Slice(r.Cooldowns, func(i, j int) bool { return r.Cooldowns[i].Via < r.Cooldowns[j].Via })
		}
		r.Status, r.Reason = status(r, live, now)
		rep.Providers = append(rep.Providers, *r)
		rep.Totals.Minute = sum(rep.Totals.Minute, r.Minute)
		rep.Totals.Hour = sum(rep.Totals.Hour, r.Hour)
		rep.Totals.Day = sum(rep.Totals.Day, r.Day)
		rep.Totals.Today = sum(rep.Totals.Today, r.Today)
		rep.Totals.Total = sum(rep.Totals.Total, r.Total)
	}
	rep.Totals.Name = "total"
	return rep
}

func sum(a, b Window) Window {
	return Window{Requests: a.Requests + b.Requests, Tokens: a.Tokens + b.Tokens, Errors: a.Errors + b.Errors}
}

func remaining(l config.Limits, minute, day Window) Remaining {
	left := func(limit, used int) *int {
		if limit <= 0 {
			return nil
		}
		v := max(limit-used, 0)
		return &v
	}
	return Remaining{
		RPM: left(l.RPM, minute.Requests),
		RPD: left(l.RPD, day.Requests),
		TPM: left(l.TPM, minute.Tokens),
		TPD: left(l.TPD, day.Tokens),
	}
}

func status(r *ProviderRow, live *Live, now time.Time) (string, string) {
	if live != nil {
		if why, ok := live.Disabled[strings.ToLower(r.Name)]; ok {
			return "disabled", why
		}
		if why, ok := live.Blocked[strings.ToLower(r.Name)]; ok {
			return "limit", why
		}
	}
	if why := exhausted(r.Limits, r.Minute, r.Day); why != "" {
		return "limit", why
	}
	if rl := r.RateLimit; rl != nil && rl.RemainingRequests != nil && *rl.RemainingRequests <= 0 {
		if reset := ParseReset(rl.ResetRequests, r.RateLimitAt); reset.After(now) {
			return "limit", "provider reported 0 requests left until " + reset.Format("15:04:05")
		}
	}
	if live != nil {
		if why, ok := live.Unavailable[strings.ToLower(r.Name)]; ok {
			return "unavailable", why
		}
		if len(r.Cooldowns) > 0 && len(r.Cooldowns) >= max(len(r.Models), 1) {
			return "cooldown", "every used model is cooling down"
		}
	}
	if r.Keys == 0 {
		return "no key", "no API key in the environment"
	}
	return "ok", ""
}

// exhausted explains which declared limit is used up ("" = none).
func exhausted(l config.Limits, minute, day Window) string {
	switch {
	case l.RPM > 0 && minute.Requests >= l.RPM:
		return "rpm " + strconv.Itoa(minute.Requests) + "/" + strconv.Itoa(l.RPM)
	case l.RPD > 0 && day.Requests >= l.RPD:
		return "rpd " + strconv.Itoa(day.Requests) + "/" + strconv.Itoa(l.RPD)
	case l.TPM > 0 && minute.Tokens >= l.TPM:
		return "tpm " + strconv.Itoa(minute.Tokens) + "/" + strconv.Itoa(l.TPM)
	case l.TPD > 0 && day.Tokens >= l.TPD:
		return "tpd " + strconv.Itoa(day.Tokens) + "/" + strconv.Itoa(l.TPD)
	}
	return ""
}

// ParseReset interprets a reset header value relative to when it was seen:
// a Go-style or "1m30s"/"6s"/"0.5s" duration, plain seconds, or a Unix
// timestamp in seconds or milliseconds. Zero time = unknown.
func ParseReset(v string, seen time.Time) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if d, err := time.ParseDuration(v); err == nil {
		return seen.Add(d)
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		switch {
		case f > 1e12: // epoch ms
			return time.UnixMilli(int64(f))
		case f > 1e9: // epoch s
			return time.Unix(int64(f), 0)
		default: // seconds from now
			return seen.Add(time.Duration(f * float64(time.Second)))
		}
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		return t
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Tracker: live, in-process quota accounting (feeds the gate)
// ---------------------------------------------------------------------------

type hit struct {
	t      time.Time
	tokens int
}

// Tracker counts upstream calls per provider over the last 24 hours and
// remembers each provider's latest quota snapshot.
type Tracker struct {
	mu     sync.Mutex
	hits   map[string][]hit
	rl     map[string]*core.RateLimit
	rlAt   map[string]time.Time
	limits func(provider string) config.Limits
	now    func() time.Time
}

// NewTracker returns a tracker using limits to look up declared quotas.
func NewTracker(limits func(provider string) config.Limits) *Tracker {
	return &Tracker{hits: map[string][]hit{}, rl: map[string]*core.RateLimit{}, rlAt: map[string]time.Time{}, limits: limits, now: time.Now}
}

// Seed replays ledger attempts (e.g. from the file at startup).
func (t *Tracker) Seed(events []ledger.Event) {
	cut := t.now().Add(-24 * time.Hour)
	for i := range events {
		e := &events[i]
		if e.Kind != ledger.KindAttempt || e.T.Before(cut) {
			continue
		}
		t.add(e.ProviderName(), e.T, e.Status, e.Tokens.Total(), e.RateLimit)
	}
}

// Observe records one live attempt.
func (t *Tracker) Observe(a core.AttemptInfo) {
	tokens := 0
	if a.Done != nil {
		tokens = max(a.Done.PromptTokens, 0) + max(a.Done.CompletionTokens, 0)
	}
	t.add(a.Provider, t.now(), a.Status, tokens, a.RateLimit)
}

func (t *Tracker) add(provider string, at time.Time, status, tokens int, rl *core.RateLimit) {
	key := strings.ToLower(provider)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !rl.Empty() {
		t.rl[key], t.rlAt[key] = rl, at
	}
	if status == 0 || status == 429 {
		return
	}
	t.hits[key] = append(t.hits[key], hit{t: at, tokens: tokens})
}

// Allow reports whether provider is within its declared quota and the
// provider has not itself reported an exhausted request budget.
func (t *Tracker) Allow(provider string) (bool, string) {
	key := strings.ToLower(provider)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	hs := t.hits[key]
	cut := now.Add(-24 * time.Hour)
	i := 0
	for i < len(hs) && hs[i].t.Before(cut) {
		i++
	}
	if i > 0 {
		hs = append(hs[:0:0], hs[i:]...)
		t.hits[key] = hs
	}
	if rl := t.rl[key]; rl != nil && rl.RemainingRequests != nil && *rl.RemainingRequests <= 0 {
		if reset := ParseReset(rl.ResetRequests, t.rlAt[key]); reset.After(now) {
			return false, "quota: provider reported 0 requests left (reset in " + reset.Sub(now).Round(time.Second).String() + ")"
		}
	}
	if t.limits == nil {
		return true, ""
	}
	l := t.limits(provider)
	if l.IsZero() {
		return true, ""
	}
	var minute, day Window
	for _, h := range hs {
		day.Requests++
		day.Tokens += h.tokens
		if now.Sub(h.t) <= time.Minute {
			minute.Requests++
			minute.Tokens += h.tokens
		}
	}
	if why := exhausted(l, minute, day); why != "" {
		return false, "quota budget used up (" + why + ")"
	}
	return true, ""
}

// Blocked returns provider → reason for every tracked provider over budget.
func (t *Tracker) Blocked(providers []string) map[string]string {
	out := map[string]string{}
	for _, p := range providers {
		if ok, why := t.Allow(p); !ok {
			out[strings.ToLower(p)] = why
		}
	}
	return out
}
