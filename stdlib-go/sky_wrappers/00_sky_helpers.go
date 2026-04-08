package sky_wrappers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

type SkyResult struct {
	Tag int
	SkyName string
	OkValue any
	ErrValue any
}

func SkyOk(v any) SkyResult {
	return SkyResult{Tag: 0, SkyName: "Ok", OkValue: sky_normalizeValue(v)}
}

func SkyErr(e any) SkyResult {
	return SkyResult{Tag: 1, SkyName: "Err", ErrValue: e}
}

type SkyMaybe struct {
	Tag int
	SkyName string
	JustValue any
}

func SkyJust(v any) SkyMaybe {
	return SkyMaybe{Tag: 0, SkyName: "Just", JustValue: sky_normalizeValue(v)}
}

func SkyNothing() SkyMaybe {
	return SkyMaybe{Tag: 1, SkyName: "Nothing"}
}

type Tuple2 struct {
    V0 any
    V1 any
}

type Tuple3 struct {
    V0 any
    V1 any
    V2 any
}

var CmdNone any = struct{ Tag int }{Tag: 0}
var SubNone any = struct{ Tag int }{Tag: 0}

func UpdateRecord(base any, update map[string]any) any {
    // Very naive record update for map-based records
    m, ok := base.(map[string]any)
    if !ok {
        return base
    }
    newMap := make(map[string]any)
    for k, v := range m {
        newMap[k] = v
    }
    for k, v := range update {
        newMap[k] = v
    }
    return newMap
}

// Identity function: passes any value through unchanged.
// Used by Sky's Db.intVal/boolVal/floatVal to store typed values in Dicts.
func Sky_Identity(v any) any { return v }

// Sky_ToJSON marshals any Go value to a JSON string.
// This is the universal FFI bridge — complex Go types that can't be
// directly mapped to Sky types are serialized as JSON. The developer
// uses Sky's Decode module to extract typed values.
func Sky_ToJSON(v any) any {
	bytes, err := json.Marshal(v)
	if err != nil { return "" }
	return string(bytes)
}

// Sky_FromJSON is the inverse — parses a JSON string into a Go any value.
func Sky_FromJSON(s any) any {
	var result any
	err := json.Unmarshal([]byte(sky_asString(s)), &result)
	if err != nil { return SkyErr(err.Error()) }
	return SkyOk(result)
}

// sky_normalizeValue converts Go-typed values to Sky-compatible types.
// Called at the FFI boundary to ensure all values use Sky's expected types:
//   - int64/int32/uint/etc → int (Sky's Int)
//   - float32 → float64 (Sky's Float)
//   - typed slices → []any
//   - typed maps → map[string]any
// This is the single normalization point for ALL Go→Sky value passing.
func sky_normalizeValue(v any) any {
	if v == nil { return v }
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(rv.Uint())
	case reflect.Float32:
		return rv.Float()
	case reflect.Slice:
		if _, ok := v.([]byte); ok { return v }
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = sky_normalizeValue(rv.Index(i).Interface())
		}
		return result
	case reflect.Map:
		// Only normalize map values (recursively) for Sky-compatible map types.
		// Keep other map types as-is (they might be opaque handles).
		if m, ok := v.(map[string]any); ok {
			for k, val := range m { m[k] = sky_normalizeValue(val) }
			return m
		}
		if m, ok := v.(map[any]any); ok {
			for k, val := range m { m[k] = sky_normalizeValue(val) }
			return m
		}
		// Convert other typed maps to map[string]any with normalized values
		result := make(map[string]any, rv.Len())
		for _, key := range rv.MapKeys() {
			result[fmt.Sprintf("%v", key.Interface())] = sky_normalizeValue(rv.MapIndex(key).Interface())
		}
		return result
	case reflect.Struct:
		// NEVER normalize Sky's own runtime types
		switch v.(type) {
		case SkyResult, SkyMaybe, Tuple2, Tuple3:
			return v
		}
		// All other structs: keep as-is (opaque handles).
		// Developers use Sky_ToJSON for explicit data extraction.
		return v
	case reflect.Ptr:
		if rv.IsNil() { return nil }
		// Pointers to data types (primitives, slices, maps) are nullable values —
		// dereference and normalize. Pointers to structs/interfaces are opaque handles.
		switch rv.Elem().Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64,
			reflect.Slice, reflect.Map, reflect.Array:
			return sky_normalizeValue(rv.Elem().Interface())
		}
		// Pointers to structs/interfaces are opaque handles — keep as-is.
		return v
	}
	return v
}

// ============= Safe Assertion Helpers =============
// Handle ALL Go numeric types (int, int8-64, uint, uint8-64, float32/64)
// that different Go libraries return. JSON returns float64, Firestore
// returns int64, other libs may return int32, uint, etc.

func sky_asInt(v any) int {
	if v == nil { return 0 }
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return int(rv.Float())
	}
	return 0
}

func sky_asString(v any) string {
	if v == nil { return "" }
	if s, ok := v.(string); ok { return s }
	// Handle fmt.Stringer interface (many Go types implement this)
	if s, ok := v.(interface{ String() string }); ok { return s.String() }
	return fmt.Sprintf("%v", v)
}

func sky_asBool(v any) bool {
	if v == nil { return false }
	if b, ok := v.(bool); ok { return b }
	// Handle string bools from forms/JSON
	if s, ok := v.(string); ok { return s == "true" || s == "1" || s == "on" }
	return false
}

func sky_asFloat(v any) float64 {
	if v == nil { return 0 }
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint())
	}
	return 0
}

func sky_asList(v any) []any {
	if l, ok := v.([]any); ok { return l }
	// Handle typed slices (e.g., []*firestore.DocumentSnapshot) via reflection
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = rv.Index(i).Interface()
		}
		return result
	}
	return []any{}
}

func v any.(func(any) any) func(any) any {
	if f, ok := v.(func(any) any); ok { return f }
	return func(_ any) any { return nil }
}

func sky_asMap(v any) map[string]any {
	if v == nil { return map[string]any{} }
	if m, ok := v.(map[string]any); ok { return m }
	// Handle map[any]any (Sky's Dict type)
	if m, ok := v.(map[any]any); ok {
		result := make(map[string]any, len(m))
		for k, val := range m { result[fmt.Sprintf("%v", k)] = val }
		return result
	}
	// Handle Tuple structs — convert to map for pattern match field access
	if t, ok := v.(Tuple2); ok {
		return map[string]any{"Tag": 0, "Tuple2Value": t.V0, "Tuple2Value1": t.V1}
	}
	if t, ok := v.(Tuple3); ok {
		return map[string]any{"Tag": 0, "Tuple3Value": t.V0, "Tuple3Value1": t.V1, "Tuple3Value2": t.V2}
	}
	// Handle any other map type via reflection
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map {
		result := make(map[string]any, rv.Len())
		for _, key := range rv.MapKeys() {
			result[fmt.Sprintf("%v", key.Interface())] = rv.MapIndex(key).Interface()
		}
		return result
	}
	return map[string]any{}
}

func sky_asMapAny(v any) map[any]any {
	if m, ok := v.(map[any]any); ok { return m }
	// Also handle map[string]any (from Go stdlib, JSON, Sky.Live runtime, etc.)
	if m, ok := v.(map[string]any); ok {
		result := make(map[any]any, len(m))
		for k, val := range m { result[k] = val }
		return result
	}
	return map[any]any{}
}

func sky_asTuple2(v any) Tuple2 {
	if t, ok := v.(Tuple2); ok { return t }
	return SkyTuple2{}
}

func sky_asTuple3(v any) Tuple3 {
	if t, ok := v.(Tuple3); ok { return t }
	return SkyTuple3{}
}

func sky_asSkyResult(v any) SkyResult {
	if r, ok := v.(SkyResult); ok { return r }
	return SkyResult{}
}

func sky_asSkyMaybe(v any) SkyMaybe {
	if m, ok := v.(SkyMaybe); ok { return m }
	return SkyMaybe{}
}

func sky_getTag(v reflect.Value) int {
	if v.Kind() != reflect.Struct { return -1 }
	tag := v.FieldByName("Tag")
	if !tag.IsValid() { return -1 }
	if i, ok := tag.Interface().(int); ok { return i }
	return -1
}

// Exported aliases for use by compiled Sky code (main/state packages)
func Sky_AsInt(v any) int { return sky_asInt(v) }
func Sky_AsString(v any) string { return sky_asString(v) }
func Sky_AsBool(v any) bool { return sky_asBool(v) }
func Sky_AsFloat(v any) float64 { return sky_asFloat(v) }
func Sky_AsList(v any) []any { return sky_asList(v) }
func Sky_AsFunc(v any) func(any) any { return v.(func(any) any) }
func Sky_AsMap(v any) map[string]any { return sky_asMap(v) }
func Sky_AsMapAny(v any) map[any]any { return sky_asMapAny(v) }
func Sky_AsTuple2(v any) Tuple2 { return sky_asTuple2(v) }
func Sky_AsTuple3(v any) Tuple3 { return sky_asTuple3(v) }
func Sky_AsSkyResult(v any) SkyResult { return sky_asSkyResult(v) }
func Sky_AsSkyMaybe(v any) SkyMaybe { return sky_asSkyMaybe(v) }

// ============= List Operations =============

func Sky_list_Map(fn any, list any) any {
	lst := sky_asList(list)
	result := make([]any, len(lst))
	for i, item := range lst {
		result[i] = fn.(func(any) any)(item)
	}
	return result
}

func Sky_list_Filter(fn any, list any) any {
	lst := sky_asList(list)
	result := []any{}
	for _, item := range lst {
		if fn.(func(any) any)(item) == true {
			result = append(result, item)
		}
	}
	return result
}

func Sky_list_Foldl(fn any, acc any, list any) any {
	lst := sky_asList(list)
	result := acc
	for _, item := range lst {
		result = sky_asFunc(fn.(func(any) any)(item))(result)
	}
	return result
}

func Sky_list_Foldr(fn any, acc any, list any) any {
	lst := sky_asList(list)
	result := acc
	for i := len(lst) - 1; i >= 0; i-- {
		result = sky_asFunc(fn.(func(any) any)(lst[i]))(result)
	}
	return result
}

func Sky_list_Head(list any) any {
	lst := sky_asList(list)
	if len(lst) == 0 {
		return SkyNothing() // Nothing
	}
	return SkyJust(lst[0])
}

func Sky_list_Tail(list any) any {
	lst := sky_asList(list)
	if len(lst) == 0 {
		return SkyNothing() // Nothing
	}
	tail := make([]any, len(lst)-1)
	copy(tail, lst[1:])
	return SkyJust(tail)
}

func Sky_list_Length(list any) any {
	lst := sky_asList(list)
	return len(lst)
}

func Sky_list_Append(a any, b any) any {
	lstA := sky_asList(a)
	lstB := sky_asList(b)
	result := make([]any, 0, len(lstA)+len(lstB))
	result = append(result, lstA...)
	result = append(result, lstB...)
	return result
}

func Sky_list_Reverse(list any) any {
	lst := sky_asList(list)
	result := make([]any, len(lst))
	for i, item := range lst {
		result[len(lst)-1-i] = item
	}
	return result
}

func Sky_list_Member(item any, list any) any {
	lst := sky_asList(list)
	for _, v := range lst {
		if reflect.DeepEqual(v, item) { return true }
	}
	return false
}

func Sky_list_Range(from any, to any) any {
	f, ok1 := from.(int)
	t, ok2 := to.(int)
	if !ok1 || !ok2 { return []any{} }
	if f > t { return []any{} }
	result := make([]any, 0, t-f+1)
	for i := f; i <= t; i++ {
		result = append(result, i)
	}
	return result
}

func Sky_list_IsEmpty(list any) any {
	lst := sky_asList(list)
	return len(lst) == 0
}

func Sky_list_Take(n any, list any) any {
	count, ok1 := n.(int)
	lst := sky_asList(list)
	if !ok1 { return []any{} }
	if count > len(lst) { count = len(lst) }
	if count < 0 { count = 0 }
	result := make([]any, count)
	copy(result, lst[:count])
	return result
}

func Sky_list_Drop(n any, list any) any {
	count, ok1 := n.(int)
	lst := sky_asList(list)
	if !ok1 { return []any{} }
	if count > len(lst) { count = len(lst) }
	if count < 0 { count = 0 }
	result := make([]any, len(lst)-count)
	copy(result, lst[count:])
	return result
}

func sky_compareValues(a, b any) int {
	switch av := a.(type) {
	case int:
		switch bv := b.(type) {
		case int:
			if av < bv { return -1 }
			if av > bv { return 1 }
			return 0
		case float64:
			if float64(av) < bv { return -1 }
			if float64(av) > bv { return 1 }
			return 0
		}
		bv := 0
		if av < bv { return -1 }
		if av > bv { return 1 }
		return 0
	case float64:
		switch bv := b.(type) {
		case float64:
			if av < bv { return -1 }
			if av > bv { return 1 }
			return 0
		case int:
			bvf := float64(bv)
			if av < bvf { return -1 }
			if av > bvf { return 1 }
			return 0
		}
		var bv float64
		if av < bv { return -1 }
		if av > bv { return 1 }
		return 0
	case string:
		bv := fmt.Sprintf("%v", b)
		if av < bv { return -1 }
		if av > bv { return 1 }
		return 0
	default:
		as, bs := fmt.Sprintf("%v", a), fmt.Sprintf("%v", b)
		if as < bs { return -1 }
		if as > bs { return 1 }
		return 0
	}
}

func Sky_list_Sort(list any) any {
	lst := sky_asList(list)
	result := make([]any, len(lst))
	copy(result, lst)
	sort.Slice(result, func(i, j int) bool {
		return sky_compareValues(result[i], result[j]) < 0
	})
	return result
}

func Sky_list_Intersperse(sep any, list any) any {
	lst := sky_asList(list)
	if len(lst) <= 1 { return lst }
	result := make([]any, 0, len(lst)*2-1)
	for i, item := range lst {
		if i > 0 { result = append(result, sep) }
		result = append(result, item)
	}
	return result
}

func Sky_list_Concat(lists any) any {
	lst := sky_asList(lists)
	result := []any{}
	for _, item := range lst {
		inner, ok := item.([]any)
		if ok { result = append(result, inner...) }
	}
	return result
}

func Sky_list_ConcatMap(fn any, list any) any {
	lst := sky_asList(list)
	result := []any{}
	for _, item := range lst {
		inner := fn.(func(any) any)(item)
		if innerLst, ok := inner.([]any); ok {
			result = append(result, innerLst...)
		}
	}
	return result
}

func Sky_list_IndexedMap(fn any, list any) any {
	lst := sky_asList(list)
	result := make([]any, len(lst))
	for i, item := range lst {
		result[i] = sky_asFunc(fn.(func(any) any)(i))(item)
	}
	return result
}

func Sky_list_Zip(listA any, listB any) any {
	a := sky_asList(listA)
	b := sky_asList(listB)
	n := len(a); if len(b) < n { n = len(b) }
	result := make([]any, n)
	for i := 0; i < n; i++ { result[i] = SkyTuple2{a[i], b[i]} }
	return result
}

func Sky_list_Unzip(list any) any {
	lst := sky_asList(list)
	as := make([]any, len(lst))
	bs := make([]any, len(lst))
	for i, item := range lst {
		t := sky_asTuple2(item)
		as[i] = t.V0
		bs[i] = t.V1
	}
	return SkyTuple2{as, bs}
}

func Sky_list_Map2(fn any, listA any, listB any) any {
	a := sky_asList(listA)
	b := sky_asList(listB)
	f := fn.(func(any) any)
	n := len(a); if len(b) < n { n = len(b) }
	result := make([]any, n)
	for i := 0; i < n; i++ { result[i] = f(a[i].(func(any) any))(b[i]) }
	return result
}

func Sky_list_Maximum(list any) any {
	lst := sky_asList(list)
	if len(lst) == 0 { return SkyNothing() }
	best := lst[0]
	for _, item := range lst[1:] {
		if sky_compareValues(item, best) > 0 { best = item }
	}
	return SkyJust(best)
}

func Sky_list_Minimum(list any) any {
	lst := sky_asList(list)
	if len(lst) == 0 { return SkyNothing() }
	best := lst[0]
	for _, item := range lst[1:] {
		if sky_compareValues(item, best) < 0 { best = item }
	}
	return SkyJust(best)
}

func Sky_list_Find(pred any, list any) any {
	lst := sky_asList(list)
	if len(lst) == 0 { return SkyNothing() }
	fn := pred.(func(any) any)
	for _, item := range lst {
		if sky_asBool(fn(item)) { return SkyJust(item) }
	}
	return SkyNothing()
}

func Sky_list_FilterMap(fn any, list any) any {
	lst := sky_asList(list)
	f := fn.(func(any) any)
	result := []any{}
	for _, item := range lst {
		maybe := f(item)
		if m, ok := maybe.(SkyMaybe); ok && m.Tag == 0 {
			result = append(result, m.JustValue)
		}
	}
	return result
}

// ============= String Operations =============

func Sky_string_Split(sep any, s any) any {
	parts := strings.Split(sky_asString(s), sky_asString(sep))
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func Sky_string_Join(sep any, list any) any {
	lst := sky_asList(list)
	parts := make([]string, len(lst))
	for i, p := range lst { parts[i] = fmt.Sprintf("%v", p) }
	return strings.Join(parts, sky_asString(sep))
}

func Sky_string_Contains(sub any, s any) any {
	return strings.Contains(sky_asString(s), sky_asString(sub))
}

func Sky_string_Replace(old any, new_ any, s any) any {
	return strings.ReplaceAll(sky_asString(s), sky_asString(old), sky_asString(new_))
}

func Sky_string_Trim(s any) any {
	return strings.TrimSpace(sky_asString(s))
}

func Sky_string_Length(s any) any {
	return len([]rune(sky_asString(s)))
}

func Sky_string_ToLower(s any) any {
	return strings.ToLower(sky_asString(s))
}

func Sky_string_ToUpper(s any) any {
	return strings.ToUpper(sky_asString(s))
}

func Sky_string_StartsWith(prefix any, s any) any {
	return strings.HasPrefix(sky_asString(s), sky_asString(prefix))
}

func Sky_string_EndsWith(suffix any, s any) any {
	return strings.HasSuffix(sky_asString(s), sky_asString(suffix))
}

func Sky_string_Slice(start any, end any, s any) any {
	runes := []rune(sky_asString(s))
	st := sky_asInt(start)
	en := sky_asInt(end)
	if st < 0 { st = 0 }
	if en > len(runes) { en = len(runes) }
	if st > en { return "" }
	return string(runes[st:en])
}

func Sky_string_IsEmpty(s any) any {
	return sky_asString(s) == ""
}

func Sky_string_FromFloat(f any) any {
	return fmt.Sprintf("%g", f)
}

func Sky_string_ToInt(s any) any {
	var n int
	_, err := fmt.Sscanf(sky_asString(s), "%d", &n)
	if err != nil { return SkyNothing() }
	return SkyJust(n)
}

func Sky_string_ToFloat(s any) any {
	var f float64
	_, err := fmt.Sscanf(sky_asString(s), "%g", &f)
	if err != nil { return SkyNothing() }
	return SkyJust(f)
}

func Sky_string_Lines(s any) any {
	parts := strings.Split(sky_asString(s), "\n")
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func Sky_string_Words(s any) any {
	parts := strings.Fields(sky_asString(s))
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func Sky_string_Repeat(n any, s any) any {
	return strings.Repeat(sky_asString(s), sky_asInt(n))
}

func Sky_string_PadLeft(n any, ch any, s any) any {
	str := sky_asString(s)
	runes := []rune(str)
	target := sky_asInt(n)
	if len(runes) >= target { return str }
	padding := strings.Repeat(sky_asString(ch), target - len(runes))
	return padding + str
}

func Sky_string_PadRight(n any, ch any, s any) any {
	str := sky_asString(s)
	runes := []rune(str)
	target := sky_asInt(n)
	if len(runes) >= target { return str }
	padding := strings.Repeat(sky_asString(ch), target - len(runes))
	return str + padding
}

func Sky_string_Left(n any, s any) any {
	runes := []rune(sky_asString(s))
	count := sky_asInt(n)
	if count > len(runes) { count = len(runes) }
	return string(runes[:count])
}

func Sky_string_Right(n any, s any) any {
	runes := []rune(sky_asString(s))
	count := sky_asInt(n)
	if count > len(runes) { count = len(runes) }
	return string(runes[len(runes)-count:])
}

func Sky_string_Reverse(s any) any {
	runes := []rune(sky_asString(s))
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func Sky_string_Indexes(sub any, s any) any {
	str := sky_asString(s)
	substr := sky_asString(sub)
	result := []any{}
	start := 0
	for {
		idx := strings.Index(str[start:], substr)
		if idx == -1 { break }
		result = append(result, start+idx)
		start += idx + len(substr)
	}
	return result
}

func Sky_string_FromBytes(b any) any {
	if bs, ok := b.([]byte); ok { return string(bs) }
	return fmt.Sprintf("%v", b)
}

// ============= Append (++ operator) =============

// Sky_Append implements the ++ operator with runtime dispatch:
// - []any ++ []any → list concatenation (append)
// - string ++ string → string concatenation
func Sky_Append(a any, b any) any {
	if la, ok := a.([]any); ok {
		if lb, ok := b.([]any); ok {
			return append(la, lb...)
		}
		return la
	}
	return sky_asString(a) + sky_asString(b)
}

// ============= Dict Operations =============

func Sky_dict_Empty() any {
	return map[any]any{}
}

func Sky_dict_Singleton(key any, val any) any {
	return map[any]any{key: val}
}

func Sky_dict_Insert(key any, val any, dict any) any {
	m := sky_asMapAny(dict)
	result := make(map[any]any, len(m)+1)
	for k, v := range m { result[k] = v }
	result[key] = val
	return result
}

func Sky_dict_Get(key any, dict any) any {
	m := sky_asMapAny(dict)
	val, ok := m[key]
	if !ok { return SkyNothing() } // Nothing
	return SkyJust(val)
}

func Sky_dict_Remove(key any, dict any) any {
	m := sky_asMapAny(dict)
	result := make(map[any]any, len(m))
	for k, v := range m {
		if k != key { result[k] = v }
	}
	return result
}

func Sky_dict_Keys(dict any) any {
	m := sky_asMapAny(dict)
	result := make([]any, 0, len(m))
	for k := range m { result = append(result, k) }
	return result
}

func Sky_dict_Values(dict any) any {
	m := sky_asMapAny(dict)
	result := make([]any, 0, len(m))
	for _, v := range m { result = append(result, v) }
	return result
}

func Sky_dict_Map(fn any, dict any) any {
	m := sky_asMapAny(dict)
	result := make(map[any]any, len(m))
	for k, v := range m {
		result[k] = sky_asFunc(fn.(func(any) any)(k))(v)
	}
	return result
}

func Sky_dict_Foldl(fn any, acc any, dict any) any {
	m := sky_asMapAny(dict)
	result := acc
	for k, v := range m {
		result = sky_asFunc(sky_asFunc(fn.(func(any) any)(k))(v))(result)
	}
	return result
}

func Sky_dict_FromList(list any) any {
	lst := sky_asList(list)
	result := make(map[any]any, len(lst))
	for _, item := range lst {
		tuple := reflect.ValueOf(item)
		key := tuple.FieldByName("V0").Interface()
		val := tuple.FieldByName("V1").Interface()
		result[key] = val
	}
	return result
}

func Sky_dict_ToList(dict any) any {
	m := sky_asMapAny(dict)
	result := make([]any, 0, len(m))
	for k, v := range m {
		result = append(result, SkyTuple2{V0: k, V1: v})
	}
	return result
}

func Sky_dict_IsEmpty(dict any) any {
	m := sky_asMapAny(dict)
	return len(m) == 0
}

func Sky_dict_Size(dict any) any {
	m := sky_asMapAny(dict)
	return len(m)
}

func Sky_dict_Member(key any, dict any) any {
	m := sky_asMapAny(dict)
	_, ok := m[key]
	return ok
}

func Sky_dict_Update(key any, fn any, dict any) any {
	m := sky_asMapAny(dict)
	result := make(map[any]any, len(m))
	for k, v := range m { result[k] = v }
	val, ok := m[key]
	var maybeVal any
	if ok {
		maybeVal = SkyJust(val)
	} else {
		maybeVal = SkyNothing()
	}
	newMaybe := fn.(func(any) any)(maybeVal)
	if m, ok := newMaybe.(SkyMaybe); ok && m.Tag == 0 {
		result[key] = m.JustValue
	} else {
		delete(result, key)
	}
	return result
}

// ============= JSON Operations =============

func Sky_json_Encode(indent any, value any) any {
	n := sky_asInt(indent)
	var data []byte
	var err error
	if n > 0 {
		data, err = json.MarshalIndent(value, "", strings.Repeat(" ", n))
	} else {
		data, err = json.Marshal(value)
	}
	if err != nil { return "" }
	return string(data)
}

func Sky_json_DecodeString(s any) any {
	var result any
	err := json.Unmarshal([]byte(sky_asString(s)), &result)
	if err != nil { return SkyErr(err.Error()) }
	return SkyOk(result)
}

func Sky_json_EncodeString(s any) any { return s }
func Sky_json_EncodeInt(n any) any { return n }
func Sky_json_EncodeFloat(f any) any { return f }
func Sky_json_EncodeBool(b any) any { return b }
func Sky_json_EncodeNull() any { return nil }

func Sky_json_EncodeList(fn any, list any) any {
	lst := sky_asList(list)
	result := make([]any, len(lst))
	for i, item := range lst {
		result[i] = fn.(func(any) any)(item)
	}
	return result
}

func Sky_json_EncodeObject(pairs any) any {
	lst := sky_asList(pairs)
	result := make(map[string]any, len(lst))
	for _, item := range lst {
		tuple := reflect.ValueOf(item)
		key := fmt.Sprintf("%v", tuple.FieldByName("V0").Interface())
		val := tuple.FieldByName("V1").Interface()
		result[key] = val
	}
	return result
}

func Sky_json_GetField(field any, obj any) any {
	m, ok := obj.(map[string]any)
	if !ok { return SkyErr("not an object") }
	f := sky_asString(field)
	val, exists := m[f]
	if !exists { return SkyErr("field not found: " + f) }
	return SkyOk(val)
}

func Sky_json_AsString(val any) any {
	s, ok := val.(string)
	if !ok { return SkyErr("not a string") }
	return SkyOk(s)
}

func Sky_json_AsInt(val any) any {
	switch v := val.(type) {
	case float64: return SkyOk(int(v))
	case int: return SkyOk(v)
	default: return SkyErr("not a number")
	}
}

func Sky_json_AsFloat(val any) any {
	switch v := val.(type) {
	case float64: return SkyOk(v)
	case int: return SkyOk(float64(v))
	default: return SkyErr("not a number")
	}
}

func Sky_json_AsBool(val any) any {
	b, ok := val.(bool)
	if !ok { return SkyErr("not a bool") }
	return SkyOk(b)
}

func Sky_json_AsList(val any) any {
	lst, ok := val.([]any)
	if !ok { return SkyErr("not a list") }
	return SkyOk(lst)
}

func Sky_json_AsNullable(val any) any {
	if val == nil { return SkyOk(SkyNothing()) } // Ok Nothing
	return SkyOk(SkyJust(val))
}

func Sky_json_At(keys any, obj any) any {
	lst := sky_asList(keys)
	current := obj
	for _, key := range lst {
		m, ok := current.(map[string]any)
		if !ok { return SkyErr("not an object at path") }
		k := sky_asString(key)
		val, exists := m[k]
		if !exists { return SkyErr("field not found: " + k) }
		current = val
	}
	return SkyOk(current)
}

func Sky_json_DecodeList(fn any, val any) any {
	lst, ok := val.([]any)
	if !ok { return SkyErr("not a list") }
	result := make([]any, 0, len(lst))
	for i, item := range lst {
		decoded := fn.(func(any) any)(item)
		dv := reflect.ValueOf(decoded)
		if dv.Kind() == reflect.Struct {
			tag := dv.FieldByName("Tag")
			if tag.IsValid() && sky_getTag(dv) == 1 {
				return SkyErr(fmt.Sprintf("decode error at index %d", i))
			}
			result = append(result, dv.FieldByName("OkValue").Interface())
		} else {
			result = append(result, decoded)
		}
	}
	return SkyOk(result)
}

func Sky_json_Map2(fn any, r1 any, r2 any) any {
	v1 := reflect.ValueOf(r1)
	v2 := reflect.ValueOf(r2)
	if sky_getTag(v1) == 1 {
		return r1
	}
	if sky_getTag(v2) == 1 {
		return r2
	}
	val1 := v1.FieldByName("OkValue").Interface()
	val2 := v2.FieldByName("OkValue").Interface()
	f := fn.(func(any) any)
	result := f(val1.(func(any) any))(val2)
	return SkyOk(result)
}

func Sky_json_Map3(fn any, r1 any, r2 any, r3 any) any {
	v1 := reflect.ValueOf(r1)
	v2 := reflect.ValueOf(r2)
	v3 := reflect.ValueOf(r3)
	if sky_getTag(v1) == 1 { return r1 }
	if sky_getTag(v2) == 1 { return r2 }
	if sky_getTag(v3) == 1 { return r3 }
	val1 := v1.FieldByName("OkValue").Interface()
	val2 := v2.FieldByName("OkValue").Interface()
	val3 := v3.FieldByName("OkValue").Interface()
	f := fn.(func(any) any)
	result := sky_asFunc(f(val1.(func(any) any))(val2))(val3)
	return SkyOk(result)
}

func Sky_json_Map4(fn any, r1 any, r2 any, r3 any, r4 any) any {
	v1 := reflect.ValueOf(r1)
	v2 := reflect.ValueOf(r2)
	v3 := reflect.ValueOf(r3)
	v4 := reflect.ValueOf(r4)
	if sky_getTag(v1) == 1 { return r1 }
	if sky_getTag(v2) == 1 { return r2 }
	if sky_getTag(v3) == 1 { return r3 }
	if sky_getTag(v4) == 1 { return r4 }
	val1 := v1.FieldByName("OkValue").Interface()
	val2 := v2.FieldByName("OkValue").Interface()
	val3 := v3.FieldByName("OkValue").Interface()
	val4 := v4.FieldByName("OkValue").Interface()
	f := fn.(func(any) any)
	result := sky_asFunc(sky_asFunc(f(val1.(func(any) any))(val2))(val3))(val4)
	return SkyOk(result)
}

// ============= Composable JSON Decoder Operations =============
// A Decoder is a func(any) any: takes JSON value, returns SkyResult

func Sky_json_decoder_String() any {
	return func(val any) any {
		s, ok := val.(string)
		if !ok { return SkyErr("Expecting a STRING") }
		return SkyOk(s)
	}
}

func Sky_json_decoder_Int() any {
	return func(val any) any {
		switch v := val.(type) {
		case float64: return SkyOk(int(v))
		case int: return SkyOk(v)
		default: return SkyErr("Expecting an INT")
		}
	}
}

func Sky_json_decoder_Float() any {
	return func(val any) any {
		switch v := val.(type) {
		case float64: return SkyOk(v)
		case int: return SkyOk(float64(v))
		default: return SkyErr("Expecting a FLOAT")
		}
	}
}

func Sky_json_decoder_Bool() any {
	return func(val any) any {
		b, ok := val.(bool)
		if !ok { return SkyErr("Expecting a BOOL") }
		return SkyOk(b)
	}
}

func Sky_json_decoder_Null(defaultVal any) any {
	return func(val any) any {
		if val != nil { return SkyErr("Expecting null") }
		return SkyOk(defaultVal)
	}
}

func Sky_json_decoder_Value() any {
	return func(val any) any {
		return SkyOk(val)
	}
}

func Sky_json_decoder_Succeed(val any) any {
	return func(_ any) any {
		return SkyOk(val)
	}
}

func Sky_json_decoder_Fail(msg any) any {
	return func(_ any) any {
		return SkyErr(msg)
	}
}

func Sky_json_decoder_Nullable(decoder any) any {
	return func(val any) any {
		if val == nil {
			return SkyOk(SkyNothing())
		}
		result := decoder.(func(any) any)(val)
		r := reflect.ValueOf(result)
		if sky_getTag(r) == 1 {
			return result
		}
		inner := r.FieldByName("OkValue").Interface()
		return SkyOk(SkyJust(inner))
	}
}

func Sky_json_decoder_Field(fieldName any, decoder any) any {
	return func(val any) any {
		m, ok := val.(map[string]any)
		f := sky_asString(fieldName)
		if !ok { return SkyErr("Expecting an OBJECT with a field named '" + f + "'") }
		v, exists := m[f]
		if !exists {
			return SkyErr("Expecting an OBJECT with a field named '" + f + "'")
		}
		return decoder.(func(any) any)(v)
	}
}

func Sky_json_decoder_At(path any, decoder any) any {
	keys := sky_asList(path)
	result := decoder
	for i := len(keys) - 1; i >= 0; i-- {
		result = Sky_json_decoder_Field(keys[i], result)
	}
	return result
}

func Sky_json_decoder_Index(idx any, decoder any) any {
	return func(val any) any {
		lst, ok := val.([]any)
		if !ok { return SkyErr("Expecting an ARRAY") }
		i := sky_asInt(idx)
		if i < 0 || i >= len(lst) {
			return SkyErr(fmt.Sprintf("Expecting a LONGER array. Need index %d but only see %d entries", i, len(lst)))
		}
		return decoder.(func(any) any)(lst[i])
	}
}

func Sky_json_decoder_List(decoder any) any {
	return func(val any) any {
		lst, ok := val.([]any)
		if !ok { return SkyErr("Expecting a LIST") }
		result := make([]any, 0, len(lst))
		d := decoder.(func(any) any)
		for i, item := range lst {
			decoded := d(item)
			r := reflect.ValueOf(decoded)
			if sky_getTag(r) == 1 {
				return SkyErr(fmt.Sprintf("Problem at index %d: %v", i, r.FieldByName("ErrValue").Interface()))
			}
			result = append(result, r.FieldByName("OkValue").Interface())
		}
		return SkyOk(result)
	}
}

func Sky_json_decoder_Dict(decoder any) any {
	return func(val any) any {
		m, ok := val.(map[string]any)
		if !ok { return SkyErr("Expecting an OBJECT") }
		result := make(map[any]any, len(m))
		d := decoder.(func(any) any)
		for k, v := range m {
			decoded := d(v)
			r := reflect.ValueOf(decoded)
			if sky_getTag(r) == 1 {
				return SkyErr(fmt.Sprintf("Problem at field '%s': %v", k, r.FieldByName("ErrValue").Interface()))
			}
			result[k] = r.FieldByName("OkValue").Interface()
		}
		return SkyOk(result)
	}
}

func Sky_json_decoder_KeyValuePairs(decoder any) any {
	return func(val any) any {
		m, ok := val.(map[string]any)
		if !ok { return SkyErr("Expecting an OBJECT") }
		result := make([]any, 0, len(m))
		d := decoder.(func(any) any)
		for k, v := range m {
			decoded := d(v)
			r := reflect.ValueOf(decoded)
			if sky_getTag(r) == 1 {
				return SkyErr(fmt.Sprintf("Problem at field '%s': %v", k, r.FieldByName("ErrValue").Interface()))
			}
			result = append(result, SkyTuple2{V0: k, V1: r.FieldByName("OkValue").Interface()})
		}
		return SkyOk(result)
	}
}

func sky_json_decoder_runAndCheck(decoder any, val any) (any, bool) {
	result := decoder.(func(any) any)(val)
	r := reflect.ValueOf(result)
	if sky_getTag(r) == 1 {
		return result, false
	}
	return r.FieldByName("OkValue").Interface(), true
}

func Sky_json_decoder_Map(fn any, decoder any) any {
	return func(val any) any {
		inner, ok := sky_json_decoder_runAndCheck(decoder, val)
		if !ok { return inner }
		return SkyOk(fn.(func(any) any)(inner))
	}
}

func Sky_json_decoder_Map2(fn any, d1 any, d2 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		f := fn.(func(any) any)
		return SkyOk(f(v1.(func(any) any))(v2))
	}
}

func Sky_json_decoder_Map3(fn any, d1 any, d2 any, d3 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		v3, ok3 := sky_json_decoder_runAndCheck(d3, val)
		if !ok3 { return v3 }
		f := fn.(func(any) any)
		return SkyOk(sky_asFunc(f(v1.(func(any) any))(v2))(v3))
	}
}

func Sky_json_decoder_Map4(fn any, d1 any, d2 any, d3 any, d4 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		v3, ok3 := sky_json_decoder_runAndCheck(d3, val)
		if !ok3 { return v3 }
		v4, ok4 := sky_json_decoder_runAndCheck(d4, val)
		if !ok4 { return v4 }
		f := fn.(func(any) any)
		return SkyOk(sky_asFunc(sky_asFunc(f(v1.(func(any) any))(v2))(v3))(v4))
	}
}

func Sky_json_decoder_Map5(fn any, d1 any, d2 any, d3 any, d4 any, d5 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		v3, ok3 := sky_json_decoder_runAndCheck(d3, val)
		if !ok3 { return v3 }
		v4, ok4 := sky_json_decoder_runAndCheck(d4, val)
		if !ok4 { return v4 }
		v5, ok5 := sky_json_decoder_runAndCheck(d5, val)
		if !ok5 { return v5 }
		f := fn.(func(any) any)
		return SkyOk(sky_asFunc(sky_asFunc(sky_asFunc(f(v1.(func(any) any))(v2))(v3))(v4))(v5))
	}
}

func Sky_json_decoder_Map6(fn any, d1 any, d2 any, d3 any, d4 any, d5 any, d6 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		v3, ok3 := sky_json_decoder_runAndCheck(d3, val)
		if !ok3 { return v3 }
		v4, ok4 := sky_json_decoder_runAndCheck(d4, val)
		if !ok4 { return v4 }
		v5, ok5 := sky_json_decoder_runAndCheck(d5, val)
		if !ok5 { return v5 }
		v6, ok6 := sky_json_decoder_runAndCheck(d6, val)
		if !ok6 { return v6 }
		f := fn.(func(any) any)
		return SkyOk(sky_asFunc(sky_asFunc(sky_asFunc(sky_asFunc(f(v1.(func(any) any))(v2))(v3))(v4))(v5))(v6))
	}
}

func Sky_json_decoder_Map7(fn any, d1 any, d2 any, d3 any, d4 any, d5 any, d6 any, d7 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		v3, ok3 := sky_json_decoder_runAndCheck(d3, val)
		if !ok3 { return v3 }
		v4, ok4 := sky_json_decoder_runAndCheck(d4, val)
		if !ok4 { return v4 }
		v5, ok5 := sky_json_decoder_runAndCheck(d5, val)
		if !ok5 { return v5 }
		v6, ok6 := sky_json_decoder_runAndCheck(d6, val)
		if !ok6 { return v6 }
		v7, ok7 := sky_json_decoder_runAndCheck(d7, val)
		if !ok7 { return v7 }
		f := fn.(func(any) any)
		return SkyOk(sky_asFunc(sky_asFunc(sky_asFunc(sky_asFunc(sky_asFunc(f(v1.(func(any) any))(v2))(v3))(v4))(v5))(v6))(v7))
	}
}

func Sky_json_decoder_Map8(fn any, d1 any, d2 any, d3 any, d4 any, d5 any, d6 any, d7 any, d8 any) any {
	return func(val any) any {
		v1, ok1 := sky_json_decoder_runAndCheck(d1, val)
		if !ok1 { return v1 }
		v2, ok2 := sky_json_decoder_runAndCheck(d2, val)
		if !ok2 { return v2 }
		v3, ok3 := sky_json_decoder_runAndCheck(d3, val)
		if !ok3 { return v3 }
		v4, ok4 := sky_json_decoder_runAndCheck(d4, val)
		if !ok4 { return v4 }
		v5, ok5 := sky_json_decoder_runAndCheck(d5, val)
		if !ok5 { return v5 }
		v6, ok6 := sky_json_decoder_runAndCheck(d6, val)
		if !ok6 { return v6 }
		v7, ok7 := sky_json_decoder_runAndCheck(d7, val)
		if !ok7 { return v7 }
		v8, ok8 := sky_json_decoder_runAndCheck(d8, val)
		if !ok8 { return v8 }
		f := fn.(func(any) any)
		return SkyOk(sky_asFunc(sky_asFunc(sky_asFunc(sky_asFunc(sky_asFunc(sky_asFunc(f(v1.(func(any) any))(v2))(v3))(v4))(v5))(v6))(v7))(v8))
	}
}

func Sky_json_decoder_AndThen(fn any, decoder any) any {
	return func(val any) any {
		inner, ok := sky_json_decoder_runAndCheck(decoder, val)
		if !ok { return inner }
		newDecoder := fn.(func(any) any)(inner)
		return newDecoder.(func(any) any)(val)
	}
}

func Sky_json_decoder_OneOf(decoders any) any {
	return func(val any) any {
		lst := sky_asList(decoders)
		for _, d := range lst {
			result := d.(func(any) any)(val)
			r := reflect.ValueOf(result)
			if sky_getTag(r) == 0 {
				return result
			}
		}
		return SkyErr("oneOf: all decoders failed")
	}
}

func Sky_json_decoder_Maybe(decoder any) any {
	return func(val any) any {
		result := decoder.(func(any) any)(val)
		r := reflect.ValueOf(result)
		if sky_getTag(r) == 1 {
			return SkyOk(SkyNothing())
		}
		inner := r.FieldByName("OkValue").Interface()
		return SkyOk(SkyJust(inner))
	}
}

func Sky_json_decoder_Lazy(thunk any) any {
	return func(val any) any {
		decoder := thunk.(func(any) any)(struct{}{})
		return decoder.(func(any) any)(val)
	}
}

func Sky_json_decoder_DecodeValue(decoder any, value any) any {
	return decoder.(func(any) any)(value)
}

func Sky_json_decoder_DecodeString(decoder any, s any) any {
	var parsed any
	err := json.Unmarshal([]byte(sky_asString(s)), &parsed)
	if err != nil { return SkyErr(err.Error()) }
	return decoder.(func(any) any)(parsed)
}

// ============= Result Operations =============

// ============= OS Helpers =============

func Sky_os_GetArgs() any {
	result := make([]any, len(os.Args))
	for i, s := range os.Args {
		result[i] = s
	}
	return result
}

// ============= Maybe Operations =============

func Sky_maybe_WithDefault(defaultVal any, maybe any) any {
	if m, ok := maybe.(SkyMaybe); ok {
		if m.Tag == 0 {
			return m.JustValue
		}
	}
	return defaultVal
}

func Sky_maybe_Map(fn any, maybe any) any {
	if m, ok := maybe.(SkyMaybe); ok {
		if m.Tag == 1 {
			return maybe
		}
		return SkyJust(fn.(func(any) any)(m.JustValue))
	}
	return maybe
}

func Sky_maybe_AndThen(fn any, maybe any) any {
	if m, ok := maybe.(SkyMaybe); ok {
		if m.Tag == 1 {
			return maybe
		}
		return fn.(func(any) any)(m.JustValue)
	}
	return maybe
}

// ============= Result Operations =============

func Sky_result_WithDefault(defaultVal any, result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if sky_getTag(r) == 0 {
			return r.FieldByName("OkValue").Interface()
		}
	}
	return defaultVal
}

func Sky_result_Map(fn any, result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if sky_getTag(r) == 1 {
			return result
		}
		inner := r.FieldByName("OkValue").Interface()
		return SkyOk(fn.(func(any) any)(inner))
	}
	return result
}

func Sky_result_AndThen(fn any, result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if sky_getTag(r) == 1 {
			return result
		}
		inner := r.FieldByName("OkValue").Interface()
		return fn.(func(any) any)(inner)
	}
	return result
}

func Sky_result_MapError(fn any, result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if sky_getTag(r) == 0 {
			return result
		}
		inner := r.FieldByName("ErrValue").Interface()
		return SkyErr(fn.(func(any) any)(inner))
	}
	return result
}

func Sky_result_ToMaybe(result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if sky_getTag(r) == 0 {
			return SkyJust(r.FieldByName("OkValue").Interface())
		}
	}
	return SkyNothing()
}

// ============= Error Operations =============

// ============= Msg Encoding =============

func Sky_msgToString(v any) any {
	if s, ok := v.(string); ok { return s }
	val := reflect.ValueOf(v)
	// Handle constructor function references (e.g., onInput SetSearch where
	// SetSearch : String -> Msg). Extract the constructor name from the Go
	// function's runtime name (e.g., "sky_state.SetSearch" → "SetSearch").
	if val.Kind() == reflect.Func {
		pc := val.Pointer()
		fn := runtime.FuncForPC(pc)
		if fn != nil {
			name := fn.Name()
			parts := strings.Split(name, ".")
			ctorName := parts[len(parts)-1]
			// Go may suffix with "-fm" for method values
			ctorName = strings.TrimSuffix(ctorName, "-fm")
			return ctorName
		}
		return fmt.Sprintf("%v", v)
	}
	// Handle map[string]any (custom ADTs use maps instead of named structs)
	if m, ok := v.(map[string]any); ok {
		skyName, _ := m["SkyName"].(string)
		if skyName == "" { return fmt.Sprintf("%v", v) }
		name := skyName
		for k, argVal := range m {
			if k == "Tag" || k == "SkyName" { continue }
			if !strings.Contains(k, "Value") { continue }
			if argVal == nil { continue }
			switch a := argVal.(type) {
			case string:
				name += " \"" + a + "\""
			case int:
				name += " " + fmt.Sprintf("%d", a)
			default:
				argStr := Sky_msgToString(a)
				name += " " + sky_asString(argStr)
			}
		}
		return name
	}
	if val.Kind() != reflect.Struct { return fmt.Sprintf("%v", v) }
	nameField := val.FieldByName("SkyName")
	if !nameField.IsValid() { return fmt.Sprintf("%v", v) }
	name := nameField.String()
	// Collect constructor arguments (fields ending in "Value")
	for i := 0; i < val.NumField(); i++ {
		field := val.Type().Field(i)
		if field.Name == "Tag" || field.Name == "SkyName" { continue }
		if !strings.Contains(field.Name, "Value") { continue }
		argVal := val.Field(i).Interface()
		if argVal == nil { continue }
		switch a := argVal.(type) {
		case string:
			name += " \"" + a + "\""
		case int:
			name += " " + fmt.Sprintf("%d", a)
		default:
			argStr := Sky_msgToString(a)
			name += " " + sky_asString(argStr)
		}
	}
	return name
}

// JS escape hatch: passes a raw string through for inline JavaScript
func Sky_JS(s any) any { return s }

func Sky_errorToString(e any) any {
	if e == nil {
		return ""
	}
	if err, ok := e.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%v", e)
}

// ============= Char Operations =============

func Sky_char_IsUpper(c any) any {
	s := sky_asString(c)
	if len(s) == 0 { return false }
	return unicode.IsUpper([]rune(s)[0])
}

func Sky_char_IsLower(c any) any {
	s := sky_asString(c)
	if len(s) == 0 { return false }
	return unicode.IsLower([]rune(s)[0])
}

func Sky_char_IsAlpha(c any) any {
	s := sky_asString(c)
	if len(s) == 0 { return false }
	return unicode.IsLetter([]rune(s)[0])
}

func Sky_char_IsDigit(c any) any {
	s := sky_asString(c)
	if len(s) == 0 { return false }
	return unicode.IsDigit([]rune(s)[0])
}

func Sky_char_IsAlphaNum(c any) any {
	s := sky_asString(c)
	if len(s) == 0 { return false }
	r := []rune(s)[0]
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func Sky_char_ToUpper(c any) any {
	s := sky_asString(c)
	return strings.ToUpper(s)
}

func Sky_char_ToLower(c any) any {
	s := sky_asString(c)
	return strings.ToLower(s)
}

func Sky_char_ToCode(c any) any {
	s := sky_asString(c)
	if len(s) == 0 { return 0 }
	return int([]rune(s)[0])
}

func Sky_char_FromCode(n any) any {
	return string(rune(sky_asInt(n)))
}

// ============= Bitwise Operations =============

func Sky_bitwise_And(a any, b any) any { return sky_asInt(a) & sky_asInt(b) }
func Sky_bitwise_Or(a any, b any) any { return sky_asInt(a) | sky_asInt(b) }
func Sky_bitwise_Xor(a any, b any) any { return sky_asInt(a) ^ sky_asInt(b) }
func Sky_bitwise_Complement(a any) any { return ^sky_asInt(a) }
func Sky_bitwise_ShiftLeftBy(amount any, val any) any { return sky_asInt(val) << uint(sky_asInt(amount)) }
func Sky_bitwise_ShiftRightBy(amount any, val any) any { return sky_asInt(val) >> uint(sky_asInt(amount)) }
func Sky_bitwise_ShiftRightZfBy(amount any, val any) any { return int(uint(sky_asInt(val)) >> uint(sky_asInt(amount))) }

// ============= Array Operations =============

func Sky_array_Empty() any { return []any{} }

func Sky_array_FromList(list any) any { return list }

func Sky_array_ToList(arr any) any { return arr }

func Sky_array_Get(index any, arr any) any {
	a := sky_asList(arr)
	i := sky_asInt(index)
	if i < 0 || i >= len(a) {
		return SkyNothing() // Nothing
	}
	return SkyJust(a[i]) // Just
}

func Sky_array_Set(index any, val any, arr any) any {
	a := sky_asList(arr)
	i := sky_asInt(index)
	if i < 0 || i >= len(a) { return arr }
	newArr := make([]any, len(a))
	copy(newArr, a)
	newArr[i] = val
	return newArr
}

func Sky_array_Push(val any, arr any) any {
	a := sky_asList(arr)
	return append(a, val)
}

func Sky_array_Length(arr any) any {
	return len(sky_asList(arr))
}

func Sky_array_Slice(start any, end any, arr any) any {
	a := sky_asList(arr)
	s := sky_asInt(start)
	e := sky_asInt(end)
	if s < 0 { s = 0 }
	if e > len(a) { e = len(a) }
	if s > e { return []any{} }
	return a[s:e]
}

func Sky_array_Map(f any, arr any) any {
	a := sky_asList(arr)
	fn := f.(func(any) any)
	result := make([]any, len(a))
	for i, v := range a { result[i] = fn(v) }
	return result
}

func Sky_array_Foldl(f any, acc any, arr any) any {
	a := sky_asList(arr)
	fn := f.(func(any) any)
	result := acc
	for _, v := range a { result = fn(v.(func(any) any))(result) }
	return result
}

func Sky_array_Foldr(f any, acc any, arr any) any {
	a := sky_asList(arr)
	fn := f.(func(any) any)
	result := acc
	for i := len(a) - 1; i >= 0; i-- { result = fn(a[i].(func(any) any))(result) }
	return result
}

func Sky_array_Append(a any, b any) any {
	return append(sky_asList(a), sky_asList(b)...)
}

func Sky_array_IndexedMap(f any, arr any) any {
	a := sky_asList(arr)
	fn := f.(func(any) any)
	result := make([]any, len(a))
	for i, v := range a { result[i] = fn(i.(func(any) any))(v) }
	return result
}

// ============= File Operations =============

func Sky_file_ReadFile(path any) any {
	data, err := os.ReadFile(sky_asString(path))
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: string(data)}
}

func Sky_file_WriteFile(path any, content any) any {
	err := os.WriteFile(sky_asString(path), []byte(sky_asString(content)), 0644)
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

func Sky_file_Exists(path any) any {
	_, err := os.Stat(sky_asString(path))
	return err == nil
}

func Sky_file_Remove(path any) any {
	err := os.Remove(sky_asString(path))
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

func Sky_file_MkdirAll(path any) any {
	err := os.MkdirAll(sky_asString(path), 0755)
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

func Sky_file_ReadDir(path any) any {
	entries, err := os.ReadDir(sky_asString(path))
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	names := make([]any, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return SkyResult{Tag: 0, OkValue: names}
}

func Sky_file_IsDir(path any) any {
	info, err := os.Stat(sky_asString(path))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// ============= Process Operations =============

func Sky_process_Run(command any, args any) any {
	argList := sky_asList(args)
	strArgs := make([]string, len(argList))
	for i, a := range argList {
		strArgs[i] = sky_asString(a)
	}
	cmd := exec.Command(sky_asString(command), strArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: fmt.Errorf("%s: %s", err.Error(), string(output))}
	}
	return SkyResult{Tag: 0, OkValue: string(output)}
}

func Sky_process_Exit(code any) any {
	os.Exit(sky_asInt(code))
	return struct{}{}
}

func Sky_process_GetEnv(key any) any {
	val, ok := os.LookupEnv(sky_asString(key))
	if !ok {
		return SkyNothing() // Nothing
	}
	return SkyJust(val) // Just
}

func Sky_process_GetCwd() any {
	dir, err := os.Getwd()
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: dir}
}

func Sky_process_LoadEnv(filePath any) any {
	p := sky_asString(filePath)
	if p == "" {
		p = ".env"
	}
	f, err := os.Open(p)
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eqIdx := strings.Index(line, "=")
		if eqIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eqIdx])
		val := strings.TrimSpace(line[eqIdx+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == 0x27 && val[len(val)-1] == 0x27)) {
			val = val[1 : len(val)-1]
		}
		os.Setenv(key, val)
	}
	if err := scanner.Err(); err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

// ============= Stdin/Stdout I/O =============

func Sky_io_ReadLine() any {
	if Sky_stdin_reader == nil {
		Sky_stdin_reader = bufio.NewReader(os.Stdin)
	}
	line, err := Sky_stdin_reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return SkyNothing()
	}
	// Strip trailing \n and \r
	line = strings.TrimRight(line, "\r\n")
	return SkyJust(line)
}

var Sky_stdin_reader *bufio.Reader

func Sky_io_ReadBytes(n any) any {
	if Sky_stdin_reader == nil {
		Sky_stdin_reader = bufio.NewReader(os.Stdin)
	}
	count := sky_asInt(n)
	buf := make([]byte, count)
	totalRead := 0
	for totalRead < count {
		nRead, err := Sky_stdin_reader.Read(buf[totalRead:])
		totalRead += nRead
		if err != nil {
			break
		}
	}
	if totalRead == 0 {
		return SkyNothing()
	}
	return SkyJust(string(buf[:totalRead]))
}

func Sky_io_WriteStdout(s any) any {
	fmt.Print(sky_asString(s))
	return struct{}{}
}

func Sky_io_WriteStderr(s any) any {
	fmt.Fprint(os.Stderr, sky_asString(s))
	return struct{}{}
}

// ============= Ref (Mutable Reference) =============

type SkyRef struct {
	Value any
}

func Sky_ref_New(val any) any {
	return &SkyRef{Value: val}
}

func Sky_ref_Get(ref any) any {
	r := ref.(*SkyRef)
	return r.Value
}

func Sky_ref_Set(val any, ref any) any {
	r := ref.(*SkyRef)
	r.Value = val
	return struct{}{}
}

func Sky_ref_Modify(fn any, ref any) any {
	r := ref.(*SkyRef)
	f := fn.(func(any) any)
	r.Value = f(r.Value)
	return struct{}{}
}

// ============= Path Operations =============

func Sky_path_Join(parts any) any {
	lst := sky_asList(parts)
	strs := make([]string, len(lst))
	for i, p := range lst {
		strs[i] = sky_asString(p)
	}
	return filepath.Join(strs...)
}

func Sky_path_Dir(path any) any {
	return filepath.Dir(sky_asString(path))
}

func Sky_path_Base(path any) any {
	return filepath.Base(sky_asString(path))
}

func Sky_path_Ext(path any) any {
	return filepath.Ext(sky_asString(path))
}

func Sky_path_IsAbs(path any) any {
	return filepath.IsAbs(sky_asString(path))
}

func Sky_path_Resolve(path any) any {
	abs, err := filepath.Abs(sky_asString(path))
	if err != nil {
		return sky_asString(path)
	}
	return abs
}

func Sky_path_RelativeTo(base any, target any) any {
	rel, err := filepath.Rel(sky_asString(base), sky_asString(target))
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: rel}
}

func Sky_path_Separator() any {
	return string(filepath.Separator)
}

// ============= Args Operations =============

func Sky_args_GetArgs() any {
	args := os.Args
	result := make([]any, len(args))
	for i, a := range args {
		result[i] = a
	}
	return result
}
