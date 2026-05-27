#!/usr/bin/env bash
# Cycle 4 HS — streaming HTTP Playwright probe.
#
# Boots `examples/28-streaming-chat` + the mock streaming server
# (examples/28-streaming-chat/mock/main.go), opens one browser tab,
# submits a prompt, and asserts:
#
#   1. The "Streaming..." live-reply region appears within 1 s of
#      submit (open + first chunk arrives fast).
#   2. The chunk counter monotonically grows to ≥ 5 within 5 s.
#   3. The final history row contains "<end>" (the mock's terminator).
#   4. The activeStream is cleared after Done (live-reply hidden).
#
# Output: PASS / FAIL one-liner + latency numbers.

set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXAMPLE_DIR="$REPO_ROOT/examples/28-streaming-chat"
BINARY="$EXAMPLE_DIR/sky-out/app"
PORT="${SKY_STREAM_PORT:-8128}"
MOCK_PORT="${SKY_MOCK_PORT:-8765}"

# When running from a worktree, ensure the runtime-go used to build
# the example matches the worktree (NOT a sibling clone that the
# probe might pick up first).
export SKY_RUNTIME_DIR="${SKY_RUNTIME_DIR:-$REPO_ROOT/runtime-go}"

if [ ! -x "$BINARY" ]; then
    echo "FAIL verify-streaming-chat"
    echo "    Sky binary missing: $BINARY"
    echo "    build first: (cd $EXAMPLE_DIR && sky build src/Main.sky)"
    exit 2
fi

# Kill any process holding the ports (zombies from prior runs).
for p in "$PORT" "$MOCK_PORT"; do
    pid=$(lsof -ti ":$p" 2>/dev/null || true)
    [ -n "$pid" ] && kill -9 $pid 2>/dev/null || true
done

# Spawn the mock streaming server.
(
    cd "$EXAMPLE_DIR/mock"
    go run main.go -addr ":$MOCK_PORT" -chunkMs 100 -chunks 20 \
        > "$EXAMPLE_DIR/mock.log" 2>&1 &
    echo $! > "$EXAMPLE_DIR/mock.pid"
)
MOCK_PID=$(cat "$EXAMPLE_DIR/mock.pid")

# Spawn the Sky.Live app.
(
    cd "$EXAMPLE_DIR"
    SKY_LIVE_PORT="$PORT" \
    SKY_DEV_BANNER=off \
    SKY_CONSOLE_EMBED=off \
    "$BINARY" > "$EXAMPLE_DIR/stream-app.log" 2>&1 &
    echo $! > "$EXAMPLE_DIR/stream-app.pid"
)
APP_PID=$(cat "$EXAMPLE_DIR/stream-app.pid")

cleanup() {
    if [ -n "${APP_PID:-}" ]; then
        kill -9 "$APP_PID" 2>/dev/null || true
    fi
    if [ -n "${MOCK_PID:-}" ]; then
        kill -9 "$MOCK_PID" 2>/dev/null || true
    fi
    rm -f "$EXAMPLE_DIR/stream-app.pid" "$EXAMPLE_DIR/mock.pid"
}
trap cleanup EXIT INT TERM

# Wait for mock to listen.
for i in $(seq 1 50); do
    if curl -fsS -o /dev/null "http://localhost:$MOCK_PORT/healthz" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
if ! curl -fsS -o /dev/null "http://localhost:$MOCK_PORT/healthz" 2>/dev/null; then
    echo "FAIL verify-streaming-chat"
    echo "    mock server failed to listen on :$MOCK_PORT within 5s"
    tail -20 "$EXAMPLE_DIR/mock.log" | sed 's/^/    /'
    exit 1
fi

# Wait for Sky.Live to listen.
for i in $(seq 1 50); do
    if curl -fsS -o /dev/null "http://localhost:$PORT/" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
if ! curl -fsS -o /dev/null "http://localhost:$PORT/" 2>/dev/null; then
    echo "FAIL verify-streaming-chat"
    echo "    Sky.Live failed to listen on :$PORT within 5s"
    tail -20 "$EXAMPLE_DIR/stream-app.log" | sed 's/^/    /'
    exit 1
fi

# Drive the Playwright probe.
SKY_STREAM_PORT="$PORT" SKY_MOCK_PORT="$MOCK_PORT" \
    node "$REPO_ROOT/scripts/verify-streaming-chat.mjs"
status=$?

if [ $status -ne 0 ]; then
    echo ""
    echo "--- Sky.Live app log (tail) ---"
    tail -30 "$EXAMPLE_DIR/stream-app.log" | sed 's/^/    /'
    echo ""
    echo "--- mock server log (tail) ---"
    tail -30 "$EXAMPLE_DIR/mock.log" | sed 's/^/    /'
fi

exit $status
