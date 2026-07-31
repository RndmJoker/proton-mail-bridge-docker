#!/usr/bin/env bash
#
# May the publish workflow push anything?
#
# Answers from the PUBLISH file at the root of the repository, which carries
# the reasoning. This script only reads it, and refuses to guess when the
# answer is neither yes nor no: a typo there must not be read as permission.
#
# Writes `enabled` to $GITHUB_OUTPUT when running in Actions, and prints the
# same to standard output so it can be run by hand.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT
readonly PUBLISH_FILE="$REPO_ROOT/PUBLISH"

if [ ! -f "$PUBLISH_FILE" ]; then
    # A missing file is not permission. Deleting it must not be the way to
    # publish, or the guard is decoration.
    echo "::error::$PUBLISH_FILE is missing. Publishing stays off until it says otherwise."
    exit 1
fi

# Not a .sh file and deliberately so: it is read both by `source` here and by
# a person, and the reasoning lives in its comments.
# shellcheck disable=SC1090
source "$PUBLISH_FILE"

case "${PUBLISH:-}" in
    yes)
        enabled=true
        echo "PUBLISH=yes, so this run may push."
        ;;
    no)
        enabled=false
        echo "PUBLISH=no, so nothing will be pushed. See the PUBLISH file for why."
        ;;
    *)
        echo "::error::PUBLISH is '${PUBLISH:-<unset>}' in $PUBLISH_FILE. Only yes or no are answers."
        exit 1
        ;;
esac

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'enabled=%s\n' "$enabled" >> "$GITHUB_OUTPUT"
fi
