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
)

// maxRecordSize is a sanity bound; a declared payload length beyond it is
// treated as a torn record (garbage length from a partial write).
const maxRecordSize = 64 << 20 // 64 MiB

// entry is one logical mutation.
type entry struct {
	seq   uint64
	op    uint8
	key   []byte
	value []byte // empty for opDelete
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
	payload := make([]byte, 0, 8+1+4+len(e.key)+len(e.value))
	var num [8]byte
	binary.LittleEndian.PutUint64(num[:8], e.seq)
	payload = append(payload, num[:8]...)
	payload = append(payload, e.op)
	binary.LittleEndian.PutUint32(num[:4], uint32(len(e.key)))
	payload = append(payload, num[:4]...)
	payload = append(payload, e.key...)
	payload = append(payload, e.value...)

	out := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(out[0:4], crc32.ChecksumIEEE(payload))
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
	copy(out[8:], payload)
	return out
}

func decodePayload(p []byte) (entry, bool) {
	if len(p) < 8+1+4 {
		return entry{}, false
	}
	e := entry{}
	e.seq = binary.LittleEndian.Uint64(p[0:8])
	e.op = p[8]
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

// replay reads every valid record from the WAL at path, calling apply in order,
// and returns the highest seq seen plus the byte offset of the end of the last
// valid record (validEnd). A missing file is not an error (fresh DB). It stops
// at the first torn/invalid record; validEnd is where the file should be
// truncated so future appends are clean.
func replay(path string, apply func(entry)) (maxSeq uint64, validEnd int64, err error) {
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
		// Copy key/value out of the reader's buffer before handing to apply,
		// so the memtable owns its bytes.
		k := append([]byte(nil), ent.key...)
		v := append([]byte(nil), ent.value...)
		apply(entry{seq: ent.seq, op: ent.op, key: k, value: v})
		if ent.seq > maxSeq {
			maxSeq = ent.seq
		}
		validEnd += int64(8 + int(plen))
	}
	return maxSeq, validEnd, nil
}
