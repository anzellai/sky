package rt

import (
	"strings"
	"testing"
)

// A JSON object that repeats a key is refused at the parse boundary
// (jsonParseDocument), so every decoder sees one rule: Json.Decode, Codec,
// Codec.auto, a Sky.Spa payload and a Std.Db.Store JSON column. Before
// v0.31.0 the parse kept the last value and dropped the others silently.

func TestJsonDuplicateKeys_Scanner(t *testing.T) {
	cases := []struct {
		doc  string
		want string // "" = no duplicate
	}{
		{`{}`, ""},
		{`[1,2,"{"]`, ""},
		{`{"a":1,"b":{"a":2},"c":[{"a":3},{"a":4}]}`, ""},
		{`{"a":1,"a":2}`, `duplicate key "a" in the top-level object`},
		{`{"a":{"b":[0,{"x":1,"x":2}]}}`, `duplicate key "x" in the object at .a.b[1]`},
		{`{"x y":{"q":1,"q":2}}`, `duplicate key "q" in the object at ["x y"]`},
		{`{"a":1,"a":2}`, `duplicate key "a" in the top-level object`},
		{`{"a\"b":1,"a\"b":2}`, `duplicate key "a\"b" in the top-level object`},
		{`{"s":"a\\","s2":"}","s":0}`, `duplicate key "s" in the top-level object`},
		{`{ "a" : 1 , "b" : 2 , "a" : 3 }`, `duplicate key "a" in the top-level object`},
	}
	for _, c := range cases {
		// The scanner runs on text the parser accepted.
		if _, err := jsonParseDocument(c.doc); err != nil && c.want == "" {
			t.Fatalf("%s: unexpected parse error %v", c.doc, err)
		}
		err := jsonCheckDuplicateKeys(c.doc)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected %v", c.doc, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", c.doc, err, c.want)
		}
	}
}

func TestJsonDuplicateKeys_ManyKeysUseTheSet(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"k` + itoaTest(i) + `":1`)
	}
	ok := b.String() + "}"
	if err := jsonCheckDuplicateKeys(ok); err != nil {
		t.Fatalf("100 distinct keys: %v", err)
	}
	dup := b.String() + `,"k42":2}`
	if err := jsonCheckDuplicateKeys(dup); err == nil || !strings.Contains(err.Error(), `"k42"`) {
		t.Fatalf("repeat after 100 keys: got %v", err)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for ; i > 0; i /= 10 {
		d = append([]byte{byte('0' + i%10)}, d...)
	}
	return string(d)
}

func TestJsonDuplicateKeys_DeepDocumentNoRecursion(t *testing.T) {
	doc := strings.Repeat(`{"a":`, 9000) + `{"z":1,"z":2}` + strings.Repeat("}", 9000)
	if err := jsonCheckDuplicateKeys(doc); err == nil {
		t.Fatal("a repeat 9000 levels down was not found")
	}
}

func TestJsonDuplicateKeys_DecodeString(t *testing.T) {
	r := JsonDec_decodeString(JsonDec_succeed(0), `{"a":1,"a":2}`)
	if !isErr(r) || !strings.Contains(extractErrMsg(r), `duplicate key "a"`) {
		t.Fatalf("decodeString: got %v", r)
	}
}

func TestJsonDuplicateKeys_DecodeValueOrderedObject(t *testing.T) {
	inner := JsonEnc_object([]any{SkyTuple2{V0: "k", V1: JsonEnc_int(1)}, SkyTuple2{V0: "k", V1: JsonEnc_int(2)}})
	outer := JsonEnc_object([]any{SkyTuple2{V0: "o", V1: JsonEnc_list([]any{inner})}})
	r := JsonDec_decodeValue(JsonDec_value(), outer)
	if msg := extractErrMsg(r); !isErr(r) || !strings.Contains(msg, `duplicate key "k" in the object at .o[0]`) {
		t.Fatalf("decodeValue: got %v", r)
	}
}

func TestJsonDuplicateKeys_StoreBlobColumn(t *testing.T) {
	_, err := rowValToJsonRaw(`{"9":1,"9":2}`, true, "blob", "counts")
	if err == nil || !strings.Contains(err.Error(), `duplicate key "9"`) {
		t.Fatalf("blob column: got %v", err)
	}
}

func TestJsonDec_KeyValuePairs_SortedAndNamed(t *testing.T) {
	r := JsonDec_decodeString(JsonDec_keyValuePairs(JsonDec_int()), `{"b":2,"a":1,"B":3}`)
	if isErr(r) {
		t.Fatal(extractErrMsg(r))
	}
	got := okVal(r).([]any)
	want := []string{"B", "a", "b"}
	for i, p := range got {
		if p.(SkyTuple2).V0 != want[i] {
			t.Fatalf("order: got %v", got)
		}
	}
	bad := JsonDec_decodeString(JsonDec_field("s", JsonDec_keyValuePairs(JsonDec_list(JsonDec_int()))), `{"s":{"a: b":[true]}}`)
	if msg := extractErrMsg(bad); msg != `at .s["a: b"][0]: expected Int, got Boolean` {
		t.Fatalf("path: got %q", msg)
	}
}

func TestJsonDec_Dict(t *testing.T) {
	r := JsonDec_decodeString(JsonDec_dict(JsonDec_int()), `{"a":1,"b":2}`)
	if isErr(r) {
		t.Fatal(extractErrMsg(r))
	}
	if n := AsInt(Dict_size(okVal(r))); n != 2 {
		t.Fatalf("size %d", n)
	}
	if r := JsonDec_decodeString(JsonDec_dict(JsonDec_int()), `[1]`); !isErr(r) {
		t.Fatal("an array read as a dict")
	}
}

// Decode.lazy recursion is as deep as the JSON, which the parser bounds at
// 10000; at that depth it must not overflow the stack or panic.
func TestJsonDec_Lazy_DeepInput(t *testing.T) {
	var depth JsonDecoder
	depth = JsonDecoder{run: func(v any) any {
		arr, ok := v.([]any)
		if !ok || len(arr) == 0 {
			return Ok[any, any](0)
		}
		r := JsonDec_lazy(func(_ any) any { return depth }).(JsonDecoder).run(arr[0])
		if isErr(r) {
			return r
		}
		return Ok[any, any](AsInt(okVal(r)) + 1)
	}}
	doc := strings.Repeat("[", 10000) + strings.Repeat("]", 10000)
	r := JsonDec_decodeString(depth, doc)
	if isErr(r) || AsInt(okVal(r)) != 9999 {
		t.Fatalf("10000 levels: got %v", r)
	}
	deeper := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	if r := JsonDec_decodeString(depth, deeper); !isErr(r) {
		t.Fatal("10001 levels parsed")
	}
	if r := JsonDec_decodeString(JsonDec_lazy(func(_ any) any { return 3 }), `1`); !isErr(r) {
		t.Fatal("a thunk that gives no decoder was not an Err")
	}
}

func TestJsonDec_DecodeValue_DepthLimit(t *testing.T) {
	var v any = []any{}
	for i := 0; i < 10001; i++ {
		v = []any{v}
	}
	r := JsonDec_decodeValue(JsonDec_value(), JsonValue{raw: v})
	if !isErr(r) || !strings.Contains(extractErrMsg(r), "deeper than 10000") {
		t.Fatalf("got %v", r)
	}
}

func TestJsonEnc_Dict_SortedAndCollisionKept(t *testing.T) {
	d := Dict_fromList([]any{SkyTuple2{V0: 9, V1: "n"}, SkyTuple2{V0: 10, V1: "t"}})
	got := JsonEnc_encode(0, JsonEnc_dict(func(k any) any { return itoaTest(AsInt(k)) }, JsonEnc_string, d))
	if got != `{"10":"t","9":"n"}` {
		t.Fatalf("got %v", got)
	}
	d2 := Dict_fromList([]any{SkyTuple2{V0: "A", V1: 1}, SkyTuple2{V0: "a", V1: 2}})
	v := JsonEnc_dict(func(k any) any { return strings.ToLower(k.(string)) }, JsonEnc_int, d2)
	if s := JsonEnc_encode(0, v); s != `{"a":1,"a":2}` {
		t.Fatalf("collision: got %v", s)
	}
	if r := JsonDec_decodeValue(JsonDec_dict(JsonDec_int()), v); !isErr(r) {
		t.Fatal("a collision decoded")
	}
}

func TestJsonPrefixError_QuotedSegment(t *testing.T) {
	inner := Err[any, any](ErrDecode(`at ["a: b"]: expected Int, got String`))
	got := extractErrMsg(jsonPrefixError(inner, ".s"))
	if got != `at .s["a: b"]: expected Int, got String` {
		t.Fatalf("got %q", got)
	}
}
