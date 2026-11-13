package config

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/CYBERSZP0NT/szmelc-router/internal/agentic"
	"github.com/CYBERSZP0NT/szmelc-router/internal/live"
)

// LiveConfig configures FreeLLMAPI live (internal/live): with a working
// FreeLLMAPI key the task router defaults to mode "live" and routes by
// richness over the signed live catalog of freellmapi.co.
type LiveConfig struct {
	// Enabled is "auto" (default: check any FreeLLMAPI key found, go live
	// when one works), "offline" (cached verdict only, no network) or
	// "off". $FREELLM_LIVE overrides it.
	Enabled string `yaml:"enabled,omitempty"`
	// Key is a license (fla_…) or sidecar (freellmapi-…) key; ${VAR}
	// works. Empty: discovered from FREELLM_API_KEY and friends.
	Key string `yaml:"key,omitempty"`
	// CatalogURL overrides the catalog service (default
	// https://api.freellmapi.co, or $CATALOG_BASE_URL).
	CatalogURL string `yaml:"catalog_url,omitempty"`
	// PubKey overrides the pinned Ed25519 catalog key (PEM, or
	// $CATALOG_PUBKEY) for a self-hosted catalog.
	PubKey string `yaml:"pubkey,omitempty"`
	// MaxModels caps the live catalog entries given to the task router
	// (default 300, richest first).
	MaxModels int `yaml:"max_models,omitempty"`
	// KeepMode keeps the configured task mode even when live works (by
	// default a working key makes "live" the default unless agentic.mode
	// or FREELLM_TASK_MODE names a mode).
	KeepMode bool `yaml:"keep_mode,omitempty"`
}

func mergeLive(base, o LiveConfig) LiveConfig {
	out := base
	if o.Enabled != "" {
		out.Enabled = o.Enabled
	}
	if o.Key != "" {
		out.Key = o.Key
	}
	if o.CatalogURL != "" {
		out.CatalogURL = o.CatalogURL
	}
	if o.PubKey != "" {
		out.PubKey = o.PubKey
	}
	if o.MaxModels != 0 {
		out.MaxModels = o.MaxModels
	}
	if o.KeepMode {
		out.KeepMode = true
	}
	return out
}

// liveSwitch returns auto | offline | off.
func (c *Config) liveSwitch() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FREELLM_LIVE")))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(c.Live.Enabled))
	}
	switch v {
	case "off", "0", "false", "no", "disabled":
		return "off"
	case "offline", "cache":
		return "offline"
	default:
		return "auto"
	}
}

// LiveOptions returns the live.Options this config implies (also used by
// `szmelc-router live`).
func (c *Config) LiveOptions() live.Options {
	o := live.Options{
		ConfigKey: c.Live.Key,
		OwnKeys:   c.Server.Auth.Keys,
		BaseURL:   firstNonEmpty(c.Live.CatalogURL, os.Getenv("CATALOG_BASE_URL")),
		PubKey:    firstNonEmpty(c.Live.PubKey, os.Getenv("CATALOG_PUBKEY")),
		Offline:   c.liveSwitch() == "offline",
	}
	for _, p := range c.Providers {
		if strings.EqualFold(p.Name, "freellmapi") {
			o.SidecarURL = p.BaseURL
		}
	}
	return o
}

// applyLive checks for a working FreeLLMAPI key and, when there is one,
// adds the live catalog to the task router and makes "live" the default
// mode (unless explicitMode). It never fails Load: errors end up in
// LiveState.Reason. Under `go test` it stays off unless FREELLM_LIVE is
// set, so a developer's exported key never makes tests hit the network.
func applyLive(c *Config, explicitMode bool) {
	sw := c.liveSwitch()
	if sw == "off" {
		c.LiveState = &live.State{Reason: "disabled (live.enabled: off / FREELLM_LIVE=off)"}
		return
	}
	if testing.Testing() && os.Getenv("FREELLM_LIVE") == "" {
		return
	}
	res := live.Resolve(context.Background(), c.LiveOptions())
	c.ApplyLiveResult(res, explicitMode)
}

// ApplyLiveResult folds a resolved live result into c (exported for
// `szmelc-router live check`, which resolves with Force).
func (c *Config) ApplyLiveResult(res live.Result, explicitMode bool) {
	st := res.State
	c.LiveState = &st
	if !res.Active {
		slog.Debug("live: inactive", "reason", st.Reason)
		return
	}

	known := make(map[string]bool, len(c.Providers))
	for _, p := range c.Providers {
		known[strings.ToLower(p.Name)] = true
	}
	skip := map[string]bool{}
	if base, err := agentic.Default(); err == nil {
		for _, e := range base.Catalog {
			skip[e.Model] = true
		}
	}
	for _, e := range c.Agentic.Catalog {
		skip[e.Model] = true
	}
	entries := live.Entries(res.Data, res.SidecarModels, known, skip, c.Live.MaxModels)
	have := map[string]bool{}
	for _, e := range c.Agentic.Catalog {
		have[e.ID] = true
	}
	for _, e := range entries {
		if !have[e.ID] {
			c.Agentic.Catalog = append(c.Agentic.Catalog, e)
		}
	}
	c.LiveState.Entries = len(entries)

	// A sidecar key found under another name (FREELLM_API_KEY) still has
	// to reach the freellmapi provider.
	if sc := res.Sidecar; sc != nil && sc.From != "config" && !strings.HasPrefix(sc.From, "FREEAPI_CONFIG") {
		for i, p := range c.Providers {
			if strings.EqualFold(p.Name, "freellmapi") && !envListHas(p.KeyEnv, sc.From) {
				c.Providers[i].KeyEnv = strings.Trim(p.KeyEnv+"|"+sc.From, "|")
			}
		}
	}

	if !explicitMode && !c.Live.KeepMode {
		c.Agentic.Mode = agentic.ModeLive
		c.LiveState.DefaultMode = true
	}
	slog.Debug("live: active", "reason", st.Reason, "entries", len(entries), "default_mode", c.LiveState.DefaultMode)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func envListHas(spec, name string) bool {
	for _, p := range strings.Split(spec, "|") {
		if strings.TrimSpace(p) == name {
			return true
		}
	}
	return false
}
