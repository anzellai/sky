package rt

import "testing"

// An `Err` or a `Nothing` that reaches a value conversion has no value to give.
// Every As* / Coerce helper must then fail as a classified panic: the loud
// class the Err carries (an Ffi boundary turned an unclassified kernel panic
// into an `Err`, and a loud one is re-raised before that, but an Err made from
// a loud message by other code keeps its class), else CoerceFailure. Before
// v0.29.2 `AsString(Err …)` printed the container (`{1 <nil> {…}}`), so a
// `callPure` String kernel that panicked made the app show text silently, and
// AsList / AsListT / AsMapT / AsMapAny / AsDict / AsRune / AsTuple* answered an
// empty value: a silent wrong answer.
func TestValueConversionsFailOnErrAndNothing(t *testing.T) {
	empties := map[string]any{
		"Err":             Err[any, any](ErrFfi(`Ffi "String_x" panicked: boom`)),
		"Err of string":   Err[any, any]("boom"),
		"typed Err":       Err[skyErrorAdt, string](makeErrorT(2, "Ffi", "boom")),
		"Nothing":         Nothing[any](),
		"typed Nothing":   Nothing[string](),
		"Err in an Ok":    Ok[any, any](Err[any, any](ErrFfi("boom"))),
		"Nothing in Ok":   Ok[any, any](Nothing[any]()),
		"Nothing in Just": Just[any](Nothing[any]()),
	}
	helpers := map[string]func(v any){
		"AsString":       func(v any) { _ = AsString(v) },
		"CoerceString":   func(v any) { _ = CoerceString(v) },
		"AsInt":          func(v any) { _ = AsInt(v) },
		"AsFloat":        func(v any) { _ = AsFloat(v) },
		"AsBool":         func(v any) { _ = AsBool(v) },
		"AsRune":         func(v any) { _ = AsRune(v) },
		"AsList":         func(v any) { _ = AsList(v) },
		"AsListAny":      func(v any) { _ = AsListAny(v) },
		"AsListT":        func(v any) { _ = AsListT[int](v) },
		"AsMapT":         func(v any) { _ = AsMapT[int](v) },
		"AsMapAny":       func(v any) { _ = AsMapAny(v) },
		"AsDict":         func(v any) { _ = AsDict(v) },
		"asSkyDict":      func(v any) { _ = asSkyDict(v) },
		"AsTuple2":       func(v any) { _ = AsTuple2(v) },
		"AsTuple3":       func(v any) { _ = AsTuple3(v) },
		"AsTuple2T":      func(v any) { _ = AsTuple2T[int, int](v) },
		"AsTuple3T":      func(v any) { _ = AsTuple3T[int, int, int](v) },
		"Coerce[string]": func(v any) { _ = Coerce[string](v) },
		"Coerce[int]":    func(v any) { _ = Coerce[int](v) },
		"Coerce[[]any]":  func(v any) { _ = Coerce[[]any](v) },
		"Coerce[SkyADT]": func(v any) { _ = Coerce[SkyADT](v) },
	}
	for label, v := range empties {
		for name, run := range helpers {
			msg, panicked := recoverMsg(func() { run(v) })
			if !panicked {
				t.Errorf("%s(%s) passed silently; an Err or a Nothing must fail", name, label)
				continue
			}
			if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
				t.Errorf("%s(%s): panic %q is %s, want CoerceFailure", name, label, msg, kind)
			}
		}
	}
}

// An Err whose message carries a loud-by-design class keeps that class when it
// reaches a value conversion, so the panic net reports it as what it is.
func TestValueConversionOfLoudErrKeepsItsClass(t *testing.T) {
	loud := Err[any, any](ErrFfi(`Ffi "Decimal_toInt" panicked: rt.Decimal.toInt: 1e30 is out of the Int range`))
	for name, run := range map[string]func(){
		"AsString":  func() { _ = AsString(loud) },
		"AsInt":     func() { _ = AsInt(loud) },
		"AsList":    func() { _ = AsList(loud) },
		"Coerce[T]": func() { _ = Coerce[string](loud) },
	} {
		msg, panicked := recoverMsg(run)
		if !panicked {
			t.Errorf("%s(loud Err) passed silently", name)
			continue
		}
		if kind, _ := classifyPanic(msg); kind != "NumericOverflow" {
			t.Errorf("%s(loud Err): panic %q is %s, want NumericOverflow", name, msg, kind)
		}
	}
}

// The values a conversion legitimately takes still pass: an Ok / a Just
// unwraps, a Result or Maybe target keeps its container, an `any` target takes
// the container as it is, and a nil list / dict is empty.
func TestValueConversionsStillUnwrapOkAndJust(t *testing.T) {
	if AsString(Ok[any, any]("a")) != "a" || AsString(Just[any]("b")) != "b" {
		t.Error("AsString no longer unwraps Ok / Just")
	}
	if xs := AsList(Ok[any, any]([]any{1, 2})); len(xs) != 2 {
		t.Errorf("AsList(Ok [1,2]) = %v", xs)
	}
	if tp := AsTuple2(Just[any](SkyTuple2{V0: 1, V1: 2})); tp.V0 != 1 || tp.V1 != 2 {
		t.Errorf("AsTuple2(Just (1,2)) = %v", tp)
	}
	if m := Coerce[SkyMaybe[any]](Nothing[any]()); m.Tag != 1 {
		t.Errorf("Coerce to a Maybe lost the Nothing: %v", m)
	}
	if r := Coerce[SkyResult[any, any]](Err[any, any]("e")); r.Tag != 1 {
		t.Errorf("Coerce to a Result lost the Err: %v", r)
	}
	if a := Coerce[any](Err[any, any]("e")); a == nil {
		t.Error("Coerce[any] dropped the Err")
	}
	if AsList(nil) != nil || len(AsMapAny(nil)) != 0 {
		t.Error("a nil list / dict is no longer empty")
	}
	if _, panicked := recoverMsg(func() { _ = AsString(SkyADT{Tag: 1, SkyName: "Red"}) }); panicked {
		t.Error("AsString of a custom type value panics")
	}
}
