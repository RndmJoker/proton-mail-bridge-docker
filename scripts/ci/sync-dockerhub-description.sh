#!/usr/bin/env bash
#
# Push docker/dockerhub-description.md to the Docker Hub repository page.
#
# Docker Hub keeps its own description, separate from this repository's readme,
# and nothing links the two. Left alone it goes stale, and the people it goes
# stale for are exactly the ones who will never see this repository: they pull
# the image and read whatever the registry page says. The two warnings about
# the volume and about the mail ports have to survive that trip.
#
# Written against the API by hand rather than through a third-party action.
# Everything else in this repository's CI is fetched pinned and run directly,
# and a container that builds the key to a mailbox is a poor place to add a
# trust relationship for the sake of ten lines of curl.
#
# Runs after the push, not before: the repository has to exist on Docker Hub
# before it can be described, and the first push is what creates it.
#
# Environment:
#   DOCKERHUB_USER    account the token belongs to
#   DOCKERHUB_TOKEN   access token with write permission, not the password

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT

readonly NAMESPACE="${DOCKERHUB_NAMESPACE:-rndmjoker}"
readonly REPOSITORY="${DOCKERHUB_REPOSITORY:-proton-mail-bridge-docker}"
readonly DESCRIPTION_FILE="$REPO_ROOT/docker/dockerhub-description.md"

# Docker Hub caps the short description at 100 characters and silently
# truncates beyond that, so it is kept well inside.
readonly SHORT_DESCRIPTION="Proton Mail Bridge for servers: no GUI, env vars, one-time login. Unofficial."

if [ -z "${DOCKERHUB_USER:-}" ] || [ -z "${DOCKERHUB_TOKEN:-}" ]; then
    echo "::error::DOCKERHUB_USER and DOCKERHUB_TOKEN must both be set." >&2
    exit 1
fi

if [ ! -f "$DESCRIPTION_FILE" ]; then
    echo "::error::$DESCRIPTION_FILE is missing." >&2
    exit 1
fi

# jq builds every JSON body here. Hand-written quoting would break on the first
# apostrophe in the description, and it is a markdown file full of prose.
#
# `// empty` rather than reading .token directly: on a rejected login Docker
# Hub answers 200 with an error object, and plain jq would turn the missing
# field into the four characters "null" and hand them on as a token.
jwt="$(
    jq -n --arg u "$DOCKERHUB_USER" --arg p "$DOCKERHUB_TOKEN" '{username: $u, password: $p}' \
        | curl -sS --fail-with-body \
            -H 'Content-Type: application/json' \
            --data-binary @- \
            https://hub.docker.com/v2/users/login/ \
        | jq -r '.token // empty'
)"

if [ -z "$jwt" ]; then
    echo "::error::Docker Hub returned no token. Check DOCKERHUB_TOKEN." >&2
    exit 1
fi

# The token never appears as a command line argument. A curl config file is
# read with the same effect and does not show up in the process list, nor in
# the trace output if anyone ever turns on `set -x` to debug this.
config_file="$(mktemp)"
readonly config_file
chmod 600 "$config_file"
trap 'rm -f "$config_file"' EXIT

printf 'header = "Authorization: JWT %s"\n' "$jwt" > "$config_file"

status="$(
    jq -n \
        --rawfile full "$DESCRIPTION_FILE" \
        --arg short "$SHORT_DESCRIPTION" \
        '{full_description: $full, description: $short}' \
    | curl -sS -o /dev/null -w '%{http_code}' \
        --config "$config_file" \
        -X PATCH \
        -H 'Content-Type: application/json' \
        --data-binary @- \
        "https://hub.docker.com/v2/repositories/$NAMESPACE/$REPOSITORY/"
)"

case "$status" in
    200)
        echo "Description updated on docker.io/$NAMESPACE/$REPOSITORY."
        ;;
    404)
        echo "::error::docker.io/$NAMESPACE/$REPOSITORY does not exist. The push should have created it." >&2
        exit 1
        ;;
    *)
        echo "::error::Docker Hub answered $status when updating the description." >&2
        exit 1
        ;;
esac
