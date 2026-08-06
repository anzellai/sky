package bluedb

import (
	"bytes"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// pointSuffix builds the 13-byte point suffix (invTs12 ‖ lenByte) for a commitTs.
func pointSuffix(ts HLC) []byte {
	s := invert12(encodeHLC(ts))
	return append(s, dataLenByte)
}

// TestCheckComparer is THE irreversible-format gate (§8.1). It runs Pebble's
// mechanical base.CheckComparer over a representative, adversarial key set:
// 0x00-bearing user-keys, a key that is a prefix of another, the empty key, all-0xFF
// keys — crossed with multiple versions (and, via markers, the tombstone case shares
// the identical key layout). A failure here means the on-disk format is unsound
// BEFORE the first SSTable is written.
func TestCheckComparer(t *testing.T) {
	userKeys := [][]byte{
		{},                 // empty key
		{0x01},             // prefix of {0x01, 0x02}
		{0x01, 0x02},       // extends {0x01}
		{0x41, 0x00, 0x42}, // 0x00-bearing user-key
		{0x00},             // a single 0x00 user-key
		{0xFF},             // high boundary
		{0xFF, 0xFF},       // all-0xFF (Successor edge)
		{0x41, 0x42, 0x43}, // "ABC"
	}
	prefixes := make([][]byte, 0, len(userKeys))
	for _, uk := range userKeys {
		prefixes = append(prefixes, dataKeyPrefix(uk))
	}

	versions := []HLC{
		{WallMs: 1, Logical: 0},
		{WallMs: 1, Logical: 1},
		{WallMs: 2, Logical: 0},
		{WallMs: 1 << 40, Logical: 7},
		{WallMs: 0xFFFFFFFFFFFF, Logical: 0xFFFFFFFF}, // near-max (smallest invTs)
	}
	suffixes := make([][]byte, 0, len(versions))
	for _, v := range versions {
		suffixes = append(suffixes, pointSuffix(v))
	}

	if err := pebble.CheckComparer(skydbComparer, prefixes, suffixes); err != nil {
		t.Fatalf("base.CheckComparer failed on skydb.mvcc.v1: %v", err)
	}
}

// TestComparerName pins the permanent format string.
func TestComparerName(t *testing.T) {
	if skydbComparer.Name != "skydb.mvcc.v1" {
		t.Fatalf("comparer name drifted: %q", skydbComparer.Name)
	}
}

// TestSplitTagIndependent verifies Split reads the trailing length byte
// arithmetically and is tag-independent (the property CheckComparer's leading-byte
// stripping relies on).
func TestSplitTagIndependent(t *testing.T) {
	uk := []byte{0x41, 0x00, 0x42} // contains 0x00
	full := encodeDataKey(uk, HLC{WallMs: 5, Logical: 2})
	wantPrefix := dataKeyPrefix(uk)
	if n := skydbSplit(full); n != len(wantPrefix) {
		t.Fatalf("Split(dataKey)=%d, want %d", n, len(wantPrefix))
	}
	if !bytes.Equal(full[:skydbSplit(full)], wantPrefix) {
		t.Fatalf("Split prefix mismatch: %x vs %x", full[:skydbSplit(full)], wantPrefix)
	}
	// Unversioned keys (trailing 0x00) split to full length.
	cl := encodeChangelogKey(HLC{WallMs: 9, Logical: 0})
	if n := skydbSplit(cl); n != len(cl) {
		t.Fatalf("Split(changelog)=%d, want %d", n, len(cl))
	}
	mk := encodeMetaKey(metaHLCHi)
	if n := skydbSplit(mk); n != len(mk) {
		t.Fatalf("Split(meta)=%d, want %d", n, len(mk))
	}
	// F2 guard: an oversized trailing length byte must not negative-index.
	corrupt := []byte{0x00, 0x41, 0xFF}
	if n := skydbSplit(corrupt); n != len(corrupt) {
		t.Fatalf("Split(corrupt-lenbyte)=%d, want %d (F2 guard)", n, len(corrupt))
	}
}

// TestVersionOrderingNewestFirst confirms the inverted suffix sorts a larger
// commitTs earlier under Compare (newest first among a user-key's versions).
func TestVersionOrderingNewestFirst(t *testing.T) {
	uk := []byte("k")
	older := encodeDataKey(uk, HLC{WallMs: 10, Logical: 0})
	newer := encodeDataKey(uk, HLC{WallMs: 20, Logical: 0})
	if skydbCompare(newer, older) >= 0 {
		t.Fatalf("expected newer < older under Compare (newest first); got %d", skydbCompare(newer, older))
	}
}

// TestPrefixBoundaryDistinctKeys is the grill C1 boundary: two distinct user-keys of
// EQUAL length must not be confused. Their prefixes differ in bytes even though
// Split returns the same integer.
func TestPrefixBoundaryDistinctKeys(t *testing.T) {
	a := encodeDataKey([]byte("aa"), HLC{WallMs: 1, Logical: 0})
	b := encodeDataKey([]byte("ab"), HLC{WallMs: 1, Logical: 0})
	if skydbSplit(a) != skydbSplit(b) {
		t.Fatalf("expected equal Split ints for equal-length user-keys")
	}
	if bytes.Equal(a[:skydbSplit(a)], b[:skydbSplit(b)]) {
		t.Fatalf("distinct user-keys must have distinct prefix BYTES (C1)")
	}
}
