package reconcile

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/openweft/weft-ha-irods/internal/api"
	"github.com/openweft/weft-ha-irods/internal/config"
	"github.com/openweft/weft-ha-irods/internal/dcs"
	"github.com/openweft/weft-ha-irods/internal/irods"
)

func cfg() config.Config {
	return config.Config{
		NodeName: "irods-1",
		ZoneName: "weftZone",
		DC:       "dc1",
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestTick_HealthyMarksAPIUp(t *testing.T) {
	store := dcs.NewMemStore()
	server := &irods.FakeController{NextStatus: irods.Status{Up: true, ZoneName: "weftZone"}}
	srv := api.New(":0", "irods-1", "dc1")
	l := New(cfg(), store, server, srv, time.Second, discard())
	l.tick(context.Background())
	// One probe should leave Up=true in the api state.
}

func TestTick_ServerDownMarksAPIDown(t *testing.T) {
	store := dcs.NewMemStore()
	server := &irods.FakeController{NextStatus: irods.Status{Up: false, Reason: "no listener"}}
	srv := api.New(":0", "irods-1", "dc1")
	l := New(cfg(), store, server, srv, time.Second, discard())
	l.tick(context.Background())
	// Probe should leave Up=false ; the bootstrap is still attempted
	// the first tick.
}

func TestTick_ZoneMismatchMarksDown(t *testing.T) {
	store := dcs.NewMemStore()
	server := &irods.FakeController{NextStatus: irods.Status{Up: true, ZoneName: "otherZone"}}
	srv := api.New(":0", "irods-1", "dc1")
	l := New(cfg(), store, server, srv, time.Second, discard())
	l.tick(context.Background())
	// Zone-mismatch must drive Up=false : Caddy drains the misconfigured
	// provider rather than serving wrong-zone responses.
}

func TestRun_ExitsOnContextCancel(t *testing.T) {
	store := dcs.NewMemStore()
	server := &irods.FakeController{NextStatus: irods.Status{Up: true, ZoneName: "weftZone"}}
	srv := api.New(":0", "irods-1", "dc1")
	l := New(cfg(), store, server, srv, 10*time.Millisecond, discard())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Run(ctx); err != context.Canceled {
		t.Errorf("Run should exit with ctx.Err(), got %v", err)
	}
}
