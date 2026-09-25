#!/usr/bin/env bash
# scripts/gates-for-change.sh — run the NARROWEST gates that prove a change.
#
# The local half of the gate policy (CLAUDE.md §0.2 / §0.2.1,
# docs/tooling/gate-harness.md "Which gates to run"):
#
#   * locally, per change: this script. It maps the paths changed against a
#     base (default origin/main; committed, staged, unstaged and untracked
#     files all count) to the gates that exercise them, prints the plan, and
#     runs it;
#   * at a merge to main or a release tag: the release workflow's FULL suite
#     (.github/workflows/release.yml), which must be green before the tag.
#     Nothing is deferred to the nightly, and this script is never a substitute
#     for that suite — it is how a change reaches it already green.
#
# Usage:
#   scripts/gates-for-change.sh [--base <ref>] [--dry-run] [--fail-fast]
#
#   --base <ref>   compare against <ref> (default origin/main, else main)
#   --dry-run      print the changed paths and the plan; run nothing
#   --fail-fast    stop at the first failing step
#
# Every step is time-bounded (scripts/lib/with-timeout.sh). A path no rule
# claims is reported as UNMAPPED: the plan says so rather than guessing, and
# the release suite is what covers it.
#
# Bash 3.2 compatible (stock macOS /bin/bash).

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"

BASE=""
DRY=0
FAIL_FAST=0
while [ $# -gt 0 ]; do
    case "$1" in
        --base) BASE="${2:-}"; shift 2 ;;
        --base=*) BASE="${1#--base=}"; shift ;;
        --dry-run) DRY=1; shift ;;
        --fail-fast) FAIL_FAST=1; shift ;;
        -h | --help) sed -n '2,27p' "$0"; exit 0 ;;
        *) echo "gates-for-change: unknown option $1" >&2; exit 2 ;;
    esac
done

if [ -z "$BASE" ]; then
    if git rev-parse --verify -q origin/main >/dev/null; then
        BASE=origin/main
    else
        BASE=main
    fi
fi
MB="$(git merge-base "$BASE" HEAD 2>/dev/null)" || {
    echo "gates-for-change: no merge base between $BASE and HEAD" >&2
    exit 2
}

CHANGED="$(
    {
        git diff --name-only "$MB"
        git ls-files -o --exclude-standard
    } | LC_ALL=C sort -u
)"

# ---- the plan: ordered steps, deduplicated -------------------------------
# Steps are "<order> <label>|<command>"; the order key sorts the plan so the
# compiler is installed before anything that measures it.
PLAN=""
add() { # <order> <label> <command>
    local line="$1 $2|$3"
    case "
$PLAN
" in
        *"
$line
"*) return 0 ;;
    esac
    PLAN="$PLAN
$line"
}
UNMAPPED=""
SH_CHANGED=""

XTASK="cargo run --release -q -p xtask --manifest-path rust/Cargo.toml --"
HARNESS="$XTASK harness"
BUILD_SKY="./scripts/build.sh"
CHANGED_EXAMPLES=""

needs_compiler() {
    add 10 "install the compiler under test (scripts/build.sh)" "$BUILD_SKY"
}

spa_e2e() {
    add 60 "e2e: Sky.Spa restore" "scripts/spa-restore-e2e.sh"
    add 60 "e2e: Sky.Spa stale handlers" "scripts/spa-stale-handler-e2e.sh"
    add 60 "e2e: Sky.Spa RPC consistency" "scripts/spa-rpc-consistency-e2e.sh"
    add 60 "e2e: Sky.Spa examples" "scripts/spa-examples-e2e.sh"
    add 60 "e2e: Sky.Spa + Sky.Live DOM identity" "scripts/spa-vdom-identity-e2e.sh"
    add 60 "e2e: Std.Ui forms" "scripts/ui-forms-e2e.sh"
    add 60 "e2e: strict Content-Security-Policy" "scripts/csp-e2e.sh"
    add 50 "harness: spa-diff-fuzz" "$HARNESS --only spa-diff-fuzz"
}

web_e2e() {
    add 60 "browser tier (verify-all-web)" "scripts/verify-all-web.sh"
    add 60 "e2e: Sky.Live client" "scripts/live-client-e2e.sh"
    add 60 "e2e: Sky.Spa + Sky.Live DOM identity" "scripts/spa-vdom-identity-e2e.sh"
}

compiler_core() {
    needs_compiler
    add 40 "corpus gates: roundtrip / resolve / infer / reject / repro" \
        "for g in roundtrip resolve infer reject repro; do $XTASK \$g || exit 1; done"
    add 45 "coerce-floor (runtime-narrowing floor)" "$XTASK coerce-floor"
    add 50 "harness T1: shared-world + corpus gates" \
        "$HARNESS --only shared-world,corpus-manifest,corpus-reject,corpus-emit-shape"
    add 50 "harness T2: behavioural corpus" "$HARNESS --tier t2 --only corpus"
    add 55 "codegen build + run of every example (build-run)" "$XTASK build-run --all --run"
    add 58 "example sweep (clean slate; unchanged examples reuse the gate build cache)" \
        "scripts/example-sweep.sh"
    add 58 "stdlib conformance" "scripts/conformance.sh"
}

while IFS= read -r f; do
    [ -n "$f" ] || continue
    mapped=1
    case "$f" in
        *.rs) add 01 "rustfmt --check" "(cd rust && cargo fmt --all -- --check)" ;;
        *.go) add 01 "gofmt -l (must list nothing)" \
            "test -z \"\$(gofmt -l runtime-go tools 2>/dev/null)\"" ;;
        *.sh) [ -f "$f" ] && SH_CHANGED="$SH_CHANGED $f" ;;
    esac
    case "$f" in
        rust/crates/syntax/* | rust/crates/hir/* | rust/crates/ty/* | rust/crates/lower/* | rust/crates/codegen/* | rust/crates/base/* | rust/crates/diagnostics/*)
            crate="${f#rust/crates/}"; crate="${crate%%/*}"
            add 20 "cargo test -p $crate" "(cd rust && cargo test -p $crate)"
            compiler_core ;;
        rust/crates/project/src/spa_*)
            add 20 "cargo test -p project" "(cd rust && cargo test -p project)"
            needs_compiler
            spa_e2e ;;
        rust/crates/project/*)
            add 20 "cargo test -p project" "(cd rust && cargo test -p project)"
            compiler_core ;;
        rust/crates/ffi/*)
            add 20 "cargo test -p ffi" "(cd rust && cargo test -p ffi)"
            needs_compiler
            add 58 "example sweep (FFI examples always rebuild)" "scripts/example-sweep.sh" ;;
        rust/crates/fmt/*)
            add 20 "cargo test -p fmt" "(cd rust && cargo test -p fmt)"
            add 40 "fmt gate (never drops a comment; idempotent)" "$XTASK fmt" ;;
        rust/crates/sky-lsp/*)
            add 20 "cargo test -p sky-lsp" "(cd rust && cargo test -p sky-lsp)"
            add 50 "harness: lsp (Neovim editor parity)" "$HARNESS --only lsp" ;;
        rust/crates/sky/*)
            add 20 "cargo test -p sky" "(cd rust && cargo test -p sky)"
            needs_compiler
            add 50 "harness: verify-cli + cli-verbs" "$HARNESS --only verify-cli,cli-verbs" ;;
        rust/crates/xtask/src/harness/*)
            add 20 "cargo test -p xtask" "(cd rust && cargo test -p xtask)" ;;
        rust/crates/xtask/src/corpus/*)
            add 20 "cargo test -p xtask" "(cd rust && cargo test -p xtask)"
            add 50 "harness: corpus gates" \
                "$HARNESS --only corpus-manifest,corpus-reject,corpus-emit-shape" ;;
        rust/crates/xtask/*)
            add 20 "cargo test -p xtask" "(cd rust && cargo test -p xtask)" ;;
        rust/crates/*)
            crate="${f#rust/crates/}"; crate="${crate%%/*}"
            add 20 "cargo test -p $crate" "(cd rust && cargo test -p $crate)" ;;
        rust/Cargo.toml | rust/Cargo.lock | rust/rust-toolchain.toml)
            add 20 "cargo test --workspace" "(cd rust && cargo test --workspace)"
            needs_compiler ;;
        runtime-go/rt/spa* | runtime-go/rt/*wasm*)
            add 20 "go test ./rt/... (-p 1)" "(cd runtime-go && go test -p 1 -count=1 ./rt/...)"
            needs_compiler
            spa_e2e
            web_e2e ;;
        runtime-go/*)
            add 20 "go test ./rt/... (-p 1)" "(cd runtime-go && go test -p 1 -count=1 ./rt/...)"
            needs_compiler
            web_e2e
            add 58 "stdlib conformance" "scripts/conformance.sh" ;;
        sky-stdlib/*)
            needs_compiler
            add 58 "stdlib conformance" "scripts/conformance.sh"
            add 50 "harness: sky-suites" "$HARNESS --only sky-suites"
            add 62 "live-docs examples" "scripts/doc-examples.sh" ;;
        tests/conformance/*)
            needs_compiler
            add 58 "stdlib conformance" "scripts/conformance.sh" ;;
        tests/*)
            needs_compiler
            add 50 "harness: sky-suites" "$HARNESS --only sky-suites" ;;
        examples/*)
            ex="${f#examples/}"; ex="${ex%%/*}"
            case " $CHANGED_EXAMPLES " in *" $ex "*) ;; *) CHANGED_EXAMPLES="$CHANGED_EXAMPLES $ex" ;; esac
            needs_compiler
            add 40 "roundtrip (examples are its corpus)" "$XTASK roundtrip"
            add 58 "example sweep (clean slate; unchanged examples reuse the gate build cache)" \
                "scripts/example-sweep.sh" ;;
        apps/ledger/*)
            needs_compiler
            add 50 "harness: apps-ledger" "$HARNESS --only apps-ledger" ;;
        apps/dispatch/*)
            needs_compiler
            add 50 "harness: apps-dispatch + destructive" \
                "$HARNESS --only apps-dispatch,apps-dispatch-destructive" ;;
        apps/relay/*)
            needs_compiler
            add 50 "harness: apps-relay" "$HARNESS --only apps-relay" ;;
        apps/fieldbook/*)
            needs_compiler
            add 50 "harness: apps-fieldbook" "$HARNESS --only apps-fieldbook" ;;
        sky-bundled/*)
            needs_compiler
            add 50 "harness: apps-bundled" "$HARNESS --only apps-bundled" ;;
        corpus/*)
            add 50 "harness: corpus-manifest" "$HARNESS --only corpus-manifest" ;;
        templates/*)
            needs_compiler
            add 50 "harness: cli-verbs (sky init)" "$HARNESS --only cli-verbs" ;;
        docs/history/*) ;;
        docs/coverage/*)
            add 30 "census: denominators + coverage-ledger" \
                "$XTASK denominators --check && $XTASK coverage-ledger --check" ;;
        docs/* | *.md)
            add 62 "live-docs examples" "scripts/doc-examples.sh"
            add 21 "docs state the current version" \
                "(cd rust && cargo test -p xtask --test docs_state_the_current_version)" ;;
        .github/*)
            add 21 "workflow structure" "(cd rust && cargo test -p xtask --test workflows_parse)" ;;
        scripts/lib/gate-build-cache.sh)
            add 21 "gate build cache" "(cd rust && cargo test -p xtask --test gate_build_cache)" ;;
        scripts/example-sweep.sh)
            add 21 "sweep shards are total" \
                "(cd rust && cargo test -p xtask --test example_sweep_shards_are_total)"
            needs_compiler
            add 58 "example sweep (clean slate; unchanged examples reuse the gate build cache)" \
                "scripts/example-sweep.sh" ;;
        scripts/verify-all-web.sh | scripts/verify-ui-showcase.sh | scripts/verify-live-app.mjs | scripts/verify-live-resilience.mjs | scripts/verify-console-e2e.mjs)
            needs_compiler
            add 60 "browser tier (verify-all-web)" "scripts/verify-all-web.sh" ;;
        scripts/*-e2e.sh)
            needs_compiler
            add 60 "e2e: ${f#scripts/}" "$f" ;;
        scripts/conformance.sh)
            needs_compiler
            add 58 "stdlib conformance" "scripts/conformance.sh" ;;
        scripts/doc-examples.sh)
            add 62 "live-docs examples" "scripts/doc-examples.sh" ;;
        scripts/*)
            add 21 "script gates (time bounds, fresh compiler)" \
                "(cd rust && cargo test -p xtask --test scripts_bound_time_portably --test gates_measure_a_fresh_compiler)" ;;
        *) mapped=0 ;;
    esac
    [ $mapped -eq 1 ] || UNMAPPED="$UNMAPPED
  $f"
done <<EOF
$CHANGED
EOF

if [ -n "$SH_CHANGED" ]; then
    add 01 "bash -n under /bin/bash (3.2 on macOS) on the changed scripts" \
        "for s in$SH_CHANGED; do /bin/bash -n \"\$s\" || exit 1; done"
fi

if [ -n "$CHANGED_EXAMPLES" ]; then
    only="$(echo $CHANGED_EXAMPLES | tr ' ' ',')"
    add 55 "codegen build + run of the changed examples" "$XTASK build-run --only=$only --run"
fi

# A change to anything a falsifier proof depends on must be re-proven before
# the release gate's `--require-proofs` will accept the gate. The run is
# incremental: it re-proves only gates whose proof-inputs digest changed (plus
# the canary), and carries the rest — seconds when nothing a proof reads moved.
if [ -n "$CHANGED" ]; then
    add 90 "falsifier proofs, incremental (commit docs/coverage/falsifier-proofs.json)" \
        "$HARNESS --verify-falsifiers"
fi

# The whole xtask suite subsumes its single-test steps.
case "$PLAN" in
    *"(cd rust && cargo test -p xtask)"*)
        PLAN="$(printf '%s\n' "$PLAN" | grep -v 'cargo test -p xtask --test')" ;;
esac
PLAN="$(printf '%s\n' "$PLAN" | sed '/^$/d' | LC_ALL=C sort -s -k1,1)"

echo "gates-for-change: base $BASE (merge base ${MB:0:12})"
n_changed=$(printf '%s\n' "$CHANGED" | sed '/^$/d' | wc -l | tr -d ' ')
echo "changed paths: $n_changed"
printf '%s\n' "$CHANGED" | sed '/^$/d' | sed 's/^/  /' | head -40
[ "$n_changed" -le 40 ] || echo "  … and $((n_changed - 40)) more"
if [ -n "$UNMAPPED" ]; then
    echo "UNMAPPED (no narrow gate; the release suite covers them):$UNMAPPED"
fi
echo
if [ -z "$PLAN" ]; then
    echo "plan: nothing to run."
    exit 0
fi
echo "plan:"
i=0
while IFS= read -r line; do
    i=$((i + 1))
    rest="${line#* }"
    printf '  %2d. %s\n      $ %s\n' "$i" "${rest%%|*}" "${rest#*|}"
done <<EOF
$PLAN
EOF

[ $DRY -eq 0 ] || exit 0

echo
failed=""
i=0
while IFS= read -r line; do
    i=$((i + 1))
    rest="${line#* }"
    label="${rest%%|*}"
    cmd="${rest#*|}"
    echo "==> [$i] $label"
    t0=$(date +%s)
    with_timeout 3600 bash -c "$cmd" </dev/null
    rc=$?
    echo "    [$i] exit $rc in $(( $(date +%s) - t0 ))s"
    if [ $rc -ne 0 ]; then
        failed="$failed
  [$i] $label (exit $rc)"
        [ $FAIL_FAST -eq 0 ] || break
    fi
done <<EOF
$PLAN
EOF

echo
if [ -n "$failed" ]; then
    echo "gates-for-change: FAIL$failed"
    exit 1
fi
echo "gates-for-change: PASS — the release workflow's full suite remains the merge/release gate."
