# syntax=docker/dockerfile:1.7

# Image layout for irods-ha catalog-provider replicas :
#
#   Stage 1 (builder) cross-builds the weft-ha-irods agent against the
#   target arch, pure-Go, CGO disabled — so the resulting binary can
#   drop into any linux/$TARGETARCH base image.
#
#   Stage 2 starts from debian:12-slim and installs irods-server +
#   irods-database-plugin-postgres from the official iRODS Consortium
#   apt repo at packages.irods.org. This is the production-supported
#   distribution path : multi-arch (linux/amd64 + linux/arm64), kept
#   current by the upstream packagers, BSD-3-Clause licensed.
#
#   We deliberately do NOT use mjstealey/irods-provider-postgres :
#   it's iRODS 4.x (2018), amd64-only, and unmaintained.
#
#   The image runs BOTH processes :
#
#     - irodsServer (the upstream daemon, started in the background via
#       the irods init script) listening on :1247,
#     - weft-ha-irods agent in the foreground holding the role API on
#       :8009 + the reconcile loop probing irods-grid.
#
#   The entrypoint script wires the two together so that if EITHER
#   process dies the container exits non-zero — the supervisor in
#   weft-agent then restarts the replica and the L4 pool drains it
#   in the meantime.
#
# Build :   docker buildx build --platform linux/amd64,linux/arm64 -t … .
# Trigger : workflow_dispatch + on push: tags ['v*'] only (no
#           autopublish on push:main — see openweft policy).

ARG GO_VERSION=1.26
ARG DEBIAN_VERSION=12-slim
ARG IRODS_VERSION=5.0.1

############################################################
# Stage 1 — build the weft-ha-irods agent
############################################################
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

# Reproducible builds : trimpath drops $GOPATH from filenames,
# -s -w strips DWARF + symbol table.
ENV CGO_ENABLED=0

WORKDIR /src

# Cache modules independently of the source for layer reuse.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
        -o /out/weft-ha-irods \
        ./cmd/weft-ha-irods

############################################################
# Stage 2 — debian + iRODS server + agent + entrypoint wrapper
############################################################
FROM debian:${DEBIAN_VERSION}
ARG IRODS_VERSION

LABEL org.opencontainers.image.source="https://github.com/openweft/weft-ha-irods"
LABEL org.opencontainers.image.description="iRODS catalog provider (debian + irods-server + irods-database-plugin-postgres) + weft-ha-irods HA agent (one process per replica micro-VM)"
LABEL org.opencontainers.image.licenses="BSD-3-Clause"

# Install the iRODS Consortium apt repo + irods-server +
# irods-database-plugin-postgres. lsb-release is used to detect the
# debian codename for the apt repo URL ; gnupg is needed by
# apt-key add. We pin the version to keep image rebuilds deterministic
# across the multi-arch build matrix.
RUN set -eux; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gnupg \
        lsb-release \
        sudo \
        procps \
    ; \
    curl -fsSL https://packages.irods.org/irods-signing-key.asc \
        | gpg --dearmor -o /etc/apt/keyrings/irods.gpg; \
    echo "deb [signed-by=/etc/apt/keyrings/irods.gpg] https://packages.irods.org/apt/ $(lsb_release -sc) main" \
        > /etc/apt/sources.list.d/renci-irods.list; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        irods-server=${IRODS_VERSION}-0~$(lsb_release -sc) \
        irods-database-plugin-postgres=${IRODS_VERSION}-0~$(lsb_release -sc) \
    ; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/weft-ha-irods /usr/local/bin/weft-ha-irods
COPY docker/entrypoint.sh /usr/local/bin/irods-ha-entrypoint
RUN chmod +x /usr/local/bin/irods-ha-entrypoint

# Ensure /usr/bin (where irods-grid + iadmin land) is on PATH for the
# agent's os/exec lookups. The base image already does this but we
# pin it for clarity.
ENV PATH="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

# iRODS listens on :1247 (control + data) ; the agent role API is :8009.
EXPOSE 1247 8009

ENTRYPOINT ["/usr/local/bin/irods-ha-entrypoint"]
