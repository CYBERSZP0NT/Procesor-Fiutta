package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: a via without "/" and a keyed record without tokens used to
// panic; attempts must not be counted as requests.
func TestCollectStatsRobust(t *testing.T) {
	p := filepath.Join(t.TempDir(), "u.jsonl")
	lines := `{"t":"2026-01-01T10:00:00Z","path":"/v1/chat/completions","status":200,"via":"plainvia"}
{"t":"2026-01-01T10:00:01Z","path":"/v1/chat/completions","status":401,"key":"sk-…abcd"}
{"t":"2026-01-01T10:00:02Z","kind":"attempt","via":"g/m","status":429}
{"t":"2026-01-01T10:00:03Z","kind":"request","via":"g/m","status":200,"key":"sk-…abcd","tokens":{"prompt_tokens":4,"completion_tokens":-1}}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := collectStats(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 3 || s.Errors != 1 || s.Providers["plainvia"].Requests != 1 || s.Providers["g"].Requests != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.Keys["sk-…abcd"].Requests != 2 || s.TotalPrompt != 4 || s.TotalCompletion != 0 {
		t.Fatalf("keys/tokens = %+v %d %d", s.Keys, s.TotalPrompt, s.TotalCompletion)
	}
}
