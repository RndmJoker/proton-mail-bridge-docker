#!/usr/bin/env bash
#
# Smoke test for the container image.
#
# It covers what only shows up once everything runs together and cannot be
# reproduced in a unit test: that the image builds, that the bridge inside it
# is the build we asked for, and that it survives a start with no account
# signed in.
#
# It never signs in. No test in this repository uses a real Proton account.
#
# Usage:
#   bash scripts/ci/smoke-test.sh          # build, then test
#   SKIP_BUILD=1 bash scripts/ci/smoke-test.sh
#
# Environment:
#   IMAGE       image tag to build and test  (default proton-mail-bridge:smoke)
#   ENGINE      docker or podman             (default: whichever is installed)
#   SKIP_BUILD  set to any value to test an image that already exists

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT
readonly IMAGE="${IMAGE:-proton-mail-bridge:smoke}"
readonly CONTAINER="proton-mail-bridge-smoke-$$"

# The bridge needs a moment to generate a GPG key, initialise pass and open the
# vault before it can be judged to have started cleanly.
readonly STARTUP_SECONDS="${STARTUP_SECONDS:-25}"

failures=0

log()  { printf '\n=== %s\n' "$*"; }
ok()    { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }

cleanup() {
    "$ENGINE" rm -f "$CONTAINER" >/dev/null 2>&1 || true

    # The volume was written by uid 1000 inside the container. Under a rootless
    # engine that maps to a subordinate uid on the host, and mode 0700 then
    # stops the host user from deleting any of it. So a throwaway container
    # empties the directory first.
    if [ -n "${volume_dir:-}" ]; then
        "$ENGINE" run --rm --user 0 -v "$volume_dir:/data:Z" \
            --entrypoint sh "$IMAGE" -c 'rm -rf /data/* /data/.[!.]*' \
            >/dev/null 2>&1 || true
        rm -rf "$volume_dir" 2>/dev/null || true
    fi
}

# --------------------------------------------------------------------------
# Setup
# --------------------------------------------------------------------------

if [ -n "${ENGINE:-}" ]; then
    :
elif command -v docker >/dev/null 2>&1; then
    ENGINE=docker
elif command -v podman >/dev/null 2>&1; then
    ENGINE=podman
else
    echo "Neither docker nor podman is installed." >&2
    exit 1
fi
readonly ENGINE

# Not a .sh file and deliberately so: it is shared with the Dockerfile build
# arguments and with CI, which read it as plain data.
# shellcheck disable=SC1091
source "$REPO_ROOT/docker/bridge-version"

volume_dir="$(mktemp -d)"
trap cleanup EXIT

# The image runs as uid 1000 and cannot chown a bind mount itself.
chmod 0777 "$volume_dir"

# --------------------------------------------------------------------------
# Build
# --------------------------------------------------------------------------

if [ -z "${SKIP_BUILD:-}" ]; then
    log "Building $IMAGE from upstream $BRIDGE_COMMIT"
    "$ENGINE" build \
        --build-arg "BRIDGE_COMMIT=$BRIDGE_COMMIT" \
        --build-arg "BRIDGE_VERSION=$BRIDGE_VERSION" \
        -f "$REPO_ROOT/docker/Dockerfile" \
        -t "$IMAGE" \
        "$REPO_ROOT"
fi

# --------------------------------------------------------------------------
# The binary is the build we asked for
# --------------------------------------------------------------------------

log "Version reported by the bridge"

version_output="$("$ENGINE" run --rm --entrypoint bridge "$IMAGE" --version 2>&1)"
printf '%s\n' "$version_output" | sed 's/^/  | /'

if printf '%s' "$version_output" | grep -qF "$BRIDGE_VERSION"; then
    ok "reports version $BRIDGE_VERSION"
else
    fail "does not report version $BRIDGE_VERSION"
fi

# The Makefile defaults would produce "3.25.0+git" and a dev build environment.
# If either default slipped through, the bridge would identify itself to Proton
# as a development build.
if printf '%s' "$version_output" | grep -qF '+git'; then
    fail "version carries the +git suffix, so BRIDGE_APP_VERSION was not applied"
else
    ok "no +git suffix"
fi

# --------------------------------------------------------------------------
# It starts with no account signed in
# --------------------------------------------------------------------------

log "Starting the container with an empty volume"

# :Z relabels the directory for SELinux. Without it the container is denied
# access on any enforcing system, Fedora and RHEL among them. Engines on hosts
# without SELinux accept the option and ignore it.
"$ENGINE" run -d \
    --name "$CONTAINER" \
    -v "$volume_dir:/data:Z" \
    -p "127.0.0.1::1143" \
    "$IMAGE" >/dev/null

sleep "$STARTUP_SECONDS"

container_logs="$("$ENGINE" logs "$CONTAINER" 2>&1)"
printf '%s\n' "$container_logs" | sed 's/^/  | /'

if [ "$("$ENGINE" inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" = "true" ]; then
    ok "still running after ${STARTUP_SECONDS}s"
else
    fail "exited within ${STARTUP_SECONDS}s"
fi

# --------------------------------------------------------------------------
# The keychain was set up
# --------------------------------------------------------------------------

# Inspected from inside the container rather than through the mount point. The
# volume is mode 0700 and owned by uid 1000, and under a rootless engine that
# uid maps to a subordinate id the host user cannot read. Checking from the
# host would fail here and pass under a rootful engine, for reasons that have
# nothing to do with what is being tested.
in_container() {
    "$ENGINE" exec "$CONTAINER" "$@" >/dev/null 2>&1
}

log "Keychain in the volume"

if in_container test -f /data/password-store/.gpg-id; then
    ok "pass store is initialised"
else
    fail "pass store was not initialised"
fi

if in_container sh -c 'gpg --list-secret-keys --with-colons | grep -q "^sec:"'; then
    ok "a GPG key is present and usable"
else
    fail "no usable GPG key"
fi

# The point of all of it. On a first start the bridge generates a vault key and
# hands it to the keychain; if pass were not usable it would fall back to
# nothing and lose the vault, and with it any account, on every restart.
if in_container sh -c 'pass ls | grep -q bridge-vault-key'; then
    ok "the bridge stored its vault key in pass"
else
    fail "no vault key in pass, so the keychain is not working"
fi

# --------------------------------------------------------------------------
# The promises made in the README hold
# --------------------------------------------------------------------------

log "Privileges and permissions"

# "The bridge does not run as root." A container that quietly ends up running
# as root would still pass every other check here.
process_uid="$("$ENGINE" exec "$CONTAINER" id -u 2>/dev/null || echo unknown)"
if [ "$process_uid" = "1000" ]; then
    ok "runs as uid 1000, not root"
else
    fail "runs as uid $process_uid, expected 1000"
fi

# "The volume and everything in it belongs to that user and is not readable by
# others." The GPG key in there has no passphrase, so the mode is the only
# thing standing between another user on the host and the mailbox.
wrong_mode=""
checked=0
for dir in gnupg password-store config data cache; do
    # A missing directory is a failure in its own right. Skipping it silently
    # would turn this check green precisely when the entrypoint never ran.
    if ! in_container test -d "/data/$dir"; then
        wrong_mode="$wrong_mode $dir(missing)"
        continue
    fi
    checked=$((checked + 1))
    mode="$("$ENGINE" exec "$CONTAINER" stat -c '%a' "/data/$dir" 2>/dev/null || echo '?')"
    [ "$mode" = "700" ] || wrong_mode="$wrong_mode $dir($mode)"
done

if [ -z "$wrong_mode" ] && [ "$checked" -eq 5 ]; then
    ok "all five volume directories exist and are mode 0700"
else
    fail "volume directories are not private:$wrong_mode"
fi

# --------------------------------------------------------------------------
# The mail ports are reachable from outside the container
# --------------------------------------------------------------------------

# This is the check the whole socat construction exists for. The bridge binds
# IMAP on the loopback address only, so without the forward nothing outside the
# container can reach it, and every other check here would still pass.

log "IMAP through the forward"

host_port="$("$ENGINE" port "$CONTAINER" 1143/tcp 2>/dev/null | head -n1 | sed 's/.*://')"

if [ -z "$host_port" ]; then
    fail "no host port is mapped to container port 1143"
else
    greeting=""
    if exec 3<>"/dev/tcp/127.0.0.1/$host_port" 2>/dev/null; then
        IFS= read -r -t 10 greeting <&3 || true
        exec 3<&-
    fi

    # Strip the CR that terminates every IMAP line.
    greeting="${greeting%$'\r'}"

    case "$greeting" in
        '* OK'*) ok "IMAP answered through the forward: $greeting" ;;
        '')      fail "connected to 127.0.0.1:$host_port but got no greeting" ;;
        *)       fail "unexpected greeting: $greeting" ;;
    esac
fi

# --------------------------------------------------------------------------
# The vault key survives a restart
# --------------------------------------------------------------------------

# Everything above starts from an empty volume, which proves the vault key gets
# written to the keychain but not that it can be read back. If it could not,
# the bridge would generate a fresh one on every start and silently drop every
# signed-in account, while all of the checks above stayed green.
#
# Kept last on purpose: it stops and starts the container, so anything running
# after it would be looking at a different process.

log "Vault key after a restart"

"$ENGINE" stop -t 10 "$CONTAINER" >/dev/null 2>&1 || true
"$ENGINE" start "$CONTAINER" >/dev/null 2>&1 || true
sleep "$STARTUP_SECONDS"

if [ "$("$ENGINE" inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" = "true" ]; then
    ok "still running after a restart"
else
    fail "did not come back up after a restart"
fi

# The log line belongs to the bridge, not to us, so a rewording upstream would
# make this check meaningless. Hence the exact count rather than a plain
# "is it absent": one occurrence is the first start and is expected, none at
# all means the line no longer says what we think it says.
restart_logs="$("$ENGINE" logs "$CONTAINER" 2>&1)"
generated="$(printf '%s\n' "$restart_logs" | grep -c 'no vault key found, generating new' || true)"

if [ "$generated" -eq 1 ]; then
    ok "the vault key was read back from pass, not regenerated"
elif [ "$generated" -gt 1 ]; then
    fail "a new vault key was generated on restart ($generated in total), so any signed-in account would be lost"
else
    fail "the first start did not log a key generation at all, so this check no longer measures anything"
fi

# --------------------------------------------------------------------------

printf '\n'
if [ "$failures" -eq 0 ]; then
    echo "Smoke test passed."
else
    echo "Smoke test failed: $failures check(s)."
    exit 1
fi
