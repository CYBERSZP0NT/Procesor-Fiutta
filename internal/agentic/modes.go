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
}

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
}

// NormalizeMode maps any spelling ("[Rozkład Jazdy]", "rozklad jazdy",
// "Jako Tako", "max", …) to a mode id. ok is false for unknown names.
func NormalizeMode(s string) (id string, ok bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.Trim(k, "[]")
	k = strings.NewReplacer("ł", "l", "ą", "a", "ó", "o", "_", "-", " ", "-").Replace(strings.TrimSpace(k))
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
	).Replace(s)
}
