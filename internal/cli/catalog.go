package cli

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/silverx/szmelc-router/internal/catalog"
	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/core"
)

// resolveCatalog builds the merged catalog from config and resolves it once
// (honouring the catalog.json cache TTL). Errors never hard-fail the CLI —
// the snapshot may come from cache or builtin data.
func resolveCatalog(ctx context.Context, cfg *config.Config) *core.Snapshot {
	cat := catalog.Assemble(cfg)
	snap, err := cat.Resolve(ctx)
	if err != nil || snap == nil {
		return &core.Snapshot{Notes: []core.ModelNote{}}
	}
	return snap
}

// mergedProviderNote returns the best available free-tier note for a
// provider, applying STEP 6 precedence (local config > remote > builtin).
// Falls back to the config entry's own notes field.
func mergedProviderNote(snap *core.Snapshot, provider string, fallback string) string {
	if snap == nil {
		return fallback
	}
	if pnote, _ := snap.NotesForProvider(provider); pnote != "" {
		return pnote
	}
	return fallback
}

// liveProviderModels fetches the live model list for providers by
// instantiating them (bounded concurrency, short timeout). Returns
// provider name → sorted model IDs, plus per-provider error notes.
func liveProviderModels(ctx context.Context, cfg *config.Config) (map[string][]string, map[string]string) {
	models := make(map[string][]string)
	errs := make(map[string]string)
	if len(cfg.Providers) == 0 {
		return models, errs
	}
	r, err := buildRouter(cfg, 10*time.Second)
	if err != nil {
		return models, errs
	}
	coreRouter := r.Core()

	type result struct {
		name string
		ids  []string
		err  string
	}
	results := make(chan result, len(cfg.Providers))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, p := range cfg.Providers {
		provInst, ok := coreRouter.Provider(p.Name)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(name string, prov core.Provider) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			lctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			ms, err := prov.Models(lctx)
			res := result{name: name}
			if err != nil {
				res.err = err.Error()
			} else {
				seen := map[string]bool{}
				for _, m := range ms {
					if m.ID != "" && !seen[m.ID] {
						seen[m.ID] = true
						res.ids = append(res.ids, m.ID)
					}
				}
				sort.Strings(res.ids)
			}
			select {
			case results <- res:
			case <-ctx.Done():
			}
		}(p.Name, provInst)
	}
	wg.Wait()
	close(results)
	for res := range results {
		if res.err != "" {
			errs[res.name] = res.err
		} else {
			models[res.name] = res.ids
		}
	}
	return models, errs
}
