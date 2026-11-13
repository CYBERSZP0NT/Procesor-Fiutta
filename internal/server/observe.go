package server

import (
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

	"github.com/CYBERSZP0NT/szmelc-router/internal/control"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
	"github.com/CYBERSZP0NT/szmelc-router/internal/usage"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

// registerObservability adds the usage / history / sessions / control API.
func (s *Server) registerObservability(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ping", s.handlePing)
	mux.HandleFunc("GET /v1/usage", s.handleUsage)
	mux.HandleFunc("GET /v1/history", s.localOnly(s.handleHistory))
	mux.HandleFunc("GET /v1/sessions", s.localOnly(s.handleSessions))
	mux.HandleFunc("GET /v1/control", s.handleControl)
	mux.HandleFunc("PUT /v1/control", s.localOnly(s.handleControl))
	mux.HandleFunc("PATCH /v1/control", s.localOnly(s.handleControl))
	mux.HandleFunc("DELETE /v1/cooldowns", s.localOnly(s.handleCooldowns))
}

// authOn reports whether bearer auth is actually enforced.
func (s *Server) authOn() bool {
	if !strings.EqualFold(s.cfg.Server.Auth.Mode, "bearer") {
		return false
	}
	for _, k := range s.cfg.Server.Auth.Keys {
		if k != "" {
			return true
		}
	}
	return false
}

// localOnly guards endpoints that change router behaviour or return logged
// content (prompt/answer previews): without auth they answer loopback
// clients only — the default bind is 0.0.0.0, and anyone on the network
// must not be able to steer the router or read its history.
func (s *Server) localOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authOn() && !isLoopbackAddr(r.RemoteAddr) {
			writeOAError(w, http.StatusForbidden,
				"this endpoint is only served to localhost while server.auth.mode is none; enable bearer auth to use it remotely",
				"permission_error", "")
			return
		}
		h(w, r)
	}
}

func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// sessionPrefix lets any OpenAI-compatible client join a session without
// custom headers: base_url http://host:port/s/<session>/v1 routes like
// /v1 and tags every call with <session>.
func sessionPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/s/"); ok {
			id, tail, _ := strings.Cut(rest, "/")
			if id != "" {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + tail
				r2.URL.RawPath = ""
				if r2.Header.Get("X-Freellm-Session") == "" {
					r2.Header.Set("X-Freellm-Session", id)
				}
				r = r2
			}
		}
		next.ServeHTTP(w, r)
	})
}

// callEvent returns the ledger record of the current request, if any.
func callEvent(ctx context.Context) *UsageRecord {
	if c := ledger.CallFrom(ctx); c != nil {
		return c.Event
	}
	return nil
}

// beginChat fills the chat fields of the request record and applies the
// per-request control override (X-Freellm-* headers, metadata).
func (s *Server) beginChat(w http.ResponseWriter, r *http.Request, q *oaChatRequest, msgs []router.Message) context.Context {
	ctx := r.Context()
	c := ledger.CallFrom(ctx)
	if c == nil { // usage middleware disabled: still track the call
		c = &ledger.Call{ID: RequestID(ctx), Source: "server", Event: &UsageRecord{T: time.Now(), Source: "server"}}
		ctx = ledger.WithCall(ctx, c)
	}
	if c.Session == "" {
		c.Session = firstNonEmpty(q.Metadata["session"], q.Metadata["freellm_session"])
	}
	c.Tags = parseTags(r.Header.Get("X-Freellm-Tags"))
	ev := c.Event
	ev.Kind = ledger.KindRequest
	ev.Model, ev.Route, ev.Stream, ev.Session, ev.Tags = q.Model, q.Model, q.Stream, c.Session, c.Tags
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			ev.Prompt = s.rt.Capture(msgs[i].Content)
			break
		}
	}
	if c.ID != "" {
		w.Header().Set("X-Freellm-Request-Id", c.ID)
	}
	if c.Session != "" {
		w.Header().Set("X-Freellm-Session", c.Session)
	}

	ov := control.Override{
		Profile:  firstNonEmpty(r.Header.Get("X-Freellm-Profile"), q.Metadata["profile"]),
		Fallback: parseBool(r.Header.Get("X-Freellm-Fallback")),
		Forever:  parseBool(r.Header.Get("X-Freellm-Forever")),
	}
	if n, err := strconv.Atoi(r.Header.Get("X-Freellm-Max-Attempts")); err == nil && n > 0 {
		ov.MaxAttempts = n
	}
	if !ov.IsZero() {
		ctx = router.WithOverride(ctx, ov)
	}
	return ctx
}

func parseBool(v string) *bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return control.Bool(true)
	case "0", "false", "off", "no":
		return control.Bool(false)
	}
	return nil
}

func parseTags(v string) map[string]string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(v, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(kv), "=")
		if k != "" {
			out[k] = val
		}
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"pid":       os.Getpid(),
		"started":   s.serverStarted,
		"uptime_s":  int64(time.Since(s.serverStarted).Seconds()),
		"in_flight": s.inFlight.Load(),
		"ledger":    s.rt.Path,
		"profile":   s.router.Resolve(r.Context()).Profile,
		"task_mode": s.router.TaskMode(),
	})
}

// events returns the history the server can see: the ledger file when
// there is one (full history), else the in-memory ring.
func (s *Server) events(full bool) []ledger.Event {
	if full && s.rt.Path != "" {
		if evs, err := s.ledgerCache.Read(); err == nil {
			return evs
		}
	}
	return s.rt.Hub.Recent()
}

func (s *Server) liveState(ctx context.Context) *usage.Live {
	res := s.router.Resolve(ctx)
	live := &usage.Live{
		Started:     s.serverStarted,
		InFlight:    s.inFlight.Load(),
		Cooldowns:   s.router.Cooldowns(),
		Unavailable: s.unavailable(ctx),
		Disabled:    map[string]string{},
	}
	names := s.router.Providers()
	for _, p := range names {
		switch {
		case res.IsDisabled(p):
			live.Disabled[strings.ToLower(p)] = "disabled by operator"
		case !res.Allowed(p):
			live.Disabled[strings.ToLower(p)] = "not in the allow-list"
		}
	}
	if res.EnforceLimits {
		live.Blocked = s.rt.Tracker.Blocked(names)
	}
	return live
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	// The file (cached) rather than the in-memory ring: the ring also holds
	// http/attempt noise and would undercount 24h windows on a busy server.
	evs := s.rt.Hub.Recent()
	if s.rt.Path != "" {
		if all, err := s.ledgerCache.Since(time.Now().Add(-25 * time.Hour)); err == nil {
			evs = all
		}
	}
	rep := s.rt.UsageReport(s.router, evs, s.liveState(ctx))
	writeJSON(w, http.StatusOK, rep)
}

// filterFromQuery parses history filters: since, until, kind (comma list),
// session, provider, id, source, errors, q, limit.
func filterFromQuery(r *http.Request) (*ledger.Filter, error) {
	q := r.URL.Query()
	now := time.Now()
	f := &ledger.Filter{
		Session: q.Get("session"), Provider: q.Get("provider"), ID: q.Get("id"),
		Source: q.Get("source"), Query: q.Get("q"),
		Errors: q.Get("errors") == "1" || q.Get("errors") == "true",
		Limit:  200,
	}
	var err error
	if f.Since, err = ledger.ParseSince(q.Get("since"), now); err != nil {
		return nil, err
	}
	if v := q.Get("until"); v != "" {
		if f.Until, err = ledger.ParseSince(v, now); err != nil {
			return nil, err
		}
	}
	if v := q.Get("kind"); v != "" {
		f.Kinds = strings.Split(v, ",")
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad limit %q", v)
		}
		f.Limit = n
	}
	return f, nil
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	f, err := filterFromQuery(r)
	if err != nil {
		writeOAError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	backlog := f.Apply(s.events(true))
	if q := r.URL.Query().Get("follow"); q != "1" && q != "true" {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": backlog, "ledger": s.rt.Path})
		return
	}

	// follow: Server-Sent Events, backlog first, then live events.
	flush := flushFn(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(e ledger.Event) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flush()
	}
	for _, e := range backlog {
		send(e)
	}
	ch, cancel := s.rt.Hub.Subscribe()
	defer cancel()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if f.Match(&e) {
				send(e)
			}
		}
	}
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sess := ledger.Sessions(s.events(true))
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && len(sess) > n {
		sess = sess[:n]
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": sess})
}

// ControlView is the GET/PUT /v1/control response.
type ControlView struct {
	Settings  control.Settings `json:"settings"`
	Effective control.Resolved `json:"effective"`
	Profiles  []string         `json:"profiles"`
	TaskMode  string           `json:"task_mode"`
	Cooldowns map[string]int   `json:"cooldowns_s,omitempty"`
}

func (s *Server) controlView(ctx context.Context) ControlView {
	cds := map[string]int{}
	for via, d := range s.router.Cooldowns() {
		cds[via] = int(d.Round(time.Second).Seconds())
	}
	return ControlView{
		Settings:  s.rt.Ctrl.Settings(),
		Effective: s.router.Resolve(ctx),
		Profiles:  []string{control.ProfileAuto, control.ProfileControl, control.ProfileBalanced},
		TaskMode:  s.router.TaskMode(),
		Cooldowns: cds,
	}
}

// handleControl: GET shows; PUT replaces the settings; PATCH merges a
// partial JSON object into them (fields present in the body win, null
// clears a field).
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeOAError(w, http.StatusBadRequest, "read body: "+err.Error(), "invalid_request_error", "")
			return
		}
		next := control.Settings{}
		if r.Method == http.MethodPatch {
			cur, _ := json.Marshal(s.rt.Ctrl.Settings())
			var base, patch map[string]any
			_ = json.Unmarshal(cur, &base)
			if err := json.Unmarshal(body, &patch); err != nil {
				writeOAError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "")
				return
			}
			if base == nil {
				base = map[string]any{}
			}
			for k, v := range patch {
				if v == nil {
					delete(base, k)
				} else {
					base[k] = v
				}
			}
			body, _ = json.Marshal(base)
		}
		if err := json.Unmarshal(body, &next); err != nil {
			writeOAError(w, http.StatusBadRequest, "invalid control settings: "+err.Error(), "invalid_request_error", "")
			return
		}
		// Hooks run commands on this machine: they come from the config file
		// only, never from the API (that would be remote code execution for
		// anyone who can reach the port).
		cur := s.rt.Ctrl.Settings().Hooks
		if !sameHooks(next.Hooks, cur) && !(r.Method == http.MethodPut && emptyHooks(next.Hooks)) {
			writeOAError(w, http.StatusBadRequest,
				"control.hooks can only be set in the config file (then restart the server)", "invalid_request_error", "hooks")
			return
		}
		next.Hooks = cur
		if err := s.rt.Ctrl.Replace(next); err != nil {
			writeOAError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
			return
		}
	}
	writeJSON(w, http.StatusOK, s.controlView(r.Context()))
}

func (s *Server) handleCooldowns(w http.ResponseWriter, r *http.Request) {
	n := s.router.ClearCooldowns(r.URL.Query().Get("via"))
	_ = s.rt.Hub.Write(ledger.Event{Kind: ledger.KindControl, Source: "server", Action: "clear-cooldowns",
		Via: r.URL.Query().Get("via"), Note: strconv.Itoa(n) + " cleared"})
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

func emptyHooks(h control.Hooks) bool {
	return len(h.PreCall) == 0 && len(h.PostCall) == 0 && h.TimeoutSec == 0
}

func sameHooks(a, b control.Hooks) bool {
	return strings.Join(a.PreCall, "\x00") == strings.Join(b.PreCall, "\x00") &&
		strings.Join(a.PostCall, "\x00") == strings.Join(b.PostCall, "\x00") && a.TimeoutSec == b.TimeoutSec
}

// unavailable caches provider availability for a few seconds: /v1/usage
// is polled, and some providers probe a daemon or a process to answer.
func (s *Server) unavailable(ctx context.Context) map[string]string {
	s.availMu.Lock()
	defer s.availMu.Unlock()
	if s.avail != nil && time.Since(s.availAt) < 15*time.Second {
		return s.avail
	}
	s.avail, s.availAt = s.router.Unavailable(ctx), time.Now()
	return s.avail
}
