#!/usr/bin/env bash
# scripts/build.sh — local single-command build.
#
# Produces:
#   sky-out/sky            — the Sky compiler (Haskell)
#   bin/sky-ffi-inspect    — the Go helper used by `sky add` for FFI inspection
#
# Optional flags:
#   --sweep       run every example end-to-end after the build (takes ~2 min)
#   --self-tests  run test-files/*.sky through `sky build`
#   --clean       remove dist-newstyle/, sky-out/, bin/ before building
#   --help        print this help
#
# Prerequisites (expected on PATH):
#   * cabal  (3.10+)    — https://www.haskell.org/ghcup/
#   * ghc    (9.4.8)    — pinned; other 9.4.x should work
#   * go     (1.21+)    — required at runtime by `sky build`

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

GHC_EXPECTED="9.4.8"

RUN_SWEEP=0
RUN_SELF_TESTS=0
DO_CLEAN=0

for arg in "$@"; do
    case "$arg" in
        --sweep)      RUN_SWEEP=1 ;;
        --self-tests) RUN_SELF_TESTS=1 ;;
        --clean)      DO_CLEAN=1 ;;
        --help|-h)
            sed -n '2,20p' "$0"
            exit 0
            ;;
        *)
            echo "unknown flag: $arg" >&2
            exit 2
            ;;
    esac
done

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ─── preflight ──────────────────────────────────────────────────────
command -v cabal >/dev/null || fail "cabal not found on PATH"
command -v ghc   >/dev/null || fail "ghc not found on PATH"
command -v go    >/dev/null || fail "go not found on PATH"

GHC_VER="$(ghc --numeric-version 2>/dev/null || echo '?')"
if [[ "$GHC_VER" != "$GHC_EXPECTED"* ]]; then
    say "warning: GHC is $GHC_VER, pinned to $GHC_EXPECTED (continuing)"
fi

# ─── clean ──────────────────────────────────────────────────────────
if [[ $DO_CLEAN -eq 1 ]]; then
    say "cleaning dist-newstyle/, sky-out/, bin/"
    rm -rf dist-newstyle sky-out bin
fi

mkdir -p sky-out bin

# ─── build compiler ─────────────────────────────────────────────────
say "building sky compiler (cabal)"
cabal update >/dev/null
cabal install exe:sky \
    --overwrite-policy=always \
    --install-method=copy \
    --installdir=sky-out

chmod +x sky-out/sky
./sky-out/sky --version

# ─── build sky-ffi-inspect ──────────────────────────────────────────
say "building sky-ffi-inspect (go)"
( cd tools/sky-ffi-inspect && go build -ldflags="-s -w" -o "$ROOT/bin/sky-ffi-inspect" . )
test -x bin/sky-ffi-inspect

# ─── optional: self-tests ──────────────────────────────────────────
if [[ $RUN_SELF_TESTS -eq 1 ]]; then
    say "running self-tests (test-files/*.sky)"
    pass=0; fail_count=0
    for f in test-files/*.sky; do
        rm -rf .skycache
        if ./sky-out/sky build "$f" 2>&1 | tail -1 | grep -q 'Build complete'; then
            pass=$((pass+1))
        else
            fail_count=$((fail_count+1))
            echo "  FAIL $f"
        fi
    done
    echo "self-tests: $pass passed, $fail_count failed"
    [[ "$fail_count" = "0" ]] || fail "self-tests failed"
fi

# ─── optional: example sweep ────────────────────────────────────────
if [[ $RUN_SWEEP -eq 1 ]]; then
    say "sweeping examples/* (clean builds, no runtime)"
    pass=0; fail_count=0; fails=()
    export SKY_RUNTIME_DIR="$ROOT/runtime-go"
    for d in examples/*/; do
        ( cd "$d" \
          && rm -rf sky-out .skycache \
          && "$ROOT/sky-out/sky" build src/Main.sky ) >/tmp/sky-build.log 2>&1 \
            && pass=$((pass+1)) \
            || { fail_count=$((fail_count+1)); fails+=("$(basename "$d")"); }
    done
    echo "examples: $pass passed, $fail_count failed"
    if [[ $fail_count -gt 0 ]]; then
        printf '  failures:%s\n' " ${fails[*]}"
        fail "example sweep failed"
    fi
fi

say "done. binaries:"
printf '  %s\n' "$ROOT/sky-out/sky" "$ROOT/bin/sky-ffi-inspect"
