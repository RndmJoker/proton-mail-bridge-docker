#!/usr/bin/env bash
#
# Run govulncheck and report what is reachable.
#
#   bash scripts/ci/govulncheck.sh source
#   bash scripts/ci/govulncheck.sh binary /usr/local/bin/bridge
#
# The source scan covers what this repository declares in go.mod. The binary
# scan covers whatever went into a compiled program, which for the bridge means
# Proton's entire dependency tree - far larger than ours, and the code that
# actually touches a mailbox. Nothing else in this project looks at it.
#
# Set ADVISORY=1 to report rather than fail. Used for the binary scan, see
# below.
#
# Writes a summary to $GITHUB_STEP_SUMMARY when running in Actions.

set -euo pipefail

# Pinned, like shellcheck, hadolint and gitleaks. `@latest` would mean the
# check changes under us: a run that passed yesterday failing today, with no
# commit in between and nothing saying which of the two moved.
readonly GOVULNCHECK_VERSION="v1.6.0"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT

mode="${1:-}"
target="${2:-}"

case "$mode" in
    source) ;;
    binary)
        if [ -z "$target" ]; then
            echo "::error::binary mode needs a path to scan." >&2
            exit 1
        fi
        if [ ! -f "$target" ]; then
            echo "::error::$target does not exist." >&2
            exit 1
        fi
        ;;
    *)
        echo "Usage: $0 source | binary <path>" >&2
        exit 1
        ;;
esac

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

# `-format json` so that govulncheck exits 0 whatever it finds. A non-zero
# exit then means the tool failed, which is a different thing and has to look
# different. The verdict is govulncheck-report.py's job.
if [ "$mode" = "source" ]; then
    go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
        -format json ./... > "$out"
else
    go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
        -format json -mode binary "$target" > "$out"
fi

report="$(mktemp)"
trap 'rm -f "$out" "$report"' EXIT

# In binary mode govulncheck reports no Go version, and its own would be the
# wrong one to print anyway: which standard library findings apply is decided
# by the toolchain that built the binary, which is recorded inside it.
scanned_go=""
if [ "$mode" = "binary" ]; then
    # Read whole, then pick. Not `| head -1 |`: head closes the pipe as soon as
    # it has its line, the writer takes a SIGPIPE, and `set -o pipefail` turns
    # that into exit 141. Whether it happens at all depends on which finishes
    # first, so it fails intermittently, which is worse than failing.
    version_output="$(go version -m "$target" 2>/dev/null || true)"

    # First line is `<path>: go1.26.5`, so the last field is the version. Taken
    # positionally rather than by matching the path, which carries a trailing
    # colon and may contain spaces.
    scanned_go="$(printf '%s\n' "$version_output" | awk 'NR == 1 { print $NF }')"
fi

set +e
SCANNED_GO_VERSION="$scanned_go" \
    python3 "$REPO_ROOT/scripts/ci/govulncheck-report.py" < "$out" > "$report"
verdict=$?
set -e

cat "$report"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        if [ "$mode" = "source" ]; then
            echo "### Vulnerabilities: this repository's dependencies"
        else
            echo "### Vulnerabilities in \`$(basename "$target")\`"
            echo ""
            echo "Everything compiled into the binary, which for the bridge is"
            echo "Proton's dependency tree rather than ours."
        fi
        echo ""
        echo '```'
        cat "$report"
        echo '```'
    } >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$verdict" -eq 2 ]; then
    exit 2
fi

if [ "$verdict" -ne 0 ] && [ "${ADVISORY:-0}" = "1" ]; then
    # Reported rather than failed, and only where a red build would be a lie
    # about who can act on it.
    #
    # The binary scan reads Proton's dependencies. The only responses available
    # here are to raise the pinned upstream commit or to wait for Proton, so a
    # blocked merge would not be a call to action - it would be a red mark that
    # stays red through no fault of the change in front of it, and those get
    # clicked past. The source scan has no such excuse and does fail.
    echo "::warning::Reachable vulnerabilities in $target. Nothing here can patch them; raising the pinned upstream commit is the lever. See the job summary."
    exit 0
fi

exit "$verdict"
