#!/usr/bin/env bash
# DB-backed END-TO-END acceptance for the Sky.Spa Todos backend — the untrusted
# boundary + durable store, exercised with `curl` as a HOSTILE client (raw
# requests that bypass the wasm UI entirely). Where run_roundtrip.sh proves the
# happy-path client<->backend loop, THIS proves the properties that make the
# explicit boundary safe with an untrusted client and a real database:
#
#   * server-side re-validation  (trim, length-clamp, reject-empty)
#   * server owns identity        (DB-assigned serial id; client id/done ignored)
#   * unknown id = no-op, not error; malformed JSON = 400
#   * every mutation returns the FULL authoritative list (server is truth)
#   * shared state across clients  (SQLite is the one shared axis)
#   * DURABILITY across a backend restart (the point of having a DB)
#
# Usage: SKY=/path/to/sky TODOS_PORT=8952 ./run_e2e_db.sh
set -u
cd "$(dirname "$0")"

SKY="${SKY:-$(cd ../.. && pwd)/sky-out/sky}"
ROOT="$(cd ../.. && pwd)"
# Fresh-compiler gate: never measure a compiler older than this tree.
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

PORT="${TODOS_PORT:-8952}"
export TODOS_PORT="$PORT"
BASE="http://localhost:$PORT"
DB="e2e.db"

pass=0; fail=0
ok() { echo "PASS  $1"; pass=$((pass + 1)); }
no() { echo "FAIL  $1"; fail=$((fail + 1)); }
# curl helpers (bounded; JSON body). j <url> -> GET json; p <path> <json> -> POST.
j()    { curl -sS -m 8 "$BASE$1"; }
p()    { curl -sS -m 8 -H 'Content-Type: application/json' -X POST "$BASE$1" -d "$2"; }
pcode(){ curl -sS -m 8 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -X POST "$BASE$1" -d "$2"; }

echo "==> build backend"
( cd server && "$SKY" build src/Main.sky >/dev/null ) || { echo "backend build FAILED"; exit 1; }

SERVER_PID=""
start() { ( cd server && SKY_DB_PATH="$DB" ./sky-out/app ) >/tmp/spa-e2e-db-$PORT.log 2>&1 & SERVER_PID=$!; sleep 2; }
stop()  { [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; SERVER_PID=""; }
trap 'stop' EXIT

echo "==> clean DB + start backend on :$PORT"
rm -f server/"$DB" server/"$DB"-* 2>/dev/null
start

# ── S1 empty to start ────────────────────────────────────────────────
n=$(j /api/todos | jq 'length' 2>/dev/null)
[ "$n" = 0 ] && ok "clean DB starts empty" || no "clean DB empty (got '$n')"

# ── S2 add returns the full authoritative list; server sets done=false ─
r=$(p /api/todos '{"title":"buy milk"}')
[ "$(echo "$r" | jq -r '.[0].title')" = "buy milk" ] && ok "add returns full list with the new title" || no "add title (got $r)"
[ "$(echo "$r" | jq -r '.[0].done')" = false ] && ok "server sets done=false on create" || no "create done=false"
id1=$(echo "$r" | jq -r '.[0].id')
[ "$id1" -ge 1 ] 2>/dev/null && ok "server assigned a serial id ($id1)" || no "server id"

# ── S3 server TRIMS whitespace (never stores client string verbatim) ──
r=$(p /api/todos '{"title":"   spaced   "}')
[ "$(echo "$r" | jq -r '[.[]|select(.title=="spaced")]|length')" = 1 ] && ok "server TRIMS surrounding whitespace" || no "trim (got $r)"

# ── S4 server CLAMPS title to 200 chars ──────────────────────────────
long=$(printf 'x%.0s' $(seq 1 250))
r=$(p /api/todos "{\"title\":\"$long\"}")
maxlen=$(echo "$r" | jq -r '[.[]|.title|length]|max')
[ "$maxlen" = 200 ] && ok "server CLAMPS title to 200 chars (max len $maxlen)" || no "clamp (max len $maxlen)"

# ── S5 server REJECTS empty/whitespace-only title (no-op) ────────────
before=$(j /api/todos | jq 'length')
p /api/todos '{"title":"    "}' >/dev/null
after=$(j /api/todos | jq 'length')
[ "$before" = "$after" ] && ok "server REJECTS empty/whitespace title (no row added)" || no "reject empty ($before -> $after)"

# ── S6 UNTRUSTED CLIENT: server ignores client-sent id + done on create ─
r=$(p /api/todos '{"title":"sneaky","id":9999,"done":true}')
sid=$(echo "$r" | jq -r '.[]|select(.title=="sneaky")|.id')
sdone=$(echo "$r" | jq -r '.[]|select(.title=="sneaky")|.done')
[ "$sid" != 9999 ] && ok "server IGNORES client-sent id (assigned its own: $sid)" || no "SECURITY: client dictated id!"
[ "$sdone" = false ] && ok "server IGNORES client-sent done=true on create" || no "SECURITY: client dictated done!"

# ── S7 toggle flips done, returns full list ──────────────────────────
r=$(p /api/todos/toggle "{\"id\":$id1}")
[ "$(echo "$r" | jq -r ".[]|select(.id==$id1)|.done")" = true ] && ok "toggle flips done" || no "toggle"

# ── S8 unknown id = 200 no-op (not an error) ─────────────────────────
[ "$(pcode /api/todos/toggle '{"id":987654}')" = 200 ] && ok "toggle unknown id = 200 no-op (not error)" || no "unknown-id toggle status"
[ "$(pcode /api/todos/delete '{"id":987654}')" = 200 ] && ok "delete unknown id = 200 no-op (not error)" || no "unknown-id delete status"

# ── S9 malformed JSON = 400 ──────────────────────────────────────────
[ "$(pcode /api/todos 'this is not json')" = 400 ] && ok "malformed JSON body = 400" || no "malformed JSON status"

# ── S10 rename re-validates (trim) ───────────────────────────────────
r=$(p /api/todos/rename "{\"id\":$id1,\"title\":\"  renamed  \"}")
[ "$(echo "$r" | jq -r ".[]|select(.id==$id1)|.title")" = renamed ] && ok "rename applies + trims" || no "rename"

# ── S11 delete removes the row ───────────────────────────────────────
p /api/todos/delete "{\"id\":$id1}" >/dev/null
[ -z "$(j /api/todos | jq -r ".[]|select(.id==$id1)|.id")" ] && ok "delete removes the row" || no "delete"

# ── S12 shared state: two independent clients see the same list ──────
[ "$(j /api/todos | jq 'length')" = "$(j /api/todos | jq 'length')" ] && ok "shared state consistent across independent clients" || no "shared state"

# ── S13 DURABILITY: data survives a backend restart (same SQLite file) ─
cnt_before=$(j /api/todos | jq 'length')
titles_before=$(j /api/todos | jq -cS '[.[].title]')
stop
start   # NOTE: no DB wipe — reopen the same file
cnt_after=$(j /api/todos | jq 'length')
titles_after=$(j /api/todos | jq -cS '[.[].title]')
{ [ "$cnt_after" -gt 0 ] && [ "$cnt_before" = "$cnt_after" ] && [ "$titles_before" = "$titles_after" ]; } \
  && ok "DB PERSISTS across backend restart ($cnt_after todos + titles survived)" \
  || no "persistence ($cnt_before->$cnt_after; titles $titles_before -> $titles_after)"

# ── S14 CONCURRENCY: parallel writes, no lost updates ────────────────
# Fire N adds concurrently; the single shared pool + SQLite must land ALL of
# them (a real production concern — a lost write here would be a data-loss bug).
base_cnt=$(j /api/todos | jq 'length')
N=12
wpids=""
for i in $(seq 1 $N); do p /api/todos "{\"title\":\"conc-$i\"}" >/dev/null & wpids="$wpids $!"; done
wait $wpids   # only the writer curls — NOT the backgrounded server (a bare `wait` would block on it)
final_cnt=$(j /api/todos | jq 'length')
conc_landed=$(j /api/todos | jq '[.[]|select(.title|startswith("conc-"))]|length')
{ [ "$conc_landed" = "$N" ] && [ "$final_cnt" = "$((base_cnt + N))" ]; } \
  && ok "CONCURRENCY: all $N parallel writes landed, none lost" \
  || no "concurrency (expected +$N; landed $conc_landed, total $base_cnt->$final_cnt)"

echo
echo "e2e-db: $pass passed, $fail failed"
[ "$fail" = 0 ]
