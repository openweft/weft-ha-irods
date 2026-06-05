# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.2.0-rc1] — 2026-06-05

This release candidate turns the v0.1 scaffold into an operational
agent : the iRODS health probe is now a real `os/exec` shell-out
against `irods-grid` + `iadmin lz` on the loopback server, the DCS
is etcd-backed under the `/weft/irods/<zone>/...` prefix the bootstrap
package already addresses, and the whole thing ships as a multi-arch
OCI image on top of `debian:12-slim` + the iRODS Consortium apt repo.

### Added

- `internal/irods` : `CommandController`, a production-ready
  `Controller` implementation that shells out to `irods-grid status
  --all` and looks for the literal `"Server is up."` marker in
  stdout. Best-effort enriches `Status.ZoneName` from `iadmin lz`
  (first non-empty line wins). 5-second per-command timeout, zero
  retries — the reconcile loop's 5-second tick is the retry policy.
  Failed exec OR a non-Up stdout surface as `Up=false` with a
  descriptive `Reason` instead of bubbling up an error, so the L4
  pool sees a clean drain signal. `FakeController` stays exported
  for unit tests + smoke dev. Tests cover up/down/exec-fail/iadmin-
  fail-keeps-up/multiline-zone via a `commandRunner` seam.
- `internal/dcs/etcd.go` : `EtcdStore` implementing `Store` against
  `go.etcd.io/etcd/client/v3`. Lazy-opens a client +
  `concurrency.Session` on first use ; `GetKey` is `client.Get`,
  `PutKeyIfAbsent` is a single-shot `Txn` comparing
  `CreateRevision == 0`, `AcquireBootstrapLock` is a
  `concurrency.NewMutex` at `/weft/irods/<zone>/bootstrap-lock`.
  Closing the store drops the lease so a fenced agent releases the
  lock within session TTL. Same pattern as the `weft-ha-forgejo`
  sibling, just with the iRODS key prefix.
- `cmd/weft-ha-irods/main.go` : runtime DCS + Controller selection.
  `WEFT_HA_IRODS_ETCD=host:2379[,...]` switches to `EtcdStore` (also
  honours `--etcd` flags as a fallback) ; default stays `MemStore`
  for single-host smoke. `WEFT_HA_IRODS_USE_REAL_CONTROLLER=1`
  switches to `CommandController` (with `WEFT_HA_IRODS_GRID_BINARY`
  / `WEFT_HA_IRODS_IADMIN_BINARY` to override paths) ; default
  stays `FakeController`. Same binary, zero build-time toggles.
- `Dockerfile` : multi-stage build. Stage 1 (`golang:1.26-alpine`)
  cross-builds the agent pure-Go (CGO=0) for `$TARGETARCH`. Stage 2
  (`debian:12-slim`) installs `irods-server` +
  `irods-database-plugin-postgres` from the official iRODS
  Consortium apt repo at `packages.irods.org` (multi-arch + actively
  maintained — the alternative `mjstealey/irods-provider-postgres`
  Docker Hub image is iRODS 4.x from 2018 and amd64-only, so it's
  not viable for our linux/amd64 + linux/arm64 target). Bolts the
  agent binary into `/usr/local/bin/` and wires it as the
  entrypoint. iRODS version pinned via the `IRODS_VERSION` build
  arg (default `5.0.1`).
- `docker/entrypoint.sh` : starts `irodsServer` via the upstream
  `/etc/init.d/irods` init script, resolves its pid (from
  `/var/run/irods/irods.pid` or `pgrep`), then execs `weft-ha-irods
  agent` in the foreground. A polling watcher signals the
  entrypoint when iRODS exits so the container terminates as a
  whole — the L4 pool drains the replica and `weft-agent`
  reschedules. SIGTERM/SIGINT/EXIT trap calls the init script's
  `stop` for a graceful DB-connection drain.
- `.github/workflows/release.yml` : `workflow_dispatch` +
  `push: tags ['v*']` only (per the openweft no-autopublish
  policy). Builds + pushes a multi-arch (`linux/amd64`,
  `linux/arm64`) image to `ghcr.io/openweft/irods-ha:{tag,latest}`.

### Notes

The v0.2 milestone leaves iCAT schema creation + admin-user create
out of scope ; iRODS' own `setup_irods.py` runs idempotently on
first boot so the only remaining advisory-locked step (zone-key
minting) is already implemented. Live integration testing against
a 3-DC etcd quorum lands in v0.2 final.

## [v0.1.0] — 2026-06-05

### Added

- Initial scaffold for the `weft-ha-irods` agent — the per-provider
  Go operator behind the `irods-ha` catalogue plugin.
- `cmd/weft-ha-irods` cobra CLI : `version` + `agent` subcommands ;
  agent flags align with the plugin's `env_from` mapping (`IRODS_*`).
- `internal/config` : the typed Config struct + Validate().
- `internal/dcs` : etcd-backed key store + bootstrap advisory lock
  (MemStore scaffold today ; etcd-backed Store in the live milestone).
- `internal/bootstrap` : iCAT schema check + key minting + seeding
  into etcd ; idempotent across providers (the second + third VM
  see the keys already present and skip the mint).
- `internal/api` : role API (`/ready`, `/zone`, `/health`) on the
  port the L4 Caddy in `weft-agent` active-probes.
- `internal/irods` : thin wrapper around `irods-grid` + `iadmin lz`
  so the reconcile loop can ask "is the local server up + serving
  the right zone ?".
- `internal/reconcile` : tick loop running every 5s by default,
  exits on SIGINT / SIGTERM.
- Cross-builds : linux/arm64 + linux/amd64 (the actual deployment
  targets — the micro-VM never runs darwin).
