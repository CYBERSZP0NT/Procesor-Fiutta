package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/silverx/szmelc-router/internal/config"
	"github.com/silverx/szmelc-router/internal/ledger"
	"github.com/silverx/szmelc-router/internal/usage"
)

func newObsServer(t *testing.T) (string, string) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Model == "bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"rl"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-ratelimit-remaining-requests", "9")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hey"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	file := filepath.Join(t.TempDir(), "ledger.jsonl")
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "fake", Impl: "openailike", BaseURL: up.URL, Kind: "keyless"}},
		Routes:    map[string][]string{"auto": {"fake/good"}, "broken": {"fake/bad"}},
		Policies:  config.PolicyConfig{CooldownShortSec: 1, CooldownLongSec: 1, MaxChainAttempts: 4},
		Limits:    map[string]config.Limits{"fake": {RPM: 50}},
		Ledger:    config.LedgerConfig{File: file, Content: "full"},
		Logging:   config.LoggingConfig{Level: "error"},
	}
	s, err := New(cfg, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); _ = s.rt.Close() })
	return ts.URL, file
}

func doJSON(t *testing.T, method, url, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestLedgerOneRecordPerRequest(t *testing.T) {
	url, file := newObsServer(t)
	chat := `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`
	if c := doJSON(t, "POST", url+"/s/sess-1/v1/chat/completions", chat, nil); c != 200 {
		t.Fatalf("chat status %d", c)
	}
	if c := doJSON(t, "POST", url+"/v1/chat/completions", `{"model":"broken","messages":[{"role":"user","content":"x"}]}`, nil); c < 400 {
		t.Fatalf("broken status %d", c)
	}
	doJSON(t, "GET", url+"/v1/usage", "", nil) // quiet path: not logged

	evs, err := ledger.ReadFile(file, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reqs, attempts, httpEv []ledger.Event
	for _, e := range evs {
		switch e.EffectiveKind() {
		case ledger.KindRequest:
			reqs = append(reqs, e)
		case ledger.KindAttempt:
			attempts = append(attempts, e)
		default:
			httpEv = append(httpEv, e)
		}
	}
	if len(reqs) != 2 || len(attempts) != 2 || len(httpEv) != 0 {
		t.Fatalf("requests=%d attempts=%d other=%d", len(reqs), len(attempts), len(httpEv))
	}
	ok, bad := reqs[0], reqs[1]
	if ok.Session != "sess-1" || ok.Via != "fake/good" || ok.Tokens.Total() != 3 || ok.Answer != "hey" || ok.Prompt != "hello" {
		t.Fatalf("ok record = %+v", ok)
	}
	if bad.Status < 400 || bad.Err == "" || bad.Attempts != 1 {
		t.Fatalf("failed record = %+v", bad)
	}
	if attempts[0].ID != ok.ID || attempts[0].RateLimit == nil || *attempts[0].RateLimit.RemainingRequests != 9 {
		t.Fatalf("attempt = %+v", attempts[0])
	}
}

func TestObservabilityAPI(t *testing.T) {
	url, _ := newObsServer(t)
	doJSON(t, "POST", url+"/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}],"metadata":{"session":"m1"}}`, nil)

	var hist struct {
		Data []ledger.Event `json:"data"`
	}
	doJSON(t, "GET", url+"/v1/history?kind=request&session=m1", "", &hist)
	if len(hist.Data) != 1 || hist.Data[0].Session != "m1" {
		t.Fatalf("history = %+v", hist.Data)
	}
	var sess struct {
		Data []ledger.SessionSummary `json:"data"`
	}
	doJSON(t, "GET", url+"/v1/sessions", "", &sess)
	if len(sess.Data) != 1 || sess.Data[0].Requests != 1 {
		t.Fatalf("sessions = %+v", sess.Data)
	}
	var rep usage.Report
	doJSON(t, "GET", url+"/v1/usage", "", &rep)
	if !rep.Live || len(rep.Providers) != 1 || rep.Providers[0].Minute.Requests != 1 || rep.Providers[0].Limits.RPM != 50 {
		t.Fatalf("usage = %+v", rep.Providers)
	}

	var v ControlView
	if c := doJSON(t, "PATCH", url+"/v1/control", `{"profile":"control","disabled":["fake"]}`, &v); c != 200 {
		t.Fatalf("patch status %d", c)
	}
	if v.Effective.Profile != "control" || v.Effective.Fallback || !v.Effective.IsDisabled("fake") {
		t.Fatalf("control = %+v", v.Effective)
	}
	if c := doJSON(t, "POST", url+"/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"x"}]}`, nil); c < 400 {
		t.Fatalf("disabled provider still served (%d)", c)
	}
	doJSON(t, "PATCH", url+"/v1/control", `{"disabled":null,"paused":true}`, &v)
	if c := doJSON(t, "POST", url+"/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"x"}]}`, nil); c != http.StatusForbidden {
		t.Fatalf("paused status %d", c)
	}
	if c := doJSON(t, "PUT", url+"/v1/control", `{"profile":"nope"}`, nil); c != 400 {
		t.Fatalf("bad profile status %d", c)
	}
	var cl struct {
		Cleared int `json:"cleared"`
	}
	if c := doJSON(t, "DELETE", url+"/v1/cooldowns", "", &cl); c != 200 {
		t.Fatalf("cooldowns status %d", c)
	}
}
