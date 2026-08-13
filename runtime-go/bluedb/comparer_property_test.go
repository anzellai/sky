package bluedb

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// This file closes the single largest hole in the irreversible-format gate.
//
// pebble.CheckComparer (run by TestCheckComparer) mechanically validates Compare,
// Equal, Split and the suffix comparers — and then stops, on a
// "// TODO(radu): check more methods". It NEVER exercises Separator, Successor,
// ImmediateSuccessor or AbbreviatedKey. Those four are not advisory: their output
// is written into SSTable index blocks and block-property filters. A bug in them
// produces an index that disagrees with Compare — reads silently skip live keys —
// and it is unfixable in place, because the bad separators are already on disk.
// The only remedy is a new comparer name and a full store rewrite. So they are
// gated here, by property, before the first SSTable exists.
//
// All randomness is a fixed-seed PCG. No time-based seeds: a format gate that
// passes or fails depending on the clock is not a gate.

const (
	propSeed1 = 0xB1EDB0
	propSeed2 = 0x5C0DE
)

// propertyCorpus returns the adversarial key corpus: the exact user-keys
// TestCheckComparer uses (empty, single 0x00, 0xFF, 0xFF 0xFF, a 0x00-bearing key,
// and prefix pairs) plus deterministic random ones — each expanded into its bare
// prefix and its versioned keys — plus the unversioned changelog and metadata
// keyspaces, which share the comparer and which CheckComparer never saw.
func propertyCorpus() (prefixes [][]byte, keys [][]byte) {
	userKeys := [][]byte{
		{},                 // empty key
		{0x01},             // prefix of {0x01, 0x02}
		{0x01, 0x02},       // extends {0x01}
		{0x41, 0x00, 0x42}, // 0x00-bearing user-key
		{0x00},             // a single 0x00 user-key
		{0xFF},             // high boundary
		{0xFF, 0xFF},       // all-0xFF (Successor edge)
		{0x41, 0x42, 0x43}, // "ABC"
		{0x41, 0x42},       // prefix of "ABC"
		{0x00, 0x00},       // all-zero (ImmediateSuccessor edge)
		{0xFF, 0x00},
		{0x00, 0xFF},
	}

	rng := rand.New(rand.NewPCG(propSeed1, propSeed2))
	for i := 0; i < 48; i++ {
		n := rng.IntN(9)
		uk := make([]byte, n)
		for j := range uk {
			// Bias towards 0x00 and 0xFF: the interesting boundaries for
			// Separator/Successor/ImmediateSuccessor all live there.
			switch rng.IntN(4) {
			case 0:
				uk[j] = 0x00
			case 1:
				uk[j] = 0xFF
			default:
				uk[j] = byte(rng.UintN(256))
			}
		}
		userKeys = append(userKeys, uk)
	}

	versions := []HLC{
		{WallMs: 1, Logical: 0},
		{WallMs: 1, Logical: 1},
		{WallMs: 2, Logical: 0},
		{WallMs: 1 << 40, Logical: 7},
		{WallMs: 0xFFFFFFFFFFFF, Logical: 0xFFFFFFFF},
	}

	for _, uk := range userKeys {
		p := dataKeyPrefix(uk)
		prefixes = append(prefixes, p)
		keys = append(keys, p)
		for _, v := range versions {
			keys = append(keys, encodeDataKey(uk, v))
		}
	}
	// Unversioned keyspaces: Split(k) == len(k), so they are their own prefixes.
	for _, v := range versions {
		k := encodeChangelogKey(v)
		prefixes = append(prefixes, k)
		keys = append(keys, k)
	}
	for _, name := range []string{metaHLCHi, metaChangelogCursor, metaGCThreshold, metaSchemaVersion} {
		k := encodeMetaKey(name)
		prefixes = append(prefixes, k)
		keys = append(keys, k)
	}
	return prefixes, keys
}

// isBarePrefix reports whether k carries no version suffix — Split(k) == len(k).
// Separator/Successor/ImmediateSuccessor must produce only such keys when they
// shorten, because a key truncated INSIDE the 13-byte suffix would bake a bogus
// trailing length byte into an SSTable index block.
func isBarePrefix(k []byte) bool { return skydbSplit(k) == len(k) }

// hasIncrementableByte reports whether the key-part has any byte below 0xFF, i.e.
// whether a strictly-greater shortened key exists at all. Every key in the data,
// changelog and metadata keyspaces qualifies (their tags are 0x00/0x01/0x02), so
// this is a statement of intent rather than an escape hatch.
func hasIncrementableByte(part []byte) bool {
	for _, b := range part {
		if b != 0xFF {
			return true
		}
	}
	return false
}

// TestSeparatorProperties: for every ordered pair a < b, Separator must land in
// [a, b) — the Pebble contract — and, whenever it actually shortens, the result
// must be a bare prefix. It is also checked with a non-empty dst, because both
// hooks re-slice dst (dst[:n]) on their fallback paths and a bad re-slice would
// corrupt the caller's buffer rather than the key.
func TestSeparatorProperties(t *testing.T) {
	_, keys := propertyCorpus()
	pairs := 0
	for _, a := range keys {
		for _, b := range keys {
			if skydbCompare(a, b) >= 0 {
				continue
			}
			pairs++

			sep := skydbSeparator(nil, a, b)
			if c := skydbCompare(a, sep); c > 0 {
				t.Fatalf("Separator(%x, %x)=%x violates Compare(a,sep)<=0 (got %d)", a, b, sep, c)
			}
			if c := skydbCompare(sep, b); c >= 0 {
				t.Fatalf("Separator(%x, %x)=%x violates Compare(sep,b)<0 (got %d)", a, b, sep, c)
			}
			// A separator that differs from a has been shortened, and a shortened
			// key MUST be prefix-only: never a truncation inside the version
			// suffix. (When no proper separator exists the hook returns a verbatim,
			// which is legal under Pebble's contract and keeps a's own suffix.)
			if !bytes.Equal(sep, a) && !isBarePrefix(sep) {
				t.Fatalf("Separator(%x, %x)=%x is shortened but not a bare prefix (Split=%d, len=%d)",
					a, b, sep, skydbSplit(sep), len(sep))
			}

			// Same call with a non-empty dst: the result must be dst ‖ sep.
			dst := []byte{0xDE, 0xAD}
			got := skydbSeparator(append([]byte(nil), dst...), a, b)
			if !bytes.HasPrefix(got, dst) {
				t.Fatalf("Separator did not preserve dst: %x", got)
			}
			if !bytes.Equal(got[len(dst):], sep) {
				t.Fatalf("Separator(dst,...) appended %x, want %x", got[len(dst):], sep)
			}
		}
	}
	if pairs == 0 {
		t.Fatal("vacuous: no ordered pairs in the corpus")
	}
	t.Logf("checked %d ordered pairs", pairs)
}

// TestSuccessorProperties: Successor must never move backwards, must be prefix-only
// when it shortens, and — the assertion that makes this test bite — must ACTUALLY
// shorten for every key whose key-part has a byte below 0xFF. Without that last
// clause a Successor that returns its input unchanged would satisfy
// Compare(a, Successor(a)) <= 0 trivially and the test would be vacuous.
func TestSuccessorProperties(t *testing.T) {
	_, keys := propertyCorpus()
	shortened := 0
	for _, a := range keys {
		succ := skydbSuccessor(nil, a)
		if c := skydbCompare(a, succ); c > 0 {
			t.Fatalf("Successor(%x)=%x violates Compare(a,succ)<=0 (got %d)", a, succ, c)
		}

		part, ok := keyPartNoSentinel(a)
		mustShorten := ok && len(part) > 0 && hasIncrementableByte(part)
		if mustShorten {
			if bytes.Equal(succ, a) {
				t.Fatalf("Successor(%x) returned its input unchanged; a strictly greater shortened key exists (key-part %x)", a, part)
			}
			if !isBarePrefix(succ) {
				t.Fatalf("Successor(%x)=%x is shortened but not a bare prefix (Split=%d, len=%d)",
					a, succ, skydbSplit(succ), len(succ))
			}
			if c := skydbCompare(a, succ); c >= 0 {
				t.Fatalf("Successor(%x)=%x must be strictly greater once shortened (got %d)", a, succ, c)
			}
			shortened++
		}

		dst := []byte{0xDE, 0xAD}
		got := skydbSuccessor(append([]byte(nil), dst...), a)
		if !bytes.HasPrefix(got, dst) || !bytes.Equal(got[len(dst):], succ) {
			t.Fatalf("Successor(dst,%x)=%x inconsistent with Successor(nil,...)=%x", a, got, succ)
		}
	}
	if shortened == 0 {
		t.Fatal("vacuous: no key in the corpus exercised the shortening path")
	}
	t.Logf("%d keys exercised the shortening path", shortened)
}

// TestImmediateSuccessorProperties: ImmediateSuccessor(p) is the seek target the
// range scan jumps to in order to leave every version of p behind. It must be a
// bare prefix strictly greater than p; every versioned key under p must sort
// before it; and it must be IMMEDIATE — no key of any other prefix may sort
// strictly between p and it.
func TestImmediateSuccessorProperties(t *testing.T) {
	prefixes, keys := propertyCorpus()
	for _, p := range prefixes {
		is := skydbImmediateSuccessor(nil, p)

		if !isBarePrefix(is) {
			t.Fatalf("ImmediateSuccessor(%x)=%x is not a bare prefix (Split=%d, len=%d)",
				p, is, skydbSplit(is), len(is))
		}
		if c := skydbCompare(p, is); c >= 0 {
			t.Fatalf("ImmediateSuccessor(%x)=%x must be strictly greater (got %d)", p, is, c)
		}
		// Every key sharing prefix p sorts strictly before it — this is what makes
		// the jump-seek skip the whole version chain rather than land inside it.
		for _, k := range keys {
			if !bytes.Equal(k[:skydbSplit(k)], p) {
				continue
			}
			if c := skydbCompare(k, is); c >= 0 {
				t.Fatalf("key %x under prefix %x does not sort before ImmediateSuccessor %x (got %d)", k, p, is, c)
			}
		}
		// Immediate: nothing with a strictly greater prefix may sort below it.
		for _, k := range keys {
			kp := k[:skydbSplit(k)]
			if bytes.Compare(kp, p) <= 0 {
				continue
			}
			if c := skydbCompare(is, k); c > 0 {
				t.Fatalf("ImmediateSuccessor(%x)=%x is not immediate: key %x with a greater prefix sorts below it", p, is, k)
			}
		}

		dst := []byte{0xDE, 0xAD}
		got := skydbImmediateSuccessor(append([]byte(nil), dst...), p)
		if !bytes.HasPrefix(got, dst) || !bytes.Equal(got[len(dst):], is) {
			t.Fatalf("ImmediateSuccessor(dst,%x)=%x inconsistent with %x", p, got, is)
		}
	}
}

// TestAbbreviatedKeyMonotonicity: AbbreviatedKey is the uint64 fast path Pebble
// uses to skip a full Compare. It is only sound if a strict inequality between two
// abbreviations implies the same strict inequality under Compare. (Equal
// abbreviations carry no information — that is the fallback into Compare — so the
// prefix-only digest is required: over the whole key, two versions of one user-key
// would abbreviate differently and the fast path would disagree with Compare.)
func TestAbbreviatedKeyMonotonicity(t *testing.T) {
	_, keys := propertyCorpus()
	sameKeyDifferentVersion := 0
	for _, a := range keys {
		for _, b := range keys {
			av, bv := skydbAbbrev(a), skydbAbbrev(b)
			c := skydbCompare(a, b)
			if av < bv && c >= 0 {
				t.Fatalf("AbbreviatedKey(%x)=%d < AbbreviatedKey(%x)=%d but Compare=%d", a, av, b, bv, c)
			}
			if av > bv && c <= 0 {
				t.Fatalf("AbbreviatedKey(%x)=%d > AbbreviatedKey(%x)=%d but Compare=%d", a, av, b, bv, c)
			}
			// The prefix-only requirement: two versions of one user-key MUST
			// abbreviate identically even though Compare separates them.
			if bytes.Equal(a[:skydbSplit(a)], b[:skydbSplit(b)]) {
				if av != bv {
					t.Fatalf("keys %x and %x share a prefix but abbreviate differently (%d vs %d)", a, b, av, bv)
				}
				if c != 0 {
					sameKeyDifferentVersion++
				}
			}
		}
	}
	if sameKeyDifferentVersion == 0 {
		t.Fatal("vacuous: corpus contained no two versions of a single user-key")
	}
	t.Logf("%d same-prefix/different-version pairs covered", sameKeyDifferentVersion)
}
