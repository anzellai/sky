#!/bin/sh
# Fieldbook — the corpus member's assertion suite.
#
# Runs every backend arm the environment can reach and enforces THE
# load-bearing assertion: the Std.Ui view lowered for Sky.Live and the
# same view handed to Sky.Tui canonicalise to identical structure.
set -u
cd "$(dirname "$0")" || exit 1
# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
. ../../scripts/lib/with-timeout.sh
# `require_tool <name> <hint>` — see scripts/lib/require-tool.sh.
. ../../scripts/lib/require-tool.sh
require_tool curl "install curl — the Sky.Live arm probes the running app over HTTP"

APP=./sky-out/app
FAIL=0

say() { printf '\n== %s\n' "$1"; }
ok()  { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; FAIL=1; }

say "structural dump: live vs tui"
mkdir -p dumps
with_timeout 60 "$APP" --dump-view live > dumps/live.txt 2>dumps/live.err || bad "live dump"
with_timeout 60 "$APP" --dump-view tui  > dumps/tui.txt  2>dumps/tui.err  || bad "tui dump"
if diff -u dumps/live.txt dumps/tui.txt; then
    ok "live and tui structural dumps are byte-identical ($(wc -l < dumps/live.txt) nodes)"
else
    bad "live and tui structural dumps DIVERGE"
fi

say "structural dump: webview vs live"
with_timeout 60 "$APP" --dump-view webview > dumps/webview.txt 2>/dev/null || bad "webview dump"
diff -q dumps/live.txt dumps/webview.txt >/dev/null \
    && ok "webview matches live" || bad "webview diverges from live"

say "in-process gate"
with_timeout 60 "$APP" --dump-view diff && ok "--dump-view diff exit 0" \
    || bad "--dump-view diff non-zero"

say "Sky.Cli export"
with_timeout 60 "$APP" --export dumps/notes.csv >/dev/null 2>&1 \
    && [ "$(wc -l < dumps/notes.csv)" -eq 8 ] \
    && ok "csv export: 1 header + 7 notes" || bad "csv export"

say "Sky.Live arm on an environment-supplied port"
PORT=8433
rm -f dumps/live-server.log
PORT=$PORT nohup "$APP" > dumps/live-server.log 2>&1 &
SRV=$!
i=0
while [ $i -lt 40 ]; do
    grep -q "Sky.Live listening on :$PORT" dumps/live-server.log 2>/dev/null && break
    i=$((i + 1)); sleep 0.25
done
grep -q "fieldbook: listening on $PORT" dumps/live-server.log \
    && ok "readiness line present" || bad "readiness line missing"
grep -q "Sky.Live listening on :$PORT" dumps/live-server.log \
    && ok "bound the PORT from the environment" || bad "did not bind \$PORT"
CODE=$(with_timeout 20 curl -s -o dumps/page.html -w '%{http_code}' "http://127.0.0.1:$PORT/")
[ "$CODE" = "200" ] && ok "GET / -> 200 ($(wc -c < dumps/page.html) bytes)" \
    || bad "GET / -> $CODE"
for tag in "<main" "<nav" "<h1" "<aside" "<footer" "<form" "<svg" "sky-key"; do
    grep -q -- "$tag" dumps/page.html && ok "served html contains $tag" \
        || bad "served html missing $tag"
done
kill $SRV 2>/dev/null
wait $SRV 2>/dev/null

printf '\n'
[ $FAIL -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
exit $FAIL
