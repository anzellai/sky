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
	// a versioned WAL, or at 0 on a legacy headerless one.
	var validEnd int64
	if head, _ := r.Peek(walHeaderLen); len(head) >= walHeaderLen && string(head[0:4]) == walMagic {
		version := head[4]
		rep.WalVersion = int(version)
		if version != walVersion {
			// A version this binary doesn't understand — Open refuses to avoid
			// misparsing a newer format and truncating good data. Don't scan on.
			rep.WalStatus = VerifyVersionUnsupported
			rep.Detail = fmt.Sprintf("WAL header version %d; this binary understands version %d", version, walVersion)
			return nil
		}
		_, _ = r.Discard(walHeaderLen)
		validEnd = walHeaderLen
	}
	// else: legacy headerless WAL (version 0) — records begin at offset 0.

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
