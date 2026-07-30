#!/usr/bin/env bash
#
# Container entrypoint for the Proton Mail Bridge.
#
# It does three things the bridge cannot do for itself in a container: give it
# a keychain, keep everything it writes inside one volume, and make its mail
# ports reachable from outside the container.
#
# This stays deliberately thin. Anything that needs to talk to the bridge over
# gRPC belongs in bridge-control, not here.

set -euo pipefail

readonly VOLUME="${BRIDGE_HOME:-/data}"
readonly LOG_LEVEL="${BRIDGE_LOG_LEVEL:-info}"
readonly IMAP_PORT="${BRIDGE_IMAP_PORT:-1143}"
readonly SMTP_PORT="${BRIDGE_SMTP_PORT:-1025}"

# How long to wait for the bridge to start listening before giving up on
# forwarding a port. The bridge opens both ports even with no account signed
# in, so in practice this is a couple of seconds; the timeout only matters if
# it picked a different port than expected.
readonly FORWARD_TIMEOUT="${BRIDGE_FORWARD_TIMEOUT:-60}"

bridge_pid=""

log() {
    printf '%s  entrypoint  %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

fail() {
    log "ERROR: $*"
    exit 1
}

# --------------------------------------------------------------------------
# Volume
# --------------------------------------------------------------------------

# A bind mount arrives with whatever ownership it had on the host, and the
# container never runs as root, so it cannot fix that itself. Failing here with
# an explanation beats failing later inside gnupg with something cryptic.
prepare_volume() {
    [ -d "$VOLUME" ] || fail "$VOLUME does not exist. Mount a volume there."

    if [ ! -w "$VOLUME" ]; then
        fail "$VOLUME is not writable by uid $(id -u). If it is a bind mount, run: chown -R 1000:1000 <path>"
    fi

    # 0700 throughout: the volume holds the GPG key that unlocks the vault, and
    # the vault holds the mailbox.
    local dir
    for dir in "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_CACHE_HOME" "$GNUPGHOME" "$PASSWORD_STORE_DIR"; do
        install -d -m 0700 "$dir"
    done
}

# --------------------------------------------------------------------------
# Keychain
# --------------------------------------------------------------------------

# The bridge stores its vault key in a keychain. In a container there is no
# secret service, which leaves pass, and pass needs a GPG key. Unattended
# operation leaves no one to type a passphrase, so the key has none.
#
# This is the single most sensitive fact about this image and it is repeated in
# the README: whoever holds the volume holds the mailbox.
setup_gpg_key() {
    if gpg --list-secret-keys --with-colons 2>/dev/null | grep -q '^sec:'; then
        return 0
    fi

    log "No GPG key in $GNUPGHOME yet, generating one (no passphrase, see README)."

    gpg --batch --gen-key >/dev/null 2>&1 <<EOF
%no-protection
Key-Type: eddsa
Key-Curve: ed25519
Key-Usage: sign
Subkey-Type: ecdh
Subkey-Curve: cv25519
Subkey-Usage: encrypt
Name-Real: Proton Mail Bridge Container
Name-Email: bridge@localhost
Expire-Date: 0
%commit
EOF
}

gpg_fingerprint() {
    gpg --list-secret-keys --with-colons | awk -F: '/^fpr:/ { print $10; exit }'
}

setup_pass_store() {
    if [ -f "$PASSWORD_STORE_DIR/.gpg-id" ]; then
        return 0
    fi

    local fingerprint
    fingerprint="$(gpg_fingerprint)"
    [ -n "$fingerprint" ] || fail "GPG key was generated but no fingerprint came back."

    log "Initialising the pass store for key $fingerprint."
    pass init "$fingerprint" >/dev/null
}

# --------------------------------------------------------------------------
# Port forwarding
# --------------------------------------------------------------------------

# The bridge binds IMAP and SMTP on 127.0.0.1 only, and refuses to be talked
# out of it. Inside a container that means nothing outside can reach them.
#
# socat listens on the container's own address and forwards to the loopback
# one, so the bridge still sees a local connection. The port number stays the
# same on both sides: two different addresses, no conflict.
#
# Order matters. The bridge picks its ports by asking the kernel which ones are
# free, and it checks the wildcard address as well as loopback. If socat bound
# the port first, the bridge would move to the next one. So the bridge always
# goes first and socat only follows once the port is actually listening.
container_address() {
    hostname -I 2>/dev/null | awk '{ print $1 }'
}

# Reads /proc/net/tcp rather than opening a connection: a probe against the
# IMAP port would show up as a real client session in the bridge log.
is_listening() {
    local port_hex
    port_hex="$(printf '%04X' "$1")"

    # Field 2 is the local address as ADDRESS:PORT, field 4 is the state.
    # 0A is TCP_LISTEN.
    awk -v suffix=":$port_hex" '
        $2 ~ suffix"$" && $4 == "0A" { found = 1 }
        END { exit !found }
    ' /proc/net/tcp
}

forward_port() {
    local port="$1" label="$2" address="$3"
    local waited=0

    while ! is_listening "$port"; do
        if [ "$waited" -ge "$FORWARD_TIMEOUT" ]; then
            log "WARNING: the bridge did not open $label port $port within ${FORWARD_TIMEOUT}s, so it stays unreachable from outside the container. It may have picked a different port; check the bridge log above."
            return 0
        fi
        sleep 2
        waited=$((waited + 2))
    done

    log "Forwarding $label: $address:$port to 127.0.0.1:$port"
    socat "TCP-LISTEN:$port,bind=$address,fork,reuseaddr" "TCP:127.0.0.1:$port" &
}

start_forwarding() {
    local address
    address="$(container_address)"

    if [ -z "$address" ]; then
        log "WARNING: could not determine the container address, mail ports stay on loopback only."
        return 0
    fi

    forward_port "$IMAP_PORT" IMAP "$address" &
    forward_port "$SMTP_PORT" SMTP "$address" &
}

# --------------------------------------------------------------------------
# Lifecycle
# --------------------------------------------------------------------------

shutdown() {
    log "Shutting down."
    if [ -n "$bridge_pid" ] && kill -0 "$bridge_pid" 2>/dev/null; then
        kill -TERM "$bridge_pid" 2>/dev/null || true
        wait "$bridge_pid" 2>/dev/null || true
    fi
    exit 0
}

main() {
    prepare_volume
    setup_gpg_key
    setup_pass_store

    trap shutdown TERM INT

    log "Starting the bridge."
    bridge --noninteractive --log-level "$LOG_LEVEL" &
    bridge_pid=$!

    start_forwarding

    # `wait` returns on every signal, not only when the process ends, so it is
    # retried until the bridge is genuinely gone. Its exit status is the
    # bridge's own and is not interesting here; the loop condition decides.
    while kill -0 "$bridge_pid" 2>/dev/null; do
        wait "$bridge_pid" || true
    done

    log "The bridge exited."
}

main "$@"
