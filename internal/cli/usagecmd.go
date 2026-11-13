package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/ledger"
	"github.com/silverx/freellm-router/internal/usage"
)

var usageOpts struct {
	models  bool
	all     bool
	offline bool
	watch   time.Duration
	file    string
}

var usageCmd = &cobra.Command{
	Use:     "usage",
	Aliases: []string{"limits", "quota"},
	Short:   "Current usage & limits across every provider and model",
	Long: `Show how much of each provider you have used and how much is left:
requests and tokens over the last minute / hour / 24 hours / today, the
limits you declared, what the provider itself last reported in its
x-ratelimit-* headers (and when), live cooldowns, and status
(ok | cooldown | limit | disabled | unavailable | no key).

With a running server (serve, wrap, or --url) the report is live:
cooldowns, availability and operator switches included. Without one it
is computed from the ledger file alone.

Limits are never guessed — free tiers change too often. Declare the ones
you know in the config and they are tracked (and, with
control.enforce_limits / profile auto|control, enforced as a budget):

  limits:
    groq:       {rpm: 30, rpd: 14400}
    openrouter: {rpm: 20, rpd: 50}

Examples:
  freellm-router usage                 # providers you used or limited
  freellm-router usage --all --models  # every provider, per-model rows
  freellm-router usage --watch 2s      # live refresh
  freellm-router limits --json         # machine-readable`,
	RunE: runUsage,
}

func init() {
	usageCmd.Flags().BoolVar(&usageOpts.models, "models", false, "show per-model rows under each provider")
	usageCmd.Flags().BoolVarP(&usageOpts.all, "all", "a", false, "show every configured provider (default: used, limited or blocked ones)")
	usageCmd.Flags().BoolVar(&usageOpts.offline, "offline", false, "do not ask a running server; read the ledger file only")
	usageCmd.Flags().DurationVarP(&usageOpts.watch, "watch", "w", 0, "refresh every interval (e.g. 2s) until Ctrl-C")
	usageCmd.Flags().StringVar(&usageOpts.file, "file", "", "ledger file to read (default: config ledger.file or the default location)")
	rootCmd.AddCommand(usageCmd)
}

// fetchUsage returns the live report from a server, else an offline one.
func fetchUsage(cfg *config.Config, offline bool, file string) (*usage.Report, error) {
	if !offline {
		c := newAPIClient(cfg)
		var rep usage.Report
		if err := c.get("/v1/usage", &rep); err == nil {
			return &rep, nil
		}
	}
	path := file
	if path == "" {
		path = ledgerPathFor(cfg)
	}
	events, err := ledger.ReadFile(path, cfg.Ledger.Keep)
	if err != nil {
		return nil, err
	}
	rep := usage.Build(time.Now(), usage.ProvidersFromConfig(cfg, keyCounter(cfg)), events, nil)
	rep.Source = path
	return rep, nil
}

func runUsage(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if usageOpts.watch <= 0 {
		rep, err := fetchUsage(cfg, usageOpts.offline, usageOpts.file)
		if err != nil {
			return err
		}
		if pJSON {
			printJSON(rep)
			return nil
		}
		printUsage(os.Stdout, rep, usageOpts.all, usageOpts.models)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for {
		rep, err := fetchUsage(cfg, usageOpts.offline, usageOpts.file)
		fmt.Print("\033[H\033[2J")
		if err != nil {
			fmt.Println("error:", err)
		} else {
			printUsage(os.Stdout, rep, usageOpts.all, usageOpts.models)
		}
		fmt.Printf("\n(refresh %s — Ctrl-C to quit)\n", usageOpts.watch)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(usageOpts.watch):
		}
	}
}

// interesting reports whether a provider row is worth showing by default.
func interesting(p *usage.ProviderRow) bool {
	return p.Total.Requests > 0 || p.Total.Errors > 0 || !p.Limits.IsZero() || len(p.Cooldowns) > 0 ||
		p.Status == "limit" || p.Status == "disabled" || p.RateLimit != nil
}

func printUsage(w io.Writer, rep *usage.Report, all, models bool) {
	src := "offline (ledger " + rep.Source + ")"
	if rep.Live {
		src = fmt.Sprintf("live server, up %s, %d in flight", humanDur(time.Since(rep.Started).Round(time.Second)), rep.InFlight)
	}
	fmt.Fprintf(w, "freellm-router usage & limits — %s\n", src)
	if !rep.Since.IsZero() {
		fmt.Fprintf(w, "history since %s · ", rep.Since.Local().Format("2006-01-02 15:04"))
	}
	rq := rep.Requests
	fmt.Fprintf(w, "requests: today %d (%d failed) · 24h %d · total %d · upstream calls 24h %d, tokens 24h %s\n\n",
		rq.Today.Requests, rq.Today.Errors, rq.Day.Requests, rq.Total.Requests,
		rep.Totals.Day.Requests, compact(rep.Totals.Day.Tokens))

	tw := tabwriter.NewWriter(w, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tSTATUS\t1m req/RPM\t24h req/RPD\t24h tok/TPD\tERR 24h\tUPSTREAM SAYS\tLAST USED\tLAT")
	shown, hidden := 0, 0
	for i := range rep.Providers {
		p := &rep.Providers[i]
		if !all && !interesting(p) {
			hidden++
			continue
		}
		shown++
		status := p.Status
		if p.Reason != "" && p.Status != "ok" {
			status += " (" + ledger.Clip(p.Reason, 40) + ")"
		}
		lat := "-"
		if p.AvgLatencyMS > 0 {
			lat = humanDur(time.Duration(p.AvgLatencyMS) * time.Millisecond)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", p.Name, status,
			ratio(p.Minute.Requests, p.Limits.RPM), ratio(p.Day.Requests, p.Limits.RPD),
			ratioTok(p.Day.Tokens, p.Limits.TPD), p.Day.Errors, upstreamSays(p), ago(p.LastUsed), lat)
		for _, cd := range p.Cooldowns {
			fmt.Fprintf(tw, "  ↳ cooldown %s\t%s left\t\t\t\t\t\t\t\n", cd.Via, humanDur(time.Duration(cd.RemainingS*float64(time.Second))))
		}
		if models {
			for _, m := range p.Models {
				fmt.Fprintf(tw, "  · %s\t%s\t\t%d req\t%s tok\t%d\t\t%s\t%s\n", strings.TrimPrefix(m.Via, p.Name+"/"),
					statusCode(m.LastStatus), m.Day.Requests, compact(m.Day.Tokens), m.Day.Errors, ago(m.LastUsed),
					humanDur(time.Duration(m.AvgLatencyMS)*time.Millisecond))
			}
		}
	}
	tw.Flush()
	if shown == 0 {
		fmt.Fprintln(w, "(no usage recorded yet — route a call with chat/exec/serve, or use --all)")
	}
	if hidden > 0 {
		fmt.Fprintf(w, "\n%d unused providers hidden (--all to show).", hidden)
	}
	fmt.Fprintln(w, "\nwindows are rolling; limits come from your config (limits:), \"upstream says\" from the provider's own x-ratelimit headers.")
}

func ratio(used, limit int) string {
	if limit <= 0 {
		return strconv.Itoa(used)
	}
	return fmt.Sprintf("%d/%d %s", used, limit, bar(used, limit))
}

func ratioTok(used, limit int) string {
	if limit <= 0 {
		return compact(used)
	}
	return compact(used) + "/" + compact(limit) + " " + bar(used, limit)
}

// bar renders a 5-cell usage gauge.
func bar(used, limit int) string {
	if limit <= 0 {
		return ""
	}
	n := min(used*5/limit, 5)
	return "[" + strings.Repeat("#", n) + strings.Repeat(".", 5-n) + "]"
}

func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

func statusCode(s int) string {
	if s == 0 {
		return "-"
	}
	return strconv.Itoa(s)
}

// upstreamSays summarizes the provider's own last quota snapshot.
func upstreamSays(p *usage.ProviderRow) string {
	rl := p.RateLimit
	if rl == nil {
		return "-"
	}
	var parts []string
	if rl.RemainingRequests != nil {
		s := strconv.FormatInt(*rl.RemainingRequests, 10)
		if rl.LimitRequests != nil {
			s += "/" + strconv.FormatInt(*rl.LimitRequests, 10)
		}
		parts = append(parts, s+" req")
	}
	if rl.RemainingTokens != nil {
		s := compact(int(*rl.RemainingTokens))
		if rl.LimitTokens != nil {
			s += "/" + compact(int(*rl.LimitTokens))
		}
		parts = append(parts, s+" tok")
	}
	if rl.RetryAfter != "" {
		parts = append(parts, "retry "+rl.RetryAfter+"s")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ") + " (" + ago(p.RateLimitAt) + ")"
}
