#!/usr/bin/env bash
#
# Tag the image that was just built and tested, push it everywhere, and check
# that every registry came back with the same digest.
#
# That last part is the reason this is a script and not three lines of YAML.
# The provenance attestation signs a digest, not a tag. If one registry stored
# a different manifest than the other, the attestation would verify against one
# image and silently not match the other, and the copy people actually pulled
# would be the unattested one. Comparing the digests is how that stops being a
# thing to hope for.
#
# ## What this check caught once, and what it actually meant
#
# On 2026-07-31 it reported two different digests and failed the step, which
# looked like Docker Hub rewriting the manifest on the way in. It was not.
# Measured on 2026-08-01 by pushing one minimal image to both registries three
# different ways (#39):
#
#   docker tag + docker push, per registry   identical, byte for byte
#   docker buildx build --push, both tags    identical, byte for byte
#   buildx imagetools create to copy across  DIFFERENT
#
# The third is the one that had been proposed as the fix. It turns a
# manifest into a manifest list on the way over - docker/buildx#2481 - so
# adopting it would have caused the problem it was meant to solve. The
# method already in use here is the correct one.
#
# The real cause was two publish runs overlapping by eight seconds and
# competing for the same movable tags. publish.yml has a `concurrency` group
# now. This comparison stays anyway: it cannot prevent anything, since the
# digest does not exist until after the push, but it is the only thing that
# would notice a registry changing its behaviour later.
#
# The refs to push are read from standard input, one per line, so this composes
# with scripts/ci/image-tags.sh.
#
# Usage:
#   bash scripts/ci/image-tags.sh | LOCAL_IMAGE=x bash scripts/ci/push-image.sh
#
# Writes `digest` to $GITHUB_OUTPUT when running in Actions.

set -euo pipefail

if [ -z "${LOCAL_IMAGE:-}" ]; then
    echo "::error::LOCAL_IMAGE must name the image that was built and tested." >&2
    exit 1
fi

mapfile -t refs

if [ "${#refs[@]}" -eq 0 ]; then
    echo "::error::No refs on standard input. Nothing would have been published." >&2
    exit 1
fi

for ref in "${refs[@]}"; do
    echo "  -> $ref"
    docker tag "$LOCAL_IMAGE" "$ref"
    docker push --quiet "$ref"
done

# RepoDigests carries one entry per registry the image was pushed to, in the
# form registry/name@sha256:... Everything after the @ has to agree.
mapfile -t digests < <(
    docker inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$LOCAL_IMAGE" \
        | sed -n 's/.*@//p' \
        | sort -u
)

case "${#digests[@]}" in
    1)
        echo "Both registries hold the same manifest: ${digests[0]}"
        ;;
    0)
        echo "::error::No digest came back from any registry. Nothing was pushed." >&2
        exit 1
        ;;
    *)
        echo "::error::The registries returned different digests, so provenance would only cover one of them:" >&2
        printf '::error::  %s\n' "${digests[@]}" >&2
        exit 1
        ;;
esac

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'digest=%s\n' "${digests[0]}" >> "$GITHUB_OUTPUT"
fi
