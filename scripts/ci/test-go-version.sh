#!/usr/bin/env bash
#
# Tests for go-version.sh: which minor release it reads from a go directive,
# and that it refuses rather than guesses when there is none.
#
# Usage:
#   bash scripts/ci/test-go-version.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly SCRIPT="$SCRIPT_DIR/go-version.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0

# check <name> <go.mod content> <expected minor, or "error">
check() {
    local name="$1" content="$2" expected="$3" status=0 got
    printf '%s\n' "$content" > "$work/go.mod"
    : > "$work/output"

    GO_MOD="$work/go.mod" GITHUB_OUTPUT="$work/output" bash "$SCRIPT" > /dev/null 2>&1 || status=$?
    got="$(sed -n 's/^minor=//p' "$work/output")"

    if [ "$expected" = "error" ]; then
        if [ "$status" -ne 0 ] && [ -z "$got" ]; then
            printf 'ok    %s: refused\n' "$name"
        else
            printf 'FAIL  %s: exit %s, minor "%s", expected a refusal\n' "$name" "$status" "$got"
            failures=$((failures + 1))
        fi
    elif [ "$status" -eq 0 ] && [ "$got" = "$expected" ]; then
        printf 'ok    %s: %s\n' "$name" "$got"
    else
        printf 'FAIL  %s: exit %s, minor "%s", expected "%s"\n' "$name" "$status" "$got" "$expected"
        failures=$((failures + 1))
    fi
}

readonly HEAD=$'module example.com/m\n'

check "minor release only"        "${HEAD}go 1.26"                           1.26
check "patch release"             "${HEAD}go 1.26.0"                         1.26
check "later patch release"       "${HEAD}go 1.27.3"                         1.27
check "release candidate"         "${HEAD}go 1.26rc1"                        1.26
check "toolchain line is ignored" "${HEAD}go 1.26.0"$'\n'"toolchain go1.27.1" 1.26
check "three-digit minor"         "${HEAD}go 1.100.2"                        1.100
check "trailing comment"          "${HEAD}go 1.26.0 // why"                  1.26
check "no go directive"           "${HEAD}"                                  error
check "only a major version"      "${HEAD}go 1"                              error

# The real one, so a go.mod this script cannot read fails here first.
status=0
GO_MOD="$SCRIPT_DIR/../../go.mod" GITHUB_OUTPUT="$work/output" bash "$SCRIPT" > /dev/null 2>&1 || status=$?
if [ "$status" -eq 0 ]; then
    printf 'ok    this repository'"'"'s go.mod: readable\n'
else
    printf 'FAIL  this repository'"'"'s go.mod: exit %s\n' "$status"
    failures=$((failures + 1))
fi

echo
if [ "$failures" -gt 0 ]; then
    echo "$failures check(s) failed."
    exit 1
fi
echo "All checks passed."
