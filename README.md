<p align="center"><img src="https://raw.githubusercontent.com/openweft/brand/main/social/openweft.png" alt="openweft" width="720"></p>

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

## Secrets

A flag value is visible in `ps` and in `/proc/<pid>/cmdline`, so every
secret is taken **by reference**. Each of `--admin-password`,
`--icat-db-password`, `--negotiation-key`, `--control-plane-key` and
`--zone-key` has a `--<name>-from <ref>` twin, resolved with
[go-authn/secretsource](https://github.com/go-authn/secretsource)
(`systemd-creds:<name>`, `keychain:`, `keepassxc:`, `file:` — mode
checked, at most 4096 bytes). The plain flag still works for one minor
release, prints a deprecation warning on stderr, and is refused together
with its twin. It will be removed in the next minor.

The agent's only child processes are `irods-grid status --all` and
`iadmin`; they receive no secret, neither by argv nor by environment
the agent sets. Keep it that way: pass any future secret to a child by a
file in a private `0700` directory or by stdin.

### Zone keys in etcd are sealed

The negotiation, control-plane and zone keys are shared between the providers
through etcd, but never stored there in plaintext. Every provider takes the
same **cluster key** (at least 32 bytes) by reference with
`--etcd-seal-key-from` (required), e.g. `systemd-creds:etcd-seal`, and seals
each value with AES-256-GCM (key derived by HKDF-SHA256, the etcd path as
additional data) before writing it. etcd holds
`weft-seal:v1:<key id>:<ciphertext>`; the cluster key itself is never in
etcd. Provision it on each node with go-ansible's `systemd_creds_encrypt`
module (or `systemd-creds encrypt`).

- **Upgrading a running zone**: start the agents with `--etcd-seal-key-from`.
  The first read of a plaintext value seals it (compare-and-swap, idempotent,
  safe with several providers) and compacts the etcd history so the old
  plaintext revision is dropped. Upgrade every provider in the same window:
  an agent from before this release cannot read sealed values.
- **Rotation**: pass the new key as `--etcd-seal-key-from` and the old one as
  `--etcd-seal-old-key-from` (repeatable); values are re-sealed under the new
  key on first read. Drop the old flag once every node has restarted.
- **Not covered**: an etcd member's WAL and snapshots written before the
  upgrade still hold the plaintext until the member is defragmented and the
  snapshots are rotated; the agent's etcd connection has no TLS or
  authentication of its own; a node (or anyone) that holds the cluster key
  reads the keys.

### systemd credentials

```ini
[Service]
LoadCredentialEncrypted=admin-password:/etc/credstore.encrypted/admin-password
LoadCredentialEncrypted=icat-db-password:/etc/credstore.encrypted/icat-db-password
ExecStart=/usr/local/bin/weft-ha-irods agent \
  --admin-password-from systemd-creds:admin-password \
  --icat-db-password-from systemd-creds:icat-db-password
```

Seal with `systemd-creds encrypt --name=admin-password - admin-password.cred`.
**TPM caveat**: do not seal with a bare `--tpm2-pcrs=7`: a Secure Boot
db/dbx or firmware update changes PCR 7 and the credential no longer
opens. Seal to `--with-key=host+tpm2` (the host key is the recovery
path), or to a signed PCR policy (`--tpm2-public-key=`), or manage the
policy with `systemd-pcrlock`; otherwise keep a plaintext copy offline
and re-seal after each firmware change.

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
