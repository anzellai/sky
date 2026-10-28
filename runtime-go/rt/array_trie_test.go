package rt

import (
	"math/rand"
	"slices"
	"testing"
)

func arrInts(a any) []int {
	xs := AsList(Array_toList(a))
	out := make([]int, len(xs))
	for i, x := range xs {
		out[i] = x.(int)
	}
	return out
}

// A model-based property test: random push / set / get / slice / append on
// an Array against a plain Go slice, with every old version kept and checked
// again at the end (persistence).
func TestArrayAgainstAListModel(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	arr := Array_empty()
	var model []int
	type snap struct {
		a any
		m []int
	}
	var snaps []snap
	for step := 0; step < 20000; step++ {
		switch op := r.Intn(10); {
		case op < 6:
			v := r.Intn(1000)
			arr = Array_push(v, arr)
			model = append(slices.Clone(model), v)
		case op < 8 && len(model) > 0:
			i, v := r.Intn(len(model)+4)-2, r.Intn(1000)
			arr = Array_set(i, v, arr)
			if i >= 0 && i < len(model) {
				model = slices.Clone(model)
				model[i] = v
			}
		default:
			i := r.Intn(len(model)+4) - 2
			got := Array_get(i, arr).(SkyMaybe[any])
			if i >= 0 && i < len(model) {
				if got.Tag != 0 || got.JustValue.(int) != model[i] {
					t.Fatalf("step %d: get %d = %+v, want %d", step, i, got, model[i])
				}
			} else if got.Tag == 0 {
				t.Fatalf("step %d: get %d out of range must be Nothing", step, i)
			}
		}
		if step%997 == 0 {
			snaps = append(snaps, snap{arr, slices.Clone(model)})
		}
	}
	if Array_length(arr).(int) != len(model) || !slices.Equal(arrInts(arr), model) {
		t.Fatal("final array differs from the model")
	}
	for i, s := range snaps {
		if !slices.Equal(arrInts(s.a), s.m) {
			t.Fatalf("snapshot %d changed after later updates", i)
		}
	}
}

func TestArrayPushAcrossTrieBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 31, 32, 33, 64, 1023, 1024, 1025, 1056, 32768, 32800, 40000} {
		a := Array_empty()
		for i := 0; i < n; i++ {
			a = Array_push(i, a)
		}
		if Array_length(a).(int) != n {
			t.Fatalf("n=%d length %v", n, Array_length(a))
		}
		for _, i := range []int{0, n / 2, n - 1} {
			if n == 0 {
				break
			}
			if g := Array_get(i, a).(SkyMaybe[any]); g.Tag != 0 || g.JustValue.(int) != i {
				t.Fatalf("n=%d get %d = %+v", n, i, g)
			}
		}
		if n > 0 {
			b := Array_set(n-1, -1, Array_set(0, -1, a))
			if Array_get(0, a).(SkyMaybe[any]).JustValue.(int) != 0 || Array_get(0, b).(SkyMaybe[any]).JustValue.(int) != -1 {
				t.Fatalf("n=%d set changed the original", n)
			}
		}
	}
}

func TestArrayHigherOrderAndSlices(t *testing.T) {
	a := Array_fromList([]any{1, 2, 3, 4, 5})
	inc := func(x any) any { return x.(int) * 10 }
	if got := arrInts(Array_map(inc, a)); !slices.Equal(got, []int{10, 20, 30, 40, 50}) {
		t.Fatalf("map: %v", got)
	}
	sum := func(x any) any { return func(acc any) any { return acc.(int)*10 + x.(int) } }
	if got := Array_foldl(sum, 0, a); got != 12345 {
		t.Fatalf("foldl: %v", got)
	}
	if got := Array_foldr(sum, 0, a); got != 54321 {
		t.Fatalf("foldr: %v", got)
	}
	even := func(x any) any { return x.(int)%2 == 0 }
	if got := arrInts(Array_filter(even, a)); !slices.Equal(got, []int{2, 4}) {
		t.Fatalf("filter: %v", got)
	}
	if got := arrInts(Array_slice(1, -1, a)); !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("slice: %v", got)
	}
	if got := arrInts(Array_slice(-2, 10, a)); !slices.Equal(got, []int{4, 5}) {
		t.Fatalf("slice clamp: %v", got)
	}
	if got := arrInts(Array_slice(3, 1, a)); len(got) != 0 {
		t.Fatalf("slice empty: %v", got)
	}
	if got := arrInts(Array_append(a, Array_repeat(2, 9))); !slices.Equal(got, []int{1, 2, 3, 4, 5, 9, 9}) {
		t.Fatalf("append: %v", got)
	}
	idx := func(i any) any { return i.(int) * i.(int) }
	if got := arrInts(Array_initialize(4, idx)); !slices.Equal(got, []int{0, 1, 4, 9}) {
		t.Fatalf("initialize: %v", got)
	}
	pairs := AsList(Array_toIndexedList(Array_fromList([]any{"a", "b"})))
	if len(pairs) != 2 || pairs[1].(SkyTuple2).V0 != 1 || pairs[1].(SkyTuple2).V1 != "b" {
		t.Fatalf("toIndexedList: %v", pairs)
	}
	if !Array_isEmpty(Array_empty()).(bool) || Array_isEmpty(a).(bool) {
		t.Fatal("isEmpty")
	}
	if !deepEq(Array_fromList([]any{1, 2}), Array_push(2, Array_push(1, Array_empty()))) {
		t.Fatal("== on two arrays with the same elements")
	}
	if deepEq(Array_fromList([]any{1, 2}), Array_fromList([]any{2, 1})) {
		t.Fatal("== on two different arrays")
	}
}

// Edit distance with an Array as the dynamic-programming row: the use case
// beyond the request.
func TestArrayEditDistance(t *testing.T) {
	dist := func(a, b string) int {
		ra, rb := []rune(a), []rune(b)
		prev := Array_initialize(len(rb)+1, func(i any) any { return i })
		for i := 1; i <= len(ra); i++ {
			cur := Array_push(i, Array_empty())
			for j := 1; j <= len(rb); j++ {
				cost := 1
				if ra[i-1] == rb[j-1] {
					cost = 0
				}
				up := Array_get(j, prev).(SkyMaybe[any]).JustValue.(int) + 1
				left := Array_get(j-1, cur).(SkyMaybe[any]).JustValue.(int) + 1
				diag := Array_get(j-1, prev).(SkyMaybe[any]).JustValue.(int) + cost
				cur = Array_push(min(min(up, left), diag), cur)
			}
			prev = cur
		}
		return Array_get(len(rb), prev).(SkyMaybe[any]).JustValue.(int)
	}
	if d := dist("kitten", "sitting"); d != 3 {
		t.Fatalf("edit distance = %d, want 3", d)
	}
	if d := dist("", "abc"); d != 3 {
		t.Fatalf("edit distance from empty = %d", d)
	}
}

func TestArrayGobRoundTrip(t *testing.T) {
	a := Array_fromList([]any{1, "x", 2.5}).(SkyArray)
	b, err := a.GobEncode()
	if err != nil {
		t.Fatal(err)
	}
	var back SkyArray
	if err := back.GobDecode(b); err != nil {
		t.Fatal(err)
	}
	if !deepEq(a, back) {
		t.Fatal("gob round trip changed the array")
	}
	if got := SkyShow(a); got != `Array.fromList [1, "x", 2.5]` {
		t.Fatalf("show: %s", got)
	}
}

func BenchmarkArrayGet1e6(b *testing.B) {
	a := Array_empty()
	for i := 0; i < 1000000; i++ {
		a = Array_push(i, a)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Array_get(i%1000000, a)
	}
}

// An Array inside a Sky.Live model survives the session store's gob wire.
func TestArrayInALiveModelSurvivesTheSessionStore(t *testing.T) {
	type model struct {
		Rows any
		Name string
	}
	s := &liveSession{model: model{Rows: Array_fromList([]any{1, 2, 3}), Name: "m"}}
	blob, err := encodeSession(s)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m := back.model.(model)
	if !deepEq(m.Rows, Array_fromList([]any{1, 2, 3})) || m.Name != "m" {
		t.Fatalf("model after the round trip: %#v", m)
	}
}
