package agentic_test

import (
	"strings"
	"testing"

	"github.com/CYBERSZP0NT/szmelc-router/internal/agentic"
)

func TestLiveModeAliases(t *testing.T) {
	for _, s := range []string{"live", "Live", "po bogatości", "bogaty", "rich"} {
		if id, ok := agentic.NormalizeMode(s); !ok || id != agentic.ModeLive {
			t.Errorf("NormalizeMode(%q) = %q, %v", s, id, ok)
		}
	}
	if o := agentic.ParseOverride("po bogatości:code"); o.Mode != agentic.ModeLive || o.Role != "code" {
		t.Errorf("ParseOverride: %+v", o)
	}
}

func TestLiveModeRanksByRichness(t *testing.T) {
	s := agentic.Spec{Mode: agentic.ModeLive, Roles: map[string]int{"chat": 1}, Catalog: []agentic.Entry{
		{ID: "small", Model: "a/small", Tier: 2, Context: 1 << 20},
		{ID: "big-ctx", Model: "b/large-1", Tier: 4, Context: 200000},
		{ID: "large-rank2", Model: "c/large-2", Tier: 4, Context: 128000, Rank: 2},
		{ID: "large-rank1", Model: "c/large-3", Tier: 4, Context: 128000, Rank: 1},
		{ID: "large-quota", Model: "d/large-4", Tier: 4, Context: 128000, Quota: 900000},
		{ID: "frontier", Model: "e/frontier", Tier: 5, Context: 32000},
	}}
	d := s.Pick(agentic.Need{Role: "chat", Complexity: 1}, 0, nil, allAvail)
	got := strings.Join(d.Chain, " ")
	// Size class first (a small-context Frontier beats every Large), then
	// context, then free quota; c's own rank orders what is left. Tier caps
	// are ignored: the Small entry is still in.
	want := "e/frontier b/large-1 d/large-4 c/large-3 c/large-2 a/small"
	if got != want {
		t.Errorf("live chain\n got %s\nwant %s", got, want)
	}
	if d.Required != 5 || !strings.Contains(d.Reason, "richest first") {
		t.Errorf("live decision: required %d reason %q", d.Required, d.Reason)
	}
	for _, c := range d.Cands {
		if c.ID == "small" && !strings.Contains(c.Lost, "poorer size class") {
			t.Errorf("small lost: %q", c.Lost)
		}
	}
}

func TestRicherNeverComparesRanksAcrossProviders(t *testing.T) {
	a := agentic.Entry{Model: "x/a", Tier: 4, Rank: 900, Context: 200000}
	b := agentic.Entry{Model: "y/b", Tier: 4, Rank: 1, Context: 100000}
	if !agentic.Richer(a, b) {
		t.Error("rank 1 at another provider must not beat a bigger context")
	}
}
