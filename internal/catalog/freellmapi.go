package catalog

import (
	"encoding/json"

	"github.com/silverx/szmelc-router/internal/core"
)

// DEFAULT_FREELLM_API_URL is the signed public catalog of freellmapi.co.
const DEFAULT_FREELLM_API_URL = "https://freellmapi.co/models"

// FreellmapiSource implements the freellmapi catalog adapter.
//
// Tolerant input shapes (all observed over the wire at some point):
//
//	[ {name|id|model, provider|owner, price?, base_url?} ]
//	{ "models": [ ... same items ... ] }
//
// Free items (price 0 / "free" / 0.0) become free-tier notes. Paid items are
// still listed (as provider specs) but carry a "paid" cap note.
func FreellmapiSource() *RemoteSource {
	return &RemoteSource{
		Name: "freellmapi",
		URL:  DEFAULT_FREELLM_API_URL,
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

			type agg struct {
				spec   core.ProviderSpec
				models []string
			}
			byProv := make(map[string]*agg)
			var notes []core.ModelNote

			for _, m := range items {
				provider := firstNonEmpty(m.s("provider"), m.s("owner"), m.s("source"), "freellmapi")
				model := firstNonEmpty(m.s("name"), m.s("id"), m.s("model"))
				if model == "" {
					continue
				}

				free := true
				if p := m.s("price"); p != "" && p != "0" && p != "free" && p != "0.0" && p != "0.00" {
					free = false
				}
				if rf, ok := m.b("free"); ok {
					free = rf
				}

				caps := "free"
				if !free {
					caps = "paid"
				}
				t := core.TierInfo{Caps: caps, Notes: "via freellmapi signed catalog", Source: "freellmapi"}
				if !free {
					t.Notes = "paid (price " + m.s("price") + ") via freellmapi"
				}
				notes = append(notes, core.ModelNote{Provider: provider, Model: model, Tier: t})

				a := byProv[provider]
				if a == nil {
					a = &agg{
						spec: core.ProviderSpec{
							Name:   provider,
							Kind:   "keyless",
							Weight: 20,
							Source: "freellmapi",
						},
					}
					byProv[provider] = a
				}
				a.models = append(a.models, model)
				a.spec.BaseURL = firstNonEmpty(a.spec.BaseURL, m.s("base_url"), m.s("url"))
			}

			provs := make([]core.ProviderSpec, 0, len(byProv))
			for _, a := range byProv {
				a.spec.Models = a.models
				provs = append(provs, a.spec)
			}
			return notes, provs, nil
		},
	}
}
