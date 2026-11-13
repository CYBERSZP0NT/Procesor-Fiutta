package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/silverx/szmelc-router/internal/config"
)

type modelsOpts struct {
	kind string
	tier bool
}

var modelsOptsVar = &modelsOpts{}

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List route aliases + per-provider model catalog",
	Long: `Show available route aliases and the provider catalog.
With --json, output is machine-readable JSON.
With --tier, free-tier notes are shown alongside providers.

Examples:
  szmelc-router models
  szmelc-router models --provider groq
  szmelc-router models --kind keyless
  szmelc-router models --json --tier`,
	RunE: runModels,
}

func init() {
	modelsCmd.Flags().StringVar(&modelsOptsVar.kind, "kind", "", "filter by kind (keyless|keyed|local|scraper|sidecar)")
	modelsCmd.Flags().BoolVarP(&modelsOptsVar.tier, "tier", "t", false, "show free-tier notes")
}

// JSON output shapes.
type modelsJSON struct {
	Routes    []routeJSON    `json:"routes"`
	Providers []providerJSON `json:"providers"`
}

type routeJSON struct {
	Name  string   `json:"name"`
	Chain []string `json:"chain"`
}

type providerJSON struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url,omitempty"`
	Notes   string `json:"notes,omitempty"`
	KeyEnv  string `json:"key_env,omitempty"`
}

func runModels(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(pCfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// Filter providers.
	var filtered []config.Provider
	for _, p := range cfg.Providers {
		if modelsOptsVar.kind != "" && !strings.EqualFold(p.Kind, modelsOptsVar.kind) {
			continue
		}
		if pProvider != "" && !strings.EqualFold(p.Name, pProvider) {
			continue
		}
		filtered = append(filtered, p)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })

	if pJSON {
		return modelsJSONOut(cfg, filtered)
	}
	return modelsTextOut(cfg, filtered)
}

func modelsJSONOut(cfg *config.Config, providers []config.Provider) error {
	routes := cfg.RouteNames()
	sort.Strings(routes)

	var out modelsJSON
	for _, r := range routes {
		out.Routes = append(out.Routes, routeJSON{Name: r, Chain: cfg.Routes[r]})
	}
	for _, p := range providers {
		e := providerJSON{Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, KeyEnv: p.KeyEnv}
		if modelsOptsVar.tier {
			e.Notes = p.Notes
		}
		out.Providers = append(out.Providers, e)
	}
	printJSON(out)
	return nil
}

func modelsTextOut(cfg *config.Config, providers []config.Provider) error {
	fmt.Println("Routes:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, name := range cfg.RouteNames() {
		chain := strings.Join(cfg.Routes[name], " → ")
		fmt.Fprintf(w, "  %-12s %s\n", name, chain)
	}
	w.Flush()

	headers := "  NAME              KIND       STATUS    BASE_URL"
	fmt.Printf("\nProviders:\n%s\n", strings.Repeat("-", len(headers)))

	w2 := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range providers {
		status := "ok"
		if p.KeyEnv != "" {
			if os.Getenv(p.KeyEnv) == "" {
				status = "no-key"
			}
		}
		baseURL := p.BaseURL
		if len(baseURL) > 40 {
			baseURL = baseURL[:37] + "..."
		}
		fmt.Fprintf(w2, "  %-16s %-9s %-9s %s\n", p.Name, p.Kind, status, baseURL)
		if modelsOptsVar.tier && p.Notes != "" {
			fmt.Fprintf(w2, "  %-16s %s\n", "", strings.Repeat(" ", 18)+p.Notes)
		}
	}
	w2.Flush()

	fmt.Printf("\n  (%d providers, %d routes)\n", len(providers), len(cfg.Routes))
	return nil
}
