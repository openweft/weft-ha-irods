package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/openweft/weft-ha-irods/internal/dcs"
)

// A zone seeded in plaintext by an older agent is re-sealed on start, the
// providers still read the same keys, and nothing readable is left in the store.
func TestLegacyPlaintextZoneIsResealedOnStart(t *testing.T) {
	ctx := context.Background()
	cfg := baseCfg()
	raw := dcs.NewMemStore()
	legacy := map[string]string{"negotiation": "NEG-plain", "control_plane": "CP-plain", "zone": "ZONE-plain"}
	for k, v := range legacy {
		raw.PutKeyIfAbsent(ctx, keyPath(cfg.ZoneName, k), v)
	}
	sealed, err := dcs.NewSealedStore(raw, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		r, err := Run(ctx, cfg, sealed, discard())
		if err != nil || r.NegotiationKey != "NEG-plain" || r.ControlPlaneKey != "CP-plain" || r.ZoneKey != "ZONE-plain" {
			t.Fatalf("result %+v, err %v", r, err)
		}
	}
	for k, v := range legacy {
		stored, _ := raw.GetKey(ctx, keyPath(cfg.ZoneName, k))
		if strings.Contains(stored, v) {
			t.Errorf("%s still plaintext in the store", k)
		}
	}
}
