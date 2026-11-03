package rt

import (
	"reflect"
	"strings"
	"testing"
)

// v0.29.2 seventh Judge: the Codec.auto checks walked only some shapes.
//   - codecAutoTypePartial did not look into a Dict value or a variant's
//     payload, so a `Maybe (Maybe Int)` there got no `|check` and
//     `Just Nothing` was stored and read back as `Nothing`.
//   - codecAutoDuplicateKey looked only at the top record, so `n = { aB, a_b }`
//     wrote `{"a_b":1,"a_b":2}` with no builder problem.
//   - a `List Json.Value` / `Dict String Json.Value` with an empty blank was
//     no problem, and one write made every read of the table fail.
// These tests generate every shape up to depth 2 (List, Array, Maybe, Dict,
// record, tuple, and a variant payload) around a leaf, and check each walker
// finds the leaf in every one and never finds a harmless leaf.

type acShapeRec[X any] struct {
	F X `sky:"f,any"`
}

func acTypeOf[X any]() reflect.Type { return reflect.TypeOf((*X)(nil)).Elem() }

// acShapes1 is every one-level shape around X.
func acShapes1[X any]() []reflect.Type {
	return []reflect.Type{
		acTypeOf[[]X](),
		acTypeOf[[2]X](),
		acTypeOf[SkyMaybe[X]](),
		acTypeOf[SkyDict[X]](),
		acTypeOf[map[string]X](),
		acTypeOf[acShapeRec[X]](),
		acTypeOf[T2[int, X]](),
		acTypeOf[T3[string, X, bool]](),
	}
}

// acShapes2 is every shape of depth 1 and 2 around X.
func acShapes2[X any]() []reflect.Type {
	out := acShapes1[X]()
	for _, s := range [][]reflect.Type{
		acShapes1[[]X](), acShapes1[SkyMaybe[X]](), acShapes1[SkyDict[X]](),
		acShapes1[acShapeRec[X]](), acShapes1[T2[int, X]](), acShapes1[[2]X](),
	} {
		out = append(out, s...)
	}
	return out
}

type acDup struct {
	AB  int `sky:"aB,int"`
	A_b int `sky:"a_b,int"`
}

type acNoDup struct {
	AB int `sky:"aB,int"`
	C  int `sky:"c,int"`
}

// A custom type whose variants carry the leaves in each one-level shape.
type acShapeVar interface {
	SkyVariant
	isAcShapeVar()
}

type acShapeVar_Mm_V struct{ V0 SkyMaybe[SkyMaybe[int]] }
type acShapeVar_Dm_V struct {
	V0 SkyDict[SkyMaybe[SkyMaybe[int]]]
}
type acShapeVar_Dup_V struct{ V0 []acDup }
type acShapeVar_N_V struct{}

func (acShapeVar_Mm_V) SkyVariantTag() int      { return 0 }
func (acShapeVar_Mm_V) SkyVariantName() string  { return "Mm" }
func (acShapeVar_Mm_V) isAcShapeVar()           {}
func (acShapeVar_Dm_V) SkyVariantTag() int      { return 1 }
func (acShapeVar_Dm_V) SkyVariantName() string  { return "Dm" }
func (acShapeVar_Dm_V) isAcShapeVar()           {}
func (acShapeVar_Dup_V) SkyVariantTag() int     { return 2 }
func (acShapeVar_Dup_V) SkyVariantName() string { return "Dup" }
func (acShapeVar_Dup_V) isAcShapeVar()          {}
func (acShapeVar_N_V) SkyVariantTag() int       { return 3 }
func (acShapeVar_N_V) SkyVariantName() string   { return "N" }
func (acShapeVar_N_V) isAcShapeVar()            {}

// A custom type with only harmless payloads.
type acPlainVar interface {
	SkyVariant
	isAcPlainVar()
}

type acPlainVar_A_V struct{ V0 SkyMaybe[int] }
type acPlainVar_B_V struct{ V0 []acNoDup }

func (acPlainVar_A_V) SkyVariantTag() int     { return 0 }
func (acPlainVar_A_V) SkyVariantName() string { return "A" }
func (acPlainVar_A_V) isAcPlainVar()          {}
func (acPlainVar_B_V) SkyVariantTag() int     { return 1 }
func (acPlainVar_B_V) SkyVariantName() string { return "B" }
func (acPlainVar_B_V) isAcPlainVar()          {}

func init() {
	RegisterAdtVariant("rt.acShapeVar", "Mm", func([]JsonRawMessage) any { return acShapeVar_Mm_V{} })
	RegisterAdtVariant("rt.acShapeVar", "Dm", func([]JsonRawMessage) any { return acShapeVar_Dm_V{} })
	RegisterAdtVariant("rt.acShapeVar", "Dup", func([]JsonRawMessage) any { return acShapeVar_Dup_V{} })
	RegisterAdtVariant("rt.acShapeVar", "N", func([]JsonRawMessage) any { return acShapeVar_N_V{} })
	RegisterAdtVariant("rt.acPlainVar", "A", func([]JsonRawMessage) any { return acPlainVar_A_V{} })
	RegisterAdtVariant("rt.acPlainVar", "B", func([]JsonRawMessage) any { return acPlainVar_B_V{} })
}

func TestCodecAutoPartialWalksEveryShape(t *testing.T) {
	partial := append(acShapes2[SkyMaybe[SkyMaybe[int]]](), acShapes2[SkyMaybe[any]]()...)
	partial = append(partial, acShapes2[acShapeVar]()...)
	partial = append(partial, acTypeOf[acShapeVar]())
	for _, ty := range partial {
		if !codecAutoTypePartial(ty, "", 0) {
			t.Errorf("%s: not partial; a Just written as null in it would read back as Nothing", ty)
		}
	}
	total := append(acShapes2[int](), acShapes2[acPlainVar]()...)
	total = append(total, acShapes2[string]()...)
	for _, ty := range total {
		// A Maybe directly inside a Maybe is the partial leaf itself.
		if strings.Contains(ty.String(), "SkyMaybe[sky-app/rt.SkyMaybe[") {
			continue
		}
		if codecAutoTypePartial(ty, "", 0) {
			t.Errorf("%s: partial, but every value it holds reads back", ty)
		}
	}
}

func TestCodecAutoDuplicateKeyWalksEveryShape(t *testing.T) {
	dups := append(acShapes2[acDup](), acShapes2[acShapeVar]()...)
	dups = append(dups, acTypeOf[acShapeVar](), acTypeOf[acDup]())
	for _, ty := range dups {
		if p := codecAutoNestedDuplicateKey(ty, true, 0, map[reflect.Type]bool{}); !strings.Contains(p, `both become the key "a_b"`) {
			t.Errorf("%s: duplicate key not found (got %q)", ty, p)
		}
		// Under autoCamel the keys differ: no problem.
		if p := codecAutoNestedDuplicateKey(ty, false, 0, map[reflect.Type]bool{}); p != "" {
			t.Errorf("%s under autoCamel: %q, want no problem", ty, p)
		}
	}
	for _, ty := range append(acShapes2[acNoDup](), acShapes2[acPlainVar]()...) {
		if p := codecAutoNestedDuplicateKey(ty, true, 0, map[reflect.Type]bool{}); p != "" {
			t.Errorf("%s: %q, want no problem", ty, p)
		}
	}
}

type acDupRow struct {
	Id string              `sky:"id,string"`
	N  acDup               `sky:"n,Main_N_R"`
	L  []acNoDup           `sky:"l,[]Main_L_R"`
	D  SkyMaybe[[]acDup]   `sky:"d,rt.SkyMaybe[[]Main_D_R]"`
	V  acShapeVar          `sky:"v,acShapeVar"`
	P  SkyDict[acPlainVar] `sky:"p,rt.SkyDict[acPlainVar]"`
}

// Codec.problems sees each nested duplicate, on the field that holds it, and
// the decoder refuses it.
func TestCodecAutoColsReportsNestedDuplicateKeys(t *testing.T) {
	got := map[string]string{}
	for _, c := range AsList(Codec_autoCols(true, acDupRow{})) {
		p := c.(T2[any, any])
		got[p.V0.(string)] = p.V1.(string)
	}
	for col, want := range map[string]bool{"id": false, "n": true, "l": false, "d": true, "v": true, "p": false} {
		has := strings.Contains(got[col], `|problem=Codec.auto: the fields "aB" and "a_b" both become the key "a_b"`)
		if has != want {
			t.Errorf("column %s kind %q: duplicate-key problem %v, want %v", col, got[col], has, want)
		}
	}
	dec := Codec_autoDecoder(true, acDupRow{})
	if r, ok := runDec(dec, map[string]any{}).(SkyResult[any, any]); !ok || r.Tag != 1 {
		t.Fatalf("the decoder of a record with a nested duplicate key gave %v, want Err", r)
	}
}

type acBlankRow struct {
	Id    string            `sky:"id,string"`
	Items []any             `sky:"items,[]any"`
	Dj    SkyDict[any]      `sky:"dj,rt.SkyDict[any]"`
	More  SkyMaybe[any]     `sky:"more,rt.SkyMaybe[any]"`
	Rec   acShapeRec[[]any] `sky:"rec,Main_Rec_R"`
	Tup   T2[int, []any]    `sky:"tup,rt.T2[int, []any]"`
	Ok    []any             `sky:"ok,[]any"`
	Js    any               `sky:"js,any"`
	Ints  []int             `sky:"ints,[]int"`
}

// A slot that holds a Json.Value is readable only when the blank shows a JSON
// value there; otherwise the field is a builder problem.
func TestCodecAutoColsReportsBlankWithNoJsonElement(t *testing.T) {
	blank := acBlankRow{
		Items: []any{},
		Dj:    SkyDict[any]{},
		More:  Nothing[any](),
		Rec:   acShapeRec[[]any]{F: []any{}},
		Tup:   T2[int, []any]{V0: 0, V1: []any{}},
		Ok:    []any{JsonValue{}},
		Js:    JsonValue{},
	}
	got := map[string]string{}
	for _, c := range AsList(Codec_autoCols(true, blank)) {
		p := c.(T2[any, any])
		got[p.V0.(string)] = p.V1.(string)
	}
	for col, want := range map[string]bool{"id": false, "items": true, "dj": true, "more": true, "rec": true, "tup": true, "ok": false, "js": false, "ints": false} {
		has := strings.Contains(got[col], "its blank has no JSON value")
		if has != want {
			t.Errorf("column %s kind %q: blank problem %v, want %v", col, got[col], has, want)
		}
	}
	// The blank with a JSON value at each slot has no problem.
	full := blank
	full.Items = []any{JsonValue{}}
	full.Dj = DictFromMap(map[string]any{"k": JsonValue{}}).(SkyDict[any])
	full.More = Just[any](JsonValue{})
	full.Rec = acShapeRec[[]any]{F: []any{JsonValue{}}}
	full.Tup = T2[int, []any]{V1: []any{JsonValue{}}}
	for _, c := range AsList(Codec_autoCols(true, full)) {
		p := c.(T2[any, any])
		if strings.Contains(p.V1.(string), "its blank has no JSON value") {
			t.Errorf("column %s kind %q: a blank with a JSON value is a problem", p.V0, p.V1)
		}
	}
}
