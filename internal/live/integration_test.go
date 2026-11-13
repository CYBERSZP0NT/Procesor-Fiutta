package live

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRealCatalogVerifies pulls the public (monthly) catalog from
// api.freellmapi.co and checks it against the pinned production key and
// the parser. Opt-in: FREELLM_LIVE_INTEGRATION=1 go test ./internal/live.
func TestRealCatalogVerifies(t *testing.T) {
	if os.Getenv("FREELLM_LIVE_INTEGRATION") == "" {
		t.Skip("set FREELLM_LIVE_INTEGRATION=1 to hit api.freellmapi.co")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl := &Client{}
	cat, _, _, err := cl.FetchCatalog(ctx, os.Getenv("FREELLM_API_KEY"), "")
	if err != nil {
		t.Fatal(err)
	}
	es := Entries(cat, nil, nil, nil, 1<<20)
	t.Logf("tier %s v%s: %d models, %d chat entries; richest: %s", cat.Tier, cat.Version, len(cat.Models), len(es), es[0].Model)
	if len(cat.Models) == 0 || len(es) == 0 {
		t.Fatal("empty catalog")
	}
	// Models per provider name, to compare with defaults/providers.yaml
	// when a new platform shows up.
	byProvider := map[string]int{}
	for _, m := range cat.Models {
		byProvider[ProviderFor(m.Platform)]++
	}
	t.Logf("models by provider name: %v", byProvider)
	if l, err := cl.CheckLicense(ctx, "fla_szmelc_integration_invalid"); err != nil || l.Valid || l.Reason == "" {
		t.Fatalf("an invalid key must come back valid=false with a reason: %+v, %v", l, err)
	}
}
