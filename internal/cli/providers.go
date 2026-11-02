package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/pkg/router"
)

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Manage configured providers (list, add, remove, test)",
	Long: `Manage the provider list in your config file.

  list    – show all configured providers
  add     – add a new provider (edit config)
  remove  – remove a provider by name
  test    – test a single provider's connectivity

Examples:
  freellm-router providers list
  freellm-router providers add --name myprovider --base-url https://api.example.com/v1 --kind keyless
  freellm-router providers remove myprovider
  freellm-router providers test groq`,
}

// ── providers list ─────────────────────────────────────────────────────
var providersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(pCfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		if pJSON {
			type pj struct {
				Name    string `json:"name"`
				Impl    string `json:"impl"`
				Kind    string `json:"kind"`
				BaseURL string `json:"base_url,omitempty"`
				KeyEnv  string `json:"key_env,omitempty"`
				Notes   string `json:"notes,omitempty"`
			}
			out := make([]pj, 0, len(cfg.Providers))
			for _, p := range cfg.Providers {
				out = append(out, pj{
					Name:    p.Name,
					Impl:    p.Impl,
					Kind:    p.Kind,
					BaseURL: p.BaseURL,
					KeyEnv:  p.KeyEnv,
					Notes:   p.Notes,
				})
			}
			printJSON(out)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "  %-16s  %-11s  %-9s  %-14s  %s\n", "NAME", "IMPL", "KIND", "KEY ENV", "BASE_URL")
		fmt.Fprintf(w, "  %-16s  %-11s  %-9s  %-14s  %s\n", "----", "----", "----", "-----", "----")
		for _, p := range cfg.Providers {
			fmt.Fprintf(w, "  %-16s  %-11s  %-9s  %-14s  %s\n",
				p.Name, p.Impl, p.Kind, p.KeyEnv, p.BaseURL)
		}
		w.Flush()
		fmt.Printf("\n  %d providers\n", len(cfg.Providers))
		return nil
	},
}

// ── providers add ───────────────────────────────────────────────────────
type addOpts struct {
	name    string
	baseURL string
	kind    string
	keyEnv  string
	impl    string
	weight  int
	notes   string
}

var addOptsVar = &addOpts{}

var providersAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new provider to config (edit the config file safely)",
	Long: `Add a new provider entry to your config file.
This is the "add a source from MORE.md" workflow — one command, no hand-editing.

The provider is appended to the config file under providers:.
If a provider with the same name exists, it will be updated in place.

Examples:
  freellm-router providers add --name deepseek --base-url https://api.deepseek.com/v1 --kind keyed --key-env DEEPSEEK_API_KEY
  freellm-router providers add --name ollama --base-url http://localhost:11434/v1 --kind local --impl ollama`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if addOptsVar.name == "" {
			return fmt.Errorf("--name is required")
		}
		if addOptsVar.baseURL == "" && addOptsVar.kind != "plugin" {
			return fmt.Errorf("--base-url is required (for non-plugin providers)")
		}

		cfgPath := configFile()
		if cfgPath == "" {
			cfgPath = "$HOME/.config/freellm/config.yaml"
		}

		cfg, err := config.Load("")
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		existsIdx := -1
		for i, p := range cfg.Providers {
			if p.Name == addOptsVar.name {
				existsIdx = i
				break
			}
		}

		impl := addOptsVar.impl
		if impl == "" {
			impl = "openailike"
		}
		kind := addOptsVar.kind
		if kind == "" {
			kind = "keyless"
		}

		entry := config.Provider{
			Name:    addOptsVar.name,
			Impl:    impl,
			BaseURL: addOptsVar.baseURL,
			Kind:    kind,
			KeyEnv:  addOptsVar.keyEnv,
			Weight:  addOptsVar.weight,
			Notes:   addOptsVar.notes,
		}

		if existsIdx >= 0 {
			cfg.Providers[existsIdx] = entry
		} else {
			cfg.Providers = append(cfg.Providers, entry)
		}

		if err := saveConfigTo(cfgPath, cfg); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}

		action := "added"
		if existsIdx >= 0 {
			action = "updated"
		}
		fmt.Printf("Provider %q %s (%d providers total).\n", entry.Name, action, len(cfg.Providers))
		fmt.Printf("  base_url: %s\n", entry.BaseURL)
		if entry.KeyEnv != "" {
			fmt.Printf("  key_env:  %s (set before use)\n", entry.KeyEnv)
		}
		return nil
	},
}

// ── providers remove ────────────────────────────────────────────────────
var providersRemoveCmd = &cobra.Command{
	Use:   "remove NAME",
	Short: "Remove a provider by name from config",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfgPath := configFile()
		if cfgPath == "" {
			cfgPath = "$HOME/.config/freellm/config.yaml"
		}

		cfg, err := config.Load("")
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		found := false
		for i, p := range cfg.Providers {
			if p.Name == name {
				cfg.Providers = append(cfg.Providers[:i], cfg.Providers[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("provider %q not found in config", name)
		}

		if err := saveConfigTo(cfgPath, cfg); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}

		fmt.Printf("Provider %q removed.\n", name)
		return nil
	},
}

// ── providers test ──────────────────────────────────────────────────────
var providersTestCmd = &cobra.Command{
	Use:   "test NAME",
	Short: "Test a single provider's connectivity",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load(pCfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		prov, ok := cfg.ProviderByName(name)
		if !ok {
			return fmt.Errorf("provider %q not found in config", name)
		}

		// Build a router with just this provider.
		provCfg := *cfg
		provCfg.Providers = []config.Provider{prov}
		r, err := buildRouter(&provCfg, globalTimeout())
		if err != nil {
			return fmt.Errorf("router: %w", err)
		}

		ctx := cmd.Context()
		messages := []router.Message{{Role: "user", Content: "pong"}}

		start := time.Now()
		via, done, err := r.Route(ctx, "auto", messages, "", func(string) {})
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("✗ %s: FAIL (%s) — %v\n", name, elapsed.Truncate(time.Millisecond), err)
			return nil
		}

		preview := done.Text
		if len(preview) > 80 {
			preview = preview[:80] + "…"
		}
		fmt.Printf("✓ %s: OK (%s) via %s\n", name, elapsed.Truncate(time.Millisecond), via)
		if preview != "" {
			fmt.Printf("  response: %s\n", preview)
		}
		return nil
	},
}

func init() {
	providersCmd.AddCommand(providersListCmd)
	providersCmd.AddCommand(providersAddCmd)
	providersCmd.AddCommand(providersRemoveCmd)
	providersCmd.AddCommand(providersTestCmd)

	providersAddCmd.Flags().StringVar(&addOptsVar.name, "name", "", "provider name (unique id)")
	providersAddCmd.Flags().StringVar(&addOptsVar.baseURL, "base-url", "", "base URL (e.g. https://api.example.com/v1)")
	providersAddCmd.Flags().StringVar(&addOptsVar.kind, "kind", "", "kind: keyless|keyed|local|scraper|sidecar|plugin")
	providersAddCmd.Flags().StringVar(&addOptsVar.keyEnv, "key-env", "", "env var name holding the API key")
	providersAddCmd.Flags().StringVar(&addOptsVar.impl, "impl", "openailike", "impl: openailike|ollama|g4fbridge|plugin")
	providersAddCmd.Flags().IntVar(&addOptsVar.weight, "weight", 25, "chain weight (higher = tried first)")
	providersAddCmd.Flags().StringVar(&addOptsVar.notes, "notes", "", "free-tier notes (shown in CLI + catalog)")
}

// configFile returns the active config file path (or "" if none).
func configFile() string {
	if pCfgPath != "" {
		return pCfgPath
	}
	if v := os.Getenv("FREELLM_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		candidate := home + "/.config/freellm/config.yaml"
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func saveConfigTo(path string, cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	// Ensure directory exists.
	dir := path[:len(path)-len("/config.yaml")]
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return os.WriteFile(path, data, 0644)
}
