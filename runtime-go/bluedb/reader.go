package bluedb

import (
	"bytes"

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
	ts := decodeDataVersion(k)
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
func (r *pebbleReader) Iterate(prefix []byte) Cursor {
	lower := append([]byte{tagData}, prefix...)
	upper := dataScanUpper(prefix)
	iter, err := r.snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return &pebbleCursor{err: err}
	}
	return &pebbleCursor{iter: iter, readTs: r.readTs, lower: lower}
}

// dataScanUpper returns the exclusive upper bound for a data-keyspace scan over
// user-keys beginning with prefix. For an empty prefix it is the whole data
// keyspace (up to the changelog tag).
func dataScanUpper(prefix []byte) []byte {
	base := append([]byte{tagData}, prefix...)
	if succ := bytesSuccessor(base); succ != nil {
		return succ
	}
	return []byte{tagChangelog}
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
		v := c.iter.Value()
		if len(v) == 0 || v[0] == markerTombstone {
			continue // tombstoned as-of readTs — skip
		}
		c.curKey = userKey
		c.curVal = append([]byte(nil), v[1:]...)
		c.curTs = decodeDataVersion(vk)
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
