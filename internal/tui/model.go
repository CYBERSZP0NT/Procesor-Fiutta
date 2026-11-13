package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/silverx/freellm-router/internal/control"
	"github.com/silverx/freellm-router/internal/ledger"
	"github.com/silverx/freellm-router/internal/usage"
	"github.com/silverx/freellm-router/pkg/router"
)

type tab int

const (
	tabDash tab = iota
	tabUsage
	tabHistory
	tabSessions
	tabChat
	tabControl
)

var tabNames = []string{"Dashboard", "Usage", "History", "Sessions", "Chat", "Control"}

// history kind filter cycle ("" = all).
var kindCycle = []string{"", ledger.KindRequest, ledger.KindAttempt, ledger.KindTask, ledger.KindSession, ledger.KindControl, ledger.KindHTTP}

// Options configure the TUI.
type Options struct {
	Interval time.Duration // refresh period (default 2s)
	Model    string        // chat model (default "auto")
	Session  string        // chat session id (default tui-<time>)
	StartTab string        // dashboard | usage | history | sessions | chat | control
}

// Model is the bubbletea model.
type Model struct {
	be   Backend
	opts Options
	w, h int
	tab  tab
	help bool

	// data
	rep        *usage.Report
	events     []ledger.Event
	sessions   []ledger.SessionSummary
	ctl        *ControlState
	dataErr    string
	refreshing bool
	lastFetch  time.Time

	status   string
	statusAt time.Time

	// usage
	uCursor int
	uAll    bool
	uDetail bool

	// history
	hCursor    int // index into the filtered list
	hFollow    bool
	hErrors    bool
	hKind      int
	hSession   string
	hFilter    textinput.Model
	hFiltering bool
	hDetail    bool
	detail     viewport.Model

	// sessions
	sCursor int

	// chat
	input     textinput.Model
	chatVP    viewport.Model
	msgs      []router.Message
	lines     []chatLine
	chatModel string
	session   string
	streaming bool
	chatCh    chan tea.Msg
	cancel    context.CancelFunc

	// control
	cCursor int
}

type chatLine struct {
	role string // user | assistant | info | error
	text string
}

// New builds the model.
func New(be Backend, o Options) Model {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.Model == "" {
		o.Model = "auto"
	}
	if o.Session == "" {
		o.Session = "tui-" + time.Now().Format("0102-150405")
	}
	in := textinput.New()
	in.Placeholder = "message… (/model X · /session X · /new · /help)"
	in.Prompt = "› "
	in.CharLimit = 0
	hf := textinput.New()
	hf.Placeholder = "filter text…"
	hf.Prompt = "/ "
	m := Model{be: be, opts: o, input: in, hFilter: hf, chatModel: o.Model, session: o.Session, hFollow: true}
	for i, n := range tabNames {
		if strings.EqualFold(n, o.StartTab) {
			m.tab = tab(i)
		}
	}
	m.lines = []chatLine{{role: "info", text: fmt.Sprintf("chat via %s · model %s · session %s", be.Describe(), m.chatModel, m.session)}}
	return m
}

// ---------------------------------------------------------------------------
// messages
// ---------------------------------------------------------------------------

type tickMsg time.Time

type dataMsg struct {
	rep    *usage.Report
	events []ledger.Event
	ctl    *ControlState
	err    error
}

type chatDeltaMsg string

type chatDoneMsg struct {
	via string
	err error
}

type statusMsg string

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) fetch() tea.Cmd {
	be := m.be
	return func() tea.Msg {
		var d dataMsg
		var errs []string
		if rep, err := be.Usage(); err != nil {
			errs = append(errs, "usage: "+err.Error())
		} else {
			d.rep = rep
		}
		if evs, err := be.History(3000); err != nil {
			errs = append(errs, "history: "+err.Error())
		} else {
			d.events = evs
		}
		if ctl, err := be.Control(); err != nil {
			errs = append(errs, "control: "+err.Error())
		} else {
			d.ctl = ctl
		}
		if len(errs) > 0 {
			d.err = fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return d
	}
}

// Init starts the refresh loop.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), tick(m.opts.Interval), textinput.Blink)
}

func (m *Model) setStatus(s string) {
	m.status, m.statusAt = s, time.Now()
}

// ---------------------------------------------------------------------------
// update
// ---------------------------------------------------------------------------

// Update handles one message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{tick(m.opts.Interval)}
		if !m.refreshing {
			m.refreshing = true
			cmds = append(cmds, m.fetch())
		}
		return m, tea.Batch(cmds...)

	case dataMsg:
		m.refreshing = false
		m.lastFetch = time.Now()
		if msg.err != nil {
			m.dataErr = msg.err.Error()
		} else {
			m.dataErr = ""
		}
		if msg.rep != nil {
			m.rep = msg.rep
		}
		if msg.events != nil {
			m.events = msg.events
			m.sessions = ledger.Sessions(msg.events)
		}
		if msg.ctl != nil {
			m.ctl = msg.ctl
		}
		if m.hFollow {
			m.hCursor = len(m.filteredEvents()) - 1
		}
		m.clampCursors()
		return m, nil

	case statusMsg:
		m.setStatus(string(msg))
		return m, m.fetch()

	case chatDeltaMsg:
		if n := len(m.lines); n > 0 && m.lines[n-1].role == "assistant" {
			m.lines[n-1].text += string(msg)
		}
		m.refreshChat()
		return m, waitChat(m.chatCh)

	case chatDoneMsg:
		m.streaming = false
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		n := len(m.lines)
		if msg.err != nil {
			if n > 0 && m.lines[n-1].role == "assistant" && m.lines[n-1].text == "" {
				m.lines = m.lines[:n-1]
			}
			m.lines = append(m.lines, chatLine{role: "error", text: msg.err.Error()})
			if len(m.msgs) > 0 { // drop the unanswered user turn
				m.msgs = m.msgs[:len(m.msgs)-1]
			}
		} else {
			answer := m.lines[n-1].text
			m.msgs = append(m.msgs, router.Message{Role: "assistant", Content: answer})
			m.lines = append(m.lines, chatLine{role: "info", text: "↳ via " + msg.via})
		}
		m.refreshChat()
		return m, m.fetch()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) layout() {
	bodyH := max(m.h-4, 3)
	m.detail.Width, m.detail.Height = m.w, bodyH
	m.chatVP.Width, m.chatVP.Height = m.w, max(bodyH-2, 1)
	m.input.Width = max(m.w-4, 10)
	m.refreshChat()
}

func (m *Model) clampCursors() {
	clamp := func(c, n int) int { return max(0, min(c, n-1)) }
	m.uCursor = clamp(m.uCursor, len(m.usageRows()))
	m.hCursor = clamp(m.hCursor, len(m.filteredEvents()))
	m.sCursor = clamp(m.sCursor, len(m.sessions))
	m.cCursor = clamp(m.cCursor, len(m.controlItems()))
}

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}

	// Text inputs capture keys while focused.
	if m.tab == tabChat && m.input.Focused() {
		switch key {
		case "esc":
			m.input.Blur()
			return m, nil
		case "enter":
			return m.sendChat()
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.chatVP, cmd = m.chatVP.Update(k)
			return m, cmd
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	if m.hFiltering {
		switch key {
		case "esc", "enter":
			m.hFiltering = false
			m.hFilter.Blur()
			if key == "esc" {
				m.hFilter.SetValue("")
			}
			m.hCursor = len(m.filteredEvents()) - 1
			return m, nil
		}
		var cmd tea.Cmd
		m.hFilter, cmd = m.hFilter.Update(k)
		m.hCursor = len(m.filteredEvents()) - 1
		return m, cmd
	}
	if m.hDetail {
		switch key {
		case "esc", "enter", "q", "backspace":
			m.hDetail = false
			return m, nil
		}
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(k)
		return m, cmd
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = !m.help
		return m, nil
	case "r":
		m.setStatus("refreshing…")
		return m, m.fetch()
	case "tab", "right", "l":
		if m.tab != tabControl || (key == "tab") {
			m.tab = (m.tab + 1) % tab(len(tabNames))
			return m, m.onEnterTab()
		}
	case "shift+tab", "left", "h":
		if m.tab != tabControl || key == "shift+tab" {
			m.tab = (m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames))
			return m, m.onEnterTab()
		}
	case "1", "2", "3", "4", "5", "6":
		m.tab = tab(key[0] - '1')
		return m, m.onEnterTab()
	}

	switch m.tab {
	case tabUsage:
		return m.keyUsage(key)
	case tabHistory:
		return m.keyHistory(key)
	case tabSessions:
		return m.keySessions(key)
	case tabChat:
		return m.keyChat(key)
	case tabControl:
		return m.keyControl(key)
	}
	return m, nil
}

func (m *Model) onEnterTab() tea.Cmd {
	if m.tab == tabChat {
		return m.input.Focus()
	}
	m.input.Blur()
	return nil
}

func moveCursor(c *int, n int, key string, page int) bool {
	switch key {
	case "up", "k":
		*c--
	case "down", "j":
		*c++
	case "pgup", "ctrl+u":
		*c -= page
	case "pgdown", "ctrl+d":
		*c += page
	case "home", "g":
		*c = 0
	case "end", "G":
		*c = n - 1
	default:
		return false
	}
	*c = max(0, min(*c, n-1))
	return true
}

func (m Model) page() int { return max(m.h-8, 1) }

func (m Model) keyUsage(key string) (tea.Model, tea.Cmd) {
	if moveCursor(&m.uCursor, len(m.usageRows()), key, m.page()) {
		return m, nil
	}
	switch key {
	case "a":
		m.uAll = !m.uAll
		m.clampCursors()
	case "enter", " ":
		m.uDetail = !m.uDetail
	case "c":
		rows := m.usageRows()
		if len(rows) > 0 {
			name := rows[m.uCursor].Name
			return m, m.clearCooldowns(name)
		}
	case "d":
		rows := m.usageRows()
		if len(rows) > 0 && m.ctl != nil {
			return m, m.toggleDisabled(rows[m.uCursor].Name)
		}
	}
	return m, nil
}

func (m Model) keyHistory(key string) (tea.Model, tea.Cmd) {
	evs := m.filteredEvents()
	if moveCursor(&m.hCursor, len(evs), key, m.page()) {
		m.hFollow = m.hCursor >= len(evs)-1
		return m, nil
	}
	switch key {
	case "/":
		m.hFiltering = true
		return m, m.hFilter.Focus()
	case "f":
		m.hFollow = !m.hFollow
		if m.hFollow {
			m.hCursor = len(evs) - 1
		}
	case "e":
		m.hErrors = !m.hErrors
		m.hCursor = len(m.filteredEvents()) - 1
	case "t", "K":
		m.hKind = (m.hKind + 1) % len(kindCycle)
		m.hCursor = len(m.filteredEvents()) - 1
	case "s":
		m.hSession = ""
		m.hCursor = len(m.filteredEvents()) - 1
	case "x":
		m.hErrors, m.hKind, m.hSession = false, 0, ""
		m.hFilter.SetValue("")
		m.hCursor = len(m.filteredEvents()) - 1
	case "enter":
		if len(evs) > 0 {
			m.hDetail = true
			m.detail.SetContent(m.eventDetail(evs[m.hCursor]))
			m.detail.GotoTop()
		}
	}
	return m, nil
}

func (m Model) keySessions(key string) (tea.Model, tea.Cmd) {
	if moveCursor(&m.sCursor, len(m.sessions), key, m.page()) {
		return m, nil
	}
	switch key {
	case "enter":
		if len(m.sessions) > 0 {
			m.hSession = m.sessions[m.sCursor].ID
			m.hKind, m.hErrors = 0, false
			m.tab = tabHistory
			m.hFollow = true
			m.hCursor = len(m.filteredEvents()) - 1
		}
	case "c":
		if len(m.sessions) > 0 {
			m.session = m.sessions[m.sCursor].ID
			m.tab = tabChat
			m.lines = append(m.lines, chatLine{role: "info", text: "session → " + m.session})
			m.refreshChat()
			return m, m.input.Focus()
		}
	}
	return m, nil
}

func (m Model) keyChat(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "i", "enter", "a":
		return m, m.input.Focus()
	case "esc":
		if m.streaming && m.cancel != nil {
			m.cancel()
		}
	default:
		switch key {
		case "up", "k":
			m.chatVP.ScrollUp(1)
		case "down", "j":
			m.chatVP.ScrollDown(1)
		case "pgup":
			m.chatVP.PageUp()
		case "pgdown":
			m.chatVP.PageDown()
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// chat
// ---------------------------------------------------------------------------

func waitChat(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m Model) sendChat() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.streaming {
		return m, nil
	}
	m.input.SetValue("")
	if strings.HasPrefix(text, "/") {
		f := strings.Fields(text)
		arg := ""
		if len(f) > 1 {
			arg = strings.Join(f[1:], " ")
		}
		switch f[0] {
		case "/model", "/m":
			if arg != "" {
				m.chatModel = arg
			}
			m.lines = append(m.lines, chatLine{role: "info", text: "model → " + m.chatModel})
		case "/session", "/s":
			if arg != "" {
				m.session = arg
			}
			m.lines = append(m.lines, chatLine{role: "info", text: "session → " + m.session})
		case "/new", "/clear":
			m.msgs = nil
			m.lines = []chatLine{{role: "info", text: "new conversation · model " + m.chatModel + " · session " + m.session}}
		case "/system":
			m.msgs = append([]router.Message{{Role: "system", Content: arg}}, m.msgs...)
			m.lines = append(m.lines, chatLine{role: "info", text: "system prompt set"})
		default:
			m.lines = append(m.lines, chatLine{role: "info", text: "commands: /model <alias|provider/model> · /session <id> · /system <text> · /new"})
		}
		m.refreshChat()
		return m, nil
	}

	m.msgs = append(m.msgs, router.Message{Role: "user", Content: text})
	m.lines = append(m.lines, chatLine{role: "user", text: text}, chatLine{role: "assistant"})
	m.streaming = true
	m.refreshChat()

	ch := make(chan tea.Msg, 64)
	m.chatCh = ch
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	be, model, session := m.be, m.chatModel, m.session
	msgs := append([]router.Message(nil), m.msgs...)
	go func() {
		via, err := be.Chat(ctx, model, session, msgs, func(s string) { ch <- chatDeltaMsg(s) })
		ch <- chatDoneMsg{via: via, err: err}
	}()
	return m, waitChat(ch)
}

func (m *Model) refreshChat() {
	if m.chatVP.Width == 0 {
		return
	}
	var b strings.Builder
	w := max(m.chatVP.Width-2, 20)
	for _, l := range m.lines {
		switch l.role {
		case "user":
			b.WriteString(sUser.Render("you ") + wrap(l.text, w-4) + "\n\n")
		case "assistant":
			t := l.text
			if t == "" && m.streaming {
				t = sDim.Render("…")
			}
			b.WriteString(sBot.Render("llm ") + wrap(t, w-4) + "\n\n")
		case "error":
			b.WriteString(sErr.Render("✗ "+wrap(l.text, w-2)) + "\n\n")
		default:
			b.WriteString(sDim.Render(wrap(l.text, w)) + "\n")
		}
	}
	m.chatVP.SetContent(b.String())
	m.chatVP.GotoBottom()
}

// ---------------------------------------------------------------------------
// control
// ---------------------------------------------------------------------------

type controlItem struct {
	label    string
	value    string
	provider string // provider rows
	act      func(s *control.Settings, dir int)
	special  string // "cooldowns" | "save"
}

func triState(p *bool) string {
	if p == nil {
		return "default (profile)"
	}
	if *p {
		return "on"
	}
	return "off"
}

func cycleTri(p **bool) {
	switch {
	case *p == nil:
		*p = control.Bool(true)
	case **p:
		*p = control.Bool(false)
	default:
		*p = nil
	}
}

func (m Model) controlItems() []controlItem {
	if m.ctl == nil {
		return nil
	}
	s, e := m.ctl.Settings, m.ctl.Effective
	esc := "default (" + dashIf(e.Escalate) + ")"
	if s.Escalate != nil {
		esc = dashIf(*s.Escalate)
	}
	items := []controlItem{
		{label: "Profile", value: e.Profile, act: func(s *control.Settings, dir int) {
			order := []string{control.ProfileBalanced, control.ProfileAuto, control.ProfileControl}
			cur, _ := control.NormalizeProfile(s.Profile)
			i := 0
			for j, p := range order {
				if p == cur {
					i = j
				}
			}
			s.Profile = order[(i+dir+len(order))%len(order)]
		}},
		{label: "Paused", value: onOff(s.Paused), act: func(s *control.Settings, _ int) { s.Paused = !s.Paused }},
		{label: "Fallback", value: triState(s.Fallback) + " → " + onOff(e.Fallback), act: func(s *control.Settings, _ int) { cycleTri(&s.Fallback) }},
		{label: "Forever (retry until answered)", value: triState(s.Forever) + " → " + onOff(e.Forever), act: func(s *control.Settings, _ int) { cycleTri(&s.Forever) }},
		{label: "Enforce limits", value: triState(s.EnforceLimits) + " → " + onOff(e.EnforceLimits), act: func(s *control.Settings, _ int) { cycleTri(&s.EnforceLimits) }},
		{label: "Max attempts", value: zeroDefault(s.MaxAttempts, ""), act: func(s *control.Settings, dir int) { s.MaxAttempts = max(0, s.MaxAttempts+dir) }},
		{label: "Max wait (forever)", value: zeroDefault(s.MaxWaitSec, "s"), act: func(s *control.Settings, dir int) { s.MaxWaitSec = max(0, s.MaxWaitSec+30*dir) }},
		{label: "Escalate to", value: esc, act: func(s *control.Settings, dir int) {
			order := []string{"<default>", "", "auto", "free", "agent"}
			cur := "<default>"
			if s.Escalate != nil {
				cur = *s.Escalate
			}
			i := 0
			for j, o := range order {
				if o == cur {
					i = j
				}
			}
			next := order[(i+dir+len(order))%len(order)]
			if next == "<default>" {
				s.Escalate = nil
			} else {
				s.Escalate = control.String(next)
			}
		}},
		{label: "Pre-call hook", value: dashIf(strings.Join(s.Hooks.PreCall, " ")) + "  (set: freellm-router ctl hook pre …)"},
		{label: fmt.Sprintf("Clear cooldowns (%d active)", len(m.ctl.Cooldowns)), special: "cooldowns"},
		{label: "Save to config file", special: "save"},
	}
	if m.rep != nil {
		for _, p := range m.rep.Providers {
			name := p.Name
			state := "enabled"
			if e.IsDisabled(name) {
				state = "DISABLED"
			} else if !e.Allowed(name) {
				state = "not allowed"
			}
			if len(s.Allow) > 0 && containsFold(s.Allow, name) {
				state += " · allow-listed"
			}
			items = append(items, controlItem{label: "provider " + name, value: state + " · " + p.Status, provider: name})
		}
	}
	return items
}

func (m Model) keyControl(key string) (tea.Model, tea.Cmd) {
	items := m.controlItems()
	if moveCursor(&m.cCursor, len(items), key, m.page()) {
		return m, nil
	}
	if len(items) == 0 || m.ctl == nil {
		return m, nil
	}
	it := items[m.cCursor]
	s := m.ctl.Settings
	dir := 1
	switch key {
	case "left", "h", "-":
		dir = -1
	case "right", "l", "+", "=", "enter", " ":
	case "a":
		if it.provider != "" {
			if containsFold(s.Allow, it.provider) {
				s.Allow = removeFold(s.Allow, it.provider)
			} else {
				s.Allow = append(append([]string{}, s.Allow...), strings.ToLower(it.provider))
			}
			return m, m.applyControl(s, "allow-list updated")
		}
		return m, nil
	case "s":
		return m, m.saveControl(s)
	default:
		return m, nil
	}
	switch {
	case it.special == "cooldowns":
		return m, m.clearCooldowns("")
	case it.special == "save":
		return m, m.saveControl(s)
	case it.provider != "":
		return m, m.toggleDisabled(it.provider)
	case it.act != nil:
		it.act(&s, dir)
		return m, m.applyControl(s, it.label+" updated")
	}
	return m, nil
}

func (m Model) applyControl(s control.Settings, msg string) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		if err := be.SetControl(s); err != nil {
			return statusMsg("✗ " + err.Error())
		}
		return statusMsg("✓ " + msg + " (live; s = save to config)")
	}
}

func (m Model) saveControl(s control.Settings) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		path, err := be.SaveControl(s)
		if err != nil {
			return statusMsg("✗ save: " + err.Error())
		}
		return statusMsg("✓ control saved to " + path)
	}
}

func (m Model) toggleDisabled(provider string) tea.Cmd {
	if m.ctl == nil {
		return nil
	}
	s := m.ctl.Settings
	if containsFold(s.Disabled, provider) {
		s.Disabled = removeFold(s.Disabled, provider)
		return m.applyControl(s, provider+" enabled")
	}
	s.Disabled = append(append([]string{}, s.Disabled...), strings.ToLower(provider))
	return m.applyControl(s, provider+" disabled")
}

func (m Model) clearCooldowns(via string) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		n, err := be.ClearCooldowns(via)
		if err != nil {
			return statusMsg("✗ " + err.Error())
		}
		return statusMsg(fmt.Sprintf("✓ cleared %d cooldown(s)", n))
	}
}

// ---------------------------------------------------------------------------
// derived data
// ---------------------------------------------------------------------------

func (m Model) usageRows() []usage.ProviderRow {
	if m.rep == nil {
		return nil
	}
	var out []usage.ProviderRow
	for _, p := range m.rep.Providers {
		if m.uAll || p.Total.Requests > 0 || p.Total.Errors > 0 || !p.Limits.IsZero() || len(p.Cooldowns) > 0 ||
			p.Status == "limit" || p.Status == "disabled" || p.RateLimit != nil {
			out = append(out, p)
		}
	}
	return out
}

func (m Model) filteredEvents() []ledger.Event {
	f := ledger.Filter{Errors: m.hErrors, Session: m.hSession, Query: m.hFilter.Value()}
	if k := kindCycle[m.hKind]; k != "" {
		f.Kinds = []string{k}
	}
	return f.Apply(m.events)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func removeFold(list []string, s string) []string {
	var out []string
	for _, x := range list {
		if !strings.EqualFold(x, s) {
			out = append(out, x)
		}
	}
	return out
}
