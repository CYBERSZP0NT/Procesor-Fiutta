package core

import (
	"net/http"
	"testing"
)

func TestParseRateLimit(t *testing.T) {
	if ParseRateLimit(http.Header{}) != nil {
		t.Fatal("no headers → nil")
	}
	h := http.Header{}
	h.Set("X-RateLimit-Limit-Requests", "1000")
	h.Set("X-RateLimit-Remaining-Requests", "0")
	h.Set("X-RateLimit-Reset-Requests", "6m0s")
	h.Set("X-RateLimit-Remaining-Tokens", "5000.0")
	h.Set("Retry-After", "7")
	rl := ParseRateLimit(h)
	if rl == nil || *rl.LimitRequests != 1000 || *rl.RemainingRequests != 0 || rl.ResetRequests != "6m0s" ||
		*rl.RemainingTokens != 5000 || rl.RetryAfter != "7" {
		t.Fatalf("rl = %+v", rl)
	}
	// unsuffixed (OpenRouter-style) family
	h = http.Header{}
	h.Set("X-RateLimit-Remaining", "12")
	if rl := ParseRateLimit(h); rl == nil || *rl.RemainingRequests != 12 {
		t.Fatalf("unsuffixed = %+v", rl)
	}
}
