package bluedb

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// mustNotPanic runs fn and converts a panic into a test failure naming the input,
// instead of aborting the whole test binary with a stack trace. The bounds guards
// in decodeDataVersion / changelogTsOf exist precisely so this never fires: both
// parse bytes that came off an iterator, i.e. off DISK, where a truncated or
// corrupt key is a data-integrity event to be reported — never a process crash in
// the middle of a scan.
func mustNotPanic(t *testing.T, what string, key []byte, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked on key %x (len=%d): %v", what, key, len(key), r)
		}
	}()
	fn()
}

// corruptKeyCorpus returns keys that are NOT well-formed: every proper truncation
// of a valid key of each kind, single-byte mutations of the trailing length byte
// and the leading tag, plus deterministic random byte strings in the length range
// where the arithmetic in the two parsers is at its most dangerous.
func corruptKeyCorpus(t *testing.T) [][]byte {
	t.Helper()

	valid := [][]byte{
		encodeDataKey([]byte{}, HLC{WallMs: 1, Logical: 1}),
		encodeDataKey([]byte("k"), HLC{WallMs: 7, Logical: 3}),
		encodeDataKey([]byte{0x41, 0x00, 0x42}, HLC{WallMs: 1 << 40, Logical: 9}),
		encodeChangelogKey(HLC{WallMs: 5, Logical: 0}),
		encodeMetaKey(metaHLCHi),
		encodeMetaKey(metaChangelogCursor),
	}

	var out [][]byte
	out = append(out, nil, []byte{})

	for _, k := range valid {
		// Every proper prefix — the classic "short read / truncated SSTable" shape.
		for n := 0; n < len(k); n++ {
			out = append(out, append([]byte(nil), k[:n]...))
		}
		// Corrupt the trailing length byte, which is what both parsers key off.
		for _, b := range []byte{0x00, 0x01, 0x0C, 0x0E, 0x7F, 0xFF} {
			c := append([]byte(nil), k...)
			c[len(c)-1] = b
			out = append(out, c)
		}
		// Corrupt the leading tag.
		for _, b := range []byte{0x01, 0x02, 0x03, 0xFF} {
			c := append([]byte(nil), k...)
			c[0] = b
			out = append(out, c)
		}
		// Over-long: trailing garbage after a valid key.
		out = append(out, append(append([]byte(nil), k...), 0xAA, 0xBB))
	}

	// Deterministic random noise, seeded — no time-based seeds.
	rng := rand.New(rand.NewPCG(0x5EED, 0xB1EDB))
	for i := 0; i < 2000; i++ {
		n := rng.IntN(20) // 0..19 spans both sides of every length threshold
		k := make([]byte, n)
		for j := range k {
			k[j] = byte(rng.UintN(256))
		}
		out = append(out, k)
	}
	return out
}

// TestDecodeDataVersionRejectsCorruptKeysWithoutPanic is the regression test for
// the bounds defect: decodeDataVersion computed start = len(key)-1-12 and sliced
// with it unchecked, so any key shorter than 13 bytes indexed negatively and
// panicked. Remove the length/trailer guard in keys.go and this test goes red.
func TestDecodeDataVersionRejectsCorruptKeysWithoutPanic(t *testing.T) {
	for _, k := range corruptKeyCorpus(t) {
		key := k
		mustNotPanic(t, "decodeDataVersion", key, func() {
			ts, ok := decodeDataVersion(key)
			if ok {
				// Accepting is only legitimate for something that really is a
				// well-formed versioned data key; then it must round-trip.
				if len(key) < 2+dataSuffixLen || key[len(key)-1] != dataLenByte {
					t.Errorf("decodeDataVersion accepted malformed key %x", key)
					return
				}
				// decodeDataVersion reads the version suffix only and does not
				// police the tag, so round-tripping is asserted for keys that are
				// actually in the data keyspace.
				if key[0] == tagData {
					if got := encodeDataKey(key[1:len(key)-1-dataSuffixLen], ts); !bytes.Equal(got, key) {
						t.Errorf("decodeDataVersion(%x)=%+v did not round-trip (got %x)", key, ts, got)
					}
				}
			}
		})
	}
}

// TestChangelogTsOfRejectsCorruptKeysWithoutPanic is the regression test for the
// second bounds defect: changelogTsOf sliced key[1:13] unchecked, so any key
// shorter than 13 bytes panicked. Remove the guard and this test goes red.
func TestChangelogTsOfRejectsCorruptKeysWithoutPanic(t *testing.T) {
	for _, k := range corruptKeyCorpus(t) {
		key := k
		mustNotPanic(t, "changelogTsOf", key, func() {
			ts, ok := changelogTsOf(key)
			if ok {
				if !bytes.Equal(encodeChangelogKey(ts), key) {
					t.Errorf("changelogTsOf accepted %x but it does not round-trip", key)
				}
			}
		})
	}
}

// TestDecodersAcceptWellFormedKeys is the other half of the guard contract: a
// guard that answered ok=false for everything would pass the no-panic tests and
// silently break every reader. Well-formed keys MUST still decode.
func TestDecodersAcceptWellFormedKeys(t *testing.T) {
	userKeys := [][]byte{{}, {0x00}, {0xFF}, {0xFF, 0xFF}, {0x41, 0x00, 0x42}, []byte("user/42")}
	versions := []HLC{
		{WallMs: 1, Logical: 0},
		{WallMs: 1, Logical: 1},
		{WallMs: 1 << 40, Logical: 7},
		{WallMs: 0xFFFFFFFFFFFF, Logical: 0xFFFFFFFF},
	}
	for _, uk := range userKeys {
		for _, v := range versions {
			key := encodeDataKey(uk, v)
			got, ok := decodeDataVersion(key)
			if !ok {
				t.Fatalf("decodeDataVersion rejected the well-formed key %x", key)
			}
			if got != v {
				t.Fatalf("decodeDataVersion(%x)=%+v, want %+v", key, got, v)
			}
		}
	}
	for _, v := range versions {
		key := encodeChangelogKey(v)
		got, ok := changelogTsOf(key)
		if !ok {
			t.Fatalf("changelogTsOf rejected the well-formed key %x", key)
		}
		if got != v {
			t.Fatalf("changelogTsOf(%x)=%+v, want %+v", key, got, v)
		}
	}
}

// TestSplitNeverPanicsOnCorruptKeys re-asserts the F2 guard over the same corpus:
// skydbSplit is the third parser reading the trailing length byte, and Pebble
// calls it on every key it reads.
func TestSplitNeverPanicsOnCorruptKeys(t *testing.T) {
	for _, k := range corruptKeyCorpus(t) {
		key := k
		mustNotPanic(t, "skydbSplit", key, func() {
			n := skydbSplit(key)
			if n < 0 || n > len(key) {
				t.Errorf("skydbSplit(%x)=%d out of range [0,%d]", key, n, len(key))
			}
		})
	}
}
