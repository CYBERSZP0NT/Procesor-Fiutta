package cli

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/tui"
)

var tuiOpts struct {
	interval time.Duration
	local    bool
	session  string
	tab      string
}

var tuiCmd = &cobra.Command{
	Use:     "tui",
	Aliases: []string{"ui", "top"},
	Short:   "Interactive terminal UI: dashboard, usage & limits, history, sessions, chat, control",
	Long: `Full-screen, keyboard-driven UI over everything the router knows and
can do:

  1 Dashboard  totals, requests/min sparkline, top providers, failures
  2 Usage      usage vs. limits, provider-reported quota, cooldowns
               (enter details · d disable provider · c clear cooldowns)
  3 History    every request / attempt / task / session / control event
               (/ search · e errors · t kind · f follow · enter details)
  4 Sessions   per-session totals (enter → that session's history)
  5 Chat       talk to any route or model (/model, /session, /system, /new)
  6 Control    profile auto ⇄ control, pause, fallback, forever, budgets,
               escalation, providers on/off, allow-list, save to config

With a running server (serve / wrap / --url) the UI is live and control
changes apply to the server. Otherwise it reads the ledger and routes chat
in-process (--local forces that).

Examples:
  szmelc-router tui
  szmelc-router tui --tab usage --interval 1s
  szmelc-router --url http://box:8787 tui`,
	RunE: runTUI,
}

func init() {
	tuiCmd.Flags().DurationVar(&tuiOpts.interval, "interval", 2*time.Second, "refresh period")
	tuiCmd.Flags().BoolVar(&tuiOpts.local, "local", false, "do not use a running server")
	tuiCmd.Flags().StringVar(&tuiOpts.session, "session", "", "session id for chat (default tui-<time>)")
	tuiCmd.Flags().StringVar(&tuiOpts.tab, "tab", "dashboard", "start tab: dashboard | usage | history | sessions | chat | control")
	rootCmd.AddCommand(tuiCmd)
}

func runTUI(cmd *cobra.Command, args []string) error {
	if !term.IsTerminal(os.Stdout.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
		return fmt.Errorf("tui needs an interactive terminal (for scripts use: usage, history, ctl, exec — all with --json)")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var be tui.Backend
	base := serverURL(cfg)
	key := newAPIClient(cfg).key
	if !tuiOpts.local && tui.Ping(base, key) {
		be = tui.NewLive(base, key, config.UserPath(pCfgPath))
	} else {
		be, err = tui.NewLocal(cfg, config.UserPath(pCfgPath), globalTimeout())
		if err != nil {
			return err
		}
	}
	defer be.Close()

	model := pModel
	m := tui.New(be, tui.Options{Interval: tuiOpts.interval, Model: model, Session: tuiOpts.session, StartTab: tuiOpts.tab})
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
