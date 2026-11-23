package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"testing"

	dcs "github.com/openweft/weft-ha-dcs"
	"github.com/openweft/weft-ha-irods/internal/config"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func baseCfg() config.Config {
	return config.Config{
		NodeName: "irods-1",
		ZoneName: "weftZone",
	}
}

func TestRun_LeaderMintsKeysAndSeedsDCS(t *testing.T) {
	store := dcs.NewMemStore()
	r, err := Run(context.Background(), baseCfg(), store, discard())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.LeaderRanFirstBootstrap {
		t.Error("first invocation on empty DCS should report LeaderRanFirstBootstrap=true")
	}
	if len(r.NegotiationKey) != 32 || len(r.ZoneKey) != 32 {
		t.Errorf("minted keys should be 32 hex chars, got %d/%d",
			len(r.NegotiationKey), len(r.ZoneKey))
	}
	// iRODS 5 has no control-plane key: none is minted any more.
	if cp, _ := store.GetKey(context.Background(), keyPath("weftZone", "control_plane")); cp != "" {
		t.Error("a control_plane key was minted")
	}
	// DCS should now hold them.
	got, _ := store.GetKey(context.Background(), keyPath("weftZone", "negotiation"))
	if got != r.NegotiationKey {
		t.Errorf("DCS negotiation key mismatch")
	}
}

func TestRun_FollowerReadsExistingKeys(t *testing.T) {
	store := dcs.NewMemStore()
	// Pre-seed as if a peer leader already minted.
	_, _ = store.PutKeyIfAbsent(context.Background(), keyPath("weftZone", "negotiation"), "neg-key")
	_, _ = store.PutKeyIfAbsent(context.Background(), keyPath("weftZone", "control_plane"), "cpk-key")
	_, _ = store.PutKeyIfAbsent(context.Background(), keyPath("weftZone", "zone"), "z-key")

	r, err := Run(context.Background(), baseCfg(), store, discard())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.LeaderRanFirstBootstrap {
		t.Error("follower should NOT report LeaderRanFirstBootstrap=true")
	}
	if r.NegotiationKey != "neg-key" || r.ZoneKey != "z-key" {
		t.Errorf("follower should read peer-seeded keys verbatim, got %+v", r)
	}
	// A value an older release stored is left exactly as it was.
	if cp, _ := store.GetKey(context.Background(), keyPath("weftZone", "control_plane")); cp != "cpk-key" {
		t.Errorf("legacy control_plane value changed to %q", cp)
	}
}

func TestRun_OperatorProvidedKeysHonoured(t *testing.T) {
	store := dcs.NewMemStore()
	cfg := baseCfg()
	cfg.NegotiationKey = "op-neg"
	cfg.ZoneKey = "op-zone"
	r, err := Run(context.Background(), cfg, store, discard())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.NegotiationKey != "op-neg" || r.ZoneKey != "op-zone" {
		t.Errorf("operator-provided keys should be used verbatim, got %+v", r)
	}
}

func TestRun_IsIdempotentAcrossTicks(t *testing.T) {
	store := dcs.NewMemStore()
	r1, err := Run(context.Background(), baseCfg(), store, discard())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	r2, err := Run(context.Background(), baseCfg(), store, discard())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if r1.NegotiationKey != r2.NegotiationKey {
		t.Error("idempotent Run must observe the SAME keys on every tick")
	}
	if r2.LeaderRanFirstBootstrap {
		t.Error("second Run should NOT report LeaderRanFirstBootstrap=true")
	}
}
