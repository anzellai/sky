package rt

import (
	"reflect"
	"strings"
	"testing"
)

// Typed-key Dict entry points.
//
// A Sky `Dict k v` is a Go `map[string]V`, so every key is stringified on the
// way in. The operations that let the key back OUT — toList, keys, values,
// foldl, map — have to undo that, and the string alone does not say what to
// undo it to ("97" is Int 97 and Char 'a'). The key type is supplied from
// outside: by the compiler (which routes to the `…IntKey` / `…CharKey` / …
// entry points) or, for the callback-taking foldl/map, by the callback's own
// declared first parameter.
//
// Issue #174 reported two failures of that decode: `Dict.toList` on a
// `Dict Char v` zeroed the key, and `Dict.foldl` on a `Dict Int v` PANICKED —
// a runtime panic from well-typed Sky. The third, unreported, is ordering:
// enumeration is defined to be ascending by key, and "10" sorts before "9".

// intDict builds the `Dict Int v` used throughout: keys deliberately span the
// 9/10 boundary where lexical and numeric order disagree.
func intDict() any {
	return Dict_fromList([]any{
		SkyTuple2{V0: 1, V1: "a"},
		SkyTuple2{V0: 2, V1: "b"},
		SkyTuple2{V0: 10, V1: "j"},
		SkyTuple2{V0: 9, V1: "i"},
	})
}

func charDict() any {
	return Dict_fromList([]any{
		SkyTuple2{V0: 'a', V1: 1},
		SkyTuple2{V0: 'b', V1: 2},
	})
}

func tupleKeys(t *testing.T, out any) []any {
	t.Helper()
	items, ok := out.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", out)
	}
	keys := make([]any, 0, len(items))
	for _, item := range items {
		tup, ok := item.(SkyTuple2)
		if !ok {
			t.Fatalf("expected SkyTuple2, got %T", item)
		}
		keys = append(keys, tup.V0)
	}
	return keys
}

// ── toList / keys: the key reaches the caller with its Sky type ────

func TestDictToListIntKeyParsesIntKeys(t *testing.T) {
	got := tupleKeys(t, Dict_toListIntKey(intDict()))
	// Ints, not strings — and in NUMERIC order (9 before 10).
	if want := []any{1, 2, 9, 10}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_toListIntKey keys = %#v, want %#v", got, want)
	}
}

func TestDictToListFloatKeyParsesFloatKeys(t *testing.T) {
	d := Dict_fromList([]any{
		SkyTuple2{V0: 1.5, V1: "a"},
		SkyTuple2{V0: 10.25, V1: "j"},
		SkyTuple2{V0: 9.75, V1: "i"},
	})
	got := tupleKeys(t, Dict_toListFloatKey(d))
	if want := []any{1.5, 9.75, 10.25}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_toListFloatKey keys = %#v, want %#v", got, want)
	}
}

// Issue #174 symptom 1: `Dict.fromList [('a', 1)] |> Dict.toList` came back
// with char code 0. Sky's Char is a Go rune, so `%v` stores the DECIMAL CODE
// ("97") and the caller's rune coercion of that string failed to 0.
func TestDictToListCharKeyParsesCharKeys(t *testing.T) {
	got := tupleKeys(t, Dict_toListCharKey(charDict()))
	if want := []any{'a', 'b'}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_toListCharKey keys = %#v, want %#v", got, want)
	}
	for _, k := range got {
		if _, ok := k.(rune); !ok {
			t.Errorf("key must be a rune, got %T", k)
		}
	}
}

func TestDictToListBoolKeyParsesBoolKeys(t *testing.T) {
	d := Dict_fromList([]any{
		SkyTuple2{V0: true, V1: "t"},
		SkyTuple2{V0: false, V1: "f"},
	})
	got := tupleKeys(t, Dict_toListBoolKey(d))
	if want := []any{false, true}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_toListBoolKey keys = %#v, want %#v", got, want)
	}
}

func TestDictKeysTypedVariants(t *testing.T) {
	if got, want := Dict_keysIntKey(intDict()), []any{1, 2, 9, 10}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_keysIntKey = %#v, want %#v", got, want)
	}
	if got, want := Dict_keysCharKey(charDict()), []any{'a', 'b'}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_keysCharKey = %#v, want %#v", got, want)
	}
}

// ── values: no key comes out, but the ORDER is the key order ───────

func TestDictValuesIntKeyFollowsNumericKeyOrder(t *testing.T) {
	got := Dict_valuesIntKey(intDict())
	// Lexical key order would be 1, 10, 2, 9 → a, j, b, i.
	if want := []any{"a", "b", "i", "j"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_valuesIntKey = %#v, want %#v", got, want)
	}
}

func TestDictValuesCharKeyFollowsCodePointOrder(t *testing.T) {
	if got, want := Dict_valuesCharKey(charDict()), []any{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_valuesCharKey = %#v, want %#v", got, want)
	}
}

// ── foldl / map: the key reaches a TYPED callback ──────────────────

// Issue #174 symptom 2. The fold function of a `Dict Int v` lowers to
// `func(int, string, []any) []any`; pre-fix the runtime handed its first
// parameter a string and skyCallDirect panicked.
func TestDictFoldlIntKeyPassesIntKeysInOrder(t *testing.T) {
	var seen []int
	fn := func(k int, _ string, acc []any) []any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldlIntKey(fn, []any{}, intDict())
	if want := []int{1, 2, 9, 10}; !reflect.DeepEqual(seen, want) {
		t.Errorf("Dict_foldlIntKey visited %v, want %v", seen, want)
	}
}

func TestDictFoldlCharKeyPassesRuneKeys(t *testing.T) {
	var seen []rune
	fn := func(k rune, _ int, acc []any) []any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldlCharKey(fn, []any{}, charDict())
	if want := []rune{'a', 'b'}; !reflect.DeepEqual(seen, want) {
		t.Errorf("Dict_foldlCharKey visited %v, want %v", seen, want)
	}
}

func TestDictMapIntKeyPassesIntKeys(t *testing.T) {
	fn := func(k int, v string) string {
		return String_fromInt(k).(string) + v
	}
	out, ok := Dict_mapIntKey(fn, intDict()).(map[string]any)
	if !ok {
		t.Fatalf("Dict_mapIntKey must return map[string]any")
	}
	// The result keeps the ORIGINAL (encoded) keys — only values change.
	if got, want := out["\x01i10"], any("10j"); got != want {
		t.Errorf("out[%q] = %#v, want %#v", "\x01i10", got, want)
	}
	if got, want := out["\x01i1"], any("1a"); got != want {
		t.Errorf("out[%q] = %#v, want %#v", "\x01i1", got, want)
	}
}

// ── The un-routed entry points read the key type off the callback ──
//
// This is what covers call sites the compiler could not type (point-free use,
// a helper that takes the Dict as a parameter). Without it those sites keep
// the panic.

func TestDictFoldlInfersKeyTypeFromCallback(t *testing.T) {
	var seen []int
	fn := func(k int, _ string, acc []any) []any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldl(fn, []any{}, intDict())
	if want := []int{1, 2, 9, 10}; !reflect.DeepEqual(seen, want) {
		t.Errorf("Dict_foldl visited %v, want %v", seen, want)
	}
}

func TestDictMapInfersKeyTypeFromCallback(t *testing.T) {
	fn := func(k rune, v int) int { return int(k) + v }
	out, ok := Dict_map(fn, charDict()).(map[string]any)
	if !ok {
		t.Fatalf("Dict_map must return map[string]any")
	}
	if got, want := out["\x01c97"], any(98); got != want {
		t.Errorf("out[%q] = %#v, want %#v", "\x01c97", got, want)
	}
}

// A curried callback (`func(int) func(string) func(any) any`) is the other
// lowering shape; In(0) still names the key type.
func TestDictFoldlInfersKeyTypeFromCurriedCallback(t *testing.T) {
	var seen []int
	fn := func(k int) func(string) func(any) any {
		return func(_ string) func(any) any {
			return func(acc any) any {
				seen = append(seen, k)
				return acc
			}
		}
	}
	Dict_foldl(fn, nil, intDict())
	if want := []int{1, 2, 9, 10}; !reflect.DeepEqual(seen, want) {
		t.Errorf("curried Dict_foldl visited %v, want %v", seen, want)
	}
}

// A key-polymorphic callback (`func(any, any, any) any`) has no key type to
// read. It used to get the string key for that reason — and that is the #174
// tail: `f : Dict k v -> …` lowers its callback to exactly this shape, so
// `String.fromInt` on the result panicked with "rt.AsInt: expected numeric
// value, got string (10)".
//
// The key now carries its own kind, so the callback gets the Int regardless.
// See dict_poly_key_test.go for the full matrix.
func TestDictFoldlKeyPolymorphicCallbackStillDecodes(t *testing.T) {
	var seen []any
	fn := func(k any, _ any, acc any) any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldl(fn, nil, intDict())
	if want := []any{1, 2, 9, 10}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("an any-typed callback saw %#v, want %#v", seen, want)
	}
}

// A callback whose key parameter is a WIDER numeric type than the decode
// produces (`int64` vs `int`) is skyCallDirect's `safeReflectConvert` arm's
// job, not this layer's — the decode only has to produce a numeric.
func TestDictFoldlWidensKeyToWiderParamType(t *testing.T) {
	var seen []int64
	fn := func(k int64, _ string, acc any) any {
		seen = append(seen, k)
		return acc
	}
	Dict_foldl(fn, nil, intDict())
	if want := []int64{1, 2, 9, 10}; !reflect.DeepEqual(seen, want) {
		t.Errorf("visited %v, want %v", seen, want)
	}
}

// ── String keys: the overwhelmingly common case, unchanged ─────────

func TestDictStringKeysUnchanged(t *testing.T) {
	d := Dict_fromList([]any{
		SkyTuple2{V0: "b", V1: 2},
		SkyTuple2{V0: "a", V1: 1},
		SkyTuple2{V0: "10", V1: 10},
		SkyTuple2{V0: "9", V1: 9},
	})
	// Lexical order, because that IS the key order for String keys.
	if got, want := Dict_keys(d), []any{"10", "9", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_keys = %#v, want %#v", got, want)
	}
	if got, want := Dict_values(d), []any{10, 9, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_values = %#v, want %#v", got, want)
	}
	if got, want := tupleKeys(t, Dict_toList(d)), []any{"10", "9", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_toList keys = %#v, want %#v", got, want)
	}
	var seen []string
	Dict_foldl(func(k string, _ int, acc any) any {
		seen = append(seen, k)
		return acc
	}, nil, d)
	if want := []string{"10", "9", "a", "b"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("Dict_foldl visited %v, want %v", seen, want)
	}
}

// ── Lookup still agrees with iteration ─────────────────────────────

func TestDictLookupAgreesWithTypedIteration(t *testing.T) {
	if got := Dict_get(10, intDict()); !reflect.DeepEqual(got, Just[any]("j")) {
		t.Errorf("Dict_get 10 = %#v, want Just \"j\"", got)
	}
	if got := Dict_get('a', charDict()); !reflect.DeepEqual(got, Just[any](1)) {
		t.Errorf("Dict_get 'a' = %#v, want Just 1", got)
	}
	if got := Dict_member('b', charDict()); got != true {
		t.Errorf("Dict_member 'b' = %v, want true", got)
	}
	after := Dict_remove(10, intDict())
	if got, want := Dict_keysIntKey(after), []any{1, 2, 9}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys after remove 10 = %#v, want %#v", got, want)
	}
}

// ── Malformed input still returns, rather than panicking ───────────

func TestDictTypedKeyFallsBackOnUnparsableKey(t *testing.T) {
	// A `map[string]any` from FFI whose keys were never Sky Ints.
	d := map[string]any{"42": "ok", "bad": "noise"}
	out := tupleKeys(t, Dict_toListIntKey(d))
	if len(out) != 2 {
		t.Fatalf("expected 2 tuples, got %d", len(out))
	}
	for _, k := range out {
		if _, ok := k.(int); !ok {
			t.Errorf("key must be int, got %T", k)
		}
	}
	if got, want := Dict_keysFloatKey(d), []any{0.0, 42.0}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dict_keysFloatKey = %#v, want %#v", got, want)
	}
}

// ── Composite keys: the documented boundary ───────────────────────
//
// `%v` is not injective for tuples/records — ("a b", "c") and ("a", "b c")
// both stringify to "{a b c}" — so a composite key does not survive the round
// trip and no decoder could be correct. Iterating such a Dict panicked with
// "argument 0 type mismatch", which reads as a compiler bug; it is a
// limitation, and it now says so.
func TestDictFoldlCompositeKeyPanicsWithTheLimitation(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected a panic naming the unsupported key type")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "rt.Dict: unsupported key type") {
			t.Fatalf("panic message = %q, want the unsupported-key-type message", msg)
		}
		if kind, _ := classifyPanic(msg); kind != "UnsupportedDictKey" {
			t.Errorf("classifyPanic kind = %q, want UnsupportedDictKey", kind)
		}
	}()
	d := Dict_fromList([]any{
		SkyTuple2{V0: SkyTuple2{V0: 1, V1: 2}, V1: "a"},
	})
	Dict_foldl(func(_ SkyTuple2, _ any, acc any) any { return acc }, nil, d)
}

// The guard must NOT fire where the callback can take the key: an any-typed
// (key-polymorphic) callback receives the string key as it always did.
func TestDictFoldlCompositeKeyWithAnyCallbackDoesNotPanic(t *testing.T) {
	d := Dict_fromList([]any{
		SkyTuple2{V0: SkyTuple2{V0: 1, V1: 2}, V1: "a"},
	})
	var seen []any
	Dict_foldl(func(k any, _ any, acc any) any {
		seen = append(seen, k)
		return acc
	}, nil, d)
	if len(seen) != 1 {
		t.Fatalf("expected 1 visit, got %d", len(seen))
	}
	if _, ok := seen[0].(string); !ok {
		t.Errorf("expected the string key, got %T", seen[0])
	}
}

func TestDictTypedKeyOnEmptyDict(t *testing.T) {
	empty := Dict_empty()
	for name, got := range map[string]any{
		"toList": Dict_toListCharKey(empty),
		"keys":   Dict_keysIntKey(empty),
		"values": Dict_valuesFloatKey(empty),
	} {
		if l, ok := got.([]any); !ok || len(l) != 0 {
			t.Errorf("%s on an empty Dict = %#v, want an empty []any", name, got)
		}
	}
	if got := Dict_foldlIntKey(func(int, any, any) any { return nil }, "acc", empty); got != "acc" {
		t.Errorf("Dict_foldlIntKey on an empty Dict = %#v, want the accumulator", got)
	}
}
