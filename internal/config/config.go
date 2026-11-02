// Package config implements the 3-layer configuration loader for
// freellm-router:
//
//  1. embedded defaults (defaults/*.yaml via go:embed)
//  2. user config file (~/.config/freellm/config.yaml or $FREELLM_CONFIG)
//  3. environment-variable overlay (FREELLM_PORT, FREELLM_HOST, …)
//
// The final step is ${VAR:-default} expansion over all string fields.
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/silverx/freellm-router/defaults"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Structs (mirror the YAML schema in AGENTS.md)
// ---------------------------------------------------------------------------

// Config is the full, resolved configuration.
type Config struct {
	Routes    map[string][]string `yaml:"routes"`
	Providers []Provider          `yaml:"providers"`
	Server    ServerConfig        `yaml:"server"`
	Policies  PolicyConfig        `yaml:"policies"`
	Catalog   CatalogConfig       `yaml:"catalog"`
	Logging   LoggingConfig       `yaml:"logging"`
	HTTP      HTTPConfig          `yaml:"http,omitempty"`
	ACL       map[string][]string `yaml:"routes_acl,omitempty"` // route → allowed key names/values
}

// Provider is one routable inference source entry.
type Provider struct {
	Name    string `yaml:"name"`
	Impl    string `yaml:"impl"`
	BaseURL string `yaml:"base_url,omitempty"`
	KeyEnv  string `yaml:"key_env,omitempty"`
	Kind    string `yaml:"kind"`
	Weight  int    `yaml:"weight,omitempty"`
	Header  string `yaml:"header,omitempty"`
	Notes   string `yaml:"notes,omitempty"`
	// External-plugin fields (used when Kind==plugin)
	Command               []string          `yaml:"command,omitempty"`
	Env                   map[string]string `yaml:"env,omitempty"`
	AllowInsecureUpstream bool              `yaml:"allow_insecure_upstream,omitempty"`
	MaxParallel           int               `yaml:"max_parallel,omitempty"`
	// Plugin lifecycle (STEP 5)
	RestartPolicy    string `yaml:"restart_policy,omitempty"`    // "never" | "on-crash" (default "on-crash")
	HandshakeTimeout int    `yaml:"handshake_timeout,omitempty"` // seconds (default 10)
	RestartMax       int    `yaml:"restart_max,omitempty"`       // max restarts before giving up (0 = infinite)
}

// ServerConfig holds HTTP server / auth settings.
type ServerConfig struct {
	Host         string     `yaml:"host"`
	Port         int        `yaml:"port"`
	Auth         AuthConfig `yaml:"auth"`
	Once         bool       `yaml:"once,omitempty"`
	MaxBodyBytes int64      `yaml:"max_body_bytes,omitempty"`
	CertFile     string     `yaml:"cert,omitempty"`
	KeyFile      string     `yaml:"key,omitempty"`
	// Rate-limit (per key, token bucket). 0 = disabled.
	RateLimitPerKeyPerMin int `yaml:"rate_limit_per_key_per_min,omitempty"`
	// Usage JSONL file (empty = default under $XDG_DATA_HOME/freellm/usage.jsonl).
	UsageFile string `yaml:"usage_file,omitempty"`
	// Usage journal rotation: max bytes per file (0 = default 10 MiB) and
	// how many rotated files to keep (0 = default 3).
	UsageMaxBytes int64 `yaml:"usage_max_bytes,omitempty"`
	UsageKeep     int   `yaml:"usage_keep,omitempty"`
	// MiddlewareOff lists disabled default middlewares by name:
	// "logging" | "cors" | "auth" | "ratelimit" | "usage".
	MiddlewareOff []string `yaml:"middleware_off,omitempty"`
}

// AuthConfig controls HTTP auth mode.
type AuthConfig struct {
	Mode string   `yaml:"mode"` // "none" | "bearer"
	Keys []string `yaml:"keys,omitempty"`
	// Names optionally maps a human key name ("alice") to the key value.
	// ACLs (Config.ACL) may reference either the name or the raw value.
	Names map[string]string `yaml:"names,omitempty"`
}

// HTTPConfig tunes the shared outbound HTTP transport (STEP 7 concurrency).
type HTTPConfig struct {
	MaxIdleConns        int `yaml:"max_idle_conns,omitempty"`
	MaxIdleConnsPerHost int `yaml:"max_idle_conns_per_host,omitempty"`
	MaxConnsPerHost     int `yaml:"max_conns_per_host,omitempty"`
}

// PolicyConfig holds router retry / cooldown tuning knobs.
type PolicyConfig struct {
	CooldownShortSec int  `yaml:"cooldown_short_s"`
	CooldownLongSec  int  `yaml:"cooldown_long_s"`
	RetrySame        bool `yaml:"retry_same"`
	MaxChainAttempts int  `yaml:"max_chain_attempts"`
	// Backoff optionally overrides the cooldown per status class
	// ("conn" | "429" | "408" | "4xx" | "auth" | "5xx"), with jitter.
	Backoff map[string]BackoffSpec `yaml:"backoff,omitempty"`
}

// BackoffSpec is one status class's cooldown tuning (STEP 7).
type BackoffSpec struct {
	BaseSec int     `yaml:"base_s,omitempty"` // base cooldown seconds (0 = class default)
	MaxSec  int     `yaml:"max_s,omitempty"`  // optional cap, seconds (0 = no cap)
	Jitter  float64 `yaml:"jitter,omitempty"` // 0..1 relative jitter
}

// CatalogConfig controls model discovery sources.
type CatalogConfig struct {
	Sources    []string `yaml:"sources,omitempty"`
	RefreshSec int      `yaml:"refresh_s,omitempty"`
	CacheFile  string   `yaml:"cache_file,omitempty"`
	// SourceURLs overrides a remote feed's endpoint per source name
	// (e.g. {"awesome-llm-apis": "https://my.mirror/apis.json"}).
	SourceURLs map[string]string `yaml:"source_urls,omitempty"`
}

// LoggingConfig controls output level/format.
type LoggingConfig struct {
	Level  string `yaml:"level"` // "debug" | "info" | "warn" | "error"
	Format string `yaml:"format"`
	File   string `yaml:"file,omitempty"`
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load resolves the 3-layer configuration and returns a fully expanded,
// validated *Config.
//
// Arguments:
//
//	– explicitPath: if non-empty, loaded as the user config file.
//	  Otherwise $FREELLM_CONFIG or ~/.config/freellm/config.yaml is tried.
func Load(explicitPath string) (*Config, error) {
	// Layer 1: embedded defaults
	base, err := loadDefaults()
	if err != nil {
		return nil, err
	}

	// Layer 2: user config file (optional)
	cfgPath := resolveConfigPath(explicitPath)
	if cfgPath != "" {
		user, err := loadUserFile(cfgPath)
		if err != nil {
			return nil, err
		}
		base = merge(base, user)
	}

	// Layer 3: env overlay
	base = applyEnv(base)

	// Expand ${VAR} and ${VAR:-default} in all string fields
	base = expandConfig(base)

	if err := base.Validate(); err != nil {
		return nil, err
	}
	return base, nil
}

// Defaults returns the embedded defaults (layer 1: providers + routes +
// built-in tuning) without touching the user config file or the env
// overlay. `config init` uses this so it works on a fresh machine where
// no config file exists yet.
func Defaults() (*Config, error) { return loadDefaults() }

func loadDefaults() (*Config, error) {
	var providersRaw struct {
		Providers []Provider `yaml:"providers"`
	}
	if err := yaml.Unmarshal(defaults.ProvidersYAML, &providersRaw); err != nil {
		return nil, fmt.Errorf("embedded providers.yaml: %w", err)
	}

	var routesRaw struct {
		Routes map[string][]string `yaml:"routes"`
	}
	if err := yaml.Unmarshal(defaults.RoutesYAML, &routesRaw); err != nil {
		return nil, fmt.Errorf("embedded routes.yaml: %w", err)
	}

	return &Config{
		Routes:    routesRaw.Routes,
		Providers: providersRaw.Providers,
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8787,
			Auth: AuthConfig{Mode: "none"},
		},
		Policies: PolicyConfig{
			CooldownShortSec: 30,
			CooldownLongSec:  3600,
			MaxChainAttempts: 6,
		},
		Catalog: CatalogConfig{
			Sources:    []string{"providers", "embedded"},
			RefreshSec: 3600,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		// Shared outbound transport pool (STEP 7 concurrency).
		HTTP: HTTPConfig{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 64,
			MaxConnsPerHost:     32,
		},
	}, nil
}

func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("FREELLM_CONFIG"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(home, ".config", "freellm", "config.yaml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func loadUserFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("user config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("user config (%s): %w", path, err)
	}
	return &c, nil
}

// applyEnv overlays high-value env vars on top of the layered config.
// FREELLM_PORT / FREELLM_HOST / FREELLM_LOG_LEVEL / FREELLM_LOG_FORMAT
func applyEnv(c *Config) *Config {
	if v := os.Getenv("FREELLM_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("FREELLM_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.Server.Port = port
		}
	}
	if v := os.Getenv("FREELLM_LOG_LEVEL"); v != "" {
		c.Logging.Level = strings.ToLower(v)
	}
	if v := os.Getenv("FREELLM_LOG_FORMAT"); v != "" {
		c.Logging.Format = strings.ToLower(v)
	}
	return c
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

// merge overlays user config on top of the base defaults.
//
//	– Routes:  per-name override (user route name win)
//	– Providers: per-name override (user provider entry wins; new appends)
//	– Other sections: full override when overlay field is non-zero
func merge(base, overlay *Config) *Config {
	out := *base

	// Routes: overlay wins per-name
	if len(overlay.Routes) > 0 {
		out.Routes = make(map[string][]string, len(base.Routes)+len(overlay.Routes))
		for name, chain := range base.Routes {
			out.Routes[name] = chain
		}
		for name, chain := range overlay.Routes {
			out.Routes[name] = chain
		}
	}

	// Providers: overlay wins per-name; overlay duplicates pass through to Validate.
	if len(overlay.Providers) > 0 {
		// Group overlay entries by (lowercase) name, preserving order.
		ovBy := make(map[string][]Provider)
		var ovOrder []string
		for _, p := range overlay.Providers {
			key := strings.ToLower(p.Name)
			if _, exists := ovBy[key]; !exists {
				ovOrder = append(ovOrder, key)
			}
			ovBy[key] = append(ovBy[key], p)
		}
		baseNames := make(map[string]bool, len(base.Providers))
		for _, p := range base.Providers {
			baseNames[strings.ToLower(p.Name)] = true
		}
		merged := make([]Provider, 0, len(base.Providers)+len(overlay.Providers))
		// Step 1: emit base providers; replace with LAST overlay entry when name matches.
		for _, p := range base.Providers {
			key := strings.ToLower(p.Name)
			if ovList, ok := ovBy[key]; ok {
				merged = append(merged, ovList[len(ovList)-1])
			} else {
				merged = append(merged, p)
			}
		}
		// Step 2: emit overlay-only providers (names not in base).
		// Append ALL entries for that name (duplicates included → Validate flags them).
		for _, key := range ovOrder {
			if baseNames[key] {
				continue
			}
			merged = append(merged, ovBy[key]...)
		}
		out.Providers = merged
	}

	// Server
	if overlay.Server.Port != 0 {
		out.Server = overlay.Server
	}
	// Policies: overlay is active when ANY knob is non-zero (including the
	// STEP-7 backoff map or RetrySame being the only change).
	if polNonZero(overlay.Policies) {
		out.Policies = overlay.Policies
	}
	// Catalog
	if len(overlay.Catalog.Sources) > 0 {
		out.Catalog = overlay.Catalog
	}
	// Logging
	if overlay.Logging.Level != "" {
		out.Logging = overlay.Logging
	}
	// Shared HTTP transport tuning (any non-zero knob activates the overlay).
	if overlay.HTTP.MaxIdleConns > 0 || overlay.HTTP.MaxIdleConnsPerHost > 0 || overlay.HTTP.MaxConnsPerHost > 0 {
		out.HTTP = overlay.HTTP
	}
	// Per-route ACL (STEP 7)
	if len(overlay.ACL) > 0 {
		out.ACL = overlay.ACL
	}

	return &out
}

// polNonZero reports whether any PolicyConfig field is set (bools included,
// so a file that only sets retry_same still overlays).
func polNonZero(p PolicyConfig) bool {
	return p.CooldownShortSec > 0 || p.CooldownLongSec > 0 ||
		p.MaxChainAttempts > 0 || len(p.Backoff) > 0 || p.RetrySame
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

// errValidation collects all validation problems into one human-readable list.
type errValidation struct {
	Problems []string
}

func (e *errValidation) Error() string {
	return "config validation:\n  – " + strings.Join(e.Problems, "\n  – ")
}

// knownImpls is the set of accepted impl names (extensible at runtime).
var knownImpls = map[string]bool{
	"openailike": true,
	"ollama":     true,
	"g4fbridge":  true,
	"plugin":     true,
}

// knownKinds is the set of accepted kind values.
var knownKinds = map[string]bool{
	"keyless": true,
	"keyed":   true,
	"local":   true,
	"scraper": true,
	"sidecar": true,
	"plugin":  true,
}

// Validate checks the fully-expanded config for internal consistency and
// returns a human-readable error listing every problem found.
func (c *Config) Validate() error {
	var problems []string

	// Provider name uniqueness
	nameSeen := make(map[string]bool)
	for _, p := range c.Providers {
		key := strings.ToLower(p.Name)
		if nameSeen[key] {
			problems = append(problems, fmt.Sprintf("duplicate provider name: %q", p.Name))
		}
		nameSeen[key] = true

		// Name required
		if p.Name == "" {
			problems = append(problems, "a providers[] entry is missing the name field")
			continue
		}
		// Impl required + known
		if p.Impl == "" {
			problems = append(problems, fmt.Sprintf("provider %q: impl field is empty (expected: openailike|ollama|g4fbridge|plugin)", p.Name))
		} else if !knownImpls[strings.ToLower(p.Impl)] {
			problems = append(problems, fmt.Sprintf("provider %q: unknown impl %q (expected: openailike|ollama|g4fbridge|plugin)", p.Name, p.Impl))
		}
		// Kind required when not inferred
		if p.Kind == "" {
			problems = append(problems, fmt.Sprintf("provider %q: kind field is empty (expected: keyless|keyed|local|scraper|sidecar|plugin)", p.Name))
		} else if !knownKinds[strings.ToLower(p.Kind)] {
			problems = append(problems, fmt.Sprintf("provider %q: unknown kind %q (expected: keyless|keyed|local|scraper|sidecar|plugin)", p.Name, p.Kind))
		}
		// Base URL required for non-local/non-plugin
		if p.Impl != "plugin" && p.Impl != "ollama" && p.BaseURL == "" {
			if p.Kind != "local" && p.Kind != "scraper" {
				problems = append(problems, fmt.Sprintf("provider %q: base_url is empty (required for impl=%s)", p.Name, p.Impl))
			}
		}
		// Plugin must have command
		if p.Kind == "plugin" && len(p.Command) == 0 {
			problems = append(problems, fmt.Sprintf("provider %q: kind=plugin but command is empty", p.Name))
		}
		// Weight bounds
		if p.Weight < 0 || p.Weight > 10_000 {
			problems = append(problems, fmt.Sprintf("provider %q: weight %d outside 0..10000", p.Name, p.Weight))
		}

		// MaxParallel bounds
		if p.MaxParallel < 0 || p.MaxParallel > 10_000 {
			problems = append(problems, fmt.Sprintf("provider %q: max_parallel %d outside 0..10000", p.Name, p.MaxParallel))
		}

		// SSRF guard (STEP 7): a provider whose kind is not local (and not a
		// plugin, which has no HTTP base URL) must not point at a loopback
		// address unless it opts in via allow_insecure_upstream.
		kindLower := strings.ToLower(p.Kind)
		if p.BaseURL != "" && kindLower != "local" && kindLower != "plugin" {
			if host, herr := baseURLHost(p.BaseURL); herr == nil && isLoopbackHost(host) && !p.AllowInsecureUpstream {
				problems = append(problems, fmt.Sprintf(
					"provider %q: base_url %q points at a loopback address while kind=%s; set allow_insecure_upstream: true to allow it",
					p.Name, p.BaseURL, p.Kind))
			}
		}
	}

	// Routes: each entry must reference a known provider
	for name, chain := range c.Routes {
		if len(chain) == 0 {
			problems = append(problems, fmt.Sprintf("route %q: chain is empty", name))
			continue
		}
		for _, entry := range chain {
			slash := strings.Index(entry, "/")
			if slash < 1 {
				problems = append(problems, fmt.Sprintf("route %q: entry %q is not in provider/model form", name, entry))
				continue
			}
			pname := strings.ToLower(entry[:slash])
			if !nameSeen[pname] {
				problems = append(problems, fmt.Sprintf("route %q: references unknown provider %q (add it to providers first)", name, pname))
			}
		}
	}

	// Server port
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		problems = append(problems, fmt.Sprintf("server.port %d outside 1..65535", c.Server.Port))
	}

	// Server auth mode
	switch strings.ToLower(c.Server.Auth.Mode) {
	case "none", "bearer":
		// ok
	default:
		problems = append(problems, fmt.Sprintf("server.auth.mode %q invalid (expected: none|bearer)", c.Server.Auth.Mode))
	}

	// Logging level
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error", "":
		// ok
	default:
		problems = append(problems, fmt.Sprintf("logging.level %q invalid (expected: debug|info|warn|error)", c.Logging.Level))
	}

	// Policies
	if c.Policies.MaxChainAttempts < 1 || c.Policies.MaxChainAttempts > 200 {
		problems = append(problems, fmt.Sprintf("policies.max_chain_attempts %d outside 1..200", c.Policies.MaxChainAttempts))
	}

	// Backoff per status class (STEP 7)
	for class, spec := range c.Policies.Backoff {
		switch class {
		case "conn", "429", "408", "4xx", "auth", "5xx":
		default:
			problems = append(problems, fmt.Sprintf("policies.backoff: unknown status class %q (expected: conn|429|408|4xx|auth|5xx)", class))
		}
		if spec.BaseSec < 0 {
			problems = append(problems, fmt.Sprintf("policies.backoff.%s: base_s %d must be >= 0", class, spec.BaseSec))
		}
		if spec.MaxSec < 0 {
			problems = append(problems, fmt.Sprintf("policies.backoff.%s: max_s %d must be >= 0", class, spec.MaxSec))
		}
		if spec.Jitter < 0 || spec.Jitter > 1 {
			problems = append(problems, fmt.Sprintf("policies.backoff.%s: jitter %v outside 0..1", class, spec.Jitter))
		}
	}

	// Per-route ACL (STEP 7)
	for route, allow := range c.ACL {
		if route == "" {
			problems = append(problems, "routes_acl: empty route name")
		}
		if len(allow) == 0 {
			problems = append(problems, fmt.Sprintf("routes_acl[%q]: allow list is empty (use an empty routes_acl section to disable)", route))
		}
		for _, k := range allow {
			if strings.TrimSpace(k) == "" {
				problems = append(problems, fmt.Sprintf("routes_acl[%q]: empty key entry", route))
			}
		}
	}

	if c.Server.Auth.Mode == "bearer" {
		for name, value := range c.Server.Auth.Names {
			if name == "" || value == "" {
				problems = append(problems, "server.auth.names: name and value must both be non-empty")
			}
		}
	}

	if len(problems) > 0 {
		return &errValidation{Problems: problems}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// baseURLHost extracts a lowercased hostname from a base URL.
func baseURLHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("no host in %q", raw)
	}
	return host, nil
}

// isLoopbackHost reports whether host is a loopback address
// (localhost, 127.0.0.0/8, ::1) — used for the SSRF guard in Validate.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "localhost" || host == "ip6-localhost" || host == "::1" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback()
	}
	return false
}

// ProviderByName looks up a provider by (case-insensitive) name.
func (c *Config) ProviderByName(name string) (Provider, bool) {
	for _, p := range c.Providers {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Provider{}, false
}

// RouteChain returns the chain for a route name (empty if not found).
func (c *Config) RouteChain(name string) []string {
	return c.Routes[strings.ToLower(name)]
}

// RouteNames returns all route alias names, sorted.
func (c *Config) RouteNames() []string {
	out := make([]string, 0, len(c.Routes))
	for n := range c.Routes {
		out = append(out, n)
	}
	// simple insertion sort (small N)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// KeyEnvMap returns a map provider→keyEnv-spec for use with core.NewEnvKeySource.
func (c *Config) KeyEnvMap() map[string]string {
	m := make(map[string]string, len(c.Providers))
	for _, p := range c.Providers {
		if p.KeyEnv != "" {
			m[strings.ToLower(p.Name)] = p.KeyEnv
		}
	}
	return m
}
