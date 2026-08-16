#!/usr/bin/env bash
#
# build-docs-site.sh — assemble the STATIC docs site for GitHub Pages.
#
# The site is a build artifact of the compiler + stdlib + docs/, NOT
# hand-maintained. `sky doc --export` renders it all:
#   * index.html      — hand-written landing (what Sky is + the three doors).
#   * reference.html  — API reference: one page per stdlib module (under m/) +
#     a searchable index + api/symbols.json, straight from the .sky source.
#   * learn/*.html    — the "Learn Sky" tour (docs/learn/, ordered curriculum
#     with a sidebar + prev/next).
#   * guide/*.html    — the curated prose guides + compiler internals (docs/,
#     excluding history / roadmaps / legacy).
# Add/rename/change a lib → the API pages change; edit a doc → the guides/tour
# change. The nav bar is baked into every page by the renderer, and `sky doc
# --export` already emits relative links (`m/<mod>.html`, `api/…`) so the site
# works on any base path (github.io/<repo>, a custom domain, or file://) with no
# post-processing. This script just runs the export and marks it for Pages.
#
# Usage:  scripts/build-docs-site.sh [out_dir]      (default: ./_site)
# Env:    SKY_BIN=/path/to/sky   (default: sky-out/sky, else `sky` on PATH)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT/_site}"
# `require_fresh_compiler <bin>` — `sky doc --export` renders the API from the
# compiler's own embedded stdlib, so a stale binary publishes a doc site for a
# stdlib that is no longer in the tree. See scripts/lib/fresh-compiler.sh.
source "$ROOT/scripts/lib/fresh-compiler.sh"

SKY="${SKY_BIN:-$ROOT/sky-out/sky}"
[ -x "$SKY" ] || SKY="$(command -v sky 2>/dev/null || true)"
require_fresh_compiler "${SKY:-}" "$ROOT"

echo "==> render doc-site (landing + reference + tour + guides) from source"
rm -rf "$OUT"; mkdir -p "$OUT"
( cd "$ROOT" && "$SKY" doc --export "$OUT" ) >/dev/null

touch "$OUT/.nojekyll"   # serve dotfiles + skip Jekyll on GitHub Pages

modules=$(find "$OUT/m" -name '*.html' | wc -l | tr -d ' ')
guides=$(( $(find "$OUT/guide" -name '*.html' 2>/dev/null | wc -l | tr -d ' ') - 1 ))
lessons=$(find "$OUT/learn" -name '*.html' 2>/dev/null | wc -l | tr -d ' ')
echo "------------------------------------------------------------"
echo "docs site built at $OUT/"
echo "  landing + reference ($modules modules) + $lessons-lesson tour + $guides guides"
