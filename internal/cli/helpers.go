package cli

import (
	"os"
	"strings"
	"time"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/internal/live"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

// buildRouter constructs a *router.Router from the loaded config.
// (Delegates to pkg/router.NewFromConfig — the same construction the HTTP
// server uses, so there is a single source of truth for config→router.)
func buildRouter(cfg *config.Config, timeout time.Duration) (*router.Router, error) {
	return router.NewFromConfig(cfg, timeout)
}

// ownAPIKey is the router's own server key from FREELLM_API_KEY. A
// FreeLLMAPI credential kept there (fla_… license, freellmapi-… sidecar
// key) is for `live`, never the router's bearer, so it is ignored here.
func ownAPIKey() string {
	v := strings.TrimSpace(os.Getenv("FREELLM_API_KEY"))
	if live.IsFreellmapiKey(v) {
		return ""
	}
	return v
}

// defaultSelector is the model used when none is given: "live" (the task
// router in mode live) when a working FreeLLMAPI key made live the
// default, else the "auto" route.
func defaultSelector(cfg *config.Config) string {
	if cfg != nil && cfg.LiveState != nil && cfg.LiveState.Active && cfg.LiveState.DefaultMode {
		return "live"
	}
	return "auto"
}
