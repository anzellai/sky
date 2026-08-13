package bluedb

import (
	"bytes"

	"github.com/cockroachdb/pebble/v2"
)

// comparerName is the PERMANENT format string baked into every SSTable's metadata.
// A store created with it refuses to open under any other comparer. Changing the
// byte layout, the HLC width, the inversion, or the length-byte convention
// requires a new name ("skydb.mvcc.v2") AND a full store rewrite/migration.
const comparerName = "skydb.mvcc.v1"

// skydbComparer is the LOCKED Comparer (§2.4). It MIRRORS the techniques of
// Pebble's shipped cockroachkvs.Comparer exactly — prefix-only Separator/Successor,
// ImmediateSuccessor = a‖0x00, AbbreviatedKey over key[:Split], a Split guard for
// an oversized length byte, and BOTH point (13B) and range (12B) suffix comparers —
// but keeps our fixed-12B *inverted* suffix layout and three-namespace tag scheme
// (wholesale adoption was rejected because cockroachkvs.Split mis-reads our
// unversioned changelog/metadata keys). Gated GREEN by pebble.CheckComparer.
var skydbComparer = &pebble.Comparer{
	Name:                 comparerName,
	Split:                skydbSplit,
	Compare:              skydbCompare,
	Equal:                skydbEqual,
	AbbreviatedKey:       skydbAbbrev,
	Separator:            skydbSeparator,
	Successor:            skydbSuccessor,
	ImmediateSuccessor:   skydbImmediateSuccessor,
	ComparePointSuffixes: skydbComparePointSuffixes,
	CompareRangeSuffixes: skydbCompareRangeSuffixes,
	// FormatKey borrowed from the default so CheckComparer's error paths never
	// nil-panic; ValidateKey left nil (optional structural check).
	FormatKey: pebble.DefaultComparer.FormatKey,
}

// skydbSplit returns the prefix length (user-key + sentinel, incl. the tag). It
// reads the TRAILING LENGTH BYTE arithmetically and NEVER scans for 0x00, so any
// 0x00 inside the user-key is irrelevant and the function is tag-independent —
// which is REQUIRED for base.CheckComparer, whose leading-byte-stripping test
// (see internal/base.CheckComparer) would otherwise fail a tag-dispatched Split.
// Includes the F2 guard so a corrupt/oversized length byte can never produce a
// negative or out-of-range boundary.
func skydbSplit(key []byte) int {
	if len(key) == 0 {
		return 0
	}
	suffixLen := int(key[len(key)-1]) // trailing length byte = len(version)+1, or 0
	if suffixLen == 0 {
		return len(key) // unversioned / flat key: whole key is the prefix
	}
	if suffixLen > len(key)-1 { // F2 GUARD: suffix cannot exceed the body
		return len(key)
	}
	return len(key) - suffixLen
}

// skydbCompare orders user-key ascending, then version descending (newest first).
// It is byte-identical to Pebble's defaultCompare(Split, ComparePointSuffixes) so
// it stays consistent under MakeAssertComparer. It MUST Split first (a whole-key
// bytes.Compare would interleave a 0x00-bearing user-key against a shorter one).
func skydbCompare(a, b []byte) int {
	na, nb := skydbSplit(a), skydbSplit(b)
	if c := bytes.Compare(a[:na], b[:nb]); c != 0 {
		return c
	}
	return skydbComparePointSuffixes(a[na:], b[nb:])
}

func skydbEqual(a, b []byte) bool { return skydbCompare(a, b) == 0 }

// skydbComparePointSuffixes compares 13-byte point suffixes (invTs12 ‖ lenByte).
// Because the invTs is pre-inverted, a plain ascending bytes.Compare yields
// descending real commitTs == newest first. The empty suffix sorts before any
// non-empty suffix.
func skydbComparePointSuffixes(a, b []byte) int {
	if len(a) == 0 || len(b) == 0 {
		return cmpInt(len(a), len(b)) // empty < non-empty
	}
	return bytes.Compare(a, b)
}

// skydbCompareRangeSuffixes compares suffixes where either may originate from a
// range key. Point suffixes are 13B (invTs12 ‖ lenByte); range suffixes are 12B
// (invTs12, no length trailer). It MUST strip the trailing length byte so a 13B
// point suffix and a 12B range suffix order consistently (comparing them under the
// default comparer would mis-order on the length trailer — a silent, irreversible
// range-key bug). Since every point suffix shares the constant 0x0D trailer,
// stripping never perturbs point-suffix order.
func skydbCompareRangeSuffixes(a, b []byte) int {
	if len(a) == 0 || len(b) == 0 {
		return cmpInt(len(a), len(b))
	}
	return bytes.Compare(stripLenByte(a), stripLenByte(b))
}

// stripLenByte drops the trailing length byte of a point suffix, yielding the bare
// 12-byte inverted version. Guarded against an empty input.
func stripLenByte(s []byte) []byte {
	if len(s) == 0 {
		return s
	}
	return s[:len(s)-1]
}

// skydbAbbrev computes the uint64 fast-path digest over the PREFIX only (§2.4 F3).
// Over the whole key, two versions of one user-key would abbreviate differently and
// the fast path would disagree with Compare.
func skydbAbbrev(key []byte) uint64 {
	return pebble.DefaultComparer.AbbreviatedKey(key[:skydbSplit(key)])
}

// skydbSeparator produces a key k with Compare(a,k) <= 0 and Compare(k,b) < 0,
// operating on the PREFIX WITHOUT its sentinel and re-appending one sentinel — the
// cockroachkvs technique (§2.4 F1/F2). Never touches the version suffix, so it can
// never truncate inside the suffix and bake a bad length byte into the SSTable
// index. Callers guarantee len(a) > 0 and len(b) > 0.
func skydbSeparator(dst, a, b []byte) []byte {
	aKey, ok := keyPartNoSentinel(a)
	if !ok {
		return append(dst, a...)
	}
	bKey, ok := keyPartNoSentinel(b)
	if !ok {
		return append(dst, a...)
	}
	if bytes.Equal(aKey, bKey) || len(aKey) == 0 || len(bKey) == 0 {
		return append(dst, a...)
	}
	n := len(dst)
	dst = pebble.DefaultComparer.Separator(dst, aKey, bKey)
	if bytes.Equal(aKey, dst[n:]) {
		// No proper shortening found — can't do better than a.
		return append(dst[:n], a...)
	}
	// A proper separator (> aKey) was found; re-append the sentinel to make it a
	// bare, valid prefix.
	return append(dst, sentinel)
}

// skydbSuccessor produces a shortened key k with Compare(a,k) <= 0, prefix-only.
func skydbSuccessor(dst, a []byte) []byte {
	if len(a) == 0 {
		return append(dst, sentinel)
	}
	aKey, ok := keyPartNoSentinel(a)
	if !ok {
		return append(dst, a...)
	}
	n := len(dst)
	dst = pebble.DefaultComparer.Successor(dst, aKey)
	if bytes.Equal(aKey, dst[n:]) {
		return append(dst[:n], a...)
	}
	return append(dst, sentinel)
}

// skydbImmediateSuccessor appends 0x00 to a bare prefix a, yielding the smallest
// prefix strictly greater than every version of a (used by the range-scan
// jump-seek, §2.5). a is guaranteed to be a bare prefix (Split(a) == len(a)).
func skydbImmediateSuccessor(dst, a []byte) []byte {
	return append(append(dst, a...), sentinel)
}

// keyPartNoSentinel returns the user-key part WITHOUT the trailing sentinel byte
// (mirrors cockroachkvs.getKeyPartFromEngineKey). prefix = key[:Split(key)] ==
// tag ‖ userKey ‖ 0x00; the key-part is prefix minus its final sentinel. Returns
// ok=false for a degenerate key with no room for a sentinel.
func keyPartNoSentinel(key []byte) (part []byte, ok bool) {
	n := skydbSplit(key)
	if n < 1 {
		return nil, false
	}
	return key[:n-1], true
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
