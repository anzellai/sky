package rt

import "testing"

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
