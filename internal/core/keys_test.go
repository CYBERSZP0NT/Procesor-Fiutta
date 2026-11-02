package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func tSetenv(t *testing.T, k, v string) {
	t.Helper()
	t.Setenv(k, v)
}

// ---------------------------------------------------------------------------
// Test: EnvKeySource — single key
// ---------------------------------------------------------------------------

func TestEnvKeySource_SingleKey(t *testing.T) {
	tSetenv(t, "TEST_GROQ_KEY", "gsk_abc123")
	src := NewEnvKeySource(map[string]string{
		"groq": "TEST_GROQ_KEY",
	})
	ctx := context.Background()

	keys := src.Keys(ctx, "groq")
	if len(keys) != 1 {
		t.Fatalf("len(keys) = %d, want 1", len(keys))
	}
	if keys[0] != "gsk_abc123" {
		t.Errorf("keys[0] = %q, want gsk_abc123", keys[0])
	}
}

// ---------------------------------------------------------------------------
// Test: EnvKeySource — multiple keys via comma-sep
// ---------------------------------------------------------------------------

func TestEnvKeySource_MultiKeyComma(t *testing.T) {
	tSetenv(t, "TEST_MULTI_KEYS", "key1,key2,key3")
	src := NewEnvKeySource(map[string]string{
		"prov": "TEST_MULTI_KEYS",
	})
	ctx := context.Background()

	keys := src.Keys(ctx, "prov")
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3", len(keys))
	}
	want := []string{"key1", "key2", "key3"}
	for i, w := range want {
		if keys[i] != w {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: EnvKeySource — pipe-separated env vars (multi-variable)
// ---------------------------------------------------------------------------

func TestEnvKeySource_PipeSeparate(t *testing.T) {
	tSetenv(t, "TEST_A_KEY", "aaa111")
	tSetenv(t, "TEST_B_KEY", "bbb222")
	src := NewEnvKeySource(map[string]string{
		"multi": "TEST_A_KEY|TEST_B_KEY",
	})
	ctx := context.Background()

	keys := src.Keys(ctx, "multi")
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(keys))
	}
	if keys[0] != "aaa111" || keys[1] != "bbb222" {
		t.Errorf("keys = %v, want [aaa111 bbb222]", keys)
	}
}

// ---------------------------------------------------------------------------
// Test: EnvKeySource — unset / empty → empty list
// ---------------------------------------------------------------------------

func TestEnvKeySource_Unset(t *testing.T) {
	t.Setenv("TEST_UNSET_KEY", "") // set but empty
	src := NewEnvKeySource(map[string]string{
		"p1": "TEST_UNSET_KEY",
	})
	ctx := context.Background()
	if keys := src.Keys(ctx, "p1"); len(keys) != 0 {
		t.Errorf("keys should be empty for empty env, got %v", keys)
	}

	// provider not in map at all
	if keys := src.Keys(ctx, "nosuchprov"); len(keys) != 0 {
		t.Errorf("keys should be nil for unknown provider, got %v", keys)
	}
}

// ---------------------------------------------------------------------------
// Test: EnvKeySource — case-insensitive provider name
// ---------------------------------------------------------------------------

func TestEnvKeySource_CaseInsensitive(t *testing.T) {
	tSetenv(t, "TEST_CI_KEY", "secret_key")
	src := NewEnvKeySource(map[string]string{
		"MyProv": "TEST_CI_KEY",
	})
	ctx := context.Background()

	for _, name := range []string{"MyProv", "myprov", "MYPROV", "mYpRoV"} {
		if keys := src.Keys(ctx, name); len(keys) != 1 || keys[0] != "secret_key" {
			t.Errorf("Keys(%q) = %v, want [secret_key]", name, keys)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: RedactedKeys — never leaks full key
// ---------------------------------------------------------------------------

func TestRedactedKeys(t *testing.T) {
	tSetenv(t, "TEST_SECRET", "sk-abcdef1234567890")
	src := NewEnvKeySource(map[string]string{
		"p": "TEST_SECRET",
	})
	ctx := context.Background()

	rk := src.RedactedKeys(ctx, "p")
	if len(rk) != 1 {
		t.Fatalf("len(RedactedKeys) = %d, want 1", len(rk))
	}
	r := rk[0]
	// should NOT contain the full key
	if strings.Contains(r, "sk-abcdef1234567890") {
		t.Errorf("RedactedKeys leaked full key: %q", r)
	}
	// should be shorter than the original
	if len(r) >= len("sk-abcdef1234567890") {
		t.Errorf("RedactedKeys %q not shorter than original", r)
	}
	t.Logf("Redacted: %q", r)
}

func TestRedactedKeys_ShortKey(t *testing.T) {
	tSetenv(t, "TEST_SHORT", "ab-cd")
	src := NewEnvKeySource(map[string]string{
		"p": "TEST_SHORT",
	})
	ctx := context.Background()

	rk := src.RedactedKeys(ctx, "p")
	if len(rk) != 1 {
		t.Fatalf("len = %d", len(rk))
	}
	if rk[0] != "***" {
		t.Errorf("short key should redact to '***', got %q", rk[0])
	}
}

// ---------------------------------------------------------------------------
// Test: FileKeySource — basic parsing
// ---------------------------------------------------------------------------

func TestFileKeySource_Basic(t *testing.T) {
	dir := t.TempDir()
	content := `
# comment line
groq=GKEY_GROQ_123
google=GKEY_GOOGLE_456
openrouter: ORKEY_789
`
	p := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	src, err := NewFileKeySource(p)
	if err != nil {
		t.Fatalf("NewFileKeySource: %v", err)
	}
	ctx := context.Background()

	if keys := src.Keys(ctx, "groq"); len(keys) != 1 || keys[0] != "GKEY_GROQ_123" {
		t.Errorf("groq keys = %v, want [GKEY_GROQ_123]", keys)
	}
	if keys := src.Keys(ctx, "google"); len(keys) != 1 || keys[0] != "GKEY_GOOGLE_456" {
		t.Errorf("google keys = %v", keys)
	}
	if keys := src.Keys(ctx, "openrouter"); len(keys) != 1 || keys[0] != "ORKEY_789" {
		t.Errorf("openrouter keys = %v", keys)
	}
	if keys := src.Keys(ctx, "unknown"); len(keys) != 0 {
		t.Errorf("unknown provider keys = %v, want empty", keys)
	}
}

// ---------------------------------------------------------------------------
// Test: FileKeySource — multiple keys per provider
// ---------------------------------------------------------------------------

func TestFileKeySource_Multi(t *testing.T) {
	dir := t.TempDir()
	content := `
prov=first-key
prov=second-key
`
	p := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := NewFileKeySource(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys := src.Keys(ctx, "prov")
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(keys))
	}
	if keys[0] != "first-key" || keys[1] != "second-key" {
		t.Errorf("keys = %v, want [first-key second-key]", keys)
	}
}

// ---------------------------------------------------------------------------
// Test: FileKeySource — bad path
// ---------------------------------------------------------------------------

func TestFileKeySource_BadPath(t *testing.T) {
	_, err := NewFileKeySource("/nonexistent/path/keys.txt")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// Test: KeySource interface satisfaction
// ---------------------------------------------------------------------------

func TestKeySource_Interfaces(t *testing.T) {
	var _ KeySource = NewEnvKeySource(nil)
	fsrc, _ := NewFileKeySource("/dev/null")
	if fsrc != nil {
		var _ KeySource = fsrc
	}
}

// ---------------------------------------------------------------------------
// Test: Availability struct
// ---------------------------------------------------------------------------

func TestAvailability(t *testing.T) {
	a := Availability{
		OK:       true,
		Reason:   "key present",
		KeyCount: 2,
	}
	if !a.OK {
		t.Error("OK should be true")
	}
	if a.Reason != "key present" {
		t.Errorf("Reason = %q", a.Reason)
	}
	if a.KeyCount != 2 {
		t.Errorf("KeyCount = %d", a.KeyCount)
	}
}

// ---------------------------------------------------------------------------
// Test: StreamDone struct
// ---------------------------------------------------------------------------

func TestStreamDone(t *testing.T) {
	d := StreamDone{
		FinishReason:     "stop",
		PromptTokens:     42,
		CompletionTokens: 128,
	}
	if d.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", d.FinishReason)
	}
	if d.PromptTokens != 42 {
		t.Errorf("PromptTokens = %d", d.PromptTokens)
	}
	if d.CompletionTokens != 128 {
		t.Errorf("CompletionTokens = %d", d.CompletionTokens)
	}

	// -1 sentinel for unknown tokens
	d2 := StreamDone{FinishReason: "length"}
	if d2.PromptTokens != 0 {
		t.Errorf("Zero prompt tokens = %d, want 0 (zero value)", d2.PromptTokens)
	}
}
