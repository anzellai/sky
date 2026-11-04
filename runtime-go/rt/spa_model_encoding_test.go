package rt

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSpaModelEncodingReuseHasBoundedWitnessWork(t *testing.T) {
	for _, n := range []int{1000, 10000, 100000} {
		m := spaProjectionModel{Session: Nothing[spaProjectionSession](), Data: make([]string, n)}
		calls := 0
		e := spaModelEncoding{generated: true, full: func(v any) string { calls++; return spaProjectionEncode(v) }}
		first, hit, _ := e.encodeNext(m)
		second, reused, nodes := e.encodeNext(m)
		// Empty list entries fit the existing persistence cap even at100K.
		if first == "" || first != second || hit || !reused || calls != 1 || nodes != 4 {
			t.Fatalf("size=%d: firsthit=%v reuse=%v calls=%d compared=%d", n, hit, reused, calls, nodes)
		}
	}
}

func TestSpaModelEncodingListWitnessIncludesAllocationLengthAndNil(t *testing.T) {
	base := []string{"a", "b", "c"}
	for _, next := range [][]string{append([]string(nil), base...), base[:2], base[1:], nil, {}} {
		m := spaProjectionModel{Data: base}
		calls := 0
		e := spaModelEncoding{generated: true, full: func(v any) string { calls++; return spaProjectionEncode(v) }}
		e.encodeNext(m)
		m.Data = next
		_, hit, _ := e.encodeNext(m)
		if hit || calls != 2 {
			t.Fatal("different visible list value/allocation reused an encoding")
		}
	}
	var nilList []string
	if same, _ := spaModelSame(nilList, []string{}); same {
		t.Fatal("nil and allocated empty representation were conflated")
	}
}

func TestSpaModelEncodingSignedZeroAndMaybeChanges(t *testing.T) {
	type model struct {
		Number float64          `sky:"number,float64"`
		Value  SkyMaybe[string] `sky:"value,rt.SkyMaybe[string]"`
	}
	m := model{Value: Nothing[string]()}
	calls := 0
	e := spaModelEncoding{generated: true, full: func(v any) string { calls++; return spaProjectionEncode(v) }}
	zero, _, _ := e.encodeNext(m)
	m.Number = math.Copysign(0, -1)
	negative, hit, _ := e.encodeNext(m)
	if hit || zero == negative || calls != 2 {
		t.Fatal("signed zero was conflated")
	}
	m.Value = Just("synthetic")
	_, hit, _ = e.encodeNext(m)
	if hit || calls != 3 {
		t.Fatal("Maybe payload change was missed")
	}
}

func TestSpaModelEncodingUnknownAndCustomKeepFullCalls(t *testing.T) {
	for _, generated := range []bool{false, true} {
		calls := 0
		e := spaModelEncoding{generated: generated, full: func(v any) string { calls++; return "{}" }}
		m := struct {
			Open any `sky:"open,any"`
		}{Open: 1}
		e.encodeNext(m)
		_, hit, _ := e.encodeNext(m)
		if hit || calls != 2 || e.model != nil {
			t.Fatal("unknown type acquired encoding reuse")
		}
	}
	calls := 0
	e := spaModelEncoding{full: func(v any) string { calls++; return "{}" }}
	m := spaProjectionModel{}
	e.encodeNext(m)
	_, hit, _ := e.encodeNext(m)
	if hit || calls != 2 {
		t.Fatal("custom encoder lost an observable call")
	}
}

func TestSpaModelEncodingFailureCapAndResetReleaseOldEntry(t *testing.T) {
	m := spaProjectionModel{}
	result := "{}"
	calls := 0
	e := spaModelEncoding{generated: true, full: func(v any) string { calls++; return result }}
	e.encodeNext(m)
	m.Text = "changed"
	result = ""
	e.encodeNext(m)
	_, hit, _ := e.encodeNext(m)
	if hit || calls != 3 || e.model != nil || e.json != "" {
		t.Fatal("failed encode was reused or retained the old entry")
	}
	result = strings.Repeat("x", spaPersistMaxBytes+1)
	e.encodeNext(m)
	if e.model != nil || e.json != "" {
		t.Fatal("cache exceeded its encoded-byte bound")
	}
	result = "{}"
	e.encodeNext(m)
	e.reset()
	_, hit, _ = e.encodeNext(m)
	if hit {
		t.Fatal("reset retained an encoding certificate")
	}
}

func TestSpaModelWitnessBoundsArraysAndExactTypes(t *testing.T) {
	a := [spaModelWitnessBudget + 1]int{}
	if same, nodes := spaModelSame(a, a); same || nodes != spaModelWitnessBudget {
		t.Fatal("large value array bypassed the witness budget")
	}
	if same, _ := spaModelSame(int(1), int64(1)); same {
		t.Fatal("different exact Go types were conflated")
	}
	long := strings.Repeat("x", 10000)
	copyOfLong := strings.Clone(long)
	if same, _ := spaModelSame(long, copyOfLong); same {
		t.Fatal("long independently allocated strings used an unbounded equality walk")
	}
	if same, _ := spaModelSame(long, long); !same {
		t.Fatal("forwarded immutable string missed identity")
	}
	if !reflect.DeepEqual(a, a) {
		t.Fatal("test array must be semantically equal despite conservative witness miss")
	}
}

func TestSpaModelEncodingReuseStillRetriesWritesAndRepublishes(t *testing.T) {
	m := spaProjectionModel{Data: []string{"synthetic"}}
	calls, writes := 0, 0
	encode := func(v any) string { calls++; return spaProjectionEncode(v) }
	e := spaModelEncoding{generated: true, full: encode}
	e.encodeNext(m)
	stored := "another tab's snapshot"
	for step := 0; step < 3; step++ {
		var metrics spaPersistMetrics
		cleared := spaPersistStepWithEncoding(m, m, encode, func(blob string) {
			writes++
			if step == 0 {
				return // denied storage; next message must retry
			}
			stored = blob
		}, []string{"session"}, true, &e, func(m spaPersistMetrics) { metrics = m })
		if cleared || metrics.EncodingReuses != 1 || metrics.Encodes != 0 {
			t.Fatal("unchanged model lost reuse or changed its session decision")
		}
		if step > 0 && stored != spaProjectionEncode(m) {
			t.Fatal("unchanged step failed to reassert this tab's snapshot")
		}
		stored = "" // removal/eviction must not suppress the next write
	}
	if calls != 1 || writes != 3 {
		t.Fatal("cache skipped a write or repeated full encoding")
	}
}

func TestSpaModelEncodingKeepsPreviousFullFailureAndCmdBoundaries(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Reading float64          `sky:"reading,float64"`
	}
	old := model{Session: Just("synthetic"), Reading: math.NaN()}
	next := model{Session: Nothing[string]()}
	calls := 0
	encode := func(v any) string { calls++; return spaProjectionEncode(v) }
	e := spaModelEncoding{generated: true, full: encode}
	e.encodeNext(next) // prime only the next-model cache
	var metrics spaPersistMetrics
	cleared := spaPersistStepWithEncoding(old, next, encode, func(string) {}, []string{"session"}, true, &e, func(m spaPersistMetrics) { metrics = m })
	if cleared || calls != 2 || metrics.Encodes != 1 || metrics.EncodingReuses != 1 {
		t.Fatal("cache bypassed the previous full encode/failure on a possible clear")
	}
	views, commands := 0, 0
	for i := 0; i < 2; i++ {
		kept := spaTransition(nil, next,
			func(_, current any) SkyTuple2 { return SkyTuple2{V0: current, V1: "command"} },
			func(any) { views++ }, func(any) { commands++ }, nil)
		spaPersistStepWithEncoding(next, kept, encode, func(string) {}, []string{"session"}, true, &e, nil)
	}
	if views != 2 || commands != 2 {
		t.Fatal("encoding reuse suppressed rendering or commands")
	}
}

func TestSpaModelEncodingEnumRegistrationInvalidates(t *testing.T) {
	type model struct {
		Kind int `sky:"kind,spaCacheEpochKind"`
	}
	RegisterEnum("spaCacheEpochKind", []string{"Before"})
	calls := 0
	e := spaModelEncoding{generated: true, full: func(v any) string { calls++; return spaProjectionEncode(v) }}
	first, _, _ := e.encodeNext(model{})
	RegisterEnum("spaCacheEpochKind", []string{"After"})
	second, hit, _ := e.encodeNext(model{})
	if hit || calls != 2 || first == second || !strings.Contains(second, "After") {
		t.Fatal("enum re-registration reused a stale encoding")
	}
	RegisterEnum("spaCacheEpochKind", []string{"After"})
	_, hit, _ = e.encodeNext(model{})
	if hit || calls != 3 {
		t.Fatal("even identical registration must invalidate conservatively")
	}
}

func TestSpaModelEncodingMetadataChangeDuringFullIsNotCached(t *testing.T) {
	calls := 0
	e := spaModelEncoding{generated: true, full: func(v any) string {
		calls++
		out := spaProjectionEncode(v)
		RegisterEnum("spaCacheDuringFull", []string{"Synthetic"})
		return out
	}}
	defer setEnumRegistration("spaCacheDuringFull", nil, false)
	m := spaProjectionModel{}
	e.encodeNext(m)
	_, hit, _ := e.encodeNext(m)
	if hit || calls != 2 || e.model != nil || e.enums != nil {
		t.Fatal("metadata changed during encoding but a certificate was retained")
	}
}

func TestSpaModelEncodingHitReleasesObsoleteModelOwner(t *testing.T) {
	type model struct {
		Text     string             `sky:"text,string"`
		Inactive SkyMaybe[[]string] `sky:"inactive,rt.SkyMaybe[[]string]"`
	}
	large := strings.Repeat("x", 8*1024*1024)
	old := model{Text: large[:1], Inactive: SkyMaybe[[]string]{Tag: 1, JustValue: make([]string, 100000)}}
	e := spaModelEncoding{generated: true, full: spaProjectionEncode}
	e.encodeNext(old)
	next := model{Text: strings.Clone(old.Text), Inactive: Nothing[[]string]()}
	_, hit, _ := e.encodeNext(next)
	retained := reflect.ValueOf(e.model)
	if !hit || retained.FieldByName("Text").Pointer() != reflect.ValueOf(next.Text).Pointer() || retained.FieldByName("Inactive").FieldByName("JustValue").Len() != 0 {
		t.Fatal("encoding hit retained an obsolete model/backing allocation")
	}
}
