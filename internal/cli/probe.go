package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

type probeOpts struct {
	concurrency int
	proTimeout  int
}

var probeOptsVar = &probeOpts{}

var probeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Ping every configured provider with a 'pong' prompt",
	Long: `Probe (provider, chain member) pairs in parallel with a minimal
"pong" prompt. Reports ok/fail status, latency, and a response preview.

Filters: --provider to test a single provider, --model to test a
specific chain member.

Examples:
  szmelc-router probe
  szmelc-router probe --provider groq
  szmelc-router probe --concurrency 4 --timeout 10
  szmelc-router probe --json`,
	RunE: runProbe,
}

func init() {
	probeCmd.Flags().IntVar(&probeOptsVar.concurrency, "concurrency", 8, "max parallel probes")
	probeCmd.Flags().IntVar(&probeOptsVar.proTimeout, "pro-timeout", 15, "per-probe timeout (seconds)")
}

type probeResult struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	OK       bool          `json:"ok"`
	Reason   string        `json:"reason,omitempty"`
	Latency  time.Duration `json:"latency,omitempty"`
	Preview  string        `json:"preview,omitempty"`
}

func runProbe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(pCfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	r, err := buildRouter(cfg, globalTimeout())
	if err != nil {
		return fmt.Errorf("router: %w", err)
	}

	// Build the probe target list from the "auto" route (or the --model route).
	routeAlias := pModel
	if routeAlias == "" {
		routeAlias = "auto"
	}

	chain := cfg.RouteChain(routeAlias)
	if chain == nil {
		// Fall back to all providers with a model from their auto chain position.
		for _, p := range cfg.Providers {
			if p.BaseURL != "" {
				chain = append(chain, p.Name+"/probe-model")
			}
		}
	}

	// Filter by --provider.
	if pProvider != "" {
		var filtered []string
		for _, c := range chain {
			if strings.HasPrefix(strings.ToLower(c), strings.ToLower(pProvider)+"/") {
				filtered = append(filtered, c)
			}
		}
		chain = filtered
	}

	// If still empty, try each provider individually.
	if len(chain) == 0 {
		for _, p := range cfg.Providers {
			if p.BaseURL == "" {
				continue
			}
			if pProvider != "" && !strings.EqualFold(p.Name, pProvider) {
				continue
			}
			chain = append(chain, p.Name+"/test")
		}
	}

	probeTimeout := time.Duration(probeOptsVar.proTimeout) * time.Second
	concurrency := probeOptsVar.concurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	messages := []router.Message{{Role: "user", Content: "pong"}}

	var (
		mu      sync.Mutex
		results []probeResult
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, entry := range chain {
		entry := entry
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			pname, mid := parseChainEntry(entry)
			ctx, cancel := context.WithTimeout(cmd.Context(), probeTimeout)
			defer cancel()

			start := time.Now()
			_, done, err := r.RouteWithOptions(ctx, entry, messages, "", nil, nil)
			latency := time.Since(start)

			res := probeResult{
				Provider: pname,
				Model:    mid,
				Latency:  latency,
			}

			if err != nil {
				res.OK = false
				res.Reason = truncate(err.Error(), 120)
			} else {
				res.OK = true
				text := done.Text
				if len(text) > 60 {
					text = text[:60] + "…"
				}
				res.Preview = text
				res.Reason = "ok"
			}

			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}()
	}

	wg.Wait()

	if pJSON {
		printJSON(results)
		return nil
	}

	// Table output.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  STATUS    LATENCY    PROVIDER/MODEL\n")
	fmt.Fprintf(w, "  %-7s  %-8s  %s\n", "-------", "--------", "----------")
	for _, res := range results {
		status := "✓ ok"
		if !res.OK {
			status = "✗ FAIL"
		}
		preview := res.Preview
		if preview == "" {
			preview = res.Reason
		}
		fmt.Fprintf(w, "  %-7s  %-8s  %s/%s  %s\n", status, res.Latency.Round(time.Millisecond).String(), res.Provider, res.Model, preview)
	}
	w.Flush()

	fmt.Println()
	okCount := 0
	for _, r := range results {
		if r.OK {
			okCount++
		}
	}
	fmt.Printf("  %d/%d probes ok\n", okCount, len(results))
	return nil
}

func parseChainEntry(entry string) (string, string) {
	idx := strings.Index(entry, "/")
	if idx <= 0 {
		return entry, ""
	}
	return entry[:idx], entry[idx+1:]
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
