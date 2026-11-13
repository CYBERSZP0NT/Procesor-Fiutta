// Package defaults exposes the embedded YAML data files that serve as the
// baseline configuration for freellm-router.
//
// Rebuild with different defaults:
//
//	go build -ldflags "-X github.com/silverx/freellm-router/defaults.CustomProvidersYAML=…"
//
// …or simply replace the .yaml files before running `make build`.
package defaults

import _ "embed"

//go:embed providers.yaml
var ProvidersYAML []byte

//go:embed routes.yaml
var RoutesYAML []byte

//go:embed agentic.yaml
var AgenticYAML []byte
