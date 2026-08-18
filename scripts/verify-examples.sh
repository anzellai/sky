#!/usr/bin/env bash
# scripts/verify-examples.sh — local end-to-end smoke for examples.
#
# Builds, runs, and screenshots each server example through a real
# Chromium via Playwright. The aim is to catch regressions where
# the example BUILDS clean but renders wrong (blank page, JS
# exception in __sky*, dead Cmd.perform dispatch). `sky verify`
# only probes / for HTTP 200 — the symptom would slip past.
#
# Usage:
#   scripts/verify-examples.sh                # every server example
#   scripts/verify-examples.sh 09 12 19       # name fragments (prefix-match)
#
# First run sets up _verify/node_modules with Playwright + Chromium.
# Both _verify/ and the harness output (_verify/<example>/) are
# gitignored — local per-developer; not source.

set -o pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# `require_fresh_compiler <bin>` — a screenshot of a page rendered by last
# week's compiler proves nothing about this week's. See the header of
# scripts/lib/fresh-compiler.sh.
source "$ROOT/scripts/lib/fresh-compiler.sh"

VDIR="$ROOT/_verify"
mkdir -p "$VDIR"

# Install dev-tooling Playwright at repo root (node_modules is
# gitignored). package.json lives at repo root so node's ES module
# resolution finds it without any NODE_PATH hackery — ES modules
# don't honour NODE_PATH.
if [[ ! -d "$ROOT/node_modules/playwright" ]]; then
    echo "[verify] one-time Playwright setup at repo root …"
    (cd "$ROOT" && npm install --silent)
    (cd "$ROOT" && npx playwright install chromium)
fi

# Was `[[ ! -x … ]]`, which answers "is there a compiler" and not "is it the
# one this run is supposed to be verifying". scripts/verify-examples.mjs uses
# the same path.
require_fresh_compiler "$ROOT/sky-out/sky" "$ROOT"

# Run from repo root so node finds ./node_modules/playwright.
node "$ROOT/scripts/verify-examples.mjs" "$@"
