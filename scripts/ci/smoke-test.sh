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
    local name
    for name in "$CONTAINER" "$CONTAINER-alt" "$CONTAINER-plain" "$CONTAINER-configured"; do
        "$ENGINE" rm -f "$name" >/dev/null 2>&1 || true
    done

    # The volumes were written by uid 1000 inside the container. Under a
    # rootless engine that maps to a subordinate uid on the host, and mode 0700
    # then stops the host user from deleting any of it. So a throwaway
    # container empties each directory first.
    local dir
    for dir in "${volume_dir:-}" "${update_volume_dir:-}"; do
        [ -n "$dir" ] || continue

        "$ENGINE" run --rm --user 0 -v "$dir:/data:Z" \
            --entrypoint sh "$IMAGE" -c 'rm -rf /data/* /data/.[!.]*' \
            >/dev/null 2>&1 || true
        rm -rf "$dir" 2>/dev/null || true
    done
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
# A configuration it cannot honour stops it
# --------------------------------------------------------------------------

# The counterpart to every check below: those show that a good configuration is
# applied, this shows that a bad one is refused rather than quietly replaced by
# a default. A container listening somewhere other than it was told to is the
# failure nobody notices.
#
# No volume is mounted. /data exists in the image and is writable, so the
# container gets far enough to reject the value, and everything it wrote is
# thrown away with the container.

log "Refusing a configuration it cannot honour"

for invalid in "BRIDGE_IMAP_PORT=143" "BRIDGE_SMTP_PORT=not-a-number" "BRIDGE_LOG_LEVEL=verbose"; do
    if output="$("$ENGINE" run --rm -e "$invalid" "$IMAGE" 2>&1)"; then
        fail "$invalid was accepted, the container started anyway"
        continue
    fi

    # An error nobody can act on is barely better than none, so the offending
    # variable has to be named in the message.
    name="${invalid%%=*}"

    if printf '%s' "$output" | grep -q "ERROR: $name"; then
        ok "$invalid is refused, and the message names $name"
    else
        fail "$invalid failed the container, but the message does not name $name: $(printf '%s' "$output" | tail -n 1)"
    fi
done

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
# bridge-control reached the bridge over gRPC
# --------------------------------------------------------------------------

# This is what separates a running bridge from a configured one. None of the
# checks above would notice if the gRPC connection never came up: the bridge
# would still start, still open its default ports, and still answer IMAP.

log "gRPC control"

if printf '%s\n' "$container_logs" | grep -q 'bridge-control  Connected to bridge'; then
    ok "bridge-control connected over gRPC"
else
    fail "bridge-control never reported a connection, so nothing was configured"
fi

# Only that the log says so. Whether it is true is measured further down,
# against the setting itself, on a vault that had it on beforehand.
if printf '%s\n' "$container_logs" | grep -q 'automatic updates off'; then
    ok "settings were applied and automatic updates were turned off"
else
    fail "bridge-control never reported applying its settings"
fi

# --------------------------------------------------------------------------
# proton-info answers
# --------------------------------------------------------------------------

log "proton-info"

info_output="$("$ENGINE" exec "$CONTAINER" proton-info 2>&1 || true)"
printf '%s\n' "$info_output" | sed 's/^/  | /'

if printf '%s' "$info_output" | grep -qF "$BRIDGE_VERSION"; then
    ok "reports the bridge version"
else
    fail "does not report the bridge version"
fi

# With an empty volume there is no account, and saying so plainly is the whole
# point: an operator who sees an empty list needs to know it is expected here
# and not a lost account.
if printf '%s' "$info_output" | grep -q 'No account is signed in'; then
    ok "says that no account is signed in"
else
    fail "does not explain the empty account list"
fi

# The fingerprint comes from a TLS handshake against the running IMAP port, so
# this also proves the mail server is up and speaking STARTTLS.
if printf '%s' "$info_output" | grep -qE 'Certificate \(SHA-256\)  [0-9A-F]{2}(:[0-9A-F]{2}){31}'; then
    ok "shows a certificate fingerprint from the live mail port"
else
    fail "no usable certificate fingerprint"
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
# The bridge's own updater is off, and was on before
# --------------------------------------------------------------------------

# Reading "off" on its own proves nothing: it would look the same if the
# setting had never been touched. So the same vault is looked at twice. First
# with the bridge started on its own, bypassing bridge-control, where the
# setting has to be the bridge's own default of on. Then with bridge-control,
# where it has to be off.
#
# Anything less is a check that cannot fail. The value lives in the vault and
# survives restarts, so a single reading says nothing about who wrote it.

log "Automatic updates"

update_volume_dir="$(mktemp -d)"
chmod 0777 "$update_volume_dir"

readonly PLAIN_CONTAINER="$CONTAINER-plain"

# Arguments after the image replace bridge-control. The entrypoint still
# prepares the volume and the keychain, so this is a bridge with everything it
# needs and nobody configuring it.
"$ENGINE" run -d \
    --name "$PLAIN_CONTAINER" \
    -v "$update_volume_dir:/data:Z" \
    "$IMAGE" bridge --grpc >/dev/null

sleep "$STARTUP_SECONDS"

plain_info="$("$ENGINE" exec "$PLAIN_CONTAINER" proton-info 2>&1 || true)"

if printf '%s' "$plain_info" | grep -q 'Bridge self-update     ON'; then
    ok "without bridge-control the bridge default is on, so this check measures something"
else
    fail "an unconfigured bridge does not report automatic updates as on; the check below no longer proves anything: $(printf '%s' "$plain_info" | grep -i 'self-update' || echo 'no such line')"
fi

"$ENGINE" rm -f "$PLAIN_CONTAINER" >/dev/null 2>&1 || true

readonly CONFIGURED_CONTAINER="$CONTAINER-configured"

"$ENGINE" run -d \
    --name "$CONFIGURED_CONTAINER" \
    -v "$update_volume_dir:/data:Z" \
    "$IMAGE" >/dev/null

sleep "$STARTUP_SECONDS"

configured_info="$("$ENGINE" exec "$CONFIGURED_CONTAINER" proton-info 2>&1 || true)"

if printf '%s' "$configured_info" | grep -q 'Bridge self-update     off'; then
    ok "bridge-control turned it off on the same vault"
else
    fail "automatic updates are still on after bridge-control ran: $(printf '%s' "$configured_info" | grep -i 'self-update' || echo 'no such line')"
fi

"$ENGINE" rm -f "$CONFIGURED_CONTAINER" >/dev/null 2>&1 || true

# --------------------------------------------------------------------------
# A configured port actually takes effect
# --------------------------------------------------------------------------

# The strongest check here, and the reason the gRPC work exists at all. The
# bridge opens 1143 by itself; every check above would pass just as well if
# BRIDGE_IMAP_PORT were ignored entirely. Asking for a different port and
# getting an answer there is the only proof that the setting travels all the
# way through: environment, gRPC call, bridge, forward.
#
# It reuses the volume, so it also covers the case that matters in practice:
# changing a port on a container that already has a vault.

log "IMAP on a configured port"

"$ENGINE" stop -t 10 "$CONTAINER" >/dev/null 2>&1 || true

readonly ALT_IMAP_PORT=2143
readonly ALT_CONTAINER="$CONTAINER-alt"

"$ENGINE" run -d \
    --name "$ALT_CONTAINER" \
    -v "$volume_dir:/data:Z" \
    -e "BRIDGE_IMAP_PORT=$ALT_IMAP_PORT" \
    -p "127.0.0.1::$ALT_IMAP_PORT" \
    "$IMAGE" >/dev/null

sleep "$STARTUP_SECONDS"

alt_logs="$("$ENGINE" logs "$ALT_CONTAINER" 2>&1)"
printf '%s\n' "$alt_logs" | tail -n 20 | sed 's/^/  | /'

alt_host_port="$("$ENGINE" port "$ALT_CONTAINER" "$ALT_IMAP_PORT/tcp" 2>/dev/null | head -n1 | sed 's/.*://')"

if [ -z "$alt_host_port" ]; then
    fail "no host port is mapped to container port $ALT_IMAP_PORT"
else
    alt_greeting=""
    if exec 3<>"/dev/tcp/127.0.0.1/$alt_host_port" 2>/dev/null; then
        IFS= read -r -t 10 alt_greeting <&3 || true
        exec 3<&-
    fi

    alt_greeting="${alt_greeting%$'\r'}"

    case "$alt_greeting" in
        '* OK'*) ok "IMAP answered on the configured port $ALT_IMAP_PORT: $alt_greeting" ;;
        '')      fail "connected to the mapping for $ALT_IMAP_PORT but got no greeting" ;;
        *)       fail "unexpected greeting on port $ALT_IMAP_PORT: $alt_greeting" ;;
    esac
fi

# The counter-check to the one above: the default port must be gone. If the
# bridge had ignored the setting and stayed on 1143, the test above could still
# pass through a forward that happens to exist.
if printf '%s\n' "$alt_logs" | grep -q "Forwarding IMAP: .*:$ALT_IMAP_PORT"; then
    ok "the forward followed the bridge to port $ALT_IMAP_PORT"
else
    fail "no forward was set up for port $ALT_IMAP_PORT"
fi

if printf '%s\n' "$alt_logs" | grep -q 'Forwarding IMAP: .*:1143'; then
    fail "IMAP is still being forwarded on 1143, so the configured port was not applied"
else
    ok "nothing is left listening on the default IMAP port"
fi

# --------------------------------------------------------------------------

printf '\n'
if [ "$failures" -eq 0 ]; then
    echo "Smoke test passed."
else
    echo "Smoke test failed: $failures check(s)."
    exit 1
fi
