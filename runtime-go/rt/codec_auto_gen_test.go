package rt

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// Hand-written codecs in the exact shape the compiler emits
// (rust/crates/lower/src/lower/auto_codec.rs), registered in init() as a
// generated program registers them. The tests prove registration, dispatch
// and that tree, bytes, value and error equal the reflective walker's.

type acgColor int

func (v acgColor) SkyEnumName() string { return EnumName("acgColor", int(v)) }

type acgItem struct {
	Title string           `sky:"title,string"`
	Done  bool             `sky:"done,bool"`
	Count int              `sky:"count,int"`
	Score float64          `sky:"score,float64"`
	Color acgColor         `sky:"color,acgColor"`
	Note  SkyMaybe[string] `sky:"note,rt.SkyMaybe[string]"`
	Tags  []string         `sky:"tags,[]string"`
}

type acgModel struct {
	Items    []acgItem               `sky:"items,[]acgItem"`
	ById     SkyDict[acgItem]        `sky:"byId,rt.SkyDict[acgItem]"`
	ByNum    SkyDict[string]         `sky:"byNum,rt.SkyDict[string]"`
	UserName string                  `sky:"userName,string"`
	Nested   SkyMaybe[SkyMaybe[int]] `sky:"nested,rt.SkyMaybe[rt.SkyMaybe[int]]"`
	Colors   []acgColor              `sky:"colors,[]acgColor"`
}

// acgBad has a field outside the closed set.
type acgBad struct {
	Data any `sky:"data,any"`
}

func init() {
	RegisterEnum("acgColor", []string{"Red", "Green"})
	for _, snake := range []bool{true, false} {
		keys := acgKeys(snake)
		RegisterAutoCodec[acgModel](snake, []AutoCodecShape{
			AutoShape[acgModel](keys.model, []string{"[]acgItem", "rt.SkyDict[acgItem]", "rt.SkyDict[string]", "string", "rt.SkyMaybe[rt.SkyMaybe[int]]", "[]acgColor"}),
			AutoShape[acgItem](keys.item, []string{"string", "bool", "int", "float64", "acgColor", "rt.SkyMaybe[string]", "[]string"}),
		}, acgTreeModel(keys), acgAppModel(keys), acgDecModel(keys))
	}
}

type acgKeyset struct{ model, item []string }

func acgKeys(snake bool) acgKeyset {
	if snake {
		return acgKeyset{[]string{"items", "by_id", "by_num", "user_name", "nested", "colors"}, []string{"title", "done", "count", "score", "color", "note", "tags"}}
	}
	return acgKeyset{[]string{"items", "byId", "byNum", "userName", "nested", "colors"}, []string{"title", "done", "count", "score", "color", "note", "tags"}}
}

// ── tree ─────────────────────────────────────────────────────────────────────

func acgTreeItem(k acgKeyset) func(acgItem, *AutoCodecCtx) (any, error) {
	return func(v acgItem, c *AutoCodecCtx) (any, error) {
		var note any
		if v.Note.Tag != 0 {
			note = nil
		} else {
			var err error
			if note, err = AutoJust(v.Note.JustValue, nil); err != nil {
				return nil, err
			}
		}
		tags := make([]any, len(v.Tags))
		for i, e := range v.Tags {
			tags[i] = e
		}
		return AutoObj(append([]string(nil), k.item...), []any{v.Title, v.Done, int64(v.Count), v.Score, c.Enum("acgColor", int64(v.Color)), note, tags}), nil
	}
}

func acgTreeModel(k acgKeyset) func(acgModel, *AutoCodecCtx) (any, error) {
	item := acgTreeItem(k)
	return func(v acgModel, c *AutoCodecCtx) (any, error) {
		items := make([]any, len(v.Items))
		for i, e := range v.Items {
			x, err := item(e, c)
			if err != nil {
				return nil, err
			}
			items[i] = x
		}
		bk, bv := AutoDictSorted(v.ById)
		byID := make([]any, len(bv))
		for i, e := range bv {
			x, err := item(e, c)
			if err != nil {
				return nil, err
			}
			byID[i] = x
		}
		nk, nv := AutoDictSorted(v.ByNum)
		byNum := make([]any, len(nv))
		for i, e := range nv {
			byNum[i] = e
		}
		var nested any
		if v.Nested.Tag == 0 {
			var inner any
			if v.Nested.JustValue.Tag == 0 {
				var err error
				if inner, err = AutoJust(int64(v.Nested.JustValue.JustValue), nil); err != nil {
					return nil, err
				}
			}
			var err error
			if nested, err = AutoJust(inner, nil); err != nil {
				return nil, err
			}
		}
		colors := make([]any, len(v.Colors))
		for i, e := range v.Colors {
			colors[i] = c.Enum("acgColor", int64(e))
		}
		return AutoObj(append([]string(nil), k.model...), []any{items, AutoObj(bk, byID), AutoObj(nk, byNum), v.UserName, nested, colors}), nil
	}
}

// ── bytes ────────────────────────────────────────────────────────────────────

func acgAppItem(k acgKeyset) func([]byte, acgItem, *AutoCodecCtx) ([]byte, error) {
	return func(b []byte, v acgItem, c *AutoCodecCtx) ([]byte, error) {
		var err error
		b = append(b, `{"`+k.item[0]+`":`...)
		b = AutoAppendString(b, v.Title)
		b = append(b, `,"`+k.item[1]+`":`...)
		b = AutoAppendBool(b, v.Done)
		b = append(b, `,"`+k.item[2]+`":`...)
		b = AutoAppendInt(b, int64(v.Count))
		b = append(b, `,"`+k.item[3]+`":`...)
		if b, err = AutoAppendFloat(b, v.Score); err != nil {
			return nil, err
		}
		b = append(b, `,"`+k.item[4]+`":`...)
		b = c.AppendEnum(b, "acgColor", int64(v.Color))
		b = append(b, `,"`+k.item[5]+`":`...)
		if v.Note.Tag != 0 {
			b = append(b, "null"...)
		} else {
			b = AutoAppendString(b, v.Note.JustValue)
		}
		b = append(b, `,"`+k.item[6]+`":[`...)
		for i, e := range v.Tags {
			if i > 0 {
				b = append(b, ',')
			}
			b = AutoAppendString(b, e)
		}
		return append(b, "]}"...), nil
	}
}

func acgAppModel(k acgKeyset) func([]byte, acgModel, *AutoCodecCtx) ([]byte, error) {
	item := acgAppItem(k)
	return func(b []byte, v acgModel, c *AutoCodecCtx) ([]byte, error) {
		var err error
		b = append(b, `{"`+k.model[0]+`":[`...)
		for i, e := range v.Items {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = item(b, e, c); err != nil {
				return nil, err
			}
		}
		b = append(b, `],"`+k.model[1]+`":{`...)
		bk, bv := AutoDictSorted(v.ById)
		for i, e := range bv {
			if i > 0 {
				b = append(b, ',')
			}
			b = AutoAppendString(b, bk[i])
			b = append(b, ':')
			if b, err = item(b, e, c); err != nil {
				return nil, err
			}
		}
		b = append(b, `},"`+k.model[2]+`":{`...)
		nk, nv := AutoDictSorted(v.ByNum)
		for i, e := range nv {
			if i > 0 {
				b = append(b, ',')
			}
			b = AutoAppendString(b, nk[i])
			b = append(b, ':')
			b = AutoAppendString(b, e)
		}
		b = append(b, `},"`+k.model[3]+`":`...)
		b = AutoAppendString(b, v.UserName)
		b = append(b, `,"`+k.model[4]+`":`...)
		if v.Nested.Tag != 0 || v.Nested.JustValue.Tag != 0 {
			b = append(b, "null"...)
		} else {
			b = AutoAppendInt(b, int64(v.Nested.JustValue.JustValue))
		}
		b = append(b, `,"`+k.model[5]+`":[`...)
		for i, e := range v.Colors {
			if i > 0 {
				b = append(b, ',')
			}
			b = c.AppendEnum(b, "acgColor", int64(e))
		}
		return append(b, "]}"...), nil
	}
}

// ── decode ───────────────────────────────────────────────────────────────────

func acgDecItem(k acgKeyset) func(any, *AutoCodecCtx) (acgItem, error) {
	return func(raw any, c *AutoCodecCtx) (out acgItem, err error) {
		m, err := AutoDecObject(raw, "acgItem")
		if err != nil {
			return out, err
		}
		var x any
		if x, err = AutoDecField(m, k.item[0], false); err != nil {
			return out, err
		}
		if out.Title, err = AutoDecString(x); err != nil {
			return out, err
		}
		if x, err = AutoDecField(m, k.item[1], false); err != nil {
			return out, err
		}
		if out.Done, err = AutoDecBool(x); err != nil {
			return out, err
		}
		if x, err = AutoDecField(m, k.item[2], false); err != nil {
			return out, err
		}
		n, err := AutoDecInt(x)
		if err != nil {
			return out, err
		}
		out.Count = int(n)
		if x, err = AutoDecField(m, k.item[3], false); err != nil {
			return out, err
		}
		if out.Score, err = AutoDecFloat(x); err != nil {
			return out, err
		}
		if x, err = AutoDecField(m, k.item[4], false); err != nil {
			return out, err
		}
		if n, err = AutoDecEnum("acgColor", x); err != nil {
			return out, err
		}
		out.Color = acgColor(n)
		if x, err = AutoDecField(m, k.item[5], true); err != nil {
			return out, err
		}
		just, err := AutoDecMaybe(x)
		if err != nil {
			return out, err
		}
		if !just {
			out.Note = Nothing[string]()
		} else {
			s, err := AutoDecString(x)
			if err != nil {
				return out, err
			}
			out.Note = Just(s)
		}
		if x, err = AutoDecField(m, k.item[6], false); err != nil {
			return out, err
		}
		items, err := AutoDecList(x)
		if err != nil {
			return out, err
		}
		out.Tags = make([]string, len(items))
		for i, it := range items {
			if out.Tags[i], err = AutoDecString(it); err != nil {
				return out, err
			}
		}
		return out, nil
	}
}

func acgDecModel(k acgKeyset) func(any, *AutoCodecCtx) (acgModel, error) {
	item := acgDecItem(k)
	return func(raw any, c *AutoCodecCtx) (out acgModel, err error) {
		m, err := AutoDecObject(raw, "acgModel")
		if err != nil {
			return out, err
		}
		var x any
		if x, err = AutoDecField(m, k.model[0], false); err != nil {
			return out, err
		}
		list, err := AutoDecList(x)
		if err != nil {
			return out, err
		}
		out.Items = make([]acgItem, len(list))
		for i, it := range list {
			if out.Items[i], err = item(it, c); err != nil {
				return out, err
			}
		}
		if x, err = AutoDecField(m, k.model[1], false); err != nil {
			return out, err
		}
		keys, raws, err := AutoDecDict(x)
		if err != nil {
			return out, err
		}
		vals := make([]acgItem, len(raws))
		for i, r := range raws {
			if vals[i], err = item(r, c); err != nil {
				return out, err
			}
		}
		out.ById = AutoDictBuild(keys, vals)
		if x, err = AutoDecField(m, k.model[2], false); err != nil {
			return out, err
		}
		keys, raws, err = AutoDecDict(x)
		if err != nil {
			return out, err
		}
		svals := make([]string, len(raws))
		for i, r := range raws {
			if svals[i], err = AutoDecString(r); err != nil {
				return out, err
			}
		}
		out.ByNum = AutoDictBuild(keys, svals)
		if x, err = AutoDecField(m, k.model[3], false); err != nil {
			return out, err
		}
		if out.UserName, err = AutoDecString(x); err != nil {
			return out, err
		}
		if x, err = AutoDecField(m, k.model[4], true); err != nil {
			return out, err
		}
		just, err := AutoDecMaybe(x)
		if err != nil {
			return out, err
		}
		if !just {
			out.Nested = Nothing[SkyMaybe[int]]()
		} else {
			inner, err := AutoDecMaybe(x)
			if err != nil {
				return out, err
			}
			if !inner {
				out.Nested = Just(Nothing[int]())
			} else {
				n, err := AutoDecInt(x)
				if err != nil {
					return out, err
				}
				out.Nested = Just(Just(int(n)))
			}
		}
		if x, err = AutoDecField(m, k.model[5], false); err != nil {
			return out, err
		}
		list, err = AutoDecList(x)
		if err != nil {
			return out, err
		}
		out.Colors = make([]acgColor, len(list))
		for i, it := range list {
			n, err := AutoDecEnum("acgColor", it)
			if err != nil {
				return out, err
			}
			out.Colors[i] = acgColor(n)
		}
		return out, nil
	}
}

// ── harness (shared with the compiled-fixture differential test) ─────────────

// autoSame is reflect.DeepEqual, with a NaN equal to itself (the %#v forms
// still tell nil slices from empty ones).
func autoSame(a, b any) bool {
	return reflect.DeepEqual(a, b) || fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}

// autoCodecReflective runs f with the generated codecs off.
func autoCodecReflective(f func()) {
	autoCodecReference.Store(true)
	defer autoCodecReference.Store(false)
	f()
}

// autoCodecCheckEncode compares the generated tree and bytes with the walker's.
func autoCodecCheckEncode(t *testing.T, v any, snake bool) {
	t.Helper()
	gen, ctx := autoCodecFor(reflect.TypeOf(v), snake)
	if gen == nil {
		t.Fatalf("no generated codec for %T (refusals %q)", v, AutoCodecRefusals())
	}
	var want any
	var wantPanic string
	autoCodecReflective(func() {
		defer func() {
			if r := recover(); r != nil {
				wantPanic = fmt.Sprint(r)
			}
		}()
		want = Codec_autoEnc(snake, v).(JsonValue).raw
	})
	got, err := gen.tree(v, ctx)
	if wantPanic != "" {
		t.Fatalf("walker panicked %q, generated tree %v %v", wantPanic, got, err)
	}
	if err != nil || !autoSame(got, want) {
		t.Fatalf("tree differs:\n gen %#v (%v)\nwant %#v", got, err, want)
	}
	if !autoSame(Codec_autoEnc(snake, v).(JsonValue).raw, want) {
		t.Fatalf("Codec_autoEnc dispatch differs")
	}
	wantBytes, wantErr := jsonMarshalTree(want)
	gotBytes, gotErr := gen.app(nil, v, ctx)
	if gotErr == errAutoFallback {
		if wantErr == nil {
			t.Fatalf("generated bytes fell back on a tree that marshals: %s", wantBytes)
		}
		return
	}
	if gotErr != nil || wantErr != nil || string(gotBytes) != string(wantBytes) {
		t.Fatalf("bytes differ:\n gen %s (%v)\nwant %s (%v)", gotBytes, gotErr, wantBytes, wantErr)
	}
}

// autoCodecCheckDecode compares the generated decode with the walker's, for
// one parsed JSON document.
func autoCodecCheckDecode(t *testing.T, witness any, snake bool, doc string) {
	t.Helper()
	raw, perr := jsonParseDocument(doc)
	if perr != nil {
		return // the parser runs before either decoder; nothing to compare
	}
	run := func() (any, string) {
		res := Codec_autoDecoder(snake, witness).(JsonDecoder).run(raw)
		if r, ok := res.(SkyResult[any, any]); ok {
			if r.Tag == 0 {
				return r.OkValue, ""
			}
			return nil, fmt.Sprint(r.ErrValue)
		}
		return nil, fmt.Sprintf("unexpected %T", res)
	}
	var want any
	var wantErr string
	autoCodecReflective(func() { want, wantErr = run() })
	got, gotErr := run()
	if gotErr != wantErr || !autoSame(got, want) {
		t.Fatalf("decode of %.200s differs:\n gen %#v %q\nwant %#v %q", doc, got, gotErr, want, wantErr)
	}
}

func acgRandom(r *rand.Rand) acgModel {
	strs := []string{"", "a", "<b>&\"x\"", " ", "é", "\x01", string([]byte{0xff, 'a'}), "naïve 🌍"}
	str := func() string { return strs[r.Intn(len(strs))] }
	floats := []float64{0, math.Copysign(0, -1), 1, 1.5, 1e21, 1e-7, -3.25, 123456789.125, math.MaxFloat64}
	item := func() acgItem {
		it := acgItem{Title: str(), Done: r.Intn(2) == 0, Count: r.Intn(2000) - 1000, Score: floats[r.Intn(len(floats))], Color: acgColor(r.Intn(3))}
		if r.Intn(2) == 0 {
			it.Note = Just(str())
		} else {
			it.Note = Nothing[string]()
		}
		for i := r.Intn(3); i > 0; i-- {
			it.Tags = append(it.Tags, str())
		}
		return it
	}
	m := acgModel{UserName: str()}
	for i := r.Intn(4); i > 0; i-- {
		m.Items = append(m.Items, item())
	}
	var ik []string
	var iv []acgItem
	for i := r.Intn(4); i > 0; i-- {
		ik, iv = append(ik, fmt.Sprintf("k%d%s", r.Intn(50), str())), append(iv, item())
	}
	m.ById = dictBuild(ik, iv, false)
	var nk, nv []string
	for i := r.Intn(4); i > 0; i-- {
		nk, nv = append(nk, fmt.Sprint(r.Intn(100)-50)), append(nv, str())
	}
	m.ByNum = dictBuild(nk, nv, false)
	switch r.Intn(3) {
	case 0:
		m.Nested = Nothing[SkyMaybe[int]]()
	case 1:
		m.Nested = Just(Nothing[int]())
	default:
		m.Nested = Just(Just(r.Intn(10)))
	}
	for i := r.Intn(4); i > 0; i-- {
		m.Colors = append(m.Colors, acgColor(r.Intn(3)))
	}
	return m
}

// acgMutations are documents a decoder must read or refuse exactly like the
// walker: type flips, missing fields, null, 1.0, huge ints, duplicate keys,
// unknown enums.
func acgMutations(doc string) []string {
	return []string{
		doc,
		strings.Replace(doc, `"done":true`, `"done":1`, 1),
		strings.Replace(doc, `"done":false`, `"done":null`, 1),
		strings.Replace(doc, `"count":`, `"count":1.0,"x":`, 1),
		strings.Replace(doc, `"count":`, `"count":99999999999999999999,"x":`, 1),
		strings.Replace(doc, `"count":`, `"count":1.5,"x":`, 1),
		strings.Replace(doc, `"count":`, `"count":"1","x":`, 1),
		strings.Replace(doc, `"score":`, `"score":"0","x":`, 1),
		strings.Replace(doc, `"color":"Red"`, `"color":"Blue"`, 1),
		strings.Replace(doc, `"color":"Red"`, `"color":0`, 1),
		strings.Replace(doc, `"title":`, `"title":5,"x":`, 1),
		strings.Replace(doc, `"title":`, `"title":"dup","title":`, 1),
		strings.Replace(doc, `"note":null`, ``, 1),
		strings.Replace(doc, `"tags":[`, `"tags":[null,`, 1),
		strings.Replace(doc, `"tags":`, `"tags":{},"x":`, 1),
		strings.Replace(doc, `"items":[`, `"items":[1,`, 1),
		strings.Replace(doc, `"user_name":`, `"x":`, 1),
		strings.Replace(doc, `"userName":`, `"x":`, 1),
		strings.Replace(doc, `"by_id":{`, `"by_id":{"zz":1,"aa":2,`, 1),
		strings.Replace(doc, `"byNum":{`, `"byNum":{"9":5,"1":6,`, 1),
		strings.Replace(doc, `"nested":`, `"nested":"x","y":`, 1),
		strings.Replace(doc, `"colors":[`, `"colors":["Green",-0,`, 1),
		`null`, `[]`, `"x"`, `{}`,
	}
}

func TestAutoCodecHandWrittenDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(20261110))
	for i := 0; i < 400; i++ {
		m := acgRandom(r)
		for _, snake := range []bool{true, false} {
			autoCodecCheckEncode(t, m, snake)
			b, err := jsonMarshalTree(Codec_autoEnc(snake, m).(JsonValue).raw)
			if err != nil {
				continue
			}
			for _, doc := range acgMutations(string(b)) {
				autoCodecCheckDecode(t, acgModel{}, snake, doc)
			}
		}
	}
}

func TestAutoCodecNaNFallsBack(t *testing.T) {
	m := acgModel{Items: []acgItem{{Score: math.NaN(), Note: Nothing[string]()}}, Nested: Nothing[SkyMaybe[int]]()}
	gen, ctx := autoCodecFor(reflect.TypeOf(m), true)
	if _, err := gen.app(nil, m, ctx); err != errAutoFallback {
		t.Fatalf("NaN: err %v, want errAutoFallback", err)
	}
	autoCodecCheckEncode(t, m, true)
}

func TestAutoCodecRegistrationRefusesMismatch(t *testing.T) {
	saved := autoCodecRefusals
	defer func() { autoCodecRefusals = saved }()
	k := acgKeys(true)
	tree, app, dec := acgTreeModel(k), acgAppModel(k), acgDecModel(k)
	declModel := []string{"[]acgItem", "rt.SkyDict[acgItem]", "rt.SkyDict[string]", "string", "rt.SkyMaybe[rt.SkyMaybe[int]]", "[]acgColor"}
	declItem := []string{"string", "bool", "int", "float64", "acgColor", "rt.SkyMaybe[string]", "[]string"}
	badKey := append([]string(nil), k.item...)
	badKey[1] = "Done"
	badDecl := append([]string(nil), declItem...)
	badDecl[4] = "int"
	cases := map[string][]AutoCodecShape{
		"key":                    {AutoShape[acgModel](k.model, declModel), AutoShape[acgItem](badKey, declItem)},
		"declared":               {AutoShape[acgModel](k.model, declModel), AutoShape[acgItem](k.item, badDecl)},
		"camel keys under snake": {AutoShape[acgModel](acgKeys(false).model, declModel), AutoShape[acgItem](k.item, declItem)},
		"missing nested shape":   {AutoShape[acgModel](k.model, declModel)},
	}
	before := len(autoCodecRegistry)
	for name, shapes := range cases {
		autoCodecRefusals = nil
		if RegisterAutoCodec[acgModel](true, shapes, tree, app, dec) || len(autoCodecRefusals) != 1 {
			t.Fatalf("%s: a mismatched registration was accepted", name)
		}
	}
	if RegisterAutoCodec[acgBad](true, []AutoCodecShape{AutoShape[acgBad]([]string{"data"}, []string{"any"})},
		func(acgBad, *AutoCodecCtx) (any, error) { return nil, nil },
		func(b []byte, _ acgBad, _ *AutoCodecCtx) ([]byte, error) { return b, nil },
		func(any, *AutoCodecCtx) (acgBad, error) { return acgBad{}, nil }) {
		t.Fatalf("a record with an any field was accepted")
	}
	if len(autoCodecRegistry) != before {
		t.Fatalf("a refused registration changed the registry")
	}
}

func TestAutoCodecDictDecodeErrorIsDeterministic(t *testing.T) {
	doc := `{"items":[],"by_id":{},"by_num":{"b":1,"a":2,"c":"ok"},"user_name":"","colors":[]}`
	raw, _ := jsonParseDocument(doc)
	var first string
	for i := 0; i < 50; i++ {
		var msg string
		autoCodecReflective(func() {
			msg = fmt.Sprint(Codec_autoDecoder(true, acgModel{}).(JsonDecoder).run(raw).(SkyResult[any, any]).ErrValue)
		})
		if i == 0 {
			first = msg
		} else if msg != first {
			t.Fatalf("Dict decode error depends on map order: %q then %q", first, msg)
		}
	}
	if !strings.Contains(first, "got Number") {
		t.Fatalf("unexpected error %q", first)
	}
	if _, err := json.Marshal(first); err != nil {
		t.Fatal(err)
	}
}
