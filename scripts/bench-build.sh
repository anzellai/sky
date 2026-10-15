#!/usr/bin/env bash
# scripts/bench-build.sh — the build-speed benchmark.
#
# Measures what a Sky user waits for when they build, on apps shaped like real
# ones, and writes one JSON record per build: wall time, CPU time (user + sys
# of `sky` and every child), peak RSS (the largest single process), and the
# per-phase `SKY_TIMINGS` table of every `sky` process in the build.
#
# Cases (`--cases`, default all):
#   app       the `xtask bench-gen` app at each `--sizes` N (Model fields =
#             update arms = N, 8 modules, seed 1), for each of `--targets`
#             (default web:app and web)
#   skyshop   examples/13-skyshop (Go FFI at 76k-symbol scale; `sky install`
#             runs once first, untimed, and needs the network)
#   desktop   the smallest generated app for --target desktop (macOS only;
#             skipped with a note elsewhere, as there is no desktop shell)
#
# Scenarios per case and repetition:
#   prime    a build in a fresh directory with the normal Sky caches. Its cost
#            depends on what earlier runs left in them (rep 1 may compile the
#            runtime, later reps do not), so it is recorded but never budgeted.
#   warm     the same directory again, nothing changed
#   newdir   the same sources copied to a new, deeper directory
#   edit     one string literal changed in one module, in the new directory.
#            The new text carries a per-run token, so no earlier run (or a
#            restored CI cache) has seen the edited program.
#   cold     a fresh directory with an EMPTY temporary GOCACHE (the Go
#            module cache and Sky's precompress cache are not emptied)
#
# Usage:
#   scripts/bench-build.sh [--reps N] [--sizes "50 100"] [--cases "app skyshop desktop"]
#                          [--targets "web:app web"]
#                          [--out FILE.json] [--budget docs/perf/build-budget.json]
#                          [--write-budget FILE] [--sky PATH --tree DIR] [--work DIR]
#                          [--budget-subset]
#
#   --budget        compare the medians (over --reps) to the budget for this
#                   platform; exit 1 when any is above budget x factor (and
#                   above budget + slack, so a 1.6 s warm build does not fail on
#                   runner noise), or when the budget has no entry for this
#                   platform or a budgeted key was not measured
#   --budget-subset with --budget, for a run that measures a subset of the
#                   budgeted cases (a pull request runs `--cases app --sizes
#                   50`): a budgeted key is required only when this run
#                   measured its case (`app50-web`, `skyshop-web`, the part
#                   before the `/`), and every key of such a case is compared
#                   as usual. It fails when no budgeted case was measured. The
#                   full run (main, nightly) never passes it, so there every
#                   budgeted key must be measured.
#   --write-budget  write this run's medians (prime excluded) as this
#                   platform's budget entry
#   --report FILE   measure nothing; compare / write the budget from a report
#                   an earlier run wrote with --out
#   --sky / --tree  measure another compiler; it must be fresh for --tree (the
#                   tree it was built from). Default: sky-out/sky of this tree.
#
# Builds run in a temporary work directory (`--work`, removed afterwards unless
# given). Nothing here builds in the repository, so sky-out/sky is never
# written. Every build is bounded by with_timeout.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/with-timeout.sh
source "$ROOT/scripts/lib/with-timeout.sh"
# shellcheck source=lib/fresh-compiler.sh
source "$ROOT/scripts/lib/fresh-compiler.sh"
# shellcheck source=lib/require-tool.sh
source "$ROOT/scripts/lib/require-tool.sh"

SKY="$ROOT/sky-out/sky"
TREE="$ROOT"
REPS=1
SIZES="50 100"
CASES="app skyshop desktop"
TARGETS="web:app web"
OUT=""
BUDGET=""
WRITE_BUDGET=""
WORK=""
FROM_REPORT=""
BUDGET_SUBSET=0
BUILD_TIMEOUT="${BENCH_BUILD_TIMEOUT:-3600}"

while [ "$#" -gt 0 ]; do
    case "$1" in
        --sky) SKY="$2"; shift 2 ;;
        --tree) TREE="$2"; shift 2 ;;
        --reps) REPS="$2"; shift 2 ;;
        --sizes) SIZES="$2"; shift 2 ;;
        --cases) CASES="$2"; shift 2 ;;
        --targets) TARGETS="$2"; shift 2 ;;
        --out) OUT="$2"; shift 2 ;;
        --budget) BUDGET="$2"; shift 2 ;;
        --write-budget) WRITE_BUDGET="$2"; shift 2 ;;
        --work) WORK="$2"; shift 2 ;;
        --report) FROM_REPORT="$2"; shift 2 ;;
        --budget-subset) BUDGET_SUBSET=1; shift ;;
        -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
        *) echo "bench-build: unknown argument '$1' (see --help)" >&2; exit 2 ;;
    esac
done

require_tool jq "install jq (apt-get install jq / brew install jq)"
require_tool go "install Go (https://go.dev/dl/)"
require_tool cargo "install the Rust toolchain (https://rustup.rs)"
if [ ! -x /usr/bin/time ]; then
    echo "FAIL: bench-build needs /usr/bin/time (apt-get install time)." >&2
    exit 1
fi
if [ -z "$FROM_REPORT" ]; then
    require_fresh_compiler "$SKY" "$TREE"
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in aarch64) ARCH=arm64 ;; amd64) ARCH=x86_64 ;; esac
PLATFORM="$OS-$ARCH"

KEEP_WORK=1
if [ -z "$WORK" ]; then
    WORK="$(mktemp -d "${TMPDIR:-/tmp}/sky-bench-build.XXXXXX")"
    KEEP_WORK=0
fi
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd)"
RUNS="$WORK/runs.jsonl"
: > "$RUNS"
cleanup() {
    if [ "$KEEP_WORK" = 0 ]; then
        chmod -R u+w "$WORK" 2>/dev/null || true
        rm -rf "$WORK"
    fi
}
trap cleanup EXIT

# ---- helpers -----------------------------------------------------------------

# Copy a project's sources (not its build outputs) from $1 to $2.
copy_project() {
    mkdir -p "$2"
    (cd "$1" && tar cf - --exclude ./sky-out --exclude ./.skyapp --exclude ./.split \
        --exclude ./.skycache --exclude ./dist --exclude '*.db' .) | (cd "$2" && tar xf -)
}

# The SKY_TIMINGS tables in a build log, as a JSON array.
timings_json() {
    awk '
        function esc(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); return s }
        /^sky timings \(/ {
            scope = $0; sub(/^sky timings \(/, "", scope); sub(/\):$/, "", scope); next
        }
        scope != "" && $NF == "MB" && $(NF-2) ~ /^[0-9.]+s$/ {
            label = $0; sub(/^  /, "", label)
            sub(/[ ]+[0-9.]+s[ ]+[0-9-]+ MB$/, "", label)
            wall = $(NF-2); sub(/s$/, "", wall)
            rss = $(NF-1); if (rss == "-") rss = "null"
            if (label == "phase") next
            printf "%s{\"scope\":\"%s\",\"phase\":\"%s\",\"wall_s\":%s,\"rss_mb\":%s}", sep, esc(scope), esc(label), wall, rss
            sep = ","
            next
        }
        /^$/ { scope = "" }
    ' "$1" | { printf '['; cat; printf ']'; }
}

# measure <case> <scenario> <rep> <dir> <log> <env-assignments...> -- <sky args...>
measure() {
    local case_id="$1" scenario="$2" rep="$3" dir="$4" log="$5"
    shift 5
    local envs=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
    shift
    local tf="$WORK/time.out" code=0
    rm -f "$tf"
    if [ "$OS" = darwin ]; then
        (cd "$dir" && export SKY_TIMINGS=1 ${envs[@]+"${envs[@]}"} &&
            with_timeout "$BUILD_TIMEOUT" /usr/bin/time -l -p -o "$tf" "$SKY" build "$@") \
            > "$log" 2>&1 || code=$?
    else
        (cd "$dir" && export SKY_TIMINGS=1 ${envs[@]+"${envs[@]}"} &&
            with_timeout "$BUILD_TIMEOUT" /usr/bin/time -o "$tf" \
            -f $'real %e\nuser %U\nsys %S\nmaxrss_kb %M' "$SKY" build "$@") \
            > "$log" 2>&1 || code=$?
    fi
    local wall user sys rss
    wall="$(awk '$1 == "real" { print $2 }' "$tf" 2>/dev/null || true)"
    user="$(awk '$1 == "user" { print $2 }' "$tf" 2>/dev/null || true)"
    sys="$(awk '$1 == "sys" { print $2 }' "$tf" 2>/dev/null || true)"
    if [ "$OS" = darwin ]; then
        rss="$(awk '/maximum resident set size/ { print $1 }' "$tf" 2>/dev/null || true)"
    else
        rss="$(awk '$1 == "maxrss_kb" { print $2 * 1024 }' "$tf" 2>/dev/null || true)"
    fi
    if [ "$code" != 0 ] || [ -z "$wall" ]; then
        echo "FAIL: $case_id $scenario (rep $rep) exited $code. Last lines of $log:" >&2
        tail -25 "$log" >&2
        exit 1
    fi
    local timings
    timings="$(timings_json "$log")"
    jq -cn --arg case "$case_id" --arg scenario "$scenario" --argjson rep "$rep" \
        --argjson wall "$wall" --argjson user "$user" --argjson sys "$sys" \
        --argjson rss "${rss:-0}" --argjson timings "$timings" \
        '{case: $case, scenario: $scenario, rep: $rep, wall_s: $wall,
          cpu_s: (($user + $sys) * 100 | round / 100), peak_rss_bytes: $rss,
          timings: $timings}' >> "$RUNS"
    printf '  %-22s %-7s rep %s  wall %7.2fs  cpu %7.2fs  peak %6d MB\n' \
        "$case_id" "$scenario" "$rep" "$wall" "$(awk -v u="$user" -v s="$sys" 'BEGIN { print u + s }')" \
        "$(( ${rss:-0} / 1048576 ))"
}

# run_case <case-id> <pristine-dir> <edit-file> <edit-from> <edit-to> -- <sky build args...>
run_case() {
    local case_id="$1" pristine="$2" edit_file="$3" edit_from="$4" edit_to="$5"
    shift 6
    local rep=1
    while [ "$rep" -le "$REPS" ]; do
        local base="$WORK/r$rep/$case_id"
        local a="$base/a" b="$base/deeper/b" c="$base/c"
        copy_project "$pristine" "$a"
        measure "$case_id" prime "$rep" "$a" "$base/prime.log" -- "$@"
        measure "$case_id" warm "$rep" "$a" "$base/warm.log" -- "$@"
        copy_project "$pristine" "$b"
        measure "$case_id" newdir "$rep" "$b" "$base/newdir.log" -- "$@"
        if ! grep -qF -- "$edit_from" "$b/$edit_file"; then
            echo "FAIL: $case_id: the edit target '$edit_from' is not in $edit_file" >&2
            exit 1
        fi
        local token
        token="$(date +%s)p$$r$rep"
        EDIT_FROM="$edit_from" EDIT_TO="${edit_to//@TOKEN@/$token}" perl -0pi -e \
            's/\Q$ENV{EDIT_FROM}\E/$ENV{EDIT_TO}/' "$b/$edit_file"
        measure "$case_id" edit "$rep" "$b" "$base/edit.log" -- "$@"
        copy_project "$pristine" "$c"
        local cache="$WORK/gocache-cold"
        rm -rf "$cache"
        mkdir -p "$cache"
        measure "$case_id" cold "$rep" "$c" "$base/cold.log" "GOCACHE=$cache" -- "$@"
        chmod -R u+w "$cache" "$base" 2>/dev/null || true
        rm -rf "$cache" "$base"
        rep=$((rep + 1))
    done
}

# ---- the cases ---------------------------------------------------------------

if [ -n "$FROM_REPORT" ]; then
    CASES=""
fi

# Every case in $CASES, measured into $RUNS.
measure_cases() {
    XTASK_BUILD="$WORK/xtask-build.log"
    echo "bench-build: building xtask (bench-gen)"
    if ! (cd "$ROOT/rust" && with_timeout 1800 cargo build --release -q -p xtask) \
        > "$XTASK_BUILD" 2>&1; then
        tail -20 "$XTASK_BUILD" >&2
        exit 1
    fi
    XTASK="$(cd "$ROOT/rust" && cargo build --release -q -p xtask --message-format=json 2>/dev/null \
        | jq -r 'select(.executable != null) | .executable' | tail -1)"

    gen_app() {
        local n="$1" dir="$WORK/src-app$1"
        if [ ! -d "$dir" ]; then
            "$XTASK" bench-gen --fields "$n" --arms "$n" --modules 8 --seed 1 --out "$dir" >&2
        fi
        echo "$dir"
    }

    for c in $CASES; do
        case "$c" in
            app)
                for n in $SIZES; do
                    src="$(gen_app "$n")"
                    for t in $TARGETS; do
                        run_case "app$n-$t" "$src" src/View/Section1.sky \
                            '"Section 1"' '"Section 1 (edited @TOKEN@)"' -- --target "$t" src/Main.sky
                    done
                done
                ;;
            skyshop)
                shop="$WORK/src-skyshop"
                copy_project "$ROOT/examples/13-skyshop" "$shop"
                rm -rf "$shop/.skydeps" "$shop/sky-ffi"
                echo "bench-build: sky install in the skyshop copy (untimed)"
                (cd "$shop" && with_timeout 1800 "$SKY" install) > "$WORK/skyshop-install.log" 2>&1 || {
                    tail -20 "$WORK/skyshop-install.log" >&2
                    exit 1
                }
                run_case "skyshop-web" "$shop" src/Page/Home.sky \
                    '"letter-spacing:.1em"' '"letter-spacing:.2em;--bench:@TOKEN@"' -- src/Main.sky
                ;;
            desktop)
                if [ "$OS" != darwin ]; then
                    echo "bench-build: desktop case runs on macOS only; not measured on $PLATFORM"
                    continue
                fi
                n="${SIZES%% *}"
                src="$(gen_app "$n")"
                run_case "app$n-desktop" "$src" src/View/Section1.sky \
                    '"Section 1"' '"Section 1 (edited @TOKEN@)"' -- --target desktop src/Main.sky
                ;;
            *) echo "bench-build: unknown case '$c'" >&2; exit 2 ;;
        esac
    done
}
if [ -n "$CASES" ]; then
    measure_cases
fi

# ---- the report --------------------------------------------------------------

if [ "$OS" = darwin ]; then
    CPUS="$(sysctl -n hw.ncpu)"
    MEM="$(sysctl -n hw.memsize)"
else
    CPUS="$(nproc)"
    MEM="$(awk '/^MemTotal:/ { print $2 * 1024 }' /proc/meminfo)"
fi
REPORT="$WORK/report.json"
if [ -n "$FROM_REPORT" ]; then
    cp -f "$FROM_REPORT" "$REPORT"
    REPS="$(jq -r '.reps' "$REPORT")"
else
jq -s --arg platform "$PLATFORM" --argjson cpus "$CPUS" --argjson mem "$MEM" \
    --arg go "$(go version)" --arg sky "$("$SKY" --version 2>/dev/null | head -1)" \
    --argjson reps "$REPS" '
    {machine: {platform: $platform, cpus: $cpus, mem_bytes: $mem, go: $go, sky: $sky},
     reps: $reps,
     medians: (group_by(.case + "/" + .scenario) | map({
        key: (.[0].case + "/" + .[0].scenario),
        value: {
          wall_s: (map(.wall_s) | sort | .[(length - 1) / 2 | floor]),
          cpu_s: (map(.cpu_s) | sort | .[(length - 1) / 2 | floor]),
          peak_rss_bytes: (map(.peak_rss_bytes) | sort | .[(length - 1) / 2 | floor])
        }}) | from_entries),
     runs: .}' "$RUNS" > "$REPORT"
fi

if [ -n "$OUT" ]; then
    cp -f "$REPORT" "$OUT"
    echo "bench-build: wrote $OUT"
fi

if [ -n "$WRITE_BUDGET" ]; then
    existing='{}'
    [ -f "$WRITE_BUDGET" ] && existing="$(cat "$WRITE_BUDGET")"
    echo "$existing" | jq --slurpfile r "$REPORT" --arg platform "$PLATFORM" '
        .factor = (.factor // 1.5)
        | .slack_s = (.slack_s // 2)
        | .slack_bytes = (.slack_bytes // 67108864)
        | .platforms[$platform] = {
            machine: $r[0].machine,
            reps: $r[0].reps,
            budget: ($r[0].medians | with_entries(select(.key | endswith("/prime") | not))
                     | map_values({wall_s, peak_rss_bytes}))
          }' > "$WRITE_BUDGET.tmp"
    mv -f "$WRITE_BUDGET.tmp" "$WRITE_BUDGET"
    echo "bench-build: wrote the $PLATFORM budget to $WRITE_BUDGET"
fi

if [ -n "$BUDGET" ]; then
    verdict="$(jq -r --slurpfile r "$REPORT" --arg platform "$PLATFORM" \
        --argjson subset "$BUDGET_SUBSET" '
        (.factor // 1.5) as $f
        | (.slack_s // 0) as $ss
        | (.slack_bytes // 0) as $sb
        | .platforms[$platform] as $p
        | if $p == null then
            "NO-BUDGET"
          else
            # A case is the key before the "/" (app50-web:app, skyshop-web).
            ($r[0].medians | keys | map(split("/")[0]) | unique) as $measured
            | ($p.budget | to_entries
               | if $subset == 1 then map(select((.key | split("/")[0]) as $c
                                                 | $measured | index($c)))
                 else . end) as $entries
            | if $subset == 1 and ($entries | length) == 0 then
                "NO-CASE: --budget-subset, but this run measured no budgeted case"
              else
            [ $entries[] | . as $b
              | ($r[0].medians[$b.key]) as $m
              | if $m == null then "MISSING \($b.key): budgeted but not measured"
                elif $m.wall_s > $b.value.wall_s * $f and $m.wall_s > $b.value.wall_s + $ss then
                  "OVER \($b.key): wall \($m.wall_s)s > \($b.value.wall_s)s x \($f)"
                elif $m.peak_rss_bytes > $b.value.peak_rss_bytes * $f
                    and $m.peak_rss_bytes > $b.value.peak_rss_bytes + $sb then
                  "OVER \($b.key): peak RSS \($m.peak_rss_bytes) > \($b.value.peak_rss_bytes) x \($f)"
                else empty end ] | join("\n")
              end
          end' "$BUDGET")"
    if [ "$verdict" = "NO-BUDGET" ]; then
        echo "FAIL: $BUDGET has no budget for $PLATFORM." >&2
        echo "  Seed it from a run on that platform: scripts/bench-build.sh --reps 3 --write-budget $BUDGET" >&2
        exit 1
    fi
    if [ -n "$verdict" ]; then
        echo "FAIL: build speed above budget ($BUDGET, medians of $REPS):" >&2
        echo "$verdict" >&2
        exit 1
    fi
    echo "bench-build: PASS — every median within budget x factor for $PLATFORM ($BUDGET)"
fi
