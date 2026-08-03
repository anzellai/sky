// Package bluedb is the embedded storage engine behind BlueDB — the Sky-native
// reactive data layer (see docs/bluedb/). This file is the write-ahead log: the
// on-disk record format, encoding, and crash-safe replay.
//
// Durability contract (see docs/bluedb/durability.md): a write is acked only
// after its record is fsync'd. Recovery replays the WAL, applying every record
// whose CRC validates and STOPPING at the first torn/invalid record — that
// boundary is the crash point; anything past it was never fully written, hence
// never acked.
package bluedb

import (
	"bufio"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
)

// Operation codes stored in each record.
const (
	opPut    uint8 = 1
	opDelete uint8 = 2
	// opBatch is an ATOMIC multi-mutation record: its payload carries N (op,key,
	// value) mutations under ONE crc + length, so the existing torn-tail logic
	// makes the whole batch all-or-nothing on a crash (the record validates
	// wholly → all N apply, or it is the torn tail → none apply). Never a subset.
	opBatch uint8 = 4
)

// maxBatchMuts bounds the declared mutation count so a corrupt/torn 4-byte count
// can't drive a huge allocation on replay (a garbage count is a torn record).
const maxBatchMuts = 1 << 24 // 16M mutations — far above any real batch

// maxRecordSize is a sanity bound; a declared payload length beyond it is
// treated as a torn record (garbage length from a partial write).
const maxRecordSize = 64 << 20 // 64 MiB

// entry is one logical mutation.
type entry struct {
	seq   uint64
	op    uint8
	key   []byte
	value []byte     // empty for opDelete
	muts  []mutation // non-nil only when op == opBatch
}

// mutation is one op inside an opBatch record.
type mutation struct {
	op    uint8 // opPut | opDelete
	key   []byte
	value []byte
}

// On-disk record layout (little-endian):
//
//	crc32  uint32   -- IEEE CRC over the payload bytes
//	length uint32   -- payload length in bytes
//	payload:
//	  seq  uint64
//	  op   uint8
//	  klen uint32
//	  key  [klen]byte
//	  val  [length-13-klen]byte
//
// The crc+length header lets replay detect a torn tail: a short read of either
// the header or the payload, an oversized length, or a CRC mismatch all mean
// "the last write didn't complete" — recovery stops there.
func encodeRecord(e entry) []byte {
	var num [8]byte
	var payload []byte
	if e.op == opBatch {
		// [seq][opBatch][mutCount] then each [op][klen][key][vlen][val]
		payload = make([]byte, 0, 8+1+4+batchMutBytes(e.muts))
		binary.LittleEndian.PutUint64(num[:8], e.seq)
		payload = append(payload, num[:8]...)
		payload = append(payload, opBatch)
		binary.LittleEndian.PutUint32(num[:4], uint32(len(e.muts)))
		payload = append(payload, num[:4]...)
		for _, m := range e.muts {
			payload = append(payload, m.op)
			binary.LittleEndian.PutUint32(num[:4], uint32(len(m.key)))
			payload = append(payload, num[:4]...)
			payload = append(payload, m.key...)
			binary.LittleEndian.PutUint32(num[:4], uint32(len(m.value)))
			payload = append(payload, num[:4]...)
			payload = append(payload, m.value...)
		}
	} else {
		payload = make([]byte, 0, 8+1+4+len(e.key)+len(e.value))
		binary.LittleEndian.PutUint64(num[:8], e.seq)
		payload = append(payload, num[:8]...)
		payload = append(payload, e.op)
		binary.LittleEndian.PutUint32(num[:4], uint32(len(e.key)))
		payload = append(payload, num[:4]...)
		payload = append(payload, e.key...)
		payload = append(payload, e.value...)
	}

	out := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(out[0:4], crc32.ChecksumIEEE(payload))
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
	copy(out[8:], payload)
	return out
}

func decodePayload(p []byte) (entry, bool) {
	if len(p) < 8+1 {
		return entry{}, false
	}
	op := p[8]
	if op == opBatch {
		return decodeBatch(p)
	}
	if len(p) < 8+1+4 {
		return entry{}, false
	}
	e := entry{}
	e.seq = binary.LittleEndian.Uint64(p[0:8])
	e.op = op
	klen := binary.LittleEndian.Uint32(p[9:13])
	if int(klen) > len(p)-13 {
		return entry{}, false
	}
	e.key = p[13 : 13+klen]
	e.value = p[13+klen:]
	if e.op != opPut && e.op != opDelete {
		return entry{}, false
	}
	return e, true
}

// decodeBatch parses an opBatch payload: [seq][opBatch][mutCount] then each
// [op][klen][key][vlen][val]. It must consume EXACTLY the payload — a mutCount
// that doesn't match the bytes present (truncated or trailing) is rejected as
// torn, so a partial write can never decode as a smaller-but-valid batch.
func decodeBatch(p []byte) (entry, bool) {
	if len(p) < 13 {
		return entry{}, false
	}
	count := binary.LittleEndian.Uint32(p[9:13])
	// count==0 is never written (an empty batch is an API no-op), so a zero-count
	// record is corruption → torn. Bound count by the smallest possible mutation
	// (1 op + 4 klen + 4 vlen = 9 bytes) BEFORE allocating, so a crafted/torn huge
	// count can't drive an OOM on replay (snapshot F4 parity).
	if count == 0 || count > maxBatchMuts || int64(count)*9 > int64(len(p)-13) {
		return entry{}, false
	}
	off := 13
	muts := make([]mutation, 0, count) // count now provably ≤ (len(p)-13)/9
	for i := uint32(0); i < count; i++ {
		if off+1+4 > len(p) {
			return entry{}, false
		}
		mop := p[off]
		if mop != opPut && mop != opDelete {
			return entry{}, false
		}
		klen := int(binary.LittleEndian.Uint32(p[off+1 : off+5]))
		off += 5
		if klen < 0 || off+klen+4 > len(p) {
			return entry{}, false
		}
		key := p[off : off+klen]
		off += klen
		vlen := int(binary.LittleEndian.Uint32(p[off : off+4]))
		off += 4
		if vlen < 0 || off+vlen > len(p) {
			return entry{}, false
		}
		val := p[off : off+vlen]
		off += vlen
		muts = append(muts, mutation{op: mop, key: key, value: val})
	}
	if off != len(p) {
		return entry{}, false // trailing bytes → mutCount lied → torn
	}
	return entry{seq: binary.LittleEndian.Uint64(p[0:8]), op: opBatch, muts: muts}, true
}

// batchMutBytes is the encoded size of a batch's mutation list (for the record
// buffer + the over-size guard).
func batchMutBytes(muts []mutation) int {
	n := 0
	for _, m := range muts {
		n += 1 + 4 + len(m.key) + 4 + len(m.value)
	}
	return n
}

// replay reads every valid record from the WAL at path, calling apply in order
// for records with seq > minSeq, and returns the highest seq seen plus the byte
// offset of the end of the last valid record (validEnd). A missing file is not
// an error (fresh DB). It stops at the first torn/invalid record; validEnd is
// where the file should be truncated so future appends are clean.
//
// minSeq is the coveredSeq of a loaded snapshot: records with seq <= minSeq are
// already reflected in the snapshot, so they are skipped. This makes recovery
// correct even in the crash window between writing a snapshot and truncating the
// WAL — a stale pre-snapshot record can never resurrect a value the snapshot
// already superseded.
func replay(path string, minSeq uint64, apply func(entry)) (maxSeq uint64, validEnd int64, err error) {
	maxSeq = minSeq
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 1<<16)
	var hdr [8]byte
	for {
		if _, e := io.ReadFull(r, hdr[:]); e != nil {
			break // clean EOF or torn header → stop
		}
		crc := binary.LittleEndian.Uint32(hdr[0:4])
		plen := binary.LittleEndian.Uint32(hdr[4:8])
		if plen == 0 || plen > maxRecordSize {
			break // garbage length → torn
		}
		payload := make([]byte, plen)
		if _, e := io.ReadFull(r, payload); e != nil {
			break // short payload → torn
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break // CRC mismatch → torn
		}
		ent, ok := decodePayload(payload)
		if !ok {
			break
		}
		if ent.seq > minSeq {
			// Copy key/value out of the record buffer before handing to apply, so
			// the memtable owns its bytes.
			if ent.op == opBatch {
				cm := make([]mutation, len(ent.muts))
				for i, m := range ent.muts {
					cm[i] = mutation{
						op:    m.op,
						key:   append([]byte(nil), m.key...),
						value: append([]byte(nil), m.value...),
					}
				}
				apply(entry{seq: ent.seq, op: opBatch, muts: cm})
			} else {
				k := append([]byte(nil), ent.key...)
				v := append([]byte(nil), ent.value...)
				apply(entry{seq: ent.seq, op: ent.op, key: k, value: v})
			}
		}
		if ent.seq > maxSeq {
			maxSeq = ent.seq
		}
		validEnd += int64(8 + int(plen))
	}
	return maxSeq, validEnd, nil
}
