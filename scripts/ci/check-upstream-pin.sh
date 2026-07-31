#!/usr/bin/env bash
#
# Does BRIDGE_TAG still resolve to BRIDGE_COMMIT upstream?
#
# This is the check the pinning exists for. The image is built from the hash,
# never from the tag, so a moved tag cannot change what gets built. What it
# would change is the meaning of the record: docker/bridge-version says this
# image is Proton's v3.25.0, and that claim is only true as long as the tag
# still points where it did.
#
# A tag can be moved by whoever controls the upstream repository, and after a
# takeover that is the cheapest thing to do. It leaves no trace in a build that
# names only the hash, which is exactly why it has to be looked for on purpose.
#
# A mismatch is a hard failure. There is no benign explanation that a person
# should not see.
#
# Annotated tags are dereferenced. Proton's are lightweight today, but a switch
# to annotated ones would otherwise compare a tag object against a commit and
# report tampering where there is none.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT

readonly UPSTREAM="${UPSTREAM:-ProtonMail/proton-bridge}"

# shellcheck disable=SC1091
source "$REPO_ROOT/docker/bridge-version"

if [ -z "${BRIDGE_TAG:-}" ] || [ -z "${BRIDGE_COMMIT:-}" ]; then
    echo "::error::BRIDGE_TAG or BRIDGE_COMMIT is empty. docker/bridge-version was not read." >&2
    exit 1
fi

ref="$(gh api "repos/$UPSTREAM/git/ref/tags/$BRIDGE_TAG" --jq '.object.type + " " + .object.sha')"
kind="${ref%% *}"
sha="${ref##* }"

if [ "$kind" = "tag" ]; then
    sha="$(gh api "repos/$UPSTREAM/git/tags/$sha" --jq '.object.sha')"
fi

if [ "$sha" = "$BRIDGE_COMMIT" ]; then
    echo "$BRIDGE_TAG still points at $BRIDGE_COMMIT."
    exit 0
fi

echo "::error::$UPSTREAM tag $BRIDGE_TAG has moved."
echo "::error::  pinned here:  $BRIDGE_COMMIT"
echo "::error::  upstream now: $sha"
echo "::error::Nothing built from docker/bridge-version is affected, because the build names the hash."
echo "::error::What is affected is the claim that this image is $BRIDGE_TAG. Look at why the tag moved before touching anything."
exit 1
