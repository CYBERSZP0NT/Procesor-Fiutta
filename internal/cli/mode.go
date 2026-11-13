package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/agentic"
	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
)

var modeCmd = &cobra.Command{
	Use:   "mode [rozklad-jazdy|jako-tako|live]",
	Short: "Show or set the agentic task router's default mode",
	Long: `The task router (model "agent") has three modes:

  rozklad-jazdy  [Rozkład Jazdy]  all in: always the strongest available model
                                  first, biggest context, cost and tier caps
                                  ignored. The default.
  jako-tako      Jako Tako        just enough: the cheapest model that reaches
                                  the tier the task needs, stronger ones only
                                  as escalation.
  live           Live             by richness: all in, ranked by the signed
                                  FreeLLMAPI live catalog (size class, then
                                  context, then free quota). The default when
                                  a working FreeLLMAPI key is found and no
                                  mode is set — see szmelc-router live.

With no argument this prints the mode in effect. With one, it writes
agentic.mode to your config file (only that key — the rest of the file is
kept). Any spelling works: "[Rozkład Jazdy]", "rozklad jazdy", "Jako Tako",
"po bogatości". Setting a mode here also opts out of the live default.

A running server is not affected until restart; switch it live with
  curl -X PUT localhost:8787/v1/route/mode -d '{"mode":"jako-tako"}'
Per request: model "agent:jako-tako", header X-Freellm-Task-Mode, or
metadata.task_mode. Env: FREELLM_TASK_MODE overrides the config.

Examples:
  szmelc-router mode
  szmelc-router mode jako-tako
  szmelc-router mode "[Rozkład Jazdy]"`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			id, ok := agentic.NormalizeMode(args[0])
			if !ok {
				return fmt.Errorf("unknown mode %q (%s)", args[0], agentic.ModeIDs())
			}
			path := config.UserPath(pCfgPath)
			if err := config.SetUserValue(path, []string{"agentic", "mode"}, id); err != nil {
				return err
			}
			fmt.Printf("✓ mode %s (%s) saved to %s\n", agentic.ModeName(id), id, path)
			return nil
		}

		cfg, err := config.Load(pCfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		current := agentic.DefaultMode
		if base, err := agentic.Default(); err == nil {
			current = base.Merge(cfg.Agentic).EffectiveMode()
		}
		if pJSON {
			printJSON(map[string]any{"mode": current, "modes": agentic.Modes})
			return nil
		}
		for _, m := range agentic.Modes {
			mark := " "
			if m.ID == current {
				mark = "▶"
			}
			fmt.Printf("%s %-16s %-14s %s\n", mark, m.Name, m.ID, m.Summary)
		}
		fmt.Printf("\nchange: szmelc-router mode %s\n", strings.Join(otherModes(current), " | "))
		return nil
	},
}

func otherModes(current string) []string {
	var out []string
	for _, m := range agentic.Modes {
		if m.ID != current {
			out = append(out, m.ID)
		}
	}
	return out
}

func init() {
	rootCmd.AddCommand(modeCmd)
}
