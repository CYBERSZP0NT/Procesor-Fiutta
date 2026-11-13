package catalog

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/silverx/szmelc-router/defaults"
	"github.com/silverx/szmelc-router/internal/appdir"
	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/core"
	"gopkg.in/yaml.v3"
)

// Registry of remote catalog feeds by config name.
var registry = map[string]*RemoteSource{
	"freellmapi":       FreellmapiSource(),
	"awesome-llm-apis": AwesomeSource(),
	"free-ai-models":   FreeAIModelsSource(),
}

// Get looks a remote feed up by config name.
func Get(name string) (*RemoteSource, bool) {
	s, ok := registry[name]
	return s, ok
}

// Names lists all remote feed names (sorted, stable).
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LocalSource is a CatalogSource over the *active* configuration (the
// merged defaults + user file). Highest precedence — local file > remote.
func LocalSource(providers []config.Provider) core.NamedSource {
	notes := make([]core.ModelNote, 0, len(providers))
	provs := make([]core.ProviderSpec, 0, len(providers))
	for _, p := range providers {
		if p.Notes != "" {
			notes = append(notes, core.ModelNote{
				Provider: p.Name,
				Tier:     core.TierInfo{Notes: p.Notes, Source: core.SourceLocal},
			})
		}
		provs = append(provs, core.ProviderSpec{
			Name:    p.Name,
			Impl:    firstNonEmpty(p.Impl, "openailike"),
			BaseURL: p.BaseURL,
			KeyEnv:  p.KeyEnv,
			Kind:    p.Kind,
			Weight:  p.Weight,
			Notes:   p.Notes,
			Source:  core.SourceLocal,
		})
	}
	return core.NamedSource{
		Name:     "providers",
		Priority: core.PriorityLocal,
		Fetch:    func(ctx context.Context) ([]core.ModelNote, error) { return notes, nil },
		ProviderList: func(ctx context.Context) ([]core.ProviderSpec, error) {
			return provs, nil
		},
	}
}

// EmbeddedSource is the builtin, offline-safe source: the embedded
// defaults/providers.yaml data file (not Go code).
func EmbeddedSource() core.NamedSource {
	type row struct {
		Name   string `yaml:"name"`
		Impl   string `yaml:"impl"`
		Base   string `yaml:"base_url"`
		Kind   string `yaml:"kind"`
		Weight int    `yaml:"weight"`
		Notes  string `yaml:"notes"`
	}
	type doc struct {
		Providers []row `yaml:"providers"`
	}
	var d doc
	_ = yaml.Unmarshal(defaults.ProvidersYAML, &d)

	notes := make([]core.ModelNote, 0, len(d.Providers))
	provs := make([]core.ProviderSpec, 0, len(d.Providers))
	for _, p := range d.Providers {
		if p.Notes != "" {
			notes = append(notes, core.ModelNote{
				Provider: p.Name,
				Tier:     core.TierInfo{Notes: p.Notes, Source: core.SourceEmbedded},
			})
		}
		provs = append(provs, core.ProviderSpec{
			Name:    p.Name,
			Impl:    firstNonEmpty(p.Impl, "openailike"),
			BaseURL: p.Base,
			Kind:    p.Kind,
			Weight:  p.Weight,
			Notes:   p.Notes,
			Source:  core.SourceEmbedded,
		})
	}
	return core.NamedSource{
		Name:     "embedded",
		Priority: core.PriorityEmbedded,
		Fetch:    func(ctx context.Context) ([]core.ModelNote, error) { return notes, nil },
		ProviderList: func(ctx context.Context) ([]core.ProviderSpec, error) {
			return provs, nil
		},
	}
}

// DefaultCacheFile returns the catalog.json cache location (~/.cache or the
// platform user cache dir). $FREELLM_CATALOG_CACHE overrides it.
func DefaultCacheFile() string {
	if v := os.Getenv("FREELLM_CATALOG_CACHE"); v != "" {
		return v
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return appdir.File(dir, "catalog.json")
	}
	home, _ := os.UserHomeDir()
	return appdir.File(filepath.Join(home, ".cache"), "catalog.json")
}

// Assemble builds the full core.Catalog from config.Catalog:
//
//	sources:  "providers" (active config) | "embedded" | any registry name
//	ttl:      catalog.refresh_s (default 3600s)
//	cache:    catalog.cache_file (default ~/.cache/szmelc/catalog.json)
//
// Unknown source names are skipped (tolerant of absence — a typo in config
// must never take the router down).
func Assemble(cfg *config.Config) *core.Catalog {
	refresh := time.Duration(cfg.Catalog.RefreshSec) * time.Second
	if refresh <= 0 {
		refresh = time.Hour
	}
	cacheFile := cfg.Catalog.CacheFile
	if cacheFile == "" {
		cacheFile = DefaultCacheFile()
	}

	var sources []core.NamedSource
	for _, name := range cfg.Catalog.Sources {
		switch {
		case name == "providers":
			sources = append(sources, LocalSource(cfg.Providers))
		case name == "embedded":
			sources = append(sources, EmbeddedSource())
		default:
			rs, ok := Get(name)
			if !ok {
				continue
			}
			if u := cfg.Catalog.SourceURLs[name]; u != "" {
				rs = rs.WithURL(u)
			}
			sources = append(sources, rs.Named(core.PriorityRemote))
		}
	}
	return core.NewCatalog(refresh, 15*time.Second, cacheFile, sources...)
}
