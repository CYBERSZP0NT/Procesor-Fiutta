// Package server implements the OpenAI-compatible HTTP server for
// freellm-router (STEP 4): chat completions (streaming + non-streaming,
// exact OpenAI shapes), models, health, notes, metrics — and the
// plugin-shaped, config-controlled middleware pipeline.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/silverx/freellm-router/internal/config"
)

// Middleware wraps an http.Handler. The pipeline is an ordered list;
// the FIRST element in the list runs FIRST (outermost).
type Middleware func(http.Handler) http.Handler

// ---------------------------------------------------------------------------
// request ID (propagation + generation)
// ---------------------------------------------------------------------------

type requestIDCtxKey struct{}

// RequestID returns the current request ID from context ("requestid"
// middleware sets it; inbound X-Request-ID is honored for propagation).
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDCtxKey{}).(string)
	return v
}

func requestIDmw(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDCtxKey{}, id))
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "rid-" + itoaNow()
	}
	return "rid-" + hex.EncodeToString(b)
}

func itoaNow() string {
	return strings.TrimSpace(time.Now().UTC().Format("20060102T150405.000000"))
}

// ---------------------------------------------------------------------------
// status-capturing response writer
// ---------------------------------------------------------------------------

// statusWriter captures response code/size for logging and usage.
// Each middleware that needs it self-wraps (a wrapped *statusWriter passes
// through unchanged via ensureStatus).
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int
}

// ensureStatus returns w as *statusWriter, wrapping if necessary.
func ensureStatus(w http.ResponseWriter) *statusWriter {
	if s, ok := w.(*statusWriter); ok {
		return s
	}
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == http.StatusOK {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// statusOut returns the captured status code.
func (s *statusWriter) statusOut() int { return s.status }

// ---------------------------------------------------------------------------
// logging (slog, structured, per-request id)
// ---------------------------------------------------------------------------

func loggingmw(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := ensureStatus(w)
			next.ServeHTTP(sw, r)
			if logger == nil {
				return
			}
			logger.Info("request",
				"request_id", RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.statusOut(),
				"bytes", sw.written,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

// ---------------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------------

func corsmw(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// auth (none | bearer; constant-time compare)
// ---------------------------------------------------------------------------

type authKeyCtxKey struct{}

// AuthKey returns the verified API key from context ("" = anonymous).
func AuthKey(ctx context.Context) string {
	v, _ := ctx.Value(authKeyCtxKey{}).(string)
	return v
}

func authmw(cfg config.AuthConfig) Middleware {
	mode := strings.ToLower(cfg.Mode)
	keys := make([]string, 0, len(cfg.Keys))
	for _, k := range cfg.Keys {
		if k != "" {
			keys = append(keys, k)
		}
	}
	if mode != "bearer" || len(keys) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			presented := bearerKey(r)
			for _, k := range keys {
				if subtle.ConstantTimeCompare([]byte(presented), []byte(k)) == 1 {
					ctx := context.WithValue(r.Context(), authKeyCtxKey{}, k)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeOAError(w, http.StatusUnauthorized, "invalid API key", "authentication_error", "api_key")
		})
	}
}

func bearerKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if q := r.URL.Query().Get("key"); q != "" {
		return q
	}
	return ""
}

// ---------------------------------------------------------------------------
// rate limit (per-key token bucket, pluggable)
// ---------------------------------------------------------------------------

// TokenBucket is a per-key token-bucket rate limiter.
type TokenBucket struct {
	rate    float64 // tokens per second
	burst   float64 // bucket capacity
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewTokenBucket creates a limiter allowing perSecond tokens with burst.
func NewTokenBucket(perSecond float64, burst float64) *TokenBucket {
	if burst < 1 {
		burst = 1
	}
	return &TokenBucket{rate: perSecond, burst: burst, buckets: make(map[string]*bucket)}
}

// Allow reports whether key may proceed at now (consumes one token if so).
func (t *TokenBucket) Allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: t.burst, last: now}
		t.buckets[key] = b
	}
	if now.After(b.last) {
		b.tokens += now.Sub(b.last).Seconds() * t.rate
		if b.tokens > t.burst {
			b.tokens = t.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Size returns the number of tracked keys (tests/diagnostics).
func (t *TokenBucket) Size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buckets)
}

func ratelimitmw(tb *TokenBucket) Middleware {
	if tb == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := AuthKey(r.Context())
			if key == "" {
				key = "anon"
			}
			if tb.Allow(key, time.Now()) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", "1")
			writeOAError(w, http.StatusTooManyRequests, "rate limit exceeded for this key", "rate_limit_exceeded", "")
		})
	}
}

// ---------------------------------------------------------------------------
// usage accounting (in-memory + optional append-JSONL, pluggable)
// ---------------------------------------------------------------------------

// UsageRecord is one accounting line.
type UsageRecord struct {
	T         time.Time `json:"t"`
	Path      string    `json:"path,omitempty"`
	Method    string    `json:"method,omitempty"`
	Status    int       `json:"status,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Key       string    `json:"key,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	// chat-specific (filled by the handler, not the middleware)
	Model  string       `json:"model,omitempty"`
	Via    string       `json:"via,omitempty"`
	Route  string       `json:"route,omitempty"`
	Stream bool         `json:"stream,omitempty"`
	Tokens *UsageTokens `json:"tokens,omitempty"`
	Err    string       `json:"error,omitempty"`
}

// UsageTokens holds per-call token counts.
type UsageTokens struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
}

// UsageRecorder is the pluggable accounting sink.
type UsageRecorder interface {
	Record(UsageRecord)
}

// BasicRecorder is the default: in-memory ring buffer. (The optional
// append-JSONL file is wired at construction in server.go.)
type BasicRecorder struct {
	entries []UsageRecord
	cap     int
	mu      sync.Mutex
	sink    func(UsageRecord) // optional file append (set via SetFileSink)
}

// NewBasicRecorder builds a recorder keeping the last cap records in memory.
func NewBasicRecorder(cap int) *BasicRecorder {
	if cap <= 0 {
		cap = 1000
	}
	return &BasicRecorder{entries: make([]UsageRecord, 0, cap), cap: cap}
}

// SetFileSink attaches an append function (typically a JSONL file writer).
func (b *BasicRecorder) SetFileSink(f func(UsageRecord)) {
	b.mu.Lock()
	b.sink = f
	b.mu.Unlock()
}

// Record implements UsageRecorder.
func (b *BasicRecorder) Record(r UsageRecord) {
	if r.T.IsZero() {
		r.T = time.Now()
	}
	b.mu.Lock()
	b.entries = append(b.entries, r)
	if len(b.entries) > b.cap {
		b.entries = b.entries[len(b.entries)-b.cap:]
	}
	sink := b.sink
	b.mu.Unlock()
	if sink != nil {
		sink(r)
	}
}

// Entries returns a copy of the in-memory records (tests / stats).
func (b *BasicRecorder) Entries() []UsageRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]UsageRecord, len(b.entries))
	copy(out, b.entries)
	return out
}

func usagemw(rec UsageRecorder) Middleware {
	if rec == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := ensureStatus(w)
			next.ServeHTTP(sw, r)
			rec.Record(UsageRecord{
				T:         start,
				Path:      r.URL.Path,
				Method:    r.Method,
				Status:    sw.statusOut(),
				RequestID: RequestID(r.Context()),
				Key:       AuthKey(r.Context()),
				LatencyMS: time.Since(start).Milliseconds(),
			})
		})
	}
}

// ---------------------------------------------------------------------------
// panic recover
// ---------------------------------------------------------------------------

func recovermw(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if logger != nil {
						logger.Error("panic in handler", "panic", rec, "path", r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					writeOAError(w, http.StatusInternalServerError, "internal error", "internal_error", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// body size limit
// ---------------------------------------------------------------------------

func bodyLimitmw(max int64) Middleware {
	if max <= 0 {
		max = 4 << 20 // 4 MiB default
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// pipeline assembly (defaults registered in ONE place; disable via config)
// ---------------------------------------------------------------------------

// namedMW is one entry in the pipeline: disable-able name + the middleware.
type namedMW struct {
	name string
	mw   Middleware
}

// defaultMiddleware returns the pipeline in ORDER (index 0 = outermost,
// runs first). This is the single source of truth for the chain BuildChain
// composes; tests assert this exact sequence.
func defaultMiddleware(
	logger *slog.Logger,
	authCfg config.AuthConfig,
	tokenBucket *TokenBucket,
	rec UsageRecorder,
	maxBody int64,
) []namedMW {
	tb := tokenBucket
	if tb == nil {
		tb = NewTokenBucket(30, 30) // permissive default when no limit is configured
	}
	return []namedMW{
		{"requestid", requestIDmw},
		{"bodylimit", bodyLimitmw(maxBody)},
		{"logging", loggingmw(logger)},
		{"cors", corsmw},
		{"auth", authmw(authCfg)},
		{"ratelimit", ratelimitmw(tb)},
		{"usage", usagemw(rec)},
		{"recover", recovermw(logger)},
	}
}

// BuildChain assembles the default middleware pipeline in the exact order
// required by the spec:
//
//	logging → CORS → auth → rate-limit → usage → panic-recover → handler
//
// (with request-ID + body-limit as the outermost plumbing). Middlewares
// listed in offNames are disabled (config: server.middleware_off).
func BuildChain(
	h http.Handler,
	logger *slog.Logger,
	authCfg config.AuthConfig,
	tokenBucket *TokenBucket,
	rec UsageRecorder,
	maxBody int64,
	offNames ...string,
) http.Handler {
	off := make(map[string]bool, len(offNames))
	for _, n := range offNames {
		off[strings.ToLower(n)] = true
	}
	mws := defaultMiddleware(logger, authCfg, tokenBucket, rec, maxBody)
	chain := h
	for i := len(mws) - 1; i >= 0; i-- {
		if off[mws[i].name] {
			continue
		}
		chain = mws[i].mw(chain)
	}
	return chain
}
