package ledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadFileLegacyAndOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.jsonl")
	lines := `{"t":"2026-01-02T10:00:00Z","path":"/v1/chat/completions","status":200,"via":"groq/x"}
not json
{"t":"2026-01-01T10:00:00Z","kind":"attempt","via":"groq/x","status":429}
{"t":"2026-01-03T10:00:00Z","path":"/v1/models","status":200}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err := ReadFile(p, 0)
	if err != nil || len(evs) != 3 {
		t.Fatalf("evs=%d err=%v", len(evs), err)
	}
	if evs[0].Kind != KindAttempt || evs[1].EffectiveKind() != KindRequest || evs[2].EffectiveKind() != KindHTTP {
		t.Fatalf("kinds: %s %s %s", evs[0].EffectiveKind(), evs[1].EffectiveKind(), evs[2].EffectiveKind())
	}
	if evs[1].ProviderName() != "groq" {
		t.Fatal("provider from via")
	}
	missing, err := ReadFile(filepath.Join(t.TempDir(), "none"), 0)
	if err != nil || len(missing) != 0 {
		t.Fatal("missing file should be empty")
	}
}

func TestFilterAndSessions(t *testing.T) {
	now := time.Now()
	evs := []Event{
		{T: now.Add(-3 * time.Hour), Kind: KindRequest, Session: "a", Status: 200, Via: "x/m", Tokens: &Tokens{Prompt: 1, Completion: 2}},
		{T: now.Add(-time.Hour), Kind: KindAttempt, Session: "a", ID: "r2", Status: 429, Via: "y/m"},
		{T: now.Add(-time.Hour), Kind: KindRequest, Session: "a", ID: "r2", Status: 502, Err: "boom"},
		{T: now, Kind: KindSession, Session: "a", Action: "end"},
		{T: now, Kind: KindRequest, Status: 200, Via: "x/m"},
	}
	since, _ := ParseSince("2h", now)
	f := Filter{Since: since, Errors: true}
	if got := f.Apply(evs); len(got) != 2 {
		t.Fatalf("since+errors = %d", len(got))
	}
	f = Filter{Kinds: []string{"request"}, Provider: "x", Limit: 1}
	if got := f.Apply(evs); len(got) != 1 || got[0].Session != "" {
		t.Fatalf("kind+provider+limit = %+v", got)
	}
	f = Filter{Query: "BOOM"}
	if got := f.Apply(evs); len(got) != 1 {
		t.Fatal("query")
	}
	if rel := Related(evs, "r2"); len(rel) != 2 {
		t.Fatalf("related = %d", len(rel))
	}
	ss := Sessions(evs)
	if len(ss) != 1 || ss[0].Requests != 2 || ss[0].Errors != 1 || ss[0].Attempts != 1 || !ss[0].Ended || ss[0].Tokens != 3 {
		t.Fatalf("sessions = %+v", ss)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local)
	for in, want := range map[string]time.Time{
		"30m":        now.Add(-30 * time.Minute),
		"7d":         now.Add(-7 * 24 * time.Hour),
		"today":      time.Date(2026, 11, 13, 0, 0, 0, 0, time.Local),
		"2026-11-01": time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local),
	} {
		got, err := ParseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseSince("soon", now); err == nil {
		t.Error("want error")
	}
}

func TestHubAndFollow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.jsonl")
	sink, err := OpenAppend(p)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(2, sink)
	ch, cancel := h.Subscribe()
	defer cancel()

	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	got := make(chan Event, 4)
	go func() { _ = Follow(ctx, p, &Filter{Kinds: []string{KindRequest}}, func(e Event) { got <- e }) }()
	time.Sleep(100 * time.Millisecond)

	for _, k := range []string{KindAttempt, KindRequest, KindRequest} {
		_ = h.Write(Event{Kind: k, Model: k})
	}
	if len(h.Recent()) != 2 {
		t.Fatal("ring capacity")
	}
	if e := <-ch; e.Kind != KindAttempt {
		t.Fatal("subscriber order")
	}
	for i := 0; i < 2; i++ {
		select {
		case e := <-got:
			if e.Kind != KindRequest {
				t.Fatal("follow filter")
			}
		case <-ctx.Done():
			t.Fatal("follow did not deliver")
		}
	}
	_ = h.Close()
}
