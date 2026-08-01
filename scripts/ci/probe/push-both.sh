#!/usr/bin/env bash
#
# Push the probe image to both registries, one of three ways.
#
# Part of the measurement for #39. Delete this directory once that is closed.
#
#   plain    docker tag + docker push, once per registry. What the real build
#            does today, and what produced two different digests.
#   copy     push to ghcr, then `buildx imagetools create` to copy the manifest
#            across by digest rather than re-uploading an image.
#   buildx   one `buildx build --push` naming both registries, so the same
#            manifest is written to both in a single operation.
#
# Each variant uses its own tag, so all three end up side by side in the same
# throwaway repository and can be compared afterwards.
#
# Usage:
#   bash scripts/ci/probe/push-both.sh plain|copy|buildx

set -euo pipefail

variant="${1:-}"
probe="${PROBE:-digest-probe}"
user="${DOCKERHUB_USER:-rndmjoker}"

ghcr="ghcr.io/$user/$probe:$variant"
hub="docker.io/$user/$probe:$variant"

case "$variant" in
    plain)
        docker tag "$probe:local" "$ghcr"
        docker tag "$probe:local" "$hub"
        docker push --quiet "$ghcr"
        docker push --quiet "$hub"

        echo "What the client believes it pushed:"
        docker image inspect "$probe:local" --format '{{range .RepoDigests}}  {{println .}}{{end}}'
        ;;

    copy)
        docker tag "$probe:local" "$ghcr"
        docker push --quiet "$ghcr"

        # Source given by tag rather than digest on purpose: this is how the
        # workflow would use it, and buildx resolves the tag itself.
        docker buildx imagetools create --tag "$hub" "$ghcr"
        ;;

    buildx)
        # A fresh context, because buildx builds from source rather than from
        # an image that already exists locally.
        mkdir -p probe-ctx
        printf 'nothing to see here\n' > probe-ctx/marker
        cat > probe-ctx/Dockerfile <<'EOF'
FROM scratch
COPY marker /marker
EOF
        docker buildx build --push --tag "$ghcr" --tag "$hub" probe-ctx
        ;;

    *)
        echo "Usage: $0 plain|copy|buildx" >&2
        exit 1
        ;;
esac
