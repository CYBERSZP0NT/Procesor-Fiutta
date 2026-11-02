package core

import (
	"context"
	"sort"
	"sync"
	"time"
)

// InMemCooldown is the default CooldownStore: a mutex-guarded in-memory map
// of label → deadline. Zero dependencies, trivially testable.
type InMemCooldown struct {
	mu   sync.RWMutex
	m    map[string]time.Time
}

var _ CooldownStore = (*InMemCooldown)(nil)

func NewInMemCooldown() *InMemCooldown {
	return &InMemCooldown{m: make(map[string]time.Time)}
}

func (c *InMemCooldown) Set(_ context.Context, label string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[label] = time.Now().Add(d)
}

func (c *InMemCooldown) Has(_ context.Context, label string) bool {
	c.mu.RLock()
	deadline, ok := c.m[label]
	if !ok {
		c.mu.RUnlock()
		return false
	}
	if time.Now().Before(deadline) {
		c.mu.RUnlock()
		return true
	}
	c.mu.RUnlock()
	// Expired — clean up under write lock.
	c.mu.Lock()
	delete(c.m, label)
	c.mu.Unlock()
	return false
}

func (c *InMemCooldown) Remaining(_ context.Context, label string) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	deadline, ok := c.m[label]
	if !ok {
		return 0
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (c *InMemCooldown) Forget(_ context.Context, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, label)
}

func (c *InMemCooldown) ForgetAll(context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = make(map[string]time.Time)
}

// Labels returns a sorted snapshot of all labels currently on cooldown.
// Intended for debugging / observability.
func (c *InMemCooldown) Labels() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	out := make([]string, 0, len(c.m))
	for l, d := range c.m {
		if now.Before(d) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// Len returns the number of active cooldowns (expired entries not yet cleaned).
func (c *InMemCooldown) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
