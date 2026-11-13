package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

// ---------------------------------------------------------------------------
// GET /v1/models — route aliases + per-provider live catalog
// ---------------------------------------------------------------------------

type oaModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	data := make([]oaModel, 0, 16)
	seen := make(map[string]bool, 64)

	// 1) Route aliases first (owned_by = szmelc-router).
	aliases := make([]string, 0, len(s.cfg.Routes))
	for name := range s.cfg.Routes {
		aliases = append(aliases, name)
	}
	sort.Strings(aliases)
	for _, name := range aliases {
		data = append(data, oaModel{ID: name, Object: "model", Created: now, OwnedBy: "szmelc-router"})
		seen[name] = true
	}

	// 1b) Task-router aliases ("agent", "task"): pick a model per request,
	// plus one pinned variant per mode ("agent:jako-tako", …) so UI clients
	// can choose the mode from their model picker.
	for _, name := range s.router.TaskAliases() {
		ids := []string{name}
		for _, m := range router.TaskModes() {
			ids = append(ids, name+":"+m.ID)
		}
		for _, id := range ids {
			if !seen[id] {
				data = append(data, oaModel{ID: id, Object: "model", Created: now, OwnedBy: "szmelc-router/agentic"})
				seen[id] = true
			}
		}
	}

	// 2) Per-provider live catalog, fetched in parallel (tolerant of failure).
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	type result struct {
		provider string
		models   []string
	}
	provs := make([]string, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		if p.BaseURL == "" {
			continue
		}
		provs = append(provs, p.Name)
	}

	ch := make(chan result, len(provs))
	var wg sync.WaitGroup
	for _, name := range provs {
		prov, ok := s.router.Core().Provider(name)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(name string, prov core.Provider) {
			defer wg.Done()
			models, _ := prov.Models(ctx)
			out := make([]string, 0, len(models))
			for _, m := range models {
				out = append(out, m.ID)
			}
			ch <- result{provider: name, models: out}
		}(name, prov)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	byProv := make(map[string][]string, len(provs))
	for res := range ch {
		byProv[res.provider] = res.models
	}
	for _, name := range provs { // preserve config order
		for _, id := range byProv[name] {
			if seen[id] || id == "" {
				continue
			}
			seen[id] = true
			data = append(data, oaModel{ID: id, Object: "model", Created: now, OwnedBy: name})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ---------------------------------------------------------------------------
// GET /v1/health — per-provider availability (upgraded: latency + last error)
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	providers := make([]map[string]any, 0, len(s.cfg.Providers))

	type probe struct {
		latencyMs int64
		err       string
	}
	probes := make(map[string]probe, len(s.cfg.Providers))

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, p := range s.cfg.Providers {
		if p.BaseURL == "" {
			continue
		}
		wg.Add(1)
		go func(name, base string) {
			defer wg.Done()
			base = strings.TrimRight(base, "/")
			// Lightest sensible liveness probe for OpenAI-like upstreams.
			u, err := url.JoinPath(base, "/models")
			if err != nil {
				probes[name] = probe{err: err.Error()}
				return
			}
			pr, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				probes[name] = probe{err: err.Error()}
				return
			}
			start := time.Now()
			resp, err := http.DefaultClient.Do(pr)
			if err != nil {
				probes[name] = probe{latencyMs: time.Since(start).Milliseconds(), err: err.Error()}
				return
			}
			io.CopyN(io.Discard, resp.Body, 512)
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				probes[name] = probe{latencyMs: time.Since(start).Milliseconds(), err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
				return
			}
			probes[name] = probe{latencyMs: time.Since(start).Milliseconds()}
		}(p.Name, p.BaseURL)
	}
	wg.Wait()

	for _, p := range s.cfg.Providers {
		row := map[string]any{
			"name": p.Name,
			"kind": p.Kind,
			"impl": p.Impl,
		}
		if avail := s.availability(ctx, p.Name); avail != nil {
			row["available"] = avail.OK
			row["reason"] = avail.Reason
			row["key_count"] = avail.KeyCount
		}
		rem := s.cooldownInfo(p.Name)
		if rem > 0 {
			row["in_cooldown"] = true
			row["cooldown_remaining_ms"] = rem
		}
		if pr, ok := probes[p.Name]; ok {
			row["probe_latency_ms"] = pr.latencyMs
			if pr.err != "" {
				row["last_probe_error"] = pr.err
			}
		}
		if le := s.lastError(p.Name); le != "" {
			row["last_error"] = le
		}
		if le, ok := row["last_error"].(string); ok && le != "" {
			status = "degraded"
		}
		providers = append(providers, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"version":   "step4",
		"uptime":    time.Since(s.serverStarted).String(),
		"providers": providers,
	})
}

func (s *Server) startedAt() time.Time {
	// best effort: use zero-time if unavailable
	return time.Time{}
}

func (s *Server) availability(ctx context.Context, name string) *core.Availability {
	prov, ok := s.router.Core().Provider(name)
	if !ok {
		return &core.Availability{OK: false, Reason: "not registered"}
	}
	return prov.Available(ctx)
}

// cooldownInfo returns the max remaining cooldown (ms) across all labels
// belonging to the named provider (0 = not cooling down).
func (s *Server) cooldownInfo(name string) int64 {
	cd := s.router.Core().Cooldown()
	if cd == nil {
		return 0
	}
	var maxMS int64
	pref := name + "/"
	for _, l := range cd.Labels() {
		if !strings.HasPrefix(l, pref) {
			continue
		}
		if rem := cd.Remaining(context.Background(), l); rem > 0 {
			if m := rem.Milliseconds(); m > maxMS {
				maxMS = m
			}
		}
	}
	return maxMS
}

// ---------------------------------------------------------------------------
// GET /v1/notes — free-tier notes (machine-readable)
// ---------------------------------------------------------------------------

func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("provider")
	notes := make([]map[string]any, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		if p.Notes == "" {
			continue
		}
		if filter != "" && !strings.EqualFold(filter, p.Name) {
			continue
		}
		row := map[string]any{
			"provider": p.Name,
			"kind":     p.Kind,
			"notes":    p.Notes,
		}
		if p.BaseURL != "" {
			row["base_url"] = p.BaseURL
		}
		if p.KeyEnv != "" {
			row["key_env"] = p.KeyEnv
		}
		notes = append(notes, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": notes})
}

// ---------------------------------------------------------------------------
// GET /v1/metrics — self-made Prometheus text format
// ---------------------------------------------------------------------------

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.metrics.WriteTo(w)
}

// ---------------------------------------------------------------------------
// GET / — friendly index
// ---------------------------------------------------------------------------

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "szmelc-router",
		"endpoints": []string{
			"POST /v1/chat/completions",
			"POST /chat/completions",
			"GET  /v1/models",
			"GET  /v1/health",
			"GET  /v1/notes",
			"GET  /v1/metrics",
			"GET  /v1/route   (task-router catalog)",
			"POST /v1/route   (task-router preview)",
			"GET  /v1/route/mode (task-router mode)",
			"PUT  /v1/route/mode (switch mode: rozklad-jazdy | jako-tako)",
		},
	})
}
