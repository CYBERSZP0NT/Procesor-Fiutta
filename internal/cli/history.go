package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
)

var histOpts struct {
	since, until string
	kinds        []string
	session      string
	provider     string
	id           string
	source       string
	errors       bool
	grep         string
	limit        int
	follow       bool
	file         string
	wide         bool
}

var historyCmd = &cobra.Command{
	Use:     "history",
	Aliases: []string{"log", "logs"},
	Short:   "Full history: every request, upstream call, task decision, session and control change",
	Long: `Browse the event ledger — one line per event, oldest first:

  request  a client call (server, chat, exec, wrap) with its outcome
  attempt  every upstream call made while serving a request (status,
           latency, tokens, x-ratelimit snapshot, cooldown applied)
  task     a task-router decision (mode, role, tier, chain, reason)
  session  a session started/ended (wrap, tui, exec --session)
  control  an operator change (profile, pause, disabled providers, …)
  http     other HTTP calls to the server (models, route, …)

The ledger is the JSONL file the server and every CLI process append to
(default ~/.local/share/szmelc/usage.jsonl; config ledger.file).

Examples:
  szmelc-router history                         # last 50 events
  szmelc-router log -f                          # follow live
  szmelc-router history --since 2h --errors     # failures, last 2 hours
  szmelc-router history --kind request,attempt --provider groq
  szmelc-router history --session build-42 -n 0 # one whole session
  szmelc-router history show 3f9a               # a request + its attempts
  szmelc-router history sessions                # per-session summaries
  szmelc-router history export --format csv > all.csv
  szmelc-router --json history --since today    # JSONL`,
	RunE: runHistory,
}

var historySessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Per-session summaries (requests, errors, tokens, providers)",
	RunE:  runHistorySessions,
}

var historyShowCmd = &cobra.Command{
	Use:   "show <request-id>",
	Short: "One request with every attempt and task decision behind it",
	Args:  cobra.ExactArgs(1),
	RunE:  runHistoryShow,
}

var historyExportOpts struct {
	format string
	out    string
}

var historyExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export the (filtered) history as jsonl, json or csv",
	RunE:  runHistoryExport,
}

var historyPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the ledger file path",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(ledgerFile())
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{historyCmd, historySessionsCmd, historyExportCmd} {
		f := c.Flags()
		f.StringVar(&histOpts.since, "since", "", "only events after (30m, 2h, 7d, today, 2026-11-01)")
		f.StringVar(&histOpts.until, "until", "", "only events before (same formats)")
		f.StringVar(&histOpts.file, "file", "", "ledger file (default: config ledger.file or the default location)")
		f.StringVar(&histOpts.session, "session", "", "only this session")
		f.StringVar(&histOpts.provider, "provider", "", "only this provider (or provider/model prefix)")
		f.StringVar(&histOpts.source, "source", "", "only from this source (server | cli | exec | wrap | tui)")
		f.BoolVarP(&histOpts.errors, "errors", "e", false, "only failures")
		f.StringVarP(&histOpts.grep, "grep", "g", "", "case-insensitive text search over the whole event")
		f.StringSliceVarP(&histOpts.kinds, "kind", "k", nil, "event kinds (request,attempt,task,session,control,http)")
	}
	historyCmd.Flags().IntVarP(&histOpts.limit, "limit", "n", 50, "show the last N matching events (0 = all)")
	historyCmd.Flags().BoolVarP(&histOpts.follow, "follow", "f", false, "keep printing new events as they happen")
	historyCmd.Flags().BoolVar(&histOpts.wide, "wide", false, "show prompt/answer previews and error text in full")
	historyCmd.Flags().StringVar(&histOpts.id, "id", "", "only this request id (prefix)")
	historySessionsCmd.Flags().IntVarP(&histOpts.limit, "limit", "n", 30, "show the N most recent sessions (0 = all)")
	historyExportCmd.Flags().StringVar(&historyExportOpts.format, "format", "jsonl", "jsonl | json | csv")
	historyExportCmd.Flags().StringVarP(&historyExportOpts.out, "output", "o", "", "write to file (default stdout)")
	historyShowCmd.Flags().StringVar(&histOpts.file, "file", "", "ledger file")
	historyCmd.AddCommand(historySessionsCmd, historyShowCmd, historyExportCmd, historyPathCmd)
	rootCmd.AddCommand(historyCmd)
}

func ledgerFile() string {
	if histOpts.file != "" {
		return histOpts.file
	}
	cfg, _ := loadConfig()
	return ledgerPathFor(cfg)
}

func historyFilter() (*ledger.Filter, error) {
	now := time.Now()
	f := &ledger.Filter{
		Kinds: histOpts.kinds, Session: histOpts.session, Provider: histOpts.provider,
		ID: histOpts.id, Source: histOpts.source, Errors: histOpts.errors, Query: histOpts.grep,
		Limit: histOpts.limit,
	}
	var err error
	if f.Since, err = ledger.ParseSince(histOpts.since, now); err != nil {
		return nil, err
	}
	if histOpts.until != "" {
		if f.Until, err = ledger.ParseSince(histOpts.until, now); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func readHistory() ([]ledger.Event, string, error) {
	path := ledgerFile()
	evs, err := ledger.ReadFile(path, 0)
	return evs, path, err
}

func runHistory(cmd *cobra.Command, args []string) error {
	f, err := historyFilter()
	if err != nil {
		return err
	}
	events, path, err := readHistory()
	if err != nil {
		return err
	}
	shown := f.Apply(events)
	out := os.Stdout
	if len(shown) == 0 && !histOpts.follow && !pJSON {
		fmt.Fprintf(out, "no matching events in %s\n", path)
		return nil
	}
	for i := range shown {
		printEvent(out, &shown[i])
	}
	if !histOpts.follow {
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	live := *f
	live.Limit = 0
	return ledger.Follow(ctx, path, &live, func(e ledger.Event) { printEvent(out, &e) })
}

// kindTag is the fixed-width label of an event kind.
func kindTag(k string) string {
	switch k {
	case ledger.KindRequest:
		return "REQ "
	case ledger.KindAttempt:
		return "  ↳ "
	case ledger.KindTask:
		return "TASK"
	case ledger.KindSession:
		return "SESS"
	case ledger.KindControl:
		return "CTL "
	}
	return "HTTP"
}

func printEvent(w io.Writer, e *ledger.Event) {
	if pJSON {
		_, _ = w.Write(ledger.Marshal(*e))
		return
	}
	k := e.EffectiveKind()
	mark := "✓"
	if e.Failed() {
		mark = "✗"
	}
	ts := e.T.Local().Format("01-02 15:04:05")
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s ", ts, kindTag(k))
	clip := func(s string, n int) string {
		if histOpts.wide {
			return strings.ReplaceAll(s, "\n", " ")
		}
		return ledger.Clip(strings.ReplaceAll(s, "\n", " "), n)
	}
	switch k {
	case ledger.KindRequest:
		fmt.Fprintf(&b, "%s %3d %s", mark, e.Status, e.Model)
		if e.Via != "" {
			fmt.Fprintf(&b, " → %s", e.Via)
		}
		fmt.Fprintf(&b, "  %s", humanDur(time.Duration(e.LatencyMS)*time.Millisecond))
		if t := e.Tokens.Total(); t > 0 {
			fmt.Fprintf(&b, "  %s tok", compact(t))
		}
		if e.Attempts > 1 {
			fmt.Fprintf(&b, "  %d tries", e.Attempts)
		}
		if e.Rounds > 1 {
			fmt.Fprintf(&b, "  %d rounds/%s waited", e.Rounds, humanDur(time.Duration(e.Waited*float64(time.Second))))
		}
		if e.Prompt != "" {
			fmt.Fprintf(&b, "  “%s”", clip(e.Prompt, 50))
		}
		if e.Err != "" {
			fmt.Fprintf(&b, "  err: %s", clip(e.Err, 90))
		}
	case ledger.KindAttempt:
		fmt.Fprintf(&b, "%s %3d #%d %s  %s", mark, e.Status, e.Attempt, e.Via, humanDur(time.Duration(e.LatencyMS)*time.Millisecond))
		if t := e.Tokens.Total(); t > 0 {
			fmt.Fprintf(&b, "  %s tok", compact(t))
		}
		if rl := e.RateLimit; rl != nil && rl.RemainingRequests != nil {
			fmt.Fprintf(&b, "  quota left %d", *rl.RemainingRequests)
		}
		if e.CooldownS > 0 {
			fmt.Fprintf(&b, "  cooldown %s", humanDur(time.Duration(e.CooldownS*float64(time.Second))))
		}
		if e.Err != "" {
			fmt.Fprintf(&b, "  %s", clip(e.Err, 90))
		}
	case ledger.KindTask:
		if t := e.Task; t != nil {
			fmt.Fprintf(&b, "%s role=%s tier=%d → %s", t.Mode, t.Role, t.Tier, clip(strings.Join(t.Chain, ", "), 80))
		}
	case ledger.KindSession:
		fmt.Fprintf(&b, "%s %s", e.Action, e.Session)
		if e.Note != "" {
			fmt.Fprintf(&b, "  %s", clip(e.Note, 80))
		}
	case ledger.KindControl:
		fmt.Fprintf(&b, "%s %s", e.Action, e.Note)
	default:
		fmt.Fprintf(&b, "%s %3d %s %s  %s", mark, e.Status, e.Method, e.Path, humanDur(time.Duration(e.LatencyMS)*time.Millisecond))
	}
	if e.ID != "" && k != ledger.KindSession && k != ledger.KindControl {
		fmt.Fprintf(&b, "  [%s", shortID(e.ID))
		if e.Session != "" {
			fmt.Fprintf(&b, " · %s", e.Session)
		}
		b.WriteString("]")
	} else if e.Session != "" && k != ledger.KindSession {
		fmt.Fprintf(&b, "  [%s]", e.Session)
	}
	fmt.Fprintln(w, b.String())
}

// shortID trims the server's "rid-" prefix and long ids for display.
func shortID(id string) string {
	id = strings.TrimPrefix(id, "rid-")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func runHistorySessions(cmd *cobra.Command, args []string) error {
	f, err := historyFilter()
	if err != nil {
		return err
	}
	f.Limit = 0
	events, path, err := readHistory()
	if err != nil {
		return err
	}
	sess := ledger.Sessions(f.Apply(events))
	if histOpts.limit > 0 && len(sess) > histOpts.limit {
		sess = sess[:histOpts.limit]
	}
	if pJSON {
		printJSON(sess)
		return nil
	}
	if len(sess) == 0 {
		fmt.Printf("no sessions in %s (start one: szmelc-router wrap --session NAME -- <cmd>)\n", path)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSOURCE\tSTARTED\tLAST\tSTATE\tREQ\tERR\tCALLS\tTOKENS\tPROVIDERS")
	for _, s := range sess {
		state := "open"
		if s.Ended {
			state = "ended"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", s.ID, s.Source,
			s.Start.Local().Format("01-02 15:04"), ago(s.Last), state, s.Requests, s.Errors,
			s.Attempts, compact(s.Tokens), providerList(s.Providers))
	}
	return tw.Flush()
}

func providerList(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+":"+strconv.Itoa(v))
	}
	sortStrings(parts)
	return strings.Join(parts, " ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func runHistoryShow(cmd *cobra.Command, args []string) error {
	events, path, err := readHistory()
	if err != nil {
		return err
	}
	id := args[0]
	rel := ledger.Related(events, id)
	if len(rel) == 0 {
		rel = ledger.Related(events, "rid-"+id)
	}
	if len(rel) == 0 {
		return fmt.Errorf("no events for request %q in %s", id, path)
	}
	if pJSON {
		printJSON(rel)
		return nil
	}
	histOpts.wide = true
	for i := range rel {
		printEvent(os.Stdout, &rel[i])
	}
	for _, e := range rel {
		if e.EffectiveKind() != ledger.KindRequest {
			continue
		}
		fmt.Println()
		fmt.Printf("request    %s  (session %s, source %s, profile %s)\n", e.ID, dash(e.Session), dash(e.Source), dash(e.Profile))
		fmt.Printf("model      %s → %s\n", e.Model, dash(e.Via))
		if e.Task != nil {
			fmt.Printf("task       %s, role %s, tier %d\n           %s\n", e.Task.Mode, e.Task.Role, e.Task.Tier, e.Task.Reason)
		}
		for _, t := range e.Trail {
			fmt.Printf("skipped    %s\n", t)
		}
		if e.Prompt != "" {
			fmt.Printf("prompt     %s\n", e.Prompt)
		}
		if e.Answer != "" {
			fmt.Printf("answer     %s\n", e.Answer)
		}
		if e.Err != "" {
			fmt.Printf("error      %s\n", e.Err)
		}
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runHistoryExport(cmd *cobra.Command, args []string) error {
	f, err := historyFilter()
	if err != nil {
		return err
	}
	f.Limit = 0
	events, _, err := readHistory()
	if err != nil {
		return err
	}
	events = f.Apply(events)
	var w io.Writer = os.Stdout
	if historyExportOpts.out != "" {
		fh, err := os.Create(historyExportOpts.out)
		if err != nil {
			return err
		}
		defer fh.Close()
		w = fh
	}
	switch strings.ToLower(historyExportOpts.format) {
	case "jsonl", "ndjson":
		for _, e := range events {
			if _, err := w.Write(ledger.Marshal(e)); err != nil {
				return err
			}
		}
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(events)
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"time", "kind", "request_id", "session", "source", "status", "model", "via",
			"latency_ms", "prompt_tokens", "completion_tokens", "attempt", "attempts", "profile", "error"})
		for _, e := range events {
			p, c := "", ""
			if e.Tokens != nil {
				p, c = strconv.Itoa(e.Tokens.Prompt), strconv.Itoa(e.Tokens.Completion)
			}
			_ = cw.Write([]string{e.T.Format(time.RFC3339Nano), e.EffectiveKind(), e.ID, e.Session, e.Source,
				strconv.Itoa(e.Status), e.Model, e.Via, strconv.FormatInt(e.LatencyMS, 10), p, c,
				strconv.Itoa(e.Attempt), strconv.Itoa(e.Attempts), e.Profile, e.Err})
		}
		cw.Flush()
		return cw.Error()
	default:
		return fmt.Errorf("unknown format %q (jsonl | json | csv)", historyExportOpts.format)
	}
	return nil
}
