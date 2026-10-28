package rt

import "testing"

// Dict_getProbeT / Dict_memberProbeT agree with the erased Dict_get /
// Dict_member on typed maps, for keys the Sky kernels encoded AND for the bare
// keys an FFI / Std.Db / JSON map holds.
func TestDictProbeTwinsMatchTheErasedKernels(t *testing.T) {
	built := Dict_insert(10, 7, Dict_insert(2, 5, Dict_empty())).(map[string]any)
	typed := map[string]int{}
	for k, v := range built {
		typed[k] = v.(int)
	}
	fromDb := map[string]int{"10": 70, "x": 1}
	for _, d := range []map[string]int{typed, fromDb} {
		for _, key := range []any{10, 2, 3, "x", "10"} {
			got := Dict_getProbeT(key, d)
			want := Dict_get(key, d).(SkyMaybe[any])
			if got.Tag != want.Tag || (got.Tag == 0 && got.JustValue != want.JustValue.(int)) {
				t.Fatalf("get %v in %v: twin %+v erased %+v", key, d, got, want)
			}
			if Dict_memberProbeT(key, d) != Dict_member(key, d).(bool) {
				t.Fatalf("member %v in %v", key, d)
			}
		}
	}
	if m := Dict_getProbeT(10, fromDb); m.Tag != 0 || m.JustValue != 70 {
		t.Fatalf("an Int key must find the bare \"10\" a Std.Db map holds: %+v", m)
	}
}

// The twin does no reflect copy of the map: a lookup allocates nothing for a
// String key.
func TestDictGetProbeTDoesNotCopyTheMap(t *testing.T) {
	d := map[string]string{}
	for i := 0; i < 1000; i++ {
		d[string(rune('a'+i%26))+itoa(i)] = "v"
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = Dict_getProbeT("a0", d)
	})
	if allocs > 1 {
		t.Fatalf("Dict_getProbeT allocated %.0f times per lookup", allocs)
	}
}

func TestDictSizeTwinsMatchTheErasedKernels(t *testing.T) {
	for _, d := range []map[string]int{{}, {"a": 1, "b": 2}} {
		if Dict_sizeT(d) != Dict_size(d).(int) || Dict_isEmptyT(d) != Dict_isEmpty(d).(bool) {
			t.Fatalf("size / isEmpty twin differs on %v", d)
		}
	}
}
