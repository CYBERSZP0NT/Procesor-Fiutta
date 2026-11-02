package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test: CooldownStore — basic set/has/forget
// ---------------------------------------------------------------------------

func TestCooldown_SetHasForget(t *testing.T) {
	cs := NewInMemCooldown()
	ctx := context.Background()

	if cs.Has(ctx, "foo") {
		t.Error("fresh store should not have 'foo' in cooldown")
	}

	cs.Set(ctx, "foo", 100*time.Millisecond)
	if !cs.Has(ctx, "foo") {
		t.Error("'foo' should be in cooldown immediately after Set")
	}

	cs.Forget(ctx, "foo")
	if cs.Has(ctx, "foo") {
		t.Error("'foo' should not be in cooldown after Forget")
	}
}

// ---------------------------------------------------------------------------
// Test: CooldownStore — TTL expiry
// ---------------------------------------------------------------------------

func TestCooldown_TTLExpiry(t *testing.T) {
	cs := NewInMemCooldown()
	ctx := context.Background()

	cs.Set(ctx, "bar", 50*time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	if cs.Has(ctx, "bar") {
		t.Error("'bar' should have expired after TTL")
	}
	if d := cs.Remaining(ctx, "bar"); d > 0 {
		t.Errorf("Remaining should be 0 after expiry, got %v", d)
	}
}

// ---------------------------------------------------------------------------
// Test: CooldownStore — Remaining reflects remaining time
// ---------------------------------------------------------------------------

func TestCooldown_Remaining(t *testing.T) {
	cs := NewInMemCooldown()
	ctx := context.Background()

	cs.Set(ctx, "baz", 900*time.Millisecond)
	d := cs.Remaining(ctx, "baz")
	if d < 800*time.Millisecond {
		t.Errorf("Remaining = %v, want >= 800ms", d)
	}
	if d > 901*time.Millisecond {
		t.Errorf("Remaining = %v, want <= 901ms", d)
	}
}

// ---------------------------------------------------------------------------
// Test: CooldownStore — ForgetAll clears everything
// ---------------------------------------------------------------------------

func TestCooldown_ForgetAll(t *testing.T) {
	cs := NewInMemCooldown()
	ctx := context.Background()

	cs.Set(ctx, "a", time.Hour)
	cs.Set(ctx, "b", time.Hour)
	cs.Set(ctx, "c", time.Hour)

	if cs.Len() != 3 {
		t.Fatalf("Len = %d, want 3", cs.Len())
	}

	cs.ForgetAll(ctx)
	if cs.Len() != 0 {
		t.Errorf("Len after ForgetAll = %d, want 0", cs.Len())
	}
	if cs.Has(ctx, "a") || cs.Has(ctx, "b") || cs.Has(ctx, "c") {
		t.Error("no labels should be in cooldown after ForgetAll")
	}
}

// ---------------------------------------------------------------------------
// Test: CooldownStore — multiple entries don't interfere
// ---------------------------------------------------------------------------

func TestCooldown_Multiple(t *testing.T) {
	cs := NewInMemCooldown()
	ctx := context.Background()

	cs.Set(ctx, "p1/m1", time.Hour)
	cs.Set(ctx, "p1/m2", time.Hour)
	cs.Set(ctx, "p2/m1", 50*time.Millisecond)

	if !cs.Has(ctx, "p1/m1") {
		t.Error("p1/m1 should be in cooldown")
	}
	if !cs.Has(ctx, "p1/m2") {
		t.Error("p1/m2 should be in cooldown")
	}
	time.Sleep(60 * time.Millisecond)
	if cs.Has(ctx, "p2/m1") {
		t.Error("p2/m1 should have expired")
	}
	if !cs.Has(ctx, "p1/m1") {
		t.Error("p1/m1 should still be in cooldown")
	}

	labels := cs.Labels()
	for i := 1; i < len(labels); i++ {
		if labels[i] < labels[i-1] {
			t.Errorf("Labels not sorted: %v", labels)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Test: UpstreamErr formatting and wrapping
// ---------------------------------------------------------------------------

func TestUpstreamErr(t *testing.T) {
	cause := errors.New("base failure")
	e := NewUpstreamErr(429, "groq", "llama-3.3-70b-versatile", "rate limited", cause)

	if e.Status != 429 {
		t.Errorf("Status = %d, want 429", e.Status)
	}
	if e.Provider != "groq" {
		t.Errorf("Provider = %q, want groq", e.Provider)
	}
	if e.Model != "llama-3.3-70b-versatile" {
		t.Errorf("Model = %q, want llama-3.3-70b-versatile", e.Model)
	}
	if e.Msg != "rate limited" {
		t.Errorf("Msg = %q, want 'rate limited'", e.Msg)
	}
	if !errors.Is(e, cause) {
		t.Error("errors.Is should find the base cause")
	}

	msg := e.Error()
	if !strings.Contains(msg, "groq") {
		t.Errorf("Error() = %q, should contain provider name", msg)
	}
	if !strings.Contains(msg, "429") {
		t.Errorf("Error() = %q, should contain status code", msg)
	}
	if !strings.Contains(msg, "rate limited") {
		t.Errorf("Error() = %q, should contain message", msg)
	}

	// IsUpstreamErr
	ue, ok := IsUpstreamErr(e)
	if !ok {
		t.Fatal("IsUpstreamErr should return true for *UpstreamErr")
	}
	if ue != e {
		t.Error("IsUpstreamErr should return the same pointer")
	}

	// IsUpstreamErr with fmt-wrapped
	wrapped := fmt.Errorf("outer wrap: %w", e)
	ue2, ok2 := IsUpstreamErr(wrapped)
	if !ok2 {
		t.Fatal("IsUpstreamErr should find *UpstreamErr inside fmt-wrapped error")
	}
	if ue2.Status != 429 {
		t.Errorf("wrapped UpstreamErr Status = %d, want 429", ue2.Status)
	}

	// IsUpstreamErr with plain error
	if _, ok := IsUpstreamErr(cause); ok {
		t.Error("IsUpstreamErr should return false for plain error")
	}
}

// ---------------------------------------------------------------------------
// Test: NoUpstreamErr path
// ---------------------------------------------------------------------------

func TestIsUpstreamErr_NoUpstream(t *testing.T) {
	if _, ok := IsUpstreamErr(nil); ok {
		t.Error("IsUpstreamErr(nil) should return false")
	}
	if _, ok := IsUpstreamErr(errors.New("plain")); ok {
		t.Error("IsUpstreamErr(plain error) should return false")
	}
	ue := NewUpstreamErr(500, "p", "m", "err", nil)
	if ue.Cause != nil {
		t.Error("Cause should be nil when passed nil")
	}
}

// ---------------------------------------------------------------------------
// Test: Kind constants match expected string values
// ---------------------------------------------------------------------------

func TestKindConstants(t *testing.T) {
	expected := map[Kind]string{
		KindKeyless: "keyless",
		KindKeyed:   "keyed",
		KindLocal:   "local",
		KindScraper: "scraper",
		KindSidecar: "sidecar",
		KindPlugin:  "plugin",
	}
	for k, want := range expected {
		if string(k) != want {
			t.Errorf("Kind = %q, want %q", string(k), want)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Model struct
// ---------------------------------------------------------------------------

func TestModel(t *testing.T) {
	m := Model{
		ID:    "llama-3.3-70b-versatile",
		Name:  "Llama 3.3 70B Versatile",
		Kinds: []Kind{KindKeyed},
		Tier: TierInfo{
			Caps:  "14,400 req/day",
			Notes: "Groq free tier",
		},
	}
	if m.ID != "llama-3.3-70b-versatile" {
		t.Errorf("ID = %q", m.ID)
	}
	if m.Kinds[0] != KindKeyed {
		t.Errorf("Kinds[0] = %q", m.Kinds[0])
	}
	if m.Tier.Caps != "14,400 req/day" {
		t.Errorf("Tier.Caps = %q", m.Tier.Caps)
	}
}

// ---------------------------------------------------------------------------
// Test: ChatRequest field access
// ---------------------------------------------------------------------------

func TestChatRequest(t *testing.T) {
	temp := 0.7
	topP := 0.9
	req := ChatRequest{
		Model:       "google/gemini-2.5-flash",
		MaxTokens:   2048,
		Temperature: &temp,
		TopP:        &topP,
		Stream:      true,
		Messages: []Message{
			{Role: RoleSystem, Content: "You are helpful."},
			{Role: RoleUser, Content: "Hello"},
		},
	}
	if req.Model != "google/gemini-2.5-flash" {
		t.Errorf("Model = %q", req.Model)
	}
	if req.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", req.MaxTokens)
	}
	if *req.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", *req.Temperature)
	}
	if *req.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", *req.TopP)
	}
	if !req.Stream {
		t.Error("Stream should be true")
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != RoleSystem {
		t.Errorf("Messages[0].Role = %q, want system", req.Messages[0].Role)
	}
	if req.Messages[1].Role != RoleUser {
		t.Errorf("Messages[1].Role = %q, want user", req.Messages[1].Role)
	}
}

// ---------------------------------------------------------------------------
// Test: ToolDef and ToolCall structures
// ---------------------------------------------------------------------------

func TestToolTypes(t *testing.T) {
	td := ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        "get_weather",
			Description: "Get weather for a location",
			Parameters:  map[string]any{"type": "object"},
		},
	}
	if td.Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q", td.Function.Name)
	}

	tc := ToolCall{
		ID:   "call-123",
		Type: "function",
		Function: ToolFunction{
			Name:      "get_weather",
			Arguments: `{"location":"SF"}`,
		},
	}
	if tc.ID != "call-123" {
		t.Errorf("ID = %q", tc.ID)
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q", tc.Function.Name)
	}
}

// ---------------------------------------------------------------------------
// Test: Roles
// ---------------------------------------------------------------------------

func TestRoles(t *testing.T) {
	if RoleSystem != "system" {
		t.Errorf("RoleSystem = %q", RoleSystem)
	}
	if RoleUser != "user" {
		t.Errorf("RoleUser = %q", RoleUser)
	}
	if RoleAssistant != "assistant" {
		t.Errorf("RoleAssistant = %q", RoleAssistant)
	}
	if RoleTool != "tool" {
		t.Errorf("RoleTool = %q", RoleTool)
	}
}
