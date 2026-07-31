#!/usr/bin/env bash
#
# Which builder will `docker build` actually use?
#
# BuildKit has been Docker's default since version 23 and the runner image
# ships a newer one, so it is in use today. Nothing pins that. Setting
# DOCKER_BUILDKIT=1 in the workflow states the intent; this script is what
# turns the intent into a fact, because a variable that is silently ignored
# looks exactly like one that works.
#
# The two builders can be told apart by their first line of output:
#
#   BuildKit      #1 [internal] load build definition from Dockerfile
#   the old one   Step 1/1 : FROM scratch
#
# They differ in caching, in how multi-stage builds are pruned, and in which
# parts of a Dockerfile they reach at all. A silent switch would therefore not
# surface as an error message but as a different image.
#
# This runs in CI only. Local builds here go through podman and buildah, a
# third builder that no environment variable selects, and running this against
# them would fail for reasons that have nothing to do with what it checks.

set -euo pipefail

probe_dir="$(mktemp -d)"
trap 'rm -rf "$probe_dir"' EXIT

# FROM scratch fetches nothing and produces an empty image. It exists only to
# make the builder identify itself, and it takes under a second.
printf 'FROM scratch\n' > "$probe_dir/Dockerfile"

echo "DOCKER_BUILDKIT=${DOCKER_BUILDKIT:-<unset>}"
docker version --format 'docker {{.Server.Version}}'

output="$(docker build "$probe_dir" 2>&1)"
printf '%s\n' "$output" | sed 's/^/  | /'

case "$output" in
    *'load build definition'*)
        echo "BuildKit, as expected."
        ;;
    *'Step 1/'*)
        echo "::error::The legacy builder is in use, not BuildKit."
        exit 1
        ;;
    *)
        # Neither marker found. Docker changed its output and this check no
        # longer knows what it is looking at, which is not the same as passing.
        # A check that cannot fail is worse than no check, because everyone
        # believes it.
        echo "::error::Could not tell which builder ran. Neither marker was found."
        exit 1
        ;;
esac
