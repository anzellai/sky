package bluedb

import (
	"math"
	"sync"
	"time"
)

// HLC is the hybrid-logical-clock commit timestamp assigned by the single
// committer: physical wall-clock milliseconds + a logical tie-breaker (§2.3).
type HLC struct {
	WallMs  uint64
	Logical uint32
}

// Less reports whether h sorts strictly before o in the total order.
func (h HLC) Less(o HLC) bool {
	if h.WallMs != o.WallMs {
		return h.WallMs < o.WallMs
	}
	return h.Logical < o.Logical
}

// IsZero reports the {0,0} sentinel (absent metadata / fresh store).
func (h HLC) IsZero() bool { return h.WallMs == 0 && h.Logical == 0 }

// wallClockMillis is the injectable physical clock source (§3.3, §7). Production
// uses time.Now().UnixMilli(); the crash/clock-rewind tests inject a rewindable
// source so the restart floor can be exercised.
type wallClockMillis func() int64

func systemWallClock() int64 { return time.Now().UnixMilli() }

// hlcClock issues strictly-monotonic HLC timestamps. next() is intended to be
// called only on the committer goroutine, but last is mutex-guarded so NowTs() can
// read the high-water from another goroutine safely.
type hlcClock struct {
	mu   sync.Mutex
	last HLC
	now  wallClockMillis
}

// newHLCClock constructs a clock floored to the persisted high-water (§3.3). The
// first next() after this returns STRICTLY greater than persisted regardless of a
// backward wall clock:
//
//	if wall > persisted.WallMs -> last = {wall, 0}   (next bumps logical to 1)
//	else                       -> last = persisted   (next bumps logical / borrows)
func newHLCClock(persisted HLC, now wallClockMillis) *hlcClock {
	if now == nil {
		now = systemWallClock
	}
	c := &hlcClock{now: now}
	wall := uint64(nonNegative(now()))
	if wall > persisted.WallMs {
		c.last = HLC{WallMs: wall, Logical: 0}
	} else {
		c.last = persisted // FLOOR to the persisted high-water
	}
	return c
}

// next returns the next strictly-monotonic commitTs (§3.3). A backward wall step
// MUST NOT re-issue a used commitTs, so the clock is floored to last; a uint32
// logical overflow within one wall-ms borrows into wall+1 and NEVER wraps (a wrap
// would send commitTs backward → two versions collide at one key → silent
// corruption).
func (c *hlcClock) next() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := uint64(nonNegative(c.now()))
	switch {
	case wall > c.last.WallMs:
		c.last = HLC{WallMs: wall, Logical: 0}
	case c.last.Logical == math.MaxUint32:
		c.last = HLC{WallMs: c.last.WallMs + 1, Logical: 0} // borrow — never wrap
	default:
		c.last = HLC{WallMs: c.last.WallMs, Logical: c.last.Logical + 1}
	}
	return c.last
}

// highWater returns the current high-water without advancing (NowTs).
func (c *hlcClock) highWater() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
