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

# How long wait_ready gives a container. Generous on purpose: a container whose
# volume already holds a vault waits for the bridge to read it before the
# sign-in page can exist, and that wait is bounded by accountsReportedTimeout in
# bridge-control, currently 30 seconds. This has to be comfortably more than
# that plus the bridge's own startup, and costs nothing when things are quick.
readonly READY_TIMEOUT="${READY_TIMEOUT:-90}"

failures=0

log()  { printf '\n=== %s\n' "$*"; }
ok()    { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }

# wait_ready blocks until bridge-control has finished starting in a container,
# or until READY_TIMEOUT passes.
#
# Replaces a fixed sleep, which was wrong in both directions: too long for a
# container that was ready in two seconds, and too short for one that has to
# wait for the bridge to read an existing vault. A fixed number that has to be
# raised whenever startup gets slower is a number that will be too small again.
#
# The marker is the sign-in page announcing itself, which is the last thing to
# appear. `Forwarding SMTP:` is written before it and was tried first: it is
# printed while runSignIn is still waiting for the bridge to read its vault, so
# waiting for it returns before the page exists.
#
# Every container here starts without an account, which is the premise of this
# whole file, so the line always comes. One that had an account signed in would
# never print it and would sit here until the timeout.
#
# The log goes into a variable rather than through a pipe. `grep -q` exits at
# the first match, the engine writing the log then takes SIGPIPE, and with
# `set -o pipefail` the whole pipeline reports failure. That only shows up once
# a log is long enough for the engine still to be writing, which is why it hit
# exactly one container and looked like a timeout.
# The second argument is how many times the line has to have appeared. It
# matters after a restart: the log keeps everything, so the first run's line is
# still there and a plain search returns at once, before the new run is up.
wait_ready() {
    local container="$1" want="${2:-1}" deadline logs
    deadline=$(( $(date +%s) + READY_TIMEOUT ))

    while [ "$(date +%s)" -lt "$deadline" ]; do
        logs="$("$ENGINE" logs "$container" 2>&1 || true)"

        if [ "$(printf '%s' "$logs" | grep -c 'No account is signed in')" -ge "$want" ]; then
            return 0
        fi

        # A container that exited is never going to become ready, and waiting
        # out the timeout would hide why.
        if [ "$("$ENGINE" inspect -f '{{.State.Running}}' "$container" 2>/dev/null)" != "true" ]; then
            return 1
        fi

        sleep 2
    done

    return 1
}

cleanup() {
    local name
    for name in "$CONTAINER" "$CONTAINER-alt" "$CONTAINER-plain" "$CONTAINER-configured" "$CONTAINER-exposed" "$CONTAINER-closed"; do
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

# Worked out here rather than passed in, so that a local build produces the
# same labels as a published one and nobody has to remember two arguments.
image_version="$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")"
readonly image_version

# A build from a tarball has no git history. "unknown" is honest; an empty
# label would look like the field was never set.
image_revision="$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
readonly image_revision

if [ -z "${SKIP_BUILD:-}" ]; then
    log "Building $IMAGE from upstream $BRIDGE_COMMIT"
    "$ENGINE" build \
        --build-arg "BRIDGE_COMMIT=$BRIDGE_COMMIT" \
        --build-arg "BRIDGE_VERSION=$BRIDGE_VERSION" \
        --build-arg "IMAGE_VERSION=$image_version" \
        --build-arg "IMAGE_REVISION=$image_revision" \
        -f "$REPO_ROOT/docker/Dockerfile" \
        -t "$IMAGE" \
        "$REPO_ROOT"
fi

# --------------------------------------------------------------------------
# The image says where it came from
# --------------------------------------------------------------------------

# This check is also the guard. The publish workflow runs this script before it
# pushes anything, so an image that lost its labels cannot reach a registry no
# matter who forgets what.
#
# org.opencontainers.image.source is what links a ghcr package back to this
# repository and makes its page show the readme. Without it the page is empty,
# which looks to a visitor like a broken package rather than a missing label.
#
# Compared against expected values, not merely checked for being non-empty: a
# mistyped label name would produce an empty string, and so would a label that
# was never set. Those have to fail the same way.

log "Labels on the image"

label() {
    "$ENGINE" inspect --format "{{index .Config.Labels \"$1\"}}" "$IMAGE" 2>/dev/null || true
}

check_label() {
    local name="$1" want="$2" got
    got="$(label "$name")"

    if [ "$got" = "$want" ]; then
        ok "$name = $got"
    else
        fail "$name is '$got', expected '$want'"
    fi
}

check_label org.opencontainers.image.source "https://github.com/RndmJoker/proton-mail-bridge-docker"
check_label org.opencontainers.image.licenses "GPL-3.0-or-later"
check_label org.opencontainers.image.version "$image_version"
check_label org.opencontainers.image.revision "$image_revision"
check_label com.rndmjoker.bridge.version "$BRIDGE_VERSION"
check_label com.rndmjoker.bridge.commit "$BRIDGE_COMMIT"

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
    -p "127.0.0.1::8443" \
    "$IMAGE" >/dev/null

sleep "$STARTUP_SECONDS"
wait_ready "$CONTAINER" || fail "the container was not ready within ${READY_TIMEOUT}s"

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
# The sign-in page runs, and is not reachable from outside
# --------------------------------------------------------------------------

# No account is signed in, so the page has to be up. And it has to be up where
# nobody outside the container can get at it: it accepts a Proton password, and
# the default is not to expose that to anything.

log "Sign-in page, not exposed"

# Matched on the sentence about where it is bound, not on an address. The log
# used to print the bind address as though it were somewhere to go, which is
# what #27 was about, so a test that looked for `https://127.0.0.1:` would be
# asserting the bug.
if printf '%s\n' "$container_logs" | grep -q 'bound inside the container, so no browser can reach it'; then
    ok "the sign-in page is running, bound inside the container"
else
    fail "no sign-in page although no account is signed in"
fi

# The counter-check: whatever the log says, it must not hand anyone a bind
# address. 0.0.0.0 is not somewhere a browser can go, and 127.0.0.1 is not
# somewhere the reader of a container log can go either.
if printf '%s\n' "$container_logs" | grep -qE 'page is at https://(0\.0\.0\.0|127\.0\.0\.1):'; then
    fail "the log offers a bind address as though it were a destination"
else
    ok "no bind address is offered as a destination"
fi

setup_fingerprint="$(printf '%s\n' "$container_logs" | sed -n 's/.*Certificate fingerprint (SHA-256): //p' | head -n1)"

if printf '%s' "$setup_fingerprint" | grep -qE '^[0-9A-F]{2}(:[0-9A-F]{2}){31}$'; then
    ok "the fingerprint is in the log, where it can be compared: ${setup_fingerprint:0:17}..."
else
    fail "no usable certificate fingerprint in the log"
fi

# A token only exists when the page is exposed. Printing one here would mean
# either the page is exposed when it should not be, or a secret is being
# written to the log for nothing.
if printf '%s\n' "$container_logs" | grep -q 'Access token for the sign-in page'; then
    fail "an access token was printed although the page is not exposed"
else
    ok "no access token was printed, because none is needed"
fi

# The counter-check that matters. The port is published, so anything that
# answers here would be reachable from the host, and from wherever the host is.
setup_host_port="$("$ENGINE" port "$CONTAINER" 8443/tcp 2>/dev/null | head -n1 | sed 's/.*://')"

if [ -z "$setup_host_port" ]; then
    fail "port 8443 is not mapped, so this check cannot tell anything"
elif curl -sk --max-time 5 "https://127.0.0.1:$setup_host_port/" >/dev/null 2>&1; then
    fail "the sign-in page answered from outside the container although it is not exposed"
else
    ok "the sign-in page does not answer from outside, even with the port published"
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

# Twice: once from the first start, once from this one.
wait_ready "$CONTAINER" 2 || fail "container was not ready again within ${READY_TIMEOUT}s"

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

wait_ready "$CONFIGURED_CONTAINER" || fail "configured was not ready within ${READY_TIMEOUT}s"

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

wait_ready "$ALT_CONTAINER" || fail "alt was not ready within ${READY_TIMEOUT}s"

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
# The exposed sign-in page is guarded
# --------------------------------------------------------------------------

# Everything above keeps the page away from the network. This is the other
# half: when it is deliberately opened, what stands between it and whoever can
# reach it.
#
# It reuses the volume from the update checks, so the certificate is the one
# that was already there. That is the point of keeping it in the volume, and
# it is checked below rather than assumed.

log "Sign-in page, exposed"

readonly EXPOSED_CONTAINER="$CONTAINER-exposed"

"$ENGINE" run -d \
    --name "$EXPOSED_CONTAINER" \
    -v "$update_volume_dir:/data:Z" \
    -e "BRIDGE_SETUP_EXPOSE=true" \
    -p "127.0.0.1::8443" \
    "$IMAGE" >/dev/null

wait_ready "$EXPOSED_CONTAINER" || fail "exposed was not ready within ${READY_TIMEOUT}s"

exposed_logs="$("$ENGINE" logs "$EXPOSED_CONTAINER" 2>&1)"

exposed_fingerprint="$(printf '%s\n' "$exposed_logs" | sed -n 's/.*Certificate fingerprint (SHA-256): //p' | head -n1)"

exposed_port="$("$ENGINE" port "$EXPOSED_CONTAINER" 8443/tcp 2>/dev/null | head -n1 | sed 's/.*://')"

setup_token="$(printf '%s\n' "$exposed_logs" | sed -n 's/.*Access token: //p' | head -n1)"

if [ -n "$setup_token" ]; then
    ok "the access token is in the log, where the operator reads it"
else
    fail "no access token in the log, so there is no way to use the exposed page"
fi

# The same token has to be in the volume as well, because that is where
# proton-login reads it from. Compared rather than merely present: two
# different values would mean the terminal way in stops working the moment the
# page is exposed.
stored_token="$("$ENGINE" exec "$EXPOSED_CONTAINER" cat /data/config/protonmail/bridge-v3/setup/token 2>/dev/null || true)"

if [ -n "$stored_token" ] && [ "$stored_token" = "$setup_token" ]; then
    ok "the same token is in the volume, where proton-login reads it"
else
    fail "the token in the volume does not match the one in the log"
fi

if "$ENGINE" exec "$EXPOSED_CONTAINER" stat -c '%a' /data/config/protonmail/bridge-v3/setup/token 2>/dev/null | grep -q '^600$'; then
    ok "the token file is not readable by anyone else"
else
    fail "the token file has the wrong mode"
fi

# Both addresses the container can actually know. The host address is not among
# them and cannot be: port publishing happens outside, and the container sees
# neither the address nor the port it landed on.
if printf '%s\n' "$exposed_logs" | grep -q 'https://127.0.0.1:8443/' \
    && printf '%s\n' "$exposed_logs" | grep -qE 'https://[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:8443/'; then
    ok "the log shows both reachable addresses in full"
else
    fail "the log does not show both addresses"
fi

if [ -z "$exposed_port" ]; then
    fail "port 8443 is not mapped"
else
    # The page itself has to be reachable without the token: it is where the
    # token gets typed in.
    page="$(curl -sk --max-time 10 "https://127.0.0.1:$exposed_port/" 2>/dev/null || true)"

    if printf '%s' "$page" | grep -q 'Sign in to Proton Mail Bridge'; then
        ok "the page answers when exposed"
    else
        fail "the exposed page did not answer"
    fi

    # The fingerprint a person compares has to be the one on the page, or
    # comparing it proves nothing.
    if [ -n "$exposed_fingerprint" ] && printf '%s' "$page" | grep -qF "$exposed_fingerprint"; then
        ok "the fingerprint on the page matches the one in the log"
    else
        fail "the fingerprint on the page does not match the log"
    fi

    # It is the same volume as the update checks used, so this is the same
    # certificate that container created. A new one here would mean the
    # fingerprint changes whenever the container is replaced, which makes it
    # worthless as something to compare against.
    if [ -n "$exposed_fingerprint" ] && [ "$exposed_fingerprint" != "$setup_fingerprint" ]; then
        ok "the certificate in this volume is its own, kept across containers"
    else
        fail "two different volumes produced the same fingerprint, so it is not per-installation"
    fi

    # Without the token, nothing.
    status_code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' "https://127.0.0.1:$exposed_port/api/status" 2>/dev/null || true)"

    if [ "$status_code" = "401" ]; then
        ok "the API refuses a request without the token"
    else
        fail "the API answered $status_code without a token, expected 401"
    fi

    status_code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' \
        -H "X-Setup-Token: not-the-token" \
        "https://127.0.0.1:$exposed_port/api/status" 2>/dev/null || true)"

    if [ "$status_code" = "401" ]; then
        ok "a wrong token is no better than none"
    else
        fail "a wrong token got $status_code, expected 401"
    fi

    # With the token but without the CSRF header. This is the request a page on
    # another site could cause a browser to make.
    status_code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' \
        -X POST \
        -H "X-Setup-Token: $setup_token" \
        -H 'Content-Type: application/json' \
        -d '{"username":"nobody@example.invalid","secret":"not-a-real-password"}' \
        "https://127.0.0.1:$exposed_port/api/login" 2>/dev/null || true)"

    if [ "$status_code" = "403" ]; then
        ok "a write without the CSRF token is refused, even with a valid access token"
    else
        fail "a write without the CSRF token got $status_code, expected 403"
    fi

    # No sign-in is attempted anywhere in this test. The request above is
    # refused before it reaches the bridge, which is why it can carry an
    # invented address without ever touching Proton.
fi

"$ENGINE" rm -f "$EXPOSED_CONTAINER" >/dev/null 2>&1 || true

# A token belongs to the page that issued it and to nothing else. Starting the
# same volume without exposure has to leave nothing behind, or a value that
# stopped meaning anything would sit in the volume looking like a live one.

log "A token does not outlive its page"

readonly CLOSED_CONTAINER="$CONTAINER-closed"

"$ENGINE" run -d \
    --name "$CLOSED_CONTAINER" \
    -v "$update_volume_dir:/data:Z" \
    "$IMAGE" >/dev/null

wait_ready "$CLOSED_CONTAINER" || fail "closed was not ready within ${READY_TIMEOUT}s"

if "$ENGINE" exec "$CLOSED_CONTAINER" test -f /data/config/protonmail/bridge-v3/setup/token 2>/dev/null; then
    fail "the token from the exposed run is still in the volume"
else
    ok "the token from the exposed run is gone"
fi

"$ENGINE" rm -f "$CLOSED_CONTAINER" >/dev/null 2>&1 || true

# --------------------------------------------------------------------------

printf '\n'
if [ "$failures" -eq 0 ]; then
    echo "Smoke test passed."
else
    echo "Smoke test failed: $failures check(s)."
    exit 1
fi
