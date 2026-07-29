#!/usr/bin/env bash
#
# release-notes.sh — extract one version's section from CHANGELOG.md for the
# GitHub Release body, so `sky upgrade` shows rich notes (it prints the Release
# body verbatim). CHANGELOG.md is the single source of truth; the GitHub Release
# body is a copy of the matching section.
#
# Usage:
#   scripts/release-notes.sh v0.19.0            # print the section body to stdout
#   scripts/release-notes.sh v0.19.0 | gh release create v0.19.0 --notes-file -
#   gh release create v0.19.0 --notes-file <(scripts/release-notes.sh v0.19.0)
#
# Matching: the FIRST `## ` header whose text contains the given version string
# (so `## v0.19.0 — …` matches `v0.19.0`). Before tagging, set that header to the
# EXACT version being released so this is unambiguous. The `## ` header line
# itself is omitted from the output (the Release title already carries the tag).
#
# Exit non-zero (with a message on stderr) when no section matches — a release
# MUST NOT proceed with empty notes.
set -euo pipefail

VERSION="${1:?usage: release-notes.sh <version>   (e.g. v0.19.0)}"
CHANGELOG="$(cd "$(dirname "$0")/.." && pwd)/CHANGELOG.md"

if [ ! -f "$CHANGELOG" ]; then
    echo "release-notes: $CHANGELOG not found" >&2
    exit 1
fi

notes="$(
    awk -v ver="$VERSION" '
        /^## / {
            if (inblk) { exit }             # next section ends the block
            if (index($0, ver) > 0) { inblk = 1; next }   # skip the header line itself
        }
        inblk { print }
    ' "$CHANGELOG"
)"

# Trim leading + trailing blank lines (portable — no `tail -r`).
notes="$(
    printf '%s\n' "$notes" | awk '
        { line[NR] = $0 }
        END {
            first = 1; last = NR
            while (first <= NR && line[first] ~ /^[[:space:]]*$/) first++
            while (last >= first && line[last] ~ /^[[:space:]]*$/) last--
            for (i = first; i <= last; i++) print line[i]
        }'
)"

if [ -z "${notes//[[:space:]]/}" ]; then
    echo "release-notes: no CHANGELOG.md section matching '$VERSION' — add one before releasing." >&2
    exit 1
fi

printf '%s\n' "$notes"
