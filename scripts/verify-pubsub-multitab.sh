#!/usr/bin/env bash
# Cycle 3 P49 — pub/sub multi-tab Playwright probe.
#
# Boots `examples/27-multi-session-chat`, opens two browser contexts
# in the same chatroom, and asserts that messages flow A ↔ B + echo
# back to the publisher within the design-doc SLA (≤ 500 ms intra-
# process). Output:
#
#   PASS verify-pubsub-multitab
#       A→B  latency_ms=<n>
#       B→A  latency_ms=<n>
#       echo latency_ms=<n>
#
# or
#
#   FAIL verify-pubsub-multitab
#       <reason>
#
# Per the P49 brief, this script is the source-of-truth runtime
# evidence that the pub/sub stack from P46 (registry) + P47 (seq
# split) + P48 (dispatch) is wired all the way to two real browser
# sessions.

set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXAMPLE_DIR="$REPO_ROOT/examples/27-multi-session-chat"
BINARY="$EXAMPLE_DIR/sky-out/app"
PORT="${SKY_PUBSUB_PORT:-8127}"

if [ ! -x "$BINARY" ]; then
    echo "FAIL verify-pubsub-multitab"
    echo "    binary missing: $BINARY"
    echo "    build first: (cd $EXAMPLE_DIR && sky build src/Main.sky)"
    exit 2
fi

# Kill any process holding the port (zombie from prior run).
pid=$(lsof -ti ":$PORT" 2>/dev/null || true)
[ -n "$pid" ] && kill -9 $pid 2>/dev/null || true

# Wipe any leftover DB so history starts empty.
rm -f -f "$EXAMPLE_DIR/chat.db"

# Spawn the app. SKY_DEV_BANNER=off keeps the console floating link
# from interfering with Playwright selectors. SKY_CONSOLE_EMBED=off
# skips the console sub-app spawn entirely (we don't need it and it
# adds ~500 ms to boot).
(
    cd "$EXAMPLE_DIR"
    SKY_LIVE_PORT="$PORT" \
    SKY_DEV_BANNER=off \
    SKY_CONSOLE_EMBED=off \
    "$BINARY" > "$EXAMPLE_DIR/pubsub-app.log" 2>&1 &
    echo $! > "$EXAMPLE_DIR/pubsub-app.pid"
)

APP_PID=$(cat "$EXAMPLE_DIR/pubsub-app.pid")

cleanup() {
    if [ -n "${APP_PID:-}" ]; then
        kill -9 "$APP_PID" 2>/dev/null || true
    fi
    rm -f -f "$EXAMPLE_DIR/pubsub-app.pid"
}
trap cleanup EXIT INT TERM

# Wait for the server to accept connections.
for i in $(seq 1 50); do
    if curl -fsS -o /dev/null "http://localhost:$PORT/" 2>/dev/null; then
        break
    fi
    sleep 0.1
done

if ! curl -fsS -o /dev/null "http://localhost:$PORT/" 2>/dev/null; then
    echo "FAIL verify-pubsub-multitab"
    echo "    server failed to listen on :$PORT within 5s"
    tail -20 "$EXAMPLE_DIR/pubsub-app.log" | sed 's/^/    /'
    exit 1
fi

# Drive the Playwright probe.
PUBSUB_PORT="$PORT" node "$REPO_ROOT/scripts/verify-pubsub-multitab.mjs"
status=$?

if [ $status -ne 0 ]; then
    echo ""
    echo "--- app log (tail) ---"
    tail -30 "$EXAMPLE_DIR/pubsub-app.log" | sed 's/^/    /'
fi

exit $status
