package cli

import (
	"os"
	"testing"
)

// TestMain keeps the CLI's ledger (history/usage) out of the real
// $HOME: every command that routes a call appends to it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "freellm-cli-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_DATA_HOME", dir)
	os.Unsetenv("FREELLM_URL")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
