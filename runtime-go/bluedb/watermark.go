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
	// durableHi reads the highest DURABLY-applied commitTs (advanced by the committer
	// only after Apply(Sync) returns). advanceThreshold clamps its candidate to this so
	// the persisted GC threshold T can never exceed what's durable on the WAL (Fix-3).
	// nil ⇒ no clamp (a hand-built registry with no engine behind it, e.g. unit tests).
	durableHi func() HLC
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

// RegisterAt records a token at an EXPLICIT readTs (the begin-snapshot path, §3.4). Unlike
// Register (which picks readTs = current high-water), the caller pins readTs = durableHi and
// hands it here so the snapshot and the token share exactly that timestamp (R-2.8). Still
// enforces readTs >= T (the Fix-3 clamp keeps T ≤ durableHi = readTs, so it never trips).
func (w *watermarkRegistry) RegisterAt(readTs HLC) (ReaderToken, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if readTs.Less(w.threshold) {
		return 0, ErrSnapshotTooOld
	}
	w.nextID++
	tok := w.nextID
	w.live[tok] = readTs
	return tok, nil
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
	// Read durableHi BEFORE taking w.mu (Fix-3): durableHi only moves UP, so a value
	// read slightly early is at most stale-low → clamps MORE conservatively, always
	// correctness-safe, and reading outside w.mu keeps the two locks (w.mu, engine
	// durMu) strictly non-nested so there is no lock-ordering hazard vs the committer.
	haveDur := w.durableHi != nil
	var dur HLC
	if haveDur {
		dur = w.durableHi()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	candidate := w.candidateLocked()
	if haveDur {
		// Clamp T ≤ durableHi. Clamping DOWN is always safe: a live token at in-memory
		// readTs > durableHi stays protected (T ≤ durableHi ≤ readTs), and this
		// guarantees persisted_T ≤ durableHi ≤ durable hlc_hi unconditionally — closing
		// both the reader-wedge and the changelog-trim-tail failure modes (§5.2, Fix-3).
		candidate = minHLC(candidate, dur)
	}
	if w.threshold.Less(candidate) {
		w.threshold = candidate
		return candidate, true
	}
	return w.threshold, false
}

// minHLC returns the lesser of two HLCs in the total order.
func minHLC(a, b HLC) HLC {
	if b.Less(a) {
		return b
	}
	return a
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
