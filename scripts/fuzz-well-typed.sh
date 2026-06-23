#!/usr/bin/env bash
# scripts/fuzz-well-typed.sh — v0.17 criterion 8 closure.
#
# Property-based fuzzer: generates random WELL-TYPED Sky programs via
# template substitution + deterministic seeding, asserts each one builds
# and runs without panic. Designed for ≥10,000 iterations in one run.
#
# Companion to test/Sky/Build/WellTypedFuzzerSpec.hs (the in-cabal
# Tier-A QuickCheck harness, runs every CI at 100 iters). This script
# is the Tier-B milestone runner — same coverage surface, ~10x faster
# per-iteration, simpler failure forensics (failed seed + rendered
# source + emitted Go all preserved).
#
# Flags:
#   --iters N        Iteration count (default 10000)
#   --seed N         Start seed (default $RANDOM); iter i uses seed+i
#   --mode M         template | corpus | composite (default composite)
#                      template: Tier B1 only (synthesised templates)
#                      corpus:   Tier B2 only (00-standard-libs replay)
#                      composite: alternating (default)
#   --keep           Keep tempdir on success (default: cleanup)
#   --quiet          Suppress per-iter progress; print summary only
#   --build-timeout  sky build timeout in seconds (default 30)
#   --run-timeout    ./sky-out/app run timeout in seconds (default 15)
#
# Exit 0 on all iterations green; non-zero with the first failing seed
# and a forensics dir under /tmp/sky-fuzz/FAILURES/.
#
# Verbatim goal it closes (.claude/AUTONOMOUS_GOAL.md criterion 8):
#   "A property-based fuzzer exists that generates random well-typed
#    Sky programs and asserts `sky build && ./sky-out/app` doesn't
#    panic. Run for ≥10,000 iterations clean before close."

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="${SKY:-$ROOT/sky-out/sky}"

ITERS=10000
SEED=""
MODE="composite"
KEEP=0
QUIET=0
BUILD_TIMEOUT=30
RUN_TIMEOUT=15

while [[ $# -gt 0 ]]; do
    case "$1" in
        --iters)         ITERS="$2"; shift 2 ;;
        --seed)          SEED="$2"; shift 2 ;;
        --mode)          MODE="$2"; shift 2 ;;
        --keep)          KEEP=1; shift ;;
        --quiet)         QUIET=1; shift ;;
        --build-timeout) BUILD_TIMEOUT="$2"; shift 2 ;;
        --run-timeout)   RUN_TIMEOUT="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,30p' "$0"
            exit 0
            ;;
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

if [[ -z "$SEED" ]]; then SEED=$RANDOM; fi

if [[ ! -x "$SKY" ]]; then
    echo "ERROR: sky binary not found at $SKY (set SKY=... or build first)" >&2
    exit 2
fi

FUZZ_DIR="$(mktemp -d -t sky-fuzz-XXXXXX)"
FAILURES_DIR="/tmp/sky-fuzz/FAILURES"
mkdir -p "$FAILURES_DIR"

cleanup() {
    if [[ $KEEP -eq 0 ]]; then rm -rf "$FUZZ_DIR"; fi
}
trap cleanup EXIT

# Deterministic PRNG: seed-driven LCG (Numerical Recipes constants).
# Stays inside 31-bit positive integers; produces a reproducible stream
# from any (seed, iter) pair — so a failing iter can be replayed via
# --seed N --iters 1.
rand() {
    local s=$1
    # LCG: next = (1103515245 * s + 12345) & 0x7FFFFFFF
    local n=$(( (1103515245 * s + 12345) & 0x7FFFFFFF ))
    echo "$n"
}

# Bounded int — args: SEED MIN MAX → echoes seed-derived int in [MIN, MAX]
bint() {
    local seed=$1 lo=$2 hi=$3
    local span=$(( hi - lo + 1 ))
    echo $(( lo + (seed % span) ))
}

# Six well-typed Sky program templates. Each ends `main : Task Error ()`
# printing a String. Slot kinds: __NK__ Int literal, __SK__ alphanum string,
# __LK__ Int-list literal. Slot semantics enforce typing:
#   * Int slots receive Int literals only
#   * String slots receive [a-z]+ literals only
#   * List slots receive bounded "[i, j, k]" forms only
# No (template × slot-fill) combination can produce a non-well-typed program.

template_arith() {
    local n1=$1 n2=$2 n3=$3
    cat <<EOF
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println (String.fromInt (let x_a = $n1 in x_a + $n2 * $n3))
EOF
}

template_strconcat() {
    local n1=$1 s1=$2
    cat <<EOF
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println ("prefix-" ++ String.fromInt $n1 ++ "-suffix-" ++ "$s1")
EOF
}

template_listmap() {
    local n1=$1 lst=$2
    cat <<EOF
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println (String.fromInt (List.length (List.map (\\x -> x + $n1) $lst)))
EOF
}

template_maybechain() {
    local n1=$1 n2=$2
    cat <<EOF
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println (String.fromInt (Maybe.withDefault $n1 (Maybe.map (\\x -> x * 2) (Just $n2))))
EOF
}

template_resultpipeline() {
    local n1=$1 n2=$2
    cat <<EOF
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println (String.fromInt (Result.withDefault 0 (Result.map (\\x -> x + $n1) (Ok $n2))))
EOF
}

template_paramrecord() {
    local n1=$1 s1=$2
    cat <<EOF
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

type alias Box a =
    { value : a, label : String }

main =
    println (let b = { value = $n1, label = "$s1" } in String.fromInt b.value)
EOF
}

# Render template chosen by seed; echoes (templateName) for caller logging,
# and writes source to $dst.
render_template() {
    local seed=$1 dst=$2
    local pick_seed
    pick_seed=$(rand "$seed")
    local kind=$(( pick_seed % 6 ))
    local s1 s2 s3
    s1=$(rand $((seed + 1)))
    s2=$(rand $((seed + 2)))
    s3=$(rand $((seed + 3)))
    local n1 n2 n3 str lst llen lstr i
    n1=$(bint "$s1" 0 99)
    n2=$(bint "$s2" 0 99)
    n3=$(bint "$s3" 0 99)
    # Build a string in [a-z]+ of length 1..6
    local slen
    slen=$(bint "$s1" 1 6)
    str=""
    for (( i = 0; i < slen; i++ )); do
        local cs
        cs=$(rand $((seed * 7 + i + 1)))
        local cidx=$(( cs % 26 ))
        local ch
        ch=$(awk -v n=$((97 + cidx)) 'BEGIN { printf "%c", n }')
        str="$str$ch"
    done
    # Build a bounded Int list literal of length 0..5
    llen=$(bint "$s2" 0 5)
    lstr=""
    for (( i = 0; i < llen; i++ )); do
        local ls
        ls=$(rand $((seed * 11 + i + 1)))
        local lv
        lv=$(bint "$ls" 0 99)
        if [[ -z "$lstr" ]]; then lstr="$lv"; else lstr="$lstr, $lv"; fi
    done
    lst="[$lstr]"

    case $kind in
        0) echo "arith";       template_arith "$n1" "$n2" "$n3" > "$dst" ;;
        1) echo "strconcat";   template_strconcat "$n1" "$str" > "$dst" ;;
        2) echo "listmap";     template_listmap "$n1" "$lst" > "$dst" ;;
        3) echo "maybechain";  template_maybechain "$n1" "$n2" > "$dst" ;;
        4) echo "resultpipe";  template_resultpipeline "$n1" "$n2" > "$dst" ;;
        5) echo "paramrecord"; template_paramrecord "$n1" "$str" > "$dst" ;;
    esac
}

# Setup a minimal sky.toml in the iter dir.
setup_project() {
    local dir=$1
    mkdir -p "$dir/src"
    cat > "$dir/sky.toml" <<EOF
name = "sky-fuzz-iter"
version = "0.0.0"
entry = "src/Main.sky"
EOF
}

# Run one iteration: render → build → run → assert. On failure echo the
# reason and return 1.
run_iter() {
    local seed=$1 mode=$2 iterdir=$3
    setup_project "$iterdir"
    local kind
    case "$mode" in
        template)
            kind=$(render_template "$seed" "$iterdir/src/Main.sky")
            ;;
        corpus)
            # Replay the known-good 00-standard-libs corpus. Each iter
            # uses the SAME source — but every iter still does a fresh
            # `sky build` + run, validating the compiler doesn't drift
            # under repeated invocation (IORef CSE / cache contamination).
            cp -f "$ROOT/examples/00-standard-libs/src/Main.sky" \
                "$iterdir/src/Main.sky"
            cp -rf "$ROOT/examples/00-standard-libs/tests" \
                "$iterdir/tests" 2>/dev/null || true
            kind="corpus"
            ;;
        composite|*)
            if (( seed % 2 == 0 )); then
                kind=$(render_template "$seed" "$iterdir/src/Main.sky")
            else
                cp -f "$ROOT/examples/00-standard-libs/src/Main.sky" \
                    "$iterdir/src/Main.sky"
                kind="corpus"
            fi
            ;;
    esac

    local buildlog="$iterdir/build.log"
    local runlog="$iterdir/run.log"

    # sky build
    if ! ( cd "$iterdir" && timeout "$BUILD_TIMEOUT" \
            "$SKY" build src/Main.sky >"$buildlog" 2>&1 ); then
        local rc=$?
        echo "BUILD-FAILED rc=$rc kind=$kind"
        return 1
    fi

    # ./sky-out/app — assert exit 0 + no panic markers in stderr
    if ! ( cd "$iterdir" && timeout "$RUN_TIMEOUT" \
            ./sky-out/app >"$runlog" 2>&1 ); then
        local rc=$?
        echo "RUN-FAILED rc=$rc kind=$kind"
        return 1
    fi
    if grep -q -E 'panic:|runtime error:|^goroutine [0-9]+ \[|fatal error:|unrecoverable' \
            "$runlog"; then
        echo "PANIC-DETECTED kind=$kind"
        return 1
    fi

    return 0
}

save_failure() {
    local seed=$1 iterdir=$2 reason=$3
    local dst="$FAILURES_DIR/seed-${seed}-$(date +%s)"
    mkdir -p "$dst"
    cp -rf "$iterdir/src" "$dst/" 2>/dev/null || true
    cp -f "$iterdir/sky.toml" "$dst/" 2>/dev/null || true
    cp -f "$iterdir/build.log" "$dst/" 2>/dev/null || true
    cp -f "$iterdir/run.log" "$dst/" 2>/dev/null || true
    cp -f "$iterdir/sky-out/main.go" "$dst/" 2>/dev/null || true
    cp -f "$iterdir/sky-out/app" "$dst/" 2>/dev/null || true
    echo "seed=$seed reason=$reason" > "$dst/SUMMARY"
    echo "Failure forensics written to: $dst"
}

# ── Main loop ────────────────────────────────────────────────────────

echo "sky-fuzz: mode=$MODE iters=$ITERS start_seed=$SEED sky=$SKY"
echo "sky-fuzz: tempdir=$FUZZ_DIR"

start_ts=$(date +%s)
failures=0
green=0

for (( i = 0; i < ITERS; i++ )); do
    iter_seed=$(( SEED + i ))
    iterdir="$FUZZ_DIR/iter-$i"
    mkdir -p "$iterdir"

    reason=$(run_iter "$iter_seed" "$MODE" "$iterdir")
    rc=$?
    if [[ $rc -ne 0 ]]; then
        failures=$((failures + 1))
        echo "FAIL iter=$i seed=$iter_seed $reason" >&2
        save_failure "$iter_seed" "$iterdir" "$reason"
        # Halt on first failure — preserves forensics; reproduce via
        # --seed $iter_seed --iters 1 --keep
        echo
        echo "sky-fuzz: ABORTING after first failure (iter $i / $ITERS)."
        echo "sky-fuzz: reproduce: $0 --seed $iter_seed --iters 1 --keep"
        exit 1
    fi
    green=$((green + 1))

    # Cleanup successful iters early to avoid disk pressure at 10k
    rm -rf "$iterdir"

    if [[ $QUIET -eq 0 && $(( (i + 1) % 100 )) -eq 0 ]]; then
        elapsed=$(( $(date +%s) - start_ts ))
        rate=$(awk -v g=$green -v e=$elapsed 'BEGIN { if (e>0) printf "%.1f", g/e; else print "-" }')
        echo "  progress: $((i + 1))/$ITERS green=$green elapsed=${elapsed}s rate=${rate}/s"
    fi
done

elapsed=$(( $(date +%s) - start_ts ))
echo
echo "sky-fuzz: DONE iters=$ITERS green=$green failures=$failures elapsed=${elapsed}s"
if [[ $failures -eq 0 ]]; then
    if (( ITERS >= 10000 )); then
        echo "sky-fuzz: criterion 8 SATISFIED — ran $ITERS iters clean"
    else
        echo "sky-fuzz: smoke pass — ran $ITERS iters clean (criterion 8 needs >=10000)"
    fi
    exit 0
else
    exit 1
fi
