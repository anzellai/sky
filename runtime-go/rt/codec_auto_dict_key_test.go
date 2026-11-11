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
// that v0.29.8 and earlier wrote into saved models and stored rows.

type dictKeyRec struct {
	Name  string          `sky:"name,string"`
	ByNum SkyDict[string] `sky:"byNum,rt.SkyDict[string]"`
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
	if !deepEq(back.ByNum, want.ByNum) || !deepEq(want.ByNum, back.ByNum) {
		t.Fatalf("the decoded Dict must equal the built one both ways")
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
