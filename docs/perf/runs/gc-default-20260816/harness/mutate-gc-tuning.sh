#!/usr/bin/env bash
# Mutation prover for the GC-tuning gate.
#
# Each mutation is applied, GREP-CONFIRMED PRESENT IN THE FILE, then the gate is
# run. A "red" that was never actually mutated is the failure mode this script
# exists to make impossible: two gates were caught today reporting `ok` in
# 0.023s because their expectation was derived from the very constant their
# mutation changed.
set -uo pipefail
# The worktree this was run from; absolute, matching the convention of
# gogc-postgres-20260816/harness. Point WT at a checkout to re-run.
WT=/Users/anzel/works/sky-wt-gcdefault
cd "$WT" || exit 1
SRC=runtime-go/rt/gc_tuning.go
PG=runtime-go/rt/pg_embed_conf.go
GATE='TestTheAppAndPostgres|TestASoftLimit|TestTheLimitIsNever|TestAMachineTooSmall|TestATinyContainer|TestAnUndetectable|TestAnExplicitOperator|TestOneExplicit|TestTheMultiplierIs|TestServerlessTakes|TestServerlessDoesNot|TestEmbeddingPostgres|TestTheLimitRises|TestAnE2Small|TestTheChoiceIs|TestExceedingTheLimit|TestApplyingTheTuning|TestTheAmbientWrapper'

cp -f "$SRC" /tmp/gc_tuning.go.orig
cp -f "$PG" /tmp/pg_embed_conf.go.orig
restore() { cp -f /tmp/gc_tuning.go.orig "$SRC"; cp -f /tmp/pg_embed_conf.go.orig "$PG"; }
trap restore EXIT

run_mutation() { # name, grep-needle, expected-test
  local name="$1" needle="$2"
  if ! grep -qF "$needle" "$SRC" && ! grep -qF "$needle" "$PG"; then
    echo "MUTATION-NOT-APPLIED: $name — needle absent: $needle"
    echo "  => a red here would be MEANINGLESS. Aborting."
    restore; exit 1
  fi
  echo "  mutation present: $needle"
  local out
  out=$(cd runtime-go && go test ./rt/ -run "$GATE" -count=1 2>&1)
  if echo "$out" | grep -q '^ok'; then
    echo "SURVIVED: $name — the gate is VACUOUS for this mutation"
    echo "$out" | tail -3
    restore; return 1
  fi
  echo "KILLED:   $name"
  echo "$out" | grep -E '^\s+gc_tuning_test.go:|^--- FAIL' | head -3
  restore
  return 0
}

fails=0

echo "== M1: the app forgets the embedded cluster claims memory =="
perl -0pi -e 's/\treturn pgSharedBuffersFor\(ram\) \+ gcPostgresWorkingSetBytes/\treturn 0 \/\/ MUT1/' "$SRC"
run_mutation "M1 pg-reserve-dropped" "return 0 // MUT1" || fails=$((fails+1))

echo "== M2: GOGC default moved to the arm the run rejected =="
perl -0pi -e 's/\tgcHeapPercent = 400/\tgcHeapPercent = 800 \/\/ MUT2/' "$SRC"
run_mutation "M2 gogc-800" "gcHeapPercent = 800 // MUT2" || fails=$((fails+1))

echo "== M3: floor lowered below the stock collector's own peak =="
perl -0pi -e 's/\tgcMinMemoryLimitBytes = 256 \* mb/\tgcMinMemoryLimitBytes = 16 * mb \/\/ MUT3/' "$SRC"
run_mutation "M3 floor-16MiB" "gcMinMemoryLimitBytes = 16 * mb // MUT3" || fails=$((fails+1))

echo "== M4: the app claims everything left, with no overshoot allowance =="
perl -0pi -e 's/\tgcAppShareNumerator   = 3/\tgcAppShareNumerator   = 4 \/\/ MUT4/' "$SRC"
run_mutation "M4 share-4-of-4" "gcAppShareNumerator   = 4 // MUT4" || fails=$((fails+1))

echo "== M5: serverless takes the multiplier too =="
perl -0pi -e 's/\tcase env\.serverless:\n\t\tout\.reason \+= "; GOGC left at the Go default on a request-billed platform"/\tcase false: \/\/ MUT5\n\t\tout.reason += "; GOGC left at the Go default on a request-billed platform"/' "$SRC"
run_mutation "M5 serverless-takes-gogc" "case false: // MUT5" || fails=$((fails+1))

echo "== M6: an explicit operator GOMEMLIMIT is overridden anyway =="
perl -0pi -e 's/\twantLimit := env\.gomemlimit == ""/\twantLimit := true \/\/ MUT6/' "$SRC"
run_mutation "M6 override-operator" 'wantLimit := true // MUT6' || fails=$((fails+1))

echo "== M7: the decision is computed but never applied =="
perl -0pi -e 's/\tif t\.setMemoryLimit \{\n\t\tdebug\.SetMemoryLimit\(t\.memoryLimitBytes\)/\tif false \{ \/\/ MUT7\n\t\tdebug.SetMemoryLimit(t.memoryLimitBytes)/' "$SRC"
run_mutation "M7 apply-is-a-noop" "if false { // MUT7" || fails=$((fails+1))

echo "== M8: the ambient wrapper stops reading the operator's GOGC =="
perl -0pi -e 's/\t\tgogc:             os\.Getenv\("GOGC"\),/\t\tgogc:             "", \/\/ MUT8/' "$SRC"
run_mutation "M8 ambient-ignores-env" 'gogc:             "", // MUT8' || fails=$((fails+1))

echo "== M9: tuningFor re-inlines a DIFFERENT share, decoupling conf from the reserve =="
perl -0pi -e 's/\tshared := pgSharedBuffersFor\(ram\)/\tshared := clampBytes(ram*25\/100, 32*mb, 8192*mb) \/\/ MUT9/' "$PG"
run_mutation "M9 conf-decoupled-from-reserve" "// MUT9" || fails=$((fails+1))

echo
if [ "$fails" -eq 0 ]; then echo "ALL 9 MUTATIONS KILLED"; else echo "$fails MUTATION(S) SURVIVED"; fi
exit "$fails"
