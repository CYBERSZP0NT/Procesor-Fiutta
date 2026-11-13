// Package cli implements the freellm-router command-line interface using
// spf13/cobra. It provides subcommands: chat, models, probe, providers,
// notes, config, and serve (stub for step 4).
package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// Global build-info variables (set by -ldflags).
var (
	version = "dev"
	commit  = "none"
	date    = ""
)

// Persistent global flags available to all subcommands.
var (
	pCfgPath    string
	pProvider   string
	pModel      string
	pTimeoutSec int
	pJSON       bool
)

// globalTimeout returns the per-request timeout as a Duration.
func globalTimeout() time.Duration {
	if pTimeoutSec > 0 {
		return time.Duration(pTimeoutSec) * time.Second
	}
	return 120 * time.Second
}

// rootCmd is the base command for the CLI.
var rootCmd = &cobra.Command{
	Use:   "freellm-router",
	Short: "One router over every free/cheap LLM source you have",
	Long: `freellm-router routes chat completions across all your free and
low-cost LLM providers with automatic fallback, key rotation, and
cooldown management.

It speaks a standard OpenAI-compatible API (server mode) and offers
a friendly CLI (chat, probe, models, providers, …).

Adding a new source is a config line or a small plugin — never a
code change to the core.`,
	Version:       fmt.Sprintf("%s (commit %s, %s)", version, commit, date),
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI. Returns 0 on success, 1 on error.
func Execute(args []string) int {
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return exitCodeOf(err)
	}
	return 0
}

// SetVersionInfo sets build-info from main (called before Execute).
func SetVersionInfo(vert, cmt, dt string) {
	version = vert
	commit = cmt
	date = dt
	rootCmd.Version = fmt.Sprintf("%s (commit %s, %s)", version, commit, date)
}

func init() {
	rootCmd.PersistentFlags().StringVar(&pCfgPath, "config", "", "path to config file (default: $FREELLM_CONFIG or ~/.config/freellm/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&pProvider, "provider", "", "default provider filter")
	rootCmd.PersistentFlags().StringVarP(&pModel, "model", "m", "", "default model / route alias")
	rootCmd.PersistentFlags().IntVar(&pTimeoutSec, "timeout", 120, "per-request timeout (seconds)")
	rootCmd.PersistentFlags().BoolVar(&pJSON, "json", false, "output JSON instead of human-readable text")
	rootCmd.PersistentFlags().StringVar(&pURL, "url", "", "running server to talk to (default: $FREELLM_URL or the configured host:port)")

	rootCmd.AddCommand(
		chatCmd,
		modelsCmd,
		probeCmd,
		providersCmd,
		notesCmd,
		statsCmd,
		configCmd,
		serveCmd,
	)
}
