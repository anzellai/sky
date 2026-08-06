package bluedb

import "sync"

// watermarkRegistry is the phase-1a WatermarkRegistry: it atomically picks a
// reader's readTs and records its token in one critical section (closes the grill
// 2a TOCTOU), and reports the persisted, monotone GC threshold T.
//
// TODO(phase1b): the GC pass that ADVANCES T behind a register barrier
// (candidate floor = min over live tokens, high-water when empty) — §5.2. Phase
// 1a keeps T at its persisted value (initially {0,0}) and never collects, so
// Register can never be rejected; the token bookkeeping is what a phase-1b GC pass
// will read to compute the candidate floor.
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
// Unused in phase 1a (no GC pass); provided so the phase-1b GC pass reads it under
// the registry lock.
func (w *watermarkRegistry) minLive() HLC {
	w.mu.Lock()
	defer w.mu.Unlock()
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
