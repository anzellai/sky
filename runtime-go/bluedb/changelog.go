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
func (c *changelog) Tail(after HLC) ([]ChangelogEntry, error) {
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
