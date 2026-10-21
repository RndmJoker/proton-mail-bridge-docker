#!/usr/bin/env bash
#
# Which Go the workflows set up: the minor release go.mod names, such as 1.26,
# for setup-go to resolve to its newest patch.
#
# setup-go can read go.mod by itself, but it installs exactly the version
# written there. Since Go 1.21 that is a patch release as soon as any
# dependency declares one: golang.org/x/net 0.60.0 and later say go 1.26.0, so
# this module does too. Given that, setup-go installs Go 1.26.0, whose standard
# library lacks every fix since, and govulncheck rightly fails on them.
#
# The go directive is the oldest Go the module accepts, not the one to build
# and scan it with. While go.mod still said `go 1.26`, setup-go treated that as
# "any 1.26" and took the newest it had, and nothing in this repository pinned a
# patch release. This keeps it that way. The setup-go steps add check-latest,
# because the runner's own cache lags: on 2026-10-21 it held 1.26.9, and
# GO-2026-6809 in crypto/x509, which govulncheck finds reachable from
# internal/certinfo, is fixed only in 1.26.10.
#
# Usage:
#   bash scripts/ci/go-version.sh
#
# Environment:
#   GO_MOD  the file to read (default: go.mod at the repository root)
#
# Writes to $GITHUB_OUTPUT when running in Actions:
#   minor  for example 1.26

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT
readonly GO_MOD="${GO_MOD:-$REPO_ROOT/go.mod}"

# `go 1.26`, `go 1.26.0` and `go 1.26rc1` all name the minor release 1.26.
minor="$(sed -n -E 's/^go ([0-9]+\.[0-9]+)([^0-9].*)?$/\1/p' "$GO_MOD")"

if ! [[ "$minor" =~ ^[0-9]+\.[0-9]+$ ]]; then
    echo "::error::Could not read a Go release from the go directive in $GO_MOD." >&2
    exit 1
fi

echo "go.mod names Go $minor. Setting up its newest patch release."

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'minor=%s\n' "$minor" >> "$GITHUB_OUTPUT"
fi
