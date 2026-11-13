// Package g4fbridge implements core.Provider for any gpt4free-class sidecar:
//
//	– g4f.dev hosted API (OpenAI-compatible)
//	– g4f python server (FastAPI, OpenAI-compatible)
//	– custom Go/Rust/Python wrappers exposing /v1/chat/completions
//
// Unlike the openailike provider (which targets vendor APIs with specific
// auth schemes), g4fbridge is tuned for the g4f ecosystem:
//
//	– Typically keyless (but optional bearer key for auth-gated sidecars)
//	– Some servers include a non-standard "status" field in response tokens
//	– Models list may include non-standard names with metadata
//
// The transport is HTTP + SSE (same as openailike) but the default
// availability semantics and error handling are g4f-aware.
package g4fbridge

import (
	"context"
	"strings"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/core"
	"github.com/CYBERSZP0NT/szmelc-router/internal/providers/openailike"
)

// Provider wraps an openailike provider with g4f-specific behavior.
// The core transport (SSE, auth, models fetch) is delegated to openailike;
// g4fbridge adjusts: default kind, availability semantics, and the provider
// label used in error messages.
type Provider struct {
	inner *openailike.Provider
}

var _ core.Provider = (*Provider)(nil)

// New creates a g4fbridge provider.
// cfg.BaseURL should point to the OpenAI-compatible endpoint
// (e.g. https://api.g4f.dev/v1 or http://localhost:8000/v1).
func New(p config.Provider, ks core.KeySource, timeout time.Duration) *Provider {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	inner := openailike.New(p, ks, timeout)
	return &Provider{inner: inner}
}

// Name returns the provider name (as configured).
func (p *Provider) Name() string { return p.inner.Name() }

// Kinds returns the transport kind.
func (p *Provider) Kinds() []core.Kind { return p.inner.Kinds() }

// Available checks whether the g4f sidecar is reachable.
// For keyless g4f endpoints, this is a simple GET /models probe.
func (p *Provider) Available(ctx context.Context) *core.Availability {
	avail := p.inner.Available(ctx)
	if avail.OK {
		// Probe reachability: try a lightweight GET /models.
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if models, err := p.inner.Models(ctx2); err == nil && len(models) > 0 {
			avail.Up = true
		}
	}
	return avail
}

// Models returns the model catalog (delegates to openailike /models fetch).
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	models, err := p.inner.Models(ctx)
	if err != nil {
		return nil, err
	}
	// Annotate with g4f provenance.
	for i := range models {
		if models[i].Tier.Notes == "" {
			models[i].Tier.Notes = "served via gpt4free sidecar"
		}
	}
	return models, nil
}

// Stream delegates to the inner openailike provider (SSE streaming).
func (p *Provider) Stream(ctx context.Context, req *core.ChatRequest, out *core.StreamOut) (*core.StreamDone, error) {
	return p.inner.Stream(ctx, req, out)
}

// NewFromBaseURL is a convenience constructor for cases where a config.Provider
// has not been validated (e.g. in quick scripts or embeds).
func NewFromBaseURL(name, baseURL string, timeout time.Duration) *Provider {
	cfg := config.Provider{
		Name:    name,
		Impl:    "g4fbridge",
		BaseURL: strings.TrimRight(baseURL, "/"),
		Kind:    "scraper",
	}
	return New(cfg, nil, timeout)
}
