#!/usr/bin/env bash
#
# Create or delete the throwaway repository the digest probe pushes into.
#
# Part of the measurement for #39. Delete this directory once that is closed.
#
# Why this exists at all: pushing into a repository that does not exist creates
# it, and Docker Hub creates it public. Nothing about this probe should end up
# publicly readable, so the repository is created private through the API
# first, and removed again at the end.
#
# Usage:
#   DOCKERHUB_TOKEN=... bash scripts/ci/probe/dockerhub-repo.sh create
#   DOCKERHUB_TOKEN=... bash scripts/ci/probe/dockerhub-repo.sh delete

set -euo pipefail

action="${1:-}"
user="${DOCKERHUB_USER:-rndmjoker}"
name="${PROBE:-digest-probe}"

if [ -z "${DOCKERHUB_TOKEN:-}" ]; then
    echo "::error::DOCKERHUB_TOKEN is not set." >&2
    exit 1
fi

# The v2 API wants a JWT, and an access token is exchanged for one at /users/login.
jwt="$(
    curl -sS -X POST https://hub.docker.com/v2/users/login \
        -H 'Content-Type: application/json' \
        -d "$(printf '{"username":"%s","password":"%s"}' "$user" "$DOCKERHUB_TOKEN")" \
        | jq -r '.token // empty'
)"

# `// empty` rather than a plain `.token`: a missing field would otherwise come
# back as the four-character string "null", which is not empty and would sail
# straight through the check below. That already happened once, in
# sync-dockerhub-description.sh.
if [ -z "$jwt" ]; then
    echo "::error::Docker Hub did not return a token. Is DOCKERHUB_TOKEN valid?" >&2
    exit 1
fi

api() {
    curl -sS -w '\n%{http_code}' -H "Authorization: JWT $jwt" "$@"
}

case "$action" in
    create)
        body="$(
            api -X POST "https://hub.docker.com/v2/repositories/" \
                -H 'Content-Type: application/json' \
                -d "$(printf '{"namespace":"%s","name":"%s","is_private":true,"description":"Throwaway, measuring #39. Delete on sight."}' "$user" "$name")"
        )"
        code="$(printf '%s' "$body" | tail -n1)"

        case "$code" in
            200|201)
                echo "Created $user/$name, private."
                ;;
            400)
                # Already there. Confirm it is private before pushing into it -
                # inheriting a public repository would defeat the point.
                echo "$user/$name already exists, checking that it is private."
                vis="$(api "https://hub.docker.com/v2/repositories/$user/$name/" | sed '$d' | jq -r '.is_private')"
                if [ "$vis" != "true" ]; then
                    echo "::error::$user/$name exists and is public. Refusing to push into it." >&2
                    exit 1
                fi
                echo "It is private."
                ;;
            *)
                echo "::error::Creating $user/$name failed with HTTP $code:" >&2
                printf '%s\n' "$body" | sed '$d' >&2
                exit 1
                ;;
        esac

        # Read it back rather than trust the create call. This is the one thing
        # in the probe that must be true.
        vis="$(api "https://hub.docker.com/v2/repositories/$user/$name/" | sed '$d' | jq -r '.is_private')"
        if [ "$vis" != "true" ]; then
            echo "::error::$user/$name does not read back as private (is_private=$vis)." >&2
            exit 1
        fi
        echo "Verified private."
        ;;

    delete)
        body="$(api -X DELETE "https://hub.docker.com/v2/repositories/$user/$name/")"
        code="$(printf '%s' "$body" | tail -n1)"
        case "$code" in
            202|204|404)
                echo "$user/$name is gone (HTTP $code)."
                ;;
            *)
                echo "::error::Deleting $user/$name failed with HTTP $code. Remove it by hand." >&2
                printf '%s\n' "$body" | sed '$d' >&2
                exit 1
                ;;
        esac
        ;;

    *)
        echo "Usage: $0 create|delete" >&2
        exit 1
        ;;
esac
