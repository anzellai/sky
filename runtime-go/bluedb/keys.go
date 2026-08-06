// Package bluedb is the Sky-native reactive data engine (L1 substrate).
//
// Phase 1a delivers the IRREVERSIBLE on-disk foundation over CockroachDB's
// Pebble: the LOCKED MVCC key encoding + custom Comparer ("skydb.mvcc.v1"), a
// versioned Put/Get/snapshot-read path, the single-writer group-commit committer
// with HLC assignment + restart floor + commit-metadata-in-batch, and the
// changelog write (opaque L1 bytes). See docs/bluedb/phase1-engine-design.md.
//
// Everything in keys.go + comparer.go is FROZEN before the first SSTable is
// written: Comparer.Name is baked into SSTable metadata and a store refuses to
// open under a different comparer. Do not change the byte layout, the HLC width,
// the inversion, or the length-byte convention without a new Name
// ("skydb.mvcc.v2") AND a full store rewrite.
package bluedb

import "encoding/binary"

// Keyspace discriminator tags (§2.1). The FIRST byte of every storage key.
const (
	tagData      byte = 0x00 // MVCC data: <uk> 0x00 <invTs 12> <lenByte>   (versioned)
	tagChangelog byte = 0x01 // changelog: <commitTs 12 BE, non-inverted>    (unversioned)
	tagMeta      byte = 0x02 // metadata:  <ascii name>                      (unversioned)
)

// Value markers (§2.5). Every stored MVCC value carries a 1-byte discriminator so
// a versioned delete (tombstone) is distinguishable from a put of empty bytes.
const (
	markerPut       byte = 0x01 // value bytes follow
	markerTombstone byte = 0x00 // versioned delete; no value follows
)

const (
	hlcEncodedLen = 12   // wallMs(BE8) ‖ logical(BE4)
	dataSuffixLen = 13   // invTs(12) ‖ lenByte(1)
	sentinel      = 0x00 // one byte separating user-key from the version suffix
	dataLenByte   = 0x0D // trailing length byte for a versioned data key = dataSuffixLen
	unversioned   = 0x00 // trailing length byte for an unversioned (flat) key
)

// Metadata key names (tag 0x02).
const (
	metaHLCHi           = "hlc_hi"           // §3.3 restart floor high-water
	metaChangelogCursor = "changelog_cursor" // §3.2 last committed changelog ts
	metaGCThreshold     = "gc_threshold"     // §5.2 (written by GC in phase 1b)
	metaSchemaVersion   = "schema_version"
)

// encodeHLC returns the 12-byte big-endian encoding of an HLC (numeric order ==
// lexicographic byte order because big-endian).
func encodeHLC(h HLC) []byte {
	out := make([]byte, hlcEncodedLen)
	binary.BigEndian.PutUint64(out[0:8], h.WallMs)
	binary.BigEndian.PutUint32(out[8:12], h.Logical)
	return out
}

// decodeHLC parses a 12-byte big-endian HLC. Callers guarantee len(b) >= 12.
func decodeHLC(b []byte) HLC {
	return HLC{
		WallMs:  binary.BigEndian.Uint64(b[0:8]),
		Logical: binary.BigEndian.Uint32(b[8:12]),
	}
}

// invert12 returns the bitwise-NOT of a 12-byte slice into a fresh slice. A larger
// real commitTs produces a smaller inverted suffix, so newest sorts FIRST under an
// ascending byte compare (§2.3).
func invert12(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = ^b[i]
	}
	return out
}

// encodeDataKey builds the LOCKED MVCC data key (§2.2):
//
//	0x00 ‖ userKey ‖ 0x00 ‖ ~(wallMs BE8 ‖ logical BE4) ‖ 0x0D
//
// The user-key is opaque and MAY contain 0x00 bytes; Split never scans for the
// separator, it reads the trailing length byte arithmetically.
func encodeDataKey(userKey []byte, commitTs HLC) []byte {
	out := make([]byte, 0, 1+len(userKey)+1+dataSuffixLen)
	out = append(out, tagData)
	out = append(out, userKey...)
	out = append(out, sentinel)
	out = append(out, invert12(encodeHLC(commitTs))...)
	out = append(out, dataLenByte)
	return out
}

// dataKeyPrefix returns 0x00 ‖ userKey ‖ 0x00 — the prefix portion (== key[:Split])
// of every version of userKey. Used to build seek targets and boundary tests.
func dataKeyPrefix(userKey []byte) []byte {
	out := make([]byte, 0, 1+len(userKey)+1)
	out = append(out, tagData)
	out = append(out, userKey...)
	out = append(out, sentinel)
	return out
}

// decodeDataVersion extracts the commitTs from a full data key by un-inverting the
// 12-byte version that sits between the sentinel and the trailing length byte.
func decodeDataVersion(key []byte) HLC {
	// version occupies [len-1-12, len-1)
	start := len(key) - 1 - hlcEncodedLen
	inv := key[start : len(key)-1]
	return decodeHLC(invert12(inv))
}

// encodeChangelogKey builds an unversioned changelog key (§4.1):
//
//	0x01 ‖ commitTs(12 BE, NON-inverted) ‖ 0x00
//
// Non-inverted so an ascending scan is chronological. The trailing 0x00 length
// byte makes Split return len(key) (whole key is the prefix) uniformly with the
// data keyspace — the phase-1a deviation that lets Split stay tag-independent so
// base.CheckComparer's leading-byte-stripping invariant holds.
func encodeChangelogKey(commitTs HLC) []byte {
	out := make([]byte, 0, 1+hlcEncodedLen+1)
	out = append(out, tagChangelog)
	out = append(out, encodeHLC(commitTs)...)
	out = append(out, unversioned)
	return out
}

// changelogTsOf parses the commitTs out of a changelog key.
func changelogTsOf(key []byte) HLC {
	return decodeHLC(key[1 : 1+hlcEncodedLen])
}

// changelogKeyspaceBounds returns the [lo, hi) bounds spanning the entire
// changelog keyspace (tag 0x01) for iterator scoping.
func changelogKeyspaceBounds() (lo, hi []byte) {
	return []byte{tagChangelog}, []byte{tagMeta}
}

// encodeMetaKey builds an unversioned metadata key: 0x02 ‖ name ‖ 0x00.
func encodeMetaKey(name string) []byte {
	out := make([]byte, 0, 1+len(name)+1)
	out = append(out, tagMeta)
	out = append(out, name...)
	out = append(out, unversioned)
	return out
}
