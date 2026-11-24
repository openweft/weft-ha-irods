package reconcile

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	dcs "github.com/openweft/weft-ha-dcs"
	"github.com/openweft/weft-ha-irods/internal/keyinstall"
)

func noInstall(string, string, keyinstall.Keys) (bool, error) { return false, nil }

func TestTick_RemovesTheUnusedControlPlaneKeyOnce(t *testing.T) {
	ctx := context.Background()
	store := dcs.NewMemStore()
	const cp = "/weft/irods/weftZone/keys/control_plane"
	store.PutKeyIfAbsent(ctx, cp, "CP-SECRET")
	var logs bytes.Buffer
	l := installLoop(t, store, slog.New(slog.NewTextHandler(&logs, nil)), noInstall)
	l.tick(ctx)
	if got, _ := store.GetKey(ctx, cp); got != "" {
		t.Fatalf("control_plane still present: %q", got)
	}
	if !strings.Contains(logs.String(), "removed unused control-plane key") || strings.Contains(logs.String(), "CP-SECRET") {
		t.Fatalf("log = %s", logs.String())
	}
	logs.Reset()
	l.tick(ctx) // idempotent: nothing more to remove, nothing logged
	if strings.Contains(logs.String(), "removed unused") {
		t.Fatalf("second tick logged a removal: %s", logs.String())
	}
}

type deleteRefused struct{ *dcs.MemStore }

func (deleteRefused) DeleteKey(context.Context, string) (bool, error) {
	return false, errors.New("refused")
}

func TestTick_ControlPlaneRemovalFailureIsLoggedAndRetried(t *testing.T) {
	var logs bytes.Buffer
	l := installLoop(t, deleteRefused{dcs.NewMemStore()}, slog.New(slog.NewTextHandler(&logs, nil)), noInstall)
	l.tick(context.Background())
	if !strings.Contains(logs.String(), "not removed") {
		t.Fatalf("failure not logged: %s", logs.String())
	}
}
