// Command weft-ha-irods is the Go-native HA operator for iRODS
// catalog providers. One agent runs alongside every provider
// micro-VM and drives :
//
//   - zone bootstrap (iCAT schema + key minting under an etcd
//     advisory lock ; idempotent across providers),
//   - role API at :8009 for the L4 Caddy in weft-agent to probe,
//   - per-tick health check against the local irods-server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/openweft/weft-ha-irods/internal/api"
	"github.com/openweft/weft-ha-irods/internal/config"
	"github.com/openweft/weft-ha-irods/internal/dcs"
	"github.com/openweft/weft-ha-irods/internal/irods"
	"github.com/openweft/weft-ha-irods/internal/reconcile"
)

// Build metadata, injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "weft-ha-irods",
		Short:        "Go-native HA operator for iRODS catalog providers",
		Long:         "weft-ha-irods bootstraps the iRODS zone (iCAT + keys), exposes a\nrole API for the L4 Caddy in weft-agent, and runs a health probe so\nthe upstream pool drains unhealthy providers.",
		SilenceUsage: true,
	}
	root.AddCommand(versionCmd(), agentCmd())
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "weft-ha-irods %s (commit %s, built %s)\n", version, commit, date)
			return err
		},
	}
}

func agentCmd() *cobra.Command {
	var (
		cfg    config.Config
		period time.Duration
	)
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the per-provider HA agent (one per iRODS catalog server)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			return runAgent(cmd.Context(), cfg, period)
		},
	}
	f := cmd.Flags()
	f.StringVar(&cfg.NodeName, "node-name", "", "unique provider name within the zone")
	f.StringVar(&cfg.ZoneName, "zone-name", "weftZone", "iRODS zone name")
	f.StringVar(&cfg.DC, "dc", "", "failure domain (datacenter / cell)")
	f.StringSliceVar(&cfg.EtcdEndpoints, "etcd", nil, "etcd endpoints (comma-separated)")
	f.StringVar(&cfg.AdminPassword, "admin-password", "", "rodsadmin bootstrap password")
	f.StringVar(&cfg.ICATDBHost, "icat-db-host", "", "catalog Postgres host")
	f.IntVar(&cfg.ICATDBPort, "icat-db-port", 5432, "catalog Postgres port")
	f.StringVar(&cfg.ICATDBName, "icat-db-name", "ICAT", "catalog Postgres database name")
	f.StringVar(&cfg.ICATDBUser, "icat-db-user", "irods", "catalog Postgres user")
	f.StringVar(&cfg.ICATDBPassword, "icat-db-password", "", "catalog Postgres password")
	f.StringVar(&cfg.NegotiationKey, "negotiation-key", "", "iRODS native auth negotiation key (empty = mint + seed via etcd)")
	f.StringVar(&cfg.ControlPlaneKey, "control-plane-key", "", "iRODS control-plane key (empty = mint + seed)")
	f.StringVar(&cfg.ZoneKey, "zone-key", "", "inter-zone trust key (empty = mint + seed)")
	f.StringVar(&cfg.APIAddr, "api-addr", ":8009", "role API listen address")
	f.StringVar(&cfg.MetricsAddr, "metrics-addr", ":9102", "Prometheus metrics listen address")
	f.DurationVar(&cfg.BootstrapTimeout, "bootstrap-timeout", 30*time.Second, "wait-for-lock timeout during bootstrap")
	f.DurationVar(&period, "reconcile-interval", 5*time.Second, "reconcile loop interval")
	return cmd
}

func runAgent(ctx context.Context, cfg config.Config, period time.Duration) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// DCS — MemStore for the scaffold ; the etcd-backed implementation
	// lands alongside the live integration test harness.
	store := dcs.NewMemStore()
	defer func() { _ = store.Close() }()

	// iRODS server controller. The scaffold ships a FakeController
	// that always reports Up=true ; the production controller (next
	// milestone) shells out to `irods-grid status` + `iadmin lz`.
	server := &irods.FakeController{NextStatus: irods.Status{Up: true, ZoneName: cfg.ZoneName}}

	apiSrv := api.New(cfg.APIAddr, cfg.NodeName, cfg.DC)
	if err := apiSrv.Start(); err != nil {
		return fmt.Errorf("starting role API: %w", err)
	}
	defer shutdown(apiSrv)

	log.Info("weft-ha-irods agent started",
		"node", cfg.NodeName, "zone", cfg.ZoneName, "dc", cfg.DC,
		"api", cfg.APIAddr, "metrics", cfg.MetricsAddr)

	loop := reconcile.New(cfg, store, server, apiSrv, period, log)
	if err := loop.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

type shutdowner interface {
	Shutdown(context.Context) error
}

func shutdown(s shutdowner) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}
