package core

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// EnvKeySource implements KeySource by looking up keys in environment
// variables.  For each provider the config may list one or more env-var
// names (pipe-separated).  Each env var may itself hold comma-separated
// keys, enabling round-robin multi-key rotation without any file.
//
// Zero external dependencies: only os.LookupEnv.
type EnvKeySource struct {
	// providerEnv maps a provider name to the list of env-var names to check.
	providerEnv map[string][]string
	mu          sync.RWMutex
}

var _ KeySource = (*EnvKeySource)(nil)

// NewEnvKeySource creates a KeySource from a provider→env-vars mapping.
// envMap should look like: map[string]string{"groq": "GROQ_API_KEY|GROQ_2"}.
// Values are pipe-separated env-var names; each env var may hold
// comma-separated key values.
func NewEnvKeySource(envMap map[string]string) *EnvKeySource {
	m := make(map[string][]string, len(envMap))
	for provider, spec := range envMap {
		parts := strings.Split(spec, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		// drop empties
		out := parts[:0]
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			m[strings.ToLower(provider)] = out
		}
	}
	return &EnvKeySource{providerEnv: m}
}

// Keys returns all keys for the given provider, in rotation order.
// Empty slice = the provider has no key set (may be keyless).
func (s *EnvKeySource) Keys(_ context.Context, provider string) []string {
	s.mu.RLock()
	vars, ok := s.providerEnv[strings.ToLower(provider)]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	var keys []string
	for _, v := range vars {
		val := strings.TrimSpace(os.Getenv(v))
		if val == "" {
			continue
		}
		cs := strings.Split(val, ",")
		for _, c := range cs {
			c = strings.TrimSpace(c)
			if c != "" {
				keys = append(keys, c)
			}
		}
	}
	return keys
}

// RedactedKeys returns the same list but with each key truncated to
// first-4 + "…"+last-2 characters (so secrets never appear in full in
// error messages or logs).
func (s *EnvKeySource) RedactedKeys(ctx context.Context, provider string) []string {
	keys := s.Keys(ctx, provider)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = redactKey(k)
	}
	return out
}

func redactKey(k string) string {
	if len(k) <= 6 {
		return "***"
	}
	return k[:4] + "…" + k[len(k)-2:]
}

// FileKeySource implements KeySource by reading keys from a JSON or
// simple key=value file.  Format: one line per key: `provider_name=key`
// (or `provider_name: key`).  Useful for CI / containers where env vars
// are awkward.
type FileKeySource struct {
	entries map[string][]string // provider → keys
}

var _ KeySource = (*FileKeySource)(nil)

// NewFileKeySource parses a file at path.
// Accepted line formats (ignores blank lines and # comments):
//
//	groq=GKEY123
//	groq: GKEY123
//	google,key1=GKEY456
//
// Multiple keys per provider are appended in order.
func NewFileKeySource(path string) (*FileKeySource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	m := make(map[string][]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// support both "provider=key" and "provider: key"
		var sep string
		if i := strings.Index(line, "="); i > 0 {
			sep = "="
		} else if i := strings.Index(line, ":"); i > 0 {
			sep = ":"
		} else {
			continue
		}
		provider := strings.TrimSpace(line[:strings.Index(line, sep)])
		key := strings.TrimSpace(line[strings.Index(line, sep)+1:])
		if provider == "" || key == "" {
			continue
		}
		m[strings.ToLower(provider)] = append(m[strings.ToLower(provider)], key)
	}
	return &FileKeySource{entries: m}, nil
}

func (s *FileKeySource) Keys(_ context.Context, provider string) []string {
	return s.entries[strings.ToLower(provider)]
}
