#!/usr/bin/env bash
#
# Tests for file-upstream-issues.sh, without GitHub.
#
# That script only runs on the day Proton publishes something, and its first
# real run is the one that files issues in two repositories. Until this test
# existed, the first time anything checked it was that run, and it failed.
#
# gh is replaced by a stand-in on PATH. It records every call, keeps the body
# of every issue it is asked to create, and answers `gh issue list` with
# whatever the case in front of it sets up. Nothing here reaches the network.
#
# Usage:
#   bash scripts/ci/test-file-upstream-issues.sh
#
# Environment:
#   FILE_UPSTREAM_ISSUES  the script under test (default: the one next to
#                         this file). Set it to check an older revision, for
#                         example to watch a test fail before its fix.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly SCRIPT="${FILE_UPSTREAM_ISSUES:-$SCRIPT_DIR/file-upstream-issues.sh}"

readonly BRIDGE_TITLE="[Feature] Update to Proton Bridge 9.9.9"
readonly MCP_TITLE="[Enhancement] Check against Proton Bridge 9.9.9"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# --------------------------------------------------------------------------
# The stand-in for gh
# --------------------------------------------------------------------------

mkdir -p "$work/bin"
cat > "$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "$STUB_DIR/calls"

case "${1:-} ${2:-}" in
    "issue list")
        if [ "${STUB_LIST_FAILS:-}" = 1 ]; then
            echo "HTTP 502: Bad Gateway" >&2
            exit 1
        fi
        printf '%s\n' "${STUB_LIST_ANSWER-[]}"
        ;;
    "issue create")
        creates=$(( $(cat "$STUB_DIR/creates" 2>/dev/null || echo 0) + 1 ))
        echo "$creates" > "$STUB_DIR/creates"
        while [ "$#" -gt 0 ]; do
            if [ "$1" = "--body-file" ]; then
                cp "$2" "$STUB_DIR/body-$creates"
            fi
            shift
        done
        echo "https://github.com/example/example/issues/$creates"
        ;;
    *)
        echo "unexpected call: gh $*" >&2
        exit 1
        ;;
esac
EOF
chmod +x "$work/bin/gh"

# --------------------------------------------------------------------------
# Running one case
# --------------------------------------------------------------------------

failures=0
case_name=""

ok()   { printf 'ok    %s: %s\n' "$case_name" "$1"; }
fail() { printf 'FAIL  %s: %s\n' "$case_name" "$1"; failures=$((failures + 1)); }

# run_case <name> <changelog text>
#
# Sets $status, $output and $stub for the checks that follow. STUB_LIST_ANSWER
# and STUB_LIST_FAILS reach the stand-in through the environment when they are
# set on the call.
run_case() {
    case_name="$1"
    stub="$(mktemp -d "$work/case.XXXXXX")"
    printf '%s' "$2" > "$stub/changelog.md"

    status=0
    output="$(
        PATH="$work/bin:$PATH" \
        STUB_DIR="$stub" \
        GH_TOKEN=test \
        UPSTREAM_TAG=v9.9.9 \
        UPSTREAM_VERSION=9.9.9 \
        PINNED_TAG=v9.9.8 \
        RELEASE_URL=https://github.com/ProtonMail/proton-bridge/releases/tag/v9.9.9 \
        CHANGELOG_FILE="$stub/changelog.md" \
        bash "$SCRIPT" 2>&1
    )" || status=$?
}

creates() { cat "$stub/creates" 2>/dev/null || echo 0; }

expect_status() {
    if [ "$status" -eq "$1" ]; then
        ok "exit status $status"
    else
        fail "exit status $status, expected $1. Output:"
        printf '%s\n' "$output" | sed 's/^/      /'
    fi
}

expect_creates() {
    local n
    n="$(creates)"
    if [ "$n" -eq "$1" ]; then
        ok "$n issue(s) created"
    else
        fail "$n issue(s) created, expected $1"
    fi
}

# expect_fence <body number> <length>
#
# The body must contain a line of exactly <length> backticks, the fence, and
# no longer one.
expect_fence() {
    local body="$stub/body-$1" length="$2" fence longer
    fence="$(printf '%*s' "$length" '' | tr ' ' '`')"
    longer="$fence\`"
    if grep -qsxF -- "$fence" "$body" && ! grep -qsF -- "$longer" "$body"; then
        ok "body $1 is fenced with $length backticks"
    else
        fail "body $1 is not fenced with exactly $length backticks"
    fi
}

expect_body_contains() {
    if grep -qsF -- "$2" "$stub/body-$1"; then
        ok "body $1 contains \"$2\""
    else
        fail "body $1 does not contain \"$2\""
    fi
}

# --------------------------------------------------------------------------
# Cases
# --------------------------------------------------------------------------

# The one that failed every day from 2026-08-27 on. No Proton release in the
# watcher's lifetime has had a backtick in its notes.
run_case "changelog without backticks" $'\n### Fixed\n* BRIDGE-000: A thing that was broken works again.'
expect_status 0
expect_creates 2
expect_fence 1 3
expect_fence 2 3
expect_body_contains 1 "BRIDGE-000: A thing that was broken works again."

run_case "empty changelog" ""
expect_status 0
expect_creates 2
expect_body_contains 1 "(Proton published no release notes.)"
expect_fence 1 3

run_case "changelog with a code block" $'Run this:\n```\nbridge --cli\n```'
expect_status 0
expect_creates 2
expect_fence 1 4
expect_fence 2 4

run_case "changelog with a run of five backticks" $'odd `````formatting````` here'
expect_status 0
expect_creates 2
expect_fence 1 6

run_case "changelog over the limit" "$(head -c 50000 /dev/zero | tr '\0' 'a')"
expect_status 0
expect_creates 2
expect_body_contains 1 "[truncated, see the release page]"

STUB_LIST_ANSWER="[{\"title\":\"$BRIDGE_TITLE\"},{\"title\":\"$MCP_TITLE\"}]" \
    run_case "both issues filed before" "nothing new"
expect_status 0
expect_creates 0

# The search is fuzzy. A title that only resembles the one wanted is somebody
# else's issue and must not count.
STUB_LIST_ANSWER='[{"title":"[Feature] Update to Proton Bridge 9.9.99"}]' \
    run_case "only a similar title exists" "nothing new"
expect_status 0
expect_creates 2

# --------------------------------------------------------------------------

echo
if [ "$failures" -gt 0 ]; then
    echo "$failures check(s) failed."
    exit 1
fi
echo "All checks passed."
