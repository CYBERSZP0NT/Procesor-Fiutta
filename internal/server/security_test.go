package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
)

func newBareServer(t *testing.T, auth config.AuthConfig) *Server {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "fake", Impl: "openailike", BaseURL: "http://127.0.0.1:1", Kind: "keyless"}},
		Routes:    map[string][]string{"auto": {"fake/m"}},
		Server:    config.ServerConfig{Auth: auth},
		Logging:   config.LoggingConfig{Level: "error"},
	}
	s, err := New(cfg, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func serve(s *Server, method, path, body, remote, bearer string) int {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if remote != "" {
		req.RemoteAddr = remote
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Code
}

func TestRemoteClientsCannotSteerWithoutAuth(t *testing.T) {
	s := newBareServer(t, config.AuthConfig{Mode: "none"})
	remote := "192.0.2.10:5555"
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/v1/control", `{"paused":true}`},
		{"PATCH", "/v1/control", `{"paused":true}`},
		{"DELETE", "/v1/cooldowns", ""},
		{"GET", "/v1/history", ""},
		{"GET", "/v1/sessions", ""},
	} {
		if code := serve(s, c.method, c.path, c.body, remote, ""); code != http.StatusForbidden {
			t.Errorf("%s %s from %s = %d, want 403", c.method, c.path, remote, code)
		}
		if code := serve(s, c.method, c.path, c.body, "127.0.0.1:5555", ""); code != http.StatusOK {
			t.Errorf("%s %s from loopback = %d, want 200", c.method, c.path, code)
		}
	}
	if s.rt.Ctrl.Settings().Paused != true {
		t.Fatal("loopback PUT should have applied")
	}
	if code := serve(s, "GET", "/v1/usage", "", remote, ""); code != http.StatusOK {
		t.Errorf("usage stays readable: %d", code)
	}

	// With bearer auth, an authenticated remote client may use them.
	sa := newBareServer(t, config.AuthConfig{Mode: "bearer", Keys: []string{"k1"}})
	if code := serve(sa, "GET", "/v1/history", "", remote, "k1"); code != http.StatusOK {
		t.Errorf("authed remote history = %d", code)
	}
}

func TestHooksAreConfigOnly(t *testing.T) {
	s := newBareServer(t, config.AuthConfig{Mode: "none"})
	lo := "127.0.0.1:1"
	if code := serve(s, "PATCH", "/v1/control", `{"hooks":{"pre_call":["sh","-c","touch /tmp/pwned"]}}`, lo, ""); code != http.StatusBadRequest {
		t.Fatalf("PATCH hooks = %d, want 400", code)
	}
	if code := serve(s, "PUT", "/v1/control", `{"profile":"auto","hooks":{"post_call":["evil"]}}`, lo, ""); code != http.StatusBadRequest {
		t.Fatalf("PUT hooks = %d, want 400", code)
	}
	if h := s.rt.Ctrl.Settings().Hooks; len(h.PreCall)+len(h.PostCall) != 0 {
		t.Fatalf("hooks changed: %+v", h)
	}
	// A PUT without hooks keeps the configured ones (none here) and applies.
	if code := serve(s, "PUT", "/v1/control", `{"profile":"auto"}`, lo, ""); code != http.StatusOK {
		t.Fatalf("PUT without hooks = %d", code)
	}
}
