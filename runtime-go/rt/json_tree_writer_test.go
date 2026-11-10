package rt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

// ── The oracle: phase 2 exactly as it was before the single-pass writer ──
//
// jsonTreeRefObject carries the previous jsonOrderedObject.MarshalJSON body
// verbatim. jsonTreeReference converts a tree so every object uses it, then
// calls json.Marshal: that is the old phase 2, byte for byte and error for
// error (the error names the type, so the reference type name is mapped back).

type jsonTreeRefObject struct {
	keys []string
	vals []any
}

func (o jsonTreeRefObject) MarshalJSON() ([]byte, error) {
	out := make([]byte, 1, 32)
	out[0] = '{'
	for i, k := range o.keys {
		if i > 0 {
			out = append(out, ',')
		}
		if encoded, ok := codecAutoSchemaKeys.Load(k); ok {
			out = append(out, encoded.(string)...)
		} else {
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
		}
		out = append(out, ':')
		value := o.vals[i]
		switch v := value.(type) {
		case int64:
			out = strconv.AppendInt(out, v, 10)
			continue
		case bool:
			out = strconv.AppendBool(out, v)
			continue
		case nil:
			out = append(out, "null"...)
			continue
		case string:
			if codecJSONSafeASCII(v) {
				out = append(out, '"')
				out = append(out, v...)
				out = append(out, '"')
				continue
			}
		}
		vb, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		out = append(out, vb...)
	}
	out = append(out, '}')
	return out, nil
}

func jsonTreeToRef(v any) any { return jsonTreeToRefAt(v, 0) }

// A cyclic tree is left as it is past 2000 levels, so json.Marshal reports
// the cycle as before.
func jsonTreeToRefAt(v any, depth int) any {
	if depth > 2000 {
		return v
	}
	switch x := v.(type) {
	case jsonOrderedObject:
		vals := make([]any, len(x.vals))
		for i, e := range x.vals {
			vals[i] = jsonTreeToRefAt(e, depth+1)
		}
		return jsonTreeRefObject{keys: x.keys, vals: vals}
	case []any:
		if x == nil {
			return x
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonTreeToRefAt(e, depth+1)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonTreeToRefAt(e, depth+1)
		}
		return out
	}
	return v
}

func jsonTreeErrText(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(err.Error(), "rt.jsonTreeRefObject", "rt.jsonOrderedObject")
}

// jsonTreeReference is the old phase 2: json.Marshal / MarshalIndent of the tree.
func jsonTreeReference(v any, indent int) ([]byte, string) {
	ref := jsonTreeToRef(v)
	var b []byte
	var err error
	if indent > 0 {
		b, err = json.MarshalIndent(ref, "", strings.Repeat(" ", indent))
	} else {
		b, err = json.Marshal(ref)
	}
	return b, jsonTreeErrText(err)
}

func jsonTreeCheckSame(t *testing.T, label string, tree any) {
	t.Helper()
	for _, indent := range []int{0, 2} {
		want, wantErr := jsonTreeReference(tree, indent)
		got, err := jsonEncodeTree(tree, indent)
		if gotErr := jsonTreeErrText(err); gotErr != wantErr || !bytes.Equal(got, want) {
			t.Fatalf("%s indent=%d:\n got %q (%s)\nwant %q (%s)", label, indent, got, gotErr, want, wantErr)
		}
	}
	// json.Marshal on the tree (encoding/json calling the new MarshalJSON at
	// the root) must also stay identical, for callers that marshal directly.
	want, wantErr := jsonTreeReference(tree, 0)
	got, err := json.Marshal(tree)
	if gotErr := jsonTreeErrText(err); gotErr != wantErr || !bytes.Equal(got, want) {
		t.Fatalf("%s via json.Marshal:\n got %q (%s)\nwant %q (%s)", label, got, gotErr, want, wantErr)
	}
}

// ── Random trees of every phase-1 node type, plus foreign values ──

type jsonTreeRand struct{ s uint64 }

func (r *jsonTreeRand) next() uint64 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return r.s
}

type jsonTreeNamedString string

type jsonTreeMarshaler struct{ text string }

func (m jsonTreeMarshaler) MarshalJSON() ([]byte, error) { return []byte(m.text), nil }

var jsonTreeStrings = []string{
	"", "a", "plain ascii", "quote\"in", "back\\slash", "<html>&amp;", "tab\there",
	"new\nline\r", "naïve ünïcode", "\u2028line\u2029sep", string([]byte{0xff, 0xfe}),
	"\x00ctl\x1f", "bell\b\f", "emoji \U0001F600", "del\x7f", "e\u0301", "a\xc3",
	strings.Repeat("x", 64), strings.Repeat("y", 65), strings.Repeat("é<", 40),
	string([]byte{0xed, 0xa0, 0x80}), "\xe2\x80", "😀\xe2\x80\xa8",
}

var jsonTreeFloats = []float64{
	0, math.Copysign(0, -1), 1.5, -2.25, 1e-7, 1e-6, 9.99999e-7, 1e20, 1e21, 123456789.125,
	-3.25e-9, 5e300, math.SmallestNonzeroFloat64, math.MaxFloat64, 0.1, 1.0 / 3.0, 100, 1e-100,
}

func (r *jsonTreeRand) str() string {
	if r.next()%4 == 0 {
		// Random bytes: any mix of control, HTML, multi-byte and invalid UTF-8.
		n := int(r.next() % 12)
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.next())
		}
		return string(b)
	}
	return jsonTreeStrings[r.next()%uint64(len(jsonTreeStrings))]
}

func (r *jsonTreeRand) tree(depth, maxDepth int) any {
	k := r.next() % 16
	if depth >= maxDepth {
		k %= 6
	}
	switch k {
	case 0:
		return r.str()
	case 1:
		return int64(r.next()) >> (r.next() % 64)
	case 2:
		return jsonTreeFloats[r.next()%uint64(len(jsonTreeFloats))]
	case 3:
		return r.next()%2 == 0
	case 4:
		return nil
	case 5:
		switch r.next() % 7 {
		case 0:
			return codecJustNull{}
		case 1:
			return json.RawMessage(` {"raw": [1, 2 ,3], "h": "<&>"} `)
		case 2:
			return []any(nil)
		case 3:
			return jsonTreeNamedString("named <" + r.str())
		case 4:
			return int(r.next() % 1000)
		case 5:
			return jsonTreeMarshaler{text: `{"m" : [ 1 , "<" ]}`}
		default:
			return float32(1.25)
		}
	case 6, 7, 8, 9:
		n := int(r.next() % 6)
		xs := make([]any, n)
		for i := range xs {
			xs[i] = r.tree(depth+1, maxDepth)
		}
		return xs
	case 10:
		return map[string]any{"z": r.tree(depth+1, maxDepth), "a<": r.tree(depth+1, maxDepth)}
	default:
		n := int(r.next() % 6)
		o := jsonOrderedObject{}
		for i := 0; i < n; i++ {
			key := r.str()
			if r.next()%3 == 0 {
				key = "id" // a registered schema key: its cached encoding is used
			}
			o.keys = append(o.keys, key)
			o.vals = append(o.vals, r.tree(depth+1, maxDepth))
		}
		return o
	}
}

func TestJsonTreeWriterMatchesMarshalOnFixtures(t *testing.T) {
	codecRegisterSchemaKey("id")
	deep := any("leaf")
	for i := 0; i < 300; i++ {
		if i%2 == 0 {
			deep = []any{deep, int64(i)}
		} else {
			deep = jsonOrderedObject{keys: []string{"d" + strconv.Itoa(i)}, vals: []any{deep}}
		}
	}
	runOfArrays := any("bottom")
	for i := 0; i < 1500; i++ { // past the 1000-level cycle-detection run
		runOfArrays = []any{runOfArrays}
	}
	cases := map[string]any{
		"empty object":   jsonOrderedObject{},
		"empty array":    []any{},
		"nil array":      []any(nil),
		"top string":     "<script>\u2028",
		"top float":      1e21,
		"floats":         []any{jsonTreeFloats[0], jsonTreeFloats[1], jsonTreeFloats[4], jsonTreeFloats[5], jsonTreeFloats[9], jsonTreeFloats[12]},
		"html and utf8":  jsonOrderedObject{keys: []string{"<k>&", string([]byte{0xff})}, vals: []any{"a&b<c>d", "x\xffy\u2029"}},
		"nested":         jsonOrderedObject{keys: []string{"id", "xs"}, vals: []any{int64(-9), []any{jsonOrderedObject{keys: []string{"t"}, vals: []any{true}}, nil}}},
		"foreign values": []any{codecJustNull{}, json.RawMessage("[ 1 , 2 ]"), jsonTreeNamedString("n"), map[string]any{"b": 1, "a": jsonOrderedObject{keys: []string{"q"}, vals: []any{"<"}}}},
		"deep":           deep,
		"array run":      runOfArrays,
		"long string":    strings.Repeat("abc", 100000),
		"long unicode":   strings.Repeat("é€😀", 20000) + "\n",
	}
	for name, tree := range cases {
		jsonTreeCheckSame(t, name, tree)
	}
}

func TestJsonTreeWriterKeepsErrorsAndTheirWrapping(t *testing.T) {
	cyc := make([]any, 1)
	cyc[0] = cyc
	selfMap := map[string]any{}
	selfMap["self"] = selfMap
	failing := codecObjectPreallocFX{err: errors.New("synthetic failure")}
	cases := map[string]any{
		"nan at root":       math.NaN(),
		"inf in object":     jsonOrderedObject{keys: []string{"x"}, vals: []any{math.Inf(1)}},
		"nan two deep":      jsonOrderedObject{keys: []string{"a"}, vals: []any{[]any{jsonOrderedObject{keys: []string{"b"}, vals: []any{math.NaN()}}}}},
		"nan after values":  []any{"ok", int64(1), jsonOrderedObject{keys: []string{"x", "y"}, vals: []any{"fine", math.Inf(-1)}}},
		"marshaler error":   jsonOrderedObject{keys: []string{"m"}, vals: []any{failing}},
		"cyclic array":      jsonOrderedObject{keys: []string{"c"}, vals: []any{cyc}},
		"cyclic map":        []any{selfMap},
		"invalid raw":       jsonOrderedObject{keys: []string{"r"}, vals: []any{json.RawMessage("{bad")}},
		"first error wins":  []any{jsonOrderedObject{keys: []string{"a"}, vals: []any{math.NaN()}}, failing},
		"error in map tree": map[string]any{"k": jsonOrderedObject{keys: []string{"n"}, vals: []any{math.NaN()}}},
	}
	for name, tree := range cases {
		_, wantErr := jsonTreeReference(tree, 0)
		if wantErr == "" {
			t.Fatalf("%s: the reference did not fail", name)
		}
		jsonTreeCheckSame(t, name, tree)
	}
	// The error chain keeps its types for errors.As callers.
	_, err := jsonMarshalTree(jsonOrderedObject{keys: []string{"a"}, vals: []any{math.NaN()}})
	var me *json.MarshalerError
	var ue *json.UnsupportedValueError
	if !errors.As(err, &me) || !errors.As(err, &ue) {
		t.Fatalf("error chain changed: %#v", err)
	}
}

func TestJsonTreeWriterEncodeKeepsClassifiedNaNFailure(t *testing.T) {
	tree := JsonValue{raw: jsonOrderedObject{keys: []string{"x"}, vals: []any{math.NaN()}}}
	defer func() {
		p := recover()
		msg := fmt.Sprint(p)
		_, wantErr := jsonTreeReference(tree.raw, 0)
		if msg != "rt.JsonEnc_encode: cannot encode the value as JSON: "+wantErr {
			t.Fatalf("panic changed: %q", msg)
		}
	}()
	JsonEnc_encode(0, tree)
	t.Fatal("NaN encoded")
}

// TestJsonTreeWriterSeededDifferential compares the writer with the old phase
// 2 on 25,000 random trees (and their indented forms).
func TestJsonTreeWriterSeededDifferential(t *testing.T) {
	codecRegisterSchemaKey("id")
	r := &jsonTreeRand{s: 0x9e3779b97f4a7c15}
	n := 25000
	if testing.Short() {
		n = 2000
	}
	for i := 0; i < n; i++ {
		tree := r.tree(0, 1+int(r.next()%7))
		jsonTreeCheckSame(t, "tree "+strconv.Itoa(i), tree)
		if i%7 == 0 {
			tree = jsonOrderedObject{keys: []string{"bad", "after"}, vals: []any{tree, math.NaN()}}
			jsonTreeCheckSame(t, "nan tree "+strconv.Itoa(i), tree)
		}
	}
}

// TestJsonTreeWriterCappedStopsWithSameErrors checks the byte limit: under the
// limit it gives the full bytes, over it gives none, it always reports the
// exact size, and either way
// it reaches the same error and calls the same Marshalers in the same order.
func TestJsonTreeWriterCappedStopsWithSameErrors(t *testing.T) {
	r := &jsonTreeRand{s: 77}
	for i := 0; i < 5000; i++ {
		tree := r.tree(0, 5)
		full, ferr := jsonMarshalTree(tree)
		for _, limit := range []int{0, 1, 7, len(full) - 1, len(full), len(full) + 3} {
			out, size, err := jsonMarshalTreeCapped(tree, limit)
			over := size > limit
			if jsonTreeErrText(err) != jsonTreeErrText(ferr) {
				t.Fatalf("tree %d limit %d: error %v, want %v", i, limit, err, ferr)
			}
			if ferr != nil {
				continue
			}
			if size != len(full) {
				t.Fatalf("tree %d limit %d: size %d for %d bytes", i, limit, size, len(full))
			}
			if !over && !bytes.Equal(out, full) {
				t.Fatalf("tree %d limit %d: bytes differ", i, limit)
			}
		}
	}
	// A NaN after the cut still fails, and every Marshaler still runs in order.
	var calls []string
	mk := func(name string) codecObjectPreallocFX {
		return codecObjectPreallocFX{effect: func() { calls = append(calls, name) }}
	}
	tree := jsonOrderedObject{keys: []string{"big", "m1", "m2", "bad"}, vals: []any{strings.Repeat("x", 1000), mk("m1"), mk("m2"), math.NaN()}}
	_, refErr := jsonTreeReference(tree, 0)
	refCalls := calls
	calls = nil
	_, size, err := jsonMarshalTreeCapped(tree, 10)
	if size != 0 || jsonTreeErrText(err) != refErr || fmt.Sprint(calls) != fmt.Sprint(refCalls) {
		t.Fatalf("capped walk changed failure or effects: size=%v err=%v calls=%v want %v %v", size, err, calls, refErr, refCalls)
	}
}

func TestJsonTreeWriterCappedDoesNotGrowPastLimit(t *testing.T) {
	blob := strings.Repeat("b", 8<<20)
	tree := jsonOrderedObject{keys: []string{"a", "blob", "z"}, vals: []any{"small", blob, int64(1)}}
	allocs := testing.AllocsPerRun(5, func() {
		out, size, err := jsonMarshalTreeCapped(tree, spaPersistMaxBytes)
		if out != nil || size != len(blob)+len(`{"a":"small","blob":"","z":1}`) || err != nil {
			panic("expected over cap with the exact size")
		}
	})
	// The writer never copies the 8 MB string: one small initial buffer only.
	if allocs > 2 {
		t.Fatalf("capped encode allocated %.0f times", allocs)
	}
}

func FuzzJsonTreeWriter(f *testing.F) {
	for _, seed := range []uint64{1, 2, 3, 12345, 0xdeadbeef} {
		f.Add(seed, "<a&b>\u2028", 1.5e-7)
	}
	f.Fuzz(func(t *testing.T, seed uint64, s string, x float64) {
		if seed == 0 {
			seed = 1
		}
		r := &jsonTreeRand{s: seed}
		tree := jsonOrderedObject{
			keys: []string{s, "v", "t"},
			vals: []any{s, x, r.tree(0, 4)},
		}
		jsonTreeCheckSame(t, "fuzz", tree)
		jsonTreeCheckSame(t, "fuzz array", []any{x, s, tree})
	})
}

// ── Benchmarks: phase 2 only, old versus new, on 1 MB and 10 MB trees ──

func jsonTreeBenchFixture(shape string, bytesWanted int) any {
	item := func(i int) any {
		return jsonOrderedObject{
			keys: []string{"id", "name", "email", "score", "active", "note", "tags", "parent"},
			vals: []any{int64(i), "item-" + strconv.Itoa(i), "user" + strconv.Itoa(i%9973) + "@example.test",
				float64(i%1000) + 0.25, i%3 == 0, "Synthetic note text for the large model benchmark, plain ASCII.",
				[]any{"alpha", "beta"}, nil},
		}
	}
	n := bytesWanted / 205
	root := jsonOrderedObject{keys: []string{"title", "count"}, vals: []any{"Large model", int64(n)}}
	switch shape {
	case "list":
		xs := make([]any, n)
		for i := range xs {
			xs[i] = item(i)
		}
		root.keys, root.vals = append(root.keys, "items"), append(root.vals, xs)
	case "dict":
		pairs := make([]any, n)
		for i := range pairs {
			pairs[i] = []any{fmt.Sprintf("k%08d", i), item(i)}
		}
		root.keys, root.vals = append(root.keys, "index"), append(root.vals, pairs)
	default:
		root.keys, root.vals = append(root.keys, "blob"), append(root.vals, strings.Repeat("blob text, ", bytesWanted/11))
	}
	return root
}

func BenchmarkJsonTreeEncode(b *testing.B) {
	for _, shape := range []string{"list", "dict", "blob"} {
		for _, mb := range []int{1, 10} {
			tree := jsonTreeBenchFixture(shape, mb<<20)
			ref := jsonTreeToRef(tree)
			name := fmt.Sprintf("%s/%dMB", shape, mb)
			b.Run(name+"/old", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					out, err := json.Marshal(ref)
					if err != nil {
						b.Fatal(err)
					}
					b.SetBytes(int64(len(out)))
				}
			})
			b.Run(name+"/new", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					out, err := jsonMarshalTree(tree)
					if err != nil {
						b.Fatal(err)
					}
					b.SetBytes(int64(len(out)))
				}
			})
			b.Run(name+"/cappedAtPersistCap", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, _, err := jsonMarshalTreeCapped(tree, spaPersistMaxBytes); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
