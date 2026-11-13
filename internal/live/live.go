package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/agentic"
	"github.com/CYBERSZP0NT/szmelc-router/internal/appdir"
)

// Defaults for Options.
const (
	DefaultTimeout    = 4 * time.Second
	DefaultLicenseTTL = 6 * time.Hour
	DefaultCatalogTTL = 12 * time.Hour // FreeLLMAPI itself polls twice a day
	DefaultGrace      = 7 * 24 * time.Hour
	DefaultMaxModels  = 300
)

// Options configure Resolve. Zero values take the defaults.
type Options struct {
	// Getenv reads the environment (default os.Getenv).
	Getenv func(string) string
	// ConfigKey is the key from the config file (live.key), may be empty.
	ConfigKey string
	// OwnKeys are the router's own server keys; never sent anywhere.
	OwnKeys []string
	// BaseURL / PubKey override the catalog service and its signing key
	// (self-hosted catalogs; tests).
	BaseURL string
	PubKey  string
	// SidecarURL is the FreeLLMAPI server a unified key is checked against.
	SidecarURL string
	// CacheDir holds live.json and live-catalog.json.
	CacheDir string
	// Force re-checks the license now (live check); ForceCatalog also
	// re-downloads the catalog (live sync).
	Force        bool
	ForceCatalog bool
	// Offline never touches the network: the cached verdict or nothing.
	Offline bool

	Timeout    time.Duration
	LicenseTTL time.Duration
	CatalogTTL time.Duration
	// Grace is how long a cached "valid" verdict survives while the
	// license service is unreachable. An outage never flips the mode.
	Grace time.Duration
	HTTP  *http.Client
	Now   func() time.Time
}

func (o *Options) defaults() {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.LicenseTTL <= 0 {
		o.LicenseTTL = DefaultLicenseTTL
	}
	if o.CatalogTTL <= 0 {
		o.CatalogTTL = DefaultCatalogTTL
	}
	if o.Grace <= 0 {
		o.Grace = DefaultGrace
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheDir == "" {
		o.CacheDir = DefaultCacheDir(o.Getenv)
	}
	if o.SidecarURL == "" {
		o.SidecarURL = firstNonEmpty(o.Getenv("FREELLMAPI_URL"), DefaultSidecarURL)
	}
}

// DefaultCacheDir is $FREELLM_LIVE_CACHE, else the user cache dir
// (~/.cache/szmelc, or ~/.cache/freellm on pre-rebrand installs).
func DefaultCacheDir(getenv func(string) string) string {
	if getenv != nil {
		if v := getenv("FREELLM_LIVE_CACHE"); v != "" {
			return v
		}
	}
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Dir(appdir.File(dir, "catalog.json"))
}

// KeyInfo is a discovered key as it is shown (masked).
type KeyInfo struct {
	Kind Kind   `json:"kind"`
	From string `json:"from"`
	Key  string `json:"key"`
}

// CatalogInfo summarizes the live catalog in use.
type CatalogInfo struct {
	Version     string    `json:"version"`
	Tier        string    `json:"tier"`
	GeneratedAt string    `json:"generated_at,omitempty"`
	Models      int       `json:"models"`
	FetchedAt   time.Time `json:"fetched_at"`
	Source      string    `json:"source"` // network | cache
}

// SidecarInfo is a working FreeLLMAPI sidecar.
type SidecarInfo struct {
	URL    string `json:"url"`
	From   string `json:"from"`
	Key    string `json:"key"`
	Models int    `json:"models"`
}

// State is the outcome of Resolve: is live on, why, and what feeds it.
type State struct {
	// Active is true when a key works (license or sidecar).
	Active bool `json:"active"`
	// Kind of the key that made it active.
	Kind Kind   `json:"kind,omitempty"`
	Key  string `json:"key,omitempty"` // masked
	From string `json:"from,omitempty"`
	// Reason is one line on why live is (not) active.
	Reason   string       `json:"reason"`
	License  *License     `json:"license,omitempty"`
	Source   string       `json:"license_source,omitempty"` // network | cache
	Catalog  *CatalogInfo `json:"catalog,omitempty"`
	Sidecar  *SidecarInfo `json:"sidecar,omitempty"`
	Keys     []KeyInfo    `json:"keys"`
	Warnings []string     `json:"warnings,omitempty"`
	// Entries is how many live catalog entries the task router got
	// (filled by the caller after filtering).
	Entries int `json:"entries,omitempty"`
	// DefaultMode is true when live became the default task mode.
	DefaultMode bool `json:"default_mode,omitempty"`
}

// Result is State plus the data behind it.
type Result struct {
	State
	Data          *Catalog // the verified live catalog (nil: none yet)
	SidecarModels []string
}

// cacheFile is live.json.
type cacheFile struct {
	KeyHash          string    `json:"key_hash"`
	License          *License  `json:"license,omitempty"`
	CatalogSig       string    `json:"catalog_sig,omitempty"`
	CatalogVersion   string    `json:"catalog_version,omitempty"`
	CatalogFetchedAt time.Time `json:"catalog_fetched_at,omitempty"`
}

const (
	stateName   = "live.json"
	catalogName = "live-catalog.json"
)

func hashKey(k string) string {
	s := sha256.Sum256([]byte("szmelc-live:" + k))
	return hex.EncodeToString(s[:])
}

func loadCache(dir string) cacheFile {
	var c cacheFile
	b, err := os.ReadFile(filepath.Join(dir, stateName))
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func saveCache(dir string, c cacheFile, catalog []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if catalog != nil {
		if err := writeFile(filepath.Join(dir, catalogName), catalog); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, stateName), b)
}

// writeFile writes atomically with mode 0600 (the cache is bound to a
// secret's hash; keep it private like the key itself).
func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Resolve discovers FreeLLMAPI keys and checks them. Without a key it
// returns at once and touches neither the network nor the disk. A license
// is checked at most once per LicenseTTL and the catalog fetched at most
// once per CatalogTTL; an unreachable service falls back to the cache.
func Resolve(ctx context.Context, o Options) Result {
	o.defaults()
	keys := Discover(o.Getenv, o.ConfigKey, o.OwnKeys)
	var r Result
	for _, k := range keys {
		r.Keys = append(r.Keys, KeyInfo{Kind: k.Kind, From: k.From, Key: k.Masked()})
	}
	if len(keys) == 0 {
		r.Reason = "no FreeLLMAPI key found (set FREELLM_API_KEY)"
		return r
	}
	cl := &Client{BaseURL: o.BaseURL, PubKey: o.PubKey, HTTP: o.HTTP}

	var licReasons []string
	for _, k := range keys {
		if k.Kind != KindLicense {
			continue
		}
		ok, why := resolveLicense(ctx, cl, k, &o, &r)
		if ok {
			break
		}
		licReasons = append(licReasons, why)
	}
	for _, k := range keys {
		if k.Kind != KindUnified || o.Offline {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, min(o.Timeout, 2*time.Second))
		ids, err := cl.ProbeSidecar(cctx, o.SidecarURL, k.Value)
		cancel()
		if err != nil {
			licReasons = append(licReasons, fmt.Sprintf("sidecar key %s (%s): %v", k.Masked(), k.From, err))
			continue
		}
		r.Sidecar = &SidecarInfo{URL: o.SidecarURL, From: k.From, Key: k.Masked(), Models: len(ids)}
		r.SidecarModels = ids
		if !r.Active {
			r.Active, r.Kind, r.Key, r.From = true, KindUnified, k.Masked(), k.From
		}
		break
	}

	switch {
	case r.Active && r.Kind == KindLicense:
		r.Reason = "license " + r.Key + " active"
		if r.License != nil && r.License.Plan != "" {
			r.Reason += " (" + r.License.Plan + ")"
		}
		if c := r.State.Catalog; c != nil {
			r.Reason += fmt.Sprintf(", live catalog v%s, %d models", c.Version, c.Models)
		}
		if r.Sidecar != nil {
			r.Reason += ", sidecar " + r.Sidecar.URL + " up"
		}
	case r.Active:
		r.Reason = fmt.Sprintf("sidecar %s answers with key %s (%d models)", r.Sidecar.URL, r.Key, r.Sidecar.Models)
	default:
		r.Reason = strings.Join(licReasons, "; ")
		if r.Reason == "" {
			r.Reason = "no working FreeLLMAPI key"
		}
	}
	return r
}

// resolveLicense checks one license key and loads its live catalog.
func resolveLicense(ctx context.Context, cl *Client, k Key, o *Options, r *Result) (bool, string) {
	now := o.Now()
	c := loadCache(o.CacheDir)
	match := c.KeyHash == hashKey(k.Value)
	if !match {
		c = cacheFile{KeyHash: hashKey(k.Value)}
	}
	label := fmt.Sprintf("license %s (%s)", k.Masked(), k.From)

	lic, src := c.License, "cache"
	fresh := lic != nil && now.Sub(lic.CheckedAt) < o.LicenseTTL
	if !fresh || o.Force {
		if o.Offline {
			if lic == nil {
				return false, label + ": not checked yet (offline)"
			}
		} else {
			cctx, cancel := context.WithTimeout(ctx, o.Timeout)
			l, err := cl.CheckLicense(cctx, k.Value)
			cancel()
			switch {
			case err == nil:
				lic, src = &l, "network"
				c.License = lic
			case errors.Is(err, ErrUnreachable) && lic != nil && lic.Valid && now.Sub(lic.CheckedAt) < o.Grace:
				r.Warnings = append(r.Warnings, "license service unreachable, keeping the verdict from "+lic.CheckedAt.Format(time.RFC3339))
			default:
				return false, fmt.Sprintf("%s: %v", label, err)
			}
		}
	}
	if !lic.Valid {
		_ = saveCache(o.CacheDir, c, nil)
		return false, label + ": not active (" + firstNonEmpty(lic.Reason, lic.Status, "invalid") + ")"
	}

	cat, info, fetched, warn := loadCatalog(ctx, cl, k, o, &c, match)
	if warn != "" {
		r.Warnings = append(r.Warnings, warn)
	}
	if cat != nil && cat.Tier != "live" {
		// Entitlement is enforced at /v1/latest: a key that only gets the
		// monthly snapshot is not live, whatever the check said.
		_ = saveCache(o.CacheDir, c, fetched)
		return false, fmt.Sprintf("%s: the catalog service serves the %q tier for it, not live", label, cat.Tier)
	}
	_ = saveCache(o.CacheDir, c, fetched)

	r.Active, r.Kind, r.Key, r.From = true, KindLicense, k.Masked(), k.From
	r.License, r.Source = lic, src
	r.Data, r.State.Catalog = cat, info
	return true, ""
}

// loadCatalog returns the live catalog: the verified cache while fresh,
// else a fetch (304 keeps the cache). fetched is the new raw body to
// persist (nil when nothing new arrived).
func loadCatalog(ctx context.Context, cl *Client, k Key, o *Options, c *cacheFile, match bool) (*Catalog, *CatalogInfo, []byte, string) {
	now := o.Now()
	var cached *Catalog
	if match && c.CatalogSig != "" {
		if b, err := os.ReadFile(filepath.Join(o.CacheDir, catalogName)); err == nil {
			if cat, err := cl.Verify(b, c.CatalogSig); err == nil {
				cached = cat
			}
		}
	}
	info := func(cat *Catalog, src string) *CatalogInfo {
		return &CatalogInfo{Version: cat.Version, Tier: cat.Tier, GeneratedAt: cat.GeneratedAt,
			Models: len(cat.Models), FetchedAt: c.CatalogFetchedAt, Source: src}
	}
	if cached != nil && (o.Offline || (!o.ForceCatalog && now.Sub(c.CatalogFetchedAt) < o.CatalogTTL)) {
		return cached, info(cached, "cache"), nil, ""
	}
	if o.Offline {
		return nil, nil, nil, "no cached live catalog (offline)"
	}
	since := ""
	if cached != nil && !o.ForceCatalog {
		since = cached.Version
	}
	cctx, cancel := context.WithTimeout(ctx, max(o.Timeout, 10*time.Second))
	cat, body, sig, err := cl.FetchCatalog(cctx, k.Value, since)
	cancel()
	switch {
	case errors.Is(err, ErrNotModified) && cached != nil:
		c.CatalogFetchedAt = now
		return cached, info(cached, "cache"), nil, ""
	case err != nil && cached != nil:
		return cached, info(cached, "cache"), nil, "live catalog refresh failed, using the cached v" + cached.Version + ": " + err.Error()
	case err != nil:
		return nil, nil, nil, "no live catalog yet (" + err.Error() + "); live mode ranks the built-in catalog"
	}
	c.CatalogSig, c.CatalogVersion, c.CatalogFetchedAt = sig, cat.Version, now
	return cat, info(cat, "network"), body, ""
}

// PlatformProviders maps FreeLLMAPI platform ids to szmelc-router provider
// names where they differ (checked against both registries). Every other
// platform keeps its name; unknown ones are dropped by the router.
var PlatformProviders = map[string]string{
	"github":      "github_models",
	"zhipu":       "zai",
	"huggingface": "hf_router",
	"opencode":    "opencode_zen",
	"aion":        "aionlabs",
	"ollama":      "ollama_cloud", // FreeLLMAPI's "ollama" is Ollama Cloud, not a local daemon
}

// ProviderFor returns the szmelc-router provider for a platform.
func ProviderFor(platform string) string {
	p := strings.ToLower(strings.TrimSpace(platform))
	if m, ok := PlatformProviders[p]; ok {
		return m
	}
	return p
}

// SizeTier maps the catalog's cross-provider size class to a task tier.
func SizeTier(label string) int {
	switch strings.ToLower(label) {
	case "frontier":
		return 5
	case "large":
		return 4
	case "medium":
		return 3
	default:
		return 2
	}
}

func speedOf(rank int) int {
	switch {
	case rank <= 0:
		return 2
	case rank <= 3:
		return 3
	case rank <= 7:
		return 2
	default:
		return 1
	}
}

// DailyTokens is a model's free capacity per day: tpd, else the monthly
// budget / 30. 0 = unknown (never guessed).
func DailyTokens(m Model) int64 {
	if m.Limits.TPD > 0 {
		return int64(m.Limits.TPD)
	}
	if n := parseBudget(m.MonthlyTokenBudget); n > 0 {
		return n / 30
	}
	return 0
}

// parseBudget reads "1M", "500K", "2.5B", "1000000"; anything else ("2x",
// "unlimited") is unknown.
func parseBudget(s string) int64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'K':
		mult, s = 1e3, s[:len(s)-1]
	case 'M':
		mult, s = 1e6, s[:len(s)-1]
	case 'B':
		mult, s = 1e9, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int64(f * mult)
}

// Entries turns the live catalog (and a working sidecar) into task-router
// catalog entries, richest first, at most max (0 = DefaultMaxModels).
// known, when non-nil, keeps only platforms whose provider is configured.
// skip lists "provider/model" labels the static catalog already has.
func Entries(cat *Catalog, sidecarModels []string, known map[string]bool, skip map[string]bool, max int) []agentic.Entry {
	if max <= 0 {
		max = DefaultMaxModels
	}
	var out []agentic.Entry
	seen := map[string]bool{}
	if cat != nil {
		for _, m := range cat.Models {
			if !m.Enabled || m.ModelID == "" || (m.Modality != "" && m.Modality != "text") {
				continue
			}
			prov := ProviderFor(m.Platform)
			if known != nil && !known[prov] {
				continue
			}
			label := prov + "/" + m.ModelID
			if skip[label] || seen[label] {
				continue
			}
			seen[label] = true
			note := firstNonEmpty(m.DisplayName, m.ModelID) + " · " + firstNonEmpty(m.SizeLabel, "?") + " · live v" + cat.Version
			out = append(out, agentic.Entry{
				ID:      "live:" + m.Platform + "/" + m.ModelID,
				Model:   label,
				Tier:    SizeTier(m.SizeLabel),
				Cost:    1,
				Speed:   speedOf(m.SpeedRank),
				Context: m.ContextWindow,
				Tools:   m.SupportsTools,
				Rank:    m.IntelligenceRank,
				Quota:   DailyTokens(m),
				Note:    note,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return agentic.Richer(out[i], out[j]) })
	if len(out) > max {
		out = out[:max]
	}
	if len(sidecarModels) > 0 && (known == nil || known["freellmapi"]) {
		// The sidecar is a whole router over its own keys and the same
		// catalog: its smartest chain sits with the frontier tier, its
		// default chain one below.
		out = append(out,
			agentic.Entry{ID: "live:sidecar/auto:smart", Model: "freellmapi/auto:smart", Tier: 5, Cost: 1, Speed: 2, Tools: true, Note: "FreeLLMAPI sidecar, smartest strategy"},
			agentic.Entry{ID: "live:sidecar/auto", Model: "freellmapi/auto", Tier: 4, Cost: 1, Speed: 2, Tools: true, Note: "FreeLLMAPI sidecar, balanced strategy"},
		)
	}
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
