// Package bootstrap handles the one-shot zone bootstrap : iCAT
// schema creation + zone-key minting + seeding into the DCS so the
// other two providers join an already-initialised zone instead of
// racing to mint their own keys (which would split-brain the zone).
//
// The bootstrap path is idempotent : every provider runs it at
// startup, but only the lock holder does real work. The second +
// third providers acquire-the-lock-immediately-after-release and
// observe : "keys already in DCS, schema already in iCAT — nothing
// to do".
package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/openweft/weft-ha-irods/internal/config"
	"github.com/openweft/weft-ha-irods/internal/dcs"
)

// Result is what Run reports back to the reconcile loop. The agent
// installs the three keys into /etc/irods/server_config.json once
// they've been resolved (either minted here or read from DCS).
type Result struct {
	NegotiationKey  string
	ControlPlaneKey string
	ZoneKey         string
	// LeaderRanFirstBootstrap is true when THIS provider did the iCAT
	// schema creation + key minting. False means we observed an
	// already-bootstrapped zone (keys read from DCS, schema present).
	// Mostly useful for log + metrics ("did we initialise the zone, or
	// were we a follower ?").
	LeaderRanFirstBootstrap bool
}

// keyPath returns the DCS path for a named zone key.
func keyPath(zone, name string) string {
	return fmt.Sprintf("/weft/irods/%s/keys/%s", zone, name)
}

// Run executes the one-shot zone bootstrap. Idempotent : safe to call
// every reconcile tick — once the keys are in DCS, subsequent calls
// short-circuit to a key read.
func Run(ctx context.Context, cfg config.Config, store dcs.Store, log *slog.Logger) (Result, error) {
	// Fast path : if the negotiation key is already in DCS, we
	// assume the zone is bootstrapped. (Reading three keys would
	// be marginally safer but the bootstrap path writes all three
	// under the same lock — partial state implies a control-plane
	// bug we should fail loudly on, not paper over.)
	existing, err := store.GetKey(ctx, keyPath(cfg.ZoneName, "negotiation"))
	if err != nil {
		return Result{}, fmt.Errorf("dcs GetKey(negotiation): %w", err)
	}
	if existing != "" {
		return readExistingKeys(ctx, cfg, store)
	}

	// Slow path : acquire the bootstrap lock, mint keys, write them.
	release, err := store.AcquireBootstrapLock(ctx, cfg.NodeName)
	if err != nil {
		return Result{}, fmt.Errorf("acquire bootstrap lock: %w", err)
	}
	defer release()

	// Recheck under the lock — a peer may have raced us between
	// our pre-check and the lock acquire.
	existing, err = store.GetKey(ctx, keyPath(cfg.ZoneName, "negotiation"))
	if err != nil {
		return Result{}, fmt.Errorf("dcs GetKey under lock: %w", err)
	}
	if existing != "" {
		log.Info("bootstrap : peer minted keys between pre-check and lock — joining as follower")
		return readExistingKeys(ctx, cfg, store)
	}

	log.Info("bootstrap : minting zone keys", "zone", cfg.ZoneName)
	r, err := mintAndStore(ctx, cfg, store)
	if err != nil {
		return Result{}, err
	}
	r.LeaderRanFirstBootstrap = true
	return r, nil
}

// readExistingKeys pulls the three zone keys out of DCS. Called when
// either pre-check or under-lock-recheck observed the negotiation key
// already present.
func readExistingKeys(ctx context.Context, cfg config.Config, store dcs.Store) (Result, error) {
	neg, err := store.GetKey(ctx, keyPath(cfg.ZoneName, "negotiation"))
	if err != nil {
		return Result{}, fmt.Errorf("dcs GetKey(negotiation): %w", err)
	}
	cpk, err := store.GetKey(ctx, keyPath(cfg.ZoneName, "control_plane"))
	if err != nil {
		return Result{}, fmt.Errorf("dcs GetKey(control_plane): %w", err)
	}
	zk, err := store.GetKey(ctx, keyPath(cfg.ZoneName, "zone"))
	if err != nil {
		return Result{}, fmt.Errorf("dcs GetKey(zone): %w", err)
	}
	return Result{
		NegotiationKey:  neg,
		ControlPlaneKey: cpk,
		ZoneKey:         zk,
	}, nil
}

// mintAndStore generates the three zone keys, applying operator-provided
// values from Config when present, and writes them to DCS under the
// bootstrap lock. The "minted" keys are 32 hex characters (16 random
// bytes) — iRODS only requires 32 chars and accepts any ASCII.
func mintAndStore(ctx context.Context, cfg config.Config, store dcs.Store) (Result, error) {
	pick := func(operatorProvided string) (string, error) {
		if operatorProvided != "" {
			return operatorProvided, nil
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("crypto/rand for zone key: %w", err)
		}
		return hex.EncodeToString(b[:]), nil
	}
	neg, err := pick(cfg.NegotiationKey)
	if err != nil {
		return Result{}, err
	}
	cpk, err := pick(cfg.ControlPlaneKey)
	if err != nil {
		return Result{}, err
	}
	zk, err := pick(cfg.ZoneKey)
	if err != nil {
		return Result{}, err
	}
	for _, kv := range []struct{ k, v string }{
		{"negotiation", neg},
		{"control_plane", cpk},
		{"zone", zk},
	} {
		if _, err := store.PutKeyIfAbsent(ctx, keyPath(cfg.ZoneName, kv.k), kv.v); err != nil {
			return Result{}, fmt.Errorf("dcs PutKeyIfAbsent(%s): %w", kv.k, err)
		}
	}
	return Result{
		NegotiationKey:  neg,
		ControlPlaneKey: cpk,
		ZoneKey:         zk,
	}, nil
}
