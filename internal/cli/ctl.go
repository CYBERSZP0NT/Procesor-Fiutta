package cli

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/control"
)

var ctlOpts struct {
	save  bool
	local bool
}

var ctlCmd = &cobra.Command{
	Use:     "ctl",
	Aliases: []string{"control"},
	Short:   "Control plane: profiles (auto ⇄ control), pause, providers, fallback, budgets, hooks",
	Long: `Steer the router — live on a running server, and/or persisted in the
config (control: section).

Profiles:
  auto      infinite & full auto: fall back freely, wait out cooldowns and
            retry until something answers, escalate to the "auto" route,
            respect quota budgets
  control   120% control: exactly the model you asked for (no fallback),
            no silent retries, budgets enforced, hooks may veto/rewrite
  balanced  the classic behaviour (walk the chain once)

Changes go to the running server (serve / wrap / --url). Add --save to
also write them to your config, or --local to only edit the config.
With no server running, the config is edited.

Examples:
  freellm-router ctl                      # status
  freellm-router ctl profile auto         # go infinite
  freellm-router ctl profile control --save
  freellm-router ctl pause / resume       # stop/allow all routing
  freellm-router ctl disable groq mistral # take providers out
  freellm-router ctl enable groq
  freellm-router ctl allow ollama         # only these (ctl allow --clear)
  freellm-router ctl fallback off         # on | off | default
  freellm-router ctl forever on           # on | off | default
  freellm-router ctl limits on            # enforce declared quotas
  freellm-router ctl max-attempts 3
  freellm-router ctl max-wait 10m         # forever-mode waiting cap (0 = none)
  freellm-router ctl escalate free        # route tried after the chain (none to stop)
  freellm-router ctl hook pre ./guard.sh  # veto/rewrite hook (hook pre --clear)
  freellm-router ctl cooldowns            # list; ctl cooldowns clear [via]

Per request: headers X-Freellm-Profile / -Fallback / -Forever /
-Max-Attempts / -Session / -Tags, or exec --profile/--forever/--no-fallback.`,
	RunE: func(cmd *cobra.Command, args []string) error { return ctlShow() },
}

func init() {
	ctlCmd.PersistentFlags().BoolVar(&ctlOpts.save, "save", false, "also persist the change to the config file")
	ctlCmd.PersistentFlags().BoolVar(&ctlOpts.local, "local", false, "only edit the config file (do not contact a server)")

	sub := func(use, short string, args cobra.PositionalArgs, fn func(args []string, s *control.Settings) error) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, a []string) error {
			return ctlApply(func(s *control.Settings) error { return fn(a, s) })
		}}
	}
	onOff := func(name string, field func(*control.Settings) **bool) func([]string, *control.Settings) error {
		return func(a []string, s *control.Settings) error {
			switch strings.ToLower(a[0]) {
			case "on", "true", "1", "yes":
				*field(s) = control.Bool(true)
			case "off", "false", "0", "no":
				*field(s) = control.Bool(false)
			case "default", "profile", "unset":
				*field(s) = nil
			default:
				return fmt.Errorf("%s: want on | off | default, got %q", name, a[0])
			}
			return nil
		}
	}

	var allowClear, hookClear bool
	allowCmd := sub("allow [provider...]", "Only route to these providers (--clear = all)", cobra.ArbitraryArgs,
		func(a []string, s *control.Settings) error {
			if allowClear || len(a) == 0 {
				s.Allow = nil
				return nil
			}
			s.Allow = lower(a)
			return nil
		})
	allowCmd.Flags().BoolVar(&allowClear, "clear", false, "remove the allow-list")

	hookCmd := sub("hook pre|post [command...]", "Set the pre-call (veto/rewrite) or post-call hook", cobra.MinimumNArgs(1),
		func(a []string, s *control.Settings) error {
			var argv []string
			// Flag parsing stops at the first argument (so hook commands can
			// carry their own flags): accept "--clear"/"none" after pre|post too.
			if len(a) == 2 && (a[1] == "--clear" || a[1] == "none") {
				hookClear = true
			}
			if !hookClear {
				if len(a) < 2 {
					return fmt.Errorf("give the hook command, or --clear")
				}
				argv = a[1:]
			}
			switch a[0] {
			case "pre":
				s.Hooks.PreCall = argv
			case "post":
				s.Hooks.PostCall = argv
			default:
				return fmt.Errorf("hook: pre | post, got %q", a[0])
			}
			return nil
		})
	hookCmd.Flags().BoolVar(&hookClear, "clear", false, "remove the hook")
	hookCmd.Flags().SetInterspersed(false)

	cooldownsCmd := &cobra.Command{
		Use:   "cooldowns [clear [provider/model]]",
		Short: "List live cooldowns, or clear them",
		Args:  cobra.MaximumNArgs(2),
		RunE:  runCtlCooldowns,
	}

	ctlCmd.AddCommand(
		&cobra.Command{Use: "status", Short: "Show settings and effective behaviour", RunE: func(*cobra.Command, []string) error { return ctlShow() }},
		sub("profile auto|control|balanced", "Switch the profile", cobra.ExactArgs(1), func(a []string, s *control.Settings) error {
			p, ok := control.NormalizeProfile(a[0])
			if !ok {
				return fmt.Errorf("unknown profile %q (auto | control | balanced)", a[0])
			}
			s.Profile = p
			return nil
		}),
		sub("pause", "Refuse every call until resume", cobra.NoArgs, func(_ []string, s *control.Settings) error { s.Paused = true; return nil }),
		sub("resume", "Allow calls again", cobra.NoArgs, func(_ []string, s *control.Settings) error { s.Paused = false; return nil }),
		sub("disable <provider...>", "Take providers out of routing", cobra.MinimumNArgs(1), func(a []string, s *control.Settings) error {
			s.Disabled = union(s.Disabled, lower(a))
			return nil
		}),
		sub("enable <provider...>", "Put providers back (all: everything)", cobra.MinimumNArgs(1), func(a []string, s *control.Settings) error {
			if len(a) == 1 && a[0] == "all" {
				s.Disabled = nil
				return nil
			}
			s.Disabled = minus(s.Disabled, lower(a))
			return nil
		}),
		allowCmd,
		sub("fallback on|off|default", "Allow walking the fallback chain", cobra.ExactArgs(1), onOff("fallback", func(s *control.Settings) **bool { return &s.Fallback })),
		sub("forever on|off|default", "Wait out cooldowns and retry until answered", cobra.ExactArgs(1), onOff("forever", func(s *control.Settings) **bool { return &s.Forever })),
		sub("limits on|off|default", "Enforce declared quotas as a budget", cobra.ExactArgs(1), onOff("limits", func(s *control.Settings) **bool { return &s.EnforceLimits })),
		sub("max-attempts <n>", "Cap upstream calls per request (0 = router default)", cobra.ExactArgs(1), func(a []string, s *control.Settings) error {
			n, err := strconv.Atoi(a[0])
			if err != nil || n < 0 {
				return fmt.Errorf("max-attempts: want a number >= 0")
			}
			s.MaxAttempts = n
			return nil
		}),
		sub("max-wait <duration>", "Cap forever-mode waiting per request (0 = until cancelled)", cobra.ExactArgs(1), func(a []string, s *control.Settings) error {
			d, err := time.ParseDuration(a[0])
			if a[0] == "0" {
				d, err = 0, nil
			}
			if err != nil || d < 0 {
				return fmt.Errorf("max-wait: want a duration like 90s or 10m")
			}
			s.MaxWaitSec = int(d.Seconds())
			return nil
		}),
		sub("escalate <route>|none|default", "Route to try when the chain is exhausted (forever mode)", cobra.ExactArgs(1), func(a []string, s *control.Settings) error {
			switch a[0] {
			case "default":
				s.Escalate = nil
			case "none", "off":
				s.Escalate = control.String("")
			default:
				s.Escalate = control.String(a[0])
			}
			return nil
		}),
		hookCmd,
		cooldownsCmd,
	)
	rootCmd.AddCommand(ctlCmd)
}

// ctlView mirrors the server's GET /v1/control body.
type ctlView struct {
	Settings  control.Settings `json:"settings"`
	Effective control.Resolved `json:"effective"`
	TaskMode  string           `json:"task_mode"`
	Cooldowns map[string]int   `json:"cooldowns_s"`
}

// ctlTarget returns a live client (nil when none is reachable or --local).
func ctlTarget(cfg *config.Config) *apiClient {
	if ctlOpts.local {
		return nil
	}
	c := newAPIClient(cfg)
	if !c.ping() {
		return nil
	}
	return c
}

func ctlShow() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var v ctlView
	where := "config " + config.UserPath(pCfgPath) + " (no server running)"
	if c := ctlTarget(cfg); c != nil {
		if err := c.get("/v1/control", &v); err != nil {
			return err
		}
		where = "live server " + c.base
	} else {
		v.Settings = cfg.Control
		v.Effective = cfg.Control.Resolve(control.Override{})
	}
	if pJSON {
		printJSON(v)
		return nil
	}
	printControl(where, v)
	return nil
}

func printControl(where string, v ctlView) {
	e := v.Effective
	onoff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	src := func(p *bool) string {
		if p == nil {
			return " (profile)"
		}
		return " (set)"
	}
	fmt.Printf("control — %s\n\n", where)
	fmt.Printf("  profile         %s\n", e.Profile)
	fmt.Printf("  paused          %s\n", onoff(e.Paused))
	fmt.Printf("  fallback        %s%s\n", onoff(e.Fallback), src(v.Settings.Fallback))
	fmt.Printf("  forever         %s%s\n", onoff(e.Forever), src(v.Settings.Forever))
	fmt.Printf("  enforce limits  %s%s\n", onoff(e.EnforceLimits), src(v.Settings.EnforceLimits))
	fmt.Printf("  escalate        %s\n", dash(e.Escalate))
	fmt.Printf("  max attempts    %s\n", zeroDash(e.MaxAttempts))
	fmt.Printf("  max wait        %s\n", zeroDash(e.MaxWaitSec, "s"))
	fmt.Printf("  disabled        %s\n", dash(strings.Join(e.Disabled, ", ")))
	fmt.Printf("  allow-list      %s\n", dash(strings.Join(e.Allow, ", ")))
	fmt.Printf("  pre-call hook   %s\n", dash(strings.Join(e.Hooks.PreCall, " ")))
	fmt.Printf("  post-call hook  %s\n", dash(strings.Join(e.Hooks.PostCall, " ")))
	if v.TaskMode != "" {
		fmt.Printf("  task mode       %s\n", v.TaskMode)
	}
	if len(v.Cooldowns) > 0 {
		fmt.Printf("  cooldowns       %d active (ctl cooldowns)\n", len(v.Cooldowns))
	}
}

func zeroDash(n int, unit ...string) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n) + strings.Join(unit, "")
}

// ctlApply reads the current settings (server, else config), applies fn,
// and writes the result to the server and/or the config.
func ctlApply(fn func(*control.Settings) error) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c := ctlTarget(cfg)
	var cur control.Settings
	if c != nil {
		var v ctlView
		if err := c.get("/v1/control", &v); err != nil {
			return err
		}
		cur = v.Settings
	} else {
		cur = cfg.Control
	}
	if err := fn(&cur); err != nil {
		return err
	}
	if c != nil {
		var v ctlView
		if err := c.do(cmdCtx(), http.MethodPut, "/v1/control", cur, &v); err != nil {
			return err
		}
		if !ctlOpts.save {
			printControl("live server "+c.base+" (not saved; add --save to persist)", v)
			return nil
		}
	}
	path := config.UserPath(pCfgPath)
	if err := config.SetUserAny(path, []string{"control"}, cur); err != nil {
		return err
	}
	where := "saved to " + path
	if c != nil {
		where = "live server " + c.base + " + " + where
	}
	printControl(where, ctlView{Settings: cur, Effective: cur.Resolve(control.Override{})})
	return nil
}

func runCtlCooldowns(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c := ctlTarget(cfg)
	if c == nil {
		return fmt.Errorf("cooldowns live in a running server; none reachable at %s (serve, wrap, or --url)", serverURL(cfg))
	}
	if len(args) > 0 {
		if args[0] != "clear" {
			return fmt.Errorf("usage: ctl cooldowns [clear [provider/model]]")
		}
		path := "/v1/cooldowns"
		if len(args) == 2 {
			path += "?via=" + args[1]
		}
		var out struct {
			Cleared int `json:"cleared"`
		}
		if err := c.do(cmdCtx(), http.MethodDelete, path, nil, &out); err != nil {
			return err
		}
		fmt.Printf("✓ cleared %d cooldown(s)\n", out.Cleared)
		return nil
	}
	var v ctlView
	if err := c.get("/v1/control", &v); err != nil {
		return err
	}
	if pJSON {
		printJSON(v.Cooldowns)
		return nil
	}
	if len(v.Cooldowns) == 0 {
		fmt.Println("no active cooldowns")
		return nil
	}
	keys := make([]string, 0, len(v.Cooldowns))
	for k := range v.Cooldowns {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		fmt.Printf("  %-50s %s left\n", k, humanDur(time.Duration(v.Cooldowns[k])*time.Second))
	}
	return nil
}

func lower(a []string) []string {
	out := make([]string, 0, len(a))
	for _, s := range a {
		for _, p := range strings.Split(s, ",") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func union(a, b []string) []string {
	out := append([]string{}, a...)
	for _, s := range b {
		if !containsFold(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func minus(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !containsFold(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
