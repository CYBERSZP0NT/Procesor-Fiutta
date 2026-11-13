package ledger

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/silverx/szmelc-router/internal/journal"
)

// Sink receives events.
type Sink interface {
	Write(Event) error
	Close() error
}

// Marshal encodes e as one JSON line (with trailing newline).
func Marshal(e Event) []byte {
	b, _ := json.Marshal(e)
	return append(b, '\n')
}

// rotating writes through a journal.Writer (size-based rotation). Only the
// long-running server uses it: several processes renaming the same file
// would race, so short-lived writers append without rotating.
type rotating struct{ w *journal.Writer }

// OpenRotating opens path for appending with size-based rotation.
func OpenRotating(path string, maxBytes int64, keep int) (Sink, error) {
	if maxBytes == 0 {
		maxBytes = journal.DefaultMaxBytes
	}
	w, err := journal.New(path, maxBytes, keep)
	if err != nil {
		return nil, err
	}
	return &rotating{w: w}, nil
}

func (r *rotating) Write(e Event) error { return r.w.AppendLine(Marshal(e)) }
func (r *rotating) Close() error        { return r.w.Close() }

// appender is a plain O_APPEND writer: each event is one write() of one
// line, so lines from concurrent processes do not interleave.
type appender struct {
	mu sync.Mutex
	f  *os.File
}

// OpenAppend opens path for appending without rotation (CLI processes).
func OpenAppend(path string) (Sink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return &appender{f: f}, nil
}

func (a *appender) Write(e Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	_, err := a.f.Write(Marshal(e))
	return err
}

func (a *appender) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}

// Discard is a Sink that drops everything (ledger disabled).
type Discard struct{}

func (Discard) Write(Event) error { return nil }
func (Discard) Close() error      { return nil }

// ---------------------------------------------------------------------------
// Hub: in-memory ring + live subscribers + optional file sink
// ---------------------------------------------------------------------------

// Hub fans events out to a file sink, keeps the most recent ones in memory
// (for the live API when no file is configured) and streams them to
// subscribers (`history --follow`, the TUI).
type Hub struct {
	mu   sync.Mutex
	ring []Event
	cap  int
	sink Sink
	subs map[chan Event]struct{}
}

// NewHub keeps the last capacity events in memory (default 5000).
func NewHub(capacity int, sink Sink) *Hub {
	if capacity <= 0 {
		capacity = 5000
	}
	if sink == nil {
		sink = Discard{}
	}
	return &Hub{cap: capacity, sink: sink, subs: map[chan Event]struct{}{}}
}

// Write records e (stamping the time if unset).
func (h *Hub) Write(e Event) error {
	if e.T.IsZero() {
		e.T = time.Now()
	}
	h.mu.Lock()
	h.ring = append(h.ring, e)
	if len(h.ring) > h.cap {
		h.ring = append(h.ring[:0:0], h.ring[len(h.ring)-h.cap:]...)
	}
	sink := h.sink
	for ch := range h.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block requests
		}
	}
	h.mu.Unlock()
	return sink.Write(e)
}

// Close closes the file sink and every subscriber.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
	return h.sink.Close()
}

// Recent returns a copy of the in-memory events, oldest first.
func (h *Hub) Recent() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.ring...)
}

// Seed preloads the ring (e.g. from the file at server start) without
// writing anything.
func (h *Hub) Seed(events []Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(events) > h.cap {
		events = events[len(events)-h.cap:]
	}
	h.ring = append(append([]Event(nil), events...), h.ring...)
	if len(h.ring) > h.cap {
		h.ring = h.ring[len(h.ring)-h.cap:]
	}
}

// Subscribe returns a channel receiving every future event and a cancel func.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// ReadFile reads the ledger at path plus its rotated files, oldest first.
// Unparseable lines are skipped; a missing file is an empty ledger.
func ReadFile(path string, keep int) ([]Event, error) {
	lines, err := journal.ReadAll(path, max(keep, journal.DefaultKeep))
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(lines))
	for _, raw := range lines {
		var e Event
		if json.Unmarshal(raw, &e) != nil || e.T.IsZero() {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out, nil
}

// Filter selects events. Zero fields match everything.
type Filter struct {
	Since    time.Time
	Until    time.Time
	Kinds    []string // EffectiveKind in this set
	Session  string
	Provider string // provider name (exact) or via prefix
	ID       string // request id
	Source   string
	Errors   bool   // only failures
	Query    string // case-insensitive substring over the whole line
	Limit    int    // keep the last N after filtering (0 = all)
}

// Match reports whether e passes f (Limit is applied by Apply).
func (f *Filter) Match(e *Event) bool {
	if !f.Since.IsZero() && e.T.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.T.After(f.Until) {
		return false
	}
	if len(f.Kinds) > 0 {
		k, ok := e.EffectiveKind(), false
		for _, want := range f.Kinds {
			if strings.EqualFold(want, k) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.Session != "" && e.Session != f.Session {
		return false
	}
	if f.ID != "" && !strings.HasPrefix(e.ID, f.ID) {
		return false
	}
	if f.Source != "" && e.Source != f.Source {
		return false
	}
	if f.Provider != "" && !strings.EqualFold(e.ProviderName(), f.Provider) &&
		!strings.HasPrefix(strings.ToLower(e.Via), strings.ToLower(f.Provider)) {
		return false
	}
	if f.Errors && !e.Failed() {
		return false
	}
	if f.Query != "" {
		b, _ := json.Marshal(e)
		if !strings.Contains(strings.ToLower(string(b)), strings.ToLower(f.Query)) {
			return false
		}
	}
	return true
}

// Apply filters events (oldest first) and keeps the last f.Limit.
func (f *Filter) Apply(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for i := range events {
		if f.Match(&events[i]) {
			out = append(out, events[i])
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out
}

// Follow tails the ledger file, calling fn for each new event that matches
// f, until ctx is done. It starts at the current end of the file and
// survives rotation (the file shrinking or being replaced).
func Follow(ctx context.Context, path string, f *Filter, fn func(Event)) error {
	var off int64
	if st, err := os.Stat(path); err == nil {
		off = st.Size()
	}
	var partial []byte
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if st.Size() < off { // rotated
			off, partial = 0, nil
		}
		if st.Size() == off {
			continue
		}
		fh, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := fh.Seek(off, io.SeekStart); err != nil {
			fh.Close()
			continue
		}
		rd := bufio.NewReader(fh)
		for {
			line, err := rd.ReadBytes('\n')
			off += int64(len(line))
			if err != nil { // incomplete last line: keep for the next tick
				partial = append(partial, line...)
				break
			}
			if len(partial) > 0 {
				line = append(partial, line...)
				partial = nil
			}
			var e Event
			if json.Unmarshal(line, &e) == nil && !e.T.IsZero() && (f == nil || f.Match(&e)) {
				fn(e)
			}
		}
		fh.Close()
	}
}
