#!/usr/bin/env bash
# mutate.sh — prove each Stage 2 gate can go RED.
#
# A gate that has never failed is a gate that has never been shown to test
# anything. Each mutation below breaks ONE property; the named test must fail,
# and the others named as "expected green" must not, or the gate is coupled to
# something other than what it claims.
#
# The mutation is applied, GREP-CONFIRMED PRESENT, the test run, then reverted
# and the revert grep-confirmed. Three separate false verdicts in this session
# came from believing an edit landed when it had not: a `perl -0pi` whose
# pattern matched nothing, an aliased `cp` that prompted instead of copying,
# and `noclobber` swallowing a redirect. Confirm, never assume.
set -euo pipefail
WT="${WT:-/Users/anzel/works/playground/sky-wt-stage2}"
LOWER="$WT/rust/crates/lower/src/lower.rs"
RT="$WT/runtime-go/rt/rt.go"
LOG="${LOG:-/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad}"

source "$WT/scripts/lib/with-timeout.sh"

confirm() { # confirm <file> <pattern> <expect-present|expect-absent>
  if grep -qF -- "$2" "$1"; then found=present; else found=absent; fi
  [ "$found" = "$3" ] || { echo "MUTATION NOT APPLIED: '$2' is $found in $1, wanted $3" >&2; exit 1; }
  echo "  confirmed: '$2' $found in $(basename "$1")"
}

run_tests() { # run_tests <tag>
  ( cd "$WT/rust" && with_timeout 3600 cargo test --release -p sky --test hof_dispatch_shape \
      > "$LOG/mut-$1.log" 2>&1 ) && echo "  cargo test: PASS" || echo "  cargo test: FAIL (rc=$?)"
  grep -E '^(test |---- |failures:)' "$LOG/mut-$1.log" | head -30
}

case "${1:?usage: mutate.sh M1|M2|M3|revert}" in
  # M1 — the routing never fires. Emission + allocation legs must go RED;
  # fallback + semantics legs must stay GREEN (the erased path still works).
  M1)
    /usr/bin/sed -i '' 's|"rt.List_mapAny" => Some(ListHof::Map),|"rt.List_mapAny___MUT" => Some(ListHof::Map),|' "$LOWER"
    confirm "$LOWER" 'rt.List_mapAny___MUT' present
    run_tests M1 ;;
  # M2 — `any` counts as proven. The fallback leg must go RED: the type-variable
  # fixture would be specialised on a proof that was never made.
  M2)
    /usr/bin/sed -i '' 's|GoTy::Any \| GoTy::TyVar(_) \| GoTy::Struct(_) => false,|GoTy::TyVar(_) \| GoTy::Struct(_) => false, GoTy::Any => true,|' "$LOWER"
    confirm "$LOWER" 'GoTy::Any => true,' present
    run_tests M2 ;;
  # M3 — an off-by-one index in the typed helper. Only the semantics leg can see
  # it: the shape is right, the allocation count is right, the answer is wrong.
  M3)
    /usr/bin/sed -i '' 's|out\[i\] = fn(i, x)|out[i] = fn(i+1, x)|' "$RT"
    confirm "$RT" 'out[i] = fn(i+1, x)' present
    run_tests M3 ;;
  revert)
    ( cd "$WT" && git checkout -- rust/crates/lower/src/lower.rs runtime-go/rt/rt.go )
    confirm "$LOWER" 'rt.List_mapAny___MUT' absent
    confirm "$LOWER" 'GoTy::Any => true,' absent
    confirm "$RT" 'out[i] = fn(i+1, x)' absent
    echo "  reverted" ;;
esac
