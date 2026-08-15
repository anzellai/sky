#!/usr/bin/env bash
#
# grill-mutation-matrix.sh — does the gate suite still discriminate when several
# defects are present AT ONCE?
#
# # Why this exists
#
# Adversarial grill round 5 rejected this branch's Std.Analytics / Std.Db pool
# work for a reason that no single-mutation check can detect: nine separate
# defects, introduced simultaneously, left both test suites GREEN. Each gate had
# been shown able to fail on its own. That is a strictly weaker property, and it
# is not the one that matters — a real regression arrives with company.
#
# So the acceptance criterion for the remediation was stated as: apply all nine
# mutations at once and the suite must go red. This script is that experiment,
# recorded so it can be re-run rather than believed.
#
# It answers two questions, and the second is the one worth the runtime:
#
#   1. Does the suite go red with all nine applied?
#   2. Do the failures DISCRIMINATE — is each mutation still detected by a gate
#      of its own, or does one defect cascade and mask the others? Nine
#      mutations producing a dozen failures could be nine gates firing plus
#      knock-ons, or three gates firing and nine cascades. Those are very
#      different levels of assurance, and only the per-mutation baseline can
#      tell them apart.
#
# The method: run the full suite once per mutation ALONE to learn each one's
# failure signature, once with ALL of them, and once unmutated. Then a mutation
# is "detected in combination" iff at least one test from its own signature is
# still failing in the combined run. Anything failing in the combined run that
# no single mutation explains is a cascade, and is reported as such rather than
# counted as evidence.
#
# # Usage
#
#   SKY_POSTGRES_BIN=/opt/homebrew/opt/postgresql@14/bin \
#     ./scripts/grill-mutation-matrix.sh [--out DIR] [--keep-logs]
#
# `SKY_POSTGRES_BIN` is NOT optional. Five of the nine mutations are caught only
# by gates that boot a real PostgreSQL; without it those gates SKIP, every one of
# those mutations reads as undetected, and the run is worse than useless because
# it looks like a result. The script refuses to start without it.
#
# Runtime is roughly eleven full runs of the rt suite — about ten minutes.
#
# # Safety
#
# Every mutation is applied to the working tree and reverted with `git checkout`
# on the exact paths it touched. The script refuses to run in a dirty tree, so a
# crash mid-run can never be confused with your own edits. Run it in a worktree.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$REPO/docs/history/embedded-postgres"
KEEP_LOGS=0
WITH_CARGO=0
LOGDIR="$(mktemp -d "${TMPDIR:-/tmp}/grill-mutation-XXXXXX")"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --keep-logs) KEEP_LOGS=1; shift ;;
    --with-cargo) WITH_CARGO=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# ── the mutations ──────────────────────────────────────────────────────
#
# Each is the defect the corresponding gate exists to catch, in the griller's
# own formulation. `id|paths|description` and a perl -0 expression that
# introduces it.

MUT_IDS=(A1 A2 A3 A6 A7 A10 A11 A12 A5b)

mut_paths() { # the files a mutation touches, for revert
  case "$1" in
    A1)  echo "runtime-go/rt/db_pool.go" ;;
    A2)  echo "runtime-go/rt/analytics_writer.go" ;;
    A3)  echo "runtime-go/rt/dbshare/dbshare.go" ;;
    A6)  echo "runtime-go/rt/analytics_writer.go" ;;
    A7)  echo "runtime-go/rt/pg_embed.go" ;;
    A10) echo "runtime-go/rt/dbshare/dbshare.go" ;;
    A11) echo "runtime-go/rt/dbshare/dbshare.go" ;;
    A12) echo "runtime-go/rt/analytics_writer.go" ;;
    A5b) echo "runtime-go/rt/console_analytics.go" ;;
  esac
}

mut_desc() {
  case "$1" in
    A1)  echo "the shared-pool sizing collapses to the bare quarter-share, so at <=4 cores the two background caps consume the whole pool and the session store is guaranteed nothing" ;;
    A2)  echo "\`SET LOCAL\` becomes a bare \`SET\`, so the pooled connection reports synchronous_commit = off for the rest of its life and the next borrower's writes are acked before the WAL hits disk" ;;
    A3)  echo "the consumer's cap is released at BEGIN rather than at COMMIT, so it bounds nothing for a consumer that writes in transactions — which the analytics writer does on every flush" ;;
    A6)  echo "the analytics queue is allocated 1024x its documented bound: 352 MB of ring buffer at open" ;;
    A7)  echo "the boot-time conf re-tune is deleted, so max_connections is frozen at initdb while the pools re-read the machine on every start" ;;
    A10) echo "the last consumer drops the pool from the registry without closing it, leaking a pool's worth of PostgreSQL backends per open/close cycle" ;;
    A11) echo "a shared pool never grows for a later consumer, so the session store runs on telemetry's 4 connections because telemetry initialises first" ;;
    A12) echo "the durable-analytics branch writes around the semaphore, so the bulkhead is decorative whenever SKY_ANALYTICS_SYNCHRONOUS_COMMIT=on" ;;
    A5b) echo "the console analytics endpoint reads without draining the buffered writer, so the Analytics tab shows a stale (or empty) view" ;;
  esac
}

mut_apply() {
  case "$1" in
    A1)  perl -0pi -e 's/\tn := c\.MaxOpenConns \+ dbAnalyticsShare \+ telemetry\.Share\n/\tn := c.MaxOpenConns\n/' \
           "$REPO/runtime-go/rt/db_pool.go" ;;
    A2)  perl -0pi -e 's/SET LOCAL synchronous_commit = off/SET synchronous_commit = off/' \
           "$REPO/runtime-go/rt/analytics_writer.go" ;;
    A3)  perl -0pi -e 's/\treturn &Tx\{Tx: tx, release: release\}, nil\n/\trelease()\n\treturn &Tx{Tx: tx, release: func() {}}, nil\n/' \
           "$REPO/runtime-go/rt/dbshare/dbshare.go" ;;
    A6)  perl -0pi -e 's/queue:         make\(chan analyticsRow, analyticsQueueCap\)/queue:         make(chan analyticsRow, analyticsQueueCap*1024)/' \
           "$REPO/runtime-go/rt/analytics_writer.go" ;;
    A7)  perl -0pi -e 's/\tif err := writeTunedConf\(s\.cfg\.dataDir, detectMachine\(\)\); err != nil \{\n\t\treturn err\n\t\}\n\tif err := s\.spawn\(\); err != nil \{/\tif err := s.spawn(); err != nil {/' \
           "$REPO/runtime-go/rt/pg_embed.go" ;;
    A10) perl -0pi -e 's/\tdelete\(registry, h\.key\)\n\treturn e\.db\.Close\(\)/\tdelete(registry, h.key)\n\treturn nil/' \
           "$REPO/runtime-go/rt/dbshare/dbshare.go" ;;
    A11) perl -0pi -e 's/\} else if cfg\.MaxOpenConns > e\.cfg\.MaxOpenConns && e\.cfg\.MaxOpenConns != 0 \{/} else if false \&\& cfg.MaxOpenConns > e.cfg.MaxOpenConns \&\& e.cfg.MaxOpenConns != 0 {/' \
           "$REPO/runtime-go/rt/dbshare/dbshare.go" ;;
    A12) perl -0pi -e 's/\tcase w\.pool != nil:\n\t\t_, err = w\.pool\.Exec\(stmt, args\.\.\.\)/\tcase w.pool != nil:\n\t\t_, err = w.db.Exec(stmt, args...)/' \
           "$REPO/runtime-go/rt/analytics_writer.go" ;;
    A5b) perl -0pi -e 's/\tanalyticsFlushPending\(\)\n\n\t_ = db\.QueryRow/\t_ = db.QueryRow/' \
           "$REPO/runtime-go/rt/console_analytics.go" ;;
  esac
}

# ── plumbing ───────────────────────────────────────────────────────────

die() { echo "grill-mutation-matrix: $*" >&2; exit 1; }

revert_all() {
  local paths=()
  for id in "${MUT_IDS[@]}"; do
    for p in $(mut_paths "$id"); do paths+=("$p"); done
  done
  ( cd "$REPO" && git checkout -- "${paths[@]}" )
  # `git checkout` restores content but not mtime ordering; touch so the Go
  # build cache cannot serve a stale object for a file it thinks is unchanged.
  ( cd "$REPO" && touch "${paths[@]}" )
}

# apply_checked runs the mutation and FAILS if the file did not change.
#
# This is not paranoia. An earlier hand-run of this experiment used a perl
# pattern whose indentation did not match, the substitution silently did
# nothing, the suite stayed green, and for a few minutes that read as "the gate
# is vacuous" rather than "the mutation was never applied". A mutation harness
# that cannot tell those apart produces confident nonsense.
apply_checked() {
  local id="$1" before after
  before="$(cd "$REPO" && git diff --stat -- $(mut_paths "$id"))"
  mut_apply "$id"
  after="$(cd "$REPO" && git diff --stat -- $(mut_paths "$id"))"
  [[ "$before" != "$after" ]] || die "mutation $id did not change $(mut_paths "$id"). The pattern no longer matches the source — most likely the code was reformatted or refactored. This run would have reported the gate as vacuous when it is not; fix the pattern."
}

# run_suite <label> — full rt suite, verbose, into the log dir.
run_suite() {
  local label="$1"
  ( cd "$REPO/runtime-go" && \
    SKY_POSTGRES_BIN="$SKY_POSTGRES_BIN" timeout 1800 go test -v ./rt/... -count=1 ) \
    > "$LOGDIR/$label.log" 2>&1 || true
}

# all_failing <label> — every top-level failing test name, one per line.
all_failing() {
  grep -E '^--- FAIL: ' "$LOGDIR/$1.log" | sed -E 's/^--- FAIL: ([^ ]+).*/\1/' | sort -u
}

# INFRA_RE matches the first line of a failure that is the HARNESS breaking, not
# a gate firing.
#
# This distinction is load-bearing and was learned the hard way. The first run of
# this experiment recorded two extra failures under mutations that could not
# possibly cause them — a queue-size change and a deleted conf re-tune, both
# "failing" initdb. The cause was two agents sharing `~/.sky/p5-live-test/<name>`
# and deleting each other's data directory mid-bootstrap. Counted naively, that
# inflates a mutation's signature with failures it did not cause; worse, in the
# combined run it could credit a gate with detecting a defect when it had merely
# tripped over a broken cluster. So these are classified out, loudly, rather than
# quietly counted.
INFRA_RE='initdb failed|boot a live cluster|first boot:|pg_ctl|Resource temporarily unavailable|no PostgreSQL binaries|too many open files|no space left'

# infra_failures <label> — failing tests whose first message is a harness error.
infra_failures() {
  local label="$1" t msg
  while read -r t; do
    [[ -z "$t" ]] && continue
    msg="$(first_message "$t" "$label")"
    if [[ "$msg" =~ $INFRA_RE ]]; then echo "$t"; fi
  done < <(all_failing "$label") | sort -u
}

# failing_tests <label> — failures attributable to a GATE, infrastructure
# breakage excluded. This is what every count below is computed from.
failing_tests() {
  comm -23 <(all_failing "$1") <(infra_failures "$1")
}

# run_cargo <label> — the Rust workspace, whose pool sizing is tied to the Go
# side by runtime-go/rt/testdata/db_pool_sizing.tsv.
#
# Opt-in (`--with-cargo`) because a cold build in a fresh worktree costs more
# than the entire Go matrix. Worth paying once: round 5's verdict was that all
# nine defects left BOTH suites green, and "the Rust suite is expected to stay
# green under Go-only mutations" is a claim, not a measurement, until something
# runs it.
run_cargo() {
  local label="$1"
  [[ $WITH_CARGO -eq 1 ]] || return 0
  ( cd "$REPO/rust" && CARGO_TARGET_DIR="$REPO/rust/local-target" \
      timeout 3000 cargo test --workspace ) > "$LOGDIR/$label-cargo.log" 2>&1 \
    && echo 0 > "$LOGDIR/$label-cargo.exit" \
    || echo $? > "$LOGDIR/$label-cargo.exit"
}

# failing_subtests <label> — nested failures, "Parent/Sub" form.
failing_subtests() {
  grep -E '^ +--- FAIL: ' "$LOGDIR/$1.log" | sed -E 's/^ +--- FAIL: ([^ ]+).*/\1/' | sort -u
}

# skipped_live <label> — live gates that skipped for want of a cluster.
skipped_live() {
  grep -E '^ *--- SKIP: ' "$LOGDIR/$1.log" | sed -E 's/^ *--- SKIP: ([^ ]+).*/\1/' | sort -u
}

# first_message <label> <test> — the first assertion line the test printed, so
# the report records WHY it failed and not merely that it did.
first_message() {
  awk -v t="$1" '
    $0 == "=== RUN   " t { on = 1; next }
    on && /^ *[A-Za-z0-9_]+\.go:[0-9]+: / { sub(/^ +/, ""); print; exit }
    on && /^--- (PASS|FAIL|SKIP)/ { exit }
  ' "$LOGDIR/$2.log" | head -1
}

# ── preflight ──────────────────────────────────────────────────────────

[[ -n "${SKY_POSTGRES_BIN:-}" ]] || die "SKY_POSTGRES_BIN is unset. Five of the nine mutations are caught ONLY by gates that boot a real PostgreSQL; without it they skip and this run would report them as undetected."
[[ -x "$SKY_POSTGRES_BIN/postgres" ]] || die "no postgres binary at $SKY_POSTGRES_BIN"
( cd "$REPO" && git diff --quiet ) || die "the working tree is dirty. This script mutates tracked files and reverts them with git checkout; refusing to run where that could destroy your edits."

mkdir -p "$OUT"
echo "grill-mutation-matrix: logs in $LOGDIR"
echo "grill-mutation-matrix: HEAD $(cd "$REPO" && git rev-parse --short HEAD)"

trap 'revert_all' EXIT

# ── the runs ───────────────────────────────────────────────────────────

echo "[0/11] baseline (unmutated)"
run_suite baseline

# A non-green baseline invalidates everything downstream, so stop here rather
# than emit a report hedged with a caveat nobody reads. The usual cause on this
# machine is not the code: it is another live suite running at the same time.
# Two of these suites at once exhaust the SysV shared-memory IDs PostgreSQL
# needs to bootstrap —
#
#   FATAL: could not create shared memory segment: No space left on device
#   HINT:  ...all available shared memory IDs have been taken...
#
# — and every live gate then fails for a reason that has nothing to do with any
# mutation. Wait for the other run, reap orphaned segments whose NATTCH is 0
# (`ipcs -mo`, `ipcrm -m <id>`), and start again.
if [[ -n "$(all_failing baseline)" ]]; then
  echo "grill-mutation-matrix: the UNMUTATED baseline is not green:" >&2
  all_failing baseline | sed 's/^/  - /' >&2
  echo >&2
  infra="$(infra_failures baseline)"
  if [[ -n "$infra" ]]; then
    echo "  Of those, these look like infrastructure rather than gates:" >&2
    echo "$infra" | sed 's/^/    * /' >&2
    echo "  Is another live suite running? Check \`ipcs -mo\` for leaked segments." >&2
  fi
  die "refusing to run the experiment against a red baseline — every count would be meaningless"
fi

echo "[1/11] all nine mutations at once"
for id in "${MUT_IDS[@]}"; do apply_checked "$id"; done
run_suite all-nine
run_cargo all-nine
revert_all

i=1
for id in "${MUT_IDS[@]}"; do
  i=$((i + 1))
  echo "[$i/11] $id alone"
  apply_checked "$id"
  run_suite "single-$id"
  revert_all
done

# ── the report ─────────────────────────────────────────────────────────

REPORT="$OUT/mutation-matrix.md"
{
  echo "# The simultaneous-mutation experiment"
  echo
  echo "Generated by \`scripts/grill-mutation-matrix.sh\` at"
  echo "\`$(cd "$REPO" && git rev-parse HEAD)\` on $(date -u '+%Y-%m-%d %H:%MZ')."
  echo "Re-run it rather than trusting this file."
  echo
  echo "Adversarial grill round 5 rejected the Std.Analytics / Std.Db pool work"
  echo "because **nine defects introduced at once left both suites green**. Each"
  echo "gate had been shown able to fail alone, which is a strictly weaker"
  echo "property than the one that matters. This is the experiment that checks the"
  echo "stronger one, and — the part a single-mutation check cannot reach —"
  echo "whether the failures still *discriminate* when all nine are present."
  echo
  echo "## Baseline"
  echo
  if [[ -z "$(failing_tests baseline)" ]]; then
    echo "Unmutated: **no failures**. (Required — an experiment run against a"
    echo "already-red tree measures nothing.)"
  else
    echo "**The unmutated tree is not green**, so every number below is suspect:"
    echo
    failing_tests baseline | sed 's/^/- /'
  fi
  echo
  live_skips="$(skipped_live baseline | grep -Ei 'live|Boot|Backends|Transaction' || true)"
  if [[ -n "$live_skips" ]]; then
    echo "Live gates that SKIPPED in the baseline (they cannot detect anything in"
    echo "this run):"
    echo
    echo "$live_skips" | sed 's/^/- /'
    echo
  else
    echo "No live gate skipped: the PostgreSQL-backed gates ran."
    echo
  fi

  echo "## Infrastructure"
  echo
  echo "Failures whose first message is the harness breaking — a cluster that"
  echo "would not bootstrap, a fork that would not fork — are classified out of"
  echo "every count below, because they are not a gate firing. They are listed"
  echo "here instead: an experiment that hides them is worse than one that has"
  echo "them."
  echo
  infra_any=0
  for label in baseline all-nine $(for id in "${MUT_IDS[@]}"; do echo "single-$id"; done); do
    inf="$(infra_failures "$label")"
    if [[ -n "$inf" ]]; then
      infra_any=1
      echo "- \`$label\`: $(echo "$inf" | tr '\n' ' ')"
    fi
  done
  [[ $infra_any -eq 0 ]] && echo "None. Every failure in every run came from a gate."
  echo
  if [[ -n "$(infra_failures all-nine)" ]]; then
    echo "> **The combined run hit infrastructure trouble.** Those gates could not"
    echo "> have detected anything in it, so the discrimination verdict below is"
    echo "> INCONCLUSIVE for any mutation whose only detector is among them."
    echo
  fi

  echo "## All nine at once"
  echo
  all_top="$(failing_tests all-nine)"
  all_sub="$(failing_subtests all-nine)"
  n_top="$(echo "$all_top" | grep -c . || true)"
  n_sub="$(echo "$all_sub" | grep -c . || true)"
  echo "**$n_top distinct top-level tests fail** (plus $n_sub named subtest(s), which"
  echo "\`go test\` reports on their own line — count the top-level figure, not the"
  echo "line count of \`--- FAIL\`)."
  echo
  echo '```'
  grep -E '^(--- FAIL: | +--- FAIL: )' "$LOGDIR/all-nine.log" || true
  echo '```'
  echo
  echo "The suite goes red, which is the acceptance criterion round 5 rejected"
  echo "this work for failing. Whether it goes red for the RIGHT reasons is the"
  echo "next section, and is the part that criterion does not actually establish."
  echo

  echo "## Does it discriminate?"
  echo
  echo "A mutation is **detected in combination** when at least one test from its"
  echo "own solo signature is still failing with all nine applied. A failure that"
  echo "no single mutation produces on its own is a **cascade**: real, but not"
  echo "evidence that any particular defect was caught."
  echo
  echo "| Mutation | Fails alone | Still failing in the combined run | Verdict |"
  echo "|---|---|---|---|"
  undetected=""
  for id in "${MUT_IDS[@]}"; do
    solo="$(failing_tests "single-$id")"
    both="$(comm -12 <(echo "$solo") <(echo "$all_top") | grep -c . || true)"
    solo_n="$(echo "$solo" | grep -c . || true)"
    if [[ "$both" -gt 0 ]]; then
      verdict="detected"
    else
      verdict="**MASKED**"
      undetected="$undetected $id"
    fi
    echo "| \`$id\` | $solo_n | $both | $verdict |"
  done
  echo
  if [[ -n "$undetected" ]]; then
    echo "**Masked mutations:$undetected.** The combined run does not distinguish"
    echo "these from a tree that does not contain them. That is the round-5 failure"
    echo "mode, in miniature."
  else
    echo "**No mutation is masked.** Every one of the nine is still called out by a"
    echo "gate of its own with the other eight present."
  fi
  echo
  echo "### Per-mutation detail"
  echo
  for id in "${MUT_IDS[@]}"; do
    echo "#### \`$id\` — $(mut_desc "$id")"
    echo
    echo "Touches \`$(mut_paths "$id")\`. Alone, it fails:"
    echo
    failing_tests "single-$id" | while read -r t; do
      [[ -z "$t" ]] && continue
      msg="$(first_message "$t" "single-$id")"
      echo "- \`$t\`"
      [[ -n "$msg" ]] && echo "  - $msg"
    done
    echo
    kept="$(comm -12 <(failing_tests "single-$id") <(echo "$all_top") | tr '\n' ' ')"
    echo "Still failing with all nine applied: ${kept:-**none — masked**}"
    echo
  done

  echo "### Cascades"
  echo
  union="$(for id in "${MUT_IDS[@]}"; do failing_tests "single-$id"; done | sort -u)"
  cascades="$(comm -23 <(echo "$all_top") <(echo "$union"))"
  if [[ -z "$cascades" ]]; then
    echo "None: every failure in the combined run is one a single mutation also"
    echo "produces. The combined failure set is exactly the union of the solo sets"
    echo "(or a subset of it)."
  else
    echo "Failing with all nine but with no single mutation alone — these are"
    echo "interactions, and are NOT counted as detection of any mutation:"
    echo
    echo "$cascades" | sed 's/^/- `/;s/$/`/'
  fi
  echo
  lost="$(comm -13 <(echo "$all_top") <(echo "$union"))"
  if [[ -n "$lost" ]]; then
    echo "### Failures a mutation causes alone but NOT in combination"
    echo
    echo "These gates fire for a defect on its own and stop firing once the others"
    echo "are present. None of them is the ONLY detector of its mutation (see the"
    echo "table above, which is what the verdict rests on), but each is a place"
    echo "where one defect shadows another's symptom:"
    echo
    echo "$lost" | sed 's/^/- `/;s/$/`/'
    echo
  fi

  echo "## The Rust suite"
  echo
  if [[ -f "$LOGDIR/all-nine-cargo.exit" ]]; then
    cexit="$(cat "$LOGDIR/all-nine-cargo.exit")"
    cres="$(grep -c '^test result:' "$LOGDIR/all-nine-cargo.log" || true)"
    cfail="$(grep -c '^test result: FAILED' "$LOGDIR/all-nine-cargo.log" || true)"
    echo "MEASURED, with all nine applied: \`cargo test --workspace\` exited"
    echo "\`$cexit\` over $cres test binaries, $cfail of them FAILED."
    echo
  else
    echo "Not measured in this run (pass \`--with-cargo\`)."
    echo
  fi
  echo "All nine mutations are in Go. \`cargo test --workspace\` is therefore"
  echo "expected to stay green under them; the Rust half of the pool"
  echo "sizing is exercised by its own mutation (make either language's constant"
  echo "diverge from \`runtime-go/rt/testdata/db_pool_sizing.tsv\` and the other"
  echo "language's gate fails). Round 5's \"both suites green\" verdict is about"
  echo "the Go suite failing to notice Go defects, not about the Rust one."
} > "$REPORT"

if [[ $KEEP_LOGS -eq 1 ]]; then
  # Summaries, not the raw logs. `go test -v` over this package is ~250 kB a
  # run and there are eleven of them; 2.8 MB of scrollback in the repository
  # would be checked in once and read by nobody. The verdict lines are what a
  # later reader needs, and the assertion text is already quoted in the report.
  for f in "$LOGDIR"/*.log; do
    b="$(basename "$f" .log)"
    {
      echo "# $b — verdict lines only, from scripts/grill-mutation-matrix.sh."
      echo "# The full verbose log is ~250 kB and is deliberately not checked in;"
      echo "# re-run the script to regenerate it."
      echo
      grep -E '^(--- (FAIL|SKIP)|[[:space:]]+--- (FAIL|SKIP)|ok |FAIL|PASS|test result:)' "$f" || true
    } > "$OUT/$b.summary.txt"
  done
  echo "grill-mutation-matrix: run summaries written to $OUT"
fi

echo "grill-mutation-matrix: wrote $REPORT"
