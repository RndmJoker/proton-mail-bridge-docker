# Proton Mail Bridge, built from the official source and run without a GUI.
#
# Two stages. The first one needs a Go toolchain, a C compiler and a handful of
# development headers; none of that has any business being in the image that
# ends up on a server, so the second stage starts from a bare Debian and copies
# in one binary.
#
# Both stages use the same Debian release on purpose. The bridge is linked
# against libsecret, libfido2, libcbor and OpenSSL at build time, and those
# shared objects have to be the same ABI at run time.
#
# Base images are pinned to a tag, not to a digest. A digest would freeze the
# security updates Debian ships into these tags, and this image holds the key
# to a whole mailbox. Reproducibility is bought back where it actually matters:
# the bridge source is pinned to a commit hash in docker/bridge-version.

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
# Stage 2: runtime
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
ENV BRIDGE_HOME=/data \
    XDG_CONFIG_HOME=/data/config \
    XDG_DATA_HOME=/data/data \
    XDG_CACHE_HOME=/data/cache \
    GNUPGHOME=/data/gnupg \
    PASSWORD_STORE_DIR=/data/password-store \
    BRIDGE_IMAP_PORT=1143 \
    BRIDGE_SMTP_PORT=1025 \
    BRIDGE_LOG_LEVEL=info

COPY --from=build /src/bridge /usr/local/bin/bridge
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod 0755 /usr/local/bin/bridge /usr/local/bin/entrypoint.sh \
    && install -d -o bridge -g bridge -m 0700 /data

USER bridge
WORKDIR /home/bridge

VOLUME ["/data"]

# IMAP and SMTP. These are what socat exposes, not what the bridge binds.
EXPOSE 1143 1025

# tini in process-group mode: the entrypoint runs the bridge and socat side by
# side, and a plain shell as PID 1 would neither forward SIGTERM to them nor
# clean up after them.
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/entrypoint.sh"]
