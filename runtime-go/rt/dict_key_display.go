package rt

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Rendering an encoded Dict key for a HUMAN.
//
// `encodeDictKey` writes a kind tag into the runtime map key so that a
// key-polymorphic helper can decode it back (see the typed-key section of
// rt.go). That tag is an INTERNAL representation and never reaches a person:
// `Debug.toString` renders a Dict through `dictEntries` (sky_show.go), and the
// session-store inspector names a key through `detagDisplayKey`.

// detagDisplayKey turns one encoded map key back into what the user wrote.
// An untagged key is its own display form.
func detagDisplayKey(raw string) string {
	if k, _, ok := decodeTaggedDictKey(raw); ok {
		if s, isStr := k.(string); isStr {
			return s
		}
		return fmt.Sprintf("%v", k)
	}
	return raw
}

// dictJSONKey is the JSON object key Codec.auto writes for one stored Dict key.
//
// A non-String key is written as its plain text: the decimal of an Int
// (`"10"`), the `%v` text of a Float or Bool, the code point of a Char. Before
// v0.29.9 the stored form (`"\u0001i10"`) reached the JSON, which a Sky
// decoder read back but every other JSON reader saw as a wrong key. The plain
// text is the bare form the runtime already reads for a Dict that came from
// outside (a foreign Dict probes it, see dictProbe), so a decoder that does not
// know the key type still finds every entry.
//
// A String key is written as stored. That keeps the escape of a String key that
// starts with the tag byte (`"\u0001s…"`), so it never reads back as an Int key.
func dictJSONKey(stored string) string {
	if len(stored) >= 2 && stored[0] == dictKeyTagByte {
		switch stored[1] {
		case 'i', 'f', 'c', 'b':
			return stored[2:]
		}
	}
	return stored
}

// codecDeclaredDict splits a declared Dict type into its key kind and its
// value type: `rt.SkyDict[@i:V]` → ('i', "V"), `rt.SkyDict[V]` → (0, "V").
// The compiler writes the key kind (`lower::ir::declared_ty`) for a key that
// is not a String, because the Go type `rt.SkyDict[V]` erases it.
func codecDeclaredDict(declared string) (kind byte, val string, ok bool) {
	inner, ok := strings.CutPrefix(declared, "rt.SkyDict[")
	if !ok {
		return 0, "", false
	}
	inner = strings.TrimSuffix(inner, "]")
	if len(inner) >= 3 && inner[0] == '@' && inner[2] == ':' {
		switch inner[1] {
		case 'i', 'f', 'c', 'b':
			return inner[1], inner[3:], true
		}
	}
	return 0, inner, true
}

// dictKeysFromJSON turns the object keys a decode read into the stored keys of
// a Dict whose key kind is `kind` ('i' Int, 'f' Float, 'c' Char, 'b' Bool; 0 a
// String or an unknown key). It inverts dictJSONKey: a plain key in its
// canonical text (`"10"`) becomes the stored key (`"\x01i10"`), so the decoded
// Dict is the one the program built and every operation, a key-generic one
// included, reads the key as its own type. A key already in the stored form
// (written by v0.29.8 and earlier) is kept. A key that is not the canonical
// text of the kind, or whose stored form the document also holds, is kept as
// read. Both decode paths (the walker and the generated codecs) call this.
func dictKeysFromJSON(kind byte, keys []string) []string {
	if kind == 0 {
		return keys
	}
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		seen[k] = true
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k
		enc, ok := dictStoredKey(kind, k)
		if ok && !seen[enc] {
			out[i] = enc
			seen[enc] = true
		}
	}
	return out
}

// dictStoredKey is the stored key for the canonical plain text `k` of a key of
// kind `kind`, or false when `k` is not that text.
func dictStoredKey(kind byte, k string) (string, bool) {
	switch kind {
	case 'i':
		if n, err := strconv.Atoi(k); err == nil && strconv.Itoa(n) == k {
			return encodeDictKey(n), true
		}
	case 'f':
		if f, err := strconv.ParseFloat(k, 64); err == nil && fmt.Sprintf("%v", f) == k {
			return encodeDictKey(f), true
		}
	case 'c':
		if n, err := strconv.Atoi(k); err == nil && strconv.Itoa(n) == k && n >= 0 && n <= 0x10FFFF {
			return encodeDictKey(rune(n)), true
		}
	case 'b':
		if k == "true" || k == "false" {
			return encodeDictKey(k == "true"), true
		}
	}
	return "", false
}

// codecRetagDicts converts the keys of every Dict in `v` (a value the walker
// decoded by its Go type alone, a custom type's payload) by the key kinds its
// declared type names. It follows the containers a declared type spells: a
// Dict, a List, a Maybe and a tuple. A record and a custom type inside `v` are
// decoded by their own tags, so their Dicts are already converted.
func codecRetagDicts(v reflect.Value, declared string) reflect.Value {
	if !v.IsValid() || declared == "" || !strings.Contains(declared, "@") {
		return v
	}
	t := v.Type()
	if kind, val, ok := codecDeclaredDict(declared); ok && isSkyDictType(t) && v.CanInterface() {
		d := v.Interface().(skyDictAny)
		var keys []string
		var vals []any
		d.dictRangeAny(func(k string, x any) bool {
			keys = append(keys, k)
			vals = append(vals, x)
			return true
		})
		vt := d.dictValueType()
		for i := range vals {
			xv := reflect.ValueOf(vals[i])
			if !xv.IsValid() {
				continue
			}
			if fixed := codecRetagDicts(xv, val); fixed.IsValid() && fixed.Type().AssignableTo(vt) {
				vals[i] = fixed.Interface()
			}
		}
		out, err := d.dictFromEntries(dictKeysFromJSON(kind, keys), vals, d.dictForeign())
		if err != nil {
			return v
		}
		return reflect.ValueOf(out)
	}
	if elem, ok := strings.CutPrefix(declared, "[]"); ok && t.Kind() == reflect.Slice {
		out := reflect.MakeSlice(t, v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(codecRetagDicts(v.Index(i), elem))
		}
		return out
	}
	if inner, ok := strings.CutPrefix(declared, "rt.SkyMaybe["); ok && isSkyMaybeType(t) {
		out := reflect.New(t).Elem()
		out.Set(v)
		jv := out.FieldByName("JustValue")
		if jv.CanSet() {
			jv.Set(codecRetagDicts(jv, strings.TrimSuffix(inner, "]")))
		}
		return out
	}
	if args := codecDeclaredTupleArgs(declared); args != nil && t.Kind() == reflect.Struct && !isSkyAdtType(t) && t.NumField() == len(args) {
		out := reflect.New(t).Elem()
		out.Set(v)
		for i := range args {
			if f := out.Field(i); f.CanSet() {
				f.Set(codecRetagDicts(f, args[i]))
			}
		}
		return out
	}
	return v
}
