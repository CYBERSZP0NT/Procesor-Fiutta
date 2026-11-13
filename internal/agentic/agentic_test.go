package agentic_test

import (
	"strings"
	"testing"

	"github.com/silverx/szmelc-router/internal/agentic"
	"github.com/silverx/szmelc-router/internal/config"
)

// defaultSpec is the embedded spec pinned to Jako Tako ("just enough"):
// most tests below are about tiers. builtinSpec keeps the default mode.
func defaultSpec(t *testing.T) agentic.Spec {
	t.Helper()
	s := builtinSpec(t)
	s.Mode = agentic.ModeJakoTako
	return s
}

func builtinSpec(t *testing.T) agentic.Spec {
	t.Helper()
	s, err := agentic.Default()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, p := range cfg.Providers {
		kinds[p.Name] = p.Kind
	}
	for i := range s.Catalog {
		s.Catalog[i].Where = kinds[s.Catalog[i].Provider()]
	}
	return *s
}

// availWhere makes only the given provider kinds available.
func availWhere(kinds ...string) agentic.AvailFunc {
	return func(e agentic.Entry) (bool, string) {
		for _, k := range kinds {
			if e.Where == k {
				return true, ""
			}
		}
		return false, "no key"
	}
}

func allAvail(agentic.Entry) (bool, string) { return true, "" }

func TestDefaultSpecIsValidAgainstEmbeddedProviders(t *testing.T) {
	s := defaultSpec(t)
	cfg, _ := config.Defaults()
	known := map[string]bool{}
	for _, p := range cfg.Providers {
		known[strings.ToLower(p.Name)] = true
	}
	if probs := s.Validate(known); len(probs) > 0 {
		t.Fatalf("default agentic spec has problems:\n%s", strings.Join(probs, "\n"))
	}
	for _, e := range s.Catalog {
		if e.Where == "" {
			t.Errorf("%s: provider %s has no kind", e.ID, e.Provider())
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		role     string
		minC     int
		maxC     int
		wantTier int // with all providers available, 0 = skip
	}{
		{"greeting", "hi", "chat", 1, 1, 1},
		{"translate pl", "Przetłumacz to zdanie na angielski: dzień dobry", "translate", 1, 3, 0},
		{"summarize", "Podsumuj ten tekst w trzech punktach: lorem ipsum dolor sit amet, consectetur adipiscing elit.", "summarize", 1, 3, 0},
		{"code fence", "Why does this panic?\n```go\nvar m map[string]int\nm[\"a\"] = 1\n```", "code", 3, 5, 4},
		{"hard plan", "Zaprojektuj architekturę rozproszonego systemu kolejek z naciskiem na bezpieczeństwo i skalowalność.", "plan", 4, 5, 5},
		{"classify", "Classify the sentiment of this review as positive or negative: great product", "classify", 1, 3, 1},
	}
	s := defaultSpec(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := agentic.Classify([]agentic.Msg{{Role: "user", Content: c.msg}}, false)
			if n.Role != c.role {
				t.Errorf("role = %s, want %s (signals %v)", n.Role, c.role, n.Signals)
			}
			if n.Complexity < c.minC || n.Complexity > c.maxC {
				t.Errorf("complexity = %d, want %d..%d (signals %v)", n.Complexity, c.minC, c.maxC, n.Signals)
			}
			if c.wantTier > 0 {
				if d := s.Pick(n, 0, nil, allAvail); d.Required != c.wantTier {
					t.Errorf("required tier = %d, want %d (%s)", d.Required, c.wantTier, d.Reason)
				}
			}
		})
	}
}

func TestHugeSystemPromptDoesNotInflateTier(t *testing.T) {
	sys := strings.Repeat("You are a coding agent. Use the tools. Refactor, debug, implement, review code. ", 400)
	n := agentic.Classify([]agentic.Msg{{Role: "system", Content: sys}, {Role: "user", Content: "hi"}}, false)
	if n.Role != "chat" || n.Complexity != 1 {
		t.Fatalf("got %s c%d, want chat c1 (signals %v)", n.Role, n.Complexity, n.Signals)
	}
	if n.Tokens < 5000 {
		t.Fatalf("tokens = %d, the whole prompt must still be measured", n.Tokens)
	}
	d := defaultSpec(t).Pick(n, 0, nil, allAvail)
	if d.Required != 1 {
		t.Fatalf("required = %d, want 1", d.Required)
	}
	// ~8k-token prompt: the 4k-context local models must be filtered out.
	for _, c := range d.Cands {
		if c.ID == "local-llama-1b" && !strings.Contains(c.Lost, "context") {
			t.Fatalf("local-llama-1b should lose on context, got %q", c.Lost)
		}
	}
}

func TestPickCheapestSufficientAndExplains(t *testing.T) {
	s := defaultSpec(t)
	d := s.Pick(agentic.Need{Role: "code", Complexity: 3, Source: "test"}, 0, nil, allAvail)
	if d.Required != 4 || d.Winner == nil || d.Winner.ID != "deepseek-chat" {
		t.Fatalf("want deepseek-chat at tier 4, got %+v (%s)", d.Winner, d.Reason)
	}
	if !strings.Contains(d.Reason, "code-prefers-coders") || !strings.Contains(d.Reason, "picked deepseek-chat") {
		t.Fatalf("reason does not explain the pick: %s", d.Reason)
	}
	// Escalation (stronger) comes before best-effort (weaker) in the chain.
	pos := map[string]int{}
	for i, m := range d.Chain {
		pos[m] = i
	}
	if pos["deepseek/deepseek-reasoner"] > pos["groq/llama-3.3-70b-versatile"] {
		t.Fatalf("tier-5 entry must precede tier-3 fallback: %v", d.Chain)
	}
	for _, c := range d.Cands {
		if c.ID != d.Winner.ID && c.Rank > 0 && c.Lost == "" {
			t.Errorf("%s is in the chain but has no 'lost' reason", c.ID)
		}
	}
}

func TestPickZeroKeyUsesKeylessAndScrapers(t *testing.T) {
	s := defaultSpec(t)
	d := s.Pick(agentic.Need{Role: "code", Complexity: 3}, 0, nil, availWhere("keyless", "scraper"))
	if d.Winner == nil || d.Winner.ID != "g4f-o3-mini" {
		t.Fatalf("want g4f-o3-mini, got %+v (%s)", d.Winner, d.Reason)
	}
	for _, m := range d.Chain {
		if strings.HasPrefix(m, "google/") || strings.HasPrefix(m, "deepseek/") {
			t.Fatalf("unavailable provider in chain: %v", d.Chain)
		}
	}
}

func TestToolsAreAHardFilter(t *testing.T) {
	s := defaultSpec(t)
	d := s.Pick(agentic.Need{Role: "chat", Complexity: 2, Tools: true}, 0, nil, allAvail)
	byModel := map[string]agentic.Entry{}
	for _, e := range s.Catalog {
		byModel[e.Model] = e
	}
	for _, m := range d.Chain {
		if !byModel[m].Tools {
			t.Fatalf("%s has no tool calling but is in the chain", m)
		}
	}
	// Tools needed, only keyless/scrapers (no tool calling) available: empty.
	d = s.Pick(agentic.Need{Role: "chat", Complexity: 2, Tools: true}, 0, nil, availWhere("keyless", "scraper"))
	if d.Winner != nil || len(d.Chain) != 0 || !strings.Contains(d.Reason, "nothing") {
		t.Fatalf("want an empty, explained decision, got %v (%s)", d.Chain, d.Reason)
	}
}

func TestEscalationRaisesTierAndBreaksCap(t *testing.T) {
	s := defaultSpec(t)
	n := agentic.Need{Role: "classify", Complexity: 2}
	d0 := s.Pick(n, 0, nil, allAvail)
	d1 := s.Pick(n, 1, map[string]bool{d0.Winner.Model: true}, allAvail)
	if d0.Required != 1 || d1.Required != 2 {
		t.Fatalf("required %d -> %d, want 1 -> 2", d0.Required, d1.Required)
	}
	for _, m := range d1.Chain {
		if m == d0.Winner.Model {
			t.Fatalf("rejected %s is still in the escalated chain", m)
		}
	}
	// max_tier 2 from sorting-stays-cheap is lifted by escalation.
	d3 := s.Pick(agentic.Need{Role: "classify", Complexity: 4}, 2, nil, allAvail)
	if d3.Required < 3 {
		t.Fatalf("escalation must break max_tier, got %d", d3.Required)
	}
}

func TestLocalOnly(t *testing.T) {
	s := defaultSpec(t)
	d := s.Pick(agentic.Need{Role: "code", Complexity: 3, Local: true}, 0, nil, allAvail)
	if d.Winner == nil || d.Winner.ID != "local-qwen-coder-7b" {
		t.Fatalf("want local coder, got %+v (%s)", d.Winner, d.Reason)
	}
	for _, m := range d.Chain {
		if !strings.HasPrefix(m, "ollama/") {
			t.Fatalf("non-local %s in a local-only chain", m)
		}
	}
}

func TestOverridesAndAliases(t *testing.T) {
	o := agentic.ParseOverride("code:4:local")
	if o.Role != "code" || o.Complexity != 4 || !o.Local {
		t.Fatalf("got %+v", o)
	}
	o = agentic.ParseOverride("role=review, complexity=9")
	if o.Role != "review" || o.Complexity != 5 {
		t.Fatalf("got %+v", o)
	}
	if o := agentic.ParseOverride("banana"); o.Role != "" || o.Complexity != 0 {
		t.Fatalf("unknown words must be ignored, got %+v", o)
	}
	a, suf, ok := agentic.SplitAlias("Agent:code:4", []string{"agent", "task"})
	if !ok || a != "agent" || suf != "code:4" {
		t.Fatalf("got %q %q %v", a, suf, ok)
	}
	if _, _, ok := agentic.SplitAlias("auto", []string{"agent"}); ok {
		t.Fatal("auto is not an agentic alias")
	}
	n := agentic.Need{Role: "chat", Complexity: 2, Source: "heuristic"}
	n.Apply(agentic.ParseOverride("plan"), "header")
	if n.Role != "plan" || n.Complexity != 2 || n.Source != "header" {
		t.Fatalf("got %+v", n)
	}
}

func TestMergeReplacesByID(t *testing.T) {
	s := defaultSpec(t)
	off := false
	esc := 0
	m := s.Merge(agentic.Spec{
		Escalations: &esc,
		Roles:       map[string]int{"chat": 3},
		Catalog: []agentic.Entry{
			{ID: "deepseek-reasoner", Model: "deepseek/deepseek-reasoner", Tier: 5, Enabled: &off},
			{ID: "my-local", Model: "ollama/qwen3:32b", Tier: 4},
		},
	})
	if m.Roles["chat"] != 3 || m.Roles["code"] != 4 {
		t.Fatalf("roles not merged per key: %v", m.Roles)
	}
	if len(m.Catalog) != len(s.Catalog)+1 || m.MaxEscalations() != 0 {
		t.Fatalf("catalog %d (base %d), escalations %d", len(m.Catalog), len(s.Catalog), m.MaxEscalations())
	}
	d := m.Pick(agentic.Need{Role: "plan", Complexity: 5}, 0, nil, allAvail)
	for _, c := range d.Cands {
		if c.ID == "deepseek-reasoner" && c.Lost != "disabled" {
			t.Fatalf("disabled entry not reported as such: %q", c.Lost)
		}
	}
}

func TestDefaultModeIsRozkladJazdy(t *testing.T) {
	s := builtinSpec(t)
	if s.EffectiveMode() != agentic.ModeRozkladJazdy || agentic.DefaultMode != agentic.ModeRozkladJazdy {
		t.Fatalf("default mode %q", s.EffectiveMode())
	}
	if (agentic.Spec{}).EffectiveMode() != agentic.ModeRozkladJazdy {
		t.Fatal("an empty spec must also default to [Rozkład Jazdy]")
	}
	// "hi" — the cheapest task there is — still gets the strongest model.
	n := agentic.Classify([]agentic.Msg{{Role: "user", Content: "hi"}}, false)
	d := s.Pick(n, 0, nil, allAvail)
	if d.Mode != agentic.ModeRozkladJazdy || d.ModeName != "[Rozkład Jazdy]" || d.Winner == nil || d.Winner.Tier != 5 {
		t.Fatalf("want a tier-5 pick in [Rozkład Jazdy], got %+v (%s)", d.Winner, d.Reason)
	}
	if d.Winner.ID != "deepseek-reasoner" {
		t.Fatalf("gemini-pro is off by default (free quota 0); want deepseek-reasoner, got %s", d.Winner.ID)
	}
	// Tie on tier: the biggest context wins (tier 4 below the winner).
	for _, c := range d.Cands {
		if c.Rank == 2 && c.ID != "gemini-flash-latest" {
			t.Fatalf("second must be the 1M-context tier-4 entry, got %s", c.ID)
		}
	}
	// Strongest first, all eligible entries, no "below tier".
	prev := 6
	byModel := map[string]agentic.Entry{}
	for _, e := range s.Catalog {
		byModel[e.Model] = e
	}
	for _, m := range d.Chain {
		if tier := byModel[m].Tier; tier > prev {
			t.Fatalf("chain not strongest-first: %v", d.Chain)
		} else {
			prev = tier
		}
	}
	if !strings.HasPrefix(d.Reason, "[Rozkład Jazdy]") || !strings.Contains(d.Header(), "mode=rozklad-jazdy") {
		t.Fatalf("reason/header do not name the mode: %s | %s", d.Reason, d.Header())
	}
}

func TestRozkladJazdyIgnoresCapsKeepsHardFilters(t *testing.T) {
	s := builtinSpec(t)
	// sorting-stays-cheap caps classify at tier 2 in Jako Tako only.
	d := s.Pick(agentic.Need{Role: "classify", Complexity: 1}, 0, nil, allAvail)
	if d.Winner == nil || d.Winner.Tier != 5 {
		t.Fatalf("tier cap applied in [Rozkład Jazdy]: %+v", d.Winner)
	}
	// Tools: still a hard filter.
	d = s.Pick(agentic.Need{Role: "chat", Complexity: 1, Tools: true}, 0, nil, allAvail)
	for _, c := range d.Cands {
		if c.Rank > 0 {
			for _, e := range s.Catalog {
				if e.ID == c.ID && !e.Tools {
					t.Fatalf("%s without tools in the chain", c.ID)
				}
			}
		}
	}
	// Zero keys: the strongest keyless/scraper model wins, not a tiny one.
	d = s.Pick(agentic.Need{Role: "chat", Complexity: 1}, 0, nil, availWhere("keyless", "scraper"))
	if d.Winner == nil || d.Winner.Tier != 4 {
		t.Fatalf("want the strongest available (tier 4), got %+v (%s)", d.Winner, d.Reason)
	}
	// Retry after a rejected answer drops the failing entry, stays all in.
	d2 := s.Pick(agentic.Need{Role: "chat", Complexity: 1}, 1, map[string]bool{d.Winner.Model: true}, availWhere("keyless", "scraper"))
	if d2.Winner == nil || d2.Winner.Model == d.Winner.Model || d2.Winner.Tier != 4 {
		t.Fatalf("retry: %+v", d2.Winner)
	}
}

func TestModeSpellingsAndPerRequestOverride(t *testing.T) {
	for in, want := range map[string]string{
		"[Rozkład Jazdy]": agentic.ModeRozkladJazdy, "rozklad jazdy": agentic.ModeRozkladJazdy,
		"ROZKLAD_JAZDY": agentic.ModeRozkladJazdy, "max": agentic.ModeRozkladJazdy,
		"Jako Tako": agentic.ModeJakoTako, "jako-tako": agentic.ModeJakoTako, "jt": agentic.ModeJakoTako,
	} {
		if got, ok := agentic.NormalizeMode(in); !ok || got != want {
			t.Errorf("NormalizeMode(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := agentic.NormalizeMode("turbo"); ok {
		t.Error("unknown mode accepted")
	}
	o := agentic.ParseOverride("jako tako:code:4")
	if o.Mode != agentic.ModeJakoTako || o.Role != "code" || o.Complexity != 4 {
		t.Fatalf("got %+v", o)
	}
	if o := agentic.ParseOverride("mode=[Rozkład Jazdy], role=chat"); o.Mode != agentic.ModeRozkladJazdy || o.Role != "chat" {
		t.Fatalf("got %+v", o)
	}
	// Per-request Jako Tako over the [Rozkład Jazdy] default.
	n := agentic.Need{Role: "chat", Complexity: 1}
	n.Apply(agentic.ParseOverride("jako-tako"), "header")
	if d := builtinSpec(t).Pick(n, 0, nil, allAvail); d.Mode != agentic.ModeJakoTako || d.Required != 1 {
		t.Fatalf("per-request mode ignored: %s", d.Reason)
	}
	if probs := (agentic.Spec{Mode: "turbo"}).Validate(nil); len(probs) == 0 {
		t.Fatal("unknown configured mode not reported")
	}
}
