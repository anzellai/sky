package bluedb

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// errCorruptChangelogKey is returned by Tail when a key in the changelog keyspace does not
// parse as a changelog key. It MUST fail the read rather than skip the entry — see Tail.
var errCorruptChangelogKey = errors.New("bluedb: malformed changelog key")

// changelog reads the commitTs-ordered post-commit stream (§4). The engine owns
// WHERE it lives (tag 0x01, keyed by commitTs, crash-atomic in the commit batch)
// and HOW it is ordered; the PAYLOAD is opaque L1 bytes (an L2-owned encoding).
type changelog struct {
	db *pebble.DB

	// e is the owning engine, present iff this changelog was handed out through
	// Engine.Changelog() — i.e. iff it can outlive a Close. Tail takes the engine's
	// check-and-pin (N4) when it is set; see Tail.
	//
	// A NIL e means "provably not raceable with Close", and there are exactly two such
	// constructions, both inside this package, both argued rather than assumed:
	//
	//   - openWith's cold-start ring seed, which runs BEFORE the engine value is returned
	//     to anyone, so no caller can hold a handle on which to call Close;
	//   - changelogTailChanges (committer.go), which runs ON the committer goroutine —
	//     Close's phase 2 wg.Wait()s that goroutine to completion before phase 3 closes
	//     the handle, so the drain barrier already covers it.
	//
	// Any THIRD construction site must pass an engine. A raw *pebble.DB with no lifecycle
	// is what GAP 1 was.
	e *pebbleEngine
}

var _ Changelog = (*changelog)(nil)

// Tail returns every entry with commitTs in (after, +inf), ascending, up to the
// current high-water — O(commits-since-after), a bounded recent-tail walk, not an
// O(N) scan. after.IsZero() reads from the beginning.
//
// FAILS CLOSED on a malformed key. Stage-1 made changelogTsOf return (HLC, bool); the
// remedy here is an ERROR, and it MUST NOT be `continue` — by mechanical analogy with
// gc.go it is the single easiest way to break serializability in this package. Tail backs
// changelogTailChanges, the Fix-8 spill fallback that computes a transaction's SSI
// validation window. Skipping a malformed key silently drops a COMMITTED change out of
// that window, so a phantom whose conflicting change sits at that key is never seen:
// under-rejection, i.e. a serializability break, exactly the class validate.go's contract
// forbids. changelogTailChanges already converts an error into ErrConflict (the driver
// re-Begins at a fresher readTs), so failing closed is both correct and already plumbed.
// It also FAILS CLOSED on a concurrent Close, and that is a distinct hazard (N4). A
// Changelog handed out by Engine.Changelog() is an exported, caller-retained value, so
// Tail can be entered at any point in the engine's life — including while Close is
// running. c.db.NewIter on a closed Pebble handle does not return an error, it PANICS
// ("pebble: closed", db.go), and that panic surfaces on the CALLER's goroutine where no
// recover in this package can reach it. The check-and-pin below is the same one
// beginSnapshot takes: once it is held, Close's phase-2 drain cannot complete — and
// therefore phase 3 cannot close the handle — until this read has returned. ErrClosed is
// the answer on the other side of the barrier, which Tail's error result already carries.
//
// The unpin is deferred FIRST so it runs LAST, after `defer iter.Close()`: the pin must
// outlive every use of the iterator, not merely the NewIter call. Entries are copied out
// into fresh slices, so nothing returned points into Pebble's memory once it is dropped.
func (c *changelog) Tail(after HLC) ([]ChangelogEntry, error) {
	if c.e != nil {
		tok, err := c.e.pinIfOpen()
		if err != nil {
			return nil, err
		}
		defer c.e.reg.unpin(tok)
	}

	lo, hi := changelogKeyspaceBounds()
	iter, err := c.db.NewIter(&pebble.IterOptions{LowerBound: lo, UpperBound: hi})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	// Seek to the first entry strictly after `after`. Changelog keys are
	// non-inverted, so ascending byte order == chronological.
	var seek []byte
	if after.IsZero() {
		seek = lo
	} else {
		seek = changelogKeyAfter(after)
	}

	var out []ChangelogEntry
	for ok := iter.SeekGE(seek); ok; ok = iter.Next() {
		ts, tsOK := changelogTsOf(iter.Key())
		if !tsOK {
			return nil, fmt.Errorf("%w: %x", errCorruptChangelogKey, iter.Key())
		}
		if !after.IsZero() && !after.Less(ts) {
			continue // defensive: skip anything <= after
		}
		v := iter.Value()
		out = append(out, ChangelogEntry{
			CommitTs: ts,
			Payload:  append([]byte(nil), v...),
		})
	}
	return out, iter.Error()
}

// changelogKeyAfter returns the smallest changelog key strictly greater than every
// key at commitTs == after (i.e. the start of the (after, +inf) tail). Since a
// changelog key is 0x01 ‖ ts ‖ 0x00, appending one more 0x00 yields the immediate
// successor of that key.
func changelogKeyAfter(after HLC) []byte {
	return append(encodeChangelogKey(after), 0x00)
}
