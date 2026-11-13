// Package router is the stable public Go API for embedding freellm-router
// in another Go service.
//
// # Stability
//
// As of v1.0.0-rc1 the exported surface (Config, ProviderConfig, Router,
// Route, RouteWithOptions, Message, Done, UpstreamErr) follows semantic
// versioning: breaking changes only in a new major. Internal packages are
// not part of this guarantee.
//
// # Example
//
//	import "github.com/silverx/freellm-router/pkg/router"
//
//	r, _ := router.New(router.Config{
//	    Providers: []router.ProviderConfig{
//	        {Name: "test", BaseURL: "http://localhost:8080/v1", Kind: "keyless"},
//	    },
//	    Routes: map[string][]string{
//	        "auto": {"test/gpt-4o-mini"},
//	    },
//	})
//
//	var text strings.Builder
//	res, err := r.Route(ctx, "auto", "Hello!", func(s string) { text.WriteString(s) })
//
// Providers are plain data — every OpenAI-compatible endpoint, Ollama, a
// g4f sidecar, or an external stdio JSON-RPC plugin (Impl = "plugin") can
// be added without touching the router. A working end-to-end example lives
// in example/embed.go.
package router

import (
	"context"
	"strings"
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/internal/core"
	g4fbridge "github.com/silverx/freellm-router/internal/providers/g4fbridge"
	"github.com/silverx/freellm-router/internal/providers/ollama"
	openailike "github.com/silverx/freellm-router/internal/providers/openailike"
	pluginproc "github.com/silverx/freellm-router/internal/providers/pluginproc"
)

// ProviderConfig is the minimum info needed to create a provider instance.
type ProviderConfig struct {
	Name    string
	Impl    string // "openailike" | "ollama" | "g4fbridge" | "plugin"
	BaseURL string
	Kind    string // "keyless" | "keyed" | "local" | …
	KeyEnv  string // env var name holding the API key
	Header  string // non-standard auth header name
	// Plugin fields (used when Impl == "plugin")
	Command []string
	Env     map[string]string
}

// Config is the public configuration for New().
type Config struct {
	Providers []ProviderConfig
	Routes    map[string][]string
	// KeyEnvMap maps provider name → env var(s) holding keys.
	// If empty, each ProviderConfig.KeyEnv is used.
	KeyEnvMap map[string]string
	// Timeout per request (default 120s).
	Timeout time.Duration
	// Cooldowns.
	CooldownShort time.Duration
	CooldownLong  time.Duration
	// MaxChainAttempts total (default 8).
	MaxChainAttempts int
	// RetrySame allows one retry on the same provider for 429/5xx.
	RetrySame bool
	// AutoAlias is the route name used as fallback (default "auto").
	AutoAlias string
	// Agentic overlays the embedded task-router spec (nil = defaults).
	Agentic *TaskSpec
}

// Router is the public handle. Call Route() to send a request.
type Router struct {
	core *core.Router
	task *taskRouter
}

// New builds a Router from Config. Supported impls: "openailike" (any
// OpenAI-compatible HTTP API), "ollama", "g4fbridge" (g4f-class sidecar) and
// "plugin" (external stdio JSON-RPC provider).
func New(cfg Config) (*Router, error) {
	// Build internal config.
	intCfg := config.Config{
		Routes:    cfg.Routes,
		Providers: make([]config.Provider, 0, len(cfg.Providers)),
		Policies: config.PolicyConfig{
			CooldownShortSec: int(cfg.CooldownShort / time.Second),
			CooldownLongSec:  int(cfg.CooldownLong / time.Second),
			MaxChainAttempts: cfg.MaxChainAttempts,
			RetrySame:        cfg.RetrySame,
		},
	}
	if intCfg.Policies.MaxChainAttempts <= 0 {
		intCfg.Policies.MaxChainAttempts = 8
	}
	if intCfg.Policies.CooldownShortSec <= 0 {
		intCfg.Policies.CooldownShortSec = 30
	}
	if intCfg.Policies.CooldownLongSec <= 0 {
		intCfg.Policies.CooldownLongSec = 3600
	}

	for _, pc := range cfg.Providers {
		intCfg.Providers = append(intCfg.Providers, config.Provider{
			Name:    pc.Name,
			Impl:    pc.Impl,
			BaseURL: pc.BaseURL,
			Kind:    pc.Kind,
			KeyEnv:  pc.KeyEnv,
			Header:  pc.Header,
			Command: pc.Command,
			Env:     pc.Env,
		})
	}

	// Determine key env map.
	keyEnvMap := cfg.KeyEnvMap
	if keyEnvMap == nil {
		keyEnvMap = make(map[string]string)
		for _, pc := range cfg.Providers {
			if pc.KeyEnv != "" {
				keyEnvMap[pc.Name] = pc.KeyEnv
			}
		}
	}

	keySrc := core.NewEnvKeySource(keyEnvMap)

	// Instantiate providers.
	provMap := make(map[string]core.Provider, len(cfg.Providers))
	for _, pc := range cfg.Providers {
		var prov core.Provider
		switch strings.ToLower(pc.Impl) {
		case "openailike", "":
			cp, _ := intCfg.ProviderByName(pc.Name)
			prov = openailike.New(cp, keySrc, cfg.Timeout)
		case "ollama":
			cp, _ := intCfg.ProviderByName(pc.Name)
			prov = ollama.New(cp, cfg.Timeout)
		case "g4fbridge":
			cp, _ := intCfg.ProviderByName(pc.Name)
			prov = g4fbridge.New(cp, keySrc, cfg.Timeout)
		case "plugin":
			cp, _ := intCfg.ProviderByName(pc.Name)
			prov = pluginproc.New(cp, nil) // nil logger = slog.Default()
		default:
			return nil, &UnsupportedImplError{Impl: pc.Impl}
		}
		if prov != nil {
			provMap[pc.Name] = prov
		}
	}

	// Build route policy.
	autoAlias := cfg.AutoAlias
	if autoAlias == "" {
		autoAlias = "auto"
	}
	provNames := make([]string, 0, len(provMap))
	for n := range provMap {
		provNames = append(provNames, n)
	}
	policy := core.NewDefaultRoutePolicy(cfg.Routes, provNames, autoAlias)

	// Build router.
	short := cfg.CooldownShort
	if short <= 0 {
		short = 30 * time.Second
	}
	long := cfg.CooldownLong
	if long <= 0 {
		long = time.Hour
	}

	cr := core.NewRouter(core.RouterOpts{
		Providers:   provMap,
		RoutePolicy: policy,
		Cooldown:    core.NewInMemCooldown(),
		Short:       short,
		Long:        long,
		RetrySame:   cfg.RetrySame,
		MaxAtt:      intCfg.Policies.MaxChainAttempts,
	})

	kinds := make(map[string]string, len(cfg.Providers))
	for _, pc := range cfg.Providers {
		if _, ok := provMap[pc.Name]; ok {
			kinds[strings.ToLower(pc.Name)] = pc.Kind
		}
	}
	tr, err := newTaskRouter(cfg.Agentic, kinds, cfg.Routes)
	if err != nil {
		return nil, err
	}

	return &Router{core: cr, task: tr}, nil
}

// Route sends a chat request through the fallback chain.
//
// Parameters:
//
//	– ctx: cancellation/timeout context
//	– selector: route alias ("auto", "free") or "provider/model" or bare model ID
//	– messages: the conversation
//	– model: the model ID to request (set in the upstream call)
//	– sink: receives text deltas (may be nil to collect internally)
//
// Returns the "via" label (provider/model that served the request),
// finish info, and any error.
func (r *Router) Route(
	ctx context.Context,
	selector string,
	messages []Message,
	model string,
	sink func(string),
) (via string, done *Done, err error) {
	req := &core.ChatRequest{
		Model:    model,
		Messages: make([]core.Message, 0, len(messages)),
	}
	for _, m := range messages {
		req.Messages = append(req.Messages, core.Message{
			Role:    core.Role(m.Role),
			Content: m.Content,
		})
	}
	req.Stream = sink != nil

	var out *core.StreamOut
	if sink != nil {
		out = &core.StreamOut{TextSink: sink}
	}

	result, err := r.dispatch(ctx, selector, req, out, nil, nil)
	if err != nil {
		return "", nil, err
	}

	d := &Done{
		FinishReason:     result.Done.FinishReason,
		PromptTokens:     result.Done.PromptTokens,
		CompletionTokens: result.Done.CompletionTokens,
	}
	if sink == nil {
		d.Text = result.FullText
	}
	return result.Via, d, nil
}

// RouteOpts holds optional request parameters.
type RouteOpts struct {
	MaxTokens   *int
	Temperature *float64
	TopP        *float64
	Tools       []Tool
	ToolChoice  string // "" | "auto" | "none" | "required" | "function:<name>"
	// Task carries task-router overrides (used when the selector is a
	// task alias such as "agent").
	Task *TaskHints
}

// RouteWithOptions is like Route but accepts advanced options.
func (r *Router) RouteWithOptions(
	ctx context.Context,
	selector string,
	messages []Message,
	model string,
	opts *RouteOpts,
	sink func(string),
) (via string, done *Done, err error) {
	req := &core.ChatRequest{
		Model:    model,
		Messages: make([]core.Message, 0, len(messages)),
	}
	for _, m := range messages {
		req.Messages = append(req.Messages, core.Message{
			Role:    core.Role(m.Role),
			Content: m.Content,
		})
	}
	if opts != nil {
		if opts.MaxTokens != nil {
			req.MaxTokens = *opts.MaxTokens
		}
		if opts.Temperature != nil {
			req.Temperature = opts.Temperature
		}
		if opts.TopP != nil {
			req.TopP = opts.TopP
		}
	}
	req.Stream = sink != nil

	var out *core.StreamOut
	if sink != nil {
		out = &core.StreamOut{TextSink: sink}
	}

	var hints *TaskHints
	if opts != nil {
		hints = opts.Task
	}
	var onTask func(*TaskDecision)
	if hints != nil {
		onTask = hints.OnDecision
	}
	result, err := r.dispatch(ctx, selector, req, out, hints, onTask)
	if err != nil {
		return "", nil, err
	}

	d := &Done{
		FinishReason:     result.Done.FinishReason,
		PromptTokens:     result.Done.PromptTokens,
		CompletionTokens: result.Done.CompletionTokens,
	}
	if sink == nil {
		d.Text = result.FullText
	}
	return result.Via, d, nil
}

// Message is a simplified message for public API users.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Done is the result metadata.
type Done struct {
	FinishReason     string `json:"finish_reason"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Text             string `json:"text,omitempty"` // set when sink was nil
}

// UpstreamErr is the typed error returned when all providers fail.
type UpstreamErr struct {
	Status   int    `json:"status"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Msg      string `json:"msg"`
}

func (e *UpstreamErr) Error() string {
	return "upstream: [" + e.Provider + "/" + e.Model + "] HTTP " + itoa(e.Status) + ": " + e.Msg
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// AsUpstreamErr unwraps err into *UpstreamErr if possible.
func AsUpstreamErr(err error) (*UpstreamErr, bool) {
	ue, ok := core.IsUpstreamErr(err)
	if !ok {
		return nil, false
	}
	return &UpstreamErr{Status: ue.Status, Provider: ue.Provider, Model: ue.Model, Msg: ue.Msg}, true
}

// Providers returns the list of registered provider names.
func (r *Router) Providers() []string {
	return r.core.ChainNames()
}

// UnsupportedImplError is returned when a provider impl is not yet supported.
type UnsupportedImplError struct {
	Impl string
}

func (e *UnsupportedImplError) Error() string {
	return "router: unsupported impl " + e.Impl + " (supported: openailike|ollama|g4fbridge|plugin)"
}

// ---------------------------------------------------------------------------
// Extended routing surface (step 4, for the HTTP server and richer embedders)
// ---------------------------------------------------------------------------

// Core returns the underlying core router (for per-provider access in the
// server: availability, catalog, cooldown).
func (r *Router) Core() *core.Router { return r.core }

// Tool is a function definition passed through to the upstream as-is.
type Tool struct {
	Name        string
	Description string
	Parameters  any // JSON schema
}

// ToolChunk is one incremental (or, for non-streaming upstreams, complete)
// tool call delivered to the Sinks.Tool callback.
type ToolChunk struct {
	ID        string
	Name      string
	Arguments string // may be a fragment in streaming mode
}

// Sinks collects response parts. Use with RouteRaw.
type Sinks struct {
	// Text receives text deltas (non-stream responses arrive as one call).
	Text func(string)
	// Tool receives tool-call chunks.
	Tool func(ToolChunk)
	// Serving is called with the "provider/model" via label as soon as the
	// chosen provider starts streaming its first delta (streaming mode).
	Serving func(via string)
	// Task is called with the task router's decision (selector is a task
	// alias): before the upstream call, again after each escalation, and
	// once more with Via set when the answer is accepted.
	Task func(*TaskDecision)
}

// RouteRaw routes a request with full control over streaming mode and sinks.
//
//	stream  – whether to request an SSE stream from the upstream
//	opts    – sampling params, tools, tool_choice (may be nil)
//	s       – sinks; nil = discard all output (rarely wanted)
//
// Returns the via label and finish metadata.
func (r *Router) RouteRaw(
	ctx context.Context,
	selector string,
	messages []Message,
	model string,
	stream bool,
	opts *RouteOpts,
	s Sinks,
) (via string, done *Done, err error) {
	req := &core.ChatRequest{
		Model:    model,
		Stream:   stream,
		Messages: make([]core.Message, 0, len(messages)),
	}
	for _, m := range messages {
		req.Messages = append(req.Messages, core.Message{
			Role:    core.Role(m.Role),
			Content: m.Content,
		})
	}
	if opts != nil {
		if opts.MaxTokens != nil {
			req.MaxTokens = *opts.MaxTokens
		}
		if opts.Temperature != nil {
			req.Temperature = opts.Temperature
		}
		if opts.TopP != nil {
			req.TopP = opts.TopP
		}
		for _, t := range opts.Tools {
			req.Tools = append(req.Tools, core.ToolDef{
				Type: "function",
				Function: core.FunctionDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
		if tc := parseToolChoice(opts.ToolChoice); tc != nil {
			req.ToolChoice = tc
		}
	}

	out := &core.StreamOut{}
	if s.Text != nil {
		out.TextSink = s.Text
	}
	if s.Tool != nil {
		out.ToolSink = func(tc core.ToolCall) {
			s.Tool(ToolChunk{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
	}
	if s.Serving != nil {
		out.Serving = s.Serving
	}

	var hints *TaskHints
	if opts != nil {
		hints = opts.Task
	}
	res, err := r.dispatch(ctx, selector, req, out, hints, s.Task)
	if err != nil {
		return "", nil, err
	}
	d := &Done{
		FinishReason:     res.Done.FinishReason,
		PromptTokens:     res.Done.PromptTokens,
		CompletionTokens: res.Done.CompletionTokens,
	}
	return res.Via, d, nil
}

// RouteOpts — extended in step 4 (Tools, ToolChoice). Existing fields
// unchanged.
func parseToolChoice(s string) *core.ToolChoice {
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "function:") {
		name := strings.TrimPrefix(s, "function:")
		if name == "" {
			return nil
		}
		return &core.ToolChoice{Type: "function", Function: &core.ToolChoiceFn{Name: name}}
	}
	if s != "auto" && s != "none" && s != "required" {
		return nil
	}
	return &core.ToolChoice{Type: s}
}

// NewFromConfig builds a Router directly from an internal config.Config.
// This is the convenience entry point for the HTTP server and CLI.
func NewFromConfig(cfg *config.Config, timeout time.Duration) (*Router, error) {
	pcs := make([]ProviderConfig, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		switch strings.ToLower(p.Impl) {
		case "openailike", "":
			pcs = append(pcs, ProviderConfig{
				Name:    p.Name,
				Impl:    "openailike",
				BaseURL: p.BaseURL,
				Kind:    p.Kind,
				KeyEnv:  p.KeyEnv,
				Header:  p.Header,
			})
		case "ollama":
			pcs = append(pcs, ProviderConfig{
				Name:    p.Name,
				Impl:    "ollama",
				BaseURL: p.BaseURL,
				Kind:    p.Kind,
				KeyEnv:  p.KeyEnv,
			})
		case "g4fbridge":
			pcs = append(pcs, ProviderConfig{
				Name:    p.Name,
				Impl:    "g4fbridge",
				BaseURL: p.BaseURL,
				Kind:    p.Kind,
				KeyEnv:  p.KeyEnv,
			})
		case "plugin":
			pcs = append(pcs, ProviderConfig{
				Name:    p.Name,
				Impl:    "plugin",
				Command: p.Command,
				Env:     p.Env,
				Kind:    p.Kind,
			})
		default:
			// Unknown impl: skip silently (validation should have caught it).
		}
	}

	rc := Config{
		Providers:        pcs,
		Routes:           cfg.Routes,
		Timeout:          timeout,
		KeyEnvMap:        cfg.KeyEnvMap(),
		CooldownShort:    time.Duration(cfg.Policies.CooldownShortSec) * time.Second,
		CooldownLong:     time.Duration(cfg.Policies.CooldownLongSec) * time.Second,
		MaxChainAttempts: cfg.Policies.MaxChainAttempts,
		RetrySame:        cfg.Policies.RetrySame,
		Agentic:          &cfg.Agentic,
	}
	return New(rc)
}
