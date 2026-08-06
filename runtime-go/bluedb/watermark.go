package bluedb

import "sync"

// watermarkRegistry is the WatermarkRegistry (§5.2): it atomically picks a reader's
// readTs and records its token in one critical section (closes the grill 2a TOCTOU),
// reports the persisted, monotone GC threshold T, and ADVANCES T behind a register
// barrier (advanceThreshold — candidate floor = min over live tokens, high-water when
// the live set is empty). The phase-1b GC pass (gc.go) drives advanceThreshold, then
// issues the physical-only deletes below T.
type watermarkRegistry struct {
	mu     sync.Mutex
	nextID ReaderToken
	live   map[ReaderToken]HLC
	// highWater reads the committer's current HLC high-water under the same lock the
	// (future) GC-floor read will take, so a registration is never invisible to a
	// concurrent floor computation.
	highWater func() HLC
	threshold HLC // T — persisted, monotone (advanced by phase-1b GC)
}

func newWatermarkRegistry(highWater func() HLC, persistedThreshold HLC) *watermarkRegistry {
	return &watermarkRegistry{
		live:      make(map[ReaderToken]HLC),
		highWater: highWater,
		threshold: persistedThreshold,
	}
}

func (w *watermarkRegistry) Register() (ReaderToken, HLC, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	readTs := w.highWater()
	if readTs.Less(w.threshold) {
		// Defensive: unreachable under the register-before-advance barrier.
		return 0, HLC{}, ErrSnapshotTooOld
	}
	w.nextID++
	tok := w.nextID
	w.live[tok] = readTs
	return tok, readTs, nil
}

func (w *watermarkRegistry) Advance(tok ReaderToken, readTs HLC) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if readTs.Less(w.threshold) {
		return ErrSnapshotTooOld
	}
	if _, ok := w.live[tok]; ok {
		w.live[tok] = readTs
	}
	return nil
}

func (w *watermarkRegistry) Release(tok ReaderToken) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.live, tok)
}

func (w *watermarkRegistry) Threshold() HLC {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.threshold
}

// minLive returns the candidate GC floor: min over live tokens, or the current
// high-water when the live set is empty (the load-bearing empty-set rule, §5.2).
// Read under the registry lock so a registration is never invisible to a
// concurrent floor computation. Callers that ADVANCE the threshold must use
// advanceThreshold (which computes the candidate AND commits it under one lock —
// the register-before-advance barrier); minLive alone is a lock-consistent read.
func (w *watermarkRegistry) minLive() HLC {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.candidateLocked()
}

// candidateLocked computes the GC-floor candidate assuming w.mu is held: min over
// live tokens, or the current high-water when the live set is empty (§5.2).
func (w *watermarkRegistry) candidateLocked() HLC {
	if len(w.live) == 0 {
		return w.highWater()
	}
	var min HLC
	first := true
	for _, ts := range w.live {
		if first || ts.Less(min) {
			min, first = ts, false
		}
	}
	return min
}

// advanceThreshold is the register-before-advance barrier (§5.2 part iii). In ONE
// critical section it computes the candidate floor (min over live tokens, or the
// high-water when the live set is empty) AND, iff the candidate is strictly greater
// than the current threshold, commits it as the new T. Because the candidate is both
// computed and stored under the same lock Register/Advance take, no in-flight
// registration can sit below the new T: any token that will exist below the candidate
// has already been recorded (and pulled the candidate down), and any token registered
// after the barrier picks readTs >= high-water >= the new T. Returns the current
// threshold and whether it moved. T only ever moves UP (monotone).
func (w *watermarkRegistry) advanceThreshold() (HLC, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	candidate := w.candidateLocked()
	if w.threshold.Less(candidate) {
		w.threshold = candidate
		return candidate, true
	}
	return w.threshold, false
}

// setThresholdAtLeast raises the in-memory threshold to at least T (used when a
// persisted T is loaded or re-affirmed). Monotone: never lowers.
func (w *watermarkRegistry) setThresholdAtLeast(t HLC) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.threshold.Less(t) {
		w.threshold = t
	}
}
