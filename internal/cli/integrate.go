package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/control"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
	"github.com/CYBERSZP0NT/szmelc-router/internal/server"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

// Exit codes of `exec` (stable: scripts branch on them).
const (
	exitOK        = 0
	exitError     = 1 // bad usage, config, I/O
	exitAllFailed = 2 // every provider failed (or forever mode gave up)
	exitVetoed    = 3 // paused, or a pre-call hook refused
)

// exitCodeError carries a specific process exit code.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// ExitCode maps an Execute error to the process exit code.
func exitCodeOf(err error) int {
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	return 1
}

// cmdCtx is the context of one short API call (the client has its own
// timeout).
func cmdCtx() context.Context { return context.Background() }

// ---------------------------------------------------------------------------
// exec
// ---------------------------------------------------------------------------

var execOpts struct {
	system      string
	profile     string
	session     string
	forever     bool
	noFallback  bool
	maxAttempts int
	maxTokens   int
	temperature float64
	quiet       bool
	file        []string
}

var execCmd = &cobra.Command{
	Use:     `exec [prompt...]`,
	Aliases: []string{"ask", "run"},
	Short:   "One-shot call for scripts and harnesses (stdin in, answer out, stable exit codes)",
	Long: `Route one prompt and print the answer — built to be called from any
script, Makefile, CI job or agent harness.

The prompt is the arguments; piped stdin is added as context (so
"git diff | szmelc-router exec 'review this'" works). A harness that keeps
a stdin pipe open without writing to it should pass --no-stdin (or set
FREELLM_NO_STDIN=1), as should calls inside a "while read" loop. The answer goes to
stdout, diagnostics to stderr. --json prints one JSON object with text,
via, tokens, latency, attempts, request id and session.

Control per call:
  --profile auto     infinite & full auto (fallback, wait out cooldowns,
                     retry until answered, escalate)
  --profile control  exactly this model, no fallback, no retries
  --forever          keep trying until answered (Ctrl-C / --deadline stops)
  --no-fallback      only the first chain entry
  --max-attempts N   cap upstream calls

Exit codes: 0 answered · 1 usage/config error · 2 every provider failed ·
3 vetoed (paused or a pre-call hook refused).

Every call is in the ledger (history), tagged with --session or
$FREELLM_SESSION.

Examples:
  szmelc-router exec "summarize this" < notes.md
  git diff | szmelc-router ask -m code "review this diff" --json
  szmelc-router exec --profile auto --deadline 1h "…"      # never give up (within 1h)
  szmelc-router exec --profile control -m groq/openai/gpt-oss-120b "…"
  answer=$(szmelc-router exec -q "one word: capital of France") || exit $?`,
	RunE: runExec,
}

var (
	execDeadline time.Duration
	execNoStdin  bool
)

func envTrue(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func init() {
	f := execCmd.Flags()
	f.StringVarP(&execOpts.system, "system", "s", "", "system message")
	f.StringVar(&execOpts.profile, "profile", "", "control profile for this call (auto | control | balanced)")
	f.StringVar(&execOpts.session, "session", "", "session id to tag the call with (default $FREELLM_SESSION)")
	f.BoolVar(&execOpts.forever, "forever", false, "retry until answered (waits out cooldowns)")
	f.BoolVar(&execOpts.noFallback, "no-fallback", false, "only try the first chain entry")
	f.IntVar(&execOpts.maxAttempts, "max-attempts", 0, "cap upstream calls (0 = default)")
	f.IntVar(&execOpts.maxTokens, "max-tokens", 0, "max tokens to generate")
	f.Float64Var(&execOpts.temperature, "temperature", 0, "sampling temperature (0 = upstream default)")
	f.BoolVarP(&execOpts.quiet, "quiet", "q", false, "no streaming: print the whole answer at the end")
	f.StringSliceVarP(&execOpts.file, "file", "f", nil, "add a file's content as context (repeatable)")
	f.BoolVar(&execNoStdin, "no-stdin", false, "never read stdin (for harnesses that keep a stdin pipe open; env FREELLM_NO_STDIN=1)")
	f.DurationVar(&execDeadline, "deadline", 0, "give up after this long overall (default: --timeout, none in forever mode)")
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return &exitCodeError{exitError, err}
	}

	var parts []string
	for _, f := range execOpts.file {
		b, err := os.ReadFile(f)
		if err != nil {
			return &exitCodeError{exitError, err}
		}
		parts = append(parts, fmt.Sprintf("--- %s ---\n%s", f, b))
	}
	noStdin := execNoStdin || envTrue("FREELLM_NO_STDIN")
	if st, err := os.Stdin.Stat(); !noStdin && err == nil && st.Mode()&os.ModeCharDevice == 0 {
		b, _ := io.ReadAll(os.Stdin)
		if s := strings.TrimSpace(string(b)); s != "" {
			parts = append(parts, s)
		}
	}
	if len(args) > 0 {
		parts = append(parts, strings.Join(args, " "))
	}
	if len(parts) == 0 {
		return &exitCodeError{exitError, fmt.Errorf("no prompt: pass it as arguments or on stdin")}
	}
	prompt := strings.Join(parts, "\n\n")

	r, rt, err := openRuntime(cfg, "exec")
	if err != nil {
		return &exitCodeError{exitError, err}
	}
	defer rt.Close()

	ov := control.Override{Profile: execOpts.profile, MaxAttempts: execOpts.maxAttempts}
	if execOpts.profile != "" {
		if _, ok := control.NormalizeProfile(execOpts.profile); !ok {
			return &exitCodeError{exitError, fmt.Errorf("unknown profile %q (auto | control | balanced)", execOpts.profile)}
		}
	}
	if cmd.Flags().Changed("forever") {
		ov.Forever = control.Bool(execOpts.forever)
	}
	if execOpts.noFallback {
		ov.Fallback = control.Bool(false)
	}

	// Deadline: explicit, else --timeout — except in forever mode, where
	// only an explicit deadline (or Ctrl-C) ends the wait.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = router.WithOverride(ctx, ov)
	dl := execDeadline
	if dl == 0 && !r.Resolve(ctx).Forever {
		dl = globalTimeout()
	}
	if dl > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dl)
		defer cancel()
	}

	selector := pModel
	if selector == "" {
		selector = defaultSelector(cfg)
	}
	msgs := make([]router.Message, 0, 2)
	if execOpts.system != "" {
		msgs = append(msgs, router.Message{Role: "system", Content: execOpts.system})
	}
	msgs = append(msgs, router.Message{Role: "user", Content: prompt})

	opts := &router.RouteOpts{}
	if execOpts.maxTokens > 0 {
		opts.MaxTokens = &execOpts.maxTokens
	}
	if execOpts.temperature > 0 {
		opts.Temperature = &execOpts.temperature
	}

	session := execOpts.session
	if session == "" {
		session = os.Getenv("FREELLM_SESSION")
	}
	stream := !execOpts.quiet && !pJSON
	var sink func(string)
	if stream {
		sink = func(s string) { fmt.Print(s) }
	}
	t0 := time.Now()
	ctx, call := rt.Begin(ctx, session, selector, prompt, stream)
	var answer strings.Builder
	if sink != nil {
		inner := sink
		sink = func(s string) { answer.WriteString(s); inner(s) }
	}
	via, done, rerr := r.RouteWithOptions(ctx, selector, msgs, "", opts, sink)
	p, c := 0, 0
	if done != nil {
		p, c = done.PromptTokens, done.CompletionTokens
		answer.WriteString(done.Text)
	}
	rt.End(call, via, p, c, answer.String(), rerr)
	ev := call.Event

	if pJSON {
		out := map[string]any{
			"ok": rerr == nil, "text": answer.String(), "via": via, "model": selector,
			"request_id": call.ID, "session": session, "profile": ev.Profile,
			"latency_ms": time.Since(t0).Milliseconds(), "attempts": ev.Attempts, "rounds": ev.Rounds,
			"tokens": map[string]int{"prompt": p, "completion": c},
		}
		if done != nil {
			out["finish_reason"] = done.FinishReason
		}
		if rerr != nil {
			out["error"] = rerr.Error()
			out["trail"] = ev.Trail
		}
		printJSON(out)
	} else if rerr == nil {
		if stream {
			fmt.Println()
		} else {
			fmt.Println(answer.String())
		}
		if v := os.Getenv("FREELLM_VERBOSE"); v != "" && v != "0" {
			fmt.Fprintf(os.Stderr, "↳ via %s · %d tries · %s · id %s\n", via, ev.Attempts, humanDur(time.Since(t0)), call.ID)
		}
	}
	if rerr == nil {
		return nil
	}
	var ve *control.VetoError
	if errors.As(rerr, &ve) {
		return &exitCodeError{exitVetoed, rerr}
	}
	return &exitCodeError{exitAllFailed, rerr}
}

// ---------------------------------------------------------------------------
// env
// ---------------------------------------------------------------------------

var envOpts struct {
	shell   string
	session string
}

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print environment variables that point any OpenAI-compatible tool at the router",
	Long: `Print the variables that make OpenAI SDKs, agent harnesses and CLIs
use this router (OPENAI_BASE_URL, OPENAI_API_BASE, OPENAI_API_KEY, plus
FREELLM_URL / FREELLM_SESSION for freellm-aware tools).

With --session, the base URL carries the session (…/s/<id>/v1), so every
call from that tool is grouped in history — no custom headers needed.

Examples:
  eval "$(szmelc-router env)"
  eval "$(szmelc-router env --session nightly-refactor)"
  szmelc-router env --shell fish | source
  szmelc-router env --shell dotenv >> .env
  szmelc-router env --shell json`,
	RunE: runEnv,
}

func init() {
	envCmd.Flags().StringVar(&envOpts.shell, "shell", "sh", "sh | fish | powershell | dotenv | json")
	envCmd.Flags().StringVar(&envOpts.session, "session", "", "tag every call with this session id")
	rootCmd.AddCommand(envCmd)
}

// envVars returns the variables for a server at base (http://host:port).
func envVars(cfg *config.Config, base, session string) [][2]string {
	api := base + "/v1"
	if session != "" {
		api = base + "/s/" + session + "/v1"
	}
	key := ownAPIKey()
	if key == "" && cfg != nil && len(cfg.Server.Auth.Keys) > 0 {
		key = cfg.Server.Auth.Keys[0]
	}
	if key == "" {
		key = "freellm" // auth.mode none: any non-empty key satisfies SDKs
	}
	vars := [][2]string{
		{"OPENAI_BASE_URL", api},
		{"OPENAI_API_BASE", api},
		{"OPENAI_API_KEY", key},
		{"FREELLM_URL", base},
	}
	if session != "" {
		vars = append(vars, [2]string{"FREELLM_SESSION", session})
	}
	return vars
}

func runEnv(cmd *cobra.Command, args []string) error {
	cfg, _ := loadConfig()
	vars := envVars(cfg, serverURL(cfg), envOpts.session)
	switch envOpts.shell {
	case "sh", "bash", "zsh":
		for _, v := range vars {
			fmt.Printf("export %s=%s\n", v[0], shQuote(v[1]))
		}
	case "fish":
		for _, v := range vars {
			fmt.Printf("set -gx %s %s\n", v[0], shQuote(v[1]))
		}
	case "powershell", "pwsh":
		for _, v := range vars {
			fmt.Printf("$env:%s = \"%s\"\n", v[0], strings.ReplaceAll(v[1], `"`, "`\""))
		}
	case "dotenv":
		for _, v := range vars {
			fmt.Printf("%s=%s\n", v[0], v[1])
		}
	case "json":
		m := map[string]string{}
		for _, v := range vars {
			m[v[0]] = v[1]
		}
		printJSON(m)
	default:
		return fmt.Errorf("unknown --shell %q (sh | fish | powershell | dotenv | json)", envOpts.shell)
	}
	return nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ---------------------------------------------------------------------------
// wrap
// ---------------------------------------------------------------------------

var wrapOpts struct {
	session string
	profile string
	note    string
	port    int
	quiet   bool
}

var wrapCmd = &cobra.Command{
	Use:   "wrap [flags] -- <command> [args...]",
	Short: "Run any command/harness against the router, as one tracked session",
	Long: `Run a command with the router wired in: OPENAI_BASE_URL (and friends)
point at a router, and every call the command makes is recorded under one
session in history.

If a server is already running (serve, or --url) it is used; otherwise a
private one is started on a free loopback port for the command's
lifetime. --profile sets the control profile of that private server
(auto = infinite & full auto, control = 120% control).

When the command exits, a session summary is printed to stderr and the
command's exit code is returned.

Examples:
  szmelc-router wrap -- python agent.py
  szmelc-router wrap --session refactor-42 --profile auto -- aider
  szmelc-router wrap --profile control -- ./my-harness.sh --task x
  szmelc-router history --session refactor-42`,
	Args: cobra.MinimumNArgs(1),
	RunE: runWrap,
}

func init() {
	f := wrapCmd.Flags()
	f.StringVar(&wrapOpts.session, "session", "", "session id (default: wrap-<time>)")
	f.StringVar(&wrapOpts.profile, "profile", "", "control profile for a private server (auto | control | balanced)")
	f.StringVar(&wrapOpts.note, "note", "", "free-text note stored with the session")
	f.IntVar(&wrapOpts.port, "port", 0, "port for the private server (0 = any free port)")
	f.BoolVar(&wrapOpts.quiet, "quiet", false, "no session summary at the end")
	f.SetInterspersed(false)
	rootCmd.AddCommand(wrapCmd)
}

func runWrap(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	session := wrapOpts.session
	if session == "" {
		session = "wrap-" + time.Now().Format("0102-150405")
	}
	if wrapOpts.profile != "" {
		p, ok := control.NormalizeProfile(wrapOpts.profile)
		if !ok {
			return fmt.Errorf("unknown profile %q (auto | control | balanced)", wrapOpts.profile)
		}
		cfg.Control.Profile = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	base := serverURL(cfg)
	client := newAPIClient(cfg)
	shared := client.ping()
	if shared && wrapOpts.profile != "" {
		fmt.Fprintf(os.Stderr, "freellm: using the running server at %s (its control profile applies; --profile only affects a private server)\n", base)
	}
	var srvDone chan error
	if !shared {
		base, srvDone, err = startPrivateServer(ctx, cfg, wrapOpts.port)
		if err != nil {
			return err
		}
	}

	note := wrapOpts.note
	if note == "" {
		note = strings.Join(args, " ")
	}
	lg := openLedgerAppender(cfg)
	_ = lg.Write(ledger.Event{T: time.Now(), Kind: ledger.KindSession, Session: session, Source: "wrap", Action: "start", Note: note, Profile: cfg.Control.Profile})

	c := exec.Command(args[0], args[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = os.Environ()
	for _, v := range envVars(cfg, base, session) {
		c.Env = append(c.Env, v[0]+"="+v[1])
	}
	t0 := time.Now()
	runErr := c.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			code = 127
		}
	}
	_ = lg.Write(ledger.Event{T: time.Now(), Kind: ledger.KindSession, Session: session, Source: "wrap", Action: "end",
		Note: fmt.Sprintf("exit %d after %s", code, humanDur(time.Since(t0).Round(time.Second))), Status: code})
	_ = lg.Close()

	if srvDone != nil {
		stop()
		select {
		case <-srvDone:
		case <-time.After(5 * time.Second):
		}
	}
	if !wrapOpts.quiet {
		printSessionSummary(cfg, session)
	}
	if runErr != nil && code == 127 {
		return runErr
	}
	if code != 0 {
		return &exitCodeError{code, fmt.Errorf("%s exited with %d", args[0], code)}
	}
	return nil
}

func openLedgerAppender(cfg *config.Config) ledger.Sink {
	path := ledgerPathFor(cfg)
	if path == "" {
		return ledger.Discard{}
	}
	s, err := ledger.OpenAppend(path)
	if err != nil {
		return ledger.Discard{}
	}
	return s
}

// startPrivateServer runs a server on loopback for the life of ctx.
func startPrivateServer(ctx context.Context, cfg *config.Config, port int) (string, chan error, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "", nil, fmt.Errorf("wrap: %w", err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	if !cfg.Ledger.Disabled && cfg.LedgerPath("") == "" {
		cfg.Ledger.File = ledger.DefaultPath()
	}
	cfg.Logging.Level = "error"
	srv, err := server.New(cfg, globalTimeout())
	if err != nil {
		return "", nil, fmt.Errorf("wrap: server: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, server.ServeOpts{Host: "127.0.0.1", Port: port}) }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	c := &apiClient{base: base, hc: newAPIClient(cfg).hc, key: newAPIClient(cfg).key}
	for i := 0; i < 50; i++ {
		if c.ping() {
			return base, done, nil
		}
		select {
		case err := <-done:
			return "", nil, fmt.Errorf("wrap: server exited: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", nil, fmt.Errorf("wrap: private server did not come up on %s", base)
}

func printSessionSummary(cfg *config.Config, session string) {
	evs, err := ledger.ReadFile(ledgerPathFor(cfg), cfg.Ledger.Keep)
	if err != nil {
		return
	}
	f := ledger.Filter{Session: session}
	for _, s := range ledger.Sessions(f.Apply(evs)) {
		fmt.Fprintf(os.Stderr, "\nfreellm session %s: %d requests (%d failed), %d upstream calls, %s tokens, providers %s\n",
			s.ID, s.Requests, s.Errors, s.Attempts, compact(s.Tokens), providerList(s.Providers))
		fmt.Fprintf(os.Stderr, "  → szmelc-router history --session %s\n", s.ID)
	}
}
