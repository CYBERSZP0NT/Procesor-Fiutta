package usage

import (
	"testing"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
	"github.com/silverx/freellm-router/internal/ledger"
)

func i64(v int64) *int64 { return &v }

func TestBuildWindowsAndLimits(t *testing.T) {
	now := time.Now()
	evs := []ledger.Event{
		{T: now.Add(-30 * time.Second), Kind: ledger.KindAttempt, Via: "g/a", Status: 200, Tokens: &ledger.Tokens{Prompt: 10, Completion: 5}},
		{T: now.Add(-20 * time.Second), Kind: ledger.KindAttempt, Via: "g/a", Status: 200, Tokens: &ledger.Tokens{Prompt: 10, Completion: 5},
			RateLimit: &core.RateLimit{RemainingRequests: i64(7), LimitRequests: i64(10)}},
		{T: now.Add(-10 * time.Second), Kind: ledger.KindAttempt, Via: "g/b", Status: 429, Err: "rl"},
		{T: now.Add(-2 * time.Hour), Kind: ledger.KindAttempt, Via: "g/a", Status: 200},
		{T: now.Add(-48 * time.Hour), Kind: ledger.KindAttempt, Via: "g/a", Status: 200},
		{T: now.Add(-10 * time.Second), Kind: ledger.KindRequest, Via: "g/a", Status: 200},
	}
	rep := Build(now, []ProviderInput{{Name: "g", Keys: 1, Limits: config.Limits{RPM: 2, RPD: 100}}, {Name: "idle", Keys: 0}}, evs, nil)
	var g, idle ProviderRow
	for _, p := range rep.Providers {
		switch p.Name {
		case "g":
			g = p
		case "idle":
			idle = p
		}
	}
	if g.Minute.Requests != 2 || g.Hour.Requests != 2 || g.Day.Requests != 3 || g.Total.Requests != 4 {
		t.Fatalf("windows: %+v %+v %+v %+v", g.Minute, g.Hour, g.Day, g.Total)
	}
	if g.Minute.Errors != 1 || g.Minute.Tokens != 30 {
		t.Fatalf("minute = %+v", g.Minute)
	}
	if g.Status != "limit" || *g.Remaining.RPM != 0 || *g.Remaining.RPD != 97 {
		t.Fatalf("status %q remaining %+v", g.Status, g.Remaining)
	}
	if g.RateLimit == nil || *g.RateLimit.RemainingRequests != 7 {
		t.Fatalf("snapshot = %+v", g.RateLimit)
	}
	if len(g.Models) != 2 || idle.Status != "no key" {
		t.Fatalf("models=%d idle=%q", len(g.Models), idle.Status)
	}
	if rep.Requests.Day.Requests != 1 {
		t.Fatalf("requests = %+v", rep.Requests)
	}
}

func TestBuildLegacyJournal(t *testing.T) {
	// Old usage journals have no attempts: their request lines count.
	evs := []ledger.Event{{T: time.Now(), Path: "/v1/chat/completions", Via: "p/m", Status: 200}}
	rep := Build(time.Now(), nil, evs, nil)
	if len(rep.Providers) != 1 || rep.Providers[0].Day.Requests != 1 {
		t.Fatalf("legacy = %+v", rep.Providers)
	}
}

func TestTrackerBudget(t *testing.T) {
	tr := NewTracker(func(p string) config.Limits {
		if p == "g" {
			return config.Limits{RPM: 2}
		}
		return config.Limits{}
	})
	for i := 0; i < 2; i++ {
		tr.Observe(core.AttemptInfo{Provider: "g", Status: 200})
	}
	tr.Observe(core.AttemptInfo{Provider: "g", Status: 429}) // no quota used
	if ok, _ := tr.Allow("g"); ok {
		t.Fatal("rpm budget should be used up")
	}
	if ok, _ := tr.Allow("other"); !ok {
		t.Fatal("unlimited provider blocked")
	}
	tr.Observe(core.AttemptInfo{Provider: "other", Status: 429, RateLimit: &core.RateLimit{RemainingRequests: i64(0), ResetRequests: "1m"}})
	if ok, why := tr.Allow("other"); ok || why == "" {
		t.Fatal("provider-reported exhaustion should block until reset")
	}
}

func TestParseReset(t *testing.T) {
	seen := time.Unix(1_700_000_000, 0)
	cases := map[string]time.Time{
		"6s":            seen.Add(6 * time.Second),
		"1m30s":         seen.Add(90 * time.Second),
		"2.5":           seen.Add(2500 * time.Millisecond),
		"1700000100":    time.Unix(1_700_000_100, 0),
		"1700000100000": time.UnixMilli(1_700_000_100_000),
	}
	for in, want := range cases {
		if got := ParseReset(in, seen); !got.Equal(want) {
			t.Errorf("%s: %v, want %v", in, got, want)
		}
	}
	if !ParseReset("garbage", seen).IsZero() {
		t.Error("garbage should be zero")
	}
}
