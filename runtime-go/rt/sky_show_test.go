package rt

import (
	"fmt"
	"testing"
)

type showShape interface{ SkyVariantTag() int }

type showCircle struct{ V0 float64 }

func (showCircle) SkyVariantTag() int     { return 0 }
func (showCircle) SkyVariantName() string { return "Circle" }

type showRect struct{ V0 struct{ H, W int } }

func (showRect) SkyVariantTag() int     { return 1 }
func (showRect) SkyVariantName() string { return "Rect" }

type showEmpty struct{}

func (showEmpty) SkyVariantTag() int     { return 2 }
func (showEmpty) SkyVariantName() string { return "Empty" }

type showWrap struct{ V0 showShape }

func (showWrap) SkyVariantTag() int     { return 0 }
func (showWrap) SkyVariantName() string { return "Wrap" }

type showUser struct {
	Name  string        `sky:"name,string"`
	Age   int           `sky:"age,int"`
	Tags  []string      `sky:"tags,[]string"`
	Color int           `sky:"color,SkyShowTest_Color"`
	Fav   SkyMaybe[int] `sky:"fav,rt.SkyMaybe[SkyShowTest_Color]"`
}

// The values `sky test` prints for a failing assertion (a downstream project
// reported `expected {0 a <nil>} but got {0 b <nil>}`) and that
// `Debug.toString` renders: Sky syntax for every runtime shape.
func TestSkyShowRendersSkySyntax(t *testing.T) {
	RegisterEnum("SkyShowTest_Color", []string{"Red", "Green"})
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"string", "a\"b\n", `"a\"b\n"`},
		{"char", 'c', `'c'`},
		{"int", 42, `42`},
		{"negative int arg", Just[any](-3), `Just (-3)`},
		{"float", 1.5, `1.5`},
		{"bool", true, `True`},
		{"unit", struct{}{}, `()`},
		{"ok", Ok[any, any]("a"), `Ok "a"`},
		{"err", Err[any, any](ErrIo("x")), `Err (Io "x")`},
		{"nothing", Nothing[any](), `Nothing`},
		{"nested", Just[any](Ok[any, any](SkyTuple2{V0: 1, V1: "x"})), `Just (Ok (1, "x"))`},
		{"tuple3", T3[int, string, bool]{V0: 1, V1: "x", V2: false}, `(1, "x", False)`},
		{"list", []any{1, 2, 3}, `[1, 2, 3]`},
		{"typed list", []string{"a", "b"}, `["a", "b"]`},
		{"empty list", []any(nil), `[]`},
		{"typed variants", []showShape{showCircle{1.5}, showRect{struct{ H, W int }{2, 1}}, showEmpty{}},
			`[Circle 1.5, Rect { h = 2, w = 1 }, Empty]`},
		{"variant of variant", showWrap{showCircle{2}}, `Wrap (Circle 2)`},
		{"erased adt", SkyADT{Tag: 0, SkyName: "Pair", Fields: []any{1, Just[any]("y")}}, `Pair 1 (Just "y")`},
		{"record with enum field", showUser{Name: "Ada", Age: 40, Tags: []string{"a"}, Color: 1, Fav: Just[int](0)},
			`{ age = 40, color = Green, fav = Just Red, name = "Ada", tags = ["a"] }`},
		{"set", Set_fromList([]any{3, 1, 2}), `Set.fromList [1, 2, 3]`},
		{"secret stays redacted", Secret{v: "hunter2"}, `[REDACTED]`},
		{"function", func(x int) int { return x }, `<function>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SkyShow(c.v); got != c.want {
				t.Errorf("SkyShow = %s, want %s", got, c.want)
			}
		})
	}
}

// `Debug.toString` keeps a top-level String unquoted (interpolation splices
// its text) and renders everything else as `SkyShow` does. `Debug_show` (the
// `Sky.Test` printer) quotes it.
func TestDebugToStringAndShow(t *testing.T) {
	if got := Debug_toString("hi"); got != "hi" {
		t.Errorf("Debug_toString(string) = %v", got)
	}
	if got := Debug_show("hi"); got != `"hi"` {
		t.Errorf("Debug_show(string) = %v", got)
	}
	if got := Debug_toString(Ok[any, any]("hi")); got != `Ok "hi"` {
		t.Errorf("Debug_toString(Ok) = %v", got)
	}
	if got := Debug_toString(Just[any](5)); got != `Just 5` {
		t.Errorf("Debug_toString(Just 5) = %v", got)
	}
}

// showColor mirrors what the compiler emits for `type Color = Red | Green |
// Blue` (a union whose constructors take no arguments): a named int with a
// generated SkyEnumName.
type showColor int

func (v showColor) SkyEnumName() string { return EnumName("SkyShowTest_Nullary", int(v)) }

// A nullary union prints its constructor name wherever it sits — the top
// level, a Maybe, a List, a tuple, a record, a Dict value — not its index.
func TestSkyShowNamesANullaryUnionEverywhere(t *testing.T) {
	RegisterEnum("SkyShowTest_Nullary", []string{"Red", "Green", "Blue"})
	type rec struct {
		Color showColor `sky:"color,SkyShowTest_Nullary"`
		Tone  showColor
	}
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"top level", showColor(2), `Blue`},
		{"in a Maybe", Just[showColor](1), `Just Green`},
		{"in a List", []showColor{0, 1, 2}, `[Red, Green, Blue]`},
		{"in an any List", []any{showColor(0), showColor(2)}, `[Red, Blue]`},
		{"in a tuple", T2[showColor, int]{V0: 0, V1: 1}, `(Red, 1)`},
		{"in a record", rec{Color: 1, Tone: 2}, `{ color = Green, tone = Blue }`},
		{"a Dict value", Dict_fromList([]any{SkyTuple2{V0: "a", V1: showColor(1)}}), `Dict.fromList [("a", Green)]`},
		{"in a Result", Ok[any, showColor](0), `Ok Red`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SkyShow(c.v); got != c.want {
				t.Errorf("SkyShow = %s, want %s", got, c.want)
			}
		})
	}
	// A kernel that reads the tag as an Int still can.
	if AsInt(any(showColor(2))) != 2 || AsIntOrZero(any(showColor(1))) != 1 {
		t.Error("AsInt / AsIntOrZero read a named int by kind")
	}
	// rt.Coerce turns a runtime-built int into the named type.
	if Coerce[showColor](any(1)) != showColor(1) {
		t.Error("Coerce int -> named int")
	}
	// `%v` (Dict key encoding, logs) still prints the ordinal.
	if s := fmt.Sprintf("%v", showColor(2)); s != "2" {
		t.Errorf("%%v of a named enum = %q, want the ordinal", s)
	}
}

// A runtime-built value of a nullary union is a plain int ordinal; code
// typed against the named int reads it through rt.EnumOf / rt.EnumTagIs /
// rt.Coerce, never a raw assertion that would fail on the int.
func TestNamedEnumAcceptsARuntimeBuiltOrdinal(t *testing.T) {
	if EnumOf[showColor](any(2)) != showColor(2) || EnumOf[showColor](any(showColor(1))) != showColor(1) {
		t.Error("EnumOf accepts the named int and its int ordinal")
	}
	if !EnumTagIs(any(1), 1) || !EnumTagIs(any(showColor(1)), 1) || EnumTagIs(any(showColor(2)), 1) {
		t.Error("EnumTagIs compares a named int and an int by value")
	}
	if !deepEq(showColor(1), 1) {
		t.Error("Sky == treats a named enum and its ordinal as equal")
	}
}
