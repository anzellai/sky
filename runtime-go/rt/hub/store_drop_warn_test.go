package hub

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer — log.Printf can fire from the batcher goroutine while the test
// reads, so the capture buffer is mutex-guarded rather than bare.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog redirects the standard logger into a buffer for the test's
// lifetime.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

func hubWarnLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "[sky.hub]") {
			out = append(out, line)
		}
	}
	return out
}

func logItem(msg string) pendingItem {
	return pendingItem{
		kind:        signalLog,
		ts:          time.Now(),
		serviceName: "svc",
		level:       "info",
		message:     msg,
	}
}

// TestHubStoreSaturationWarnsOncePerEpoch — Insert's docstring says a burst
// that fills the channel "surfaces as a single warn log line per epoch so the
// operator notices without a flood". It did not surface at all: the drop path
// incremented a counter and returned, and that counter is exposed only through
// `Stats()`, which nothing scrapes on the hub. Telemetry the hub was asked to
// keep vanished with no line anywhere saying so.
//
// The Store is built as a literal on purpose. Insert's drop accounting is
// entirely local to Insert, and a real newStore spawns a batcher that races the
// producer — a test that has to out-run a goroutine to observe a drop is
// asserting it won a race. Nothing is skipped by doing this: the epoch window
// is read through `dropWarnWindow()`, which supplies the default itself, so
// there is no constructor wiring for a literal to miss (the same shape as
// `flushInterval()` over its override).
func TestHubStoreSaturationWarnsOncePerEpoch(t *testing.T) {
	buf := captureLog(t)

	const cap = 4
	s := &Store{queue: make(chan pendingItem, cap)}

	// First burst: 4 accepted, 6 dropped.
	first := make([]pendingItem, 10)
	for i := range first {
		first[i] = logItem(fmt.Sprintf("first-%d", i))
	}
	s.Insert(first)

	if _, dropped := s.Stats(); dropped != 6 {
		t.Fatalf("dropped = %d, want 6 (cap %d, sent %d)", dropped, cap, len(first))
	}
	lines := hubWarnLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 warn line for the first epoch, got %d:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "dropped 6 telemetry") {
		t.Errorf("warn line does not report HOW MANY were dropped: %q\n"+
			"  'something was dropped' without a magnitude is not actionable", lines[0])
	}

	// Same epoch: more drops, still no second line. This is the half that
	// makes the warning usable — a saturated hub drops thousands of items a
	// second, and a line each would bury the one that matters.
	second := make([]pendingItem, 5)
	for i := range second {
		second[i] = logItem(fmt.Sprintf("second-%d", i))
	}
	s.Insert(second)
	if _, dropped := s.Stats(); dropped != 11 {
		t.Fatalf("dropped = %d, want 11 after the second burst", dropped)
	}
	if lines := hubWarnLines(buf.String()); len(lines) != 1 {
		t.Errorf("want 1 warn line for the whole epoch, got %d — the flood the "+
			"docstring promises to prevent:\n%s", len(lines), strings.Join(lines, "\n"))
	}

	// New epoch: one more line, counting every drop SINCE THE LAST LINE — the
	// 5 suppressed inside the epoch plus the 3 new ones. Reporting only the
	// latest burst would let the suppressed ones disappear, which would make
	// the rate limit a second way to lose data quietly.
	s.dropWarnWindowNanos.Store(1)
	third := make([]pendingItem, 3)
	for i := range third {
		third[i] = logItem(fmt.Sprintf("third-%d", i))
	}
	s.Insert(third)

	lines = hubWarnLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("want a 2nd warn line once the epoch expired, got %d:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[1], "dropped 8 telemetry") {
		t.Errorf("2nd warn line %q should report the 8 dropped since the 1st line "+
			"(5 suppressed inside the epoch + 3 new)", lines[1])
	}
}

// TestHubStoreDropWarnWindowHasANonZeroDefault — a zero window would turn the
// rate limit off and make every dropped item its own log line, which is the
// flood the epoch exists to prevent. The default lives in the reader, so this
// covers a literal and a newStore-built Store alike.
func TestHubStoreDropWarnWindowHasANonZeroDefault(t *testing.T) {
	var s Store
	if got := s.dropWarnWindow(); got <= 0 {
		t.Fatalf("dropWarnWindow() = %v, want a positive default", got)
	}
	if got := s.dropWarnWindow(); got != defaultDropWarnWindow {
		t.Errorf("dropWarnWindow() = %v, want %v", got, defaultDropWarnWindow)
	}
}
