package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/silverx/szmelc-router/internal/ledger"
	"github.com/silverx/szmelc-router/internal/usage"
)

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#5A3FD9", Dark: "#A78BFA"}
	cOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#4ADE80"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#FBBF24"}
	cErr    = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F87171"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6E7781", Dark: "#8B949E"}

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sTab    = lipgloss.NewStyle().Padding(0, 1).Foreground(cDim)
	sTabOn  = lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sOK     = lipgloss.NewStyle().Foreground(cOK)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sErr    = lipgloss.NewStyle().Foreground(cErr)
	sBold   = lipgloss.NewStyle().Bold(true)
	sSel    = lipgloss.NewStyle().Reverse(true)
	sUser   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sBot    = lipgloss.NewStyle().Bold(true).Foreground(cOK)
	sBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cDim).Padding(0, 1)
	sHeader = lipgloss.NewStyle().Bold(true).Underline(true)
)

// View renders the screen.
func (m Model) View() string {
	if m.w == 0 {
		return "loading…"
	}
	header := m.viewHeader()
	footer := m.viewFooter()
	bodyH := max(m.h-lipgloss.Height(header)-lipgloss.Height(footer), 1)
	var body string
	switch {
	case m.help:
		body = m.viewHelp()
	case m.tab == tabDash:
		body = m.viewDash(bodyH)
	case m.tab == tabUsage:
		body = m.viewUsage(bodyH)
	case m.tab == tabHistory:
		body = m.viewHistory(bodyH)
	case m.tab == tabSessions:
		body = m.viewSessions(bodyH)
	case m.tab == tabChat:
		body = m.viewChat(bodyH)
	case m.tab == tabControl:
		body = m.viewControl(bodyH)
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) viewHeader() string {
	tabs := []string{sTitle.Render("szmelc-router ")}
	for i, n := range tabNames {
		label := strconv.Itoa(i+1) + " " + n
		if tab(i) == m.tab {
			tabs = append(tabs, sTabOn.Render(label))
		} else {
			tabs = append(tabs, sTab.Render(label))
		}
	}
	left := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	right := sDim.Render(m.be.Describe())
	if m.ctl != nil {
		badge := profileBadge(m.ctl.Effective.Profile)
		if m.ctl.Effective.Paused {
			badge = sErr.Bold(true).Render(" PAUSED ") + " " + badge
		}
		right = badge + " " + right
	}
	gap := max(m.w-lipgloss.Width(left)-lipgloss.Width(right), 1)
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > m.w {
		line = left
	}
	return line + "\n" + sDim.Render(strings.Repeat("─", max(m.w, 1)))
}

func profileBadge(p string) string {
	switch p {
	case "auto":
		return sOK.Bold(true).Render("∞ auto")
	case "control":
		return sWarn.Bold(true).Render("⛭ control")
	}
	return sDim.Render("balanced")
}

func (m Model) viewFooter() string {
	var keys string
	switch {
	case m.hFiltering:
		keys = "type to filter · enter apply · esc clear"
	case m.hDetail:
		keys = "↑↓ scroll · esc back"
	case m.tab == tabDash:
		keys = "1-6/tab switch · r refresh · ? help · q quit"
	case m.tab == tabUsage:
		keys = "↑↓ select · enter details · a all providers · d disable/enable · c clear cooldowns · q quit"
	case m.tab == tabHistory:
		keys = "↑↓ · enter details · / search · e errors · t kind · s all sessions · f follow · x reset · q quit"
	case m.tab == tabSessions:
		keys = "↑↓ · enter show history · c chat in session · q quit"
	case m.tab == tabChat:
		if m.input.Focused() {
			keys = "enter send · esc leave input · /model /session /system /new"
		} else {
			keys = "i/enter type · esc stop stream · ↑↓ scroll · q quit"
		}
	case m.tab == tabControl:
		keys = "↑↓ select · ←→/enter change · space toggle provider · a allow-list · s save to config · q quit"
	}
	status := ""
	if m.status != "" && time.Since(m.statusAt) < 6*time.Second {
		status = m.status
	} else if m.dataErr != "" {
		status = sErr.Render("✗ " + ledger.Clip(m.dataErr, 120))
	} else if !m.lastFetch.IsZero() {
		status = sDim.Render("updated " + m.lastFetch.Format("15:04:05"))
	}
	line := sDim.Render(keys)
	gap := m.w - lipgloss.Width(line) - lipgloss.Width(status)
	if gap < 1 {
		return sDim.Render(strings.Repeat("─", max(m.w, 1))) + "\n" + status
	}
	return sDim.Render(strings.Repeat("─", max(m.w, 1))) + "\n" + line + strings.Repeat(" ", gap) + status
}

func (m Model) viewHelp() string {
	return sBox.Render(strings.Join([]string{
		sTitle.Render("szmelc-router TUI"),
		"",
		"1 Dashboard  totals, requests/min sparkline, top providers, recent failures",
		"2 Usage      usage vs. declared limits, provider-reported quota, cooldowns",
		"3 History    every request, upstream attempt, task decision, session, control change",
		"4 Sessions   per-session totals; enter jumps to that session's history",
		"5 Chat       talk to any route/model; /model, /session, /system, /new",
		"6 Control    profile auto (∞) ⇄ control (⛭), pause, fallback, forever,",
		"             budgets, escalation, providers on/off, allow-list, save",
		"",
		"Profiles: auto = infinite & full auto (fallback, wait out cooldowns, retry until",
		"answered, escalate) · control = exactly what you asked for, no fallback, no silent",
		"retries, budgets enforced, hooks may veto · balanced = classic.",
		"",
		"Data: " + m.be.Describe(),
		"",
		sDim.Render("? closes this help"),
	}, "\n"))
}

// ---------------------------------------------------------------------------
// dashboard
// ---------------------------------------------------------------------------

func (m Model) viewDash(h int) string {
	if m.rep == nil {
		return sDim.Render("loading…")
	}
	rep := m.rep
	rq := rep.Requests
	failRate := 0.0
	if rq.Day.Requests > 0 {
		failRate = 100 * float64(rq.Day.Errors) / float64(rq.Day.Requests)
	}
	cds, limited, ok := 0, 0, 0
	for _, p := range rep.Providers {
		cds += len(p.Cooldowns)
		switch p.Status {
		case "limit":
			limited++
		case "ok":
			if p.Total.Requests > 0 {
				ok++
			}
		}
	}
	box := func(title, value, sub string) string {
		return sBox.Width(max((m.w-10)/4, 16)).Render(sDim.Render(title) + "\n" + sBold.Render(value) + "\n" + sDim.Render(sub))
	}
	boxes := lipgloss.JoinHorizontal(lipgloss.Top,
		box("requests today", strconv.Itoa(rq.Today.Requests), fmt.Sprintf("%d failed · total %d", rq.Today.Errors, rq.Total.Requests)),
		box("last 24h", strconv.Itoa(rq.Day.Requests), fmt.Sprintf("%.1f%% failed", failRate)),
		box("tokens 24h", compact(rep.Totals.Day.Tokens), fmt.Sprintf("%d upstream calls", rep.Totals.Day.Requests)),
		box("providers", fmt.Sprintf("%d active", ok), fmt.Sprintf("%d limited · %d cooldowns", limited, cds)),
	)

	var b strings.Builder
	b.WriteString(boxes + "\n")
	if m.ctl != nil {
		e := m.ctl.Effective
		b.WriteString(fmt.Sprintf(" profile %s · fallback %s · forever %s · limits %s · task mode %s · in flight %d\n\n",
			profileBadge(e.Profile), onOff(e.Fallback), onOff(e.Forever), onOff(e.EnforceLimits), dashIf(m.ctl.TaskMode), rep.InFlight))
	}

	// requests per minute, last 60 minutes
	spark, peak := m.sparkline(60)
	b.WriteString(sHeader.Render("requests / minute (last 60 min)") + sDim.Render(fmt.Sprintf("  peak %d", peak)) + "\n")
	b.WriteString(sOK.Render(spark) + "\n\n")

	// top providers 24h + recent failures side by side
	left := m.topProviders(8)
	right := m.recentFailures(8)
	colW := max((m.w-4)/2, 30)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(colW).Render(left),
		lipgloss.NewStyle().Width(colW).Render(right)))
	_ = h
	return b.String()
}

func (m Model) sparkline(minutes int) (string, int) {
	buckets := make([]int, minutes)
	now := time.Now()
	for i := range m.events {
		e := &m.events[i]
		if e.EffectiveKind() != ledger.KindRequest {
			continue
		}
		// Wall-clock minutes, so buckets do not shift between refreshes.
		age := int(now.Truncate(time.Minute).Sub(e.T.Truncate(time.Minute)).Minutes())
		if age >= 0 && age < minutes {
			buckets[minutes-1-age]++
		}
	}
	peak := 0
	for _, v := range buckets {
		peak = max(peak, v)
	}
	ticks := []rune("▁▂▃▄▅▆▇█")
	var sb strings.Builder
	for _, v := range buckets {
		if peak == 0 || v == 0 {
			sb.WriteRune('·')
			continue
		}
		sb.WriteRune(ticks[min(v*(len(ticks)-1)/peak, len(ticks)-1)])
	}
	return sb.String(), peak
}

func (m Model) topProviders(n int) string {
	type kv struct {
		name string
		day  usage.Window
	}
	var list []kv
	for _, p := range m.rep.Providers {
		if p.Day.Requests > 0 || p.Day.Errors > 0 {
			list = append(list, kv{p.Name, p.Day})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].day.Requests > list[j].day.Requests })
	var b strings.Builder
	b.WriteString(sHeader.Render("top providers (24h upstream calls)") + "\n")
	if len(list) == 0 {
		b.WriteString(sDim.Render("no calls yet") + "\n")
	}
	top := 1
	if len(list) > 0 {
		top = max(list[0].day.Requests, 1)
	}
	for i, p := range list {
		if i >= n {
			break
		}
		w := p.day.Requests * 20 / top
		b.WriteString(fmt.Sprintf("%-16s %s %d", ledger.Clip(p.name, 16), sOK.Render(strings.Repeat("█", w))+sDim.Render(strings.Repeat("░", 20-w)), p.day.Requests))
		if p.day.Errors > 0 {
			b.WriteString(sErr.Render(fmt.Sprintf("  %d err", p.day.Errors)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) recentFailures(n int) string {
	var b strings.Builder
	b.WriteString(sHeader.Render("recent failures") + "\n")
	count := 0
	for i := len(m.events) - 1; i >= 0 && count < n; i-- {
		e := &m.events[i]
		if !e.Failed() || e.EffectiveKind() == ledger.KindHTTP {
			continue
		}
		count++
		what := e.Via
		if what == "" {
			what = e.Model
		}
		b.WriteString(sDim.Render(e.T.Local().Format("15:04:05")) + " " + sErr.Render(strconv.Itoa(e.Status)) + " " +
			ledger.Clip(what, 28) + " " + sDim.Render(ledger.Clip(oneLine(e.Err), 40)) + "\n")
	}
	if count == 0 {
		b.WriteString(sOK.Render("none 🎉") + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

func (m Model) viewUsage(h int) string {
	if m.rep == nil {
		return sDim.Render("loading…")
	}
	rows := m.usageRows()
	var b strings.Builder
	src := "offline (ledger only)"
	if m.rep.Live {
		src = "live server"
	}
	b.WriteString(sDim.Render(fmt.Sprintf("%s · %d of %d providers shown (a = all) · windows are rolling", src, len(rows), len(m.rep.Providers))) + "\n")
	b.WriteString(sHeader.Render(fmt.Sprintf("%-18s %-12s %-16s %-17s %-14s %-6s %-22s %s",
		"PROVIDER", "STATUS", "1m req/RPM", "24h req/RPD", "24h tokens", "ERR", "UPSTREAM SAYS", "LAST USED")) + "\n")
	listH := h - 2
	if m.uDetail {
		listH = max(h/2-2, 3)
	}
	start := scrollStart(m.uCursor, len(rows), listH)
	for i := start; i < len(rows) && i < start+listH; i++ {
		p := rows[i]
		line := fmt.Sprintf("%-18s %-12s %-16s %-17s %-14s %-6d %-22s %s",
			ledger.Clip(p.Name, 18), p.Status, gauge(p.Minute.Requests, p.Limits.RPM), gauge(p.Day.Requests, p.Limits.RPD),
			gaugeTok(p.Day.Tokens, p.Limits.TPD), p.Day.Errors, ledger.Clip(upstreamSays(&p), 22), agoShort(p.LastUsed))
		switch {
		case i == m.uCursor:
			line = sSel.Render(padTo(line, m.w))
		case p.Status == "limit" || p.Status == "disabled":
			line = sErr.Render(line)
		case p.Status == "cooldown" || p.Status == "unavailable" || p.Status == "no key":
			line = sWarn.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(rows) == 0 {
		b.WriteString(sDim.Render("no usage yet — press a to list every provider") + "\n")
	}
	if m.uDetail && len(rows) > 0 {
		b.WriteString("\n" + m.providerDetail(rows[m.uCursor]))
	}
	return b.String()
}

func (m Model) providerDetail(p usage.ProviderRow) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(p.Name) + sDim.Render(fmt.Sprintf("  kind %s · keys %s · status %s %s", dashIf(p.Kind), keysStr(p.Keys), p.Status, p.Reason)) + "\n")
	if p.Notes != "" {
		b.WriteString(sDim.Render(ledger.Clip(p.Notes, m.w-2)) + "\n")
	}
	l := p.Limits
	b.WriteString(fmt.Sprintf("declared limits: rpm %s · rpd %s · tpm %s · tpd %s   windows: 1m %d req · 1h %d · 24h %d · today %d · total %d\n",
		zeroDefault(l.RPM, ""), zeroDefault(l.RPD, ""), zeroDefault(l.TPM, ""), zeroDefault(l.TPD, ""),
		p.Minute.Requests, p.Hour.Requests, p.Day.Requests, p.Today.Requests, p.Total.Requests))
	if p.RateLimit != nil {
		raw, _ := json.Marshal(p.RateLimit)
		b.WriteString(fmt.Sprintf("provider said (%s via %s): %s\n", agoShort(p.RateLimitAt), p.RateLimitVia, raw))
	}
	for _, cd := range p.Cooldowns {
		b.WriteString(sWarn.Render(fmt.Sprintf("cooldown %s: %s left", cd.Via, (time.Duration(cd.RemainingS)*time.Second).String())) + "\n")
	}
	if p.LastError != "" {
		b.WriteString(sErr.Render("last error: "+oneLine(p.LastError)) + "\n")
	}
	for _, md := range p.Models {
		b.WriteString(fmt.Sprintf("  · %-40s 24h %4d req %8s tok %3d err · last %s · %s\n", ledger.Clip(md.Via, 40), md.Day.Requests,
			compact(md.Day.Tokens), md.Day.Errors, statusStr(md.LastStatus), agoShort(md.LastUsed)))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

func (m Model) viewHistory(h int) string {
	if m.hDetail {
		return m.detail.View()
	}
	evs := m.filteredEvents()
	var filters []string
	if k := kindCycle[m.hKind]; k != "" {
		filters = append(filters, "kind="+k)
	}
	if m.hErrors {
		filters = append(filters, "errors")
	}
	if m.hSession != "" {
		filters = append(filters, "session="+m.hSession)
	}
	if v := m.hFilter.Value(); v != "" {
		filters = append(filters, "text="+v)
	}
	follow := ""
	if m.hFollow {
		follow = sOK.Render(" ● following")
	}
	var b strings.Builder
	head := fmt.Sprintf("%d events", len(evs))
	if len(filters) > 0 {
		head += " · " + strings.Join(filters, " · ")
	}
	b.WriteString(sDim.Render(head) + follow + "\n")
	if m.hFiltering {
		b.WriteString(m.hFilter.View() + "\n")
		h--
	}
	listH := h - 1
	start := scrollStart(m.hCursor, len(evs), listH)
	for i := start; i < len(evs) && i < start+listH; i++ {
		line := padTo(eventLine(&evs[i]), m.w)
		if lipgloss.Width(line) > m.w {
			line = ledger.Clip(line, m.w-1)
		}
		switch {
		case i == m.hCursor:
			line = sSel.Render(line)
		case evs[i].Failed():
			line = sErr.Render(line)
		case evs[i].EffectiveKind() == ledger.KindAttempt || evs[i].EffectiveKind() == ledger.KindHTTP:
			line = sDim.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(evs) == 0 {
		b.WriteString(sDim.Render("no events (x resets filters)") + "\n")
	}
	return b.String()
}

func eventLine(e *ledger.Event) string {
	k := e.EffectiveKind()
	mark := "✓"
	if e.Failed() {
		mark = "✗"
	}
	ts := e.T.Local().Format("01-02 15:04:05")
	dur := (time.Duration(e.LatencyMS) * time.Millisecond).String()
	switch k {
	case ledger.KindRequest:
		s := fmt.Sprintf("%s REQ  %s %3d %s", ts, mark, e.Status, e.Model)
		if e.Via != "" {
			s += " → " + e.Via
		}
		s += "  " + dur
		if t := e.Tokens.Total(); t > 0 {
			s += fmt.Sprintf("  %s tok", compact(t))
		}
		if e.Attempts > 1 {
			s += fmt.Sprintf("  %d tries", e.Attempts)
		}
		if e.Session != "" {
			s += "  [" + e.Session + "]"
		}
		if e.Prompt != "" {
			s += "  “" + ledger.Clip(oneLine(e.Prompt), 40) + "”"
		}
		if e.Err != "" {
			s += "  " + ledger.Clip(oneLine(e.Err), 60)
		}
		return s
	case ledger.KindAttempt:
		s := fmt.Sprintf("%s   ↳ %s %3d #%d %s  %s", ts, mark, e.Status, e.Attempt, e.Via, dur)
		if rl := e.RateLimit; rl != nil && rl.RemainingRequests != nil {
			s += fmt.Sprintf("  quota %d", *rl.RemainingRequests)
		}
		if e.Err != "" {
			s += "  " + ledger.Clip(oneLine(e.Err), 60)
		}
		return s
	case ledger.KindTask:
		if t := e.Task; t != nil {
			return fmt.Sprintf("%s TASK %s role=%s tier=%d → %s", ts, t.Mode, t.Role, t.Tier, ledger.Clip(strings.Join(t.Chain, ", "), 60))
		}
	case ledger.KindSession:
		return fmt.Sprintf("%s SESS %s %s  %s", ts, e.Action, e.Session, ledger.Clip(oneLine(e.Note), 60))
	case ledger.KindControl:
		return fmt.Sprintf("%s CTL  %s %s", ts, e.Action, e.Note)
	}
	return fmt.Sprintf("%s HTTP %s %3d %s %s  %s", ts, mark, e.Status, e.Method, e.Path, dur)
}

func (m Model) eventDetail(e ledger.Event) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(strings.ToUpper(e.EffectiveKind())+" "+e.ID) + "\n\n")
	raw, _ := json.MarshalIndent(e, "", "  ")
	b.WriteString(string(raw) + "\n")
	if e.ID != "" {
		rel := ledger.Related(m.events, e.ID)
		if len(rel) > 1 {
			b.WriteString("\n" + sHeader.Render("everything in this request") + "\n")
			for i := range rel {
				b.WriteString(eventLine(&rel[i]) + "\n")
			}
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

func (m Model) viewSessions(h int) string {
	var b strings.Builder
	b.WriteString(sHeader.Render(fmt.Sprintf("%-28s %-7s %-12s %-10s %-6s %5s %5s %6s %8s  %s",
		"SESSION", "SOURCE", "STARTED", "LAST", "STATE", "REQ", "ERR", "CALLS", "TOKENS", "PROVIDERS")) + "\n")
	start := scrollStart(m.sCursor, len(m.sessions), h-1)
	for i := start; i < len(m.sessions) && i < start+h-1; i++ {
		s := m.sessions[i]
		state := "open"
		if s.Ended {
			state = "ended"
		}
		var provs []string
		for k, v := range s.Providers {
			provs = append(provs, k+":"+strconv.Itoa(v))
		}
		sort.Strings(provs)
		line := fmt.Sprintf("%-28s %-7s %-12s %-10s %-6s %5d %5d %6d %8s  %s", ledger.Clip(s.ID, 28), s.Source,
			s.Start.Local().Format("01-02 15:04"), agoShort(s.Last), state, s.Requests, s.Errors, s.Attempts,
			compact(s.Tokens), strings.Join(provs, " "))
		if i == m.sCursor {
			line = sSel.Render(padTo(line, m.w))
		} else if s.Errors > 0 {
			line = sWarn.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(m.sessions) == 0 {
		b.WriteString(sDim.Render("no sessions yet — szmelc-router wrap --session NAME -- <cmd>, exec --session, or chat here") + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// chat
// ---------------------------------------------------------------------------

func (m Model) viewChat(h int) string {
	state := ""
	if m.streaming {
		state = sOK.Render(" ● streaming (esc to stop)")
	}
	info := sDim.Render(fmt.Sprintf("model %s · session %s", m.chatModel, m.session)) + state
	vp := m.chatVP
	vp.Height = max(h-3, 1)
	return vp.View() + "\n" + info + "\n" + m.input.View()
}

// ---------------------------------------------------------------------------
// control
// ---------------------------------------------------------------------------

func (m Model) viewControl(h int) string {
	if m.ctl == nil {
		return sDim.Render("loading…")
	}
	items := m.controlItems()
	var b strings.Builder
	where := "changes apply to this TUI process; s saves to config"
	if m.be.Live() {
		where = "changes apply live to the server; s also saves to config"
	}
	b.WriteString(sDim.Render(where) + "\n")
	start := scrollStart(m.cCursor, len(items), h-1)
	for i := start; i < len(items) && i < start+h-1; i++ {
		it := items[i]
		line := fmt.Sprintf("  %-34s %s", it.label, it.value)
		if it.special != "" {
			line = "  ▸ " + it.label
		}
		if it.provider != "" && strings.HasPrefix(it.value, "DISABLED") {
			line = sErr.Render(line)
		}
		if i == m.cCursor {
			line = sSel.Render(padTo(line, m.w))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func scrollStart(cursor, n, height int) int {
	if height <= 0 || n <= height {
		return 0
	}
	start := cursor - height/2
	return max(0, min(start, n-height))
}

func padTo(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func wrap(s string, w int) string {
	if w <= 10 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func dashIf(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func zeroDefault(n int, unit string) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n) + unit
}

func keysStr(n int) string {
	if n < 0 {
		return "n/a"
	}
	return strconv.Itoa(n)
}

func statusStr(s int) string {
	if s == 0 {
		return "-"
	}
	return strconv.Itoa(s)
}

func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

func gauge(used, limit int) string {
	if limit <= 0 {
		return strconv.Itoa(used)
	}
	n := min(used*5/limit, 5)
	return fmt.Sprintf("%d/%d %s", used, limit, strings.Repeat("■", n)+strings.Repeat("□", 5-n))
}

func gaugeTok(used, limit int) string {
	if limit <= 0 {
		return compact(used)
	}
	return compact(used) + "/" + compact(limit)
}

func agoShort(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func upstreamSays(p *usage.ProviderRow) string {
	rl := p.RateLimit
	if rl == nil {
		return "-"
	}
	var parts []string
	if rl.RemainingRequests != nil {
		s := strconv.FormatInt(*rl.RemainingRequests, 10)
		if rl.LimitRequests != nil {
			s += "/" + strconv.FormatInt(*rl.LimitRequests, 10)
		}
		parts = append(parts, s+" req")
	}
	if rl.RemainingTokens != nil {
		parts = append(parts, compact(int(*rl.RemainingTokens))+" tok")
	}
	if len(parts) == 0 && rl.RetryAfter != "" {
		parts = append(parts, "retry "+rl.RetryAfter+"s")
	}
	return strings.Join(parts, ", ")
}
