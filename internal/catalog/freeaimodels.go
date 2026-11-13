package catalog

import (
	"encoding/json"
	"fmt"

	"github.com/silverx/szmelc-router/internal/core"
)

// DEFAULT_FREE_AI_MODELS_URL is the ClawLabsAI/free-ai-models daily status
// feed. Entries report the *current* state of each model (up/down), which is
// ideal for tier-note enrichment (caps = up/down) rather than adding
// providers.
const DEFAULT_FREE_AI_MODELS_URL = "https://raw.githubusercontent.com/ClawLabsAI/free-ai-models/main/status.json"

// FreeAIModelsSource implements the ClawLabsAI/free-ai-models adapter.
//
// Tolerated item keys:
//
//	model|name, provider|source, status|state ("up"|"down"|bool|ok),
//	latency_ms|latency
func FreeAIModelsSource() *RemoteSource {
	return &RemoteSource{
		Name: "free-ai-models",
		URL:  DEFAULT_FREE_AI_MODELS_URL,
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
			type agg struct {
				spec   core.ProviderSpec
				models []string
				up     int
			}
			byProv := make(map[string]*agg)

			for _, m := range items {
				model := firstNonEmpty(m.s("model"), m.s("name"))
				provider := firstNonEmpty(m.s("provider"), m.s("source"), "unknown")
				if model == "" {
					continue
				}

				caps := "unknown"
				notesTxt := "daily status from ClawLabsAI/free-ai-models"
				if v, ok := m.b("status"); ok {
					if v {
						caps = "up"
					} else {
						caps = "down"
					}
					notesTxt = fmt.Sprintf("currently %s", caps)
				}
				if s := firstNonEmpty(m.s("status"), m.s("state")); s != "" {
					switch s {
					case "up", "ok", "online", "working", "true":
						caps = "up"
					case "down", "offline", "broken", "error", "false":
						caps = "down"
					}
					notesTxt = fmt.Sprintf("currently %s", caps)
				}
				if n := firstNonEmpty(m.s("latency_ms"), m.s("latency")); n != "" {
					notesTxt += " (" + n + " ms last run)"
				}

				notes = append(notes, core.ModelNote{
					Provider: provider,
					Model:    model,
					Tier:     core.TierInfo{Caps: caps, Notes: notesTxt, Source: "free-ai-models"},
				})

				a := byProv[provider]
				if a == nil {
					a = &agg{spec: core.ProviderSpec{
						Name: provider, Kind: "keyless", Weight: 15, Source: "free-ai-models",
						Notes: "status-tracked (ClawLabsAI/free-ai-models)",
					}}
					byProv[provider] = a
				}
				a.models = append(a.models, model)
				if caps == "up" {
					a.up++
				}
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
