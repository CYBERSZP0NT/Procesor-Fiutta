package cli

import (
	"time"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/pkg/router"
)

// buildRouter constructs a *router.Router from the loaded config.
// (Delegates to pkg/router.NewFromConfig — the same construction the HTTP
// server uses, so there is a single source of truth for config→router.)
func buildRouter(cfg *config.Config, timeout time.Duration) (*router.Router, error) {
	return router.NewFromConfig(cfg, timeout)
}
