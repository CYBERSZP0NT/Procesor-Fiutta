package agentic

import "strings"

// Task-router modes.
const (
	// ModeRozkladJazdy ("[Rozkład Jazdy]") always goes all in: the strongest
	// model available, the biggest context, cost and speed ignored, tier caps
	// from rules ignored. The default mode.
	ModeRozkladJazdy = "rozklad-jazdy"
	// ModeJakoTako ("Jako Tako") is "just enough": the cheapest model that
	// reaches the tier the task needs, stronger ones only as escalation.
	ModeJakoTako = "jako-tako"
	// ModeLive ("Live") goes all in like [Rozkład Jazdy] but ranks by
	// richness, fed by the signed FreeLLMAPI live catalog: size class,
	// then context window, then free daily capacity. It becomes the
	// default when a working FreeLLMAPI key is found (internal/live).
	ModeLive = "live"
)

// DefaultMode is used when neither the config nor the request names one.
const DefaultMode = ModeRozkladJazdy

// ModeInfo describes one mode.
type ModeInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// Modes lists the available modes, default first.
var Modes = []ModeInfo{
	{ModeRozkladJazdy, "[Rozkład Jazdy]", "all in: strongest available model first, biggest context, cost and tier caps ignored"},
	{ModeJakoTako, "Jako Tako", "just enough: cheapest model that reaches the required tier, stronger ones as escalation"},
	{ModeLive, "Live", "by richness: FreeLLMAPI live catalog, biggest size class, context and free quota first (default with a working FreeLLMAPI key)"},
}

// ModeIDs is the list of mode ids for messages ("rozklad-jazdy | …").
func ModeIDs() string {
	ids := make([]string, len(Modes))
	for i, m := range Modes {
		ids[i] = m.ID
	}
	return strings.Join(ids, " | ")
}

// allIn reports whether a mode ignores tier caps and cost (every
// eligible entry in the chain, strongest first).
func allIn(mode string) bool { return mode == ModeRozkladJazdy || mode == ModeLive }

var modeAliases = map[string]string{
	"rozklad-jazdy": ModeRozkladJazdy,
	"rozklad":       ModeRozkladJazdy,
	"rj":            ModeRozkladJazdy,
	"max":           ModeRozkladJazdy,
	"full":          ModeRozkladJazdy,
	"jako-tako":     ModeJakoTako,
	"jakotako":      ModeJakoTako,
	"jt":            ModeJakoTako,
	"balanced":      ModeJakoTako,
	"just-enough":   ModeJakoTako,
	"live":          ModeLive,
	"bogaty":        ModeLive,
	"bogato":        ModeLive,
	"po-bogatosci":  ModeLive,
	"rich":          ModeLive,
	"richness":      ModeLive,
	"freellmapi":    ModeLive,
}

// NormalizeMode maps any spelling ("[Rozkład Jazdy]", "rozklad jazdy",
// "Jako Tako", "max", …) to a mode id. ok is false for unknown names.
func NormalizeMode(s string) (id string, ok bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.Trim(k, "[]")
	k = strings.NewReplacer("ł", "l", "ą", "a", "ó", "o", "ś", "s", "ć", "c", "_", "-", " ", "-").Replace(strings.TrimSpace(k))
	id, ok = modeAliases[k]
	return id, ok
}

// ModeName returns the display name of a mode id.
func ModeName(id string) string {
	for _, m := range Modes {
		if m.ID == id {
			return m.Name
		}
	}
	return id
}

// EffectiveMode returns the configured mode (normalized), else DefaultMode.
func (s Spec) EffectiveMode() string {
	if id, ok := NormalizeMode(s.Mode); ok {
		return id
	}
	return DefaultMode
}

// joinModeWords turns multi-word mode names into single tokens so that
// override strings can be split on spaces and colons.
func joinModeWords(s string) string {
	return strings.NewReplacer(
		"[rozkład jazdy]", ModeRozkladJazdy,
		"rozkład jazdy", ModeRozkladJazdy,
		"rozklad jazdy", ModeRozkladJazdy,
		"jako tako", ModeJakoTako,
		"po bogatości", ModeLive,
		"po bogatosci", ModeLive,
	).Replace(s)
}
