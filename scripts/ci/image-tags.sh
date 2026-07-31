#!/usr/bin/env bash
#
# Work out which tags a published image gets.
#
# Kept out of the workflow so that it can be read, linted and run on its own.
# A tag scheme buried in a YAML `run:` block is one nobody checks until the
# wrong thing is published under the right name.
#
# The scheme, for bridge 3.25.0 in image version 0.5.0:
#
#   3.25.0-0.5.0   both versions. The only one that changes when nothing but
#                  this repository changed, so a new entrypoint is visible
#                  without waiting for Proton to release something
#   3.25.0         newest image for that bridge version
#   3.25           newest image for that bridge minor
#   edge           newest build, whatever it contains
#
# There is deliberately no `latest`. Every release so far is marked as a
# pre-release on GitHub, and a `latest` tag would say the opposite to every
# tool that looks for one.
#
# None of these are immutable, because the nightly rebuild moves all of them:
# that is the point, it is how Debian's security updates reach the image. Only
# the digest is fixed, which is also what the provenance attestation binds to.
#
# Usage:
#   bash scripts/ci/image-tags.sh                  # print the refs, one per line
#   REGISTRIES="ghcr.io/x/y docker.io/x/y" ...
#
# Writes `tags` and `primary` to $GITHUB_OUTPUT when running in Actions.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT

# Space separated, in order. The first one is the primary: it is what the
# provenance attestation is filed against, and it has to be the registry that
# accepts the attestation being pushed alongside the image.
readonly REGISTRIES="${REGISTRIES:-ghcr.io/rndmjoker/proton-mail-bridge-docker docker.io/rndmjoker/proton-mail-bridge-docker}"

# Not a .sh file and deliberately so: it is shared with the Dockerfile build
# arguments and with CI, which read it as plain data.
# shellcheck disable=SC1091
source "$REPO_ROOT/docker/bridge-version"

image_version="$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")"

if [ -z "${BRIDGE_VERSION:-}" ]; then
    echo "::error::BRIDGE_VERSION is empty. docker/bridge-version was not read." >&2
    exit 1
fi

if [ -z "$image_version" ]; then
    echo "::error::VERSION is empty." >&2
    exit 1
fi

# 3.25.0 -> 3.25. Anything that is not two dot-separated numbers followed by a
# third is a mistake worth stopping for, not something to guess at.
case "$BRIDGE_VERSION" in
    [0-9]*.[0-9]*.[0-9]*) bridge_minor="${BRIDGE_VERSION%.*}" ;;
    *)
        echo "::error::BRIDGE_VERSION '$BRIDGE_VERSION' is not in major.minor.patch form." >&2
        exit 1
        ;;
esac

readonly TAGS="${BRIDGE_VERSION}-${image_version} ${BRIDGE_VERSION} ${bridge_minor} edge"

refs=""
for registry in $REGISTRIES; do
    for tag in $TAGS; do
        refs="${refs}${registry}:${tag}"$'\n'
    done
done
refs="${refs%$'\n'}"

primary="$(printf '%s' "$REGISTRIES" | cut -d' ' -f1):${BRIDGE_VERSION}-${image_version}"

printf '%s\n' "$refs"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
        printf 'tags<<EOF\n%s\nEOF\n' "$refs"
        printf 'primary=%s\n' "$primary"
        printf 'bridge_version=%s\n' "$BRIDGE_VERSION"
        printf 'image_version=%s\n' "$image_version"
    } >> "$GITHUB_OUTPUT"
fi
