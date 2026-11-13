// Package catalog contains pluggable remote catalog adapters (each a small
// CatalogSource implementation over HTTP) plus the registry + assembler that
// turn `config.Catalog.Sources` into a core.Catalog.
//
// STEP 6 — model knowledge stops being static: these adapters + the local /
// builtin sources are merged by internal/core/catalog with
// precedence (local > remote > builtin) and cached to catalog.json.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/silverx/szmelc-router/internal/core"
)

// RemoteSource is one HTTP catalog feed. Adapters supply Parse; the base
// handles transport, timeouts and tolerance of absence.
type RemoteSource struct {
	Name    string // registry key, e.g. "awesome-llm-apis"
	URL     string // default endpoint (config can override per source)
	Timeout time.Duration
	HTTP    *http.Client

	// Parse turns the raw JSON body into notes + provider specs.
	// It decides how tolerant the feed is (unknown shapes → empty, not error,
	// UNLESS the body is not JSON at all).
	Parse func(body []byte) (notes []core.ModelNote, provs []core.ProviderSpec, err error)
}

func (r *RemoteSource) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: r.timeout()}
}

func (r *RemoteSource) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 15 * time.Second
}

// doFetch performs the HTTP GET (bounded by ctx) and returns the raw body.
func (r *RemoteSource) doFetch(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "szmelc-router/1.0 (catalog)")

	resp, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", r.Name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d", r.Name, resp.StatusCode)
	}
	return body, nil
}

// Fetch implements the notes half of the source.
func (r *RemoteSource) Fetch(ctx context.Context) ([]core.ModelNote, error) {
	body, err := r.doFetch(ctx)
	if err != nil {
		return nil, err
	}
	notes, _, perr := r.Parse(body)
	if perr != nil {
		return nil, fmt.Errorf("%s: parse: %w", r.Name, perr)
	}
	return notes, nil
}

// ProviderList implements the provider-enumeration half (used by `discover`).
func (r *RemoteSource) ProviderList(ctx context.Context) ([]core.ProviderSpec, error) {
	body, err := r.doFetch(ctx)
	if err != nil {
		return nil, err
	}
	_, provs, perr := r.Parse(body)
	if perr != nil {
		return nil, fmt.Errorf("%s: parse: %w", r.Name, perr)
	}
	return provs, nil
}

// Named wraps this feed as a core.NamedSource at the given precedence.
func (r *RemoteSource) Named(priority int) core.NamedSource {
	return core.NamedSource{
		Name:         r.Name,
		Priority:     priority,
		Fetch:        r.Fetch,
		ProviderList: r.ProviderList,
	}
}

// WithURL returns a copy pointed at a different endpoint (config override).
func (r *RemoteSource) WithURL(u string) *RemoteSource {
	cp := *r
	cp.URL = u
	return &cp
}

// ---------------------------------------------------------------------------
// Shared tolerant JSON helpers
// ---------------------------------------------------------------------------

// parseArray locates the feed's item array. A body that is not JSON at all
// is treated as an error (the endpoint probably moved); valid JSON that
// simply contains no array yields nil (tolerant of absence).
func parseArray(body []byte) (json.RawMessage, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("body is not JSON")
	}
	return findArray(body), nil
}

// findArray locates "the" array inside a JSON payload that may be:
// a bare array, an object with one of the known list keys
// (models/apis/entries/data/results), or an object with exactly one array
// field. This makes every adapter tolerate minor feed-shape drift.
func findArray(body []byte) json.RawMessage {
	var top []json.RawMessage
	if err := json.Unmarshal(body, &top); err == nil {
		if len(top) > 0 {
			return json.RawMessage(body)
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	for _, key := range []string{"models", "apis", "entries", "data", "results", "items", "feeds", "providers"} {
		if v, ok := obj[key]; ok {
			if arr, ok := asArray(v); ok {
				return arr
			}
		}
	}
	// Fallback: the sole array-valued field.
	var sole json.RawMessage
	for _, v := range obj {
		if arr, ok := asArray(v); ok {
			if sole != nil {
				return nil // more than one array → ambiguous, be strict
			}
			sole = arr
		}
	}
	if sole != nil {
		return sole
	}
	return nil
}

func asArray(v json.RawMessage) (json.RawMessage, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err == nil {
		if len(arr) > 0 {
			return json.RawMessage(v), true
		}
	}
	return nil, false
}

type strMap map[string]any

func (m strMap) s(key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func (m strMap) b(key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		return x != 0, true
	case string:
		switch x {
		case "true", "yes", "1", "required", "keyed":
			return true, true
		case "false", "no", "0", "optional", "keyless":
			return false, true
		}
	}
	return false, false
}

func (m strMap) list(key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if x, ok := v.([]any); ok {
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil // CSV strings are handled by the callers (split on ",")
}

// firstNonEmpty returns the first non-empty string among the given values.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
