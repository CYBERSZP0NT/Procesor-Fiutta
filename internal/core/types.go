// Package core defines the domain types, interfaces, and value objects
// shared across every other package in freellm-router.
//
// Design rules (see AGENTS.md):
//   – No provider name appears as a constant here.
//   – Every function takes context.Context first.
//   – Errors are wrapped with fmt.Errorf("%w") into *UpstreamErr.
package core

import (
	"context"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Provider kinds (transport category, NOT a specific vendor)
// ---------------------------------------------------------------------------

type Kind string

const (
	KindKeyless   Kind = "keyless"   // no API key required at all
	KindKeyed     Kind = "keyed"     // requires an API key from KeySource
	KindLocal     Kind = "local"     // runs on local hardware (e.g. ollama)
	KindScraper   Kind = "scraper"   // keyless scraped / reverse-engineered web
	KindSidecar   Kind = "sidecar"   // another router/sidecar (freellmapi, etc.)
	KindPlugin    Kind = "plugin"    // external subprocess (pluginproc)
)

// TierInfo holds free-tier metadata for a model (display only, not enforced).
type TierInfo struct {
	Caps   string `json:"caps,omitempty"   yaml:"caps,omitempty"`
	Notes  string `json:"notes,omitempty"  yaml:"notes,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

// Model represents one routable model (across a provider).
type Model struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Kinds []Kind `json:"kinds,omitempty"`
	Tier  TierInfo `json:"tier,omitempty"`
}

// ---------------------------------------------------------------------------
// Provider – the single extension point
// ---------------------------------------------------------------------------

// Availability is returned by Provider.Available and reflects whether
// the provider is callable right now.
type Availability struct {
	OK      bool          `json:"ok"`
	Reason  string        `json:"reason,omitempty"` // human-readable why
	KeyCount int          `json:"key_count,omitempty"`
	Up      bool          `json:"up,omitempty"`     // optional reachability probe
	Latency time.Duration `json:"latency,omitempty"`
}

// Provider is one routable inference source.
// The concrete implementations live in internal/providers/*.
type Provider interface {
	// Name returns a unique provider identifier (e.g. "groq", "pollinations").
	// This must match the name field in defaults/providers.yaml or config.
	Name() string

	// Models returns the provider's model catalog.
	// May return nil/empty when the catalog is unknown or the provider
	// is not yet connected (caller should handle gracefully).
	Models(ctx context.Context) ([]Model, error)

	// Stream sends a chat request and streams back text/tool/reasoning.
	// out.TextSink is called for each text delta.
	// Returns StreamDone with finish reason and (optional) token counts.
	Stream(ctx context.Context, req *ChatRequest, out *StreamOut) (*StreamDone, error)

	// Availability reports whether this provider is callable right now:
	// key present? up? in cooldown?
	Available(ctx context.Context) *Availability

	// Kinds returns the transport kind(s) of this provider.
	Kinds() []Kind
}

// ---------------------------------------------------------------------------
// Chat request / streaming types
// ---------------------------------------------------------------------------

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`
	Name    string `json:"name,omitempty"`
	// ToolCallID is filled when Role==RoleTool.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls is filled when Role==RoleAssistant && finish_reason=="tool_calls".
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents a function call the model requested.
type ToolCall struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"` // always "function"
	Function  ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

// ToolDef describes a tool available to the model.
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"` // JSON Schema object
}

// ChatRequest is everything needed to send one chat-completion request
// to any upstream, regardless of provider.
type ChatRequest struct {
	Model        string     `json:"model"`
	Messages     []Message  `json:"messages"`
	MaxTokens    int        `json:"max_tokens,omitempty"`
	Temperature  *float64   `json:"temperature,omitempty"`
	TopP         *float64   `json:"top_p,omitempty"`
	Tools        []ToolDef  `json:"tools,omitempty"`
	ToolChoice   *ToolChoice `json:"tool_choice,omitempty"`
	Stream       bool       `json:"stream"`
	ResponseFmt  string     `json:"response_format,omitempty"`
}

type ToolChoice struct {
	Type     string `json:"type"` // "auto" | "none" | "required" | "function"
	Function *ToolChoiceFn `json:"function,omitempty"`
}

type ToolChoiceFn struct {
	Name string
}

// StreamOut carries the sinks a provider writes to during streaming.
// All sinks are optional — the provider only calls the relevant ones.
type StreamOut struct {
	TextSink      func(string)      // text delta callback
	ToolSink      func(ToolCall)    // incremental tool-call chunk
	ReasoningSink func(string)      // reasoning / thinking text
	ErrSink       func(error)       // non-fatal upstream error notice
}

// StreamDone is returned by Provider.Stream after the full response has
// been delivered through the StreamOut sinks.
type StreamDone struct {
	FinishReason     string `json:"finish_reason"` // "stop"|"length"|"tool_calls"|"content_filter"|…
	PromptTokens     int    `json:"prompt_tokens"`     // -1 when upstream does not report
	CompletionTokens int    `json:"completion_tokens"` // -1 when upstream does not report
}

// ---------------------------------------------------------------------------
// Route resolution
// ---------------------------------------------------------------------------

// ModelSelector is the user's request: "what chain do I want?"
// It can be an alias name ("auto", "free", …), a "provider/model" string,
// a bare provider name, or a bare model ID.
type ModelSelector struct {
	// Name is the model/alias string from the API request or CLI.
	Name string
	// Context is additional context (e.g. detected task type) for future
	// semantic routing — currently unused by the default policy.
	Context map[string]string
}

// RoutePolicy expands a ModelSelector into an ordered []Model chain.
// The default implementation is in internal/core (see router_policy.go).
type RoutePolicy interface {
	Resolve(ctx context.Context, sel ModelSelector, known map[string]Model) []Model
}

// SemanticRouteHook is an extension point for "route by prompt content"
// plugins (STEP 2+). Currently unused — kept as an interface so the
// composition root can plug one in without changing RoutePolicy.
type SemanticRouteHook interface {
	Adjust(ctx context.Context, chain []Model, req *ChatRequest) []Model
}

// ---------------------------------------------------------------------------
// Key management
// ---------------------------------------------------------------------------

// KeySource supplies zero or more API keys for a given provider.
// Multi-key → round-robin rotation (the router cycles across them).
type KeySource interface {
	// Keys returns the list of available keys for provider.
	// Empty slice = no key (provider may still be keyless).
	Keys(ctx context.Context, provider string) []string
}

// ---------------------------------------------------------------------------
// Cooldown / availability
// ---------------------------------------------------------------------------

// CooldownStore tracks when a provider/model label was last put on cooldown
// and whether it is still cooling down.
type CooldownStore interface {
	// Set puts label on cooldown for the given duration.
	Set(ctx context.Context, label string, d time.Duration)

	// Has returns true if label is currently in cooldown.
	Has(ctx context.Context, label string) bool

	// Remaining returns the remaining cooldown duration (0 if not cooling down).
	Remaining(ctx context.Context, label string) time.Duration

	// Forget removes label from the store (e.g. after a successful call).
	Forget(ctx context.Context, label string)

	// ForgetAll removes all entries (useful for tests / admin reset).
	ForgetAll(ctx context.Context)
}

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

// ModelNote is a small metadata record that accompanies a model in the catalog.
type ModelNote struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Tier     TierInfo
}

// CatalogSource provides model/tier metadata.
// The router merges results from multiple sources (embedded, live, remote).
type CatalogSource interface {
	Fetch(ctx context.Context) []ModelNote
}

// ---------------------------------------------------------------------------
// Typed error
// ---------------------------------------------------------------------------

// UpstreamErr is the single typed error returned from Provider.Stream and
// the Router after all fallbacks are exhausted.
type UpstreamErr struct {
	Status   int
	Provider string
	Model    string
	Msg      string
	// Cause optionally holds the underlying Go error (for %w chaining).
	Cause error
}

func (e *UpstreamErr) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("upstream: [%s/%s] HTTP %d: %s", e.Provider, e.Model, e.Status, e.Msg)
	}
	return fmt.Sprintf("upstream: [%s/%s] HTTP %d: %s", e.Provider, e.Model, e.Status, e.Msg)
}

func (e *UpstreamErr) Unwrap() error { return e.Cause }

// NewUpstreamErr wraps a base error into *UpstreamErr with the given metadata.
func NewUpstreamErr(status int, provider, model, msg string, cause error) *UpstreamErr {
	return &UpstreamErr{Status: status, Provider: provider, Model: model, Msg: msg, Cause: cause}
}

// IsUpstreamErr reports whether err (or its cause chain) is a *UpstreamErr.
func IsUpstreamErr(err error) (*UpstreamErr, bool) {
	for err != nil {
		if ue, ok := err.(*UpstreamErr); ok {
			return ue, true
		}
		uu, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		err = uu.Unwrap()
	}
	return nil, false
}
