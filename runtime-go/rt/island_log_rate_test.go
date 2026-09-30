package rt

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

func islandDropLines(marker string) int {
	n := 0
	for _, e := range telemetry.Default().RecentLogs(0) {
		if strings.Contains(e.Message, marker) {
			n++
		}
	}
	return n
}

// TestIslandDropLog_IsRateLimited is the D-8 regression: every malformed or
// undecodable widget-island event wrote one warn line, and a client can send
// such events in a loop, so the log volume was the client's to choose. The
// lines are bounded per minute, and the count of the ones not logged is
// reported, never lost.
func TestIslandDropLog_IsRateLimited(t *testing.T) {
	islandDropLog.mu.Lock()
	islandDropLog.windowFrom, islandDropLog.logged, islandDropLog.suppressed = time.Now(), 0, 0
	islandDropLog.mu.Unlock()
	marker := fmt.Sprintf("island-flood-%d", time.Now().UnixNano())
	for i := 0; i < 10000; i++ {
		islandLogDropLimited(marker)
	}
	if n := islandDropLines(marker); n > islandDropLogMax || n == 0 {
		t.Fatalf("10000 dropped events wrote %d lines, want 1 to %d", n, islandDropLogMax)
	}
	islandDropLog.mu.Lock()
	suppressed := islandDropLog.suppressed
	islandDropLog.windowFrom = time.Now().Add(-2 * islandDropLogWindow)
	islandDropLog.mu.Unlock()
	if suppressed != 10000-islandDropLogMax {
		t.Fatalf("suppressed = %d, want %d", suppressed, 10000-islandDropLogMax)
	}
	islandLogDropLimited(marker)
	summary := fmt.Sprintf("%d more widget events were dropped", suppressed)
	if islandDropLines(summary) == 0 {
		t.Fatalf("the next window did not report the %d events it did not log", suppressed)
	}
}
