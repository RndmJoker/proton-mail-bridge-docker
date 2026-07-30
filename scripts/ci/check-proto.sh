#!/usr/bin/env bash
#
# Checks that proto/bridge.proto is still byte-for-byte what upstream has at
# the commit pinned in docker/bridge-version.
#
# The copy itself is not the point. The point is that raising BRIDGE_COMMIT
# changes the interface this container drives the bridge through, and without
# this check that would happen silently: the image would build, the smoke test
# would pass, and a renamed field would only show up as a call that does
# nothing.
#
# Usage:
#   bash scripts/ci/check-proto.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT

readonly LOCAL_FILE="$REPO_ROOT/proto/bridge.proto"

# Where the file lives in Proton's tree.
readonly UPSTREAM_PATH="internal/frontend/grpc/bridge.proto"

# Not a .sh file and deliberately so: it is shared with the Dockerfile build
# arguments and with CI, which read it as plain data.
# shellcheck disable=SC1091
source "$REPO_ROOT/docker/bridge-version"

if [ ! -f "$LOCAL_FILE" ]; then
    echo "proto/bridge.proto is missing." >&2
    exit 1
fi

url="https://raw.githubusercontent.com/ProtonMail/proton-bridge/${BRIDGE_COMMIT}/${UPSTREAM_PATH}"

echo "Comparing proto/bridge.proto against $BRIDGE_TAG ($BRIDGE_COMMIT)"

upstream_file="$(mktemp)"
trap 'rm -f "$upstream_file"' EXIT

if ! curl -sSfLo "$upstream_file" "$url"; then
    echo "Could not fetch $url" >&2
    exit 1
fi

if diff -u "$upstream_file" "$LOCAL_FILE" > /dev/null; then
    echo "  ok    identical to upstream"
    exit 0
fi

echo "  FAIL  proto/bridge.proto differs from upstream at the pinned commit." >&2
echo >&2
echo "If BRIDGE_COMMIT was raised on purpose, read the diff before copying the" >&2
echo "new file in: it is the interface bridge-control and proton-info depend on." >&2
echo >&2
diff -u "$upstream_file" "$LOCAL_FILE" | head -100 >&2
exit 1
