package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/journal"
	"github.com/silverx/szmelc-router/internal/ledger"
)

var statsOptsVar struct {
	date string
	file string
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Aggregate the usage journal (top providers, error rates, per-key usage)",
	Long: `Aggregate the structured usage journal (append-only JSONL, one line per
request: time, route, via, tokens, latency, status).

By default reads the same journal the HTTP server writes
($XDG_DATA_HOME/szmelc/usage.jsonl, or server.usage_file from the config).

Examples:
  szmelc-router stats                    # all-time summary
  szmelc-router stats --date 2025-07-14  # one day
  szmelc-router stats --json             # machine-readable
  szmelc-router stats --file /var/log/freellm/usage.jsonl`,
	RunE: runStats,
}

func init() {
	statsCmd.Flags().StringVar(&statsOptsVar.date, "date", "", "restrict to one day (YYYY-MM-DD, local time)")
	statsCmd.Flags().StringVar(&statsOptsVar.file, "file", "", "journal file to read (default: server usage journal)")
}

// Stats is the aggregate output.
type Stats struct {
	Date            string             `json:"date,omitempty"`
	Total           int                `json:"total"`
	Errors          int                `json:"errors"`
	Streaming       int                `json:"streaming"`
	AvgLatencyMS    int64              `json:"avg_latency_ms"`
	StatusCounts    map[string]int     `json:"status_counts,omitempty"`
	Providers       map[string]ProvAgg `json:"providers,omitempty"`
	Keys            map[string]KeyAgg  `json:"keys,omitempty"`
	TotalPrompt     int                `json:"total_prompt_tokens"`
	TotalCompletion int                `json:"total_completion_tokens"`
}

// ProvAgg aggregates per "provider" (the provider part of via).
type ProvAgg struct {
	Requests int   `json:"requests"`
	Errors   int   `json:"errors"`
	Stream   int   `json:"streaming"`
	Latency  int64 `json:"total_latency_ms"`
}

// KeyAgg aggregates per (redacted) key.
type KeyAgg struct {
	Requests   int `json:"requests"`
	Errors     int `json:"errors"`
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
}

func runStats(cmd *cobra.Command, args []string) error {
	path := statsOptsVar.file
	if path == "" {
		cfg, _ := config.Load(pCfgPath)
		path = ledgerPathFor(cfg)
	}
	return printStats(path, statsOptsVar.date)
}

// defaultJournalPath mirrors the server's default journal location.
func defaultJournalPath() string { return ledger.DefaultPath() }

// collectStats reads `path` (+ rotated files) and aggregates the request
// records (attempts, task decisions and other ledger events are skipped).
func collectStats(path, day string) (*Stats, error) {
	events, err := ledger.ReadFile(path, journal.DefaultKeep)
	if err != nil {
		return nil, err
	}
	s := &Stats{
		StatusCounts: map[string]int{},
		Providers:    map[string]ProvAgg{},
		Keys:         map[string]KeyAgg{},
	}
	var latTotal int64
	for i := range events {
		rec := &events[i]
		if rec.EffectiveKind() != ledger.KindRequest {
			continue
		}
		if day != "" && rec.T.Format("2006-01-02") != day {
			continue
		}
		s.Total++
		if rec.Stream {
			s.Streaming++
		}
		latTotal += rec.LatencyMS
		failed := rec.Failed()
		if failed {
			s.Errors++
			s.StatusCounts[fmt.Sprintf("%d", rec.Status)]++
		}
		if prov := rec.ProviderName(); prov != "" {
			agg := s.Providers[prov]
			agg.Requests++
			if failed {
				agg.Errors++
			}
			agg.Stream += boolInt(rec.Stream)
			agg.Latency += rec.LatencyMS
			s.Providers[prov] = agg
		}
		var prompt, completion int
		if rec.Tokens != nil {
			prompt, completion = max(rec.Tokens.Prompt, 0), max(rec.Tokens.Completion, 0)
		}
		s.TotalPrompt += prompt
		s.TotalCompletion += completion
		if rec.Key != "" {
			agg := s.Keys[rec.Key]
			agg.Requests++
			if failed {
				agg.Errors++
			}
			agg.Prompt += prompt
			agg.Completion += completion
			s.Keys[rec.Key] = agg
		}
	}
	if s.Total > 0 {
		s.AvgLatencyMS = latTotal / int64(s.Total)
	}
	if day != "" {
		s.Date = day
	}
	return s, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func printStats(path, day string) error {
	s, err := collectStats(path, day)
	if err != nil {
		return err
	}
	if s.Total == 0 {
		fmt.Println("no usage records found at " + path)
		return nil
	}
	if pJSON {
		printJSON(s)
		return nil
	}

	dateLabel := "all time"
	if day != "" {
		dateLabel = day
	}
	fmt.Printf("szmelc-router usage stats (%s, %s)\n", path, dateLabel)
	fmt.Printf("  total:      %d requests (%d streaming), %d errors (%.1f%%), avg latency %dms\n",
		s.Total, s.Streaming, s.Errors, pct(s.Errors, s.Total), s.AvgLatencyMS)
	if len(s.StatusCounts) > 0 {
		fmt.Printf("  errors by status: %s\n", statusLine(s.StatusCounts))
	}
	if len(s.Providers) > 0 {
		fmt.Println("  top providers:")
		w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
		fmt.Fprintln(w, "    provider\trequests\terrors\tstreaming")
		for _, name := range topProviders(s.Providers, 10) {
			a := s.Providers[name]
			fmt.Fprintf(w, "    %s\t%d\t%d\t%d\n", name, a.Requests, a.Errors, a.Stream)
		}
		w.Flush()
	}
	if len(s.Keys) > 0 {
		fmt.Println("  per-key consumption:")
		w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
		fmt.Fprintln(w, "    key\trequests\tprompt_tok\tcompletion_tok")
		for _, k := range topKeys2(s.Keys, 10) {
			a := s.Keys[k]
			fmt.Fprintf(w, "    %s\t%d\t%d\t%d\n", k, a.Requests, a.Prompt, a.Completion)
		}
		w.Flush()
	}
	if s.TotalPrompt > 0 || s.TotalCompletion > 0 {
		fmt.Printf("  tokens:     %d prompt + %d completion\n", s.TotalPrompt, s.TotalCompletion)
	}
	return nil
}

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(part) / float64(total)
}

func statusLine(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

type provEntry = struct{ name string }

func topProviders(m map[string]ProvAgg, n int) []string {
	type kv struct {
		k string
		v int
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k, v.Requests + v.Errors})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.k)
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func topKeys2(m map[string]KeyAgg, n int) []string {
	type kv struct {
		k string
		v int
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k, v.Requests})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.k)
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}
