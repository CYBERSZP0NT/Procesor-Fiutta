package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// STEP 6 — Catalog: merge model/tier knowledge from many sources, dedupe by
// precedence, and cache the result on disk so the router still "knows" its
// free tiers with the network switched off.
//
// Precedence (for the same provider/model key):
//
//	local file (active config) > remote feeds > builtin (embedded defaults)
// ---------------------------------------------------------------------------

// Priority tiers for NamedSource. Higher wins in a merge conflict.
const (
	PriorityLocal    = 100 // active config (defaults + user file)
	PriorityRemote   = 50  // remote catalog feeds (freellmapi, awesome, …)
	PriorityEmbedded = 10  // pure builtin, offline-safe
)

// Source tags used in ModelNote.Tier.Source / ProviderSpec.Source.
const (
	SourceLocal    = "local"   // active configuration
	SourceEmbedded = "builtin" // embedded defaults
	SourceRemote   = "remote"  // generic remote feed (specific feed also named in notes)
)

// ProviderSpec is a ready-to-paste provider entry (used by `discover`).
type ProviderSpec struct {
	Name    string   `json:"name"`
	Impl    string   `json:"impl,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	KeyEnv  string   `json:"key_env,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Weight  int      `json:"weight,omitempty"`
	Notes   string   `json:"notes,omitempty"`
	Models  []string `json:"models,omitempty"`
	Source  string   `json:"source"` // where this spec came from
}

// SourceStatus records how each source behaved during the last resolve.
type SourceStatus struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	Count     int       `json:"count"`
	Err       string    `json:"err,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Snapshot is the merged, serializable catalog state (also the on-disk
// shape of catalog.json).
type Snapshot struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Stale     bool           `json:"stale,omitempty"` // true when served from cache after a failure
	Sources   []SourceStatus `json:"sources,omitempty"`
	Notes     []ModelNote    `json:"notes"`
	Providers []ProviderSpec `json:"providers,omitempty"`
}

// NamedSource is one catalog input. Fetch is required; ProviderList is
// optional (only sources that can enumerate complete provider entries
// provide it — this is what powers `discover`).
type NamedSource struct {
	Name         string
	Priority     int
	Fetch        func(ctx context.Context) ([]ModelNote, error)
	ProviderList func(ctx context.Context) ([]ProviderSpec, error)
}

type sourceResult struct {
	status SourceStatus
	notes  []ModelNote
	provs  []ProviderSpec
	prio   int
}

// Catalog orchestrates its sources with an on-disk cache + TTL.
type Catalog struct {
	mu           sync.Mutex
	refresh      time.Duration
	cacheFile    string
	fetchTimeout time.Duration
	sources      []NamedSource
}

// NewCatalog builds a Catalog over the given sources.
//
//	refresh      — cache TTL; Resolve() skips the network while the cache is
//	               younger than this.  <= 0 means "always refetch".
//	fetchTimeout — bound applied to the whole fetch phase.
//	cacheFile    — catalog.json path ("" = no on-disk cache).
func NewCatalog(refresh, fetchTimeout time.Duration, cacheFile string, sources ...NamedSource) *Catalog {
	if fetchTimeout <= 0 {
		fetchTimeout = 15 * time.Second
	}
	return &Catalog{
		refresh:      refresh,
		cacheFile:    cacheFile,
		fetchTimeout: fetchTimeout,
		sources:      sources,
	}
}

// RefreshTTL returns the configured cache TTL.
func (c *Catalog) RefreshTTL() time.Duration { return c.refresh }

// Resolve returns the merged snapshot, honouring the cache TTL.
//
// Behaviour (kill-network safe):
//  1. cache newer than TTL                     → return it (no network)
//  2. network fetch (all sources in parallel)  → merge, save cache, return
//  3. all sources failed AND a cache exists    → return the stale cache
func (c *Catalog) Resolve(ctx context.Context) (*Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.refresh > 0 {
		if cached, ok := c.loadCacheLocked(); ok && now.Sub(cached.FetchedAt) < c.refresh {
			cached.Stale = false
			return cached, nil
		}
	}

	fctx, fcancel := context.WithTimeout(ctx, c.fetchTimeout)
	defer fcancel()

	results := make([]sourceResult, len(c.sources))
	var wg sync.WaitGroup
	for i := range c.sources {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := c.sources[i]
			r := sourceResult{prio: src.Priority}
			r.status.Name = src.Name
			r.status.FetchedAt = time.Now()
			if src.Fetch != nil {
				notes, err := src.Fetch(fctx)
				if err != nil {
					r.status.Err = err.Error()
				} else {
					r.status.OK = true
					r.status.Count = len(notes)
					r.notes = notes
				}
			}
			if src.ProviderList != nil {
				provs, perr := src.ProviderList(fctx)
				if perr != nil {
					if r.status.Err == "" {
						r.status.Err = perr.Error()
					}
				} else {
					r.provs = provs
				}
			}
			results[i] = r
		}(i)
	}
	wg.Wait()

	anyOK := false
	for _, r := range results {
		if r.status.OK {
			anyOK = true
			break
		}
	}
	if !anyOK {
		if cached, ok := c.loadCacheLocked(); ok {
			cached.Stale = true
			return cached, nil
		}
		// Fall through: return an empty-but-well-formed snapshot.
	}

	snap := c.mergeLocked(results)
	snap.FetchedAt = time.Now()
	c.saveCacheLocked(snap)
	return snap, nil
}

// NotesForProvider returns merged notes for one provider (both the
// provider-level entry with empty Model and per-model entries).
func (s *Snapshot) NotesForProvider(name string) (provider string, models []ModelNote) {
	for _, n := range s.Notes {
		if n.Provider != name {
			continue
		}
		if n.Model == "" {
			provider = n.Tier.Notes
			continue
		}
		models = append(models, n)
	}
	return provider, models
}

// ProvidersByName indexes snapshot provider specs by name.
func (s *Snapshot) ProvidersByName() map[string]ProviderSpec {
	out := make(map[string]ProviderSpec, len(s.Providers))
	for _, p := range s.Providers {
		out[p.Name] = p
	}
	return out
}

// mergeLocked applies de-dup with precedence (higher priority wins; on a
// tie, the earlier source wins) and sorts for stable output.
func (c *Catalog) mergeLocked(results []sourceResult) *Snapshot {
	type noteEntry struct {
		note ModelNote
		prio int
		pos  int
	}
	notes := make(map[string]*noteEntry)
	provs := make(map[string]ProviderSpec)
	provPrio := make(map[string]int)
	provPos := make(map[string]int)

	for i, r := range results {
		for _, n := range r.notes {
			key := n.Provider + "\x00" + n.Model
			prev, ok := notes[key]
			if !ok || r.prio > prev.prio || (r.prio == prev.prio && i < prev.pos) {
				notes[key] = &noteEntry{note: n, prio: r.prio, pos: i}
			}
		}
		for _, sp := range r.provs {
			prevPrio := provPrio[sp.Name]
			if _, ok := provs[sp.Name]; !ok || r.prio > prevPrio || (r.prio == prevPrio && i < provPos[sp.Name]) {
				provs[sp.Name] = sp
				provPrio[sp.Name] = r.prio
				provPos[sp.Name] = i
			}
		}
	}

	snap := &Snapshot{
		Sources:   make([]SourceStatus, 0, len(results)),
		Notes:     make([]ModelNote, 0, len(notes)),
		Providers: make([]ProviderSpec, 0, len(provs)),
	}
	for _, r := range results {
		snap.Sources = append(snap.Sources, r.status)
	}
	for _, e := range notes {
		snap.Notes = append(snap.Notes, e.note)
	}
	for _, sp := range provs {
		snap.Providers = append(snap.Providers, sp)
	}

	sort.Slice(snap.Notes, func(i, j int) bool {
		if snap.Notes[i].Provider != snap.Notes[j].Provider {
			return snap.Notes[i].Provider < snap.Notes[j].Provider
		}
		return snap.Notes[i].Model < snap.Notes[j].Model
	})
	sort.Slice(snap.Providers, func(i, j int) bool { return snap.Providers[i].Name < snap.Providers[j].Name })
	return snap
}

// specPriority maps a spec's source tag back to a priority tier.
func specPriority(source string) int {
	switch source {
	case SourceLocal:
		return PriorityLocal
	case SourceEmbedded:
		return PriorityEmbedded
	default:
		return PriorityRemote
	}
}

// loadCacheLocked reads cacheFile (caller holds c.mu).
func (c *Catalog) loadCacheLocked() (*Snapshot, bool) {
	if c.cacheFile == "" {
		return nil, false
	}
	data, err := os.ReadFile(c.cacheFile)
	if err != nil {
		return nil, false
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false
	}
	if s.FetchedAt.IsZero() && len(s.Notes) == 0 {
		return nil, false
	}
	return &s, true
}

// saveCacheLocked writes cacheFile (caller holds c.mu).
func (c *Catalog) saveCacheLocked(s *Snapshot) {
	if c.cacheFile == "" {
		return
	}
	if dir := filepath.Dir(c.cacheFile); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	s.Stale = false
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := c.cacheFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.cacheFile)
}
