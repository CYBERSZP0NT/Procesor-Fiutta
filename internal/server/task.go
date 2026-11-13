package server

import (
	"io"
	"net/http"

	"github.com/silverx/szmelc-router/pkg/router"
)

// handleRouteCatalog serves GET /v1/route: the effective task-router spec
// with live availability per catalog entry.
func (s *Server) handleRouteCatalog(w http.ResponseWriter, r *http.Request) {
	spec := s.router.TaskSpec()
	avail := s.router.TaskAvailability(r.Context())
	type entry struct {
		router.TaskEntry
		Available string `json:"available"`
	}
	cat := make([]entry, 0, len(spec.Catalog))
	for _, e := range spec.Catalog {
		cat = append(cat, entry{TaskEntry: e, Available: avail[e.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":              "freellm.task_router",
		"enabled":             spec.IsEnabled(),
		"mode":                s.router.TaskMode(),
		"modes":               router.TaskModes(),
		"aliases":             s.router.TaskAliases(),
		"roles":               spec.Roles,
		"complexity":          spec.Complexity,
		"rules":               spec.Rules,
		"escalations":         spec.MaxEscalations(),
		"below_tier_fallback": spec.BelowTierFallback == nil || *spec.BelowTierFallback,
		"catalog":             cat,
	})
}

// handlePlan serves POST /v1/route: what the task router would pick for a
// chat-completions body, without calling any upstream. "model" may be a
// task alias with a suffix ("agent:code:4"); empty means the first alias.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOAError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "body")
		return
	}
	var q oaChatRequest
	if err := jsonUnmarshal(body, &q); err != nil {
		writeOAError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "body")
		return
	}
	if q.Model != "" && !s.router.IsTaskAlias(q.Model) {
		writeOAError(w, http.StatusBadRequest, "model "+q.Model+" is not a task-router alias (try one of /v1/route aliases)", "invalid_request_error", "model")
		return
	}
	msgs := make([]router.Message, 0, len(q.Messages))
	for _, m := range q.Messages {
		msgs = append(msgs, router.Message{Role: m.Role, Content: m.contentString()})
	}
	d, err := s.router.PlanTask(r.Context(), q.Model, msgs, len(q.Tools) > 0, taskHints(r, q.Metadata))
	if err != nil {
		writeOAError(w, http.StatusNotFound, err.Error(), "invalid_request_error", "")
		return
	}
	setTaskHeaders(w.Header(), d)
	writeJSON(w, http.StatusOK, d)
}

// taskHints collects the per-request task-router overrides.
func taskHints(r *http.Request, md map[string]string) *router.TaskHints {
	return &router.TaskHints{
		Override: r.Header.Get("X-Freellm-Task"),
		Mode:     r.Header.Get("X-Freellm-Task-Mode"),
		Metadata: md,
	}
}

// handleMode serves GET /v1/route/mode (current mode + the list) and
// PUT|POST /v1/route/mode {"mode": "jako-tako"} — switches the default mode
// for every following request until restart (persist it with
// `szmelc-router mode <mode>` or agentic.mode in the config).
func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeOAError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "body")
			return
		}
		var q struct {
			Mode string `json:"mode"`
		}
		if err := jsonUnmarshal(body, &q); err != nil {
			writeOAError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "body")
			return
		}
		if _, err := s.router.SetTaskMode(q.Mode); err != nil {
			writeOAError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "mode")
			return
		}
		s.logger.Info("task-router mode switched", "mode", s.router.TaskMode())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "freellm.task_mode",
		"mode":   s.router.TaskMode(),
		"modes":  router.TaskModes(),
	})
}
