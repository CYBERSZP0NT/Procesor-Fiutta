package agentic

import (
	"fmt"
	"sort"
	"strings"
)

// Candidate is one catalog entry as the picker saw it.
type Candidate struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Tier  int    `json:"tier"`
	Cost  int    `json:"cost"`
	Where string `json:"where,omitempty"`
	// Rank is the 1-based position in the chain (0 = not in the chain).
	Rank int `json:"rank,omitempty"`
	// Lost says why the entry is not first (empty for the winner).
	Lost string `json:"lost,omitempty"`
}

// Decision is the outcome of Pick.
type Decision struct {
	Mode     string      `json:"mode"`      // mode id (rozklad-jazdy | jako-tako | live)
	ModeName string      `json:"mode_name"` // display name ([Rozkład Jazdy] | Jako Tako | Live)
	Need     Need        `json:"need"`
	Attempt  int         `json:"attempt"`  // escalation step (0 = first try)
	Base     int         `json:"base"`     // role tier
	Adjust   int         `json:"adjust"`   // complexity adjustment
	Required int         `json:"required"` // final required tier after rules (5 in [Rozkład Jazdy])
	Rules    []string    `json:"rules,omitempty"`
	Winner   *Candidate  `json:"winner,omitempty"`
	Chain    []string    `json:"chain"` // provider/model, try in order
	Reason   string      `json:"reason"`
	Cands    []Candidate `json:"candidates"`
	// Via is filled by the caller with the entry that actually served.
	Via string `json:"via,omitempty"`
}

// AvailFunc reports whether an entry can be tried right now (key set,
// local daemon up, not cooling down). why explains a false. It is called
// once per eligible entry; cache per provider on the caller's side.
type AvailFunc func(entry Entry) (ok bool, why string)

// Pick turns a Need into a ranked chain. attempt raises the required tier
// (escalation); exclude lists "provider/model" labels to leave out (e.g.
// the one that just produced an empty answer); avail may be nil.
//
// The mode comes from need.Mode, else the spec. In [Rozkład Jazdy] every
// eligible entry is in the chain, strongest first (tier, then context),
// cost and speed ignored, rule tier caps ignored; hard filters (tools,
// context, local, availability) still apply — those are physics, not
// thrift. Live is all in too, ranked by richness (Richer: size tier,
// context, daily free tokens, intelligence rank). In Jako
// Tako the cheapest sufficient entry goes first.
func (s Spec) Pick(need Need, attempt int, exclude map[string]bool, avail AvailFunc) Decision {
	d := Decision{Need: need, Attempt: attempt}
	d.Mode = s.EffectiveMode()
	if m, ok := NormalizeMode(need.Mode); ok {
		d.Mode = m
	}
	d.ModeName = ModeName(d.Mode)
	full := allIn(d.Mode)
	rich := d.Mode == ModeLive

	d.Base = s.Roles[need.Role]
	if d.Base == 0 {
		d.Base = s.Roles["chat"]
		if d.Base == 0 {
			d.Base = 2
		}
	}
	d.Adjust = s.Complexity[need.Complexity]
	required := d.Base + d.Adjust + attempt

	// Rules: tighten the window, collect preferences and allowed kinds.
	minT, maxT := 1, 5
	var prefer []string
	var only []string
	for _, r := range s.Rules {
		if !r.matches(need) {
			continue
		}
		d.Rules = append(d.Rules, r.ID)
		if r.MinTier > minT {
			minT = r.MinTier
		}
		if r.MaxTier != 0 && r.MaxTier < maxT {
			maxT = r.MaxTier
		}
		prefer = append(prefer, r.Prefer...)
		if len(r.OnlyWhere) > 0 {
			only = intersect(only, r.OnlyWhere)
		}
	}
	if need.Local {
		only = intersect(only, []string{"local"})
	}
	if minT > maxT {
		minT = maxT
	}
	// Escalation may break through a rule's max_tier (the cap exists to save
	// money, not to keep failing), but never through min_tier.
	if attempt > 0 && maxT < 5 {
		maxT = min(5, maxT+attempt)
	}
	d.Required = clamp(clamp(required, minT, maxT), 1, 5)
	if full {
		d.Required = 5 // all in: no "enough", no caps
	}

	preferIdx := map[string]int{}
	for i, id := range prefer {
		if _, seen := preferIdx[id]; !seen {
			preferIdx[id] = i
		}
	}

	// Filter.
	var ok, below []int
	cands := make([]Candidate, len(s.Catalog))
	for i, e := range s.Catalog {
		c := Candidate{ID: e.ID, Model: e.Model, Tier: e.Tier, Cost: e.Cost, Where: e.Where}
		switch {
		case !e.enabled():
			c.Lost = "disabled"
		case exclude[e.Model]:
			c.Lost = "already tried, answer rejected"
		case need.Tools && !e.Tools:
			c.Lost = "no tool calling"
		case e.Context > 0 && need.Tokens > e.Context:
			c.Lost = fmt.Sprintf("context %d < prompt ~%d tokens", e.Context, need.Tokens)
		case only != nil && !StrList(only).has(e.Where):
			c.Lost = "kind " + orDash(e.Where) + " not allowed (" + strings.Join(only, ",") + ")"
		}
		if c.Lost == "" && avail != nil {
			if good, why := avail(e); !good {
				c.Lost = "unavailable: " + orDash(why)
			}
		}
		if c.Lost == "" {
			if !full && e.Tier < d.Required {
				c.Lost = fmt.Sprintf("tier %d < required %d", e.Tier, d.Required)
				below = append(below, i)
			} else {
				ok = append(ok, i)
			}
		}
		cands[i] = c
	}

	// Rank: lowest sufficient tier, then preference, then good_at, then
	// cost, then speed, then catalog order — "just enough", not more.
	role := need.Role
	key := func(i int) []int {
		e := s.Catalog[i]
		p, has := preferIdx[e.ID]
		if !has {
			p = 1 << 20
		}
		g := 1
		if StrList(e.GoodAt).has(role) {
			g = 0
		}
		if full {
			return []int{-e.Tier, p, g, -e.Context, -e.Speed, i}
		}
		return []int{e.Tier, p, g, e.Cost, -e.Speed, i}
	}
	sort.SliceStable(ok, func(a, b int) bool {
		if rich {
			ea, eb := s.Catalog[ok[a]], s.Catalog[ok[b]]
			// Rule preferences still come first among equals in tier.
			if ea.Tier == eb.Tier {
				pa, pb := key(ok[a])[1], key(ok[b])[1]
				if pa != pb {
					return pa < pb
				}
			}
			if Richer(ea, eb) != Richer(eb, ea) {
				return Richer(ea, eb)
			}
			return ok[a] < ok[b]
		}
		return lessInts(key(ok[a]), key(ok[b]))
	})
	// Below-tier best effort: strongest first, then cheapest.
	sort.SliceStable(below, func(a, b int) bool {
		ea, eb := s.Catalog[below[a]], s.Catalog[below[b]]
		if ea.Tier != eb.Tier {
			return ea.Tier > eb.Tier
		}
		return lessInts(key(below[a])[1:], key(below[b])[1:])
	})

	order := ok
	if s.fallback() {
		order = append(append([]int(nil), ok...), below...)
	}
	for rank, i := range order {
		cands[i].Rank = rank + 1
		d.Chain = append(d.Chain, s.Catalog[i].Model)
	}

	if len(order) > 0 {
		wi := order[0]
		w := cands[wi]
		w.Lost = ""
		d.Winner = &w
		for _, i := range ok[min(1, len(ok)):] {
			if rich {
				cands[i].Lost = loseWhyRich(s.Catalog[i], s.Catalog[wi], preferIdx)
			} else if full {
				cands[i].Lost = loseWhyFull(s.Catalog[i], s.Catalog[wi], role, preferIdx)
			} else {
				cands[i].Lost = loseWhy(s.Catalog[i], s.Catalog[wi], role, preferIdx)
			}
		}
		cands[wi].Lost = "" // also a best-effort winner from below the tier
	}
	d.Cands = cands
	d.Reason = d.explain(s, len(ok) > 0)
	return d
}

func (r Rule) matches(n Need) bool {
	w := r.When
	if len(w.Role) > 0 && !w.Role.has(n.Role) {
		return false
	}
	if w.ComplexityMin != 0 && n.Complexity < w.ComplexityMin {
		return false
	}
	if w.ComplexityMax != 0 && n.Complexity > w.ComplexityMax {
		return false
	}
	if w.Tools != nil && *w.Tools != n.Tools {
		return false
	}
	if w.Local != nil && *w.Local != n.Local {
		return false
	}
	return true
}

func loseWhy(e, win Entry, role string, prefer map[string]int) string {
	switch {
	case e.Tier > win.Tier:
		return fmt.Sprintf("tier %d is more than needed", e.Tier)
	case hasPref(prefer, win.ID) && !hasPref(prefer, e.ID):
		return "not preferred by a rule"
	case hasPref(prefer, win.ID) && prefer[e.ID] > prefer[win.ID]:
		return "lower in the rule's prefer list"
	case StrList(win.GoodAt).has(role) && !StrList(e.GoodAt).has(role):
		return "not good at " + role
	case e.Cost > win.Cost:
		return fmt.Sprintf("costs more (%d > %d)", e.Cost, win.Cost)
	case e.Speed < win.Speed:
		return fmt.Sprintf("slower (%d < %d)", e.Speed, win.Speed)
	default:
		return "later in the catalog (fallback)"
	}
}

func loseWhyFull(e, win Entry, role string, prefer map[string]int) string {
	switch {
	case e.Tier < win.Tier:
		return fmt.Sprintf("weaker (tier %d < %d)", e.Tier, win.Tier)
	case hasPref(prefer, win.ID) && !hasPref(prefer, e.ID):
		return "not preferred by a rule"
	case hasPref(prefer, win.ID) && prefer[e.ID] > prefer[win.ID]:
		return "lower in the rule's prefer list"
	case StrList(win.GoodAt).has(role) && !StrList(e.GoodAt).has(role):
		return "not good at " + role
	case e.Context < win.Context:
		return fmt.Sprintf("smaller context (%d < %d)", e.Context, win.Context)
	case e.Speed < win.Speed:
		return fmt.Sprintf("slower (%d < %d)", e.Speed, win.Speed)
	default:
		return "later in the catalog (fallback)"
	}
}

func loseWhyRich(e, win Entry, prefer map[string]int) string {
	switch {
	case e.Tier < win.Tier:
		return fmt.Sprintf("poorer size class (tier %d < %d)", e.Tier, win.Tier)
	case hasPref(prefer, win.ID) && !hasPref(prefer, e.ID):
		return "not preferred by a rule"
	case e.Context < win.Context:
		return fmt.Sprintf("smaller context (%d < %d)", e.Context, win.Context)
	case e.Quota < win.Quota:
		return fmt.Sprintf("smaller free quota (%d < %d tokens/day)", e.Quota, win.Quota)
	case rankKey(e.Rank) > rankKey(win.Rank):
		return fmt.Sprintf("lower intelligence rank (%d > %d)", e.Rank, win.Rank)
	case e.Speed < win.Speed:
		return fmt.Sprintf("slower (%d < %d)", e.Speed, win.Speed)
	default:
		return "later in the catalog (fallback)"
	}
}

func hasPref(m map[string]int, id string) bool { _, ok := m[id]; return ok }

func (d Decision) explain(s Spec, sufficient bool) string {
	n := d.Need
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s c%d (%s) -> ", d.ModeName, n.Role, n.Complexity, n.Source)
	if allIn(d.Mode) {
		if d.Mode == ModeLive {
			b.WriteString("by richness, richest first [")
		} else {
			b.WriteString("all in, strongest first [")
		}
		if d.Attempt > 0 {
			fmt.Fprintf(&b, "retry %d, ", d.Attempt)
		}
		if len(d.Rules) > 0 {
			fmt.Fprintf(&b, "rules %s, ", strings.Join(d.Rules, ","))
		}
		b.WriteString("cost and tier caps ignored]")
		if n.Tools {
			b.WriteString(", needs tools")
		}
		if n.Local {
			b.WriteString(", local only")
		}
		if d.Winner == nil {
			b.WriteString("; nothing in the catalog qualifies")
		} else {
			e := s.Catalog[d.idx(s)]
			fmt.Fprintf(&b, "; picked %s (tier %d, context %d), %d in chain", e.ID, e.Tier, e.Context, len(d.Chain))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "tier %d", d.Required)
	fmt.Fprintf(&b, " [%s %d %+d", n.Role, d.Base, d.Adjust)
	if d.Attempt > 0 {
		fmt.Fprintf(&b, ", escalation +%d", d.Attempt)
	}
	if len(d.Rules) > 0 {
		fmt.Fprintf(&b, ", rules %s", strings.Join(d.Rules, ","))
	}
	b.WriteString("]")
	if n.Tools {
		b.WriteString(", needs tools")
	}
	if n.Local {
		b.WriteString(", local only")
	}
	switch {
	case d.Winner == nil:
		b.WriteString("; nothing in the catalog qualifies")
	case !sufficient:
		fmt.Fprintf(&b, "; no tier-%d model available, best effort %s (tier %d)", d.Required, d.Winner.ID, d.Winner.Tier)
	default:
		e := s.Catalog[d.idx(s)]
		fmt.Fprintf(&b, "; picked %s (tier %d, cost %d", e.ID, e.Tier, e.Cost)
		if StrList(e.GoodAt).has(n.Role) {
			b.WriteString(", good at " + n.Role)
		}
		fmt.Fprintf(&b, "), %d in chain", len(d.Chain))
	}
	return b.String()
}

func (d Decision) idx(s Spec) int {
	for i, e := range s.Catalog {
		if d.Winner != nil && e.ID == d.Winner.ID {
			return i
		}
	}
	return 0
}

func intersect(a, b []string) []string {
	if a == nil {
		return append([]string(nil), b...)
	}
	var out []string
	for _, x := range a {
		if StrList(b).has(x) {
			out = append(out, x)
		}
	}
	if out == nil {
		out = []string{} // non-nil: "nothing allowed"
	}
	return out
}

func lessInts(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Header renders the decision as a compact ASCII header value.
func (d Decision) Header() string {
	pick := "-"
	if d.Winner != nil {
		pick = d.Winner.Model
	}
	return fmt.Sprintf("mode=%s; role=%s; complexity=%d; tier=%d; pick=%s; source=%s; attempt=%d",
		d.Mode, d.Need.Role, d.Need.Complexity, d.Required, pick, d.Need.Source, d.Attempt)
}

// RolesByTier lists the configured roles, cheapest first (for display).
func (s Spec) RolesByTier() []string { return sortedRoles(s.Roles) }
