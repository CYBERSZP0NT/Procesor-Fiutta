package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
	"github.com/silverx/freellm-router/pkg/router"
)

// Server is the OpenAI-compatible HTTP server. Build with New, serve with Run.
type Server struct {
	cfg     *config.Config
	router  *router.Router
	metrics *Metrics
	rec     *BasicRecorder
	logger  *slog.Logger

	maxBody int64

	mu      sync.RWMutex
	lastErr map[string]string // provider name → last upstream error text

	inFlight int64 // atomic: requests currently inside the chat handler

	once   bool
	onceCh chan int32 // success(1) / failure(2) of the first chat call
	onceDo sync.Once

	serverStarted time.Time
}

// New creates the server from a validated config. timeout bounds upstream calls.
func New(cfg *config.Config, timeout time.Duration) (*Server, error) {
	r, err := router.NewFromConfig(cfg, timeout)
	if err != nil {
		return nil, err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: levelFromName(cfg.Logging.Level),
	}))

	m := NewMetrics()
	rec := NewBasicRecorder(1000)

	s := &Server{
		serverStarted: time.Now(),
		cfg:           cfg,
		router:        r,
		metrics:       m,
		rec:           rec,
		logger:        logger,
		maxBody:       cfg.Server.MaxBodyBytes,
		lastErr:       make(map[string]string),
		onceCh:        make(chan int32, 1),
	}
	// Wire upstream-error observation into the core router for metrics.
	r.Core().SetOnUpstreamError(func(via string, err error) {
		provider := via
		if i := strings.Index(via, "/"); i > 0 {
			provider = via[:i]
		}
		status := 0
		if ue, ok := core.IsUpstreamErr(err); ok {
			status = ue.Status
		}
		m.RecordUpstreamError(provider, status)
		s.setLastErr(provider, err)
	})

	// Optional usage JSONL file.
	if path := cfg.Server.UsageFile; path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			rec.SetFileSink(func(r UsageRecord) {
				line := toJSONLine(r)
				_, _ = f.Write(line)
			})
		}
	}
	return s, nil
}

func (s *Server) setLastErr(provider string, err error) {
	s.mu.Lock()
	s.lastErr[provider] = err.Error()
	s.mu.Unlock()
}

func (s *Server) lastError(provider string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr[provider]
}

func toJSONLine(r UsageRecord) []byte {
	b, _ := jsonMarshal(r)
	b = append(b, '\n')
	return b
}

// ---------------------------------------------------------------------------
// HTTP wiring
// ---------------------------------------------------------------------------

// Handler returns the fully-middleware-wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /chat/completions", s.handleChat)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /models", s.handleModels)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /v1/notes", s.handleNotes)
	mux.HandleFunc("GET /notes", s.handleNotes)
	mux.HandleFunc("GET /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /{$}", s.handleRoot)

	return BuildChain(
		mux,
		s.logger,
		s.cfg.Server.Auth,
		buildBucket(s.cfg),
		s.rec,
		s.maxBody,
		s.cfg.Server.MiddlewareOff...,
	)
}

func buildBucket(cfg *config.Config) *TokenBucket {
	if perMin := cfg.Server.RateLimitPerKeyPerMin; perMin > 0 {
		return NewTokenBucket(float64(perMin)/60.0, float64(perMin))
	}
	return nil
}

func levelFromName(name string) slog.Level {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ---------------------------------------------------------------------------
// Run (listen, signals, graceful shutdown, --once)
// ---------------------------------------------------------------------------

// ServeOpts overrides the config values for one run.
type ServeOpts struct {
	Host  string
	Port  int
	Cert  string
	Key   string
	Once  bool
	Grace time.Duration
	// MaxBodyBytes overrides cfg.Server.MaxBodyBytes when > 0.
	MaxBodyBytes int64
}

// Run blocks until shutdown (SIGINT/SIGTERM, --once completion, or error).
func (s *Server) Run(ctx context.Context, o ServeOpts) error {
	host := o.Host
	if host == "" {
		host = s.cfg.Server.Host
	}
	port := o.Port
	if port == 0 {
		port = s.cfg.Server.Port
	}
	cert, key := s.cfg.Server.CertFile, s.cfg.Server.KeyFile
	if o.Cert != "" {
		cert = o.Cert
	}
	if o.Key != "" {
		key = o.Key
	}
	grace := o.Grace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	if o.Once {
		s.once = true
	}

	// ctx may come from the CLI; also listen for OS signals in-process.
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		var err error
		if cert != "" && key != "" {
			err = srv.ListenAndServeTLS(cert, key)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	s.logger.Info("server started", "addr", addr, "tls", cert != "" && key != "")

	for {
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			s.drain(grace)
			sctx, cancel := context.WithTimeout(context.Background(), grace)
			shutErr := srv.Shutdown(sctx)
			cancel()
			return shutErr
		case code := <-s.onceCh:
			s.logger.Info("--once: first request finished", "code", code)
			s.drain(5 * time.Second)
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(sctx)
			cancel()
			if code == 1 {
				return nil
			}
			return &OnceFirstRequestError{Code: code}
		}
	}
}

// drain waits (up to d) for in-flight chat streams to finish.
func (s *Server) drain(d time.Duration) {
	deadline := time.Now().Add(d)
	for {
		if s.inFlight == 0 {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// InFlight returns the current count of in-progress chat requests (tests).
func (s *Server) InFlight() int64 { return s.inFlight }

// Metrics exposes the metrics registry (tests / embeds).
func (s *Server) Metrics() *Metrics { return s.metrics }

// Recorders exposes the usage recorder (tests / CLI stats).
func (s *Server) Recorders() *BasicRecorder { return s.rec }

// Router exposes the underlying router (embeds).
func (s *Server) Router() *router.Router { return s.router }

// ---------------------------------------------------------------------------
// chat completions handler
// ---------------------------------------------------------------------------

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeOAError(w, http.StatusMethodNotAllowed, "use POST", "invalid_request_error", "")
		return
	}

	start := time.Now()
	s.inFlight++
	defer func() {
		s.inFlight--
		if s.once {
			// fired below on success/failure via onceFire
		}
	}()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader surfaces a 413 via a *http.MaxBytesError / EOF after limit.
		s.onceFail("body read failed")
		writeOAError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "body")
		return
	}

	var q oaChatRequest
	if err := jsonUnmarshal(body, &q); err != nil {
		s.onceFail("invalid json: " + err.Error())
		writeOAError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "body")
		return
	}

	if q.Model == "" {
		s.onceFail("missing model")
		writeOAError(w, http.StatusBadRequest, "'model' is required", "invalid_request_error", "model")
		return
	}
	if len(q.Messages) == 0 {
		s.onceFail("no messages")
		writeOAError(w, http.StatusBadRequest, "'messages' must be a non-empty array", "invalid_request_error", "messages")
		return
	}

	// Build messages for the router.
	msgs := make([]router.Message, 0, len(q.Messages))
	for _, m := range q.Messages {
		// Convert tool call args raw → string
		msgs = append(msgs, router.Message{
			Role:    m.Role,
			Content: contentString(m.Content),
		})
	}

	// Sampling options.
	var opts *router.RouteOpts
	if q.MaxTokens > 0 || q.Temperature != nil || q.TopP != nil || len(q.Tools) > 0 || len(q.ToolChoice) > 0 {
		opts = &router.RouteOpts{}
		if q.MaxTokens > 0 {
			mt := q.MaxTokens
			opts.MaxTokens = &mt
		}
		opts.Temperature = q.Temperature
		opts.TopP = q.TopP
		for _, t := range q.Tools {
			opts.Tools = append(opts.Tools, router.Tool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		if len(q.ToolChoice) > 0 {
			opts.ToolChoice = toolChoiceString(q.ToolChoice)
		}
	}

	finish := flushFn(w)
	chunkID := newChunkID()
	completed := false
	var buf strings.Builder
	var toolBuf []router.ToolChunk
	var via atomic.Value // set by Serving callback
	sinks := router.Sinks{
		Text: func(delta string) {
			buf.WriteString(delta)
			if !completed {
				s.commitStream(w, &via, chunkID, q.Model)
				completed = true
			}
			writeChunk(w, map[string]any{
				"id":      chunkID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   q.Model,
				"choices": []map[string]any{{
					"index":         0,
					"delta":         map[string]any{"content": delta},
					"finish_reason": nil,
				}},
			}, finish)
		},
		Tool: func(tc router.ToolChunk) {
			toolBuf = append(toolBuf, tc)
			if !completed {
				s.commitStream(w, &via, chunkID, q.Model)
				completed = true
			}
			writeChunk(w, map[string]any{
				"id":      chunkID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   q.Model,
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []map[string]any{{
							"index": 0,
							"id":    tc.ID,
							"type":  "function",
							"function": map[string]any{
								"name":      tc.Name,
								"arguments": tc.Arguments,
							},
						}},
					},
					"finish_reason": nil,
				}},
			}, finish)
		},
		Serving: func(v string) { via.Store(v) },
	}

	selector := q.Model
	if _, isAlias := s.cfg.Routes[selector]; !isAlias {
		// keep selector as-is; default policy will resolve it
	}

	v, done, rerr := s.router.RouteRaw(r.Context(), selector, msgs, q.Model, q.Stream, opts, sinks)
	if rerr != nil {
		s.onceFail(selector + " failed: " + rerr.Error())
		if !completed {
			status, typ := statusFromError(rerr)
			writeOAError(w, status, "all providers failed for route "+selector+": "+errToMsg(rerr), typ, "")
			s.metrics.RecordRequest(time.Since(start))
			return
		}
		// Stream already committed: emit an error note + [DONE] so clients close cleanly.
		writeChunk(w, map[string]any{
			"id":     chunkID,
			"object": "chat.completion.chunk",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "error",
			}},
		}, finish)
		writeSSEDone(w, finish)
		s.metrics.RecordRequest(time.Since(start))
		return
	}

	// Success.
	if !q.Stream {
		s.commitOnce(w, v, q.Model)
		resp := s.buildNonStreamResp(chunkID, q.Model, selector, &buf, done, toolBuf)
		writeJSON(w, http.StatusOK, resp)
	} else {
		if !completed {
			s.commitOnce(w, v, q.Model)
			completed = true
		}
		// Final chunk with finish_reason (+ usage when known).
		final := map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   q.Model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": done.FinishReason,
			}},
		}
		if done.PromptTokens > 0 && done.CompletionTokens > 0 {
			final["usage"] = map[string]int{
				"prompt_tokens":     done.PromptTokens,
				"completion_tokens": done.CompletionTokens,
				"total_tokens":      done.PromptTokens + done.CompletionTokens,
			}
		}
		writeChunk(w, final, finish)
		writeSSEDone(w, finish)
	}

	s.metrics.RecordRoute(labelFor(q.Model))
	s.metrics.RecordRequest(time.Since(start))

	// Usage record (chat level with via/tokens).
	s.rec.Record(UsageRecord{
		T:         start,
		Path:      r.URL.Path,
		Method:    r.Method,
		Status:    http.StatusOK,
		RequestID: RequestID(r.Context()),
		Key:       AuthKey(r.Context()),
		LatencyMS: time.Since(start).Milliseconds(),
		Model:     q.Model,
		Route:     selector,
		Via:       v,
		Stream:    q.Stream,
		Tokens: &UsageTokens{
			Prompt:     done.PromptTokens,
			Completion: done.CompletionTokens,
		},
	})

	s.onceOK()
}

func (s *Server) onceOK() {
	if !s.once {
		return
	}
	s.onceDo.Do(func() { s.onceCh <- 1 })
}

func (s *Server) onceFail(reason string) {
	if !s.once {
		return
	}
	s.logger.Warn("--once: request failed", "reason", reason)
	s.onceDo.Do(func() { s.onceCh <- 2 })
}

// commitStream writes the streaming response headers (once) and sets the
// X-Freellm-Routed-Via header from the currently-serving provider.
func (s *Server) commitStream(w http.ResponseWriter, via *atomic.Value, chunkID, model string) {
	v := ""
	if x, ok := via.Load().(string); ok && x != "" {
		v = x
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if v != "" {
		w.Header().Set("X-Freellm-Routed-Via", v)
	}
}

// commitOnce writes headers for a JSON (non-stream) response with the via header.
func (s *Server) commitOnce(w http.ResponseWriter, via, model string) {
	if via != "" {
		w.Header().Set("X-Freellm-Routed-Via", via)
	}
}

func (s *Server) buildNonStreamResp(id, model, via string, text *strings.Builder, done *router.Done, toolBuf []router.ToolChunk) map[string]any {
	usage := map[string]int{
		"prompt_tokens":     done.PromptTokens,
		"completion_tokens": done.CompletionTokens,
		"total_tokens":      done.PromptTokens + done.CompletionTokens,
	}
	if done.PromptTokens <= 0 && done.CompletionTokens <= 0 {
		// Spec: emit -1 when the upstream does not report usage.
		usage = map[string]int{
			"prompt_tokens":     -1,
			"completion_tokens": -1,
			"total_tokens":      -1,
		}
	}

	choice := map[string]any{
		"index":         0,
		"message":       map[string]any{"role": "assistant", "content": text.String()},
		"finish_reason": done.FinishReason,
	}
	if done.FinishReason == "tool_calls" && len(toolBuf) > 0 {
		choice["message"].(map[string]any)["content"] = nil
		tcs := make([]map[string]any, 0, len(toolBuf))
		for i, tc := range toolBuf {
			tcs = append(tcs, map[string]any{
				"index": i,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		choice["message"].(map[string]any)["tool_calls"] = tcs
	}

	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
		"usage":   usage,
	}
	if via != "" {
		resp["freellm_via"] = via // non-breaking extra field; the header is authoritative
	}
	return resp
}

func labelFor(model string) string {
	// Truncate very long model labels to keep metric cardinality bounded.
	if len(model) > 80 {
		return model[:80]
	}
	if model == "" {
		return "default"
	}
	return model
}

// OnceFirstRequestError is returned by Run when the first (and only) request fails.
type OnceFirstRequestError struct{ Code int32 }

func (e *OnceFirstRequestError) Error() string {
	return "once: first request failed (code " + strconv.Itoa(int(e.Code)) + ")"
}
