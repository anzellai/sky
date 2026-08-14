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
}

var _ Reader = (*pebbleReader)(nil)

func (r *pebbleReader) ReadTs() HLC { return r.readTs }

func (r *pebbleReader) Close() {
	if r.reg != nil {
		r.reg.Release(r.tok)
	}
	if r.snap != nil {
		_ = r.snap.Close()
	}
}

// Get resolves userKey as of readTs (§2.5). The seek lands on the newest version
// with commitTs <= readTs (inverted suffix + SeekGE); a tombstone there reads as
// absent. The boundary test is a byte compare of the two PREFIXES (grill C1 fix) —
// never an equality of the two Split() integers, which collide for equal-length
// user-keys and would leak a neighbouring key's value.
func (r *pebbleReader) Get(userKey []byte) (value []byte, commitTs HLC, ok bool) {
	prefix := dataKeyPrefix(userKey)
	lower := prefix
	upper := append(append([]byte(nil), prefix...), sentinel) // immediate successor
	iter, err := r.snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, HLC{}, false
	}
	defer iter.Close()

	target := encodeDataKey(userKey, r.readTs)
	if !iter.SeekGE(target) || !iter.Valid() {
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
	if len(v) == 0 || v[0] == markerTombstone {
		return nil, ts, false // visible version is a delete → absent as-of readTs
	}
	out := append([]byte(nil), v[1:]...)
	return out, ts, true
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
		return &pebbleCursor{err: fmt.Errorf(
			"bluedb: inverted data-scan bounds for prefix %x: [% x, % x)", prefix, lower, upper)}
	}
	iter, err := r.snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return &pebbleCursor{err: err}
	}
	return &pebbleCursor{iter: iter, readTs: r.readTs, lower: lower}
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
}

var _ Cursor = (*pebbleCursor)(nil)

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
			c.err = c.iter.Error()
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
				c.err = c.iter.Error()
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
