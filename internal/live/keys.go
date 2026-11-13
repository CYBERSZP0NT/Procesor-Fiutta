// Package live wires a FreeLLMAPI key into the router. A working key
// switches the default task-router mode to "live": routing by richness,
// fed by the signed live catalog of freellmapi.co.
//
// Two different credentials go by "the freellmapi key", told apart by
// prefix and never sent to the other's host:
//
//	fla_…        Premium license ("live" plan). Checked against the catalog
//	             service (GET /v1/license/check), then used as the Bearer
//	             for the signed live catalog (GET /v1/latest).
//	freellmapi-… unified key of a FreeLLMAPI server you run yourself (the
//	             sidecar). Checked with GET $FREELLMAPI_URL/v1/models.
package live

import (
	"encoding/json"
	"os"
	"strings"
)

// Kind is the kind of a FreeLLMAPI credential.
type Kind string

const (
	// KindLicense is a Premium license key (fla_…), the "live" plan.
	KindLicense Kind = "license"
	// KindUnified is a sidecar's unified API key (freellmapi-…).
	KindUnified Kind = "unified"
	// KindUnknown has no known prefix. It is treated as a license only
	// when it comes from a variable named for one.
	KindUnknown Kind = "unknown"
)

// Prefixes of the two credential kinds.
const (
	LicensePrefix = "fla_"
	UnifiedPrefix = "freellmapi-"
)

// Classify returns the kind of v by its prefix.
func Classify(v string) Kind {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, LicensePrefix):
		return KindLicense
	case strings.HasPrefix(v, UnifiedPrefix):
		return KindUnified
	default:
		return KindUnknown
	}
}

// IsFreellmapiKey reports whether v is shaped like a FreeLLMAPI credential
// (license or unified key). Such a value in FREELLM_API_KEY is not the
// router's own server key.
func IsFreellmapiKey(v string) bool { return Classify(v) != KindUnknown }

// Key is one discovered credential.
type Key struct {
	Value string `json:"-"`
	Kind  Kind   `json:"kind"`
	// From names where it was found: an env var, "config", or
	// "FREEAPI_CONFIG_JSON.license" and the like.
	From string `json:"from"`
}

// Masked returns the key safe to print.
func (k Key) Masked() string { return Mask(k.Value) }

// Mask shortens a secret to its prefix and last four characters.
func Mask(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 12 {
		return strings.Repeat("•", len(v))
	}
	head := 4
	if strings.HasPrefix(v, UnifiedPrefix) {
		head = len(UnifiedPrefix)
	}
	return v[:head] + "…" + v[len(v)-4:]
}

// licenseVars may hold a license key; a value without a known prefix is
// still tried as one. FREELLM_API_KEY is the name the router documents.
var licenseVars = []string{"FREELLM_API_KEY", "FREELLMAPI_LICENSE_KEY", "FREELLMAPI_PREMIUM_KEY", "FREELLMAPI_LICENSE"}

// unifiedVars hold a sidecar key; a value without a known prefix is
// taken as a unified key (the sidecar may use a custom one).
var unifiedVars = []string{"FREELLMAPI_API_KEY", "FREELLMAPI_KEY"}

// Discover finds FreeLLMAPI credentials in the environment, the
// configured key (configKey, may be empty) and FreeLLMAPI's own
// declarative config (FREEAPI_CONFIG_JSON / FREEAPI_CONFIG_PATH "license").
// Values listed in ownKeys (the router's own server keys) are skipped, so
// a router bearer kept in FREELLM_API_KEY never leaves the machine.
// Duplicates are dropped; licenses come first.
func Discover(getenv func(string) string, configKey string, ownKeys []string) []Key {
	if getenv == nil {
		getenv = os.Getenv
	}
	own := map[string]bool{}
	for _, k := range ownKeys {
		own[strings.TrimSpace(k)] = true
	}
	seen := map[string]bool{}
	var lic, uni []Key
	add := func(v, from string, unknownAs Kind) {
		v = strings.TrimSpace(v)
		if v == "" || own[v] || seen[v] {
			return
		}
		k := Key{Value: v, Kind: Classify(v), From: from}
		if k.Kind == KindUnknown {
			if unknownAs == "" {
				return
			}
			k.Kind = unknownAs
		}
		seen[v] = true
		if k.Kind == KindLicense {
			lic = append(lic, k)
		} else {
			uni = append(uni, k)
		}
	}
	add(configKey, "config", KindLicense)
	for _, n := range licenseVars {
		add(getenv(n), n, KindLicense)
	}
	for _, n := range unifiedVars {
		add(getenv(n), n, KindUnified)
	}
	if v := declarativeLicense(getenv); v != "" {
		add(v, "FREEAPI_CONFIG.license", KindLicense)
	}
	return append(lic, uni...)
}

// declarativeLicense reads "license" from FreeLLMAPI's declarative startup
// config, so a key already set up for the sidecar is found too.
func declarativeLicense(getenv func(string) string) string {
	raw := []byte(getenv("FREEAPI_CONFIG_JSON"))
	if len(raw) == 0 {
		if p := getenv("FREEAPI_CONFIG_PATH"); p != "" {
			raw, _ = os.ReadFile(p)
		}
	}
	if len(raw) == 0 {
		return ""
	}
	var c struct {
		License string `json:"license"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	return c.License
}
