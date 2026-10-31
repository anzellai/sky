package rt

import "testing"

// Dict_getProbeT / Dict_memberProbeT agree with the erased Dict_get /
// Dict_member on typed maps, for keys the Sky kernels encoded AND for the bare
// keys an FFI / Std.Db / JSON map holds.
func TestDictProbeTwinsMatchTheErasedKernels(t *testing.T) {
	typed := AsMapT[int](Dict_insert(10, 7, Dict_insert(2, 5, Dict_empty())))
	fromDb := AsMapT[int](map[string]int{"10": 70, "x": 1})
	for _, d := range []SkyDict[int]{typed, fromDb} {
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

// The twin does no copy of the Dict: a lookup allocates nothing for a
// String key.
func TestDictGetProbeTDoesNotCopyTheMap(t *testing.T) {
	d := SkyDict[string]{}
	for i := 0; i < 1000; i++ {
		d = d.Insert(string(rune('a'+i%26))+itoa(i), "v")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = Dict_getProbeT("a0", d)
	})
	if allocs > 1 {
		t.Fatalf("Dict_getProbeT allocated %.0f times per lookup", allocs)
	}
}

func TestDictSizeTwinsMatchTheErasedKernels(t *testing.T) {
	for _, d := range []SkyDict[int]{{}, AsMapT[int](map[string]int{"a": 1, "b": 2})} {
		if Dict_sizeT(d) != Dict_size(d).(int) || Dict_isEmptyT(d) != Dict_isEmpty(d).(bool) {
			t.Fatalf("size / isEmpty twin differs on %v", d)
		}
	}
}

// The typed-key twins (D2) agree with the erased kernels on Dicts the kernels
// built and on foreign maps holding bare keys, and an Int-keyed insert stores
// its own copy of the key (the probe key aliases a stack buffer).
func TestDictTypedKeyTwinsMatchTheErasedKernels(t *testing.T) {
	typed := AsMapT[int](Dict_insert(10, 7, Dict_insert(2, 5, Dict_empty())))
	fromDb := AsMapT[int](map[string]int{"10": 70, "x": 1})
	for _, d := range []SkyDict[int]{typed, fromDb} {
		for _, k := range []int{10, 2, 3} {
			got, want := Dict_getIntT(k, d), Dict_get(k, d).(SkyMaybe[any])
			if got.Tag != want.Tag || (got.Tag == 0 && got.JustValue != want.JustValue.(int)) {
				t.Fatalf("getIntT %d: %+v vs %+v", k, got, want)
			}
			if Dict_memberIntT(k, d) != Dict_member(k, d).(bool) {
				t.Fatalf("memberIntT %d", k)
			}
			if !deepEq(Dict_removeIntT(k, d), Dict_remove(k, d)) {
				t.Fatalf("removeIntT %d", k)
			}
			if !deepEq(Dict_insertIntT(k, 99, d), Dict_insert(k, 99, d)) {
				t.Fatalf("insertIntT %d", k)
			}
		}
		for _, k := range []string{"x", "10", "\x01weird"} {
			if !deepEq(Dict_insertStrT(k, 1, d), Dict_insert(k, 1, d)) ||
				Dict_memberStrT(k, d) != Dict_member(k, d).(bool) {
				t.Fatalf("Str twin %q", k)
			}
		}
	}
	if Dict_insertIntT(10, 1, fromDb).Len() != 2 {
		t.Fatalf("insertIntT beside a bare \"10\" added a duplicate")
	}
	var d SkyDict[int]
	for i := 0; i < 2000; i++ {
		d = Dict_insertIntT(i*37, i, d)
	}
	for i := 0; i < 2000; i++ {
		if m := Dict_getIntT(i*37, d); m.Tag != 0 || m.JustValue != i {
			t.Fatalf("key %d after 2000 twin inserts: %+v", i*37, m)
		}
	}
	keys := Dict_keysIntKey(d).([]any)
	for i, k := range keys {
		if k != any(i*37) {
			t.Fatalf("a stored key was overwritten: keys[%d] = %v", i, k)
		}
	}
	if n := testing.AllocsPerRun(100, func() { _ = Dict_getIntT(370, d) }); n != 0 {
		t.Fatalf("an Int-keyed lookup allocated %.0f times", n)
	}
}
