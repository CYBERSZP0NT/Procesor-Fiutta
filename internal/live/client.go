package live

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is FreeLLMAPI's catalog and license service.
const DefaultBaseURL = "https://api.freellmapi.co"

// DefaultSidecarURL is where a self-hosted FreeLLMAPI listens by default.
const DefaultSidecarURL = "http://localhost:3001"

// PinnedPubKey is the Ed25519 key the production catalog is signed with
// (the same key FreeLLMAPI pins in server/src/services/catalog-sync.ts).
// Anything unsigned or signed with another key is discarded.
const PinnedPubKey = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAq9yv4+3EeyMHKsfVYBhkcz1lYgIXSUeHNnN6tNgYX3k=
-----END PUBLIC KEY-----
`

// ErrNotModified is returned by FetchCatalog on HTTP 304.
var ErrNotModified = errors.New("catalog not modified")

// ErrUnreachable wraps network failures, so callers can keep a cached
// result instead of treating an outage as a bad key.
var ErrUnreachable = errors.New("service unreachable")

// License is the catalog service's verdict on a license key.
type License struct {
	Valid     bool      `json:"valid"`
	Plan      string    `json:"plan,omitempty"` // annual | lifetime
	Status    string    `json:"status,omitempty"`
	ExpiresAt string    `json:"expiresAt,omitempty"`
	Reason    string    `json:"reason,omitempty"` // unknown_key | expired | canceled | refunded
	CheckedAt time.Time `json:"checked_at"`
}

// Catalog is the signed FreeLLMAPI catalog (chat models only; other
// modalities are ignored).
type Catalog struct {
	Version     string  `json:"version"`
	GeneratedAt string  `json:"generatedAt"`
	Tier        string  `json:"tier"` // live | monthly
	Models      []Model `json:"models"`
}

// Model is one chat model of the catalog.
type Model struct {
	Platform           string `json:"platform"`
	ModelID            string `json:"modelId"`
	DisplayName        string `json:"displayName"`
	IntelligenceRank   int    `json:"intelligenceRank"` // 1 = best, within its platform
	SpeedRank          int    `json:"speedRank"`        // 1 = fastest … 11
	SizeLabel          string `json:"sizeLabel"`        // Frontier | Large | Medium | Small
	Limits             Limits `json:"limits"`
	MonthlyTokenBudget string `json:"monthlyTokenBudget,omitempty"`
	ContextWindow      int    `json:"contextWindow,omitempty"`
	Enabled            bool   `json:"enabled"`
	SupportsVision     bool   `json:"supportsVision"`
	SupportsTools      bool   `json:"supportsTools"`
	Modality           string `json:"modality,omitempty"` // text (default) | image | audio
}

// Limits are a catalog model's per-key caps (0 = unknown).
type Limits struct {
	RPM int `json:"rpm"`
	RPD int `json:"rpd"`
	TPM int `json:"tpm"`
	TPD int `json:"tpd"`
}

// Client talks to the catalog service and to a sidecar.
type Client struct {
	BaseURL string // catalog service (default DefaultBaseURL)
	PubKey  string // PEM, default PinnedPubKey
	HTTP    *http.Client
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// CheckLicense asks the catalog service whether key is an active license
// (GET /v1/license/check). It never activates anything. A rejected key is
// a License with Valid false, not an error; errors wrap ErrUnreachable
// when the service could not be asked.
func (c *Client) CheckLicense(ctx context.Context, key string) (License, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/v1/license/check", nil)
	if err != nil {
		return License{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	res, err := c.http().Do(req)
	if err != nil {
		return License{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 500 {
		return License{}, fmt.Errorf("%w: license check HTTP %d", ErrUnreachable, res.StatusCode)
	}
	var l License
	if err := json.Unmarshal(body, &l); err != nil {
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return License{Reason: "unknown_key", CheckedAt: time.Now()}, nil
		}
		return License{}, fmt.Errorf("license check HTTP %d: unexpected body", res.StatusCode)
	}
	if res.StatusCode >= 400 {
		l.Valid = false
		if l.Reason == "" {
			l.Reason = fmt.Sprintf("http_%d", res.StatusCode)
		}
	}
	l.CheckedAt = time.Now()
	return l, nil
}

// FetchCatalog downloads the catalog with key as Bearer (GET /v1/latest),
// verifies its Ed25519 signature over the exact bytes received and parses
// it. since, when set, lets the service answer 304 (ErrNotModified). The
// raw verified bytes and the signature are returned for caching.
func (c *Client) FetchCatalog(ctx context.Context, key, since string) (*Catalog, []byte, string, error) {
	u, err := url.Parse(c.base() + "/v1/latest")
	if err != nil {
		return nil, nil, "", err
	}
	if since != "" {
		q := u.Query()
		q.Set("since", since)
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, "", err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return nil, nil, "", ErrNotModified
	}
	if res.StatusCode != http.StatusOK {
		if res.StatusCode >= 500 {
			return nil, nil, "", fmt.Errorf("%w: catalog HTTP %d", ErrUnreachable, res.StatusCode)
		}
		return nil, nil, "", fmt.Errorf("catalog HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	sig := res.Header.Get("X-Catalog-Signature")
	cat, err := c.Verify(body, sig)
	if err != nil {
		return nil, nil, "", err
	}
	return cat, body, sig, nil
}

// Verify checks sig (base64 Ed25519) over body against the pinned key and
// parses the catalog. Cached catalogs go through it again on load.
func (c *Client) Verify(body []byte, sig string) (*Catalog, error) {
	if sig == "" {
		return nil, errors.New("catalog response missing signature")
	}
	pub, err := parsePubKey(c.PubKey)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return nil, fmt.Errorf("catalog signature: %w", err)
	}
	if !ed25519.Verify(pub, body, raw) {
		return nil, errors.New("catalog signature verification FAILED — discarding response")
	}
	var cat Catalog
	if err := json.Unmarshal(body, &cat); err != nil {
		return nil, fmt.Errorf("catalog payload: %w", err)
	}
	if cat.Version == "" || cat.Tier == "" {
		return nil, errors.New("catalog payload has unexpected shape: missing version or tier")
	}
	return &cat, nil
}

func parsePubKey(p string) (ed25519.PublicKey, error) {
	if p == "" {
		p = PinnedPubKey
	}
	p = strings.ReplaceAll(p, `\n`, "\n")
	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		return nil, errors.New("catalog public key: not PEM")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("catalog public key: %w", err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("catalog public key: not Ed25519")
	}
	return pub, nil
}

// ProbeSidecar checks a unified key against a FreeLLMAPI server
// (GET base/v1/models) and returns the model ids it serves.
func (c *Client) ProbeSidecar(ctx context.Context, base, key string) ([]string, error) {
	if base == "" {
		base = DefaultSidecarURL
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("sidecar rejected the key (HTTP %d)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sidecar /v1/models HTTP %d", res.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("sidecar /v1/models: %w", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}
