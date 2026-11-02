package config

import (
	"os"
	"regexp"
	"strings"
)

// envVarRE matches ${VAR} and ${VAR:-default} patterns.
// Group 1 = VAR name for simple case; group 2 = VAR name, group 3 = default.
var envVarRE = regexp.MustCompile(
	`\$\{([A-Za-z_][A-Za-z0-9_]*)\}` + "|" + // ${VAR}
	`\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]+)\}`, // ${VAR:-default}
)

// expandString replaces ${VAR} and ${VAR:-default} occurrences with the
// value of the environment variable, or the default when the variable is
// unset or empty.
func expandString(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envVarRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := envVarRE.FindStringSubmatch(m)
		var name, def string
		if sub[1] != "" {
			name = sub[1]
		} else {
			name = sub[2]
			def = sub[3]
		}
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v
		}
		return def
	})
}

// expandConfig walks every string field in the Config struct tree and
// applies expandString in place (via reassignment).
func expandConfig(c *Config) *Config {
	// Routes: map[string][]string
	for name, chain := range c.Routes {
		for i, e := range chain {
			c.Routes[name][i] = expandString(e)
		}
	}

	// Providers
	for i := range c.Providers {
		c.Providers[i].Name = expandString(c.Providers[i].Name)
		c.Providers[i].Impl = expandString(c.Providers[i].Impl)
		c.Providers[i].BaseURL = expandString(c.Providers[i].BaseURL)
		c.Providers[i].KeyEnv = expandString(c.Providers[i].KeyEnv)
		c.Providers[i].Kind = expandString(c.Providers[i].Kind)
		c.Providers[i].Header = expandString(c.Providers[i].Header)
		c.Providers[i].Notes = expandString(c.Providers[i].Notes)
		for j, c2 := range c.Providers[i].Command {
			c.Providers[i].Command[j] = expandString(c2)
		}
		for k, v := range c.Providers[i].Env {
			c.Providers[i].Env[k] = expandString(v)
		}
	}

	// Server
	c.Server.Host = expandString(c.Server.Host)
	c.Server.CertFile = expandString(c.Server.CertFile)
	c.Server.KeyFile = expandString(c.Server.KeyFile)

	// Catalog
	if c.Catalog.CacheFile != "" {
		c.Catalog.CacheFile = expandString(c.Catalog.CacheFile)
	}

	// Logging
	c.Logging.Level = expandString(c.Logging.Level)
	c.Logging.Format = expandString(c.Logging.Format)
	c.Logging.File = expandString(c.Logging.File)

	return c
}
