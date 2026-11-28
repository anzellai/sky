#!/usr/bin/env bash
#
# Run the Go runtime's timing-dependent tests many times, under the race
# detector, with a small GOMAXPROCS.
#
# WHY THIS EXISTS
# ---------------
# `Http.Stream.forEachChunk` could return Ok with only part of a finished
# stream: a Go `select` picked the `done` case over chunks still queued (fixed
# in f7fd47176, with a deterministic regression test). Every earlier run of its
# test passed. One per-commit macOS run out of about 40 hit it by chance. A
# gate that trusts ONE green run of a test whose outcome depends on goroutine
# scheduling cannot see that class. This script runs those tests -count times
# with few OS threads, which makes the unlucky interleavings common, so a
# release only ships after each of them has passed many times on each platform.
#
# The selection is the tests that use goroutines, `select`, deadlines or
# sleeps in the stream, socket, Task deadline / spawn / parallel, Std.Sync and
# goroutine-context paths. Add a prefix to STRESS_RUN when a new test of that
# kind is written. One -count=1 pass takes about 10 s on a laptop.
#
# USAGE
#   go-test-stress.sh <count> <gomaxprocs>     (run from runtime-go/)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/with-timeout.sh
source "$ROOT/scripts/lib/with-timeout.sh"
# shellcheck source=../lib/require-tool.sh
source "$ROOT/scripts/lib/require-tool.sh"

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <count> <gomaxprocs>" >&2
  exit 2
fi
COUNT="$1"
PROCS="$2"
case "$COUNT$PROCS" in
  *[!0-9]*) echo "go-test-stress: count and gomaxprocs must be integers" >&2; exit 2 ;;
esac

require_tool go "install Go 1.26 (https://go.dev/dl/)"

STRESS_RUN='^(TestHttpStream|TestStream|TestTaskTimeout|TestTaskRemaining|TestTaskSpawn|TestTaskParallel|TestGoSky|TestGoroutineContext|TestSync|TestWs|TestWebSocket|TestServerWebSocket|TestCloseAll|TestHttp_|TestEmailSend_|TestAi_|TestAiStore_|TestSourceRunner|TestWithSpan|TestRunWithRequestID|TestLiveSession_MarkDone|TestApplyStreamSubsDiff|TestSweepSessionless)'

cd "$ROOT/runtime-go"

# A selection that matches nothing would report `ok` having run nothing.
selected="$(CGO_ENABLED=1 go test -race -list "$STRESS_RUN" ./rt | grep -c '^Test' || true)"
if [ "$selected" -lt 50 ]; then
  echo "go-test-stress: the selection matches only $selected tests (expected at least 50); a rename dropped them" >&2
  exit 1
fi
echo "go-test-stress: $selected tests x $COUNT runs, GOMAXPROCS=$PROCS, -race"

# go's own -timeout names the hung test and dumps stacks; with_timeout is the
# backstop for a hang outside a test (the build, TestMain).
GOMAXPROCS="$PROCS" CGO_ENABLED=1 with_timeout 1800 \
  go test -race -count="$COUNT" -timeout 25m -run "$STRESS_RUN" ./rt
