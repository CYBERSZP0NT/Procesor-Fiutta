package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePresetsAndOverrides(t *testing.T) {
	auto := Settings{Profile: "auto"}.Resolve(Override{})
	if !auto.Forever || !auto.Fallback || !auto.EnforceLimits || auto.Escalate != "auto" {
		t.Fatalf("auto preset = %+v", auto)
	}
	ctl := Settings{Profile: "control"}.Resolve(Override{})
	if ctl.Fallback || ctl.Forever || !ctl.EnforceLimits {
		t.Fatalf("control preset = %+v", ctl)
	}
	// Explicit settings beat the preset.
	r := Settings{Profile: "auto", Forever: Bool(false), Escalate: String("")}.Resolve(Override{})
	if r.Forever || r.Escalate != "" {
		t.Fatalf("explicit = %+v", r)
	}
	// A per-request profile replaces the preset but keeps operator state.
	r = Settings{Profile: "auto", Paused: true, Disabled: []string{"groq"}}.Resolve(Override{Profile: "control", MaxAttempts: 2})
	if r.Profile != ProfileControl || r.Fallback || !r.Paused || !r.IsDisabled("GROQ") || r.MaxAttempts != 2 {
		t.Fatalf("override = %+v", r)
	}
	if !(Resolved{}).Allowed("x") || (Resolved{Allow: []string{"a"}}).Allowed("b") {
		t.Fatal("allow-list")
	}
}

func TestControllerRejectsBadProfile(t *testing.T) {
	c := New(Settings{})
	if err := c.Replace(Settings{Profile: "nope"}); err == nil {
		t.Fatal("want error")
	}
	var seen string
	c.OnChange(func(_, n Settings) { seen = n.Profile })
	if _, err := c.Update(func(s *Settings) { s.Profile = "infinite" }); err != nil || seen != ProfileAuto {
		t.Fatalf("err=%v seen=%q", err, seen)
	}
}

func script(t *testing.T, body string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{p}
}

func TestPreHook(t *testing.T) {
	ctx := context.Background()
	if v := (Hooks{}).RunPre(ctx, CallInfo{}); !v.Allow {
		t.Fatal("no hook must allow")
	}
	if v := (Hooks{PreCall: script(t, "exit 0")}).RunPre(ctx, CallInfo{}); !v.Allow {
		t.Fatal("exit 0 must allow")
	}
	v := (Hooks{PreCall: script(t, "echo nope >&2; exit 1")}).RunPre(ctx, CallInfo{})
	if v.Allow || v.Reason != "pre-call hook: nope" {
		t.Fatalf("deny = %+v", v)
	}
	v = (Hooks{PreCall: script(t, `cat >/dev/null; echo '{"allow":true,"model":"free"}'`)}).RunPre(ctx, CallInfo{Model: "auto"})
	if !v.Allow || v.Model != "free" {
		t.Fatalf("rewrite = %+v", v)
	}
	v = (Hooks{PreCall: []string{"/nonexistent/hook"}}).RunPre(ctx, CallInfo{})
	if v.Allow {
		t.Fatal("a hook that cannot run must veto (fail closed)")
	}
	v = (Hooks{PreCall: script(t, "sleep 5"), TimeoutSec: 1}).RunPre(ctx, CallInfo{})
	if v.Allow {
		t.Fatal("timed-out hook must veto")
	}
}
