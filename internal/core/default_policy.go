package core

import (
	"context"
	"sort"
	"strings"
)

// DefaultRoutePolicy resolves a ModelSelector into a chain of Provider-Model pairs.
//
// Resolution rules (in priority order):
//  1. If sel.Name matches a known route alias (e.g. "auto", "free") → use that chain.
//  2. If sel.Name contains "/" and the part before the first "/" is a known
//     provider → return that single (provider, model) entry + auto as safety net.
//  3. If sel.Name matches a known provider name → use the `auto` chain filtered
//     to that provider only.
//  4. If sel.Name is a bare model ID → scan `auto` chain for any entry whose
//     model ID contains sel.Name; if found use that; else use full `auto`.
//  5. Fallback: the `auto` route.
//
// The chains are data (from config), not compiled-in constants.
type DefaultRoutePolicy struct {
	routes  map[string][]string // alias → []provider/model
	provs   []string            // sorted provider names (for rule 3)
	autoKey string              // default fallback alias name
	weights map[string]int      // provider → weight (chain tiebreaks, STEP 7)
}

// NewDefaultRoutePolicy creates the policy from a routes map and provider list.
// routes should be the full map from config (alias → chain entries).
// providers is the list of known provider names (any order).
func NewDefaultRoutePolicy(routes map[string][]string, providers []string, autoKey string) *DefaultRoutePolicy {
	if autoKey == "" {
		autoKey = "auto"
	}
	// Sort providers for stable iteration.
	sorted := make([]string, len(providers))
	copy(sorted, providers)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return &DefaultRoutePolicy{
		routes:  routes,
		provs:   sorted,
		autoKey: autoKey,
	}
}

var _ RoutePolicy = (*DefaultRoutePolicy)(nil)

// SetWeights installs per-provider weights used to order chain entries when
// several providers tie for the same requested model (rule 4). Higher weight
// goes first; equal weights keep the route-table order (stable sort).
func (p *DefaultRoutePolicy) SetWeights(w map[string]int) {
	norm := make(map[string]int, len(w))
	for k, v := range w {
		norm[strings.ToLower(k)] = v
	}
	p.weights = norm
}

// Resolve implements RoutePolicy.
func (p *DefaultRoutePolicy) Resolve(ctx context.Context, sel ModelSelector, known map[string]Model) []Model {
	name := strings.TrimSpace(sel.Name)
	if name == "" {
		name = p.autoKey
	}

	// Rule 1: exact route alias match (case-insensitive).
	if chain, ok := p.routes[strings.ToLower(name)]; ok {
		return expandChain(chain)
	}

	// Rule 2: provider/model — the part before first "/" is a known provider.
	if idx := strings.Index(name, "/"); idx > 0 {
		pname := strings.ToLower(name[:idx])
		// Check if pname is a known provider.
		if isKnownProvider(p.provs, pname) {
			// Build: that entry first, then remaining auto chain (safety net).
			entry := name // "provider/model" (preserved case for model)
			result := expandChain([]string{entry})
			// Safety net: append auto entries not already present.
			autoChain, ok := p.routes[p.autoKey]
			if ok {
				seen := map[string]bool{strings.ToLower(entry): true}
				for _, ac := range autoChain {
					if !seen[strings.ToLower(ac)] {
						result = append(result, expandChain([]string{ac})...)
						seen[strings.ToLower(ac)] = true
					}
				}
			}
			return result
		}
	}

	// Rule 3: bare provider name.
	if isKnownProvider(p.provs, strings.ToLower(name)) {
		// Use auto chain but only that provider's entries; if none, just auto.
		autoChain, ok := p.routes[p.autoKey]
		if ok {
			filtered := make([]string, 0)
			for _, entry := range autoChain {
				if strings.EqualFold(entry[:strings.Index(entry, "/")], name) {
					filtered = append(filtered, entry)
				}
			}
			if len(filtered) > 0 {
				return expandChain(filtered)
			}
		}
		return expandChain(p.routes[p.autoKey])
	}

	// Rule 4: bare model ID — scan the auto chain for matches.
	// When several providers offer the requested model, that is a tie:
	// order the candidates by provider weight (higher first, stable).
	if autoChain, ok := p.routes[p.autoKey]; ok {
		matches := make([]string, 0, 1)
		for _, entry := range autoChain {
			// Match the model part (after first "/") case-insensitive.
			midx := strings.Index(entry, "/")
			if midx > 0 && midx < len(entry)-1 {
				modelPart := entry[midx+1:]
				if strings.EqualFold(modelPart, name) {
					matches = append(matches, entry)
				}
			}
		}
		if len(matches) > 1 {
			matches = sortByWeight(p.weights, matches)
		}
		if len(matches) > 0 {
			return expandChain(matches)
		}
	}

	// Rule 5: full auto fallback.
	if autoChain, ok := p.routes[p.autoKey]; ok {
		return expandChain(autoChain)
	}

	return nil
}

// expandChain converts []string ("provider/model") to []Model with Provider + ID set.
func expandChain(entries []string) []Model {
	out := make([]Model, 0, len(entries))
	for _, e := range entries {
		idx := strings.Index(e, "/")
		if idx <= 0 || idx >= len(e)-1 {
			// Malformed entry; put it as model with unknown provider.
			out = append(out, Model{Provider: e, ID: e})
			continue
		}
		prov := e[:idx]
		model := e[idx+1:]
		out = append(out, Model{Provider: prov, ID: model})
	}
	return out
}

// sortByWeight stable-sorts "provider/model" entries by provider weight
// (higher weight first). Entries with equal (or missing) weight keep their
// original relative order.
func sortByWeight(weights map[string]int, entries []string) []string {
	w := func(e string) int {
		i := strings.Index(e, "/")
		if i <= 0 {
			return 0
		}
		if weights == nil {
			return 0
		}
		return weights[strings.ToLower(e[:i])]
	}
	out := make([]string, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool { return w(out[i]) > w(out[j]) })
	return out
}

func isKnownProvider(provs []string, name string) bool {
	for _, p := range provs {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}
