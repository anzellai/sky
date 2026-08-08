#!/usr/bin/env bash
#
# build-docs-site.sh — assemble the STATIC docs site for GitHub Pages.
#
# The site is a build artifact of the compiler + stdlib, NOT hand-maintained:
#   * API reference  — `sky doc --export` renders one page per stdlib module +
#     a searchable index + api/symbols.json, straight from the stdlib .sky
#     source. Add/remove/change a lib → the API pages change on next build.
#   * The server-oriented links `sky doc` emits (`/m/<mod>`, `/api/…`) are
#     rewritten here to STATIC-RELATIVE (`m/<mod>.html`, `api/…`) so the site
#     works on any base path (github.io/<repo>, a custom domain, or file://).
#
# Prose walkthroughs + example galleries are layered on top of this in a later
# pass (they need a Markdown→HTML step); for now the site is the auto-generated,
# always-current API reference + a landing page.
#
# Usage:  scripts/build-docs-site.sh [out_dir]      (default: ./_site)
# Env:    SKY_BIN=/path/to/sky   (default: sky-out/sky, else `sky` on PATH)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT/_site}"
SKY="${SKY_BIN:-$ROOT/sky-out/sky}"
[ -x "$SKY" ] || SKY="$(command -v sky 2>/dev/null || true)"
[ -n "${SKY:-}" ] && [ -x "$SKY" ] || { echo "build-docs-site: no sky binary (set SKY_BIN)"; exit 2; }

echo "==> render API doc-site from stdlib source"
rm -rf "$OUT"; mkdir -p "$OUT"
( cd "$ROOT" && "$SKY" doc --export "$OUT" ) >/dev/null

echo "==> static-ify links (server routes → relative static files)"
# index.html: /m/<mod>  ->  m/<mod>.html   ;  /api/  ->  api/
perl -i -pe 's{href="/m/([^"]+)"}{href="m/$1.html"}g; s{/api/}{api/}g;' "$OUT/index.html"
# per-module pages live in m/, so root-relative refs need a `../` hop.
find "$OUT/m" -name '*.html' -print0 | while IFS= read -r -d '' f; do
  perl -i -pe 's{href="/"}{href="../index.html"}g; s{href="/m/([^"]+)"}{href="$1.html"}g; s{/api/}{../api/}g;' "$f"
done

echo "==> nav + landing niceties (.nojekyll, cross-links API ↔ Guides)"
touch "$OUT/.nojekyll"   # serve dotfiles + skip Jekyll on GitHub Pages
# API index: a banner + a nav bar linking to the Guides + GitHub.
perl -i -pe 's{<h1>Sky API documentation</h1>}{<p style="margin:0 0 .6rem;font-size:.95rem"><a href="guide/index.html">Guides</a> · <a href="index.html"><b>API reference</b></a> · <a href="https://github.com/anzellai/sky">GitHub</a></p><h1>Sky API documentation</h1><p style="color:#666;margin:.2em 0 1em">Every stdlib module, generated from source on each build — always current. See the <a href="guide/index.html">Guides</a> for walkthroughs, or run <code>sky doc &lt;Module&gt;</code> locally.</p>}' "$OUT/index.html"
# Module pages: swap the bare "all modules" back-link for a small nav bar.
find "$OUT/m" -name '*.html' -print0 | while IFS= read -r -d '' f; do
  perl -i -pe 's{<p><a href="\.\./index\.html">&larr; all modules</a></p>}{<nav style="font-size:.9rem;margin-bottom:1rem"><a href="../index.html">&larr; API reference</a> · <a href="../guide/index.html">Guides</a></nav>}' "$f"
done

count=$(find "$OUT/m" -name '*.html' | wc -l | tr -d ' ')
guides=$(( $(find "$OUT/guide" -name '*.html' 2>/dev/null | wc -l | tr -d ' ') - 1 ))
echo "------------------------------------------------------------"
echo "docs site built at $OUT/  ($count API module pages + $guides guides + searchable index)"
