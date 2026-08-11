package rt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Issue #174 tail — the key type has to survive ERASURE.
//
// dict_typed_key_test.go covers the case where the key type is supplied from
// OUTSIDE the key: by the compiler's call-site routing, or by the callback's
// declared first parameter. A key-polymorphic helper (`f : Dict k v -> …`) has
// neither — the lowering erases `k` to `any` — so the runtime handed back the
// raw map key and `String.fromInt` panicked with "rt.AsInt: expected numeric
// value, got string (10)".
//
// The fix makes the KEY ITSELF carry its kind. These tests pin the encoding,
// the decode, the fallbacks that keep un-encoded maps working, and the display
// path that has to hide the whole thing from the user again.

// anyKeyFn is the shape a key-polymorphic Sky helper lowers its callback to:
// every parameter erased to `any`. It is what made `dictKeyKindForFn`
// unable to help.
func anyKeyFoldSeen(dict any) []any {
	var seen []any
	fn := func(k any, _ any, acc any) any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldl(fn, nil, dict)
	return seen
}

// ── The encoding ───────────────────────────────────────────────────

func TestEncodeDictKeyTagsEveryNonStringKind(t *testing.T) {
	cases := []struct {
		name string
		key  any
		want string
	}{
		{"Int", 10, "\x01i10"},
		{"Int negative", -3, "\x01i-3"},
		{"Char", 'a', "\x01c97"},
		{"Float", 1.5, "\x01f1.5"},
		{"Bool true", true, "\x01btrue"},
		{"Bool false", false, "\x01bfalse"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := encodeDictKey(c.key); got != c.want {
				t.Errorf("encodeDictKey(%#v) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

// String keys are the shape that crosses into JSON objects, Std.Db rows, HTTP
// headers and FFI `map[string]any`, where the key IS the external name. They
// must come out byte-identical to what they were before this change.
func TestEncodeDictKeyLeavesStringKeysVerbatim(t *testing.T) {
	for _, s := range []string{"", "a", "user_id", "10", "true", "a b", "ключ", "a:b/c"} {
		if got := encodeDictKey(s); got != s {
			t.Errorf("encodeDictKey(%q) = %q, want it unchanged", s, got)
		}
	}
}

// A String key that starts with the tag byte is the one ambiguity, and it is
// escaped rather than left to be read back as some other kind's key.
func TestEncodeDictKeyEscapesStringStartingWithTag(t *testing.T) {
	raw := "\x01i10"
	enc := encodeDictKey(raw)
	if enc == raw {
		t.Fatalf("a String key starting with the tag byte must be escaped, got %q", enc)
	}
	got, kind, ok := decodeTaggedDictKey(enc)
	if !ok || kind != dictKeyString {
		t.Fatalf("decodeTaggedDictKey(%q) = (%#v, %v, %v), want a String", enc, got, kind, ok)
	}
	if got != any(raw) {
		t.Errorf("escaped String key decoded to %#v, want %#v", got, raw)
	}
}

// A composite key (tuple/record/list/ADT) is not decodable — `%v` is not
// injective for it — so it stays untagged and one-way, exactly as before.
func TestEncodeDictKeyLeavesCompositeKeysUntagged(t *testing.T) {
	key := SkyTuple2{V0: "a", V1: "b"}
	enc := encodeDictKey(key)
	if strings.ContainsRune(enc, dictKeyTagByte) {
		t.Fatalf("a composite key must not be tagged, got %q", enc)
	}
	if want := fmt.Sprintf("%v", key); enc != want {
		t.Errorf("encodeDictKey(tuple) = %q, want %q", enc, want)
	}
}

func TestEncodeDecodeRoundTripsEveryDecodableKind(t *testing.T) {
	for _, key := range []any{"a", "", 0, 10, -3, 'a', 'ω', 1.5, -0.25, true, false} {
		enc := encodeDictKey(key)
		got, _, ok := decodeTaggedDictKey(enc)
		if !ok {
			// Only an untagged String key takes this branch, and then the
			// encoded form IS the key.
			if s, isStr := key.(string); isStr && enc == s {
				continue
			}
			t.Fatalf("encodeDictKey(%#v) = %q did not decode", key, enc)
		}
		if !reflect.DeepEqual(got, key) {
			t.Errorf("round trip of %#v (%T) gave %#v (%T)", key, key, got, got)
		}
	}
}

// ── The panic that started it: a key-polymorphic callback ──────────

func TestKeyPolymorphicFoldlSeesTypedKeysForEveryKind(t *testing.T) {
	cases := []struct {
		name string
		dict any
		want []any
	}{
		{
			"Int",
			Dict_fromList([]any{
				SkyTuple2{V0: 1, V1: "a"},
				SkyTuple2{V0: 10, V1: "j"},
				SkyTuple2{V0: 9, V1: "i"},
			}),
			// Numeric order, not the lexical order of "1","10","9".
			[]any{1, 9, 10},
		},
		{
			"Char",
			Dict_fromList([]any{
				SkyTuple2{V0: 'b', V1: 2},
				SkyTuple2{V0: 'a', V1: 1},
			}),
			[]any{'a', 'b'},
		},
		{
			"Float",
			Dict_fromList([]any{
				SkyTuple2{V0: 10.25, V1: "j"},
				SkyTuple2{V0: 1.5, V1: "a"},
			}),
			[]any{1.5, 10.25},
		},
		{
			"Bool",
			Dict_fromList([]any{
				SkyTuple2{V0: true, V1: "t"},
				SkyTuple2{V0: false, V1: "f"},
			}),
			[]any{false, true},
		},
		{
			"String",
			Dict_fromList([]any{
				SkyTuple2{V0: "b", V1: 2},
				SkyTuple2{V0: "a", V1: 1},
			}),
			[]any{"a", "b"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := anyKeyFoldSeen(c.dict)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("an any-typed callback saw %#v, want %#v", got, c.want)
			}
		})
	}
}

// The un-routed `Dict_keys` / `Dict_toList` / `Dict_values` are what a
// key-polymorphic helper compiles to: no compiler routing, no callback to read.
func TestUnroutedIterationDecodesTaggedKeys(t *testing.T) {
	d := intDict()

	keys, ok := Dict_keys(d).([]any)
	if !ok {
		t.Fatalf("Dict_keys must return []any")
	}
	if want := []any{1, 2, 9, 10}; !reflect.DeepEqual(keys, want) {
		t.Errorf("Dict_keys = %#v, want %#v", keys, want)
	}

	if got := tupleKeys(t, Dict_toList(d)); !reflect.DeepEqual(got, []any{1, 2, 9, 10}) {
		t.Errorf("Dict_toList keys = %#v, want [1 2 9 10]", got)
	}

	vals, ok := Dict_values(d).([]any)
	if !ok {
		t.Fatalf("Dict_values must return []any")
	}
	// Ordered by the NUMERIC key: "10" sorts before "9" lexically.
	if want := []any{"a", "b", "i", "j"}; !reflect.DeepEqual(vals, want) {
		t.Errorf("Dict_values = %#v, want %#v", vals, want)
	}
}

// ── The fallbacks that keep un-encoded maps working ────────────────
//
// A `map[string]any` typed as a `Dict Int v` but built somewhere else — an FFI
// return, a Std.Db row, a JSON object, a session payload from an older binary —
// holds "10", not the encoded form. Everything about it must behave as it did
// before this change.

func legacyIntMap() any {
	return map[string]any{"1": "a", "2": "b", "10": "j", "9": "i"}
}

func TestUntaggedMapStillUsesTheCompilerRoutedKind(t *testing.T) {
	got := tupleKeys(t, Dict_toListIntKey(legacyIntMap()))
	if want := []any{1, 2, 9, 10}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_toListIntKey on an untagged map = %#v, want %#v", got, want)
	}
}

func TestUntaggedMapStillUsesTheCallbackDeclaredKind(t *testing.T) {
	var seen []int
	fn := func(k int, _ any, acc any) any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldl(fn, nil, legacyIntMap())
	if want := []int{1, 2, 9, 10}; !reflect.DeepEqual(seen, want) {
		t.Errorf("Dict_foldl on an untagged map visited %v, want %v", seen, want)
	}
}

func TestLookupFindsAnUntaggedKey(t *testing.T) {
	if got := Dict_member(10, legacyIntMap()); got != any(true) {
		t.Errorf("Dict_member(10) on an untagged map = %#v, want true", got)
	}
	got := Dict_get(10, legacyIntMap())
	if want := Just[any]("j"); !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_get(10) on an untagged map = %#v, want %#v", got, want)
	}
}

// Inserting into such a map must REPLACE the existing entry, not add a second
// key that is logically the same one.
func TestInsertIntoUntaggedMapReplacesRatherThanDuplicates(t *testing.T) {
	out := Dict_insert(10, "J", legacyIntMap())
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Dict_insert must return map[string]any")
	}
	if len(m) != 4 {
		t.Fatalf("insert added a duplicate key: %d entries, want 4 (%v)", len(m), m)
	}
	if got := m["10"]; got != any("J") {
		t.Errorf("m[\"10\"] = %#v, want \"J\"", got)
	}
}

func TestRemoveFromUntaggedMapRemovesTheEntry(t *testing.T) {
	out := Dict_remove(10, legacyIntMap())
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Dict_remove must return map[string]any")
	}
	if _, still := m["10"]; still || len(m) != 3 {
		t.Errorf("remove left %v, want the \"10\" entry gone", m)
	}
}

// A map holding BOTH conventions (insert into an FFI-supplied map) still
// enumerates — totally ordered, no failed type assertion in the comparator.
func TestMixedTaggedAndUntaggedMapEnumerates(t *testing.T) {
	mixed := Dict_insert(3, "c", map[string]any{"1": "a"})
	keys, ok := Dict_keys(mixed).([]any)
	if !ok {
		t.Fatalf("Dict_keys must return []any")
	}
	if len(keys) != 2 {
		t.Fatalf("mixed map enumerated %#v, want 2 keys", keys)
	}
	// "1" has no tag and no supplied kind, so it stays a String; 3 decodes.
	if keys[0] != any("1") || keys[1] != any(3) {
		t.Errorf("mixed map enumerated %#v, want [\"1\" 3]", keys)
	}
}

// ── Composite keys are still reported, not silently mis-decoded ────

func TestCompositeKeyStillPanicsWithTheDocumentedMessage(t *testing.T) {
	d := Dict_fromList([]any{
		SkyTuple2{V0: SkyTuple2{V0: 1, V1: 2}, V1: "x"},
	})
	fn := func(_ SkyTuple2, _ any, acc any) any { return acc }
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("a composite Dict key must be reported, not silently mis-decoded")
		}
		if !strings.Contains(fmt.Sprint(r), "unsupported key type") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	Dict_foldl(fn, nil, d)
}

// ── Display: the tag is internal and must never reach a human ──────

func TestToStringRendersDictKeysInTheirLogicalForm(t *testing.T) {
	cases := []struct {
		name string
		dict any
		want string
	}{
		{"Int", Dict_fromList([]any{SkyTuple2{V0: 10, V1: "j"}}), "map[10:j]"},
		{"Char", Dict_fromList([]any{SkyTuple2{V0: 'a', V1: 1}}), "map[97:1]"},
		{"Float", Dict_fromList([]any{SkyTuple2{V0: 1.5, V1: "a"}}), "map[1.5:a]"},
		{"Bool", Dict_fromList([]any{SkyTuple2{V0: true, V1: "t"}}), "map[true:t]"},
		{"String", Dict_fromList([]any{SkyTuple2{V0: "a", V1: 1}}), "map[a:1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Debug_toString(c.dict); got != any(c.want) {
				t.Errorf("Debug_toString = %#v, want %#v", got, c.want)
			}
			if got := Basics_toString(c.dict); got != c.want {
				t.Errorf("Basics_toString = %#v, want %#v", got, c.want)
			}
		})
	}
}

// Ordering of the rendered map is fmt's (lexical over the displayed key), and
// that is what it was before the encoding existed.
func TestToStringOrdersRenderedKeysLikeItAlwaysDid(t *testing.T) {
	got := Debug_toString(intDict())
	if want := "map[1:a 10:j 2:b 9:i]"; got != any(want) {
		t.Errorf("Debug_toString(intDict) = %#v, want %#v", got, want)
	}
}

// A Dict nested inside a record / list / another Dict is detagged in place —
// the walk rebuilds same-typed values, so the rest of the render is untouched.
func TestToStringDetagsNestedDicts(t *testing.T) {
	type holder struct {
		Name string
		ByID any
	}
	v := holder{Name: "x", ByID: Dict_fromList([]any{SkyTuple2{V0: 7, V1: "s"}})}
	got := Basics_toString(v)
	if strings.ContainsRune(got, dictKeyTagByte) {
		t.Fatalf("a nested Dict leaked the tag: %q", got)
	}
	if want := "{x map[7:s]}"; got != want {
		t.Errorf("Basics_toString(record) = %q, want %q", got, want)
	}

	list := []any{Dict_fromList([]any{SkyTuple2{V0: 7, V1: "s"}})}
	if got := Basics_toString(list); strings.ContainsRune(got, dictKeyTagByte) {
		t.Fatalf("a Dict inside a list leaked the tag: %q", got)
	}

	outer := Dict_fromList([]any{
		SkyTuple2{V0: 1, V1: Dict_fromList([]any{SkyTuple2{V0: 2, V1: "z"}})},
	})
	if got := Basics_toString(outer); got != "map[1:map[2:z]]" {
		t.Errorf("Basics_toString(Dict of Dict) = %q, want %q", got, "map[1:map[2:z]]")
	}
}

// A value with no Dict in it renders byte-identically to plain `%v` — the
// display path must not become a second, divergent renderer.
func TestToStringLeavesNonDictValuesAlone(t *testing.T) {
	for _, v := range []any{42, "hello", []any{1, 2}, map[string]any{"a": 1}, SkyTuple2{V0: 1, V1: "x"}} {
		if got, want := Basics_toString(v), fmt.Sprintf("%v", v); got != want {
			t.Errorf("Basics_toString(%#v) = %q, want %q", v, got, want)
		}
	}
}

// ── The encoded key has to survive the wire ────────────────────────
//
// A Dict travels inside a Sky.Live model to a session store (gob) and back. The
// tag byte is `\x01` and not `\x00` precisely so that it also survives a text
// column: PostgreSQL rejects a NUL byte in `text`/`jsonb`.

func TestTagByteIsNotNul(t *testing.T) {
	if dictKeyTagByte == 0 {
		t.Fatal("the tag byte must not be NUL — PostgreSQL rejects it in text/jsonb")
	}
}

func TestEncodedKeySurvivesAMapCopy(t *testing.T) {
	d := intDict()
	// AsDict is the reflect-copy every typed narrowing goes through.
	copied := AsDict(d)
	keys, ok := Dict_keys(copied).([]any)
	if !ok {
		t.Fatalf("Dict_keys must return []any")
	}
	if want := []any{1, 2, 9, 10}; !reflect.DeepEqual(keys, want) {
		t.Errorf("after AsDict, Dict_keys = %#v, want %#v", keys, want)
	}
}
