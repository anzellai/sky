package rt

import (
	"sync"
	"time"
)

// The Sky.Spa client holds network work while its page is leaving.
//
// `beforeunload` fires when a navigation starts (a link to a server page, a
// typed address, `location.assign`). WebKit then cancels every request in
// flight, and the retry coordinator re-sends a cancelled RPC after its
// back-off. A request started on a page that is unloading never reaches the
// server: WebKit refuses it at the call site and reports "Fetch API cannot
// load … due to access control checks" as a page error. So, as the Sky.Live
// client does (`__skyLeavingUntil`), the page counts as leaving for
// spaLeaveWindow after `beforeunload`, and fetchOnce starts no attempt until
// it ends. If the page goes away, nothing was sent. If it stays (a download, a
// 204 answer, a navigation the browser abandoned), the window ends and the
// request proceeds as before. A back/forward cache restore (`pageshow` with
// `persisted`) ends it at once.
//
// The boot loader (spa_boot.go) listens for `beforeunload` from the moment the
// page loads and keeps the time in `__skyLeavingUntil`. A navigation can start
// while the wasm downloads or before the client runs (the first RPC,
// SpaInit_, goes out as the client starts): the client takes that time when
// it starts (adoptBoot, spa_leave_wasm.go) and listens itself from then on.
const spaLeaveWindow = 10 * time.Second

type spaLeaveGate struct {
	mu    sync.Mutex
	until time.Time
	// stay is closed when the page is known to stay (pageshow) or the window
	// has ended, waking every attempt that waits.
	stay chan struct{}
}

var spaLeave spaLeaveGate

// leave records that a navigation has started. A second `beforeunload`
// extends the window.
func (g *spaLeaveGate) leave(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.until = now.Add(spaLeaveWindow)
	if g.stay == nil {
		g.stay = make(chan struct{})
	}
}

// adoptBoot takes the leaving time the boot loader recorded before the
// client started (spa_boot.go keeps it in `__skyLeavingUntil`, milliseconds
// since the epoch, 0 when the page is not leaving). A navigation that started
// while the wasm downloaded or booted then holds the client's first requests
// too. A time in the past, or past the window from now, is not taken as is:
// the first means the page stayed, the second is clamped to the window.
func (g *spaLeaveGate) adoptBoot(now time.Time, untilMs float64) {
	if !(untilMs > 0) {
		return
	}
	until := time.UnixMilli(int64(untilMs))
	if !until.After(now) {
		return
	}
	if max := now.Add(spaLeaveWindow); until.After(max) {
		until = max
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.until) {
		g.until = until
	}
	if g.stay == nil {
		g.stay = make(chan struct{})
	}
}

// stayed records that the page is in use again (a pageshow from the
// back/forward cache): waiting attempts proceed at once.
func (g *spaLeaveGate) stayed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.endLocked()
}

func (g *spaLeaveGate) endLocked() {
	g.until = time.Time{}
	if g.stay != nil {
		close(g.stay)
		g.stay = nil
	}
}

// hold reports how much longer the page counts as leaving at `now`, and the
// channel that ends the wait early. It is zero when the page is not leaving.
func (g *spaLeaveGate) hold(now time.Time) (time.Duration, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.until.IsZero() {
		return 0, nil
	}
	if !now.Before(g.until) {
		g.endLocked()
		return 0, nil
	}
	return g.until.Sub(now), g.stay
}

// wait blocks while the page is leaving. It returns at once when it is not.
func (g *spaLeaveGate) wait(now func() time.Time, after func(time.Duration) <-chan time.Time) {
	for {
		d, stay := g.hold(now())
		if d <= 0 {
			return
		}
		select {
		case <-after(d):
		case <-stay:
		}
	}
}
