#!/usr/bin/env bash
#
# Container entrypoint for the Proton Mail Bridge.
#
# It does the two things that have to happen before anything can talk to the
# bridge at all: make the volume usable, and put a keychain in a container that
# has none. Then it hands over to bridge-control and disappears.
#
# This stays deliberately thin. Anything that needs to talk to the bridge over
# gRPC belongs in bridge-control, not here. That includes the mail port
# forwarding, which used to live here: it has to happen after the ports are
# known, and the ports are only known over gRPC.

set -euo pipefail

readonly VOLUME="${BRIDGE_HOME:-/data}"

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

main() {
    prepare_volume
    setup_gpg_key
    setup_pass_store

    log "Handing over to bridge-control."

    # exec rather than a background process: bridge-control is what has to
    # receive SIGTERM from the container runtime, and a shell in between would
    # have to forward it correctly to gain nothing.
    exec bridge-control
}

main "$@"
