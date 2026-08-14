package bluedb

import (
	"bytes"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// pebbleReader is a snapshot-consistent view as of a fixed readTs (§2.5). It pins a
// Pebble snapshot seqnum so a reader mid-scan never observes a version a concurrent
// (phase-1b) GC pass is deleting.
type pebbleReader struct {
	snap   *pebble.Snapshot
	readTs HLC
	tok    ReaderToken
	reg    *watermarkRegistry

	// err latches the FIRST I/O error observed by ANY read taken through this reader —
	// a point read (defect H3) or a scan (defect H3b). Get's (value, commitTs, ok) shape
	// has no error channel and a Cursor that stopped early is shaped exactly like an
	// exhausted one, so an unreadable SSTable would otherwise be reported as ok=false /
	// zero rows — indistinguishable from an absent key / an empty collection. It is
	// latched (never cleared) so a single transient read failure anywhere in the txn's
	// life poisons the whole reader: Txn.Commit consults it and fails closed.
	//
	// Defect H3b — WHY SCAN ERRORS LAND HERE AND NOT ONLY ON THE CURSOR. A Cursor keeps
	// its own Err() (which cursor failed, and why: that is per-cursor diagnosis and is
	// unchanged). But the commit-boundary question is transaction-scoped — "may this
	// txn's read-set be trusted?" — and a cursor is a transient object the body creates,
	// drains and drops. An error reachable ONLY through an object the caller may discard
	// cannot be a commit guarantee; that is verbatim the argument H3 made for Get, and
	// ScanCollection is the exported query surface it was not applied to. So a failed
	// scan poisons the reader too, and Txn.Commit's single existing check covers both.
	err error
}

var _ Reader = (*pebbleReader)(nil)

func (r *pebbleReader) ReadTs() HLC { return r.readTs }

// Err reports the first I/O error a read on this reader hit — point read or scan — or
// nil. See the err field: a non-nil Err means at least one Get's "absent" answer is NOT
// evidence of absence, or at least one scan's row set is NOT evidence of the collection's
// contents, so every consumer of this reader must fail closed rather than act on it.
func (r *pebbleReader) Err() error { return r.err }

// Close releases this reader's pinned view. THE ORDER OF THE TWO STATEMENTS IS THE
// CONTRACT (defect N4, residual arm): the *pebble.Snapshot is closed FIRST and the
// watermark token released LAST.
//
// The token is what the close drain counts (watermark.go's pins set): waitDrained
// returns the instant the last token goes back, and Close's phase 3 then calls
// e.db.Close(). Releasing the token first opens a window — between the release and
// snap.Close() — in which the engine believes no reader is live while a snapshot is
// still registered with pebble. A transaction ending exactly as the drain completes
// lands in it, and e.db.Close() then reports "leaked snapshots: N open snapshots"
// (pebble db.go:1818). Releasing LAST makes the window unrepresentable: the release
// happens-after snap.Close() returns, and the drain observing an empty pin set
// happens-after the release.
//
// Begin()'s reader is the path that needs this. Snapshot()/snapshotAt() wrap their
// reader in a trackedReader whose OUTER pin already spans the whole teardown, but
// beginSnapshot hands its *pebbleReader straight to Txn, whose Commit/Abort call this
// Close directly — so this ordering is that path's only guarantee.
func (r *pebbleReader) Close() {
	if r.snap != nil {
		_ = r.snap.Close()
	}
	// LAST. Do not hoist: see the doc comment.
	if r.reg != nil {
		r.reg.Release(r.tok)
	}
}

// Get resolves userKey as of readTs (§2.5). The seek lands on the newest version
// with commitTs <= readTs (inverted suffix + SeekGE); a tombstone there reads as
// absent. The boundary test is a byte compare of the two PREFIXES (grill C1 fix) —
// never an equality of the two Split() integers, which collide for equal-length
// user-keys and would leak a neighbouring key's value.
//
// Defect H3: a failed positioning is NOT absence. `SeekGE` returns false both when the
// key genuinely has no visible version AND when the block it needed could not be read,
// so the iterator must be interrogated with iter.Error() after positioning; the error is
// latched on r.err and surfaced by Err(). Checking the NewIter error alone fixes NOTHING
// — pebble.Snapshot.NewIter (pebble/v2@v2.1.6 snapshot.go:62-69) returns a nil error
// unconditionally and panics on a closed snapshot instead.
func (r *pebbleReader) Get(userKey []byte) (value []byte, commitTs HLC, ok bool) {
	prefix := dataKeyPrefix(userKey)
	lower := prefix
	upper := append(append([]byte(nil), prefix...), sentinel) // immediate successor
	iter, err := r.snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		// Defence in depth, currently UNREACHABLE for a snapshot: Snapshot.NewIter
		// (snapshot.go:62-69) always returns a nil error. Kept — and latched, not
		// silently dropped — so that if the constructor ever gains a real failure mode
		// (or this reader is repointed at *pebble.DB, whose NewIter CAN error), the
		// failure surfaces as an error rather than as a wrong "absent".
		r.latch(err)
		return nil, HLC{}, false
	}
	defer iter.Close()

	target := encodeDataKey(userKey, r.readTs)
	seeked := iter.SeekGE(target)
	if e := iter.Error(); e != nil {
		// THE H3 fix. An unreadable SSTable/block reaches us as (!seeked, Error() != nil);
		// without this branch it is laundered into "the row is absent", which is how a
		// swallowed I/O error becomes an unwanted INSERT at the commit boundary.
		r.latch(e)
		return nil, HLC{}, false
	}
	if !seeked || !iter.Valid() {
		return nil, HLC{}, false
	}
	k := iter.Key()
	if !bytes.Equal(k[:skydbSplit(k)], prefix) {
		return nil, HLC{}, false // fell off this user-key → no visible version <= readTs
	}
	ts, tsOK := decodeDataVersion(k)
	if !tsOK {
		// Return ABSENT, never a {0,0} commitTs. A zero HLC leaking into
		// pointRead.versionSeen is precisely the fresh-store-sentinel confusion the ok
		// flag exists to prevent (HLC.IsZero is the "empty store" marker), and the
		// read-set would then carry a dependency on a version that never existed.
		return nil, HLC{}, false
	}
	v := iter.Value()
	if e := iter.Error(); e != nil {
		// A lazily-fetched value can fail its block read AFTER positioning succeeded. Same
		// contract: never report an I/O failure as an answer about the row.
		r.latch(e)
		return nil, HLC{}, false
	}
	if len(v) == 0 || v[0] == markerTombstone {
		return nil, ts, false // visible version is a delete → absent as-of readTs
	}
	out := append([]byte(nil), v[1:]...)
	return out, ts, true
}

// latch records the FIRST read I/O error on this reader (point read or scan). First-wins:
// the earliest failure is the one that explains any downstream "absent" / "empty", and a
// later error must not overwrite it (nor must a later SUCCESS clear it — a reader that has
// once lied about absence stays poisoned for its whole life, because the read-set it fed is
// already built). A nil error is NOT a latch: pebbleCursor.Next calls this with
// iter.Error() on every exhaustion, and clean exhaustion is the overwhelmingly common case.
func (r *pebbleReader) latch(err error) {
	if err == nil {
		return
	}
	if r.err == nil {
		r.err = err
	}
}

// failedCursor returns a cursor that is born failed, latching the reason on the reader
// first. Every "this scan cannot run" exit in Iterate goes through it, so no such exit can
// produce a cursor whose error is invisible at the commit boundary (defect H3b).
func (r *pebbleReader) failedCursor(err error) Cursor {
	r.latch(err)
	return &pebbleCursor{err: err, owner: r}
}

// Iterate returns an ordered, snapshot-consistent cursor over distinct user-keys
// sharing prefix (nil ⇒ whole data keyspace), taking the newest visible version
// <= readTs at each and skipping tombstones (§2.5). O(log n + k).
//
// BOTH bounds MUST end in 0x00 (defect N1). skydbSplit reads a key's TRAILING byte
// as a suffix length, so a bound of length L ending in byte v is misparsed as
// prefix=bound[:L-v] iff 0 < v <= L-1. The naive `tagData ‖ prefix` ends in an
// arbitrary user byte and detonates: with prefix = collName ‖ 0x1F, n <= 29 is
// correct, n == 30 collapses the lower bound's prefix to [0x00] — the WHOLE data
// keyspace, so another collection's rows are returned, decoded and predicate-matched
// as if they belonged — and n >= 31 inverts lower > upper, which Pebble scans as
// zero rows with no error. Content-independent failure begins at len(prefix) >= 255
// for arbitrary bytes, or >= 127 for an ASCII tail.
//
// The fix is here, in the CALLER. It is NOT in skydbSplit: comparerName
// "skydb.mvcc.v1" is frozen into every SSTable's metadata, and changing Split
// changes SSTable ordering and breaks the leading-byte-stripping invariant
// base.CheckComparer enforces — it would require skydb.mvcc.v2 plus a full store
// rewrite. On-disk bytes are unaffected by this function: no bound built here is
// ever persisted (the module's only persisted bound is gc.go's DeleteRange pair,
// which is well-formed independently).
func (r *pebbleReader) Iterate(prefix []byte) Cursor {
	// tagData ‖ prefix ‖ 0x00 — ends in 0x00, so Split returns len and it is a bare
	// prefix. It is <= every data key whose user-key begins with prefix (equal-prefix
	// keys carry a 13-byte suffix, and the empty suffix sorts first).
	lower := dataKeyPrefix(prefix)
	upper := dataScanUpper(prefix)
	// Structural check, not a cover-up: Pebble carries no production assertion on an
	// inverted bound pair — it simply yields zero rows with Err() == nil, which is
	// indistinguishable from an empty collection. That silence IS the second half of
	// N1. With the bounds above it can never fire (succ differs from lower at an index
	// inside lower, upward); it exists so any future bound-construction regression is
	// loud instead of returning a wrong-but-plausible empty result.
	if skydbCompare(lower, upper) >= 0 {
		return r.failedCursor(fmt.Errorf(
			"bluedb: inverted data-scan bounds for prefix %x: [% x, % x)", prefix, lower, upper))
	}
	iter, err := r.snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return r.failedCursor(err)
	}
	return &pebbleCursor{iter: iter, readTs: r.readTs, lower: lower, owner: r}
}

// dataScanUpper returns the exclusive upper bound for a data-keyspace scan over
// user-keys beginning with prefix. For an empty prefix it is the whole data
// keyspace (up to the changelog tag).
//
// The returned bound always ends in 0x00 (or is a self-evidently bare one-byte tag),
// so skydbSplit returns len and never mis-reads it as a suffix — see Iterate (N1).
func dataScanUpper(prefix []byte) []byte {
	if len(prefix) == 0 {
		// Whole data keyspace. []byte{tagChangelog} is the first key of the next
		// namespace and is self-evidently bare (len 1, so Split's F2 guard returns
		// len). [0x01, 0x00] — what the successor path below would produce — is also
		// correct, but only by a non-obvious argument; prefer the evident form.
		return []byte{tagChangelog}
	}
	base := append([]byte{tagData}, prefix...)
	succ := bytesSuccessor(base)
	if succ == nil {
		// UNREACHABLE: base[0] == tagData == 0x00 != 0xFF, so a successor always
		// exists. Kept rather than dropped because falling through to
		// append(nil, sentinel) would yield []byte{0x00} — an upper bound BELOW every
		// data key, i.e. a silent zero-row scan.
		return []byte{tagChangelog}
	}
	// succ's last byte is (non-0xFF)+1 ∈ [0x01, 0xFF], so succ never ends in 0x00
	// while every stored key's Split-prefix always does. No valid prefix can equal
	// succ, so [succ, succ‖0x00) contains no valid prefix and succ‖0x00 excludes
	// exactly what bare succ would — while being a well-formed bare bound.
	return append(succ, sentinel)
}

// bytesSuccessor returns the smallest byte string strictly greater than every
// string that has b as a prefix: increment the last non-0xFF byte and truncate.
// Returns nil if b is all 0xFF (no finite successor).
func bytesSuccessor(b []byte) []byte {
	out := append([]byte(nil), b...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] != 0xFF {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}

// pebbleCursor walks distinct visible user-keys in ascending order.
type pebbleCursor struct {
	iter   *pebble.Iterator
	readTs HLC
	lower  []byte

	started    bool
	lastPrefix []byte

	curKey []byte
	curVal []byte
	curTs  HLC
	err    error

	// owner is the reader this cursor scans through, or nil for a cursor built outside a
	// reader (deadReader's). Defect H3b: a scan failure is latched on it as well as kept
	// here, because a cursor's Err() is only as good as the caller's discipline in reading
	// it, while the reader's latch is what Txn.Commit already consults.
	owner *pebbleReader
}

var _ Cursor = (*pebbleCursor)(nil)

// fail records a scan error on BOTH this cursor (per-cursor diagnosis: which scan failed)
// and the owning reader (per-transaction poison: the commit boundary's fail-closed check).
// A nil error is a no-op — Next calls this on every exhaustion, clean or not.
func (c *pebbleCursor) fail(err error) {
	if err == nil {
		return
	}
	if c.err == nil {
		c.err = err
	}
	if c.owner != nil {
		c.owner.latch(err)
	}
}

func (c *pebbleCursor) Next() bool {
	if c.err != nil || c.iter == nil {
		return false
	}
	for {
		if !c.started {
			c.iter.SeekGE(c.lower)
			c.started = true
		} else {
			// Jump past every older version of the last visited prefix.
			c.iter.SeekGE(append(append([]byte(nil), c.lastPrefix...), sentinel))
		}
		if !c.iter.Valid() {
			c.fail(c.iter.Error())
			return false
		}
		k := c.iter.Key()
		prefix := append([]byte(nil), k[:skydbSplit(k)]...)
		c.lastPrefix = prefix

		// Seek to the newest version <= readTs within this prefix.
		userKey := userKeyOfPrefix(prefix)
		if !c.iter.SeekGE(encodeDataKey(userKey, c.readTs)) || !c.iter.Valid() {
			// No version <= readTs for this prefix (and none after) — but there may be
			// a later prefix; continue jumps via lastPrefix.
			if !c.iter.Valid() {
				c.fail(c.iter.Error())
				return false
			}
			continue
		}
		vk := c.iter.Key()
		if !bytes.Equal(vk[:skydbSplit(vk)], prefix) {
			// This prefix has no visible version <= readTs; skip to the next prefix.
			continue
		}
		ts, tsOK := decodeDataVersion(vk)
		if !tsOK {
			// SKIP THIS PREFIX. Same reasoning as Get's absent: Cursor.CommitTs() must
			// never hand back a {0,0} sentinel for a real row (scanPrefixMaterialize
			// stores it, and a txn would carry it as a dependency). c.lastPrefix is
			// already this prefix, so the loop's next seek jumps past it.
			continue
		}
		v := c.iter.Value()
		if e := c.iter.Error(); e != nil {
			// Defect H3b, the value arm — the exact sibling of Get's post-Value() check.
			// A lazily-fetched value can fail its block read AFTER positioning succeeded,
			// and pebble hands back an empty slice when it does. Falling through would read
			// len(v) == 0 as markerTombstone's neighbour and `continue` — laundering "this
			// block could not be read" into "this row was deleted", which is the same lie as
			// H3's absent, one row at a time and with no cursor error to show for it.
			c.fail(e)
			return false
		}
		if len(v) == 0 || v[0] == markerTombstone {
			continue // tombstoned as-of readTs — skip
		}
		c.curKey = userKey
		c.curVal = append([]byte(nil), v[1:]...)
		c.curTs = ts
		return true
	}
}

func (c *pebbleCursor) Key() []byte   { return c.curKey }
func (c *pebbleCursor) Value() []byte { return c.curVal }
func (c *pebbleCursor) CommitTs() HLC { return c.curTs }
func (c *pebbleCursor) Err() error    { return c.err }
func (c *pebbleCursor) Close() {
	if c.iter != nil {
		_ = c.iter.Close()
	}
}

// userKeyOfPrefix strips the tag (byte 0) and trailing sentinel (byte len-1) from a
// data-key prefix (0x00 ‖ userKey ‖ 0x00).
func userKeyOfPrefix(prefix []byte) []byte {
	if len(prefix) < 2 {
		return nil
	}
	return prefix[1 : len(prefix)-1]
}
