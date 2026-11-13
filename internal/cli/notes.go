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

var notesCmd = &cobra.Command{
	Use:   "notes",
	Short: "Show the free-tier cheat sheet (from catalog data, nothing hardcoded)",
	Long: `Print a human-readable cheat sheet of free-tier notes for every configured
provider. The data comes from the ` + "`notes`" + ` field in defaults/providers.yaml
and any user config overrides — nothing is hardcoded in Go.

Examples:
  szmelc-router notes
  szmelc-router notes --provider groq
  szmelc-router --json notes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(pCfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		// Collect providers with notes.
		type noteEntry struct {
			Name  string
			Kind  string
			Notes string
		}

		var entries []noteEntry
		for _, p := range cfg.Providers {
			if p.Notes == "" {
				continue
			}
			if pProvider != "" && !strings.EqualFold(p.Name, pProvider) {
				continue
			}
			entries = append(entries, noteEntry{Name: p.Name, Kind: p.Kind, Notes: p.Notes})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

		if len(entries) == 0 {
			fmt.Println("No notes configured.")
			return nil
		}

		if pJSON {
			printJSON(entries)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Printf("Free-tier cheat sheet (%d providers with notes)\n", len(entries))
		fmt.Println(strings.Repeat("─", 60))
		for _, e := range entries {
			fmt.Fprintf(w, "  %-16s  %s\n", e.Name+" ("+e.Kind+")", e.Notes)
		}
		w.Flush()
		return nil
	},
}
