package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/control"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
	"github.com/CYBERSZP0NT/szmelc-router/internal/usage"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

type fakeBackend struct {
	settings control.Settings
	events   []ledger.Event
	cleared  int
}

func (f *fakeBackend) Describe() string { return "fake" }
func (f *fakeBackend) Live() bool       { return true }
func (f *fakeBackend) Close() error     { return nil }
func (f *fakeBackend) Usage() (*usage.Report, error) {
	provs := []usage.ProviderInput{{Name: "groq", Kind: "keyed", Keys: 1, Limits: config.Limits{RPM: 30}}, {Name: "idle", Keys: -1}}
	return usage.Build(time.Now(), provs, f.events, &usage.Live{}), nil
}
func (f *fakeBackend) History(limit int) ([]ledger.Event, error) { return f.events, nil }
func (f *fakeBackend) Control() (*ControlState, error) {
	return &ControlState{Settings: f.settings, Effective: f.settings.Resolve(control.Override{})}, nil
}
func (f *fakeBackend) SetControl(s control.Settings) error { f.settings = s; return nil }
func (f *fakeBackend) SaveControl(s control.Settings) (string, error) {
	f.settings = s
	return "/tmp/x.yaml", nil
}
func (f *fakeBackend) ClearCooldowns(string) (int, error) { f.cleared++; return 2, nil }
func (f *fakeBackend) Chat(_ context.Context, model, session string, msgs []router.Message, onDelta func(string)) (string, error) {
	onDelta("hello ")
	onDelta("there")
	return "groq/llama", nil
}

func sampleEvents() []ledger.Event {
	now := time.Now()
	return []ledger.Event{
		{T: now.Add(-3 * time.Minute), Kind: ledger.KindSession, Session: "s1", Action: "start"},
		{T: now.Add(-2 * time.Minute), Kind: ledger.KindRequest, ID: "r1", Session: "s1", Model: "auto", Via: "groq/llama", Status: 200, Tokens: &ledger.Tokens{Prompt: 5, Completion: 7}, Prompt: "hi"},
		{T: now.Add(-2 * time.Minute), Kind: ledger.KindAttempt, ID: "r1", Session: "s1", Via: "groq/llama", Provider: "groq", Attempt: 1, Status: 200, Tokens: &ledger.Tokens{Prompt: 5, Completion: 7}},
		{T: now.Add(-1 * time.Minute), Kind: ledger.KindRequest, ID: "r2", Model: "auto", Status: 502, Err: "all failed"},
		{T: now.Add(-1 * time.Minute), Kind: ledger.KindAttempt, ID: "r2", Via: "groq/llama", Provider: "groq", Attempt: 1, Status: 429, Err: "rate limited"},
	}
}

// run applies msg and executes returned commands (one level, plus batches)
// so async results are fed back like the bubbletea runtime would.
func run(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	nm, cmd := m.Update(msg)
	m = nm.(Model)
	return drain(t, m, cmd, 0)
}

func drain(t *testing.T, m Model, cmd tea.Cmd, depth int) Model {
	if cmd == nil || depth > 20 {
		return m
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-out:
	case <-time.After(300 * time.Millisecond): // ticks/blink: not needed
		return m
	}
	switch v := msg.(type) {
	case nil, tickMsg:
		return m
	case tea.BatchMsg:
		for _, c := range v {
			m = drain(t, m, c, depth+1)
		}
		return m
	}
	nm, next := m.Update(msg)
	return drain(t, nm.(Model), next, depth+1)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func setup(t *testing.T) (Model, *fakeBackend) {
	be := &fakeBackend{events: sampleEvents()}
	m := New(be, Options{Session: "t1"})
	m = run(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	m = drain(t, m, m.fetch(), 0)
	return m, be
}

func TestTabsRender(t *testing.T) {
	m, _ := setup(t)
	want := map[string]string{
		"1": "requests today", "2": "groq", "3": "REQ", "4": "s1", "6": "Profile",
	}
	for k, w := range want {
		m = run(t, m, key(k))
		if v := m.View(); !strings.Contains(v, w) {
			t.Errorf("tab %s: view lacks %q:\n%s", k, w, v)
		}
	}
}

func TestHistoryFilters(t *testing.T) {
	m, _ := setup(t)
	m = run(t, m, key("3"))
	if n := len(m.filteredEvents()); n != 5 {
		t.Fatalf("all events = %d, want 5", n)
	}
	m = run(t, m, key("e"))
	if n := len(m.filteredEvents()); n != 2 {
		t.Fatalf("errors = %d, want 2", n)
	}
	m = run(t, m, key("t")) // kind → request
	if n := len(m.filteredEvents()); n != 1 {
		t.Fatalf("errors+request = %d, want 1", n)
	}
	m = run(t, m, key("x"))
	m = run(t, m, key("/"))
	for _, r := range "rate" {
		m = run(t, m, key(string(r)))
	}
	m = run(t, m, key("enter"))
	if n := len(m.filteredEvents()); n != 1 {
		t.Fatalf("text filter = %d, want 1", n)
	}
	m = run(t, m, key("enter")) // details
	if !m.hDetail || !strings.Contains(m.View(), "rate limited") {
		t.Fatalf("detail view missing:\n%s", m.View())
	}
}

func TestSessionsJumpToHistory(t *testing.T) {
	m, _ := setup(t)
	m = run(t, m, key("4"))
	m = run(t, m, key("enter"))
	if m.tab != tabHistory || m.hSession != "s1" {
		t.Fatalf("tab=%d session=%q", m.tab, m.hSession)
	}
	if n := len(m.filteredEvents()); n != 3 {
		t.Fatalf("session events = %d, want 3", n)
	}
}

func TestChatStreams(t *testing.T) {
	m, _ := setup(t)
	m = run(t, m, key("5"))
	if !m.input.Focused() {
		t.Fatal("chat input should focus")
	}
	for _, r := range "hi" {
		m = run(t, m, key(string(r)))
	}
	m = run(t, m, key("enter"))
	if m.streaming {
		t.Fatal("still streaming after done")
	}
	if len(m.msgs) != 2 || m.msgs[1].Content != "hello there" {
		t.Fatalf("msgs = %+v", m.msgs)
	}
	if !strings.Contains(m.View(), "via groq/llama") {
		t.Fatalf("view lacks via:\n%s", m.View())
	}
	m = run(t, m, key("/"))
	for _, r := range "model free" {
		m = run(t, m, key(string(r)))
	}
	m = run(t, m, key("enter"))
	if m.chatModel != "free" {
		t.Fatalf("model = %q", m.chatModel)
	}
}

func TestControlEdits(t *testing.T) {
	m, be := setup(t)
	m = run(t, m, key("6"))
	m = run(t, m, key("enter")) // profile balanced → auto
	if be.settings.Profile != control.ProfileAuto {
		t.Fatalf("profile = %q", be.settings.Profile)
	}
	m = run(t, m, key("down"))
	m = run(t, m, key("enter")) // paused
	if !be.settings.Paused {
		t.Fatal("not paused")
	}
	// provider rows come after the fixed items
	for i := 0; i < 20 && m.controlItems()[m.cCursor].provider != "groq"; i++ {
		m = run(t, m, key("down"))
	}
	m = run(t, m, key(" "))
	if len(be.settings.Disabled) != 1 || be.settings.Disabled[0] != "groq" {
		t.Fatalf("disabled = %v", be.settings.Disabled)
	}
	m = run(t, m, key("s"))
	if !strings.Contains(m.status, "saved") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestUsageDetailAndAll(t *testing.T) {
	m, be := setup(t)
	m = run(t, m, key("2"))
	if n := len(m.usageRows()); n != 1 { // idle hidden
		t.Fatalf("rows = %d", n)
	}
	m = run(t, m, key("a"))
	if n := len(m.usageRows()); n != 2 {
		t.Fatalf("rows with all = %d", n)
	}
	m = run(t, m, key("enter"))
	if !strings.Contains(m.View(), "declared limits") {
		t.Fatalf("no detail:\n%s", m.View())
	}
	m = run(t, m, key("c"))
	if be.cleared != 1 {
		t.Fatal("cooldowns not cleared")
	}
}
