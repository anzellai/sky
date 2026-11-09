package rt

import (
	"runtime"
	"strings"
	"testing"
)

// A Model field the compiler cannot prove closed (here a Json.Value) is an
// "unknown" field of the mixed persistence engine. Its bytes used to be built
// in full even when nobody could store them: an excluded field, the previous
// Model's sign-out check, and a snapshot already past the 1.5 MB cap. They are
// now walked and measured only. These tests prove the outcome is unchanged
// (the differential against the full reference encoder) and that the large
// buffer is no longer built.

type spaMixedCapModel struct {
	Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
	Doc     any              `sky:"doc,any"`
	Title   string           `sky:"title,string"`
}

func spaMixedCapDoc(n int) any {
	return JsonValue{raw: jsonOrderedObject{
		keys: []string{"text", "tags"},
		vals: []any{strings.Repeat("d", n), []any{"a<b", int64(3), 1.5}},
	}}
}

func spaMixedCapAllocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestSpaMixedUnknownFieldOutcomeUnchangedAroundTheCap(t *testing.T) {
	for _, n := range []int{10, 1_000, 1_499_900, 1_500_100, 3_000_000} {
		for _, session := range []SkyMaybe[string]{Nothing[string](), Just("user-1")} {
			model := func(_ *[]string) any {
				return spaMixedCapModel{Session: session, Doc: spaMixedCapDoc(n), Title: "t"}
			}
			for _, exclude := range [][]string{nil, {"doc"}, {"title"}} {
				for _, previous := range []bool{false, true} {
					spaMixedDifferential(t, model, spaMixedEncodeOptions{
						Exclude: exclude, Protected: []string{"session"},
						CapBytes: spaPersistMaxBytes, PreviousOnly: previous,
					})
				}
			}
		}
	}
}

func TestSpaMixedDoesNotBuildBytesThatCannotBeStored(t *testing.T) {
	const big = 8 << 20
	e := spaMixedTestEncoder(&[]string{})
	m := spaMixedCapModel{Session: Just("user-1"), Doc: spaMixedCapDoc(big), Title: "t"}
	cases := map[string]spaMixedEncodeOptions{
		"over the cap":   {Protected: []string{"session"}, CapBytes: spaPersistMaxBytes},
		"excluded":       {Exclude: []string{"doc"}, Protected: []string{"session"}, CapBytes: spaPersistMaxBytes},
		"previous model": {Protected: []string{"session"}, CapBytes: spaPersistMaxBytes, PreviousOnly: true},
	}
	for name, options := range cases {
		var got spaMixedEncoded
		allocated := spaMixedCapAllocated(func() { got = spaEncodeMixed(e, nil, m, options) })
		if !got.Valid || got.Payload != "" && name == "over the cap" {
			t.Fatalf("%s: invalid result", name)
		}
		if name == "over the cap" && got.EncodedBytes <= spaPersistMaxBytes {
			t.Fatalf("%s: size %d is not over the cap", name, got.EncodedBytes)
		}
		// Building the 8 MB field (as before) allocates at least 8 MB.
		if allocated > big/4 {
			t.Fatalf("%s: allocated %d bytes for an %d-byte field nobody stores", name, allocated, big)
		}
	}
}
