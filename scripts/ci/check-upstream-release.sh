#!/usr/bin/env bash
#
# Has Proton published a bridge release other than the one this image is built
# from?
#
# Reports, and nothing more. It does not raise BRIDGE_COMMIT, it does not build
# and it does not publish. What Proton changed gets read by a person first: the
# container drives the bridge over a gRPC interface, and a renamed field turns
# into a call that quietly does nothing. Finding that out from a mail client
# that stopped working is much worse than a day of delay.
#
# Any difference counts, not only a higher version. A latest release that went
# backwards means somebody unpublished something, and that deserves a look at
# least as much as a new one does.
#
# Writes to $GITHUB_OUTPUT when running in Actions:
#   differs         true or false
#   tag             the upstream tag, for example v3.26.0
#   version         the same without the v
#   pinned_tag      what this repository is built from today
#   url             the release page
#   published       ISO timestamp
#   changelog_file  path to the release body, verbatim

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT

readonly UPSTREAM="${UPSTREAM:-ProtonMail/proton-bridge}"
readonly CHANGELOG_FILE="${CHANGELOG_FILE:-${RUNNER_TEMP:-/tmp}/upstream-changelog.md}"

# shellcheck disable=SC1091
source "$REPO_ROOT/docker/bridge-version"

if [ -z "${BRIDGE_TAG:-}" ]; then
    echo "::error::BRIDGE_TAG is empty. docker/bridge-version was not read." >&2
    exit 1
fi

release="$(gh api "repos/$UPSTREAM/releases/latest")"

tag="$(printf '%s' "$release" | jq -r '.tag_name // empty')"
url="$(printf '%s' "$release" | jq -r '.html_url // empty')"
published="$(printf '%s' "$release" | jq -r '.published_at // empty')"

if [ -z "$tag" ]; then
    echo "::error::$UPSTREAM has no latest release, or the answer had no tag_name." >&2
    exit 1
fi

printf '%s' "$release" | jq -r '.body // ""' > "$CHANGELOG_FILE"

if [ "$tag" = "$BRIDGE_TAG" ]; then
    differs=false
    echo "Up to date. $UPSTREAM latest is $tag, which is what this image is built from."
else
    differs=true
    echo "$UPSTREAM latest is $tag. This image is built from $BRIDGE_TAG."
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
        printf 'differs=%s\n' "$differs"
        printf 'tag=%s\n' "$tag"
        printf 'version=%s\n' "${tag#v}"
        printf 'pinned_tag=%s\n' "$BRIDGE_TAG"
        printf 'url=%s\n' "$url"
        printf 'published=%s\n' "$published"
        printf 'changelog_file=%s\n' "$CHANGELOG_FILE"
    } >> "$GITHUB_OUTPUT"
fi
