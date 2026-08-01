#!/usr/bin/env bash
#
# Check that the throwaway repository the digest probe pushes into exists and
# is private. Creates nothing.
#
# Part of the measurement for #39. Delete this directory once that is closed.
#
# Why it only checks: pushing into a repository that does not exist creates it,
# and Docker Hub creates it public. So something has to establish, before the
# first push, that the target is private.
#
# The first attempt had this script create the repository through the API. That
# came back `403 access denied: insufficient scope`: DOCKERHUB_TOKEN may push
# and pull, and it may not manage repositories. Which is the right shape for
# that secret - a token that can create and delete repositories is not what a
# publish workflow needs, and widening it for a one-off measurement would be
# the wrong trade. So the repository is created by hand, once, and this checks.
#
# Usage:
#   DOCKERHUB_TOKEN=... bash scripts/ci/probe/dockerhub-repo.sh

set -euo pipefail

user="${DOCKERHUB_USER:-rndmjoker}"
name="${PROBE:-digest-probe}"

if [ -z "${DOCKERHUB_TOKEN:-}" ]; then
    echo "::error::DOCKERHUB_TOKEN is not set." >&2
    exit 1
fi

instructions() {
    cat >&2 <<TEXT
::error::Create it first, at https://hub.docker.com/repository/create
::error::  Namespace:  $user
::error::  Name:       $name
::error::  Visibility: Private
::error::Delete it again once this measurement is done. Nothing else uses it.
TEXT
}

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

response="$(
    curl -sS -w '\n%{http_code}' -H "Authorization: JWT $jwt" \
        "https://hub.docker.com/v2/repositories/$user/$name/"
)"
code="$(printf '%s' "$response" | tail -n1)"
body="$(printf '%s' "$response" | sed '$d')"

case "$code" in
    200) ;;
    404)
        echo "::error::$user/$name does not exist." >&2
        instructions
        exit 1
        ;;
    *)
        echo "::error::Asking about $user/$name returned HTTP $code:" >&2
        printf '%s\n' "$body" >&2
        exit 1
        ;;
esac

private="$(printf '%s' "$body" | jq -r '.is_private')"

if [ "$private" != "true" ]; then
    echo "::error::$user/$name exists but is public (is_private=$private). Refusing to push into it." >&2
    instructions
    exit 1
fi

echo "$user/$name exists and is private. Safe to push into."
