package rt

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// The record a form submit fills: `type alias Typed = { title : String, age :
// Int, agree : Bool, note : Maybe String, ratio : Float }`.
type formTyped struct {
	Title string
	Age   int
	Agree bool
	Note  SkyMaybe[string]
	Ratio float64
}

func typedCtor(c formTyped) any {
	return SkyADT{Tag: 0, SkyName: "SubmitTyped", Fields: []any{c}}
}

// The typed codegen usually wraps a constructor as `func(any) any` whose body
// narrows the argument with rt.Coerce — the record type is not visible from
// the outside. This is that shape.
func typedCtorAny(v any) any {
	return typedCtor(Coerce[formTyped](v))
}

func submitRaw(s string) []json.RawMessage { return []json.RawMessage{json.RawMessage(s)} }

func fieldsOf(t *testing.T, result any) formTyped {
	t.Helper()
	adt, ok := result.(SkyADT)
	if !ok {
		t.Fatalf("expected the SubmitTyped Msg, got %T %v", result, result)
	}
	rec, ok := adt.Fields[0].(formTyped)
	if !ok {
		t.Fatalf("expected formTyped, got %T", adt.Fields[0])
	}
	return rec
}

const goodSubmit = `{"title":"t","age":"42","agree":"on","note":"","ratio":" 1.5 "}`

// UF-7 / L4: "42" into an Int field used to become 0 and a checked checkbox
// ("on") used to become False — on a typed constructor AND on the `func(any)
// any` wrapper. Both now decode the text.
func TestFormSubmit_DecodesIntBoolFloatMaybe(t *testing.T) {
	for name, handler := range map[string]any{"typed": typedCtor, "any-wrapped": typedCtorAny} {
		got := fieldsOf(t, applyMsgArgs(handler, submitRaw(goodSubmit), ""))
		if got.Title != "t" || got.Age != 42 || !got.Agree || got.Ratio != 1.5 {
			t.Errorf("%s: wrong record %+v", name, got)
		}
		if got.Note.Tag != 1 {
			t.Errorf("%s: an empty control is Nothing for a Maybe field, got %+v", name, got.Note)
		}
	}
}

// An unchecked checkbox is left out of a submit: that is False, not an error.
func TestFormSubmit_AbsentBoolIsFalse(t *testing.T) {
	got := fieldsOf(t, applyMsgArgs(typedCtor, submitRaw(`{"title":"t","age":"1","ratio":"0","note":"x"}`), ""))
	if got.Agree {
		t.Errorf("absent checkbox must be False, got %+v", got)
	}
	if got.Note.Tag != 0 || got.Note.JustValue != "x" {
		t.Errorf("a filled Maybe field is Just, got %+v", got.Note)
	}
}

// A String field with no control decodes as "" — as HTML submits an empty text
// input. A record often carries a String the form does not edit (an image URL
// an upload sets), and refusing the whole submit for it broke a real admin
// form (every product save failed).
func TestFormSubmit_AbsentStringIsEmpty(t *testing.T) {
	for hname, handler := range map[string]any{"typed": typedCtor, "any-wrapped": typedCtorAny} {
		res := applyMsgArgs(handler, submitRaw(`{"age":"1","ratio":"0"}`), "")
		if _, dropped := res.(msgDecodeError); dropped {
			t.Fatalf("%s: a String field with no control must decode as \"\", not drop the submit", hname)
		}
		if got := fieldsOf(t, res); got.Title != "" || got.Age != 1 {
			t.Fatalf("%s: want title \"\" and age 1, got %+v", hname, got)
		}
	}
}

// A missing or unparsable NUMBER field, or a Bool that does not parse, is a
// CLASSIFIED decode error: the event is dropped (msgDecodeError), never
// delivered with a zero value.
func TestFormSubmit_BadFieldDropsTheEvent(t *testing.T) {
	cases := map[string]string{
		"not an Int":    `{"title":"t","age":"forty","ratio":"0"}`,
		"empty Int":     `{"title":"t","age":"","ratio":"0"}`,
		"not a Bool":    `{"title":"t","age":"1","agree":"maybe","ratio":"0"}`,
		"missing Float": `{"title":"t","age":"1"}`,
	}
	for name, raw := range cases {
		for hname, handler := range map[string]any{"typed": typedCtor, "any-wrapped": typedCtorAny} {
			res := applyMsgArgs(handler, submitRaw(raw), "")
			if _, ok := res.(msgDecodeError); !ok {
				t.Errorf("%s / %s: expected the event to be dropped, got %T %+v", name, hname, res, res)
			}
		}
	}
}

func TestDecodeFormRecord_ErrorNamesTheField(t *testing.T) {
	_, err := decodeFormRecord(FormFields{"title": "t", "age": "x", "ratio": "0"}, typeOfFormTyped())
	var fe *FormDecodeError
	if !errors.As(err, &fe) || fe.Field != "age" {
		t.Fatalf("expected a FormDecodeError on field age, got %v", err)
	}
}

// rt.Coerce of a form into a record is strict too (the Spa path narrows the
// FormFields value inside the handler): it raises *FormDecodeError.
func TestCoerce_FormFieldsIntoRecordIsStrict(t *testing.T) {
	ok := Coerce[formTyped](FormFields{"title": "a", "age": "7", "agree": "true", "ratio": "2"})
	if ok.Age != 7 || !ok.Agree {
		t.Fatalf("good form decoded wrong: %+v", ok)
	}
	defer func() {
		r := recover()
		if _, isForm := asFormDecodeError(r); !isForm {
			t.Fatalf("expected a *FormDecodeError panic, got %v", r)
		}
	}()
	_ = Coerce[formTyped](FormFields{"title": "a", "age": "7"})
}

// A Dict String String handler still receives the raw field map.
func TestFormSubmit_DictHandlerKeepsTheMap(t *testing.T) {
	dictMsg := func(m map[string]string) any { return SkyADT{SkyName: "D", Fields: []any{m}} }
	res := applyMsgArgs(dictMsg, submitRaw(`{"a":"1","b":"x"}`), "")
	adt, ok := res.(SkyADT)
	if !ok {
		t.Fatalf("got %T", res)
	}
	if m := adt.Fields[0].(map[string]string); m["a"] != "1" || m["b"] != "x" {
		t.Fatalf("map lost fields: %v", m)
	}
}

func typeOfFormTyped() reflect.Type { return reflect.TypeOf(formTyped{}) }
