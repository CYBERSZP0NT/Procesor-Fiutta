// Package agentic is the built-in task router: it reads what a chat request
// asks for (role + complexity), works out the tier of model that is "just
// enough" for it, and turns that into an ordered fallback chain — cheapest
// sufficient model first, stronger ones next (escalation), weaker ones last
// (best effort). Every decision carries a one-line reason and every catalog
// entry with why it lost, so the choice is never a black box.
//
// The router decides by readable rules, not by asking a model: tiers, costs
// and rules are data (defaults/agentic.yaml + the user's `agentic:` config
// section), the classifier is a deterministic heuristic.
package agentic

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/silverx/freellm-router/defaults"
)

// Spec is the whole task-router configuration.
type Spec struct {
	// Enabled turns the task router on (default true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Aliases are the model names that trigger task routing (default
	// "agent", "task"). "agent:code:4" style suffixes force role/complexity.
	Aliases []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	// Roles maps a task role to its base tier (1-5).
	Roles map[string]int `yaml:"roles,omitempty" json:"roles,omitempty"`
	// Complexity maps complexity 1-5 to a tier adjustment.
	Complexity map[int]int `yaml:"complexity,omitempty" json:"complexity,omitempty"`
	// Catalog lists the models the router may pick, with tier/cost/speed.
	Catalog []Entry `yaml:"catalog,omitempty" json:"catalog,omitempty"`
	// Rules adjust the tier window, preferences and allowed kinds.
	Rules []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
	// BelowTierFallback appends weaker entries at the end of the chain as
	// a best-effort last resort (default true: free sources are flaky).
	BelowTierFallback *bool `yaml:"below_tier_fallback,omitempty" json:"below_tier_fallback,omitempty"`
	// Escalations is how many times a non-streaming answer that comes back
	// empty or filtered is retried one tier higher (default 1, 0 = off).
	Escalations *int `yaml:"escalations,omitempty" json:"escalations,omitempty"`
}

// Entry is one routable model in the catalog.
type Entry struct {
	ID      string   `yaml:"id" json:"id"`
	Model   string   `yaml:"model" json:"model"` // "provider/model"
	Tier    int      `yaml:"tier" json:"tier"`   // 1 (tiny) … 5 (strongest)
	Cost    int      `yaml:"cost" json:"cost"`   // 0 free/local … higher = burns paid credits
	Speed   int      `yaml:"speed" json:"speed"` // 1 slow … 3 fast
	Context int      `yaml:"context,omitempty" json:"context,omitempty"`
	Tools   bool     `yaml:"tools,omitempty" json:"tools,omitempty"`
	GoodAt  []string `yaml:"good_at,omitempty" json:"good_at,omitempty"`
	Enabled *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Note    string   `yaml:"note,omitempty" json:"note,omitempty"`
	// Where is the provider kind (local | keyless | keyed | scraper |
	// sidecar | plugin). Filled from the provider config when empty.
	Where string `yaml:"where,omitempty" json:"where,omitempty"`
}

// Provider returns the provider part of Model.
func (e Entry) Provider() string {
	if i := strings.Index(e.Model, "/"); i > 0 {
		return e.Model[:i]
	}
	return e.Model
}

// ModelID returns the model part of Model (may itself contain "/").
func (e Entry) ModelID() string {
	if i := strings.Index(e.Model, "/"); i > 0 {
		return e.Model[i+1:]
	}
	return ""
}

func (e Entry) enabled() bool { return e.Enabled == nil || *e.Enabled }

// Rule narrows the tier window or the candidates for matching tasks.
type Rule struct {
	ID        string   `yaml:"id" json:"id"`
	When      When     `yaml:"when" json:"when"`
	MinTier   int      `yaml:"min_tier,omitempty" json:"min_tier,omitempty"`
	MaxTier   int      `yaml:"max_tier,omitempty" json:"max_tier,omitempty"`
	Prefer    []string `yaml:"prefer,omitempty" json:"prefer,omitempty"`         // entry ids, best first
	OnlyWhere []string `yaml:"only_where,omitempty" json:"only_where,omitempty"` // allowed provider kinds
	Note      string   `yaml:"note,omitempty" json:"note,omitempty"`
}

// When is a rule's condition; empty fields match anything.
type When struct {
	Role          StrList `yaml:"role,omitempty" json:"role,omitempty"`
	ComplexityMin int     `yaml:"complexity_min,omitempty" json:"complexity_min,omitempty"`
	ComplexityMax int     `yaml:"complexity_max,omitempty" json:"complexity_max,omitempty"`
	Tools         *bool   `yaml:"tools,omitempty" json:"tools,omitempty"`
	Local         *bool   `yaml:"local,omitempty" json:"local,omitempty"`
}

// StrList accepts either a YAML string or a list of strings.
type StrList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *StrList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = StrList{n.Value}
		return nil
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*s = l
	return nil
}

func (s StrList) has(v string) bool {
	for _, x := range s {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// Default returns the embedded default spec (defaults/agentic.yaml).
func Default() (*Spec, error) {
	var s Spec
	if err := yaml.Unmarshal(defaults.AgenticYAML, &s); err != nil {
		return nil, fmt.Errorf("agentic: parse embedded defaults: %w", err)
	}
	return &s, nil
}

// Merge overlays user settings onto s and returns the result: scalars and
// aliases replace, roles/complexity merge per key, catalog entries and
// rules replace by id (new ids are appended).
func (s Spec) Merge(o Spec) Spec {
	out := s
	if o.Enabled != nil {
		out.Enabled = o.Enabled
	}
	if len(o.Aliases) > 0 {
		out.Aliases = o.Aliases
	}
	if o.BelowTierFallback != nil {
		out.BelowTierFallback = o.BelowTierFallback
	}
	if o.Escalations != nil {
		out.Escalations = o.Escalations
	}
	out.Roles = mergeMap(s.Roles, o.Roles)
	out.Complexity = mergeMap(s.Complexity, o.Complexity)
	out.Catalog = mergeByID(s.Catalog, o.Catalog, func(e Entry) string { return e.ID })
	out.Rules = mergeByID(s.Rules, o.Rules, func(r Rule) string { return r.ID })
	return out
}

func mergeMap[K comparable](a, b map[K]int) map[K]int {
	out := make(map[K]int, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func mergeByID[T any](base, over []T, id func(T) string) []T {
	out := append([]T(nil), base...)
	idx := make(map[string]int, len(out))
	for i, v := range out {
		idx[id(v)] = i
	}
	for _, v := range over {
		if i, ok := idx[id(v)]; ok {
			out[i] = v
			continue
		}
		idx[id(v)] = len(out)
		out = append(out, v)
	}
	return out
}

// IsEnabled reports whether task routing is on.
func (s Spec) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// AliasNames returns the configured aliases (default "agent", "task").
func (s Spec) AliasNames() []string {
	if len(s.Aliases) == 0 {
		return []string{"agent", "task"}
	}
	return s.Aliases
}

func (s Spec) fallback() bool { return s.BelowTierFallback == nil || *s.BelowTierFallback }

// MaxEscalations returns the quality-gate escalation budget.
func (s Spec) MaxEscalations() int {
	if s.Escalations == nil {
		return 1
	}
	if *s.Escalations < 0 {
		return 0
	}
	return *s.Escalations
}

// Validate reports structural problems (duplicate ids, bad tiers, rules
// that prefer unknown entries). knownProviders, when non-nil, also flags
// catalog entries whose provider is not configured.
func (s Spec) Validate(knownProviders map[string]bool) []string {
	var probs []string
	ids := map[string]bool{}
	for i, e := range s.Catalog {
		where := fmt.Sprintf("agentic.catalog[%d] %q", i, e.ID)
		switch {
		case e.ID == "":
			probs = append(probs, where+": id is required")
		case ids[e.ID]:
			probs = append(probs, where+": duplicate id")
		}
		ids[e.ID] = true
		if e.ModelID() == "" {
			probs = append(probs, where+": model must be provider/model")
		} else if knownProviders != nil && !knownProviders[strings.ToLower(e.Provider())] {
			probs = append(probs, where+": unknown provider "+e.Provider())
		}
		if e.Tier < 1 || e.Tier > 5 {
			probs = append(probs, where+": tier must be 1-5")
		}
	}
	for r, t := range s.Roles {
		if t < 1 || t > 5 {
			probs = append(probs, fmt.Sprintf("agentic.roles.%s: tier must be 1-5", r))
		}
	}
	for i, r := range s.Rules {
		where := fmt.Sprintf("agentic.rules[%d] %q", i, r.ID)
		if r.ID == "" {
			probs = append(probs, where+": id is required")
		}
		for _, p := range r.Prefer {
			if !ids[p] {
				probs = append(probs, where+": prefers unknown entry "+p)
			}
		}
		if r.MinTier != 0 && r.MaxTier != 0 && r.MinTier > r.MaxTier {
			probs = append(probs, where+": min_tier > max_tier")
		}
	}
	sort.Strings(probs)
	return probs
}
