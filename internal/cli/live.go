package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/agentic"
	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/live"
)

var liveCmd = &cobra.Command{
	Use:   "live [status|check|sync]",
	Short: "FreeLLMAPI live: key, license, signed live catalog and the live default",
	Long: `With a working FreeLLMAPI key the task router defaults to mode "live":
routing by richness over the signed live catalog of freellmapi.co — the
biggest size class first (Frontier > Large > Medium > Small), then the
biggest context window, then the biggest free daily quota.

Keys are found in FREELLM_API_KEY (also FREELLMAPI_LICENSE_KEY,
FREELLMAPI_API_KEY, live.key in the config, FreeLLMAPI's own
FREEAPI_CONFIG_JSON/FREEAPI_CONFIG_PATH "license") and told apart by prefix:

  fla_…         Premium license ("live" plan). Checked with
                GET api.freellmapi.co/v1/license/check, then the live catalog
                is pulled from /v1/latest and verified (Ed25519, pinned key).
  freellmapi-…  unified key of a FreeLLMAPI server you run (the sidecar).
                Checked with GET $FREELLMAPI_URL/v1/models.

The verdict is cached (license 6h, catalog 12h; a "valid" verdict survives
a service outage for 7 days), so normal commands rarely touch the network.

  status   what was found and what it does (default; uses the cache)
  check    re-check the key now, ignoring the cache
  sync     re-check and re-download the live catalog now

Opt out: live.enabled: off (or FREELLM_LIVE=off). Keep your mode but use the
live catalog: live.keep_mode: true, or set any mode (szmelc-router mode …).
Use it per request: model "live", "agent:live", header X-Freellm-Task-Mode: live.`,
	Args:      cobra.MaximumNArgs(1),
	ValidArgs: []string{"status", "check", "sync"},
	RunE:      runLive,
}

func init() {
	rootCmd.AddCommand(liveCmd)
}

func runLive(cmd *cobra.Command, args []string) error {
	action := "status"
	if len(args) == 1 {
		action = strings.ToLower(args[0])
	}
	if action != "status" && action != "check" && action != "sync" {
		return fmt.Errorf("unknown action %q (status | check | sync)", action)
	}
	prev, had := os.LookupEnv("FREELLM_LIVE")
	if action != "status" {
		// Load must not run the cached check first; it is forced below.
		os.Setenv("FREELLM_LIVE", "off")
	}
	cfg, err := config.Load(pCfgPath)
	if had {
		os.Setenv("FREELLM_LIVE", prev)
	} else {
		os.Unsetenv("FREELLM_LIVE")
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if action != "status" {
		o := cfg.LiveOptions()
		o.Offline = false
		o.Force = true
		o.ForceCatalog = action == "sync"
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cfg.ApplyLiveResult(live.Resolve(ctx, o), cfg.Agentic.Mode != "")
	}
	st := cfg.LiveState
	if st == nil {
		st = &live.State{Reason: "not checked"}
	}
	mode := agentic.DefaultMode
	if base, err := agentic.Default(); err == nil {
		mode = base.Merge(cfg.Agentic).EffectiveMode()
	}
	if pJSON {
		printJSON(map[string]any{"live": st, "mode": mode})
		return nil
	}
	printLive(st, mode)
	return nil
}

func printLive(st *live.State, mode string) {
	mark := "✗"
	if st.Active {
		mark = "✓"
	}
	fmt.Printf("%s live      %s\n", mark, st.Reason)
	if len(st.Keys) == 0 {
		fmt.Println("  keys      none (export FREELLM_API_KEY=fla_…)")
	}
	for _, k := range st.Keys {
		fmt.Printf("  key       %-8s %s  (%s)\n", k.Kind, k.Key, k.From)
	}
	if l := st.License; l != nil {
		fmt.Printf("  license   valid=%t plan=%s status=%s expires=%s  [%s, checked %s]\n",
			l.Valid, dash(l.Plan), dash(l.Status), dash(l.ExpiresAt), dash(st.Source), l.CheckedAt.Format(time.RFC3339))
	}
	if c := st.Catalog; c != nil {
		fmt.Printf("  catalog   %s v%s, %d models  [%s, fetched %s]\n", c.Tier, c.Version, c.Models, c.Source, c.FetchedAt.Format(time.RFC3339))
	}
	if s := st.Sidecar; s != nil {
		fmt.Printf("  sidecar   %s, %d models (key %s from %s)\n", s.URL, s.Models, s.Key, s.From)
	}
	if st.Active {
		fmt.Printf("  router    %d live entries in the task catalog\n", st.Entries)
	}
	for _, w := range st.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
	def := ""
	switch {
	case st.DefaultMode:
		def = " — the live default (no mode set in config/env)"
	case st.Active && mode != agentic.ModeLive:
		def = " — kept: a mode is set in config/env (szmelc-router mode live to switch)"
	}
	fmt.Printf("  mode      %s (%s)%s\n", agentic.ModeName(mode), mode, def)
}
