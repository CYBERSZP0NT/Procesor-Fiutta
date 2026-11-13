package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
	"github.com/CYBERSZP0NT/szmelc-router/internal/obs"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

// pURL is the --url persistent flag (running server to talk to).
var pURL string

// loadConfig loads the config (shared error wording).
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(pCfgPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// ledgerPathFor returns the ledger file the CLI reads and writes.
func ledgerPathFor(cfg *config.Config) string {
	if cfg == nil {
		return ledger.DefaultPath()
	}
	return cfg.LedgerPath(ledger.DefaultPath())
}

// serverURL returns the base URL (no trailing slash, no /v1) of the server
// to talk to: --url, $FREELLM_URL, else the configured host:port on
// loopback.
func serverURL(cfg *config.Config) string {
	u := pURL
	if u == "" {
		u = os.Getenv("FREELLM_URL")
	}
	if u == "" {
		host, port := "127.0.0.1", 8787
		if cfg != nil {
			if cfg.Server.Port != 0 {
				port = cfg.Server.Port
			}
			if h := cfg.Server.Host; h != "" && h != "0.0.0.0" && h != "::" {
				host = h
			}
		}
		u = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	}
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, "/v1")
	return u
}

// apiClient talks to a running szmelc-router server.
type apiClient struct {
	base string
	key  string
	hc   *http.Client
}

func newAPIClient(cfg *config.Config) *apiClient {
	key := ownAPIKey()
	if key == "" && cfg != nil && len(cfg.Server.Auth.Keys) > 0 {
		key = cfg.Server.Auth.Keys[0]
	}
	return &apiClient{base: serverURL(cfg), key: key, hc: &http.Client{Timeout: 10 * time.Second}}
}

// ping reports whether the server answers /v1/ping quickly.
func (c *apiClient) ping() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	var out map[string]any
	return c.do(ctx, http.MethodGet, "/v1/ping", nil, &out) == nil
}

func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%s %s: %s", method, path, e.Error.Message)
		}
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *apiClient) get(path string, out any) error {
	return c.do(context.Background(), http.MethodGet, path, nil, out)
}

// keyCounter counts configured API keys per provider without building a
// router (no plugin processes are started).
func keyCounter(cfg *config.Config) func(string) int {
	src := core.NewEnvKeySource(cfg.KeyEnvMap())
	return func(p string) int { return len(src.Keys(context.Background(), strings.ToLower(p))) }
}

// openRuntime builds a router + runtime for an in-process call (chat, exec).
func openRuntime(cfg *config.Config, source string) (*router.Router, *obs.Runtime, error) {
	r, err := buildRouter(cfg, globalTimeout())
	if err != nil {
		return nil, nil, fmt.Errorf("router: %w", err)
	}
	// Seeding parses the ledger; a one-shot call only needs it to enforce
	// declared quotas.
	rt := obs.Open(cfg, obs.Options{Source: source, Seed: hasLimits(cfg), DefaultPath: ledger.DefaultPath()})
	rt.Attach(r)
	return r, rt, nil
}

// lastUser returns the last user message's content.
func lastUser(msgs []router.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// routeLogged routes one call and records it in the ledger.
func routeLogged(ctx context.Context, r *router.Router, rt *obs.Runtime, session, selector string,
	msgs []router.Message, opts *router.RouteOpts, sink func(string)) (string, *router.Done, error) {
	var answer strings.Builder
	wrapped := sink
	if sink != nil {
		wrapped = func(s string) { answer.WriteString(s); sink(s) }
	}
	ctx, call := rt.Begin(ctx, session, selector, lastUser(msgs), sink != nil)
	via, done, err := r.RouteWithOptions(ctx, selector, msgs, "", opts, wrapped)
	p, c := 0, 0
	if done != nil {
		p, c = done.PromptTokens, done.CompletionTokens
		if done.Text != "" {
			answer.WriteString(done.Text)
		}
	}
	rt.End(call, via, p, c, answer.String(), err)
	return via, done, err
}

// humanDur formats d compactly (1.2s, 3m4s, 2h3m, 4d2h).
func humanDur(d time.Duration) string {
	switch {
	case d < 0:
		return "-" + humanDur(-d)
	case d == 0:
		return "0s"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

// ago formats the time since t ("-" for zero).
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	if time.Since(t) < time.Second {
		return "just now"
	}
	return humanDur(time.Since(t).Round(time.Second)) + " ago"
}

// hasLimits reports whether any provider quota is declared.
func hasLimits(cfg *config.Config) bool {
	if len(cfg.Limits) > 0 {
		return true
	}
	for _, p := range cfg.Providers {
		if p.Limits != nil && !p.Limits.IsZero() {
			return true
		}
	}
	return false
}
