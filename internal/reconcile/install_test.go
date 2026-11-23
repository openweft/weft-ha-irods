package reconcile

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	dcs "github.com/openweft/weft-ha-dcs"
	"github.com/openweft/weft-ha-irods/internal/api"
	"github.com/openweft/weft-ha-irods/internal/irods"
	"github.com/openweft/weft-ha-irods/internal/keyinstall"
)

// rotated answers a different zone key than the one stored.
type rotated struct {
	dcs.Store
	zone string
}

func (r rotated) GetKey(ctx context.Context, path string) (string, error) {
	if strings.HasSuffix(path, "/keys/zone") {
		return r.zone, nil
	}
	return r.Store.GetKey(ctx, path)
}

type call struct {
	path, owner string
	keys        keyinstall.Keys
}

func installLoop(t *testing.T, store dcs.Store, log *slog.Logger, fn func(string, string, keyinstall.Keys) (bool, error)) *Loop {
	t.Helper()
	c := cfg()
	c.ServerConfig = "/etc/irods/server_config.json"
	c.ServerConfigOwner = "irods"
	server := &irods.FakeController{NextStatus: irods.Status{Up: true, ZoneName: "weftZone"}}
	l := New(c, store, server, api.New(":0", "irods-1", "dc1"), time.Second, log)
	l.install = fn
	return l
}

func TestTick_InstallsMintedKeysEveryTickAndFollowsRotation(t *testing.T) {
	store := dcs.NewMemStore()
	var calls []call
	l := installLoop(t, store, discard(), func(p, o string, k keyinstall.Keys) (bool, error) {
		calls = append(calls, call{p, o, k})
		return true, nil
	})
	l.tick(context.Background())
	l.tick(context.Background())
	if len(calls) != 2 {
		t.Fatalf("installer called %d times, want once per tick", len(calls))
	}
	c := calls[0]
	if c.path != "/etc/irods/server_config.json" || c.owner != "irods" {
		t.Fatalf("wrong target: %+v", c)
	}
	if len(c.keys.Negotiation) != 32 || c.keys.Zone == "" || c.keys != calls[1].keys {
		t.Fatalf("keys not the stored ones: %+v vs %+v", c.keys, calls[1].keys)
	}

	// Rotation: the store (written out of band, e.g. re-sealed by an
	// operator) now answers another zone key ; the next tick installs it.
	l.store = rotated{Store: store, zone: "rotatedZoneKey"}
	l.tick(context.Background())
	if got := calls[len(calls)-1].keys.Zone; got != "rotatedZoneKey" {
		t.Fatalf("rotated key not installed: %q", got)
	}
}

func TestTick_InstallFailureDoesNotStopTheProbeAndLeaksNothing(t *testing.T) {
	store := dcs.NewMemStore()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	var keys keyinstall.Keys
	l := installLoop(t, store, log, func(_, _ string, k keyinstall.Keys) (bool, error) {
		keys = k
		return false, errors.New("refused")
	})
	l.tick(context.Background())
	if !strings.Contains(logs.String(), "not installed") {
		t.Fatalf("failure not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), keys.Negotiation) || strings.Contains(logs.String(), keys.Zone) {
		t.Fatal("a key value reached the log")
	}
}

func TestTick_ChangedIsLoggedWithoutKeys(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	var keys keyinstall.Keys
	l := installLoop(t, dcs.NewMemStore(), log, func(_, _ string, k keyinstall.Keys) (bool, error) {
		keys = k
		return true, nil
	})
	l.tick(context.Background())
	if !strings.Contains(logs.String(), "restart it to apply") {
		t.Fatalf("change not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), keys.Negotiation) || strings.Contains(logs.String(), keys.Zone) {
		t.Fatal("a key value reached the log")
	}
}

func TestInstallKeys_DefaultInstallerIsKeyinstall(t *testing.T) {
	c := cfg()
	c.ServerConfig = t.TempDir() + "/absent.json"
	c.ServerConfigOwner = "irods"
	l := New(c, dcs.NewMemStore(), &irods.FakeController{}, api.New(":0", "n", "d"), time.Second, discard())
	l.install = nil
	l.tick(context.Background()) // must not panic: the missing file is only logged
}
