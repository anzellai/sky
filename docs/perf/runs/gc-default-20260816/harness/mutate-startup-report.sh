#!/usr/bin/env bash
# Mutation prover for the startup-block gates. Same discipline as
# scratch-mutate.sh: every mutation is grep-confirmed present before a red is
# believed.
set -uo pipefail
# The worktree this was run from; absolute, matching the convention of
# gogc-postgres-20260816/harness. Point WT at a checkout to re-run.
WT=/Users/anzel/works/sky-wt-gcdefault
cd "$WT" || exit 1
SRC=runtime-go/rt/startup_report.go
GATE='TestNoAddedStartupLineLooksLikeAListeningLine|TestProductionPrintsNoConsoleLineAndNoScolding|TestTheDevBlockNamesOnlyVariablesTheRuntimeReads|TestEveryVariableTheDevBlockNamesHasAReaderInThisPackage|TestTheDevBlockStaysShort|TestAnOperatorsOwnGCSettingIsVisiblyHonoured|TestNoConsoleMountedMeansNoConsoleLine|TestSkyGcQuietDropsOnlyTheGcLine'

cp -f "$SRC" /tmp/startup_report.go.orig
restore() { cp -f /tmp/startup_report.go.orig "$SRC"; }
trap restore EXIT

run_mutation() {
  local name="$1" needle="$2"
  if ! grep -qF "$needle" "$SRC"; then
    echo "MUTATION-NOT-APPLIED: $name — needle absent: $needle"; restore; exit 1
  fi
  echo "  mutation present: $needle"
  local out
  out=$(cd runtime-go && go test ./rt/ -run "$GATE" -count=1 2>&1)
  if echo "$out" | grep -q '^ok'; then
    echo "SURVIVED: $name — VACUOUS"; restore; return 1
  fi
  echo "KILLED:   $name"
  echo "$out" | grep -E '^--- FAIL' | head -3
  restore; return 0
}

fails=0

echo "== M11: banner tells every user to set a variable no runtime code reads =="
perl -0pi -e 's/SKY_CONSOLE_AUTH=token", "to deploy"\)/SKY_CONSOLE_AUTH=token SKY_AUTH_TOKEN_SECRET=x", "to deploy") \/\/ MUT11/' "$SRC"
run_mutation "M11 names-SKY_AUTH_TOKEN_SECRET" "// MUT11" || fails=$((fails+1))

echo "== M12: banner names a variable that simply does not exist =="
perl -0pi -e 's/SKY_CONSOLE_AUTH=token", "to deploy"\)/SKY_CONSOLE_SECRET=x", "to deploy") \/\/ MUT12/' "$SRC"
run_mutation "M12 names-nonexistent-var" "// MUT12" || fails=$((fails+1))

echo "== M13: an added line takes the shape the port parsers key on =="
perl -0pi -e 's/\(open — no login in dev\)/(listening, open - no login) \/\/MUT13/' "$SRC"
run_mutation "M13 added-line-says-listening" "//MUT13" || fails=$((fails+1))

echo "== M14: the console block survives ENV=production =="
perl -0pi -e 's/\tdev := consoleURL != "" \&\& !production/\tdev := consoleURL != "" \/\/ MUT14/' "$SRC"
run_mutation "M14 console-line-in-production" "// MUT14" || fails=$((fails+1))

echo "== M15: the GC line is dropped from the block entirely =="
perl -0pi -e 's/\tif !gcQuiet \{/\tif false { \/\/ MUT15/' "$SRC"
run_mutation "M15 gc-line-dropped" "if false { // MUT15" || fails=$((fails+1))

echo
if [ "$fails" -eq 0 ]; then echo "ALL 5 MUTATIONS KILLED"; else echo "$fails SURVIVED"; fi
exit "$fails"
