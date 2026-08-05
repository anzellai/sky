// Verify is the READ-ONLY integrity scanner behind `sky bluedb <path> verify`
// (roadmap Tier 4 G4). It is the operator-facing diagnostic paired with the
// G2 refuse-to-open guard: G2 makes Open fail CLOSED on mid-file corruption so
// no valid tail is ever truncated away; Verify tells the operator WHERE the
// first bad byte is, WITHOUT ever writing to (let alone truncating) the file.
//
// It mirrors replay's reading discipline — same header peek, same crc+len
// framing, same G2 forward-probe classification — but it never applies a
// record, never truncates, and instead of stopping at the first torn record it
// CLASSIFIES the file. Every path is read-only: os.Open / bufio.Reader over the
// WAL, and Stat + ReadAt (via probeMidFileCorruption) for the probe. A
// corrupt-but-readable file is reported through VerifyReport, not returned as an
// error — an error is reserved for an unexpected IO failure so the CLI can
// print the report rather than crash.
package bluedb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// WAL status classifications (VerifyReport.WalStatus).
const (
	// VerifyClean — every record from the start to a clean EOF validated.
	VerifyClean = "clean"
	// VerifyTornTail — a partial final record (an interrupted last write). The
	// safe, common crash case; Open truncates it and recovers.
	VerifyTornTail = "torn-tail"
	// VerifyCorruption — a valid record follows an invalid one (mid-file rot), or
	// the forward-probe couldn't confirm a torn tail within its budget. Open
	// refuses (fail closed); this is the case G2 defends against.
	VerifyCorruption = "corruption"
	// VerifyVersionUnsupported — the WAL header carries a version this binary
	// doesn't understand. Open refuses rather than misparse a newer format.
	VerifyVersionUnsupported = "version-unsupported"
)

// Snapshot status classifications (VerifyReport.SnapStatus).
const (
	SnapClean              = "clean"
	SnapBadMagic           = "bad-magic"
	SnapVersionUnsupported = "version-unsupported"
	SnapCorrupt            = "corrupt"
	SnapAbsent             = "absent"
)

// VerifyReport is the read-only integrity verdict for a BlueDB store file. It
// captures enough for an operator (or CI) to know whether Open would succeed
// and, if not, exactly where the WAL first goes bad.
type VerifyReport struct {
	// WAL (the store file itself).
	WalExists      bool   // false → missing file (a fresh / never-written store)
	WalVersion     int    // 0 = legacy headerless; else the header version byte
	WalRecords     int    // count of valid records scanned before any stop-point
	WalBytes       int64  // WAL file size in bytes
	WalStatus      string // one of the Verify* constants above
	FirstBadOffset int64  // byte offset of the first torn/invalid record; -1 if none
	Detail         string // short human note for a torn-tail / corruption / bad version

	// Snapshot (path + ".snap").
	SnapExists     bool
	SnapStatus     string // one of the Snap* constants above
	SnapCoveredSeq uint64 // max committed seq the snapshot captures (when clean)

	// OK is true iff Open would succeed: the WAL is clean or a torn-tail AND the
	// snapshot is clean or absent. corruption / version-unsupported → false.
	OK bool
}

// Verify scans the store file at path (its WAL) and the sibling snapshot at
// path+".snap" READ-ONLY, returning a VerifyReport. It never opens the engine
// (which would truncate a torn tail or refuse a corrupt file) and never writes.
// An error is returned ONLY for an unexpected IO failure; a corrupt file is
// reported through the returned VerifyReport with err == nil.
func Verify(path string) (VerifyReport, error) {
	rep := VerifyReport{
		WalStatus:      VerifyClean,
		FirstBadOffset: -1,
		SnapStatus:     SnapAbsent,
	}
	if err := verifyWal(path, &rep); err != nil {
		return rep, err
	}
	if err := verifySnap(path+".snap", &rep); err != nil {
		return rep, err
	}
	walOK := rep.WalStatus == VerifyClean || rep.WalStatus == VerifyTornTail
	snapOK := rep.SnapStatus == SnapClean || rep.SnapStatus == SnapAbsent
	rep.OK = walOK && snapOK
	return rep, nil
}

// verifyWal scans the WAL read-only, classifying it per the G2 rules Open uses.
func verifyWal(path string, rep *VerifyReport) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			rep.WalExists = false // fresh DB — the WAL is the whole (empty) history
			return nil
		}
		return err
	}
	defer f.Close()
	rep.WalExists = true
	if fi, e := f.Stat(); e == nil {
		rep.WalBytes = fi.Size()
	}

	r := bufio.NewReaderSize(f, 1<<16)

	// validEnd tracks the byte offset just past the last VALID record — i.e. the
	// offset of the current (possibly torn) record. It starts after the header on
	// a versioned WAL, or at 0 on a legacy headerless one. The version gate mirrors
	// replay's (magic-absent legacy → v1; version 1 → v1; version 2 → v2; a
	// magic-present version this binary can't parse → unsupported).
	var validEnd int64
	useV2 := false
	if head, _ := r.Peek(walHeaderLen); len(head) >= walHeaderLen && string(head[0:4]) == walMagic {
		version := head[4]
		rep.WalVersion = int(version)
		switch {
		case version == walVersion: // 2
			useV2 = true
			_, _ = r.Discard(walHeaderLen)
			validEnd = walHeaderLen
		case version == 1:
			_, _ = r.Discard(walHeaderLen)
			validEnd = walHeaderLen
		default:
			// A version this binary doesn't understand (newer, or a corrupt version
			// byte) — Open refuses to avoid misparsing + truncating good data.
			rep.WalStatus = VerifyVersionUnsupported
			rep.Detail = fmt.Sprintf("WAL header version %d; this binary understands version %d", version, walVersion)
			return nil
		}
	}
	// else: legacy headerless WAL (version 0) — records begin at offset 0 (v1).

	if useV2 {
		return verifyWalV2(f, r, rep, validEnd)
	}
	return verifyWalV1(f, r, rep, validEnd)
}

// verifyWalV1 classifies a legacy/version-1 WAL record-at-a-time (each valid
// framed record counts; stop at the first torn record; record-granularity probe).
func verifyWalV1(f *os.File, r *bufio.Reader, rep *VerifyReport, validEnd int64) error {
	var hdr [8]byte
	for {
		torn := false
		if n, e := io.ReadFull(r, hdr[:]); e != nil {
			if e == io.EOF && n == 0 {
				// Clean EOF exactly on a record boundary: every record validated.
				return nil
			}
			torn = true // a partial (torn) header — a stop-point
		}
		var plen uint32
		if !torn {
			crc := binary.LittleEndian.Uint32(hdr[0:4])
			plen = binary.LittleEndian.Uint32(hdr[4:8])
			if plen == 0 || plen > maxRecordSize {
				torn = true // garbage length
			} else {
				payload := make([]byte, plen)
				if _, e := io.ReadFull(r, payload); e != nil {
					torn = true // short payload
				} else if crc32.ChecksumIEEE(payload) != crc {
					torn = true // CRC mismatch
				} else if _, ok := decodePayload(payload); !ok {
					torn = true // undecodable (e.g. unknown opcode)
				}
			}
		}
		if torn {
			// G2 classification at validEnd (the stop-point offset). Reuse the same
			// read-only forward-probe Open uses: a valid record after the invalid
			// one means MID-FILE CORRUPTION; none means a TORN TAIL; a budget-
			// exhausted probe is classified as corruption (fail closed), matching
			// Open's refuse-to-open behaviour.
			mid, inconclusive, perr := probeMidFileCorruption(f, validEnd)
			if perr != nil {
				return perr // unexpected IO error
			}
			rep.FirstBadOffset = validEnd
			switch {
			case mid:
				rep.WalStatus = VerifyCorruption
				rep.Detail = fmt.Sprintf("a valid record follows the invalid one at offset %d (mid-file corruption); Open refuses to avoid discarding %d byte(s)", validEnd, rep.WalBytes-validEnd)
			case inconclusive:
				rep.WalStatus = VerifyCorruption
				rep.Detail = fmt.Sprintf("could not confirm the invalid record at offset %d is a partial final write within the probe budget; classified as corruption (fail closed)", validEnd)
			default:
				rep.WalStatus = VerifyTornTail
				rep.Detail = fmt.Sprintf("partial final record at offset %d (torn tail — Open truncates it and recovers)", validEnd)
			}
			return nil
		}
		rep.WalRecords++
		validEnd += int64(8 + int(plen))
	}
}

// verifyWalV2 classifies a version-2 WAL at GROUP granularity, mirroring replayV2:
// DATA records buffer into a pending group; a valid opCommit closes it (WalRecords
// counts only COMMITTED data records, so a torn/uncommitted trailing group is not
// counted). A torn record is classified via the group-granularity discriminator —
// a fully-valid committed group after it is VerifyCorruption (Open refuses); none
// is VerifyTornTail (recoverable). A clean EOF with a non-empty uncommitted pending
// group is ALSO VerifyTornTail (recoverable), NOT VerifyClean — the trailing group
// never had its commit/fsync land, so Open truncates it and recovers the prefix.
func verifyWalV2(f *os.File, r *bufio.Reader, rep *VerifyReport, startEnd int64) error {
	lastCommitEnd := startEnd
	offset := startEnd
	var pendingCount uint32
	var pendingHi uint64

	// classifyTorn fills the report for a stop-point at `at` via the group probe.
	classifyTorn := func(at int64) error {
		mid, inconclusive, perr := probeForValidCommitGroup(f, at)
		if perr != nil {
			return perr
		}
		rep.FirstBadOffset = at
		switch {
		case mid:
			rep.WalStatus = VerifyCorruption
			rep.Detail = fmt.Sprintf("a fully-committed group follows the invalid record at offset %d (mid-file corruption); Open refuses to avoid discarding %d byte(s)", at, rep.WalBytes-lastCommitEnd)
		case inconclusive:
			rep.WalStatus = VerifyCorruption
			rep.Detail = fmt.Sprintf("could not confirm the invalid tail at offset %d is an un-acked in-flight group within the probe budget; classified as corruption (fail closed)", at)
		default:
			rep.WalStatus = VerifyTornTail
			rep.Detail = fmt.Sprintf("un-acked in-flight group at offset %d (torn tail — Open truncates it and recovers the committed prefix)", at)
		}
		return nil
	}

	var hdr [8]byte
	for {
		torn := false
		cleanEOF := false
		if n, e := io.ReadFull(r, hdr[:]); e != nil {
			if e == io.EOF && n == 0 {
				cleanEOF = true
			} else {
				torn = true
			}
		}
		var payload []byte
		var plen uint32
		if !torn && !cleanEOF {
			crc := binary.LittleEndian.Uint32(hdr[0:4])
			plen = binary.LittleEndian.Uint32(hdr[4:8])
			if plen == 0 || plen > maxRecordSize {
				torn = true
			} else {
				payload = make([]byte, plen)
				if _, e := io.ReadFull(r, payload); e != nil {
					torn = true
				} else if crc32.ChecksumIEEE(payload) != crc {
					torn = true
				}
			}
		}
		var ent entry
		if !torn && !cleanEOF {
			var ok bool
			if ent, ok = decodePayload(payload); !ok {
				torn = true
			}
		}

		if cleanEOF {
			if pendingCount > 0 {
				// A trailing group whose commit/fsync never landed → recoverable.
				rep.FirstBadOffset = lastCommitEnd
				rep.WalStatus = VerifyTornTail
				rep.Detail = fmt.Sprintf("un-acked in-flight group of %d record(s) at offset %d with no trailing commit (torn tail — Open truncates it and recovers the committed prefix)", pendingCount, lastCommitEnd)
			}
			return nil
		}
		if torn {
			return classifyTorn(offset)
		}

		if ent.op == opCommit {
			if ent.count != pendingCount || ent.seq != pendingHi {
				// CRC-valid but structurally inconsistent → real corruption.
				rep.FirstBadOffset = offset
				rep.WalStatus = VerifyCorruption
				rep.Detail = fmt.Sprintf("commit record at offset %d does not match its group (count %d/seq %d vs %d records/high-water %d); Open refuses", offset, ent.count, ent.seq, pendingCount, pendingHi)
				return nil
			}
			rep.WalRecords += int(pendingCount) // only COMMITTED data records count
			offset += int64(8 + int(plen))
			lastCommitEnd = offset
			pendingCount = 0
			pendingHi = 0
			continue
		}

		// DATA record — buffer into the pending group.
		pendingCount++
		pendingHi = ent.seq
		if pendingCount > maxGroupRecords {
			return classifyTorn(offset) // no commit within the format bound → malformed
		}
		offset += int64(8 + int(plen))
	}
}

// verifySnap classifies the snapshot at snapPath read-only. It peeks the magic
// and version itself (so the status doesn't depend on loadSnapshot's error
// strings) and delegates the CRC + structural check to loadSnapshot.
func verifySnap(snapPath string, rep *VerifyReport) error {
	b, err := os.ReadFile(snapPath)
	if err != nil {
		if os.IsNotExist(err) {
			rep.SnapExists = false
			rep.SnapStatus = SnapAbsent
			return nil
		}
		return err
	}
	rep.SnapExists = true
	if len(b) < 4 || string(b[0:4]) != snapMagic {
		rep.SnapStatus = SnapBadMagic
		return nil
	}
	if len(b) < 5 {
		rep.SnapStatus = SnapCorrupt // magic present but truncated before the version byte
		return nil
	}
	if b[4] != snapVersion {
		rep.SnapStatus = SnapVersionUnsupported
		return nil
	}
	// Header is well-formed — let loadSnapshot do the CRC + truncation checks.
	_, coveredSeq, lerr := loadSnapshot(snapPath)
	if lerr != nil {
		rep.SnapStatus = SnapCorrupt
		return nil
	}
	rep.SnapStatus = SnapClean
	rep.SnapCoveredSeq = coveredSeq
	return nil
}
