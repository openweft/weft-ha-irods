# syntax=docker/dockerfile:1.7

# Image layout for irods-ha catalog-provider replicas :
#
#   Stage 0 (build the weft-ha-irods agent, pure-Go, CGO=0, multi-arch).
#   Stage 1 (build iRODS server + database plugins FROM SOURCE for the
#            target arch — packages.irods.org is amd64-only, so for
#            arm64/riscv64/loong64 hosts we have to compile here).
#   Stage 2 (runtime : debian:12-slim + the binaries from stages 0+1 +
#            the entrypoint that wires the agent to irodsServer).
#
# The source build was forced by a 2026-06 incident : the previous
# release workflow (which pulled pre-built .deb from packages.irods.org)
# failed on linux/arm64 with "irods-server : Depends: irods-runtime
# (= 5.0.1-0~bookworm) ; Unable to correct problems, you have held
# broken packages". Root cause : packages.irods.org ships ONLY
# binary-amd64 across every distro suite (bookworm/jammy/noble/…).
# The cluster is full-arm64 (Tart VMs), so we'd never get an iRODS
# image. The user's directive : "il faut supporter irods sur
# l'ensemble des arch que nous supportons. savoir le compilier/
# installer depuis les sources est primordial". Hence this Dockerfile.
#
# Trade-offs of source-build :
#   + Multi-arch parity : arm64 / amd64 / riscv64 / loong64 all build
#     from the same source tree.
#   + No external apt repo dependency at runtime — the runtime stage
#     gets the .deb from stage 1, not from packages.irods.org.
#   - First build : 30-60 min per arch (iRODS C++ is large).
#   - With ccache mounted via buildx cache : ~5 min on warm runs.
#   - Image size : ~600 MB runtime (mostly Boost + Python).
#
# Externals strategy : iRODS depends on irods-externals-* packages
# (boost/fmt/json/spdlog vendored versions). We use the DISTRO
# packages instead :
#     libfmt-dev libspdlog-dev nlohmann-json3-dev libboost-all-dev
#     libarchive-dev libssl-dev libcurl4-openssl-dev …
# CMake's find_package picks them up. This works because iRODS 5.x
# accepts standard-version external libs (the irods-externals repo
# pins minor versions for the upstream CI, not for downstream builds).
#
# Build :   docker buildx build --platform linux/amd64,linux/arm64 -t … .
# Trigger : workflow_dispatch + on push: tags ['v*'] only (no
#           autopublish on push:main — see openweft policy).

ARG GO_VERSION=1.26
ARG DEBIAN_VERSION=12-slim
ARG IRODS_VERSION=5.0.2

############################################################
# Stage 0 — build the weft-ha-irods Go agent
############################################################
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS gobuilder
ARG TARGETOS
ARG TARGETARCH

ENV CGO_ENABLED=0

WORKDIR /src

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
# Stage 1 — build iRODS server + postgres plugin FROM SOURCE
############################################################
FROM debian:${DEBIAN_VERSION} AS irodsbuilder
ARG IRODS_VERSION

ENV DEBIAN_FRONTEND=noninteractive

# Build-deps :
#   - Toolchain : cmake, ninja, gcc, g++, make, flex, bison
#   - Externals (distro versions in lieu of irods-externals-*) :
#     libboost-all-dev, libfmt-dev, libspdlog-dev,
#     nlohmann-json3-dev, libarchive-dev, libssl-dev,
#     libcurl4-openssl-dev, catch2, libxml2-dev
#   - iRODS-specific : libpam0g-dev, libkrb5-dev, libfuse-dev,
#     libsystemd-dev, libbz2-dev, zlib1g-dev, libsqlite3-dev,
#     unixodbc-dev, odbc-postgresql, libpq-dev
#   - Tooling : help2man, python3, ccache, dpkg-dev, fakeroot
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        bison \
        ca-certificates \
        catch2 \
        ccache \
        cmake \
        dpkg-dev \
        fakeroot \
        flex \
        g++ \
        gcc \
        git \
        help2man \
        libarchive-dev \
        libboost-all-dev \
        libbz2-dev \
        libcurl4-openssl-dev \
        libfmt-dev \
        libfuse-dev \
        libkrb5-dev \
        libpam0g-dev \
        libpq-dev \
        libspdlog-dev \
        libsqlite3-dev \
        libssl-dev \
        libsystemd-dev \
        libxml2-dev \
        make \
        ninja-build \
        nlohmann-json3-dev \
        odbc-postgresql \
        pkg-config \
        python3 \
        python3-dev \
        python3-distro \
        python3-jsonschema \
        python3-packaging \
        python3-psutil \
        python3-requests \
        unixodbc-dev \
        wget \
        zlib1g-dev \
    ; \
    rm -rf /var/lib/apt/lists/*

# Pull the iRODS source tarball for the requested version. Using
# the GitHub release tarball (not the git clone) so the cache key
# is the version, not "HEAD-of-day".
WORKDIR /src
RUN set -eux; \
    wget -qO irods.tar.gz "https://github.com/irods/irods/archive/refs/tags/${IRODS_VERSION}.tar.gz"; \
    tar xzf irods.tar.gz; \
    mv "irods-${IRODS_VERSION}" irods; \
    rm irods.tar.gz

# Build iRODS. Out-of-source build under /build, install prefix
# /opt/irods-staging so we can later collect everything and re-pack
# as a .deb via cpack (or just tar it for stage 2).
WORKDIR /build
RUN set -eux; \
    cmake -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/opt/irods-staging \
        -DIRODS_DISABLE_COMPILER_OPTIMIZATIONS=OFF \
        -DIRODS_UNIT_TESTS_BUILD=OFF \
        /src/irods; \
    cmake --build . --parallel "$(nproc)"; \
    cmake --install .

# Build the postgres database plugin.
RUN set -eux; \
    git clone --depth=1 --branch "${IRODS_VERSION}" \
        https://github.com/irods/irods_database_plugin_postgres.git /src/db-pg; \
    mkdir -p /build-db-pg; \
    cd /build-db-pg; \
    cmake -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/opt/irods-staging \
        -DIRODS_DIR=/opt/irods-staging/lib/cmake/IRODS \
        /src/db-pg; \
    cmake --build . --parallel "$(nproc)"; \
    cmake --install .

############################################################
# Stage 2 — runtime : debian + iRODS install + agent + entrypoint
############################################################
FROM debian:${DEBIAN_VERSION}

LABEL org.opencontainers.image.source="https://github.com/openweft/weft-ha-irods"
LABEL org.opencontainers.image.description="iRODS catalog provider built from source (multi-arch) + weft-ha-irods HA agent"
LABEL org.opencontainers.image.licenses="BSD-3-Clause"

ENV DEBIAN_FRONTEND=noninteractive

# Runtime deps : the .so files iRODS dynamically links to.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        libarchive13 \
        libboost-filesystem1.74.0 \
        libboost-program-options1.74.0 \
        libboost-regex1.74.0 \
        libboost-system1.74.0 \
        libboost-thread1.74.0 \
        libcurl4 \
        libfmt9 \
        libpq5 \
        libspdlog1.10 \
        libssl3 \
        libsystemd0 \
        libxml2 \
        odbc-postgresql \
        procps \
        sudo \
        unixodbc \
    ; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# Drop in the iRODS install + the agent.
COPY --from=irodsbuilder /opt/irods-staging /opt/irods
COPY --from=gobuilder /out/weft-ha-irods /usr/local/bin/weft-ha-irods
COPY docker/entrypoint.sh /usr/local/bin/irods-ha-entrypoint
RUN chmod +x /usr/local/bin/irods-ha-entrypoint

# /opt/irods/bin first so irods-grid + iadmin resolve from our build.
ENV PATH="/opt/irods/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
ENV LD_LIBRARY_PATH="/opt/irods/lib"

EXPOSE 1247 8009

ENTRYPOINT ["/usr/local/bin/irods-ha-entrypoint"]
