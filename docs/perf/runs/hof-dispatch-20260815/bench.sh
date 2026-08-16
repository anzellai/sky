#!/usr/bin/env bash
# A/B throughput bench for the HOF eta-expansion fix.
#
# The two binaries differ by exactly one thing: the `func_shape_eta` branch in
# `coerce_if_needed`. Same tree, same Go toolchain, same app source — the
# "before" compiler was built from this worktree with that one branch gated off.
#
# Runs alternate B/A/B/A/B/A so thermal drift and burst-credit decay fall on
# both arms equally rather than on whichever went second.
set -uo pipefail

S=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/hofdispatch
GEN=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench/bin/skyliveload
APPDIR=/Users/anzel/works/playground/sky/.claude/worktrees/hof-dispatch/examples/26-ui-showcase
PORT=8537
SESSIONS="${SESSIONS:-25}"
DUR="${DUR:-20s}"
WARMUP="${WARMUP:-5s}"
RAMP="${RAMP:-3s}"

run_one() {
  local arm="$1" rep="$2"
  local bin="$S/app-$arm"
  pkill -f "$S/app-" 2>/dev/null
  sleep 1
  ( cd "$APPDIR" && GOMAXPROCS=1 SKY_LIVE_PORT=$PORT SKY_LIVE_STORE=memory exec "$bin" >"$S/run-$arm-$rep.applog" 2>&1 ) &
  local apppid=$!
  # wait for readiness rather than sleeping a guessed interval
  local i=0
  until curl -fsS -o /dev/null "http://127.0.0.1:$PORT/" 2>/dev/null; do
    i=$((i+1))
    if [ $i -gt 60 ]; then echo "arm=$arm rep=$rep FAILED_TO_START"; kill $apppid 2>/dev/null; return 1; fi
    sleep 0.5
  done
  # A STALE app on this port answers curl too, and would silently serve every
  # request while the binary under test sat dead. Prove the listener is ours.
  local owner
  owner=$(lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1)
  if [ "$owner" != "$apppid" ]; then
    echo "arm=$arm rep=$rep WRONG_LISTENER port=$PORT owner=$owner ours=$apppid"
    kill $apppid 2>/dev/null; return 1
  fi
  "$GEN" -url "http://127.0.0.1:$PORT" -sessions "$SESSIONS" -think 0 \
    -duration "$DUR" -warmup "$WARMUP" -ramp "$RAMP" -assume-yes \
    -label "$arm-r$rep" -json "$S/res-$arm-$rep.json" \
    >"$S/gen-$arm-$rep.log" 2>&1
  local rc=$?
  kill $apppid 2>/dev/null
  wait $apppid 2>/dev/null
  sleep 1
  if [ $rc -ne 0 ]; then echo "arm=$arm rep=$rep GEN_FAILED rc=$rc"; tail -3 "$S/gen-$arm-$rep.log"; return 1; fi
  return 0
}

for rep in 1 2 3; do
  for arm in before after; do
    run_one "$arm" "$rep" || echo "  (run $arm-$rep failed)"
  done
done
echo "BENCH DONE"
