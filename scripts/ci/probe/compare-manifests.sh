#!/usr/bin/env bash
#
# Read both registries' manifests back and say exactly how they differ.
#
# Part of the measurement for #39. Delete this directory once that is closed.
#
# The digest is a hash over the manifest bytes as the registry stores them, so
# "the digests differ" and "the manifests differ" are the same statement. What
# is not obvious is *what* differs - media type, field order, an added or
# dropped field - and that is the thing worth knowing before choosing a fix.
#
# Deliberately curl and not `docker manifest inspect`: the latter reformats
# what it prints, which is exactly the property under investigation.
#
# Usage:
#   bash scripts/ci/probe/compare-manifests.sh <variant>

set -euo pipefail

variant="${1:-}"
probe="${PROBE:-digest-probe}"
user="${DOCKERHUB_USER:-rndmjoker}"

[ -n "$variant" ] || { echo "Usage: $0 <variant>" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Every media type a registry might hand back, so nothing is excluded by the
# request itself. A registry that can only produce one of them will, and that
# is a result rather than an error.
readonly ACCEPT='application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json'

fetch() {
    local registry="$1" token_url="$2" scope="$3" out="$4"

    local token
    token="$(curl -sS "$token_url" | jq -r '.token // empty')"
    if [ -z "$token" ]; then
        echo "::error::No token from $token_url" >&2
        return 1
    fi

    curl -sS -D "$out.headers" -H "Authorization: Bearer $token" -H "Accept: $ACCEPT" \
        "https://$registry/v2/$scope/manifests/$variant" -o "$out.body"
}

fetch "ghcr.io" \
    "https://ghcr.io/token?service=ghcr.io&scope=repository:$user/$probe:pull" \
    "$user/$probe" "$work/ghcr"

# Docker Hub's token endpoint needs credentials for a private repository. The
# push login above already wrote them to ~/.docker/config.json.
hub_auth="$(jq -r '.auths["https://index.docker.io/v1/"].auth // empty' ~/.docker/config.json)"
hub_token="$(
    curl -sS -H "Authorization: Basic $hub_auth" \
        "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$user/$probe:pull" \
        | jq -r '.token // empty'
)"
[ -n "$hub_token" ] || { echo "::error::No token from Docker Hub." >&2; exit 1; }

curl -sS -D "$work/hub.headers" -H "Authorization: Bearer $hub_token" -H "Accept: $ACCEPT" \
    "https://registry-1.docker.io/v2/$user/$probe/manifests/$variant" -o "$work/hub.body"

report() {
    local name="$1" file="$2"
    echo "--- $name"
    echo "    digest reported: $(grep -i '^docker-content-digest:' "$file.headers" | tr -d '\r' | awk '{print $2}')"
    echo "    digest computed: sha256:$(sha256sum "$file.body" | cut -d' ' -f1)"
    echo "    content-type:    $(grep -i '^content-type:' "$file.headers" | tr -d '\r' | awk '{print $2}')"
    echo "    bytes:           $(wc -c < "$file.body")"
    echo "    mediaType field: $(jq -r '.mediaType // "(absent)"' "$file.body")"
    echo "    config type:     $(jq -r '.config.mediaType // "(absent)"' "$file.body")"
    echo "    layer types:     $(jq -r '[.layers[]?.mediaType] | join(", ")' "$file.body")"
}

echo "=== $variant"
report "ghcr.io" "$work/ghcr"
report "docker.io" "$work/hub"

echo ""
if cmp -s "$work/ghcr.body" "$work/hub.body"; then
    echo "IDENTICAL: both registries store the same manifest, byte for byte."
else
    echo "DIFFERENT. The manifests, side by side:"
    diff -u <(jq -S . "$work/ghcr.body") <(jq -S . "$work/hub.body") || true
    echo ""
    echo "Raw bytes, in case the difference is only formatting:"
    diff -u <(cat "$work/ghcr.body"; echo) <(cat "$work/hub.body"; echo) || true
fi
echo ""
