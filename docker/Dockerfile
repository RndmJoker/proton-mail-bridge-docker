# Proton Mail Bridge, built from the official source and run without a GUI.
#
# Three stages. The first two need a Go toolchain, and the first one a C
# compiler and a handful of development headers as well; none of that has any
# business being in the image that ends up on a server, so the last stage
# starts from a bare Debian and copies in the finished binaries.
#
# Both stages use the same Debian release on purpose. The bridge is linked
# against libsecret, libfido2, libcbor and OpenSSL at build time, and those
# shared objects have to be the same ABI at run time.
#
# Base images are pinned to a tag, not to a digest. A digest would freeze the
# security updates Debian ships into these tags, and this image holds the key
# to a whole mailbox. Reproducibility is bought back where it actually matters:
# the bridge source is pinned to a commit hash in docker/bridge-version.
#
# Three builders have to understand this file: BuildKit, which is what CI uses
# and asserts, and podman with buildah, which is what it is built with locally.
# No environment variable selects between the last two, so the file stays on
# what all three understand: no `# syntax=` directive and no BuildKit-only
# instruction such as `RUN --mount=type=cache`. Changing that for the sake of
# build speed would mean giving up the local build, and is a decision to be
# made deliberately rather than by adding a line here.

# ---------------------------------------------------------------------------
# Stage 1: build the bridge core from source
# ---------------------------------------------------------------------------
FROM golang:1.26-bookworm AS build

# Both are supplied by the caller from docker/bridge-version. No defaults on
# purpose: a missing value must fail the build, not silently produce an image
# built from something else.
ARG BRIDGE_COMMIT
ARG BRIDGE_VERSION

# libsecret is needed by the keychain package. libfido2, libcbor and OpenSSL
# come in through the Makefile, which links the FIDO2 stack even for the
# headless build; the code is behind a `linux || darwin` build tag with no way
# to switch it off. pkg-config resolves all of them for cgo.
#
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install --no-install-recommends -y \
        libsecret-1-dev \
        libfido2-dev \
        libcbor-dev \
        libssl-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Fetching the single pinned commit instead of cloning: it is a fraction of the
# transfer, and it fails outright if the hash does not exist upstream.
#
# The repository is left in place rather than exported, because the Makefile
# calls utils/get_revision.sh and stamps the real commit into the binary. That
# is worth keeping: `bridge --version` then reports what it was actually built
# from.
RUN git init -q . \
    && git remote add origin https://github.com/ProtonMail/proton-bridge.git \
    && git fetch -q --depth 1 origin "${BRIDGE_COMMIT}" \
    && git checkout -q FETCH_HEAD

RUN go mod download

# BUILD_ENV=prod and an explicit version are what keep the "+git" suffix and
# the "dev" environment out of what the bridge reports to Proton.
#
# build-nogui also produces the launcher binary, which exists to run automatic
# updates. It is deliberately not copied into the runtime stage: a container
# that overwrites its own binary makes the image worthless as a record of what
# is running.
#
# Kept as its own layer rather than folded into the download above, so that
# editing a build flag does not throw away the module cache.
# hadolint ignore=DL3059
RUN make build-nogui \
        BRIDGE_APP_VERSION="${BRIDGE_VERSION}" \
        BUILD_ENV=prod

# ---------------------------------------------------------------------------
# Stage 2: build this project's own binaries
# ---------------------------------------------------------------------------
FROM golang:1.26-bookworm AS control

# unzip is for the protoc release archive, which is only published as a zip.
# Everything else the generator needs is already in the Go image.
#
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install --no-install-recommends -y unzip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Dependencies before sources, so that editing a Go file does not throw away
# the module download.
COPY go.mod go.sum ./
RUN go mod download

COPY proto/ proto/
COPY scripts/generate-proto.sh scripts/
COPY internal/ internal/
COPY cmd/ cmd/

# The gRPC client is generated here rather than committed. See proto/README.md.
RUN bash scripts/generate-proto.sh

# CGO_ENABLED=0 makes these static, so they carry no library dependency into
# the runtime stage. Unlike the bridge itself they need nothing from libc that
# would justify the link.
#
# -trimpath keeps the build machine's directory layout out of the binaries.
# hadolint ignore=DL3059
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bridge-control ./cmd/bridge-control \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/proton-info ./cmd/proton-info \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/proton-login ./cmd/proton-login

# ---------------------------------------------------------------------------
# Stage 3: runtime
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

# pass and gnupg are the keychain: a container has no secret service, and pass
# is the only other backend the bridge supports. socat forwards the mail ports,
# which the bridge only ever binds on the loopback address. tini reaps orphans
# and passes signals through to a process group.
#
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install --no-install-recommends -y \
        ca-certificates \
        gnupg \
        pass \
        socat \
        tini \
        libsecret-1-0 \
        libfido2-1 \
        libcbor0.8 \
        libssl3 \
    && rm -rf /var/lib/apt/lists/*

# Fixed UID and GID so a bind-mounted volume can be chowned to a known number
# from outside. Nothing here ever runs as root.
RUN groupadd --gid 1000 bridge \
    && useradd --uid 1000 --gid 1000 --create-home --home-dir /home/bridge bridge

# Everything the bridge writes is redirected into a single directory through
# the XDG variables it honours, so one volume covers the vault, the keychain
# and the GPG key that unlocks it.
#
# These have to be set on the image rather than in the entrypoint: `docker
# exec` starts a process with the image's environment and nothing else, and
# proton-info finds the running bridge through exactly these paths.
#
# The tuneable settings are deliberately absent. Their defaults live in
# internal/config, where they are tested, and are documented in .env.example.
# Repeating them here would be a second place to change them, and the two
# would eventually disagree without anything noticing.
ENV BRIDGE_HOME=/data \
    XDG_CONFIG_HOME=/data/config \
    XDG_DATA_HOME=/data/data \
    XDG_CACHE_HOME=/data/cache \
    GNUPGHOME=/data/gnupg \
    PASSWORD_STORE_DIR=/data/password-store

COPY --from=build /src/bridge /usr/local/bin/bridge
COPY --from=control /out/bridge-control /usr/local/bin/bridge-control
COPY --from=control /out/proton-info /usr/local/bin/proton-info
COPY --from=control /out/proton-login /usr/local/bin/proton-login
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod 0755 \
        /usr/local/bin/bridge \
        /usr/local/bin/bridge-control \
        /usr/local/bin/proton-info \
        /usr/local/bin/proton-login \
        /usr/local/bin/entrypoint.sh \
    && install -d -o bridge -g bridge -m 0700 /data

# ---------------------------------------------------------------------------
# What the image says about itself
# ---------------------------------------------------------------------------
#
# As late as it can go, which is not quite the end. An ARG invalidates every
# layer after it as soon as its value changes, and IMAGE_REVISION changes with
# every commit; placed further up, editing a readme would throw away the apt
# install. It cannot go after `USER bridge`, because writing the file below
# needs root, so what gets rebuilt on every commit is this plus five metadata
# instructions that cost nothing.
#
# org.opencontainers.image.source is the one that does real work: ghcr.io has
# no description field of its own and shows the readme of whichever repository
# a package is linked to. Without this label the package page stays empty and
# offers no way back here. Docker Hub keeps its own text, see
# docker/dockerhub-description.md.
#
# The two BRIDGE_* values are declared again because build arguments do not
# cross a FROM. They are repeated here rather than left to the readme so that
# `docker inspect` alone answers which upstream commit is inside. For an image
# whose whole point is a pinned commit, that answer should not require cloning
# anything.
#
# Supplied by the caller, like the ones in the first stage and for the same
# reason: no defaults, because an image that cannot say what it is built from
# is worse than a build that stops. scripts/ci/smoke-test.sh works them out, so
# neither a local build nor CI passes them by hand.
ARG BRIDGE_COMMIT
ARG BRIDGE_VERSION
ARG IMAGE_VERSION
ARG IMAGE_REVISION

# This file is what keeps the labels below honest, and it is not optional.
#
# buildah builds the cache key of a LABEL instruction from the unexpanded text.
# For the cache, `${IMAGE_REVISION}` stays literally that, so a rebuild with a
# different value reuses the old layer and the image ships the previous
# commit's labels without a word. Measured on 2026-07-31: two builds, different
# build arguments, identical labels.
#
# A RUN is keyed on the expanded command, so it breaks the cache correctly and
# everything after it is rebuilt. Verified in both directions on the same day:
# without this line the second build kept the first one's label; with it, it
# did not.
#
# BuildKit may well handle the LABEL correctly on its own. This file has to
# work under three builders, and being right under only two of them is the
# failure this comment exists to prevent.
#
# The file earns its place anyway: `cat /etc/image-provenance` answers what is
# inside without docker inspect, which is the only way to ask from a shell in
# the running container.
# The test is not decoration. An ARG without a default does not stop a build
# when it is missing, it expands to nothing, so the image would ship empty
# labels and look fine doing it. The two in the first stage fail loudly by
# accident, because `git fetch ""` cannot succeed; these two would not.
RUN test -n "${IMAGE_VERSION}"  || { echo "IMAGE_VERSION is empty"  >&2; exit 1; } \
    && test -n "${IMAGE_REVISION}" || { echo "IMAGE_REVISION is empty" >&2; exit 1; } \
    && printf 'image-version=%s\nimage-revision=%s\nbridge-version=%s\nbridge-commit=%s\n' \
        "${IMAGE_VERSION}" "${IMAGE_REVISION}" "${BRIDGE_VERSION}" "${BRIDGE_COMMIT}" \
        > /etc/image-provenance

LABEL org.opencontainers.image.title="proton-mail-bridge-docker" \
      org.opencontainers.image.description="Proton Mail Bridge for servers: no graphical interface, configured through environment variables, one-time login. Unofficial." \
      org.opencontainers.image.source="https://github.com/RndmJoker/proton-mail-bridge-docker" \
      org.opencontainers.image.url="https://github.com/RndmJoker/proton-mail-bridge-docker" \
      org.opencontainers.image.documentation="https://github.com/RndmJoker/proton-mail-bridge-docker#readme" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      com.rndmjoker.bridge.version="${BRIDGE_VERSION}" \
      com.rndmjoker.bridge.commit="${BRIDGE_COMMIT}"

USER bridge
WORKDIR /home/bridge

VOLUME ["/data"]

# IMAP and SMTP, and the sign-in page. The mail ports are what socat exposes,
# not what the bridge binds. The sign-in page is only reachable from outside
# when BRIDGE_SETUP_EXPOSE is set; it is listed because publishing the port is
# pointless otherwise, not because it is open by default.
EXPOSE 1143 1025 8443

# tini in process-group mode: the entrypoint runs the bridge and socat side by
# side, and a plain shell as PID 1 would neither forward SIGTERM to them nor
# clean up after them.
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/entrypoint.sh"]
