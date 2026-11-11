package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Codec.auto writes a Dict's key as the plain key a JSON reader expects
// (`"10"` for a `Dict Int v` key 10), never the runtime's internal stored
// form (`"\u0001i10"`). It reads both: the plain form, and the stored form
// that v0.29.8 and earlier wrote into saved models and stored rows. Either
// way the decoded Dict holds the stored key, because the declared type names
// the key kind (`rt.SkyDict[@i:V]`), so a key-generic helper reads an Int.

type dictKeyRec struct {
	Name  string          `sky:"name,string"`
	ByNum SkyDict[string] `sky:"byNum,rt.SkyDict[@i:string]"`
	ByStr SkyDict[int]    `sky:"byStr,rt.SkyDict[int]"`
}

func dictKeyRecSample() dictKeyRec {
	return dictKeyRec{
		Name: "x",
		ByNum: AsMapT[string](Dict_fromList([]any{
			SkyTuple2{V0: 2, V1: "two"}, SkyTuple2{V0: 10, V1: "ten"}, SkyTuple2{V0: -1, V1: "minus"},
		})),
		ByStr: AsMapT[int](Dict_fromList([]any{
			SkyTuple2{V0: "b", V1: 2}, SkyTuple2{V0: "\x01i5", V1: 5},
		})),
	}
}

func dictKeyEncode(t *testing.T, v any) string {
	t.Helper()
	tree, err := codecAutoEncodeVal(reflect.ValueOf(v), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dictKeyDecode(t *testing.T, doc string) dictKeyRec {
	t.Helper()
	var generic any
	if err := json.Unmarshal([]byte(doc), &generic); err != nil {
		t.Fatal(err)
	}
	back, err := codecAutoDecodeVal(reflect.TypeOf(dictKeyRec{}), generic, false)
	if err != nil {
		t.Fatal(err)
	}
	return back.Interface().(dictKeyRec)
}

func TestCodecAutoWritesAnIntDictKeyAsItsDecimal(t *testing.T) {
	got := dictKeyEncode(t, dictKeyRecSample())
	// Byte-wise order of the written keys: the order the stored keys had,
	// since every stored Int key carries the same two-byte tag.
	if !strings.Contains(got, `"byNum":{"-1":"minus","10":"ten","2":"two"}`) {
		t.Fatalf("an Int key must be its decimal: %s", got)
	}
	if strings.Contains(got, `"\u0001i`) {
		t.Fatalf("the internal key form leaked: %s", got)
	}
	// A String key that starts with the tag byte keeps its escape, so it
	// can never read back as an Int key.
	if !strings.Contains(got, `"byStr":{"\u0001s\u0001i5":5,"b":2}`) {
		t.Fatalf("a String key must be written as stored: %s", got)
	}
}

func dictKeyCheckDecoded(t *testing.T, back dictKeyRec) {
	t.Helper()
	want := dictKeyRecSample()
	if !deepEq(back, want) {
		t.Fatalf("decoded %#v, want %#v", back, want)
	}
	if !reflect.DeepEqual(dictContent(back.ByNum), dictContent(want.ByNum)) {
		t.Fatalf("the decoded Dict must hold the stored keys: %q", dictContent(back.ByNum))
	}
	// A key-generic helper (no key type at the call site) reads Ints.
	keys := AsList(Dict_keys(back.ByNum))
	if len(keys) != 3 || keys[0] != -1 || keys[1] != 2 || keys[2] != 10 {
		t.Fatalf("Dict.keys = %#v, want Ints [-1 2 10]", keys)
	}
	if got := Dict_get(10, back.ByNum); !deepEq(got, Just[any]("ten")) {
		t.Fatalf("Dict.get 10 = %#v", got)
	}
	after := Dict_insert(10, "TEN", back.ByNum)
	if n := asSkyDict(after).dictLen(); n != 3 {
		t.Fatalf("inserting an existing key must replace it, len %d", n)
	}
}

func TestCodecAutoReadsThePlainDictKeyForm(t *testing.T) {
	doc := dictKeyEncode(t, dictKeyRecSample())
	dictKeyCheckDecoded(t, dictKeyDecode(t, doc))
}

// A model saved by v0.29.8 (a Sky.Spa client's saved model, a stored row)
// holds the internal form. It must still decode to the same value.
func TestCodecAutoReadsTheV0298DictKeyForm(t *testing.T) {
	old := `{"name":"x","byNum":{"\u0001i-1":"minus","\u0001i10":"ten","\u0001i2":"two"},"byStr":{"b":2,"\u0001s\u0001i5":5}}`
	dictKeyCheckDecoded(t, dictKeyDecode(t, old))
}

// The generated codecs' helpers give the same keys, in the same order.
func TestAutoDictSortedWritesPlainKeys(t *testing.T) {
	d := dictKeyRecSample().ByNum
	ks, vs := AutoDictSorted(d)
	if strings.Join(ks, ",") != "-1,10,2" || strings.Join(vs, ",") != "minus,ten,two" {
		t.Fatalf("AutoDictSorted = %q %q", ks, vs)
	}
}

func TestAutoDictBuildIntKeysReadsBothForms(t *testing.T) {
	want := dictKeyRecSample().ByNum
	for _, keys := range [][]string{
		{"-1", "10", "2"},
		{"\x01i-1", "\x01i10", "\x01i2"},
		{"-1", "\x01i10", "2"},
	} {
		got := AutoDictBuildIntKeys(keys, []string{"minus", "ten", "two"})
		if !reflect.DeepEqual(dictContent(got), dictContent(want)) {
			t.Fatalf("keys %q: %v, want %v", keys, dictContent(got), dictContent(want))
		}
	}
	// A key that is not a canonical Int stays as it was read (the
	// reflective walker keeps it too), so the two paths agree.
	got := AutoDictBuildIntKeys([]string{"010", "k"}, []string{"a", "b"})
	if _, ok := got.Lookup("010"); !ok {
		t.Fatalf("a non-canonical key must stay verbatim: %v", dictContent(got))
	}
	both := AutoDictBuildIntKeys([]string{"10", "\x01i10"}, []string{"a", "b"})
	if both.dictLen() != 2 {
		t.Fatalf("one key in both forms keeps both entries: %v", dictContent(both))
	}
}

// A Dict Int at every depth the walker reads by declared type: in a List, a
// Maybe, a tuple, and a custom type's payload (read by its Go type, then
// converted by the payload field's tag).
type dkShape interface {
	SkyVariantTag() int
	SkyVariantName() string
	isDkShape()
}

type dkShape_Circle_V struct {
	V0 SkyDict[string] `sky:"v0,rt.SkyDict[@i:string]"`
}
type dkShape_Lines_V struct {
	V0 []SkyMaybe[SkyDict[int]] `sky:"v0,[]rt.SkyMaybe[rt.SkyDict[@i:int]]"`
}

func (dkShape_Circle_V) SkyVariantTag() int     { return 0 }
func (dkShape_Circle_V) SkyVariantName() string { return "Circle" }
func (dkShape_Circle_V) isDkShape()             {}
func (dkShape_Lines_V) SkyVariantTag() int      { return 1 }
func (dkShape_Lines_V) SkyVariantName() string  { return "Lines" }
func (dkShape_Lines_V) isDkShape()              {}

func init() {
	RegisterAdtVariant("rt.dkShape", "Circle", func([]JsonRawMessage) any { return dkShape_Circle_V{} })
	RegisterAdtVariant("rt.dkShape", "Lines", func([]JsonRawMessage) any { return dkShape_Lines_V{} })
}

type dictKeyDeep struct {
	Pair   T2[int, SkyDict[string]]  `sky:"pair,rt.T2[int, rt.SkyDict[@i:string]]"`
	Lists  []SkyDict[int]            `sky:"lists,[]rt.SkyDict[@i:int]"`
	Maybe  SkyMaybe[SkyDict[string]] `sky:"maybe,rt.SkyMaybe[rt.SkyDict[@i:string]]"`
	Nested SkyDict[SkyDict[string]]  `sky:"nested,rt.SkyDict[@i:rt.SkyDict[@i:string]]"`
	Shape  dkShape                   `sky:"shape,dkShape"`
	Lines  dkShape                   `sky:"lines,dkShape"`
}

func dkInt(pairs ...any) SkyDict[int] {
	var xs []any
	for i := 0; i < len(pairs); i += 2 {
		xs = append(xs, SkyTuple2{V0: pairs[i], V1: pairs[i+1]})
	}
	return AsMapT[int](Dict_fromList(xs))
}

func dkStr(pairs ...any) SkyDict[string] {
	var xs []any
	for i := 0; i < len(pairs); i += 2 {
		xs = append(xs, SkyTuple2{V0: pairs[i], V1: pairs[i+1]})
	}
	return AsMapT[string](Dict_fromList(xs))
}

func dictKeyDeepSample() dictKeyDeep {
	return dictKeyDeep{
		Pair:   T2[int, SkyDict[string]]{V0: 1, V1: dkStr(3, "c")},
		Lists:  []SkyDict[int]{dkInt(30, 1), dkInt(40, 2)},
		Maybe:  Just[SkyDict[string]](dkStr(7, "seven")),
		Nested: AsMapT[SkyDict[string]](Dict_fromList([]any{SkyTuple2{V0: 8, V1: dkStr(9, "nine")}})),
		Shape:  dkShape_Circle_V{V0: dkStr(5, "five")},
		Lines:  dkShape_Lines_V{V0: []SkyMaybe[SkyDict[int]]{Just[SkyDict[int]](dkInt(6, 0))}},
	}
}

// dkSumKeys adds every key of every Dict in `d` as a key-generic helper reads
// it (Dict.keys with no key type): an Int key that came back as a String
// panics here, as it would in the program.
func dkSumKeys(t *testing.T, d dictKeyDeep) int {
	t.Helper()
	sum := 0
	add := func(dict any) {
		for _, k := range AsList(Dict_keys(dict)) {
			n, ok := k.(int)
			if !ok {
				t.Fatalf("a key came back as %T %#v, not an Int", k, k)
			}
			sum += n
		}
	}
	add(d.Pair.V1)
	for _, x := range d.Lists {
		add(x)
	}
	add(d.Maybe.JustValue)
	add(d.Nested)
	for _, x := range AsList(Dict_values(d.Nested)) {
		add(x)
	}
	add(d.Shape.(dkShape_Circle_V).V0)
	add(d.Lines.(dkShape_Lines_V).V0[0].JustValue)
	return sum
}

func TestCodecAutoReadsIntKeysAtEveryDepth(t *testing.T) {
	var generic any
	text := dictKeyEncode(t, dictKeyDeepSample())
	if strings.Contains(text, `"\u0001i`) {
		t.Fatalf("the internal key form leaked: %s", text)
	}
	old := text
	for _, n := range []string{"3", "30", "40", "7", "8", "9", "5", "6"} {
		old = strings.ReplaceAll(old, `"`+n+`":`, `"\u0001i`+n+`":`)
	}
	if old == text {
		t.Fatalf("no key was rewritten: %s", text)
	}
	want := 3 + 30 + 40 + 7 + 8 + 9 + 5 + 6
	for _, doc := range []string{text, old} {
		if err := json.Unmarshal([]byte(doc), &generic); err != nil {
			t.Fatal(err)
		}
		back, err := codecAutoDecodeVal(reflect.TypeOf(dictKeyDeep{}), generic, false)
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		got := back.Interface().(dictKeyDeep)
		if sum := dkSumKeys(t, got); sum != want {
			t.Fatalf("key sum %d, want %d", sum, want)
		}
		if !deepEq(got, dictKeyDeepSample()) {
			t.Fatalf("decoded %#v", got)
		}
		if again := dictKeyEncode(t, got); again != text {
			t.Fatalf("re-encode = %s, want %s", again, text)
		}
	}
}
