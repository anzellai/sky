package rt

import (
	"reflect"
	"testing"
)

// A request-shaped seed reaches `init` / `App.withRequest` as a map[string]any
// whose Dict fields (headers/params/cookies) are THEMSELVES map[string]any —
// Sky's generic Dict rep (Dict_empty returns map[string]any). A typed record
// parameter wants those fields as map[string]string. Go maps are invariant, so
// before coerceReflectArg learned to rebuild a map element-wise, the string
// fields survived the map→struct coercion but every Dict field arrived EMPTY —
// silently dropping the request's headers and cookies at first paint.
func TestCoerceReflectArg_SkyDictIntoTypedStructField(t *testing.T) {
	type reqStruct struct {
		Method  string
		Headers map[string]string
		Cookies map[string]string
	}
	seed := map[string]any{
		"method":  "GET",
		"headers": map[string]any{"X-Who": "ada", "Host": "example"},
		"cookies": map[string]any{"sky_sid": "abc123"},
	}
	got := coerceReflectArg(reflect.ValueOf(seed), reflect.TypeOf(reqStruct{})).
		Interface().(reqStruct)

	if got.Method != "GET" {
		t.Fatalf("Method = %q, want GET", got.Method)
	}
	if got.Headers["X-Who"] != "ada" {
		t.Errorf("Headers[X-Who] = %q, want ada — Dict field lost in map→struct coercion", got.Headers["X-Who"])
	}
	if got.Headers["Host"] != "example" {
		t.Errorf("Headers[Host] = %q, want example", got.Headers["Host"])
	}
	if got.Cookies["sky_sid"] != "abc123" {
		t.Errorf("Cookies[sky_sid] = %q, want abc123", got.Cookies["sky_sid"])
	}
}

// The element-wise map→map coercion in isolation: map[string]any (values boxed)
// → map[string]string.
func TestCoerceReflectArg_MapStringAnyToMapStringString(t *testing.T) {
	src := map[string]any{"a": "1", "b": "2"}
	out := coerceReflectArg(reflect.ValueOf(src), reflect.TypeOf(map[string]string{})).
		Interface().(map[string]string)
	if out["a"] != "1" || out["b"] != "2" || len(out) != 2 {
		t.Errorf("map[string]any → map[string]string = %v, want {a:1 b:2}", out)
	}
}
