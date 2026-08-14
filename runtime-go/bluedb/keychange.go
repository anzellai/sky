package bluedb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// keychange.go — the L2 encoding of one committed transaction's row-level changes
// (§3.2/§3.3). It is serialized into the OPAQUE CommitReq.ChangelogPayload bytes; L1 stores
// them verbatim at 0x01‖commitTs and never interprets them. Only the embedded committer
// (validation) and Phase-4 fan-out ever decode — never the storage seam — so swapping L1
// for a SQL storage adapter (which validates via BEGIN/COMMIT, not this changelog) is
// unaffected (R-2.2).

// CollID is a stable per-collection id (assigned by the L0/L3 registry, Phase 3).
type CollID uint32

// IndexID is a stable per-(collection,index) id.
type IndexID uint32

// KeyChange is one row-level change of a committed transaction (§3.2).
type KeyChange struct {
	Coll     CollID       // owning collection — the collection-level fallback witness matches on this
	Pk       []byte       // the user-key (== VersionedWrite.UserKey) — point-read validation
	Op       Op           // OpPut | OpDelete
	Record   []byte       // put: row bytes (L4 fan-out); delete: nil. Validation ignores it.
	NewIndex []IndexCoord // positions the row NOW occupies (put); nil for delete
	OldIndex []IndexCoord // positions the row VACATED (update/delete); nil for insert
}

// IndexCoord is one order-preserving index-entry coordinate (§3.2). Key is produced by the
// single encodeIndexKey — the SAME encoder Txn.Scan's lo/hi bounds go through (R-2.1).
type IndexCoord struct {
	Index IndexID
	Key   []byte
}

// payloadFmtV1 tags the changelog-payload wire format. The payload (unlike keys.go) is NOT
// comparer-frozen — only its commitTs KEY is — so this byte lets the shape evolve without a
// store rewrite.
const payloadFmtV1 byte = 0x01

var errBadPayload = errors.New("bluedb: malformed changelog payload")

// EncodeChangelogPayload serializes one transaction's KeyChange list into the opaque bytes
// L2 puts in CommitReq.ChangelogPayload. Length-prefixed, deterministic, versioned by the
// 1-byte format tag.
func EncodeChangelogPayload(changes []KeyChange) []byte {
	buf := make([]byte, 0, 16+len(changes)*24)
	buf = append(buf, payloadFmtV1)
	buf = appendUvarint(buf, uint64(len(changes)))
	for i := range changes {
		c := &changes[i]
		buf = appendUvarint(buf, uint64(c.Coll))
		buf = appendBytes(buf, c.Pk)
		buf = append(buf, byte(c.Op))
		buf = appendBytes(buf, c.Record)
		buf = appendCoords(buf, c.NewIndex)
		buf = appendCoords(buf, c.OldIndex)
	}
	return buf
}

// DecodeChangelogPayload is the inverse — used by the committer's ring rebuild / spill
// fallback and (Phase 4) reactivity fan-out.
func DecodeChangelogPayload(payload []byte) ([]KeyChange, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	r := &payloadReader{b: payload}
	tag, err := r.byte()
	if err != nil {
		return nil, err
	}
	if tag != payloadFmtV1 {
		return nil, fmt.Errorf("%w: unknown format tag 0x%02x", errBadPayload, tag)
	}
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(payload)) { // upper bound: never more changes than bytes
		return nil, fmt.Errorf("%w: implausible change count %d", errBadPayload, n)
	}
	out := make([]KeyChange, 0, n)
	for i := uint64(0); i < n; i++ {
		var c KeyChange
		coll, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		c.Coll = CollID(coll)
		if c.Pk, err = r.bytes(); err != nil {
			return nil, err
		}
		op, err := r.byte()
		if err != nil {
			return nil, err
		}
		c.Op = Op(op)
		if c.Record, err = r.bytes(); err != nil {
			return nil, err
		}
		if c.NewIndex, err = r.coords(); err != nil {
			return nil, err
		}
		if c.OldIndex, err = r.coords(); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func appendUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

func appendBytes(b, v []byte) []byte {
	b = appendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func appendCoords(b []byte, coords []IndexCoord) []byte {
	b = appendUvarint(b, uint64(len(coords)))
	for i := range coords {
		b = appendUvarint(b, uint64(coords[i].Index))
		b = appendBytes(b, coords[i].Key)
	}
	return b
}

// payloadReader is a bounds-checked cursor over the payload bytes.
type payloadReader struct {
	b   []byte
	pos int
}

func (r *payloadReader) byte() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, errBadPayload
	}
	v := r.b[r.pos]
	r.pos++
	return v, nil
}

func (r *payloadReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		return 0, errBadPayload
	}
	r.pos += n
	return v, nil
}

func (r *payloadReader) bytes() ([]byte, error) {
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)-r.pos) {
		return nil, errBadPayload
	}
	if n == 0 {
		return nil, nil
	}
	out := append([]byte(nil), r.b[r.pos:r.pos+int(n)]...)
	r.pos += int(n)
	return out, nil
}

func (r *payloadReader) coords() ([]IndexCoord, error) {
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)-r.pos) {
		return nil, errBadPayload
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]IndexCoord, 0, n)
	for i := uint64(0); i < n; i++ {
		idx, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		key, err := r.bytes()
		if err != nil {
			return nil, err
		}
		out = append(out, IndexCoord{Index: IndexID(idx), Key: key})
	}
	return out, nil
}
