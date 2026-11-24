package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	dcs "github.com/openweft/weft-ha-dcs"
)

const removedMsg = "removed unused control-plane key"

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func TestRemoveUnusedControlPlaneKey_PlaintextPresent(t *testing.T) {
	ctx := context.Background()
	cfg := baseCfg()
	raw := dcs.NewMemStore()
	raw.PutKeyIfAbsent(ctx, keyPath(cfg.ZoneName, "control_plane"), "CP-SECRET")
	raw.PutKeyIfAbsent(ctx, keyPath(cfg.ZoneName, "zone"), "z")
	var buf bytes.Buffer
	if err := RemoveUnusedControlPlaneKey(ctx, cfg, raw, logTo(&buf)); err != nil {
		t.Fatal(err)
	}
	if got, _ := raw.GetKey(ctx, keyPath(cfg.ZoneName, "control_plane")); got != "" {
		t.Errorf("control_plane still present: %q", got)
	}
	if got, _ := raw.GetKey(ctx, keyPath(cfg.ZoneName, "zone")); got != "z" {
		t.Error("another key was touched")
	}
	if !strings.Contains(buf.String(), removedMsg) || strings.Contains(buf.String(), "CP-SECRET") {
		t.Errorf("log = %q", buf.String())
	}
}

func TestRemoveUnusedControlPlaneKey_SealedPresent(t *testing.T) {
	ctx := context.Background()
	cfg := baseCfg()
	raw := dcs.NewMemStore()
	sealed, err := dcs.NewSealedStore(raw, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealed.PutKeyIfAbsent(ctx, keyPath(cfg.ZoneName, "control_plane"), "CP-SECRET"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RemoveUnusedControlPlaneKey(ctx, cfg, sealed, logTo(&buf)); err != nil {
		t.Fatal(err)
	}
	if got, _ := raw.GetKey(ctx, keyPath(cfg.ZoneName, "control_plane")); got != "" {
		t.Errorf("sealed control_plane still present: %q", got)
	}
	if !strings.Contains(buf.String(), removedMsg) {
		t.Errorf("log = %q", buf.String())
	}
}

func TestRemoveUnusedControlPlaneKey_Absent(t *testing.T) {
	ctx := context.Background()
	cfg := baseCfg()
	var buf bytes.Buffer
	for range 2 { // idempotent
		if err := RemoveUnusedControlPlaneKey(ctx, cfg, dcs.NewMemStore(), logTo(&buf)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(buf.String(), removedMsg) {
		t.Errorf("absent key logged as removed: %q", buf.String())
	}
}

// deleteFails is a store whose DeleteKey refuses.
type deleteFails struct{ *dcs.MemStore }

func (deleteFails) DeleteKey(context.Context, string) (bool, error) {
	return false, errors.New("boom")
}

func TestRemoveUnusedControlPlaneKey_StoreErrorIsReturned(t *testing.T) {
	err := RemoveUnusedControlPlaneKey(context.Background(), baseCfg(), deleteFails{dcs.NewMemStore()}, logTo(&bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

// A store without DeleteKey is left alone.
func TestRemoveUnusedControlPlaneKey_NoDeleter(t *testing.T) {
	type plain struct{ dcs.Store }
	if err := RemoveUnusedControlPlaneKey(context.Background(), baseCfg(), plain{dcs.NewMemStore()}, logTo(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
}
