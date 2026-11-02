package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/journal"
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
($XDG_DATA_HOME/freellm/usage.jsonl, or server.usage_file from the config).

Examples:
  freellm-router stats                    # all-time summary
  freellm-router stats --date 2025-07-14  # one day
  freellm-router stats --json             # machine-readable
  freellm-router stats --file /var/log/freellm/usage.jsonl`,
	RunE: runStats,
}

func init() {
	statsCmd.Flags().StringVar(&statsOptsVar.date, "date", "", "restrict to one day (YYYY-MM-DD, local time)")
	statsCmd.Flags().StringVar(&statsOptsVar.file, "file", "", "journal file to read (default: server usage journal)")
}

// journalRecord is the subset of the server's UsageRecord the stats need.
type journalRecord struct {
	T         time.Time `json:"t"`
	Status    int       `json:"status"`
	Key       string    `json:"key"`
	LatencyMS int64     `json:"latency_ms"`
	Model     string    `json:"model"`
	Via       string    `json:"via"`
	Route     string    `json:"route"`
	Stream    bool      `json:"stream"`
	Err       string    `json:"error"`
	Tokens    *struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
	} `json:"tokens"`
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
		if cfg, err := config.Load(pCfgPath); err == nil && cfg.Server.UsageFile != "" {
			path = cfg.Server.UsageFile
		} else {
			path = defaultJournalPath()
		}
	}
	return printStats(path, statsOptsVar.date)
}

// defaultJournalPath mirrors the server's default journal location.
func defaultJournalPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "freellm", "usage.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".local", "share", "freellm", "usage.jsonl")
	}
	return filepath.Join(home, ".local", "share", "freellm", "usage.jsonl")
}

// parseErr is reserved for structured parse errors (future).
type parseErr struct {
	line int
	err  error
}

var _ = parseErr{}

// collectStats reads `path` (+ rotated files) and aggregates.
func collectStats(path, day string) (*Stats, error) {
	lines, err := journal.ReadAll(path, journal.DefaultKeep)
	if err != nil {
		return nil, err
	}
	s := &Stats{
		StatusCounts: map[string]int{},
		Providers:    map[string]ProvAgg{},
		Keys:         map[string]KeyAgg{},
	}
	var latTotal int64
	for i, raw := range lines {
		var rec journalRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue // tolerate partially-written or foreign lines
		}
		if day != "" {
			if rec.T.Format("2006-01-02") != day {
				continue
			}
		}
		s.Total++
		if rec.Stream {
			s.Streaming++
		}
		latTotal += rec.LatencyMS
		if rec.Status >= 400 {
			s.Errors++
			s.StatusCounts[fmt.Sprintf("%d", rec.Status)]++
		}
		if rec.Via != "" {
			prov := rec.Via[:strings.IndexByte(rec.Via, '/')]
			if prov == "" {
				prov = rec.Via
			}
			agg := s.Providers[prov]
			agg.Requests++
			if rec.Status >= 400 {
				agg.Errors++
			}
			agg.Stream += boolInt(rec.Stream)
			agg.Latency += rec.LatencyMS
			s.Providers[prov] = agg
		}
		if rec.Key != "" {
			k := rec.Key
			agg := s.Keys[k]
			agg.Requests++
			if rec.Status >= 400 {
				agg.Errors++
			}
			if rec.Tokens != nil {
				agg.Prompt += rec.Tokens.Prompt
				agg.Completion += rec.Tokens.Completion
			}
			s.Keys[k] = agg
			s.TotalPrompt += rec.Tokens.Prompt
			s.TotalCompletion += rec.Tokens.Completion
		} else if rec.Tokens != nil {
			s.TotalPrompt += rec.Tokens.Prompt
			s.TotalCompletion += rec.Tokens.Completion
		}
		_ = i
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
	fmt.Printf("freellm-router usage stats (%s, %s)\n", path, dateLabel)
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
