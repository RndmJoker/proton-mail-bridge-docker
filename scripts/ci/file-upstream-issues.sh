#!/usr/bin/env bash
#
# File the two issues a new upstream bridge release calls for: one here, and
# one in the MCP server that talks to the same bridge.
#
# Both repositories use issue forms with different fields, so each body is
# written to match its own template rather than one text being pasted into
# both. An issue created through the API does not pass through the form, and
# one that ignores the shape of the form is immediately recognisable as
# machine-made in the wrong way.
#
# The changelog goes in verbatim, inside a code fence. That is not for looks:
#
#   - It is someone else's text arriving unread. As running markdown an @name
#     in it would notify a real person, and a #42 would write a cross-reference
#     into an unrelated issue in this repository.
#   - A fence also makes it obvious at a glance that the words are Proton's and
#     not a summary written here.
#
# The fence is made longer than the longest run of backticks in the body, so a
# changelog containing code blocks cannot break out of it.
#
# Environment:
#   GH_TOKEN         installation token of the bot, scoped to both repositories
#   UPSTREAM_TAG     the new upstream tag, for example v3.26.0
#   UPSTREAM_VERSION the same without the v
#   PINNED_TAG       what this repository is built from today
#   RELEASE_URL      the upstream release page
#   CHANGELOG_FILE   file holding the release body

set -euo pipefail

readonly BRIDGE_REPO="${BRIDGE_REPO:-RndmJoker/proton-mail-bridge-docker}"
readonly MCP_REPO="${MCP_REPO:-RndmJoker/proton-mcp}"
readonly UPSTREAM="${UPSTREAM:-ProtonMail/proton-bridge}"

for required in GH_TOKEN UPSTREAM_TAG UPSTREAM_VERSION PINNED_TAG RELEASE_URL CHANGELOG_FILE; do
    if [ -z "${!required:-}" ]; then
        echo "::error::$required is not set." >&2
        exit 1
    fi
done

if [ ! -f "$CHANGELOG_FILE" ]; then
    echo "::error::$CHANGELOG_FILE does not exist." >&2
    exit 1
fi

# GitHub rejects an issue body over 65536 characters. Losing the tail of a
# changelog is survivable; having the whole thing rejected is not, and the link
# to the full text is right there either way.
readonly CHANGELOG_LIMIT=40000

# --------------------------------------------------------------------------
# The changelog, fenced so that nothing in it is interpreted
# --------------------------------------------------------------------------

changelog="$(cat "$CHANGELOG_FILE")"

if [ -z "$changelog" ]; then
    changelog="(Proton published no release notes.)"
fi

if [ "${#changelog}" -gt "$CHANGELOG_LIMIT" ]; then
    changelog="${changelog:0:$CHANGELOG_LIMIT}"$'\n\n[truncated, see the release page]'
fi

longest_run="$(printf '%s' "$changelog" | grep -o '`\+' | awk '{ if (length($0) > m) m = length($0) } END { print m + 0 }')"
fence_length=3
if [ "$longest_run" -ge 3 ]; then
    fence_length=$((longest_run + 1))
fi
fence="$(printf '%*s' "$fence_length" '' | tr ' ' '`')"

fenced_changelog="${fence}
${changelog}
${fence}"

# --------------------------------------------------------------------------
# Only once per version
# --------------------------------------------------------------------------

# Searched over open and closed alike. An update that was already dealt with
# and closed must not come back the next morning.
# The search is a fuzzy one, so its result is filtered down to an exact title
# match afterwards. The title goes into jq through --arg rather than being
# pasted into the filter: it is built from an upstream tag, and a value from
# somewhere else does not belong in the text of an expression.
already_filed() {
    local repo="$1" title="$2" found
    found="$(gh issue list --repo "$repo" --state all --search "$title in:title" --json title \
        | jq --arg t "$title" '[.[] | select(.title == $t)] | length')"
    [ "$found" -gt 0 ]
}

file_issue() {
    local repo="$1" title="$2" labels="$3" body="$4"

    if already_filed "$repo" "$title"; then
        echo "  $repo: \"$title\" already exists, leaving it alone."
        return 0
    fi

    # Through a file, never as a command line argument: the body is multi-line
    # and full of characters the shell would otherwise get an opinion about.
    local body_file
    body_file="$(mktemp)"
    printf '%s\n' "$body" > "$body_file"

    gh issue create \
        --repo "$repo" \
        --title "$title" \
        --label "$labels" \
        --body-file "$body_file"

    rm -f "$body_file"
}

# --------------------------------------------------------------------------
# Here: the update itself
# --------------------------------------------------------------------------

# Matches .github/ISSUE_TEMPLATE/feature_request.yml: Description, Rationale.
read -r -d '' bridge_body <<EOF || true
## Feature request

### Description

Proton published [$UPSTREAM_TAG]($RELEASE_URL). This image is built from \`$PINNED_TAG\`.

Work on the update starts as soon as possible.

Work:

- [ ] Read the changelog below for changes to the gRPC interface
- [ ] Raise \`BRIDGE_TAG\`, \`BRIDGE_COMMIT\` and \`BRIDGE_VERSION\` in \`docker/bridge-version\`
- [ ] \`scripts/ci/check-proto.sh\` will fail if \`bridge.proto\` changed. That is the check working. Copy the new file in only after reading the difference
- [ ] Smoke test against the new build
- [ ] Sign in by hand once. No test here uses a real Proton account, so this is the only thing that exercises it

### Rationale

The container drives the bridge over the gRPC interface that release may have
changed. A renamed field does not fail the build; it turns into a call that
quietly does nothing, and the first sign of it is a mail client that stopped
working.

Nothing is rebuilt or published automatically for this. Publishing is a tag,
and a tag is a decision.

### Changelog, as published by Proton

$fenced_changelog

---

Filed by the upstream watcher in [$BRIDGE_REPO](https://github.com/$BRIDGE_REPO/blob/main/.github/workflows/upstream.yml), from [$UPSTREAM]($RELEASE_URL).
EOF

# --------------------------------------------------------------------------
# proton-mcp: whether it still fits
# --------------------------------------------------------------------------

# Matches that repository's enhancement form: Affected feature, Proposal,
# Current behaviour. Its fields are not the same as the ones above.
read -r -d '' mcp_body <<EOF || true
## Enhancement

### Affected feature

The connection to the Proton Mail Bridge: IMAP and SMTP, and the bridge
password they are reached with.

### Proposal

Check this server against Proton Bridge $UPSTREAM_VERSION, released as
[$UPSTREAM_TAG]($RELEASE_URL), and note anything that changed.

- [ ] Read the changelog below for anything touching IMAP, SMTP or the bridge password
- [ ] Run the test suite against a bridge on the new version
- [ ] Update the \`Bridge version\` example in \`.github/ISSUE_TEMPLATE/bug_report.yml\` if the version people are likely to report has moved on
- [ ] Say in the readme which bridge versions this was tried against

### Current behaviour

Built and tried against $PINNED_TAG. Whether $UPSTREAM_TAG changes anything
here is unknown until someone looks.

### Changelog, as published by Proton

$fenced_changelog

---

Filed by the upstream watcher in [$BRIDGE_REPO](https://github.com/$BRIDGE_REPO/blob/main/.github/workflows/upstream.yml), from [$UPSTREAM]($RELEASE_URL).
EOF

echo "Filing issues for $UPSTREAM_TAG:"
file_issue "$BRIDGE_REPO" "[Feature] Update to Proton Bridge $UPSTREAM_VERSION" "enhancement,priority: high" "$bridge_body"
file_issue "$MCP_REPO" "[Enhancement] Check against Proton Bridge $UPSTREAM_VERSION" "enhancement" "$mcp_body"
