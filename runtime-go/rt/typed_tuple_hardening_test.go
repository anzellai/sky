package rt

import "testing"

// Phase 0 (v0.17 typed-tuple ceiling) hardens the runtime reflection
// sites that hard-asserted `SkyTuple2` so a distinct nominal generic
// instantiation (`T2[string, int]`) flows through soundly instead of
// panicking or silently returning nil. These tests pin that contract:
// typed tuples now work through fst/snd/Dict.fromList, while the
// genuinely-bad-shape silent-skip semantics of the typed Dict builders
// are preserved.

// A typed tuple literal, as emitted by Phase 1 typed-tuple codegen.
// Its Go static type is `T2[string, int]`, a distinct nominal from
// `SkyTuple2 = T2[any, any]`.
func typedStrIntPair(k string, v int) T2[string, int] {
	return T2[string, int]{V0: k, V1: v}
}

func TestBasicsFstSndTypedTuple(t *testing.T) {
	tup := typedStrIntPair("k", 7)

	if got := Basics_fst(tup); got != "k" {
		t.Fatalf("Basics_fst on typed T2[string,int]: want \"k\", got %v (%T)", got, got)
	}
	if got := Basics_snd(tup); got != 7 {
		t.Fatalf("Basics_snd on typed T2[string,int]: want 7, got %v (%T)", got, got)
	}

	// Fast path (erased SkyTuple2) must stay byte-identical.
	erased := SkyTuple2{V0: "a", V1: 1}
	if got := Basics_fst(erased); got != "a" {
		t.Fatalf("Basics_fst on SkyTuple2: want \"a\", got %v", got)
	}
	if got := Basics_snd(erased); got != 1 {
		t.Fatalf("Basics_snd on SkyTuple2: want 1, got %v", got)
	}

	// A genuine non-tuple still yields nil (was `return nil`).
	if got := Basics_fst(42); got != nil {
		t.Fatalf("Basics_fst on non-tuple: want nil, got %v", got)
	}
	if got := Basics_snd("nope"); got != nil {
		t.Fatalf("Basics_snd on non-tuple: want nil, got %v", got)
	}
}

func TestBasicsFstSndTypedTuple3(t *testing.T) {
	tup3 := T3[string, int, bool]{V0: "x", V1: 9, V2: true}
	if got := Basics_fst(tup3); got != "x" {
		t.Fatalf("Basics_fst on typed T3: want \"x\", got %v", got)
	}
	// AsTuple2 reflect-reboxes the first TWO fields of any struct, so
	// snd of a T3 returns the second element.
	if got := Basics_snd(tup3); got != 9 {
		t.Fatalf("Basics_snd on typed T3: want 9, got %v", got)
	}
}

func TestDictFromListTypedTuple(t *testing.T) {
	// Was a hard panic on the `item.(SkyTuple2)` assertion.
	list := []any{
		typedStrIntPair("one", 1),
		typedStrIntPair("two", 2),
	}
	got := Dict_fromList(list).(map[string]any)
	if len(got) != 2 || got["one"] != 1 || got["two"] != 2 {
		t.Fatalf("Dict_fromList on typed T2 list: got %#v", got)
	}

	// Mixed erased + typed entries still build.
	mixed := []any{
		SkyTuple2{V0: "a", V1: 10},
		typedStrIntPair("b", 20),
	}
	gm := Dict_fromList(mixed).(map[string]any)
	if gm["a"] != 10 || gm["b"] != 20 {
		t.Fatalf("Dict_fromList mixed: got %#v", gm)
	}
}

func TestDictFromListTTypedTuple(t *testing.T) {
	list := []any{
		typedStrIntPair("one", 1),
		typedStrIntPair("two", 2),
	}
	got := Dict_fromListT[int](list)
	if got["one"] != 1 || got["two"] != 2 {
		t.Fatalf("Dict_fromListT[int] on typed T2 list: got %#v", got)
	}
}

func TestDictFromListTSkipsNonTuple(t *testing.T) {
	// A genuine non-tuple element must be silently skipped (preserving
	// the `default:` arm semantics), not panic.
	list := []any{
		typedStrIntPair("keep", 5),
		42,     // non-tuple: skipped
		"nope", // non-tuple: skipped
	}
	got := Dict_fromListT[int](list)
	if len(got) != 1 || got["keep"] != 5 {
		t.Fatalf("Dict_fromListT skip: want single {keep:5}, got %#v", got)
	}
}

func TestJsonEncObjectTypedTuple(t *testing.T) {
	// JsonEnc.object takes `List (String, Value)`; typed-tuple codegen emits
	// `rt.T2[string, JsonValue]`. Was silently skipped → empty `{}` object.
	pairs := []any{
		T2[string, JsonValue]{V0: "name", V1: JsonValue{raw: "Alice"}},
		T2[string, JsonValue]{V0: "age", V1: JsonValue{raw: 30}},
	}
	obj := JsonEnc_object(pairs)
	got := JsonEnc_encode(0, obj).(string)
	// Insertion order preserved (Elm Json.Encode.object semantics) — name
	// was inserted before age.
	want := `{"name":"Alice","age":30}`
	if got != want {
		t.Fatalf("JsonEnc_object typed T2: want %s, got %s", want, got)
	}

	// Erased SkyTuple2 path stays byte-identical.
	erased := []any{
		SkyTuple2{V0: "k", V1: JsonValue{raw: true}},
	}
	if g := JsonEnc_encode(0, JsonEnc_object(erased)).(string); g != `{"k":true}` {
		t.Fatalf("JsonEnc_object erased: got %s", g)
	}
}

func TestUnpackPairTypedTuple(t *testing.T) {
	// unpackPair pulls (String, V) for Db.updateFields/insertFields; typed
	// codegen emits `rt.T2[string, V]`. Was silently dropped.
	col, val, ok := unpackPair(T2[string, int]{V0: "age", V1: 42})
	if !ok || col != "age" || val != 42 {
		t.Fatalf("unpackPair typed T2: got col=%q val=%v ok=%v", col, val, ok)
	}
	// Non-tuple → false (unchanged).
	if _, _, ok := unpackPair(99); ok {
		t.Fatalf("unpackPair non-tuple: want ok=false")
	}
	// Erased SkyTuple2 → still works.
	if c, v, ok := unpackPair(SkyTuple2{V0: "name", V1: "bob"}); !ok || c != "name" || v != "bob" {
		t.Fatalf("unpackPair erased: got c=%q v=%v ok=%v", c, v, ok)
	}
}

func TestDictFromListTASkipsNonTuple(t *testing.T) {
	list := []any{
		typedStrIntPair("keep", 5),
		42,     // non-tuple: skipped
		"nope", // non-tuple: skipped
	}
	got := Dict_fromListTA(list).(map[string]any)
	if len(got) != 1 || got["keep"] != 5 {
		t.Fatalf("Dict_fromListTA skip: want single {keep:5}, got %#v", got)
	}

	// Typed tuples flow through too.
	typed := []any{typedStrIntPair("a", 1)}
	gt := Dict_fromListTA(typed).(map[string]any)
	if gt["a"] != 1 {
		t.Fatalf("Dict_fromListTA typed: got %#v", gt)
	}
}
