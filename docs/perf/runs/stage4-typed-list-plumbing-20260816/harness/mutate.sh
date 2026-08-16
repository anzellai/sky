#!/usr/bin/env bash
# mutate.sh — prove each Stage 4 gate can actually go RED.
#
# A gate that has never been seen to fail is not evidence. The discipline below
# is Stage 3's (`../stage3-generic-defs-20260816/harness/mutate.sh`), unchanged,
# because it was built against three FALSE verdicts that session produced:
#
#   * an aliased `cp -i` silently declined to overwrite a compiler binary, and
#     only grepping the built artefact caught it;
#   * a `perl -0pi` matched nothing and reported success;
#   * `noclobber` served a stale sibling agent's log as a fresh green.
#
# So: mutate in place, never via a copy; assert the PRE-state literal exists
# BEFORE the edit, that the mutation exists AFTER it, and that it is GONE after
# revert — three edges, not one; use `grep -qF` so a pattern containing [ { or |
# cannot silently fail to match; and give every log a $$-stamped name, because
# the scratchpad is shared with parallel agents.
#
# THE MATRIX
#
#   S1  rt.List_appendT aliases its left operand again (the original one-liner)
#       -> go test TestListAppendT_doesNotAliasItsLeftOperand
#   S2  the `++` predicate stops requiring the two element types to be EQUAL
#       -> `sky build` on 19-skyforum (a []A ++ []B instantiation)
#   S3  the `++` predicate stops requiring `provable` on the element
#       -> `sky build` / the corpus (a GoTy::TyVar named in an instantiation)
#   S4  the twin is called with its operands SWAPPED
#       -> xtask build-run --golden (whole-program stdout)
#   S5  the unary twin table points isEmpty at List_lengthT (wrong return type)
#       -> `cargo build` or `sky build`
#   S6  the unary predicate stops requiring `provable` on the element
#       -> `sky build` / the corpus
#   S7  List_isEmptyT returns len(xs) != 0
#       -> go test TestListIsEmptyT_agreesWithErasedKernel
set -uo pipefail
WT="${WT:-/Users/anzel/works/playground/sky-stage4}"
LOG="${LOG:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4/mutate-$$-$(date +%s).log}"
LOWER="$WT/rust/crates/lower/src/lower.rs"
RT="$WT/runtime-go/rt/rt.go"
SED=/usr/bin/sed
PERL=/usr/bin/perl

M="${1:?usage: mutate.sh S1|S2|S3|S4|S5|S6|S7}"

# Every log NAMES ITS OWN MUTATION, which a stale log physically cannot do.
exec > >(tee "$LOG") 2>&1
echo "MUTATION $M  wt=$WT  pid=$$  $(date -u +%FT%TZ)"

revert() {
  (cd "$WT" && git checkout -- rust/crates/lower/src/lower.rs runtime-go/rt/rt.go)
}
trap revert EXIT

# edge1: the PRE-state literal must be present, or the recipe is stale.
pre() {
  if ! grep -qF -- "$2" "$1"; then
    echo "VOID: pre-state literal absent from $1 — recipe is stale:"; echo "  $2"
    exit 90
  fi
  echo "  edge1 pre-state present:  $2"
}
# edge2: the mutation must be present AFTER the edit, or the edit matched nothing.
post() {
  if ! grep -qF -- "$2" "$1"; then
    echo "VOID: mutation did not land in $1 — the edit matched nothing:"; echo "  $2"
    exit 91
  fi
  echo "  edge2 mutation landed:    $2"
}
# edge3: after revert the mutation must be GONE, or the trap did not fire.
check_reverted() {
  if grep -qF -- "$2" "$1"; then
    echo "VOID: mutation SURVIVED revert in $1 — the tree is dirty:"; echo "  $2"
    exit 92
  fi
  echo "  edge3 revert clean:       $2"
}

MUT=""
case "$M" in
  S1)
    F="$RT"
    PRE="out := make([]A, 0, len(a)+len(b))"
    MUT="return append(a, b...)"
    pre "$F" "$PRE"
    $PERL -0pi -e 's/\tout := make\(\[\]A, 0, len\(a\)\+len\(b\)\)\n\tout = append\(out, a\.\.\.\)\n\tout = append\(out, b\.\.\.\)\n\treturn out\n/\treturn append(a, b...)\n/' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: go test TestListAppendT_doesNotAliasItsLeftOperand ---"
    (cd "$WT/runtime-go" && go test -timeout 240s -run TestListAppendT ./rt/); echo "gate exit=$?"
    # S1b — and expect GREEN from the corpus, which is the POINT. The README
    # claims "every corpus gate passes the aliasing bug"; that claim is worth
    # only what it is measured at, so measure it rather than assert it. The
    # aliasing form returns the correct VALUE and corrupts a DIFFERENT one, only
    # when the left operand carries spare capacity, and nothing in
    # infer/roundtrip/golden constructs that condition.
    if [ "${S1B:-0}" = "1" ]; then
      echo "--- S1b: expect GREEN (the gates do not catch it) ---"
      (cd "$WT/rust" && cargo build --release -p sky 2>&1 | tail -2)
      (cd "$WT/rust" && cargo run --release -p xtask -- build-run --golden 2>&1 | tail -6); echo "corpus exit=$?"
    fi
    ;;
  S2)
    F="$LOWER"; PRE="if le == re && provable(le) {"; MUT="if provable(le) { // MUTANT-S2"
    pre "$F" "$PRE"
    $SED -i 's|if le == re \&\& provable(le) {|if provable(le) { // MUTANT-S2|' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: cargo build then sky build 19-skyforum ---"
    (cd "$WT/rust" && cargo build --release -p sky 2>&1 | tail -3)
    (cd "$WT/examples/19-skyforum" && "$WT/rust/target/release/sky" build src/Main.sky 2>&1 | tail -5); echo "gate exit=$?"
    ;;
  S3)
    F="$LOWER"; PRE="if le == re && provable(le) {"; MUT="if le == re { // MUTANT-S3"
    pre "$F" "$PRE"
    $SED -i 's|if le == re \&\& provable(le) {|if le == re { // MUTANT-S3|' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: cargo build then the corpus ---"
    (cd "$WT/rust" && cargo build --release -p sky 2>&1 | tail -3)
    (cd "$WT/rust" && cargo run --release -p xtask -- build-run 2>&1 | tail -8); echo "gate exit=$?"
    ;;
  S4)
    F="$LOWER"; PRE="vec![l, r],"; MUT="vec![r, l], // MUTANT-S4"
    pre "$F" "$PRE"
    $SED -i 's|vec!\[l, r\],|vec![r, l], // MUTANT-S4|' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: xtask build-run --golden ---"
    (cd "$WT/rust" && cargo build --release -p sky 2>&1 | tail -3)
    (cd "$WT/rust" && cargo run --release -p xtask -- build-run --golden 2>&1 | tail -12); echo "gate exit=$?"
    ;;
  S5)
    F="$LOWER"; PRE='"rt.List_isEmpty" => Some(("rt.List_isEmptyT", GoTy::Bare(Prim::Bool))),'
    MUT='"rt.List_isEmpty" => Some(("rt.List_lengthT", GoTy::Bare(Prim::Bool))), // MUTANT-S5'
    pre "$F" "$PRE"
    $SED -i 's|"rt.List_isEmpty" => Some(("rt.List_isEmptyT", GoTy::Bare(Prim::Bool))),|"rt.List_isEmpty" => Some(("rt.List_lengthT", GoTy::Bare(Prim::Bool))), // MUTANT-S5|' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: cargo build then sky build 19-skyforum ---"
    (cd "$WT/rust" && cargo build --release -p sky 2>&1 | tail -3)
    (cd "$WT/examples/19-skyforum" && "$WT/rust/target/release/sky" build src/Main.sky 2>&1 | tail -6); echo "gate exit=$?"
    ;;
  S6)
    F="$LOWER"; PRE="                if let GoTy::Slice(e) = &xs.ty {"; MUT="if true { // MUTANT-S6"
    pre "$F" "$PRE"
    $PERL -0pi -e 's/(if let GoTy::Slice\(e\) = &xs\.ty \{\n                    )if provable\(e\) \{/$1if true { \/\/ MUTANT-S6/' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: cargo build then the corpus ---"
    (cd "$WT/rust" && cargo build --release -p sky 2>&1 | tail -3)
    (cd "$WT/rust" && cargo run --release -p xtask -- build-run 2>&1 | tail -8); echo "gate exit=$?"
    ;;
  S7)
    F="$RT"; PRE="func List_isEmptyT[A any](xs []A) bool { return len(xs) == 0 }"
    MUT="func List_isEmptyT[A any](xs []A) bool { return len(xs) != 0 }"
    pre "$F" "$PRE"
    $SED -i 's|func List_isEmptyT\[A any\](xs \[\]A) bool { return len(xs) == 0 }|func List_isEmptyT[A any](xs []A) bool { return len(xs) != 0 }|' "$F"
    post "$F" "$MUT"
    echo "--- expect RED: go test TestListIsEmptyT / TestListUnaryT ---"
    (cd "$WT/runtime-go" && go test -timeout 240s -run 'TestListIsEmptyT|TestListUnaryT' ./rt/); echo "gate exit=$?"
    ;;
  *) echo "unknown mutation $M"; exit 2 ;;
esac

revert
trap - EXIT
check_reverted "$F" "$MUT"
echo "MUTATION $M complete"
