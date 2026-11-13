package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetUserValueKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	orig := "# my config\nroutes:\n  mine: [g4f/gpt-4o-mini] # keep me\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetUserValue(path, []string{"agentic", "mode"}, "jako-tako"); err != nil {
		t.Fatal(err)
	}
	if err := SetUserValue(path, []string{"agentic", "mode"}, "rozklad-jazdy"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	out := string(b)
	for _, want := range []string{"# my config", "# keep me", "mine:", "mode: rozklad-jazdy"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "mode:") != 1 || strings.Contains(out, "jako-tako") {
		t.Errorf("value not replaced in place:\n%s", out)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agentic.Mode != "rozklad-jazdy" || len(cfg.Routes["mine"]) != 1 {
		t.Fatalf("reloaded: mode %q routes %v", cfg.Agentic.Mode, cfg.Routes["mine"])
	}
}

func TestSetUserValueCreatesFileAndEnvWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := SetUserValue(path, []string{"agentic", "mode"}, "jako-tako"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Agentic.Mode != "jako-tako" {
		t.Fatalf("mode %q err %v", cfg.Agentic.Mode, err)
	}
	t.Setenv("FREELLM_TASK_MODE", "rozklad-jazdy")
	if cfg, _ = Load(path); cfg.Agentic.Mode != "rozklad-jazdy" {
		t.Fatalf("FREELLM_TASK_MODE ignored: %q", cfg.Agentic.Mode)
	}
	t.Setenv("FREELLM_TASK_MODE", "turbo")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("unknown mode must fail validation, got %v", err)
	}
}
