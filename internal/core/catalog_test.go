package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCacheFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "catalog.json")
}

// fakeSource is a scripted catalog source for tests.
type fakeSource struct {
	name  string
	prio  int
	notes []ModelNote
	provs []ProviderSpec
	fail  bool
	fails int
}

func (s *fakeSource) source() NamedSource {
	return NamedSource{
		Name:     s.name,
		Priority: s.prio,
		Fetch: func(ctx context.Context) ([]ModelNote, error) {
			if s.fail {
				return nil, errors.New("network down")
			}
			s.fails++
			return s.notes, nil
		},
		ProviderList: func(ctx context.Context) ([]ProviderSpec, error) {
			if s.fail {
				return nil, errors.New("network down")
			}
			return s.provs, nil
		},
	}
}

func findNote(t *testing.T, s *Snapshot, prov, model string) *ModelNote {
	t.Helper()
	for i := range s.Notes {
		if s.Notes[i].Provider == prov && s.Notes[i].Model == model {
			return &s.Notes[i]
		}
	}
	t.Fatalf("note %s/%s not found in snapshot", prov, model)
	return nil
}

func TestCatalogPrecedenceLocalOverRemoteOverBuiltin(t *testing.T) {
	local := &fakeSource{
		name: "providers", prio: PriorityLocal,
		notes: []ModelNote{{Provider: "groq", Tier: TierInfo{Notes: "local note", Source: SourceLocal}}},
	}
	remote := &fakeSource{
		name: "freellmapi", prio: PriorityRemote,
		notes: []ModelNote{
			{Provider: "groq", Tier: TierInfo{Notes: "remote note", Source: "freellmapi"}},
			{Provider: "groq", Model: "llama-3.3", Tier: TierInfo{Caps: "up", Source: "freellmapi"}},
		},
	}
	builtin := &fakeSource{
		name: "embedded", prio: PriorityEmbedded,
		notes: []ModelNote{{Provider: "groq", Tier: TierInfo{Notes: "builtin note", Source: SourceEmbedded}}},
	}

	c := NewCatalog(0, time.Second, "", local.source(), remote.source(), builtin.source())
	snap, err := c.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	got := findNote(t, snap, "groq", "")
	if got.Tier.Notes != "local note" {
		t.Errorf("precedence: provider-level note = %q, want %q (local > remote > builtin)", got.Tier.Notes, "local note")
	}

	// Per-model note (no conflict) survives the merge.
	m := findNote(t, snap, "groq", "llama-3.3")
	if m.Tier.Source != "freellmapi" {
		t.Errorf("per-model note lost: %+v", m)
	}
	if len(snap.Sources) != 3 {
		t.Errorf("source statuses: want 3, got %d", len(snap.Sources))
	}
}

func TestCatalogTTLCacheAvoidsRefetch(t *testing.T) {
	f := testCacheFile(t)
	src := &fakeSource{
		name: "embedded", prio: PriorityEmbedded,
		notes: []ModelNote{{Provider: "x", Tier: TierInfo{Notes: "fresh"}}},
	}
	c := NewCatalog(time.Hour, time.Second, f, src.source())

	ctx := context.Background()
	s1, err := c.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Stale {
		t.Error("fresh resolve should not be stale")
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	callsAfterFirst := src.fails

	// Within TTL: must come from cache — the live source must NOT be called.
	s2, err := c.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Stale {
		t.Error("in-TTL resolve should not be stale")
	}
	if src.fails != callsAfterFirst {
		t.Errorf("expected no refetch within TTL (fails=%d, was=%d)", src.fails, callsAfterFirst)
	}

	// Force staleness by zeroing the cached timestamp.
	rewritten := `{"fetched_at":"0001-01-01T00:00:00Z","notes":[{"provider":"x","model":"","tier":{"notes":"old"}}]}`
	if err := os.WriteFile(f, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	s3, err := c.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s3.Stale {
		t.Error("successful refetch should clear stale flag")
	}
	if src.fails != callsAfterFirst+1 {
		t.Errorf("expected exactly one refetch after forced staleness (fails=%d)", src.fails)
	}
	if len(s3.Notes) == 0 || s3.Notes[0].Tier.Notes != "fresh" {
		t.Errorf("refetched note = %+v, want live value", s3.Notes)
	}
}

func TestCatalogOfflineFallsBackToStaleCache(t *testing.T) {
	f := testCacheFile(t)
	serving := &fakeSource{
		name: "embedded", prio: PriorityEmbedded,
		notes: []ModelNote{{Provider: "x", Tier: TierInfo{Notes: "fresh"}}},
	}
	c := NewCatalog(time.Hour, time.Second, f, serving.source())
	if _, err := c.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Network dies (and the TTL has expired, so a refetch is attempted).
	serving.fail = true
	c2 := NewCatalog(0, time.Second, f, serving.source())
	snap, err := c2.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Stale {
		t.Error("expected stale fallback when all sources fail and cache exists")
	}
	if len(snap.Notes) != 1 || snap.Notes[0].Tier.Notes != "fresh" {
		t.Errorf("stale cache content altered: %+v", snap.Notes)
	}
}

func TestCatalogOfflineNoCacheWorks(t *testing.T) {
	src := &fakeSource{name: "nope", prio: PriorityRemote}
	src.fail = true
	c := NewCatalog(time.Hour, time.Second, filepath.Join(t.TempDir(), "missing", "catalog.json"), src.source())
	snap, err := c.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Notes) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap.Notes)
	}
	found := false
	for _, ss := range snap.Sources {
		if ss.Name == "nope" && !ss.OK && ss.Err != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected failing source status in snapshot")
	}
}

func TestCatalogPartialFailureStillMerges(t *testing.T) {
	good := &fakeSource{
		name: "ok", prio: PriorityEmbedded,
		notes: []ModelNote{{Provider: "a", Tier: TierInfo{Notes: "good"}}},
	}
	bad := &fakeSource{name: "bad", prio: PriorityRemote}
	bad.fail = true

	c := NewCatalog(0, time.Second, "", good.source(), bad.source())
	snap, err := c.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Notes) != 1 || snap.Notes[0].Tier.Notes != "good" {
		t.Errorf("partial merge lost good source: %+v", snap.Notes)
	}
}
