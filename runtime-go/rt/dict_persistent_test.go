package rt

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// dictContent is a Dict's entries as a plain map, for comparing against a
// model.
func dictContent[V any](d SkyDict[V]) map[string]V {
	out := map[string]V{}
	d.Range(func(k string, v V) bool {
		out[k] = v
		return true
	})
	return out
}

func checkDictAgainst(t *testing.T, label string, d SkyDict[int], ref map[string]int) {
	t.Helper()
	if d.Len() != len(ref) {
		t.Fatalf("%s: Len %d, want %d", label, d.Len(), len(ref))
	}
	for k, v := range ref {
		if got, ok := d.Lookup(k); !ok || got != v {
			t.Fatalf("%s: Lookup(%q) = %d, %v; want %d", label, k, got, ok, v)
		}
	}
	if got := dictContent(d); !reflect.DeepEqual(got, ref) {
		t.Fatalf("%s: Range yields %d entries that differ from the model", label, len(got))
	}
}

// A 40,000-step model test against a Go map: random inserts, updates and
// removes over a key space that makes the Dict grow past the flat limit, shrink
// back and grow again. Every 2,000 steps the version is kept, and at the end
// EVERY kept version must still hold exactly what it held when it was taken:
// no update may write into a version that came before it.
func TestSkyDictModelAndPersistence(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	var d SkyDict[int]
	ref := map[string]int{}
	type snap struct {
		d   SkyDict[int]
		ref map[string]int
	}
	var snaps []snap
	for step := 0; step < 40000; step++ {
		space := 3000
		if (step/5000)%2 == 1 {
			space = 12 // stay around the flat limit for a while
		}
		k := encodeDictKey(r.Intn(space))
		switch op := r.Intn(10); {
		case op < 6:
			d = d.Insert(k, step)
			ref[k] = step
		case op < 9:
			d = d.Remove(k)
			delete(ref, k)
		default:
			if _, ok := d.Lookup(k); ok != hasKey(ref, k) {
				t.Fatalf("step %d: Lookup(%q) presence differs", step, k)
			}
		}
		if step%2000 == 0 {
			cp := map[string]int{}
			for k, v := range ref {
				cp[k] = v
			}
			snaps = append(snaps, snap{d, cp})
		}
	}
	checkDictAgainst(t, "final", d, ref)
	for i, s := range snaps {
		checkDictAgainst(t, fmt.Sprintf("snapshot %d", i), s.d, s.ref)
	}
	// Remove everything: the empty Dict is the zero value again.
	for k := range ref {
		d = d.Remove(k)
	}
	if d.Len() != 0 || d.trie || d.kids != nil {
		t.Fatalf("emptied Dict is not the empty value: %+v", d)
	}
}

func hasKey(m map[string]int, k string) bool {
	_, ok := m[k]
	return ok
}

// Full 64-bit hash collisions (forced: the hash is a parameter of the trie
// walk) land in a collision bucket, and insert / update / remove / lookup all
// work through it, including collapsing the bucket back to a leaf.
func TestSkyDictForcedCollisions(t *testing.T) {
	const h = uint64(0xDEADBEEFCAFEF00D)
	// Two keys with one hash: a chain of single-child branches down to a
	// bucket (an insert that meets a leaf re-hashes the leaf's real key, so
	// the first pair is built directly).
	r := dictMerge[int](0, "a", 0, h, "b", 1, h)
	d := SkyDict[int]{kids: r.kids, bm: r.bm, n: 2, trie: true}
	keys := []string{"a", "b", "c", "d"}
	for i, k := range keys[2:] {
		d = d.insertH(k, i+2, h)
	}
	// A key with a different hash beside the bucket.
	d = d.insertH("other", 99, 0x0123456789ABCDEF)
	if d.Len() != 5 {
		t.Fatalf("Len %d, want 5", d.Len())
	}
	for i, k := range keys {
		if v, ok := d.lookupH(k, h); !ok || v != i {
			t.Fatalf("lookupH(%q) = %d, %v", k, v, ok)
		}
	}
	d2 := d.insertH("b", 42, h)
	if v, _ := d2.lookupH("b", h); v != 42 || d2.Len() != 5 {
		t.Fatalf("update in a bucket: %d, Len %d", v, d2.Len())
	}
	if v, _ := d.lookupH("b", h); v != 1 {
		t.Fatalf("the update wrote into the old version: %d", v)
	}
	for _, k := range []string{"a", "c", "d"} {
		d2 = d2.removeH(k, h)
	}
	if d2.Len() != 2 {
		t.Fatalf("after removes: Len %d, want 2", d2.Len())
	}
	if v, ok := d2.lookupH("b", h); !ok || v != 42 {
		t.Fatalf("the last bucket entry was lost: %d, %v", v, ok)
	}
	if _, ok := d2.lookupH("a", h); ok {
		t.Fatalf("a removed key is still found")
	}
	if got := dictContent(d2); len(got) != 2 || got["b"] != 42 || got["other"] != 99 {
		t.Fatalf("Range after collapse: %v", got)
	}
	if d3 := d2.removeH("zzz", h); d3.Len() != 2 {
		t.Fatalf("removing an absent colliding key changed the Dict")
	}
}

// dictBuild (the one-pass constructor fromList / narrowing / decoding use)
// builds the same content as inserting one key at a time.
func TestDictBuildMatchesInserts(t *testing.T) {
	for _, n := range []int{0, 1, dictFlatMax, dictFlatMax + 1, 1000, 20000} {
		keys := make([]string, n)
		vals := make([]int, n)
		var byInsert SkyDict[int]
		for i := 0; i < n; i++ {
			keys[i] = encodeDictKey(i * 7)
			vals[i] = i
			byInsert = byInsert.Insert(keys[i], i)
		}
		built := dictBuild(keys, vals, false)
		if !reflect.DeepEqual(dictContent(built), dictContent(byInsert)) || built.Len() != n {
			t.Fatalf("n=%d: dictBuild differs from inserts", n)
		}
		if !dictsEqual(built, byInsert) {
			t.Fatalf("n=%d: dictsEqual says the two differ", n)
		}
	}
	// Repeated keys: the LAST value wins (Dict.fromList is a foldl of insert).
	got := Dict_fromList([]any{SkyTuple2{V0: 1, V1: "a"}, SkyTuple2{V0: 2, V1: "b"}, SkyTuple2{V0: 1, V1: "c"}})
	if want := []any{SkyTuple2{V0: 1, V1: "c"}, SkyTuple2{V0: 2, V1: "b"}}; !reflect.DeepEqual(Dict_toList(got), want) {
		t.Fatalf("fromList with a repeated key = %v, want %v", Dict_toList(got), want)
	}
}

// The erased kernels keep a Dict's value type, so the typed slot that receives
// the result takes it in O(1): AsMapT of a SkyDict[V] allocates nothing.
func TestDictKernelsKeepTheValueType(t *testing.T) {
	d := AsMapT[int](Dict_empty())
	for i := 0; i < 100; i++ {
		d = AsMapT[int](Dict_insert(i, any(i*i), any(d)))
	}
	out := Dict_insert(5, any(-1), any(d))
	if _, ok := out.(SkyDict[int]); !ok {
		t.Fatalf("Dict_insert on a SkyDict[int] returned %T", out)
	}
	if n := testing.AllocsPerRun(100, func() { _ = AsMapT[int](out) }); n != 0 {
		t.Fatalf("AsMapT of a SkyDict[int] allocated %.0f times", n)
	}
	if v := Dict_getProbeT(5, AsMapT[int](out)); v.Tag != 0 || v.JustValue != -1 {
		t.Fatalf("get after update = %+v", v)
	}
	if v := Dict_getProbeT(5, d); v.JustValue != 25 {
		t.Fatalf("the update changed the version it came from: %+v", v)
	}
	// A value that cannot become V widens the Dict instead of panicking.
	w := Dict_insert(1000, any("text"), any(d))
	if _, ok := w.(SkyDict[any]); !ok {
		t.Fatalf("a mismatched value: got %T, want the widened SkyDict[any]", w)
	}
	if Dict_size(w).(int) != 101 {
		t.Fatalf("widened Dict lost entries: %d", Dict_size(w))
	}
}

// An insert into a large Dict copies one path, not the Dict: a regression to
// the map copy (O(n) bytes per insert, O(n²) for a fold) fails here long
// before it fails the perf gate's wall-clock budget.
func TestDictInsertDoesNotCopyTheDict(t *testing.T) {
	d := Dict_empty()
	for i := 0; i < 20000; i++ {
		d = Dict_insert(i, any(i), d)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	const n = 200
	for i := 0; i < n; i++ {
		d = Dict_insert(20000+i, any(i), d)
	}
	runtime.ReadMemStats(&after)
	perInsert := float64(after.TotalAlloc-before.TotalAlloc) / n
	// The map copy allocated ~1 MB per insert at this size; a path copy is a
	// few kB.
	if perInsert > 16<<10 {
		t.Fatalf("one insert into a 20,000-entry Dict allocated %.0f bytes: the Dict is being copied", perInsert)
	}
}

// Sky `==` on two Dicts compares their content: the same entries built in a
// different order (one flat, one grown into a trie and shrunk back) are equal.
func TestDictEqualityIsByContent(t *testing.T) {
	a := Dict_empty()
	for i := 0; i < 10; i++ {
		a = Dict_insert(i, any(fmt.Sprint(i)), a)
	}
	b := Dict_empty()
	for i := 39; i >= 0; i-- {
		b = Dict_insert(i, any(fmt.Sprint(i)), b)
	}
	for i := 10; i < 40; i++ {
		b = Dict_remove(i, b)
	}
	if !deepEq(a, b) {
		t.Fatalf("equal content compared unequal")
	}
	c := Dict_insert(3, any("three"), b)
	if deepEq(a, c) {
		t.Fatalf("different values compared equal")
	}
	// Inside a record, through the struct walk.
	type rec struct{ D SkyDict[any] }
	if !deepEq(rec{a.(SkyDict[any])}, rec{b.(SkyDict[any])}) {
		t.Fatalf("records holding equal Dicts compared unequal")
	}
}

type dictSessionModel struct {
	Name   string
	Counts SkyDict[int]
	Any    any
	Seen   any
}

// A Dict (and a Set) in a Sky.Live model survives the gob session store, in a
// typed field and held as `any`.
func TestDictAndSetSurviveTheSessionStore(t *testing.T) {
	gob.Register(dictSessionModel{})
	counts := AsMapT[int](Dict_fromList([]any{SkyTuple2{V0: 1, V1: 3}, SkyTuple2{V0: 20, V1: 7}}))
	anyDict := Dict_fromList([]any{SkyTuple2{V0: "k", V1: "v"}})
	set := Set_fromList([]any{"b", "a"})
	s := &liveSession{model: dictSessionModel{Name: "ada", Counts: counts, Any: anyDict, Seen: set}}
	blob, err := encodeSession(s)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m := back.model.(dictSessionModel)
	if !deepEq(m.Counts, counts) || !deepEq(m.Any, anyDict) {
		t.Fatalf("Dicts changed across the store: %v / %v", m.Counts, m.Any)
	}
	if got := Dict_toList(m.Counts); !reflect.DeepEqual(got, Dict_toList(counts)) {
		t.Fatalf("toList after the store = %v", got)
	}
	if got := Set_toList(m.Seen); !reflect.DeepEqual(got, []any{"a", "b"}) {
		t.Fatalf("Set after the store = %v", got)
	}
}

// The upgrade path. testdata/v028-session-dict.gob was written by the v0.28
// runtime (encodeSession of a model whose `Counts` field was the Go map a
// Dict used to be). This binary cannot read it: decodeSession returns an
// error that says why and what happens (the session is dropped and the
// visitor starts a new one), never a panic and never a half-decoded model.
func TestV028SessionWithADictIsACleanReset(t *testing.T) {
	gob.Register(v028SessionModel{})
	blob, err := os.ReadFile("testdata/v028-session-dict.gob")
	if err != nil {
		t.Fatal(err)
	}
	var sess *liveSession
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decoding a v0.28 session panicked: %v", r)
			}
		}()
		sess, err = decodeSession(blob)
	}()
	if sess != nil {
		t.Fatalf("a v0.28 session holding a Dict decoded into %+v", sess.model)
	}
	if err == nil {
		t.Fatalf("decoding a v0.28 session holding a Dict succeeded")
	}
	for _, want := range []string{"older Sky", "Dict", "v0.29.0", "starts a new session"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

// v028SessionModel is the model type the fixture was written with; its
// `Counts` field is now a Dict.
type v028SessionModel struct {
	Name   string
	Counts SkyDict[int]
	Tags   []string
}

// JSON (a Spa wire payload, a JSON session store, a durable snapshot) writes a
// Dict exactly as it wrote the Go map, and reads it back.
func TestDictJSONMatchesTheMapItReplaced(t *testing.T) {
	d := AsMapT[int](Dict_fromList([]any{SkyTuple2{V0: 10, V1: 1}, SkyTuple2{V0: 2, V1: 5}}))
	got, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(map[string]int{"\x01i10": 1, "\x01i2": 5})
	if !bytes.Equal(got, want) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
	var back SkyDict[int]
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if !dictsEqual(back, d) {
		t.Fatalf("JSON round trip changed the Dict: %v", dictContent(back))
	}
}

type dictCodecRec struct {
	Name  string          `sky:"name,string"`
	Score SkyDict[int]    `sky:"score,rt.SkyDict[int]"`
	Tags  SkyDict[string] `sky:"tags,rt.SkyDict[string]"`
}

// Codec.auto encodes a record's Dict field as a JSON object and decodes it
// back into the typed Dict.
func TestCodecAutoRoundTripsADictField(t *testing.T) {
	rec := dictCodecRec{
		Name:  "x",
		Score: AsMapT[int](Dict_fromList([]any{SkyTuple2{V0: "a", V1: 1}, SkyTuple2{V0: "b", V1: 2}})),
		Tags:  AsMapT[string](Dict_fromList([]any{SkyTuple2{V0: 3, V1: "t"}})),
	}
	raw, err := codecAutoEncodeVal(reflect.ValueOf(rec), false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(raw)
	if !strings.Contains(string(b), `"score":{"a":1,"b":2}`) {
		t.Fatalf("encoded %s", b)
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	back, err := codecAutoDecodeVal(reflect.TypeOf(dictCodecRec{}), generic, false)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Interface().(dictCodecRec)
	if !deepEq(got, rec) {
		t.Fatalf("decoded %+v", got)
	}
}

// Elm's union prefers the left Dict; a foreign (FFI / Std.Db) map's bare keys
// stay reachable from an Int key after a union and an insert.
func TestDictUnionAndForeignKeys(t *testing.T) {
	left := Dict_fromList([]any{SkyTuple2{V0: 1, V1: "L"}})
	right := Dict_fromList([]any{SkyTuple2{V0: 1, V1: "R"}, SkyTuple2{V0: 2, V1: "R2"}})
	u := Dict_union(left, right)
	if want := []any{SkyTuple2{V0: 1, V1: "L"}, SkyTuple2{V0: 2, V1: "R2"}}; !reflect.DeepEqual(Dict_toList(u), want) {
		t.Fatalf("union = %v, want %v", Dict_toList(u), want)
	}
	fromDb := map[string]any{"10": "db"}
	u2 := Dict_union(Dict_fromList([]any{SkyTuple2{V0: 3, V1: "x"}}), fromDb)
	if got := Dict_get(10, u2); !reflect.DeepEqual(got, Just[any]("db")) {
		t.Fatalf("the bare \"10\" from a Std.Db map is lost after union: %v", got)
	}
	u3 := Dict_insert(10, any("new"), u2)
	if Dict_size(u3).(int) != 2 {
		t.Fatalf("insert beside a bare key added a duplicate: %v", Dict_toList(u3))
	}
}

// Set: O(log n) updates that keep old versions, toList in order.
func TestSetIsPersistent(t *testing.T) {
	s := Set_empty()
	var olds []any
	for i := 0; i < 300; i++ {
		s = Set_insert(i%97, s)
		if i%50 == 0 {
			olds = append(olds, s)
		}
	}
	if Set_size(s).(int) != 97 {
		t.Fatalf("size %d", Set_size(s))
	}
	s2 := Set_remove(5, s)
	if Set_member(5, s).(bool) != true || Set_member(5, s2).(bool) != false {
		t.Fatalf("remove changed the version it came from")
	}
	if Set_size(olds[0]).(int) != 1 {
		t.Fatalf("the first version holds %d elements", Set_size(olds[0]))
	}
	got := Set_toList(Set_union(Set_fromList([]any{3, 1}), Set_fromList([]any{2, 3})))
	if !reflect.DeepEqual(got, []any{1, 2, 3}) {
		t.Fatalf("union toList = %v", got)
	}
	keys := Set_toList(Set_diff(s, Set_fromList([]any{0, 1}))).([]any)
	if !sort.SliceIsSorted(keys, func(i, j int) bool { return keys[i].(int) < keys[j].(int) }) || len(keys) != 95 {
		t.Fatalf("diff = %v", keys)
	}
}

// dictDepth is the deepest leaf of a Dict's trie (0 for a flat Dict).
func dictDepth[V any](d SkyDict[V]) int {
	if !d.trie {
		return 0
	}
	var walk func(s *dslot[V], depth int) int
	walk = func(s *dslot[V], depth int) int {
		best := depth
		for i, c := range s.children() {
			if c.kids != nil {
				if got := walk(&s.children()[i], depth+1); got > best {
					best = got
				}
			}
		}
		return best
	}
	r := d.root()
	return walk(&r, 1)
}

// The short-key hash spreads the keys real Dicts hold (sequential Ints,
// numbered Strings) evenly. For a uniform hash the deepest of n leaves in a
// 16-way trie sits near log16(n²/2), 8.1 for 100,000 keys; a weak mix on
// these regular keys would go far deeper.
func TestDictHashKeepsTheTrieShallow(t *testing.T) {
	for _, mk := range []func(int) string{
		func(i int) string { return encodeDictKey(i) },
		func(i int) string { return "user-" + fmt.Sprint(i) },
		func(i int) string { return encodeDictKey(i * 1024) },
		func(i int) string { return fmt.Sprintf("a-much-longer-key-name-%08d", i) },
	} {
		keys := make([]string, 100000)
		vals := make([]int, len(keys))
		for i := range keys {
			keys[i] = mk(i)
		}
		d := dictBuild(keys, vals, false)
		if got := dictDepth(d); got > 10 {
			t.Fatalf("keys like %q: trie depth %d for 100,000 keys", keys[1], got)
		}
	}
}

// The fast Int and Char key encodings write exactly the `%v` bytes the
// general path writes, so keys stored by earlier versions still match.
func TestEncodeDictKeyFastPathsMatchTheGeneralForm(t *testing.T) {
	for _, n := range []int{0, 1, -1, 9, 10, 255, 256, -12345, 1 << 40, -(1 << 62)} {
		if got, want := encodeDictKey(n), "\x01i"+fmt.Sprintf("%v", n); got != want {
			t.Errorf("Int %d: %q, want %q", n, got, want)
		}
	}
	for _, r := range []rune{'a', 0, 'é', '😀'} {
		if got, want := encodeDictKey(r), "\x01c"+fmt.Sprintf("%v", r); got != want {
			t.Errorf("Char %q: %q, want %q", r, got, want)
		}
	}
}
