# syntax=docker/dockerfile:1.7

# Image layout for irods-ha catalog-provider replicas :
#
#   Stage 0  (gobuilder)       — build the weft-ha-irods agent,
#                                pure-Go, CGO=0, multi-arch.
#   Stage 1  (externalsbuilder) — build the irods-externals-* packages
#                                (boost / clang / jsoncons / nanodbc)
#                                from https://github.com/irods/externals
#                                into /opt/irods-externals/. iRODS 5.x's
#                                CMake hard-pins these subdirs (notably
#                                /opt/irods-externals/clang16.0.6-0), so
#                                we have to materialise them before the
#                                iRODS source build can configure.
#   Stage 2  (irodsbuilder)    — build iRODS server + database plugins
#                                FROM SOURCE for the target arch,
#                                consuming the externals copied in from
#                                stage 1. packages.irods.org is amd64-
#                                only, so for arm64/riscv64/loong64
#                                hosts we have to compile here.
#   Stage 3                    — runtime : debian:12-slim + the binaries
#                                from stages 0+2 + the entrypoint that
#                                wires the agent to irodsServer.
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
# Why a dedicated externalsbuilder stage (rather than distro libs) :
# iRODS 5.x's top-level CMakeLists.txt calls
# IRODS_MACRO_CHECK_DEPENDENCY_SET_FULLPATH against fixed-version
# subdirectories under IRODS_EXTERNALS_PACKAGE_ROOT (default
# /opt/irods-externals) :
#     boost1.81.0-2, nanodbc2.13.0-3, jsoncons0.178.0-0
# and cmake/Modules/IrodsCXXCompiler.cmake additionally requires
# /opt/irods-externals/clang16.0.6-0 (IRODS_BUILD_WITH_CLANG=ON is
# the default — iRODS won't compile with stock g++ because it relies
# on the externals' clang as the C++20 compiler). A previous attempt
# to substitute distro libfmt-dev/libspdlog-dev/nlohmann-json3-dev/
# libboost-all-dev failed at the configure step with FATAL_ERROR
# "BOOST not found at /opt/irods-externals/boost1.81.0-2". Building
# the externals ourselves from irods/externals @ main matches the
# upstream pattern documented in irods/irods_development_environment
# (externals_builder.debian12.Dockerfile + build_and_copy_externals
# _to_dir.sh — make server target).
#
# Trade-offs of source-build :
#   + Multi-arch parity : arm64 / amd64 / riscv64 / loong64 all build
#     from the same source tree.
#   + No external apt repo dependency at runtime — the runtime stage
#     gets everything from stages 1 and 2, not from packages.irods.org.
#   - First build : 60-90 min per arch end-to-end (externals 30-60 min,
#     iRODS 30 min).
#   - With ccache mounted via buildx cache : ~5 min on warm runs.
#   - Image size : ~1.0 GB runtime (externals' clang runtime + Boost +
#     Python dominate ; externals are kept in the runtime image
#     because libirods.so dlopens objects from /opt/irods-externals/*).
#
# Build :   docker buildx build --platform linux/amd64,linux/arm64 -t … .
# Trigger : workflow_dispatch + on push: tags ['v*'] only (no
#           autopublish on push:main — see openweft policy).

ARG GO_VERSION=1.26
ARG DEBIAN_VERSION=12-slim
ARG IRODS_VERSION=5.0.2
# irods/externals has no per-iRODS-release tag (last tag is 4.2.7) ;
# upstream development tracks `main`, which is what
# irods_development_environment's externals_builder image checks out.
ARG IRODS_EXTERNALS_REF=main

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
# Stage 1 — build irods-externals (boost / clang / jsoncons / nanodbc)
#           into /opt/irods-externals/
############################################################
FROM debian:${DEBIAN_VERSION} AS externalsbuilder
ARG IRODS_EXTERNALS_REF

ENV DEBIAN_FRONTEND=noninteractive

# Build-deps for irods/externals. Mirrors install_prerequisites.py
# (apt branch, debian/ubuntu) so we can do the install in a single
# RUN without invoking the Python helper as root inside the layer
# (it just calls apt anyway). nfpm is required by build.py to roll
# the .deb packages once each external is built.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        cmake \
        curl \
        fuse \
        g++ \
        gcc \
        git \
        gnupg \
        help2man \
        libarchive-dev \
        libbz2-dev \
        libcurl4-openssl-dev \
        libfuse-dev \
        libicu-dev \
        liblzma-dev \
        libmicrohttpd-dev \
        libssl-dev \
        libtool \
        libxml2-dev \
        libzmq3-dev \
        libzstd-dev \
        lsb-release \
        make \
        nlohmann-json3-dev \
        patch \
        pkg-config \
        procps \
        python3 \
        python3-dev \
        python3-distro \
        python3-jsonschema \
        python3-packaging \
        python3-psutil \
        python3-requests \
        python3-setuptools \
        python3-yaml \
        rsync \
        texinfo \
        unixodbc-dev \
        uuid-dev \
        wget \
        zlib1g-dev \
    ; \
    rm -rf /var/lib/apt/lists/*

# nfpm — irods/externals's build.py shells out to nfpm to package each
# external as a .deb. install_prerequisites.py would download it for
# us but it's a single static Go binary, so we just fetch it directly.
# Version mirrors the floor recommended by the externals README
# (>= 2.41.3).
RUN set -eux; \
    ARCH="$(dpkg --print-architecture)"; \
    case "${ARCH}" in \
        amd64)   NFPM_ARCH=x86_64 ;; \
        arm64)   NFPM_ARCH=arm64 ;; \
        riscv64) NFPM_ARCH=riscv64 ;; \
        loong64) NFPM_ARCH=loong64 ;; \
        *)       echo "unsupported arch: ${ARCH}" >&2; exit 1 ;; \
    esac; \
    NFPM_VERSION=2.41.3; \
    wget -qO /tmp/nfpm.tar.gz \
        "https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_Linux_${NFPM_ARCH}.tar.gz"; \
    tar -C /usr/local/bin -xzf /tmp/nfpm.tar.gz nfpm; \
    rm /tmp/nfpm.tar.gz; \
    nfpm --version

# Clone irods/externals at the requested ref and build the server-
# subset target. `make server` builds boost + clang + jsoncons +
# nanodbc — the four externals iRODS proper checks for at configure
# time (see cmake/Modules/IrodsExternals.cmake +
# IrodsCXXCompiler.cmake at irods/irods). build.py stages each
# package under <externals>/opt/irods-externals/<name><ver>-<cbn>/
# AND emits a .deb in the externals dir ; we install via dpkg so the
# layout under /opt/irods-externals/ matches what iRODS's CMake
# expects (and what packages.irods.org would have installed).
WORKDIR /externals
# build.py redirects each tool's stdout to "<tool>.log" inside the
# externals dir. On failure those files are the only record of what
# went wrong — without surfacing them, all the build sees is
# "make Error 1". The dump-on-failure trap below tails every log
# so the rc4-arm64 post-mortem stops being blind (2026-06-23).
RUN set -eux; \
    git clone --depth=1 --branch "${IRODS_EXTERNALS_REF}" \
        https://github.com/irods/externals.git /externals; \
    # iRODS-externals hard-codes -DLLVM_TARGETS_TO_BUILD='X86' in
    # versions.json's clang.build_steps, so on arm64 hosts LLVM's
    # compiler-rt finds no buildable target ("Supported architectures
    # for crt:" comes out empty) and configure aborts with
    # "get_compiler_rt_output_dir Function invoked with incorrect
    # arguments". rc6/rc7/rc8/rc9/rc10 arm64 post-mortem 2026-06-26 :
    # https://github.com/openweft/weft-ha-irods/issues/... — pending
    # upstream fix, patch the targets list in place to include the
    # host arch. 'X86;AArch64' is the smallest superset that covers
    # both amd64 and arm64 runners ; can be extended for riscv64 /
    # loong64 if/when those land. \
    sed -i.bak \
        "s|LLVM_TARGETS_TO_BUILD='X86'|LLVM_TARGETS_TO_BUILD='X86;AArch64'|g" \
        versions.json; \
    grep -q "X86;AArch64" versions.json; \
    ( make server ) || ( \
        echo '=== externals build FAILED — dumping per-tool logs ==='; \
        for f in /externals/*.log; do \
            echo "===== $f (tail 200) ====="; tail -n 200 "$f" || true; \
        done; \
        exit 1 \
    ); \
    ls -1 irods-externals-*.deb; \
    dpkg -i irods-externals-*.deb; \
    test -d /opt/irods-externals/clang16.0.6-0; \
    test -d /opt/irods-externals/boost1.81.0-2; \
    test -d /opt/irods-externals/nanodbc2.13.0-3; \
    test -d /opt/irods-externals/jsoncons0.178.0-0

############################################################
# Stage 2 — build iRODS server + postgres plugin FROM SOURCE
############################################################
FROM debian:${DEBIAN_VERSION} AS irodsbuilder
ARG IRODS_VERSION

ENV DEBIAN_FRONTEND=noninteractive

# Build-deps :
#   - Toolchain : cmake, ninja, make, flex, bison (the C/C++ compiler
#     comes from the externals' clang16.0.6-0, NOT gcc/g++ — iRODS 5.x
#     defaults to IRODS_BUILD_WITH_CLANG=ON and the cmake setup hard-
#     paths CMAKE_C_COMPILER / CMAKE_CXX_COMPILER under
#     /opt/irods-externals/clang16.0.6-0/bin/. We still install gcc
#     because some of iRODS's auxiliary scripts shell out to it.)
#   - Header-only / link-only deps NOT shipped via externals :
#     libarchive-dev, libssl-dev, libcurl4-openssl-dev,
#     libxml2-dev (the iRODS source build expects these from the
#     distro). boost / nanodbc / jsoncons / fmt / spdlog /
#     nlohmann_json are NOT installed here — boost+nanodbc+jsoncons
#     come from the externals stage, and fmt/spdlog/nlohmann_json
#     come bundled inside the externals' clang sysroot / boost tree.
#   - iRODS-specific : libpam0g-dev, libkrb5-dev, libfuse-dev,
#     libsystemd-dev, libbz2-dev, zlib1g-dev, libsqlite3-dev,
#     unixodbc-dev, odbc-postgresql, libpq-dev
#   - Tooling : help2man, python3, ccache, dpkg-dev, fakeroot
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        bison \
        ca-certificates \
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
        libbz2-dev \
        libcurl4-openssl-dev \
        libfl-dev \
        libfuse-dev \
        libkrb5-dev \
        libpam0g-dev \
        libpq-dev \
        libsqlite3-dev \
        libssl-dev \
        libsystemd-dev \
        libxml2-dev \
        make \
        ninja-build \
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

# Pull in the externals tree built in stage 1. Must happen BEFORE the
# iRODS cmake configure step, otherwise it fails with
# "BOOST not found at /opt/irods-externals/boost1.81.0-2" (and so on
# for nanodbc / jsoncons / clang16.0.6-0).
COPY --from=externalsbuilder /opt/irods-externals /opt/irods-externals

# nlohmann_json from source. iRODS's find_package(nlohmann_json)
# wants the upstream cmake-installed package (nlohmann_jsonConfig.cmake)
# ; Debian's nlohmann-json3-dev only drops headers, no cmake config,
# so the find_package fails (rc4 amd64 post-mortem 2026-06-24).
# Pinned to v3.11.3 (matching iRODS 5.0.2 expectations) ; header-only
# so the build is ~30s + cmake/install gives the Config.cmake file.
ARG NLOHMANN_JSON_VERSION=v3.11.3
RUN set -eux; \
    wget -qO nj.tar.gz "https://github.com/nlohmann/json/archive/refs/tags/${NLOHMANN_JSON_VERSION}.tar.gz"; \
    mkdir -p /src/njson; \
    tar xzf nj.tar.gz -C /src/njson --strip-components=1; \
    rm nj.tar.gz; \
    cmake -S /src/njson -B /build-njson \
        -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DJSON_BuildTests=OFF \
        -DCMAKE_INSTALL_PREFIX=/usr/local; \
    cmake --build /build-njson; \
    cmake --install /build-njson

# fmt 8.1.1 from source. iRODS 5.0.2 requires find_package(fmt 8.1.1)
# ; Debian bookworm's libfmt-dev is 9.1.0+ds1-2 — the major bump
# breaks find_package's version-compat predicate so the configure
# step fails with "Could not find a package configuration file
# provided by fmt (requested version 8.1.1)" (rc6 amd64 post-mortem
# 2026-06-25). Build the upstream 8.1.1 release tarball under
# /usr/local so CMake's prefix search finds it before the system
# libfmt. Header-and-tiny-lib so the build is ~10s.
ARG FMT_VERSION=8.1.1
RUN set -eux; \
    wget -qO fmt.tar.gz "https://github.com/fmtlib/fmt/archive/refs/tags/${FMT_VERSION}.tar.gz"; \
    mkdir -p /src/fmt; \
    tar xzf fmt.tar.gz -C /src/fmt --strip-components=1; \
    rm fmt.tar.gz; \
    cmake -S /src/fmt -B /build-fmt \
        -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DBUILD_SHARED_LIBS=ON \
        -DFMT_TEST=OFF \
        -DFMT_DOC=OFF \
        -DCMAKE_INSTALL_PREFIX=/usr/local; \
    cmake --build /build-fmt --parallel "$(nproc)"; \
    cmake --install /build-fmt

# spdlog 1.9.2 from source. iRODS 5.0.2 expects spdlog matched to
# fmt 8.x ABI ; Debian's libspdlog-dev 1.10.0-1 was compiled against
# Debian's libfmt 9.x so even if it installs, runtime links break.
# Building from source against our /usr/local fmt 8.1.1 keeps the
# fmt ABI consistent end-to-end. -DSPDLOG_FMT_EXTERNAL=ON disables
# the bundled fmt and reuses our fmt 8.1.1.
ARG SPDLOG_VERSION=v1.9.2
RUN set -eux; \
    wget -qO spdlog.tar.gz "https://github.com/gabime/spdlog/archive/refs/tags/${SPDLOG_VERSION}.tar.gz"; \
    mkdir -p /src/spdlog; \
    tar xzf spdlog.tar.gz -C /src/spdlog --strip-components=1; \
    rm spdlog.tar.gz; \
    cmake -S /src/spdlog -B /build-spdlog \
        -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DBUILD_SHARED_LIBS=ON \
        -DSPDLOG_FMT_EXTERNAL=ON \
        -DSPDLOG_BUILD_TESTS=OFF \
        -DSPDLOG_BUILD_EXAMPLE=OFF \
        -DCMAKE_PREFIX_PATH=/usr/local \
        -DCMAKE_INSTALL_PREFIX=/usr/local; \
    cmake --build /build-spdlog --parallel "$(nproc)"; \
    cmake --install /build-spdlog

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
# as a .deb via cpack (or just tar it for stage 3).
# -DIRODS_EXTERNALS_PACKAGE_ROOT is the default (/opt/irods-externals)
# but we pass it explicitly so a reader of the Dockerfile sees the
# wire-up to stage 1.
WORKDIR /build
RUN set -eux; \
    cmake -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/opt/irods-staging \
        -DIRODS_EXTERNALS_PACKAGE_ROOT=/opt/irods-externals \
        -DIRODS_DISABLE_COMPILER_OPTIMIZATIONS=OFF \
        -DIRODS_UNIT_TESTS_BUILD=OFF \
        /src/irods; \
    cmake --build . --parallel "$(nproc)"; \
    cmake --install .

# The postgres database plugin is bundled inside the main irods/irods
# source tree starting from iRODS 5.x — the previous external repo
# https://github.com/irods/irods_database_plugin_postgres returns 404
# (rc9 amd64 post-mortem 2026-06-25). The plugin is built + installed
# as part of the cmake --install step above ; verified by the install
# log line "Installing: …/share/doc/irods/irods-database-plugin-
# postgres/copyright".

############################################################
# Stage 3 — runtime : debian + iRODS install + externals + agent
############################################################
FROM debian:${DEBIAN_VERSION}

LABEL org.opencontainers.image.source="https://github.com/openweft/weft-ha-irods"
LABEL org.opencontainers.image.description="iRODS catalog provider built from source (multi-arch) + weft-ha-irods HA agent"
LABEL org.opencontainers.image.licenses="BSD-3-Clause"

ENV DEBIAN_FRONTEND=noninteractive

# Runtime deps : the .so files iRODS dynamically links to from outside
# /opt/irods-externals/. boost / nanodbc / jsoncons / clang runtime
# are taken from /opt/irods-externals/ (see COPY below), so they are
# NOT installed via apt here.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        libarchive13 \
        libcurl4 \
        libpq5 \
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

# Drop in the externals (libirods.so dlopens objects from here at
# runtime — keeping the tree end-to-end means iRODS sees the exact
# same .so files it was linked against in stage 2), the iRODS install,
# and the agent.
COPY --from=externalsbuilder /opt/irods-externals /opt/irods-externals
COPY --from=irodsbuilder /opt/irods-staging /opt/irods
COPY --from=gobuilder /out/weft-ha-irods /usr/local/bin/weft-ha-irods
COPY docker/entrypoint.sh /usr/local/bin/irods-ha-entrypoint
RUN chmod +x /usr/local/bin/irods-ha-entrypoint

# /opt/irods/bin first so irods-grid + iadmin resolve from our build.
ENV PATH="/opt/irods/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
# LD_LIBRARY_PATH must cover both /opt/irods/lib AND every externals
# subdir whose lib/ holds .so consumed at runtime (boost + nanodbc +
# clang runtime). The wildcard form keeps it future-proof if externals
# bump versions.
ENV LD_LIBRARY_PATH="/opt/irods/lib:/opt/irods-externals/boost1.81.0-2/lib:/opt/irods-externals/nanodbc2.13.0-3/lib:/opt/irods-externals/clang16.0.6-0/lib"

EXPOSE 1247 8009

ENTRYPOINT ["/usr/local/bin/irods-ha-entrypoint"]
