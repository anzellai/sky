#!/usr/bin/env bash
# mutate.sh — prove each Stage 3 gate can actually go RED.
#
# A gate that has never been seen to fail is not evidence. This session has
# already produced three FALSE verdicts, and the recipe below is built against
# each of them by name:
#
#   1. An aliased `cp -i` silently declined to overwrite a compiler binary, and
#      the only thing that caught it was grepping the built artefact.
#   2. A `perl -0pi` matched nothing and reported success.
#   3. `noclobber` served a four-day-old sibling agent's log as a fresh green.
#
# So: never `cp`; mutate in place with an ABSOLUTE `/usr/bin/sed` so no alias or
# shell function is on the path; assert the pre-state literal exists BEFORE the
# edit, the mutation exists AFTER it, and is gone after revert (three edges, not
# one); use `grep -qF` so a pattern containing [ { or | cannot silently fail to
# match; and require every log to NAME ITS OWN MUTATION, which a stale log
# physically cannot do.
#
# The scratchpad is shared with parallel agents, so log paths carry $$ as well
# as a timestamp (see the `shared_scratchpad_noclobber_trap` note).
set -euo pipefail
set +o noclobber

WT="${WT:-/Users/anzel/works/playground/sky-stage3}"
LOG="${LOG:?set LOG to a run-specific log dir}"
mkdir -p "$LOG"
source "$WT/scripts/lib/with-timeout.sh"

SED=/usr/bin/sed
GREP=/usr/bin/grep

# confirm <file> <fixed-string> <present|absent> <label>
confirm() {
  local f="$1" pat="$2" want="$3" label="$4"
  if [ "$want" = present ]; then
    "$GREP" -qF -- "$pat" "$f" || { echo "MUTATE-FAIL[$label]: expected present but absent: $pat" >&2; exit 3; }
  else
    "$GREP" -qF -- "$pat" "$f" && { echo "MUTATE-FAIL[$label]: expected absent but present: $pat" >&2; exit 3; }
  fi
}

# run_gate <mutation-token> <gate-cmd...>
# The log MUST name the mutation token, or it is a log from another run.
run_gate() {
  local token="$1"; shift
  local out="$LOG/mut-$token-$(date +%s)-$$.log"
  echo "== mutation $token ==" >| "$out"
  echo "MUTATION_TOKEN=$token" >> "$out"
  ( cd "$WT" && with_timeout 3600 "$@" ) >> "$out" 2>&1 && local rc=0 || local rc=$?
  "$GREP" -qF "MUTATION_TOKEN=$token" "$out" || { echo "STALE-LOG[$token]: log does not name its own mutation" >&2; exit 4; }
  # freshness: the log must be newer than the compiler it describes
  [ "$out" -nt "$WT/rust/target/release/sky" ] || echo "WARN[$token]: log older than compiler binary" >&2
  echo "$token rc=$rc log=$out"
  return 0
}

LOWER="$WT/rust/crates/lower/src/lower.rs"
RT="$WT/runtime-go/rt/rt.go"

# ── S1 — the routing never fires ────────────────────────────────────────────
# The typed re-target table entry is renamed so no call site is ever proven.
# MUST go red: the emission leg (a gate asserting markerFlags emits the typed
# call) and the allocation leg. MUST stay green: semantics — the erased path is
# still correct, which is the whole point of the fallback.
s1() {
  confirm "$LOWER" 'rt.List_foldlElemFirstT' present S1-pre
  "$SED" -i '' 's/rt\.List_foldlElemFirstT/rt.List_foldlElemFirstT___MUT/g' "$LOWER"
  confirm "$LOWER" 'rt.List_foldlElemFirstT___MUT' present S1-post
  run_gate S1 cargo run --release -p xtask -- coerce-floor
  "$SED" -i '' 's/rt\.List_foldlElemFirstT___MUT/rt.List_foldlElemFirstT/g' "$LOWER"
  confirm "$LOWER" 'rt.List_foldlElemFirstT___MUT' absent S1-revert
}

# ── S2 — the fold DIRECTION ─────────────────────────────────────────────────
# THE trap of this change, and note what it is NOT.
#
# The obvious mutation — swap the callback's two arguments, `fn(x, acc)` ->
# `fn(acc, x)` — DOES NOT COMPILE: `fn` is `func(A, B) B`, so applying it as
# `fn(B, A)` is a Go type error. That is a real safety property of the twin's
# signature and it is worth stating, but a mutation the compiler rejects proves
# nothing about the GATES. It was in the first draft of this file and would have
# reported a red that no gate produced.
#
# The mutation that does compile and does change the answer is the ITERATION
# DIRECTION: fold right-to-left instead of left-to-right. Same signature, same
# shape, same allocation count, wrong result for any non-commutative callback —
# which is why `list_typed_twin_test.go` accumulates with string concatenation
# and subtraction rather than a sum. A sum-based test passes in both directions.
#
# MUST go red: semantics — `runtime-go/rt` tests first (fastest), then
# build-run's stdout diff against the oracle.
# MUST stay green: emission shape, allocation count.
s2() {
  local pre='	for _, x := range xs {
		acc = fn(x, acc)'
  confirm "$RT" 'acc = fn(x, acc)' present S2-pre
  # Reverse the walk in List_foldlElemFirstT only (first match; List_foldlT
  # above it uses `fn(acc, x)` and is untouched).
  /usr/bin/perl -0pi -e 's/\tfor _, x := range xs \{\n\t\tacc = fn\(x, acc\)/\tfor _i := len(xs) - 1; _i >= 0; _i-- {\n\t\tx := xs[_i]\n\t\tacc = fn(x, acc)/' "$RT"
  confirm "$RT" 'for _i := len(xs) - 1; _i >= 0; _i--' present S2-post
  run_gate S2 env CGO_ENABLED=1 go test ./runtime-go/rt/... -run 'Foldl|AnyT' -count=1
  /usr/bin/perl -0pi -e 's/\tfor _i := len\(xs\) - 1; _i >= 0; _i-- \{\n\t\tx := xs\[_i\]\n\t\tacc = fn\(x, acc\)/\tfor _, x := range xs \{\n\t\tacc = fn(x, acc)/' "$RT"
  confirm "$RT" 'for _i := len(xs) - 1; _i >= 0; _i--' absent S2-revert
  confirm "$RT" 'acc = fn(x, acc)' present S2-revert-restored
}

# ── S3 — `provable` relaxed ─────────────────────────────────────────────────
# Accepting a Go type parameter as provable is the exact defect Stage 2's own
# mutation matrix exists to prove is a defect. MUST go red: the fallback leg.
s3() {
  confirm "$LOWER" 'GoTy::Any | GoTy::TyVar(_) | GoTy::Struct(_) => false,' present S3-pre
  "$SED" -i '' 's/GoTy::Any | GoTy::TyVar(_) | GoTy::Struct(_) => false,/GoTy::Any | GoTy::Struct(_) => false,\n        GoTy::TyVar(_) => true,/' "$LOWER"
  confirm "$LOWER" 'GoTy::TyVar(_) => true,' present S3-post
  run_gate S3 cargo run --release -p xtask -- build-run --all --run
  "$SED" -i '' 's/GoTy::Any | GoTy::Struct(_) => false,\n        GoTy::TyVar(_) => true,/GoTy::Any | GoTy::TyVar(_) | GoTy::Struct(_) => false,/' "$LOWER"
  confirm "$LOWER" 'GoTy::TyVar(_) => true,' absent S3-revert
}

# ── S4 — the O(n)-copy guard ────────────────────────────────────────────────
# `func_shape_eta_applies`'s `rebuilds` check (lower.rs ~:2944) refuses to
# retype a callback whose param or result narrowing would REBUILD a slice or
# map — "never trade an O(1) reflect box for an O(n) copy". Its doc comment is
# written ABOUT `List.foldl` with a Dict accumulator. Nothing covers it today.
# This needs a NEW gate asserting a list/dict-accumulator foldl stays ERASED.
# Until that gate exists, S4 is UNPROVEN and must be reported as such rather
# than quietly omitted.
s4() {
  echo "S4 UNPROVEN: no gate yet asserts a list-accumulator foldl stays erased." >&2
  return 0
}

case "${1:-all}" in
  S1) s1 ;; S2) s2 ;; S3) s3 ;; S4) s4 ;;
  all) s1; s2; s3; s4 ;;
  *) echo "usage: mutate.sh [S1|S2|S3|S4|all]" >&2; exit 2 ;;
esac
