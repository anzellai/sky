package main

// Differential test of the generated typed Codec.auto codecs of this compiled
// program against the reflective walker (rt.AutoCodecUseReference). Copied
// into sky-out/ by rust/crates/sky/tests/auto_codec_flow.rs after the build.

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"sky-app/rt"
)

func reference[T any](f func() T) (out T) {
	rt.AutoCodecUseReference(true)
	defer rt.AutoCodecUseReference(false)
	return f()
}

type encoded struct {
	tree  any
	text  string
	panic string
}

func encodeWith(snake bool, v any) (e encoded) {
	defer func() {
		if r := recover(); r != nil {
			e.panic = fmt.Sprint(r)
		}
	}()
	e.tree = rt.Codec_autoEnc(snake, v)
	e.text = rt.JsonEnc_encode(0, e.tree).(string)
	return e
}

func decodeWith(snake bool, doc string) (string, any) {
	res := rt.JsonDec_decodeString(rt.Codec_autoDecoder(snake, Main_Model_R{}), doc)
	r := res.(rt.SkyResult[any, any])
	if r.Tag != 0 {
		return fmt.Sprint(r.ErrValue), nil
	}
	return "", r.OkValue
}

// same is reflect.DeepEqual with a NaN equal to itself.
func same(a, b any) bool {
	return reflect.DeepEqual(a, b) || fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}

var strs = []string{"", "a", "<b>&\"x\"", "  ", "é", "\x01\x1f", string([]byte{0xff, 'a'}), "naïve 🌍", `\`, "</script>"}
var floats = []float64{0, math.Copysign(0, -1), 1, 1.5, 1e21, 1e20, 1e-7, 1e-6, -3.25, 123456789.125, math.MaxFloat64, math.SmallestNonzeroFloat64}

func randStr(r *rand.Rand) string { return strs[r.Intn(len(strs))] }

func randItem(r *rand.Rand) Main_Item_R {
	it := Main_Item_R{Title: randStr(r), Done: r.Intn(2) == 0, Count: r.Intn(4000) - 2000,
		Score: floats[r.Intn(len(floats))], Color: Main_Color(r.Intn(3)), PriceMinor: r.Int() - r.Int()}
	if r.Intn(2) == 0 {
		it.Note = rt.Just(randStr(r))
	} else {
		it.Note = rt.Nothing[string]()
	}
	for i := r.Intn(3); i > 0; i-- {
		it.Tags = append(it.Tags, randStr(r))
	}
	return it
}

func randModel(r *rand.Rand) Main_Model_R {
	m := Main_Model_R{UserName: randStr(r)}
	for i := r.Intn(4); i > 0; i-- {
		m.Items = append(m.Items, randItem(r))
	}
	var ik []string
	var iv []Main_Item_R
	for i := r.Intn(4); i > 0; i-- {
		ik, iv = append(ik, fmt.Sprintf("k%d%s", r.Intn(40), randStr(r))), append(iv, randItem(r))
	}
	m.ById = rt.AutoDictBuild(ik, iv)
	var nk, nv []string
	for i := r.Intn(4); i > 0; i-- {
		nk, nv = append(nk, fmt.Sprint(r.Intn(200)-100)), append(nv, randStr(r))
	}
	m.ByNum = rt.AutoDictBuild(nk, nv)
	switch r.Intn(3) {
	case 0:
		m.Nested = rt.Nothing[rt.SkyMaybe[int]]()
	case 1:
		m.Nested = rt.Just(rt.Nothing[int]())
	default:
		m.Nested = rt.Just(rt.Just(r.Intn(10)))
	}
	for i := r.Intn(4); i > 0; i-- {
		m.Colors = append(m.Colors, Main_Color(r.Intn(3)))
	}
	var sk []string
	var sv []float64
	for i := r.Intn(3); i > 0; i-- {
		sk, sv = append(sk, randStr(r)), append(sv, floats[r.Intn(len(floats))])
	}
	m.Scores = rt.AutoDictBuild(sk, sv)
	for i := r.Intn(3); i > 0; i-- {
		row := []int{}
		for j := r.Intn(3); j > 0; j-- {
			row = append(row, r.Intn(100))
		}
		m.Grid = append(m.Grid, row)
	}
	if r.Intn(2) == 0 {
		m.Pinned = rt.Just(randItem(r))
	} else {
		m.Pinned = rt.Nothing[Main_Item_R]()
	}
	return m
}

func mutations(doc string) []string {
	rep := func(old, new string) string { return strings.Replace(doc, old, new, 1) }
	return []string{
		doc,
		rep(`"done":true`, `"done":1`),
		rep(`"done":false`, `"done":null`),
		rep(`"count":`, `"count":1.0,"x":`),
		rep(`"count":`, `"count":99999999999999999999,"x":`),
		rep(`"count":`, `"count":-9223372036854775808,"x":`),
		rep(`"count":`, `"count":1e2,"x":`),
		rep(`"count":`, `"count":1.5,"x":`),
		rep(`"count":`, `"count":"1","x":`),
		rep(`"score":`, `"score":"0","x":`),
		rep(`"score":`, `"score":-0,"x":`),
		rep(`"score":`, `"score":1e400,"x":`),
		rep(`"color":"Red"`, `"color":"Purple"`),
		rep(`"color":"Green"`, `"color":1`),
		rep(`"title":`, `"title":5,"x":`),
		rep(`"title":`, `"title":"dup","title":`),
		rep(`"title":`, `"title":" <>&","x":`),
		rep(`"note":null`, ``),
		rep(`"note":`, `"note":[],"x":`),
		rep(`"tags":[`, `"tags":[null,`),
		rep(`"tags":`, `"tags":{},"x":`),
		rep(`"items":[`, `"items":[1,`),
		rep(`"user_name":`, `"x":`),
		rep(`"userName":`, `"x":`),
		rep(`"by_id":{`, `"by_id":{"zz":1,"aa":2,`),
		rep(`"by_num":{`, `"by_num":{"9":"n","1":6,`),
		rep(`"nested":`, `"nested":"x","y":`),
		rep(`"colors":[`, `"colors":["Green",-0,`),
		rep(`"scores":{`, `"scores":{"q":"1",`),
		rep(`"grid":[`, `"grid":[[1.5],`),
		rep(`"pinned":null`, `"pinned":{}`),
		rep(`"price_minor":`, `"price_minor":true,"x":`),
		rep(`"priceMinor":`, `"price_minor":1,"x":`),
		`null`, `[]`, `"x"`, `{}`, `1`,
	}
}

func checkEncode(t *testing.T, snake bool, m Main_Model_R) {
	t.Helper()
	want := reference(func() encoded { return encodeWith(snake, m) })
	got := encodeWith(snake, m)
	if got.panic != want.panic || !same(got.tree, want.tree) || got.text != want.text {
		t.Fatalf("encode differs (snake=%v):\n gen %#v %q %q\nwant %#v %q %q", snake, got.tree, got.text, got.panic, want.tree, want.text, want.panic)
	}
	// The generated bytes fall back exactly when the walker's text fails (a
	// NaN or infinite Float), and are the walker's text otherwise.
	fast := rt.Codec_autoJsonT(snake, m)
	if (fast.Tag != 0) != (want.panic != "") || (fast.Tag == 0 && fast.JustValue != want.text) {
		t.Fatalf("generated bytes differ (snake=%v):\n gen %v %s\nwant %s %q", snake, fast.Tag, fast.JustValue, want.text, want.panic)
	}
}

func checkDecode(t *testing.T, snake bool, doc string) {
	t.Helper()
	type result struct {
		e string
		v any
	}
	w := reference(func() result { e, v := decodeWith(snake, doc); return result{e, v} })
	wantErr, want := w.e, w.v
	gotErr, got := decodeWith(snake, doc)
	if gotErr != wantErr || !same(got, want) {
		t.Fatalf("decode of %.300s differs (snake=%v):\n gen %#v %q\nwant %#v %q", doc, snake, got, gotErr, want, wantErr)
	}
}

func TestGeneratedCodecsAreRegistered(t *testing.T) {
	if r := rt.AutoCodecRefusals(); len(r) != 0 {
		t.Fatalf("refused registrations: %q", r)
	}
	for _, snake := range []bool{true, false} {
		if !rt.AutoCodecRegistered(Main_Model_R{}, snake) {
			t.Fatalf("Main_Model_R (snake=%v) has no generated codec", snake)
		}
	}
	if rt.AutoCodecRegistered(Main_Mixed_R{}, true) {
		t.Fatalf("Main_Mixed_R holds a tuple and must keep the reflective path")
	}
}

func TestGeneratedCodecsMatchTheWalker(t *testing.T) {
	r := rand.New(rand.NewSource(20261110))
	for i := 0; i < 1500; i++ {
		m := randModel(r)
		for _, snake := range []bool{true, false} {
			checkEncode(t, snake, m)
			text := reference(func() encoded { return encodeWith(snake, m) }).text
			if text == "" {
				continue
			}
			for _, doc := range mutations(text) {
				checkDecode(t, snake, doc)
			}
		}
	}
}

func TestGeneratedCodecsNaNAndInfinity(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		m := Main_Model_R{Items: []Main_Item_R{{Score: f, Note: rt.Nothing[string]()}},
			Nested: rt.Nothing[rt.SkyMaybe[int]](), Pinned: rt.Nothing[Main_Item_R]()}
		if fast := rt.Codec_autoJsonT(true, m); fast.Tag == 0 {
			t.Fatalf("%v: generated bytes did not fall back: %s", f, fast.JustValue)
		}
		want := reference(func() encoded { return encodeWith(true, m) })
		got := encodeWith(true, m)
		if got.panic != want.panic || got.text != want.text {
			t.Fatalf("%v: %q %q, want %q %q", f, got.text, got.panic, want.text, want.panic)
		}
	}
}

func FuzzGeneratedDecode(f *testing.F) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 20; i++ {
		m := randModel(r)
		text := reference(func() encoded { return encodeWith(true, m) }).text
		for _, doc := range mutations(text)[:6] {
			f.Add(doc, true)
		}
	}
	f.Fuzz(func(t *testing.T, doc string, snake bool) {
		checkDecode(t, snake, doc)
	})
}
