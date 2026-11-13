package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFile(t *testing.T) {
	base := t.TempDir()
	want := filepath.Join(base, Name, "config.yaml")
	legacy := filepath.Join(base, LegacyName, "config.yaml")

	if got := File(base, "config.yaml"); got != want {
		t.Fatalf("no files: got %s, want %s", got, want)
	}
	touch(t, legacy)
	if got := File(base, "config.yaml"); got != legacy {
		t.Fatalf("legacy only: got %s, want %s", got, legacy)
	}
	touch(t, want)
	if got := File(base, "config.yaml"); got != want {
		t.Fatalf("both: got %s, want %s", got, want)
	}
}
