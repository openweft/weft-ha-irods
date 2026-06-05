// Package config holds the static bootstrap configuration for a
// weft-ha-irods agent and its validation.
package config

import (
	"fmt"
	"time"
)

// Config is the static bootstrap configuration for one agent. In
// production it's populated from CLI flags fed by the irods-ha
// plugin's `env_from` mapping ; one Config per running provider VM.
type Config struct {
	// NodeName uniquely identifies this provider within the zone. Used
	// as the bootstrap-lock owner identity in etcd.
	NodeName string
	// ZoneName is the iRODS zone (namespace clients address) the
	// providers serve. Immutable once the iCAT schema is bootstrapped.
	ZoneName string
	// DC is the failure domain (datacenter / cell) this provider lives
	// in. Used in log lines + the /zone response body.
	DC string
	// EtcdEndpoints lists the etcd endpoints backing the key store +
	// bootstrap advisory lock.
	EtcdEndpoints []string

	// AdminPassword is the bootstrap password for the rodsadmin user.
	// On first run, the bootstrap path passes this to `irods` /
	// `iadmin moduser rodsadmin password`.
	AdminPassword string

	// Catalog Postgres connection. The agent dials directly during
	// bootstrap to create the database + schema ; once iRODS itself
	// is up, the iRODS server holds its own connection pool.
	ICATDBHost     string
	ICATDBPort     int
	ICATDBName     string
	ICATDBUser     string
	ICATDBPassword string

	// Zone keys. Empty values mean "the bootstrap leader mints one + seeds
	// it via etcd" ; non-empty values are operator-provided pre-shared
	// secrets the agent installs verbatim.
	NegotiationKey  string
	ControlPlaneKey string
	ZoneKey         string

	// APIAddr is the listen address for the role API (the L4 Caddy in
	// weft-agent active-probes /ready here).
	APIAddr string
	// MetricsAddr is the Prometheus /metrics listen address. Separate
	// port from APIAddr so a stuck scrape handler can't stall the role
	// probe.
	MetricsAddr string

	// BootstrapTimeout caps how long the agent waits to acquire the
	// bootstrap advisory lock on a fresh install. Past this point we
	// log + give up that tick ; the next reconcile retries.
	BootstrapTimeout time.Duration
}

// Validate reports the first problem with c, or nil if it's usable.
func (c Config) Validate() error {
	switch {
	case c.NodeName == "":
		return fmt.Errorf("node-name must not be empty")
	case c.ZoneName == "":
		return fmt.Errorf("zone-name must not be empty")
	case c.DC == "":
		return fmt.Errorf("dc (failure domain) must not be empty")
	case len(c.EtcdEndpoints) == 0:
		return fmt.Errorf("at least one etcd endpoint is required")
	case c.AdminPassword == "":
		return fmt.Errorf("admin-password must not be empty")
	case c.ICATDBHost == "":
		return fmt.Errorf("icat-db-host must not be empty")
	case c.ICATDBPort == 0:
		return fmt.Errorf("icat-db-port must be > 0")
	case c.ICATDBUser == "":
		return fmt.Errorf("icat-db-user must not be empty")
	case c.ICATDBPassword == "":
		return fmt.Errorf("icat-db-password must not be empty")
	case c.APIAddr == "":
		return fmt.Errorf("api-addr must not be empty")
	case c.MetricsAddr == "":
		return fmt.Errorf("metrics-addr must not be empty")
	default:
		return nil
	}
}
