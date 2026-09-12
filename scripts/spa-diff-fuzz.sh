#!/usr/bin/env bash
# Sky.Spa differential split fuzzer — run it against a project.
#
# Generates the differential-fuzzer harness for a Sky.Spa (or Std.App web:app)
# project, builds it, and runs it OFFLINE. The harness runs each checkable server
# branch two ways over random (Model, Msg) — the direct `update` vs the Sky.Spa
# split plumbing (build request -> reconstruct -> update -> write-set -> apply
# delta) — and exits non-zero on a divergence. A divergence is a dropped
# read/write-set field or a Msg-arg/Model-field collision: the silent-wrong-answer
# class that shipped to darraghstudio (region switch, basket shipping) and that a
# plain `sky build` cannot catch. See docs/design/auto-testing.md (mode A).
#
# Usage:
#   scripts/spa-diff-fuzz.sh <path/to/project-or-entry.sky> [--iters N] [--seed S]
#
# The in-repo CI gate is `xtask harness --only spa-diff-fuzz` (bodies::spa_diff_fuzz),
# which fuzzes the in-repo fixtures in-process; THIS script is the runner for a
# user's own app (e.g. darraghstudio), which lives outside the compiler tree.
#
# It runs OFFLINE by design — the checkable branches are effect-free, so no DB,
# no credentials, and no network are needed (a branch that forces an effect at
# update time is fenced out and deferred to the scenario harness).
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "$REPO_ROOT/scripts/lib/with-timeout.sh"
source "$REPO_ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$REPO_ROOT/sky-out/sky" "$REPO_ROOT"
SKY="$REPO_ROOT/sky-out/sky"

ITERS=200
SEED=20260912
ENTRY=""
while [ $# -gt 0 ]; do
    case "$1" in
        --iters) [ $# -ge 2 ] || { echo "spa-diff-fuzz: --iters requires a number" >&2; exit 2; }; ITERS="$2"; shift 2 ;;
        --seed)  [ $# -ge 2 ] || { echo "spa-diff-fuzz: --seed requires a number" >&2; exit 2; }; SEED="$2"; shift 2 ;;
        -*) echo "spa-diff-fuzz: unknown option $1" >&2; exit 2 ;;
        *) ENTRY="$1"; shift ;;
    esac
done
[ -n "$ENTRY" ] || { echo "usage: scripts/spa-diff-fuzz.sh <project-or-entry.sky> [--iters N] [--seed S]" >&2; exit 2; }

# A non-dot output dir: `sky build`'s module discovery skips a path with a
# dot-directory segment, so the harness must NOT be generated under a `.`-dir.
OUT="$(mktemp -d "${TMPDIR:-/tmp}/sky-spa-diff-fuzz.XXXXXX")"
trap 'rm -rf "$OUT"' EXIT

echo "spa-diff-fuzz: generating harness for $ENTRY -> $OUT"
if ! with_timeout 300 "$SKY" spa-diff-fuzz "$ENTRY" --out "$OUT" --iters "$ITERS" --seed "$SEED"; then
    echo "spa-diff-fuzz: generation failed" >&2
    exit 1
fi

echo "spa-diff-fuzz: building the harness"
if ! ( cd "$OUT" && with_timeout 600 "$SKY" build src/Main.sky ); then
    echo "spa-diff-fuzz: harness build failed" >&2
    exit 1
fi

echo "spa-diff-fuzz: running the harness (offline)"
# Unset DATABASE_URL so a divergence cannot be masked by a live DB; the checkable
# branches never touch it.
if ( cd "$OUT" && DATABASE_URL= with_timeout 120 ./sky-out/app ); then
    echo "spa-diff-fuzz: PASS (no split-vs-direct divergence)"
    exit 0
else
    echo "spa-diff-fuzz: FAIL — a divergence was found (see the line above)" >&2
    exit 1
fi
