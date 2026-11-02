package core

import (
	"context"
	"fmt"
)

// LimitedProvider wraps another Provider with a semaphore-based cap on
// concurrent in-flight Stream() calls (config: providers[].max_parallel).
//
// All other methods pass straight through. Acquiring a slot that cannot be
// obtained before the caller's context deadline/cancellation fails the
// attempt with the context error (classified as "conn" by the backoff
// policy / router).
type LimitedProvider struct {
	inner Provider
	sem   chan struct{}
	limit int
}

var _ Provider = (*LimitedProvider)(nil)

// NewLimitedProvider returns a concurrency-capped wrapper around p.
// n <= 0 defaults to 8. The wrapper is safe for concurrent use.
func NewLimitedProvider(p Provider, n int) *LimitedProvider {
	if n <= 0 {
		n = 8
	}
	return &LimitedProvider{inner: p, sem: make(chan struct{}, n), limit: n}
}

// Limit returns the configured cap (for introspection/tests).
func (l *LimitedProvider) Limit() int { return l.limit }

// Inner returns the wrapped provider (for introspection).
func (l *LimitedProvider) Inner() Provider { return l.inner }

func (l *LimitedProvider) Name() string  { return l.inner.Name() }
func (l *LimitedProvider) Kinds() []Kind { return l.inner.Kinds() }
func (l *LimitedProvider) Models(ctx context.Context) ([]Model, error) {
	return l.inner.Models(ctx)
}
func (l *LimitedProvider) Available(ctx context.Context) *Availability {
	return l.inner.Available(ctx)
}

// Stream acquires a slot (bounded by the caller ctx), then delegates.
func (l *LimitedProvider) Stream(ctx context.Context, req *ChatRequest, out *StreamOut) (*StreamDone, error) {
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	select {
	case l.sem <- struct{}{}:
	case <-ctx2.Done():
		return nil, fmt.Errorf("limited[%s]: waiting for in-flight slot: %w", l.inner.Name(), ctx2.Err())
	}
	defer func() { <-l.sem }()
	return l.inner.Stream(ctx2, req, out)
}
