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
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// WAL file header (G1). A versioned 5-byte prefix — 4-byte magic + 1-byte
// version — written once when a WAL file is created fresh (see writeWalHeader).
// It lets recovery reject a WAL written by a NEWER binary (a format it can't
// safely parse) with an explicit error INSTEAD of mistaking an unknown record
// for a torn tail and truncating valid data. Mirrors the snapshot format
// (snapshot.go). A file that does NOT begin with the magic is a LEGACY headerless
// WAL (implicitly version 0) and replays from offset 0 exactly as before —
// backward compatible; the header is added the next time the WAL is written fresh
// (fresh Open, or the post-checkpoint truncate-and-recreate).
// walVersion is the on-disk WAL format version.
//
//   - **v1** — one group commit writes N data records then ONE fsync; acks fire
//     after the fsync. It has NO durable commit boundary, so a power loss during
//     the fsync can leave the tail as [durable acked prefix][in-flight group with
//     an interior page HOLE: a torn record followed by a later valid record].
//     Recovery then sees valid-after-torn → classifies it as mid-file corruption →
//     REFUSES to open → strands the recoverable acked prefix.
//   - **v2** — each group appends a PER-GROUP commit record (opCommit) after its N
//     data records, covered by the SAME single fsync (N+1 records, one fsync). The
//     commit record is the durable commit boundary: recovery can now tell an
//     un-acked in-flight trailing group (truncate + recover the committed prefix)
//     from real bit-rot behind acked data (refuse — preserve G2). See
//     docs/bluedb/durability.md § "WAL v2 commit records".
//
// v1/legacy WALs keep their OLD semantics on replay (magic-present version 1, or a
// magic-ABSENT headerless file); only v2 uses commit-record recovery. A v1 store
// migrates to v2 the next time its header is rewritten fresh — a checkpoint's
// truncate-and-recreate (doCheckpoint) — never eagerly on open.
const (
	walMagic     = "BWAL"
	walVersion   = 2
	walHeaderLen = 5 // len(walMagic) + 1 version byte
)

// walHeaderBytes is the 5-byte prefix written to a fresh WAL file.
func walHeaderBytes() []byte {
	h := make([]byte, walHeaderLen)
	copy(h[0:4], walMagic)
	h[4] = walVersion
	return h
}

// probeBudgetBytes caps the CRC work the G2 forward-probe performs before giving
// up and failing closed (refuse to open). A genuine torn tail concludes far below
// this; the cap only guards a pathological/adversarial file from a catastrophic
// scan. Exhausting the budget fails CLOSED (Open returns an error, truncates
// nothing), so the cap can never cause data loss.
const probeBudgetBytes = 1 << 30 // 1 GiB of CRC work

// Operation codes stored in each record.
//
// On-disk tag census: 1=opPut, 2=opDelete, 4=opBatch, 6=opCommit are ENCODED to
// the WAL. 3=opCheckpoint and 5=opBackup are channel-only markers (db.go) routed
// through the commit queue and NEVER encoded — so 6 is the next free ENCODED tag.
const (
	opPut    uint8 = 1
	opDelete uint8 = 2
	// opBatch is an ATOMIC multi-mutation record: its payload carries N (op,key,
	// value) mutations under ONE crc + length, so the existing torn-tail logic
	// makes the whole batch all-or-nothing on a crash (the record validates
	// wholly → all N apply, or it is the torn tail → none apply). Never a subset.
	opBatch uint8 = 4
	// opCommit is the WAL v2 per-group COMMIT boundary. It rides the same
	// [crc][len][payload] frame as any record (so its CRC self-validates a torn
	// commit), with a fixed 13-byte payload: [seq uint64][opCommit uint8][count
	// uint32] — the group's high-water seq and the number of DATA records the
	// group wrote. A group is durable (and its writers acked) only once this
	// record is present AND fsync'd behind its N data records. Recovery flushes a
	// buffered group only when it reads this record and cross-checks count/seq.
	opCommit uint8 = 6
)

// maxGroupRecords bounds how many DATA records replay/verify will buffer for one
// pending group before its commit record must appear. It is a FORMAT-LEVEL bound,
// DECOUPLED from the runtime maxBatch tunable (db.go) — the on-disk format must
// not be coupled to a tunable. Sized well above the largest group the committer
// can emit (maxBatch), so exceeding it means the WAL is malformed (a commit
// record went missing / was corrupted), never a legitimate large group.
const maxGroupRecords = 4096

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
	count uint32     // number of DATA records in the group; only when op == opCommit
}

// commitEntry builds the WAL v2 per-group commit record for a group whose
// high-water seq is seq and which wrote count DATA records.
func commitEntry(seq uint64, count int) entry {
	return entry{seq: seq, op: opCommit, count: uint32(count)}
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
	if e.op == opCommit {
		// v2 per-group commit boundary: [seq uint64][opCommit uint8][count uint32]
		// = exactly 13 bytes.
		payload = make([]byte, 0, 13)
		binary.LittleEndian.PutUint64(num[:8], e.seq)
		payload = append(payload, num[:8]...)
		payload = append(payload, opCommit)
		binary.LittleEndian.PutUint32(num[:4], e.count)
		payload = append(payload, num[:4]...)
	} else if e.op == opBatch {
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
	if op == opCommit {
		return decodeCommit(p)
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

// decodeCommit parses a WAL v2 commit payload: [seq uint64][opCommit uint8][count
// uint32]. It requires the payload to be EXACTLY 13 bytes — mirroring decodeBatch's
// exact-consume guard, a partial (short) OR oversized (trailing bytes) commit MUST
// NOT decode as valid, because a torn commit is torn.
func decodeCommit(p []byte) (entry, bool) {
	if len(p) != 13 {
		return entry{}, false
	}
	return entry{
		seq:   binary.LittleEndian.Uint64(p[0:8]),
		op:    opCommit,
		count: binary.LittleEndian.Uint32(p[9:13]),
	}, true
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

// replay reads the WAL at path, applies its recoverable records via apply (in
// order, for records with seq > minSeq), and returns the highest COMMITTED seq
// plus validEnd — the byte offset the caller should truncate to (the end of the
// recoverable prefix). A missing file is not an error (fresh DB).
//
// It routes by the version header: a magic-absent legacy WAL and a magic-present
// version-1 WAL run replayV1 (record-at-a-time apply, stop at the first torn
// record, record-granularity G2 probe); a version-2 WAL runs replayV2 (buffer
// each group, apply only on a valid per-group commit record, group-granularity
// discriminator). Either way the (G2) principle holds: a torn tail with real acked
// data behind the stop-point → REFUSE; an un-acked in-flight tail → truncate +
// recover. See replayV1 / replayV2 for the per-path detail. replay also refuses
// (G1) a WAL whose version header is NEWER than this binary understands.
//
// minSeq is the coveredSeq of a loaded snapshot: records with seq <= minSeq are
// already reflected in the snapshot, so they are skipped. This makes recovery
// correct even in the crash window between writing a snapshot and truncating the
// WAL — a stale pre-snapshot record can never resurrect a value the snapshot
// already superseded.
func replay(path string, minSeq uint64, apply func(entry)) (maxSeq uint64, validEnd int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 1<<16)

	// Version gate (G1). The legacy replay-from-offset-0 path keys on the magic
	// being ABSENT only. A file that BEGINS with the magic carries a version
	// header; peek it WITHOUT consuming and route by version:
	//   version == 2 (walVersion) → v2 commit-record recovery
	//   version == 1              → v1 old semantics (unchanged)
	//   version  > 2              → REFUSE ("newer binary" — an old binary must
	//                               not misparse + truncate a newer format)
	//   version ∉ {1,2} (incl 0)  → REFUSE (a magic-present unknown version is NOT
	//                               routed to legacy replay-from-0, which would
	//                               truncate the whole file)
	// A file with NO magic is a LEGACY headerless WAL (implicitly version 0) and
	// replays from offset 0 exactly as before.
	useV2 := false
	if head, _ := r.Peek(walHeaderLen); len(head) >= walHeaderLen && string(head[0:4]) == walMagic {
		version := head[4]
		switch {
		case version == walVersion: // 2
			useV2 = true
			_, _ = r.Discard(walHeaderLen)
			validEnd = walHeaderLen // records begin AFTER the header
		case version == 1:
			_, _ = r.Discard(walHeaderLen)
			validEnd = walHeaderLen
		case version > walVersion:
			return 0, 0, fmt.Errorf("bluedb: WAL version %d unsupported (written by a newer binary); refusing to open to avoid truncating your data", version)
		default: // magic present but version not in {1,2} (e.g. a corrupt version byte)
			return 0, 0, fmt.Errorf("bluedb: WAL version %d unsupported", version)
		}
	}
	// else: legacy headerless WAL (version 0), or an empty/sub-header file — v1
	// replay reads records from the current offset (0), unchanged from pre-G1.

	if useV2 {
		return replayV2(f, r, minSeq, validEnd, apply)
	}
	return replayV1(f, r, minSeq, validEnd, apply)
}

// replayV1 is the pre-v2 replay: apply each valid record in order (seq > minSeq),
// stop at the first torn/invalid record, and disambiguate a torn tail (truncate)
// from mid-file corruption (refuse) via the record-granularity forward probe. It
// runs for magic-absent legacy WALs and for magic-present version-1 WALs.
func replayV1(f *os.File, r *bufio.Reader, minSeq uint64, validEnd int64, apply func(entry)) (maxSeq uint64, _ int64, err error) {
	maxSeq = minSeq
	var hdr [8]byte
	for {
		torn := false
		if _, e := io.ReadFull(r, hdr[:]); e != nil {
			torn = true // clean EOF (nothing read) or torn header → stop-point
		}
		var payload []byte
		var plen uint32
		if !torn {
			crc := binary.LittleEndian.Uint32(hdr[0:4])
			plen = binary.LittleEndian.Uint32(hdr[4:8])
			if plen == 0 || plen > maxRecordSize {
				torn = true // garbage length
			} else {
				payload = make([]byte, plen)
				if _, e := io.ReadFull(r, payload); e != nil {
					torn = true // short payload
				} else if crc32.ChecksumIEEE(payload) != crc {
					torn = true // CRC mismatch
				}
			}
		}
		var ent entry
		if !torn {
			var ok bool
			if ent, ok = decodePayload(payload); !ok {
				torn = true // undecodable (e.g. unknown opcode from a newer binary)
			}
		}
		if torn {
			// G2: `validEnd` is the offset of this stop-point. Distinguish a torn
			// tail (safe to truncate) from mid-file corruption (must refuse).
			mid, inconclusive, perr := probeMidFileCorruption(f, validEnd)
			if perr != nil {
				return 0, 0, perr
			}
			if mid {
				return 0, 0, fmt.Errorf("bluedb: WAL corruption at offset %d — a valid record follows an invalid one; refusing to open (would discard %d bytes). Run 'sky bluedb verify' / restore from backup",
					validEnd, discardBytes(f, validEnd))
			}
			if inconclusive {
				return 0, 0, fmt.Errorf("bluedb: WAL corruption at offset %d — could not confirm the invalid tail is a partial final write within the probe budget; refusing to open to avoid discarding %d bytes. Run 'sky bluedb verify' / restore from backup",
					validEnd, discardBytes(f, validEnd))
			}
			break // genuine torn tail → stop; caller truncates at validEnd
		}
		if ent.seq > minSeq {
			// Copy key/value out of the record buffer before handing to apply, so
			// the memtable owns its bytes.
			apply(copyEntry(ent))
		}
		if ent.seq > maxSeq {
			maxSeq = ent.seq
		}
		validEnd += int64(8 + int(plen))
	}
	return maxSeq, validEnd, nil
}

// replayV2 is the WAL v2 replay: DATA records are BUFFERED (not applied) into the
// current pending group and flushed only when a valid opCommit closes the group,
// so recovery has a durable per-group commit boundary. It returns the last
// COMMITTED group's high-water seq (NOT a maxSeq that would include dropped
// uncommitted seqs) and validEnd = the byte offset just past the last commit (the
// truncation boundary for any un-acked in-flight trailing group).
//
// The discriminator (the headline fix): on a torn/undecodable record, replay
// REFUSES only if a FULLY-VALID COMMITTED GROUP exists at/after the torn offset —
// real bit-rot behind acked data (preserve G2). A bare valid opCommit is NOT
// sufficient (an in-flight group's own commit sector can survive out of order;
// refusing on it would re-introduce the stranding bug). If no complete valid group
// follows, the trailing group is an un-acked in-flight write → truncate at the
// last committed boundary and recover the committed prefix.
func replayV2(f *os.File, r *bufio.Reader, minSeq uint64, startEnd int64, apply func(entry)) (maxSeq uint64, validEnd int64, err error) {
	lastCommitEnd := startEnd
	lastCommitSeq := minSeq
	offset := startEnd
	var pending []entry

	// discriminate classifies a stop-point at `at`: a fully-valid committed group
	// after it is real mid-file corruption (refuse); otherwise it is an un-acked
	// in-flight trailing group → truncate to the last committed boundary.
	discriminate := func(at int64) (uint64, int64, error) {
		mid, inconclusive, perr := probeForValidCommitGroup(f, at)
		if perr != nil {
			return 0, 0, perr
		}
		if mid {
			return 0, 0, fmt.Errorf("bluedb: WAL corruption at offset %d — a fully-committed group follows an invalid record; refusing to open (would discard %d bytes). Run 'sky bluedb verify' / restore from backup",
				at, discardBytes(f, lastCommitEnd))
		}
		if inconclusive {
			return 0, 0, fmt.Errorf("bluedb: WAL corruption at offset %d — could not confirm the invalid tail is an un-acked in-flight group within the probe budget; refusing to open to avoid discarding %d bytes. Run 'sky bluedb verify' / restore from backup",
				at, discardBytes(f, lastCommitEnd))
		}
		return lastCommitSeq, lastCommitEnd, nil
	}

	var hdr [8]byte
	for {
		torn := false
		cleanEOF := false
		if n, e := io.ReadFull(r, hdr[:]); e != nil {
			if e == io.EOF && n == 0 {
				cleanEOF = true // exactly on a record boundary
			} else {
				torn = true // partial header → stop-point
			}
		}
		var payload []byte
		var plen uint32
		if !torn && !cleanEOF {
			crc := binary.LittleEndian.Uint32(hdr[0:4])
			plen = binary.LittleEndian.Uint32(hdr[4:8])
			if plen == 0 || plen > maxRecordSize {
				torn = true // garbage length
			} else {
				payload = make([]byte, plen)
				if _, e := io.ReadFull(r, payload); e != nil {
					torn = true // short payload
				} else if crc32.ChecksumIEEE(payload) != crc {
					torn = true // CRC mismatch
				}
			}
		}
		var ent entry
		if !torn && !cleanEOF {
			var ok bool
			if ent, ok = decodePayload(payload); !ok {
				torn = true // undecodable
			}
		}

		if cleanEOF {
			// pending empty → everything committed (no truncate). pending non-empty →
			// a trailing group whose commit/fsync never landed → drop it (truncate to
			// the last committed boundary). validEnd = lastCommitEnd in both cases.
			return lastCommitSeq, lastCommitEnd, nil
		}
		if torn {
			return discriminate(offset)
		}

		if ent.op == opCommit {
			// Cross-check the commit closes exactly the buffered group. A CRC-valid
			// commit whose count/seq don't match its group is real corruption.
			var hi uint64
			if len(pending) > 0 {
				hi = pending[len(pending)-1].seq
			}
			if ent.count != uint32(len(pending)) || ent.seq != hi {
				return 0, 0, fmt.Errorf("bluedb: WAL corruption at offset %d — commit record does not match its group (count %d/seq %d vs %d records/high-water %d); refusing to open. Run 'sky bluedb verify' / restore from backup",
					offset, ent.count, ent.seq, len(pending), hi)
			}
			// Flush: apply each buffered entry with seq > minSeq (per-entry filter,
			// so a group straddling a snapshot's coveredSeq applies only its unseen
			// records; a fully-covered group applies none but still advances).
			for _, pe := range pending {
				if pe.seq > minSeq {
					apply(pe)
				}
			}
			offset += int64(8 + int(plen))
			lastCommitEnd = offset
			lastCommitSeq = ent.seq
			pending = pending[:0]
			continue
		}

		// DATA record (put/delete/batch): buffer with copy-out; do NOT apply yet.
		pending = append(pending, copyEntry(ent))
		if len(pending) > maxGroupRecords {
			// A commit never arrived within the format bound → malformed WAL.
			// Treat the current record as a stop-point and discriminate (refuse if a
			// committed group follows; else truncate the un-acked run).
			return discriminate(offset)
		}
		offset += int64(8 + int(plen))
	}
}

// copyEntry deep-copies an entry's key/value/muts out of the record buffer so the
// memtable (or a pending group) owns its bytes independent of the read buffer.
func copyEntry(e entry) entry {
	if e.op == opBatch {
		cm := make([]mutation, len(e.muts))
		for i, m := range e.muts {
			cm[i] = mutation{
				op:    m.op,
				key:   append([]byte(nil), m.key...),
				value: append([]byte(nil), m.value...),
			}
		}
		return entry{seq: e.seq, op: opBatch, muts: cm}
	}
	return entry{
		seq:   e.seq,
		op:    e.op,
		key:   append([]byte(nil), e.key...),
		value: append([]byte(nil), e.value...),
	}
}

// discardBytes is the number of bytes from the stop-point `pos` to EOF — the
// amount a truncate would throw away (for the refuse-to-open diagnostics).
func discardBytes(f *os.File, pos int64) int64 {
	if fi, e := f.Stat(); e == nil && fi.Size() > pos {
		return fi.Size() - pos
	}
	return 0
}

// probeMidFileCorruption inspects the bytes AFTER a torn/invalid record at `pos`
// (G2). It reads the remaining region once and scans it for a record whose CRC
// validates and payload decodes.
//
//	mid=true          → a valid record follows the invalid one: MID-FILE CORRUPTION.
//	inconclusive=true → the probe hit its work budget without a verdict: fail closed.
//	both false        → no valid record follows: a TORN TAIL (safe to truncate).
func probeMidFileCorruption(f *os.File, pos int64) (mid bool, inconclusive bool, err error) {
	fi, err := f.Stat()
	if err != nil {
		return false, false, err
	}
	remaining := fi.Size() - pos
	if remaining <= 8 {
		// Fewer than a record header's worth of bytes follow → nothing valid can
		// come after → definitively a torn tail.
		return false, false, nil
	}
	buf := make([]byte, remaining)
	n, rerr := f.ReadAt(buf, pos)
	if n < len(buf) {
		if rerr == nil {
			rerr = io.ErrUnexpectedEOF
		}
		return false, false, rerr
	}
	// Scan from offset 1: offset 0 is the record we already know is invalid.
	found, budgetExhausted := scanForValidRecord(buf, 1)
	return found, budgetExhausted, nil
}

// scanForValidRecord byte-scans buf from minOff for the first record whose framing
// (crc32 + length header) and payload decode successfully. Returns found on the
// first hit, or exhausted when the CRC work budget is spent before a verdict — the
// caller then fails closed (never truncates on an inconclusive scan).
func scanForValidRecord(buf []byte, minOff int) (found bool, exhausted bool) {
	budget := int64(probeBudgetBytes)
	for o := minOff; o+8 <= len(buf); o++ {
		plen := binary.LittleEndian.Uint32(buf[o+4 : o+8])
		if plen == 0 || plen > maxRecordSize {
			continue // not a plausible record header here
		}
		end := o + 8 + int(plen)
		if end > len(buf) {
			continue // declared payload runs past EOF here
		}
		budget -= int64(plen)
		if budget < 0 {
			return false, true // inconclusive → caller fails closed (safe)
		}
		crc := binary.LittleEndian.Uint32(buf[o : o+4])
		if crc32.ChecksumIEEE(buf[o+8:end]) != crc {
			continue
		}
		if _, ok := decodePayload(buf[o+8 : end]); ok {
			return true, false // a genuinely valid record follows the invalid one
		}
	}
	return false, false
}

// probeForValidCommitGroup is the WAL v2 counterpart of probeMidFileCorruption:
// it inspects the bytes AFTER a torn/invalid record at `pos` for a FULLY-VALID
// COMMITTED GROUP (not merely a valid record). It reads the remaining region once
// (read-only) and scans it at GROUP granularity.
//
//	mid=true          → a complete committed group follows → MID-FILE CORRUPTION.
//	inconclusive=true → the probe hit its work budget without a verdict: fail closed.
//	both false        → no complete committed group follows → an un-acked in-flight
//	                    trailing group (safe to truncate to the last committed end).
func probeForValidCommitGroup(f *os.File, pos int64) (mid bool, inconclusive bool, err error) {
	fi, err := f.Stat()
	if err != nil {
		return false, false, err
	}
	remaining := fi.Size() - pos
	if remaining <= 8 {
		// Fewer than a record header's worth of bytes follow → no group can → torn.
		return false, false, nil
	}
	buf := make([]byte, remaining)
	n, rerr := f.ReadAt(buf, pos)
	if n < len(buf) {
		if rerr == nil {
			rerr = io.ErrUnexpectedEOF
		}
		return false, false, rerr
	}
	// Scan from offset 1: offset 0 is the record we already know is invalid, so a
	// valid group cannot start there.
	found, budgetExhausted := scanForValidCommitGroup(buf, 1)
	return found, budgetExhausted, nil
}

// scanForValidCommitGroup byte-scans buf from minOff for the first FULLY-VALID
// COMMITTED GROUP: k >= 1 contiguous CRC-valid, well-framed DATA records with
// strictly-contiguous increasing seqs [hi-k+1..hi], IMMEDIATELY followed by a
// CRC-valid opCommit whose count == k AND seq == hi. This group-granularity check
// is the discriminator: a BARE valid opCommit is NOT a group (an in-flight group's
// own commit sector can survive out of order after a torn data record), so it does
// NOT trigger a refuse — only a whole surviving committed group (real bit-rot
// behind acked data) does. Returns found on the first such group, or exhausted
// when the CRC work budget is spent before a verdict (caller fails closed).
func scanForValidCommitGroup(buf []byte, minOff int) (found bool, exhausted bool) {
	budget := int64(probeBudgetBytes)
	for o := minOff; o+8 <= len(buf); o++ {
		// Attempt to parse a complete [DATA × k][commit] group starting at o.
		p := o
		var k uint32
		var hiSeq uint64
		seqOK := true
		valid := false
		for p+8 <= len(buf) {
			plen := binary.LittleEndian.Uint32(buf[p+4 : p+8])
			if plen == 0 || plen > maxRecordSize {
				break // not a plausible record header here
			}
			end := p + 8 + int(plen)
			if end > len(buf) {
				break // declared payload runs past EOF here
			}
			budget -= int64(plen)
			if budget < 0 {
				return false, true // inconclusive → caller fails closed (safe)
			}
			crc := binary.LittleEndian.Uint32(buf[p : p+4])
			if crc32.ChecksumIEEE(buf[p+8:end]) != crc {
				break // torn/rotted record → this candidate group can't complete
			}
			ent, ok := decodePayload(buf[p+8 : end])
			if !ok {
				break
			}
			if ent.op == opCommit {
				// A commit closes the candidate group iff it has >= 1 preceding data
				// record with contiguous seqs and count/seq both match.
				if k >= 1 && seqOK && ent.count == k && ent.seq == hiSeq {
					valid = true
				}
				break
			}
			// DATA record — require strictly-contiguous increasing seqs.
			if k > 0 && ent.seq != hiSeq+1 {
				seqOK = false
			}
			hiSeq = ent.seq
			k++
			if k > maxGroupRecords {
				break // a group larger than the format bound is not a valid group
			}
			p = end
		}
		if valid {
			return true, false
		}
	}
	return false, false
}
