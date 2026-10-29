#!/usr/bin/env bash
# scripts/build.sh — local single-command build.
#
# Produces:
#   sky-out/sky            — the Sky compiler (Rust) — the only
#                             artefact needed by end users; the
#                             sky-ffi-inspect helper is embedded and
#                             self-provisions into
#                             $XDG_CACHE_HOME/sky/tools/ on first use.
#   bin/sky-ffi-inspect    — local dev copy (optional). Contributors
#                             get an in-tree binary so FfiGen
#                             resolves without paying the first-use
#                             go-build cost each branch switch.
#
# Optional flags:
#   --sweep       run every example end-to-end after the build (takes ~2 min)
#   --clean       remove rust/target/, sky-out/, bin/ before building
#   --help        print this help
#
# Prerequisites (expected on PATH):
#   * cargo + rustc     — see rust/rust-toolchain.toml
#   * go     (1.25+)    — required at runtime by `sky build`

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# shellcheck source=lib/cargo-target.sh
source "$ROOT/scripts/lib/cargo-target.sh"

RUN_SWEEP=0
DO_CLEAN=0

for arg in "$@"; do
    case "$arg" in
        --sweep)      RUN_SWEEP=1 ;;
        --clean)      DO_CLEAN=1 ;;
        --help|-h)
            sed -n '2,22p' "$0"
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
command -v cargo >/dev/null || { echo "cargo not found on PATH"; exit 1; }
command -v go    >/dev/null || fail "go not found on PATH"

# ─── clean ──────────────────────────────────────────────────────────
if [[ $DO_CLEAN -eq 1 ]]; then
    say "cleaning rust/target/, sky-out/, bin/"
    rm -rf rust/target sky-out bin
fi

mkdir -p sky-out bin

# ─── build compiler ─────────────────────────────────────────────────
# The embedded runtime-go / sky-stdlib / sky-bundled trees are staged by
# rust/crates/ffi/build.rs, which cargo re-runs when any of them changes.

say "building sky compiler (cargo)"
# Ask cargo where it put the binary rather than assuming `rust/target/release`.
# See scripts/lib/cargo-target.sh for the incident: that assumption silently
# installed a pre-fix compiler, because cargo was honouring CARGO_TARGET_DIR and
# writing elsewhere while the `cp` kept succeeding on the older file.
( cd rust && cargo build --release --locked -p sky )
install_binary "$(cargo_bin_path "$ROOT/rust" sky --release)" "$ROOT/sky-out/sky" || exit 1
./sky-out/sky --version

# ─── build sky-ffi-inspect ──────────────────────────────────────────
# Optional — the helper's source is embedded into the sky binary and
# self-provisions at first use. We still build it in-tree so
# contributors avoid the first-use cost when switching branches.
say "building sky-ffi-inspect (go) — optional dev copy"
( cd tools/sky-ffi-inspect && go build -ldflags="-s -w" -o "$ROOT/bin/sky-ffi-inspect" . )
test -x bin/sky-ffi-inspect

# ─── optional: example sweep ────────────────────────────────────────
if [[ $RUN_SWEEP -eq 1 ]]; then
    say "sweeping examples/* (clean builds, no runtime)"
    pass=0; fail_count=0; fails=()
    for d in examples/*/; do
        # USER RULE (anzellai/sky#299): examples/76-ai-shell-command is excluded from
        # CI in general (no CI job builds, trains, evaluates or runs it), so it is
        # skipped here by name.
        case "$d" in examples/76-ai-shell-command/) continue ;; esac
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

# ─── post-build hygiene: keep go-build cache from growing without bound ───
# CLAUDE.md §6 — Sky compiler rebuilds + example sweeps accumulate multi-GB
# go-build entries that auto-prune doesn't catch on macOS.
#
# It used to wipe the whole cache whenever it passed 5 GB, on EVERY run. This
# script runs before almost every gate (it installs the compiler every gate
# measures), and a routine session's cache sits above 5 GB, so each run threw
# away the cache the next gate's `go build` / `go test` needed and paid ~1-2 min
# to repopulate it — on a machine with room to spare. Wipe now only when it
# matters: the cache is over 5 GB AND the disk has under 30 GB free (the policy
# example-sweep.sh already used), or the cache alone passes 20 GB.
#
# Cross-platform cache path detection — Linux uses $XDG_CACHE_HOME or
# ~/.cache/go-build; macOS uses ~/Library/Caches/go-build. Pre-fix,
# the hardcoded macOS path made `du` exit non-zero on Linux runners
# (path doesn't exist), and pipefail + set -e propagated that to a
# script-level exit 1 — even though the build passed cleanly (Linux
# CI failure 2026-06-04 on commit 549d4701).
go_cache_dir="${HOME}/Library/Caches/go-build"
[[ -d "$go_cache_dir" ]] || go_cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/go-build"
if [[ -d "$go_cache_dir" ]]; then
    cache_kb=$(du -sk "$go_cache_dir" 2>/dev/null | awk '{print $1}')
    cache_kb=${cache_kb:-0}
    free_kb=$(df -k / 2>/dev/null | awk 'NR==2 {print $4}')
    free_kb=${free_kb:-999999999}
    if [[ "$cache_kb" -gt 20971520 ]] || { [[ "$cache_kb" -gt 5242880 ]] && [[ "$free_kb" -lt 31457280 ]]; }; then
        cache_gb=$(( cache_kb / 1048576 ))
        say "go-build cache is ${cache_gb} GB with $(( free_kb / 1048576 )) GB free — running 'go clean -cache'"
        go clean -cache 2>/dev/null || true
    fi
fi

say "done. binaries:"
printf '  %s\n' "$ROOT/sky-out/sky" "$ROOT/bin/sky-ffi-inspect"
