package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/ledger"
	"github.com/CYBERSZP0NT/szmelc-router/internal/server"
)

type serveFlags struct {
	host     string
	port     int
	cert     string
	key      string
	maxBody  int64
	once     bool
	graceSec int
}

var serveF = &serveFlags{}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the OpenAI-compatible HTTP server",
	Long: `Start the OpenAI-compatible HTTP server (STEP 4).

Point any OpenAI SDK at it:
  base_url   = http://host:port/v1
  api_key    = <server key, or anything if auth.mode = none>
  model      = "auto" | "free" | "groq/llama-3.3-70b-versatile" | …

Endpoints:
  POST /v1/chat/completions   streaming + non-streaming, exact OpenAI shapes
  GET  /v1/models             route aliases + live per-provider catalogs
  GET  /v1/health             per-provider availability (latency, keys, cooldown)
  GET  /v1/notes              free-tier notes as JSON
  GET  /v1/metrics            Prometheus text format (self-made)

The middleware pipeline (logging → CORS → auth → rate-limit → usage
accounting → panic-recover) is config-controlled: disable via
server.middleware_off in the YAML config.`,
	// RunE: replaced in STEP 4 by the live implementation.
	RunE:         runServe,
	SilenceUsage: true,
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(pCfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// The server keeps the event ledger (history, usage, sessions) at the
	// default location unless the config names one or disables it.
	if !cfg.Ledger.Disabled && cfg.LedgerPath("") == "" {
		cfg.Ledger.File = ledger.DefaultPath()
	}

	timeout := globalTimeout()
	srv, err := server.New(cfg, timeout)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	opts := server.ServeOpts{
		Host:  serveF.host,
		Port:  serveF.port,
		Cert:  serveF.cert,
		Key:   serveF.key,
		Once:  serveF.once,
		Grace: serveGrace(),
	}
	if serveF.maxBody > 0 {
		opts.MaxBodyBytes = serveF.maxBody
	}

	// Honor OS signals even though the CLI context is already root-bound.
	ctx, stopSignals := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	return srv.Run(ctx, opts)
}

func serveGrace() time.Duration {
	if serveF.graceSec > 0 {
		return time.Duration(serveF.graceSec) * time.Second
	}
	return 0 // server default
}

func init() {
	serveCmd.Flags().StringVar(&serveF.host, "host", "0.0.0.0", "bind address")
	serveCmd.Flags().IntVar(&serveF.port, "port", 0, "listen port (0 = config value, default 8787)")
	serveCmd.Flags().StringVar(&serveF.cert, "cert", "", "TLS cert file (optional)")
	serveCmd.Flags().StringVar(&serveF.key, "key", "", "TLS key file (optional)")
	serveCmd.Flags().Int64Var(&serveF.maxBody, "max-body-bytes", 0, "max request body bytes (0 = config, default 4 MiB)")
	serveCmd.Flags().BoolVar(&serveF.once, "once", false, "handle one request then exit (useful for harnesses)")
	serveCmd.Flags().IntVar(&serveF.graceSec, "grace-seconds", 10, "seconds to drain in-flight streams on shutdown")
}
