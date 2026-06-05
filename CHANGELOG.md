# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial scaffold for the `weft-ha-irods` agent — the per-provider
  Go operator behind the `irods-ha` catalogue plugin.
- `cmd/weft-ha-irods` cobra CLI : `version` + `agent` subcommands ;
  agent flags align with the plugin's `env_from` mapping (`IRODS_*`).
- `internal/config` : the typed Config struct + Validate().
- `internal/dcs` : etcd-backed key store + bootstrap advisory lock.
  Uses the same `concurrency.NewMutex` pattern as
  `weft-ha-postgresql`'s leader election, but with no continuous
  campaigning — the lock is held only during the one-shot bootstrap.
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

### Status

This is the initial scaffold. The agent compiles, runs, and exposes
its health surface. End-to-end integration with a live iRODS
server + Postgres backend is the v0.2 milestone ; until then the
agent treats the bootstrap + key-sync paths as functional and the
irods-grid integration as best-effort (errors are logged but don't
fail the reconcile tick).
