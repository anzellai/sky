package rt

// Std.I18n's Text and Locale in Codec.auto, the Sky.Spa wire and a stored
// model. Both are opaque standard-library custom types (erased to SkyADT), so
// like Decimal, Money and Currency they are read and written by their
// declared owner, never by a guess at their payload types.
//
//	Locale  "zh-Hant-HK"
//	Text    {"key": "shop.added", "args": [["item", "s", "tea"]]}
//	        {"verbatim": "Ada"}
//
// An argument is [name, kind, payload]: the wire form of Std.I18n.Arg. This
// file has no build tag: a program that stores a Text compiles it whether or
// not it renders one.

import (
	"fmt"
	"reflect"
	"strings"
)

const skyI18nTextOwner = "builtin:Std.I18n.Text"
const skyI18nLocaleOwner = "builtin:Std.I18n.Locale"

// The constructors of Std.I18n's types, in declaration order (their tags).
const (
	i18nTextMessage  = "Text__Message"
	i18nTextVerbatim = "Text__Verbatim"
	i18nLocaleCtor   = "Locale__Internal"
)

func isI18nOwner(owner string) bool {
	return owner == skyI18nTextOwner || owner == skyI18nLocaleOwner
}

func i18nADT(rv reflect.Value) (SkyADT, bool) {
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return SkyADT{}, false
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || !rv.CanInterface() {
		return SkyADT{}, false
	}
	adt, ok := rv.Interface().(SkyADT)
	return adt, ok
}

// i18nString reads a payload that must be a string.
func i18nString(v any) (string, bool) {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.String {
		return "", false
	}
	return rv.String(), true
}

// i18nTriples reads a list of (String, String, String) in any of the shapes
// the runtime gives a list of tuples.
func i18nTriplesOf(v any) ([][3]string, error) {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil, nil
	}
	if rv.Kind() != reflect.Slice {
		return nil, fmt.Errorf("Codec.auto: invalid Text arguments %s", rv.Type())
	}
	out := make([][3]string, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		t := AsTuple3(rv.Index(i).Interface())
		a, ok1 := i18nString(t.V0)
		b, ok2 := i18nString(t.V1)
		c, ok3 := i18nString(t.V2)
		if !ok1 || !ok2 || !ok3 {
			return nil, fmt.Errorf("Codec.auto: invalid Text argument")
		}
		out = append(out, [3]string{a, b, c})
	}
	return out, nil
}

// codecI18nEncode writes a Text or a Locale.
func codecI18nEncode(rv reflect.Value, owner string) (any, error) {
	adt, ok := i18nADT(rv)
	if !ok {
		return nil, fmt.Errorf("Codec.auto: expected a %s", strings.TrimPrefix(owner, "builtin:"))
	}
	if adt.SkyOwner != "" && adt.SkyOwner != owner {
		return nil, fmt.Errorf("Codec.auto: ADT provenance %q conflicts with declared %s", adt.SkyOwner, owner)
	}
	switch {
	case owner == skyI18nLocaleOwner && adt.SkyName == i18nLocaleCtor && len(adt.Fields) == 1:
		if s, ok := i18nString(adt.Fields[0]); ok {
			return s, nil
		}
	case owner == skyI18nTextOwner && adt.SkyName == i18nTextVerbatim && len(adt.Fields) == 1:
		if s, ok := i18nString(adt.Fields[0]); ok {
			return jsonOrderedObject{keys: []string{"verbatim"}, vals: []any{s}}, nil
		}
	case owner == skyI18nTextOwner && adt.SkyName == i18nTextMessage && len(adt.Fields) == 2:
		k, ok := i18nString(adt.Fields[0])
		args, err := i18nTriplesOf(adt.Fields[1])
		if err != nil {
			return nil, err
		}
		if ok {
			list := make([]any, 0, len(args))
			for _, a := range args {
				list = append(list, []any{a[0], a[1], a[2]})
			}
			return jsonOrderedObject{keys: []string{"key", "args"}, vals: []any{k, list}}, nil
		}
	}
	return nil, fmt.Errorf("Codec.auto: invalid %s payload", strings.TrimPrefix(owner, "builtin:"))
}

// codecI18nDecode reads a Text or a Locale.
func codecI18nDecode(raw any, owner string) (SkyADT, error) {
	if owner == skyI18nLocaleOwner {
		s, ok := raw.(string)
		if !ok || !i18nPlausibleTag(s) {
			return SkyADT{}, fmt.Errorf("Codec.auto: expected a locale tag, got %s", jsonValueKind(raw))
		}
		return SkyADT{SkyOwner: owner, Tag: 0, SkyName: i18nLocaleCtor, Fields: []any{s}}, nil
	}
	obj, ok := jsonObjectMap(raw)
	if !ok {
		return SkyADT{}, fmt.Errorf("Codec.auto: expected a Text object, got %s", jsonValueKind(raw))
	}
	if v, ok := obj["verbatim"]; ok {
		s, ok := v.(string)
		if !ok || len(obj) != 1 {
			return SkyADT{}, fmt.Errorf("Codec.auto: a verbatim Text is {\"verbatim\": string}")
		}
		return SkyADT{SkyOwner: owner, Tag: 1, SkyName: i18nTextVerbatim, Fields: []any{s}}, nil
	}
	key, ok := obj["key"].(string)
	if !ok {
		return SkyADT{}, fmt.Errorf("Codec.auto: a Text needs \"key\" (a string)")
	}
	rawArgs, _ := obj["args"].([]any)
	if _, present := obj["args"]; present && rawArgs == nil {
		if obj["args"] != nil {
			return SkyADT{}, fmt.Errorf("Codec.auto: Text \"args\" must be an array")
		}
	}
	args := make([]any, 0, len(rawArgs))
	for _, a := range rawArgs {
		t, ok := a.([]any)
		if !ok || len(t) != 3 {
			return SkyADT{}, fmt.Errorf("Codec.auto: a Text argument is [name, kind, payload]")
		}
		var s [3]string
		for i := range t {
			if s[i], ok = t[i].(string); !ok {
				return SkyADT{}, fmt.Errorf("Codec.auto: a Text argument is three strings")
			}
		}
		if len(s[1]) != 1 || !strings.Contains("sidmptDl", s[1]) {
			return SkyADT{}, fmt.Errorf("Codec.auto: unknown Text argument kind %q", s[1])
		}
		args = append(args, SkyTuple3{V0: s[0], V1: s[1], V2: s[2]})
	}
	return SkyADT{SkyOwner: owner, Tag: 0, SkyName: i18nTextMessage, Fields: []any{key, args}}, nil
}

// i18nPlausibleTag accepts the characters of a BCP 47 tag. Std.I18n parses
// the tag again where it uses it.
func i18nPlausibleTag(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
