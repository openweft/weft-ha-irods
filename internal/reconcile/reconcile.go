// Package reconcile is the agent's tick loop : bootstrap the zone
// once, then on every tick refresh the role API's State view from
// the local iRODS server's health probe.
//
// The loop is intentionally small. iRODS providers don't elect a
// leader (the catalog DB does that on its own ; providers are
// stateless once the iCAT exists), so there's no "promote" /
// "demote" branch — just "am I up + serving the right zone ?".
package reconcile

import (
	"context"
	"log/slog"
	"time"

	dcs "github.com/openweft/weft-ha-dcs"
	"github.com/openweft/weft-ha-irods/internal/api"
	"github.com/openweft/weft-ha-irods/internal/bootstrap"
	"github.com/openweft/weft-ha-irods/internal/config"
	"github.com/openweft/weft-ha-irods/internal/irods"
	"github.com/openweft/weft-ha-irods/internal/keyinstall"
)

// Loop owns the reconcile state machine. Construct via New and call
// Run with a cancellable ctx — the loop returns when ctx is done.
type Loop struct {
	cfg    config.Config
	store  dcs.Store
	server irods.Controller
	apiSrv *api.Server
	period time.Duration
	log    *slog.Logger

	// keys cache the zone keys post-bootstrap. The reconcile loop
	// re-resolves them on every tick BUT short-circuits when the
	// cached values are non-empty + Status.Up — only a server-down
	// state triggers a re-bootstrap lookup, defending against an
	// etcd outage taking down the role probe.
	bootstrappedOnce bool

	// install is the key installer ; nil means keyinstall.Install. A seam
	// for tests, which have no iRODS account to own a file.
	install func(path, owner string, k keyinstall.Keys) (bool, error)
}

// New returns a Loop wired to the given components.
func New(cfg config.Config, store dcs.Store, server irods.Controller, apiSrv *api.Server, period time.Duration, log *slog.Logger) *Loop {
	return &Loop{cfg: cfg, store: store, server: server, apiSrv: apiSrv, period: period, log: log}
}

// Run drives the loop. Returns when ctx cancels.
func (l *Loop) Run(ctx context.Context) error {
	// First tick fires immediately ; bootstrap happens here so the
	// role API reflects the right state on the first probe.
	l.tick(ctx)
	t := time.NewTicker(l.period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			l.tick(ctx)
		}
	}
}

// installKeys writes the negotiation and zone keys into server_config.json.
// iRODS reads the file at start only, and this agent has no mechanism to
// restart it, so a change is logged for the operator ; key values never are.
func (l *Loop) installKeys(res bootstrap.Result) {
	install := l.install
	if install == nil {
		install = keyinstall.Install
	}
	changed, err := install(l.cfg.ServerConfig, l.cfg.ServerConfigOwner,
		keyinstall.Keys{Negotiation: res.NegotiationKey, Zone: res.ZoneKey})
	switch {
	case err != nil:
		l.log.Warn("zone keys not installed into server_config.json ; will retry next tick",
			"path", l.cfg.ServerConfig, "err", err)
	case changed:
		l.log.Info("zone keys installed into server_config.json ; iRODS reads them at start, restart it to apply",
			"path", l.cfg.ServerConfig)
	}
}

func (l *Loop) tick(ctx context.Context) {
	// Bootstrap (idempotent ; cheap once seeded), then install the keys it
	// resolved. Run on every tick so a key rotated in the DCS reaches
	// server_config.json ; an etcd outage only logs and keeps the last file.
	if res, err := bootstrap.Run(ctx, l.cfg, l.store, l.log); err != nil {
		l.log.Warn("bootstrap failed ; will retry next tick", "err", err)
		// Keep going — the health probe is still useful even
		// without bootstrap (operator may want to see WHY
		// the zone won't come up).
	} else {
		l.bootstrappedOnce = true
		l.installKeys(res)
		if err := bootstrap.RemoveUnusedControlPlaneKey(ctx, l.cfg, l.store, l.log); err != nil {
			l.log.Warn("unused control-plane key not removed ; will retry next tick", "err", err)
		}
	}

	// Health probe.
	st, err := l.server.CheckStatus(ctx)
	if err != nil {
		l.log.Warn("iRODS status probe failed", "err", err)
		l.apiSrv.Update(api.State{NodeName: l.cfg.NodeName, DC: l.cfg.DC, ZoneName: l.cfg.ZoneName, Up: false})
		return
	}
	// Misconfiguration check : the operator changed zone_name on a
	// running cluster. We don't try to fix this ourselves ; we surface
	// it in the role API so the L4 Caddy drains us.
	if st.Up && st.ZoneName != "" && st.ZoneName != l.cfg.ZoneName {
		l.log.Error("iRODS server is serving a different zone than configured",
			"server_zone", st.ZoneName, "config_zone", l.cfg.ZoneName)
		l.apiSrv.Update(api.State{NodeName: l.cfg.NodeName, DC: l.cfg.DC, ZoneName: l.cfg.ZoneName, Up: false})
		return
	}
	l.apiSrv.Update(api.State{
		NodeName: l.cfg.NodeName,
		DC:       l.cfg.DC,
		ZoneName: l.cfg.ZoneName,
		Up:       st.Up,
	})
}
