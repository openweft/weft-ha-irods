# weft-ha-irods

Go-native HA operator for iRODS catalog providers, packaged as the
`irods-ha` plugin in openweft's catalogue.

One agent runs alongside every iRODS catalog service provider micro-VM.
Together with the L4 Caddy in `weft-agent`, three replicas spread across
DCs form an active-active iRODS zone : clients connect on 1247/tcp and
Caddy load-balances onto whichever provider currently passes the agent's
health probe.

## Architecture

| Layer            | Component                                              |
| ---------------- | ------------------------------------------------------ |
| Catalog database | `postgres-ha` (separate plugin — install first)        |
| Providers        | 3× `irods-server` (BSD-3-Clause) + this agent          |
| Routing          | L4 Caddy in `weft-agent` → 1247/tcp                    |
| Coordination     | etcd (zone keys + bootstrap leader election only)      |

iRODS itself is stateless once the iCAT schema is in place — providers
are read-write equivalent and failover is a load-balancer drain, NOT a
Postgres-style leader election. The agent's job is therefore narrower
than `weft-ha-postgresql`'s :

1. **Zone bootstrap** — the first provider to acquire an etcd advisory
   lock creates the iCAT schema in the shared Postgres + mints the
   negotiation / control-plane / zone keys. The keys are seeded into
   etcd so the other two providers pick them up instead of re-minting
   and ending up with a split-brain zone.
2. **Config reconciliation** — `/etc/irods/server_config.json` is
   templated from plugin inputs every tick ; an operator changing the
   `tenant_network_cidrs` input takes effect without an SSH session.
3. **Health probe** — runs `irods-grid` against the local server and
   exposes `/ready` + `/zone` on `:8009` for the L4 Caddy active probe.

What's intentionally **not** here :

- Resource-server placement — iRODS' own `imeta`/`iadmin` surface owns
  storage resource policy. The plugin provisions a per-DC local resource
  vault and that's it.
- Replication strategy — `core.re` rules (e.g. "replicate to two DCs on
  PUT") are operator-authored ; the agent does not mint them.
- Federation — set `zone_key` in the plugin inputs to enable federation,
  but the federation handshake stays an `iadmin mkzone` operator step.

## Layout

```
cmd/weft-ha-irods/      cobra entrypoint, agent subcommand
internal/
  api/                  :8009 /ready, /zone, /health
  bootstrap/            first-time iCAT schema + key seeding
  config/               plugin-input → in-process Config
  dcs/                  etcd-backed key + lock store
  irods/                local irods-server controller (status, restart)
  reconcile/            tick loop : bootstrap once, then config + health
```

## Build

```sh
task build           # host arch
task build-linux     # linux/arm64 + linux/amd64 (what the micro-VM runs)
task test            # go test -race
```

## License

BSD-3-Clause — same as iRODS itself, so the packaged OCI image stays
under one license.
