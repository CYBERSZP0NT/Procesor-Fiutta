package catalog

import (
	"encoding/json"
	"strings"

	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
)

// DEFAULT_AWESOME_URL is the mnfst/awesome-free-llm-apis daily JSON, served
// raw through jsDelivr (no auth, CDN-cached — ideal for `discover`).
const DEFAULT_AWESOME_URL = "https://cdn.jsdelivr.net/gh/mnfst/awesome-free-llm-apis@main/data/apis.json"

// AwesomeSource implements the awesome-free-llm-apis adapter.
//
// The feed is a list of API entries; keys tolerated per entry:
//
//	name, url|base_url|endpoint, key|key_required|auth (bool|string),
//	models (list or "a, b, c"), free (bool)
//
// It is the primary *provider-enumerating* feed for `discover --apply`.
func AwesomeSource() *RemoteSource {
	return &RemoteSource{
		Name: "awesome-llm-apis",
		URL:  DEFAULT_AWESOME_URL,
		Parse: func(body []byte) ([]core.ModelNote, []core.ProviderSpec, error) {
			arr, perr := parseArray(body)
			if perr != nil {
				return nil, nil, perr
			}
			if arr == nil {
				return nil, nil, nil
			}
			var items []strMap
			if err := json.Unmarshal(arr, &items); err != nil {
				return nil, nil, err
			}

			var notes []core.ModelNote
			provs := make([]core.ProviderSpec, 0, len(items))

			for _, m := range items {
				name := firstNonEmpty(m.s("name"), m.s("title"))
				url := firstNonEmpty(m.s("url"), m.s("base_url"), m.s("endpoint"), m.s("api_url"))
				if name == "" && url == "" {
					continue
				}
				if name == "" {
					name = url
				}

				keyed := false
				hasKey := false
				for _, k := range []string{"key_required", "key", "auth", "api_key"} {
					if v, ok := m.b(k); ok {
						keyed = v
						hasKey = true
						break
					}
				}
				kind := "keyless"
				if hasKey && keyed {
					kind = "keyed"
				}

				models := m.list("models")
				if len(models) == 0 {
					if s := m.s("models"); s != "" {
						parts := strings.Split(s, ",")
						for _, p := range parts {
							if p = strings.TrimSpace(p); p != "" {
								models = append(models, p)
							}
						}
					}
				}

				provs = append(provs, core.ProviderSpec{
					Name:    name,
					Impl:    "openailike",
					BaseURL: url,
					Kind:    kind,
					Weight:  20,
					Notes:   "free-tier entry from mnfst/awesome-free-llm-apis (daily JSON)",
					Models:  models,
					Source:  "awesome-llm-apis",
				})

				for _, mdl := range models {
					notes = append(notes, core.ModelNote{
						Provider: name,
						Model:    mdl,
						Tier: core.TierInfo{
							Caps:   "free",
							Notes:  "listed in mnfst/awesome-free-llm-apis",
							Source: "awesome-llm-apis",
						},
					})
				}
			}
			return notes, provs, nil
		},
	}
}
