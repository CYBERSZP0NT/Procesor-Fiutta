// Package appdir names the per-user directories szmelc-router keeps its
// config, ledger and cache in, with a fallback to the pre-rebrand name.
package appdir

import (
	"os"
	"path/filepath"
)

const (
	// Name is the directory name under ~/.config, $XDG_DATA_HOME and the
	// user cache dir.
	Name = "szmelc"
	// LegacyName is the directory name used before the szmelc-router
	// rebrand. It is still read when only the legacy file exists.
	LegacyName = "freellm"
)

// File returns base/Name/file. When that file does not exist but
// base/LegacyName/file does, the legacy path is returned instead, so
// existing installs keep their config, usage ledger and catalog cache.
func File(base, file string) string {
	p := filepath.Join(base, Name, file)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	legacy := filepath.Join(base, LegacyName, file)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return p
}
