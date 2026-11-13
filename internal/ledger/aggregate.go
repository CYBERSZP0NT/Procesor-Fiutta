package ledger

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SessionSummary aggregates one session's events.
type SessionSummary struct {
	ID        string         `json:"id"`
	Source    string         `json:"source,omitempty"`
	Note      string         `json:"note,omitempty"`
	Start     time.Time      `json:"start"`
	Last      time.Time      `json:"last"`
	Ended     bool           `json:"ended"`
	Requests  int            `json:"requests"`
	Errors    int            `json:"errors"`
	Attempts  int            `json:"attempts"`
	Tokens    int            `json:"tokens"`
	Providers map[string]int `json:"providers,omitempty"` // served requests per provider
}

// Sessions groups events by session id, most recently active first.
// Events without a session are not part of any session.
func Sessions(events []Event) []SessionSummary {
	by := map[string]*SessionSummary{}
	for i := range events {
		e := &events[i]
		if e.Session == "" {
			continue
		}
		s := by[e.Session]
		if s == nil {
			s = &SessionSummary{ID: e.Session, Start: e.T, Providers: map[string]int{}}
			by[e.Session] = s
		}
		if e.T.Before(s.Start) {
			s.Start = e.T
		}
		if e.T.After(s.Last) {
			s.Last = e.T
		}
		if s.Source == "" {
			s.Source = e.Source
		}
		switch e.EffectiveKind() {
		case KindSession:
			if e.Action == "end" {
				s.Ended = true
			}
			if e.Note != "" && s.Note == "" {
				s.Note = e.Note
			}
		case KindRequest:
			s.Requests++
			if e.Failed() {
				s.Errors++
			} else if p := e.ProviderName(); p != "" {
				s.Providers[p]++
			}
			s.Tokens += e.Tokens.Total()
		case KindAttempt:
			s.Attempts++
		}
	}
	out := make([]SessionSummary, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// Related returns the events that belong to request id (the request
// itself, its attempts and task decisions), oldest first.
func Related(events []Event, id string) []Event {
	var out []Event
	for _, e := range events {
		if id != "" && strings.HasPrefix(e.ID, id) {
			out = append(out, e)
		}
	}
	return out
}

// ParseSince turns a human time bound into an instant:
// "15m", "2h", "7d", "today", "yesterday", "2026-11-01", RFC 3339.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return time.Time{}, nil
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch s {
	case "today":
		return midnight, nil
	case "yesterday":
		return midnight.AddDate(0, 0, -1), nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, strings.ToUpper(s)); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("bad time %q (use 30m, 2h, 7d, today, 2026-11-01)", s)
}
