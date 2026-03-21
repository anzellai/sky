import fs from "fs";
import path from "path";
import { InspectResult, Param } from "./inspect-package.js";
import { lowerCamelCase, isGoPointerToPrimitive } from "./type-mapper.js";

function makeSafeGoName(pkgName: string) {
    return pkgName.replace(/[\/\.-]/g, "_");
}

export function generateWrappers(pkgName: string, pkg: InspectResult, usedSymbols?: Set<string>) {
    const safePkg = makeSafeGoName(pkgName);
    
    const wrapperDir = path.join(".skycache", "go", "wrappers");
    fs.mkdirSync(wrapperDir, { recursive: true });
    const helperPath = path.join(wrapperDir, "00_sky_helpers.go");
    
      let helperCode = `package sky_wrappers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

type SkyResult struct {
	Tag int
	SkyName string
	OkValue any
	ErrValue any
}

func SkyOk(v any) SkyResult {
	return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v}
}

func SkyErr(e any) SkyResult {
	return SkyResult{Tag: 1, SkyName: "Err", ErrValue: e}
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

// ============= List Operations =============

func Sky_list_Map(fn any, list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
	result := make([]any, len(lst))
	for i, item := range lst {
		result[i] = fn.(func(any) any)(item)
	}
	return result
}

func Sky_list_Filter(fn any, list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
	result := []any{}
	for _, item := range lst {
		if fn.(func(any) any)(item) == true {
			result = append(result, item)
		}
	}
	return result
}

func Sky_list_Foldl(fn any, acc any, list any) any {
	lst, ok := list.([]any)
	if !ok { return acc }
	result := acc
	for _, item := range lst {
		result = fn.(func(any) any)(item).(func(any) any)(result)
	}
	return result
}

func Sky_list_Foldr(fn any, acc any, list any) any {
	lst, ok := list.([]any)
	if !ok { return acc }
	result := acc
	for i := len(lst) - 1; i >= 0; i-- {
		result = fn.(func(any) any)(lst[i]).(func(any) any)(result)
	}
	return result
}

func Sky_list_Head(list any) any {
	lst, ok := list.([]any)
	if !ok || len(lst) == 0 {
		return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil} // Nothing
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: lst[0]}
}

func Sky_list_Tail(list any) any {
	lst, ok := list.([]any)
	if !ok || len(lst) == 0 {
		return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil} // Nothing
	}
	tail := make([]any, len(lst)-1)
	copy(tail, lst[1:])
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: tail}
}

func Sky_list_Length(list any) any {
	lst, ok := list.([]any)
	if !ok { return 0 }
	return len(lst)
}

func Sky_list_Append(a any, b any) any {
	lstA, okA := a.([]any)
	lstB, okB := b.([]any)
	if !okA { lstA = []any{} }
	if !okB { lstB = []any{} }
	result := make([]any, 0, len(lstA)+len(lstB))
	result = append(result, lstA...)
	result = append(result, lstB...)
	return result
}

func Sky_list_Reverse(list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
	result := make([]any, len(lst))
	for i, item := range lst {
		result[len(lst)-1-i] = item
	}
	return result
}

func Sky_list_Member(item any, list any) any {
	lst, ok := list.([]any)
	if !ok { return false }
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
	lst, ok := list.([]any)
	if !ok { return true }
	return len(lst) == 0
}

func Sky_list_Take(n any, list any) any {
	count, ok1 := n.(int)
	lst, ok2 := list.([]any)
	if !ok1 || !ok2 { return []any{} }
	if count > len(lst) { count = len(lst) }
	if count < 0 { count = 0 }
	result := make([]any, count)
	copy(result, lst[:count])
	return result
}

func Sky_list_Drop(n any, list any) any {
	count, ok1 := n.(int)
	lst, ok2 := list.([]any)
	if !ok1 || !ok2 { return []any{} }
	if count > len(lst) { count = len(lst) }
	if count < 0 { count = 0 }
	result := make([]any, len(lst)-count)
	copy(result, lst[count:])
	return result
}

func Sky_list_Sort(list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
	result := make([]any, len(lst))
	copy(result, lst)
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprintf("%v", result[i]) < fmt.Sprintf("%v", result[j])
	})
	return result
}

func Sky_list_Intersperse(sep any, list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
	if len(lst) <= 1 { return lst }
	result := make([]any, 0, len(lst)*2-1)
	for i, item := range lst {
		if i > 0 { result = append(result, sep) }
		result = append(result, item)
	}
	return result
}

func Sky_list_Concat(lists any) any {
	lst, ok := lists.([]any)
	if !ok { return []any{} }
	result := []any{}
	for _, item := range lst {
		inner, ok := item.([]any)
		if ok { result = append(result, inner...) }
	}
	return result
}

func Sky_list_ConcatMap(fn any, list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
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
	lst, ok := list.([]any)
	if !ok { return []any{} }
	result := make([]any, len(lst))
	for i, item := range lst {
		result[i] = fn.(func(any) any)(i).(func(any) any)(item)
	}
	return result
}

func Sky_list_Zip(listA any, listB any) any {
	a, ok1 := listA.([]any)
	b, ok2 := listB.([]any)
	if !ok1 || !ok2 { return []any{} }
	n := len(a); if len(b) < n { n = len(b) }
	result := make([]any, n)
	for i := 0; i < n; i++ { result[i] = Tuple2{a[i], b[i]} }
	return result
}

func Sky_list_Unzip(list any) any {
	lst, ok := list.([]any)
	if !ok { return Tuple2{[]any{}, []any{}} }
	as := make([]any, len(lst))
	bs := make([]any, len(lst))
	for i, item := range lst {
		t := item.(Tuple2)
		as[i] = t.V0
		bs[i] = t.V1
	}
	return Tuple2{as, bs}
}

func Sky_list_Map2(fn any, listA any, listB any) any {
	a, ok1 := listA.([]any)
	b, ok2 := listB.([]any)
	if !ok1 || !ok2 { return []any{} }
	f := fn.(func(any) any)
	n := len(a); if len(b) < n { n = len(b) }
	result := make([]any, n)
	for i := 0; i < n; i++ { result[i] = f(a[i]).(func(any) any)(b[i]) }
	return result
}

func Sky_list_Maximum(list any) any {
	lst, ok := list.([]any)
	if !ok || len(lst) == 0 { return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing"} }
	best := lst[0]
	for _, item := range lst[1:] {
		if fmt.Sprintf("%v", item) > fmt.Sprintf("%v", best) { best = item }
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: best}
}

func Sky_list_Minimum(list any) any {
	lst, ok := list.([]any)
	if !ok || len(lst) == 0 { return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing"} }
	best := lst[0]
	for _, item := range lst[1:] {
		if fmt.Sprintf("%v", item) < fmt.Sprintf("%v", best) { best = item }
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: best}
}

func Sky_list_Find(pred any, list any) any {
	lst, ok := list.([]any)
	if !ok { return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing"} }
	fn := pred.(func(any) any)
	for _, item := range lst {
		if fn(item).(bool) { return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: item} }
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing"}
}

func Sky_list_FilterMap(fn any, list any) any {
	lst, ok := list.([]any)
	if !ok { return []any{} }
	f := fn.(func(any) any)
	result := []any{}
	for _, item := range lst {
		maybe := f(item)
		// Check if it's Just (Tag 0) or Nothing (Tag 1)
		type MaybeResult struct { Tag int; JustValue any }
		if m, ok := maybe.(MaybeResult); ok && m.Tag == 0 {
			result = append(result, m.JustValue)
		} else {
			// Try struct literal form
			v := reflect.ValueOf(maybe)
			if v.Kind() == reflect.Struct {
				tagField := v.FieldByName("Tag")
				if tagField.IsValid() && tagField.Int() == 0 {
					valField := v.FieldByName("JustValue")
					if valField.IsValid() { result = append(result, valField.Interface()) }
				}
			}
		}
	}
	return result
}

// ============= String Operations =============

func Sky_string_Split(sep any, s any) any {
	parts := strings.Split(s.(string), sep.(string))
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func Sky_string_Join(sep any, list any) any {
	lst := list.([]any)
	parts := make([]string, len(lst))
	for i, p := range lst { parts[i] = fmt.Sprintf("%v", p) }
	return strings.Join(parts, sep.(string))
}

func Sky_string_Contains(sub any, s any) any {
	return strings.Contains(s.(string), sub.(string))
}

func Sky_string_Replace(old any, new_ any, s any) any {
	return strings.ReplaceAll(s.(string), old.(string), new_.(string))
}

func Sky_string_Trim(s any) any {
	return strings.TrimSpace(s.(string))
}

func Sky_string_Length(s any) any {
	return len([]rune(s.(string)))
}

func Sky_string_ToLower(s any) any {
	return strings.ToLower(s.(string))
}

func Sky_string_ToUpper(s any) any {
	return strings.ToUpper(s.(string))
}

func Sky_string_StartsWith(prefix any, s any) any {
	return strings.HasPrefix(s.(string), prefix.(string))
}

func Sky_string_EndsWith(suffix any, s any) any {
	return strings.HasSuffix(s.(string), suffix.(string))
}

func Sky_string_Slice(start any, end any, s any) any {
	runes := []rune(s.(string))
	st := start.(int)
	en := end.(int)
	if st < 0 { st = 0 }
	if en > len(runes) { en = len(runes) }
	if st > en { return "" }
	return string(runes[st:en])
}

func Sky_string_IsEmpty(s any) any {
	return s.(string) == ""
}

func Sky_string_FromFloat(f any) any {
	return fmt.Sprintf("%g", f)
}

func Sky_string_ToInt(s any) any {
	var n int
	_, err := fmt.Sscanf(s.(string), "%d", &n)
	if err != nil { return SkyErr(err.Error()) }
	return SkyOk(n)
}

func Sky_string_ToFloat(s any) any {
	var f float64
	_, err := fmt.Sscanf(s.(string), "%g", &f)
	if err != nil { return SkyErr(err.Error()) }
	return SkyOk(f)
}

func Sky_string_Lines(s any) any {
	parts := strings.Split(s.(string), "\\n")
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func Sky_string_Words(s any) any {
	parts := strings.Fields(s.(string))
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func Sky_string_Repeat(n any, s any) any {
	return strings.Repeat(s.(string), n.(int))
}

func Sky_string_PadLeft(n any, ch any, s any) any {
	str := s.(string)
	for len([]rune(str)) < n.(int) { str = ch.(string) + str }
	return str
}

func Sky_string_PadRight(n any, ch any, s any) any {
	str := s.(string)
	for len([]rune(str)) < n.(int) { str = str + ch.(string) }
	return str
}

func Sky_string_Left(n any, s any) any {
	runes := []rune(s.(string))
	count := n.(int)
	if count > len(runes) { count = len(runes) }
	return string(runes[:count])
}

func Sky_string_Right(n any, s any) any {
	runes := []rune(s.(string))
	count := n.(int)
	if count > len(runes) { count = len(runes) }
	return string(runes[len(runes)-count:])
}

func Sky_string_Reverse(s any) any {
	runes := []rune(s.(string))
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func Sky_string_Indexes(sub any, s any) any {
	str := s.(string)
	substr := sub.(string)
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

// ============= Dict Operations =============

func Sky_dict_Empty() any {
	return map[any]any{}
}

func Sky_dict_Singleton(key any, val any) any {
	return map[any]any{key: val}
}

func Sky_dict_Insert(key any, val any, dict any) any {
	m := dict.(map[any]any)
	result := make(map[any]any, len(m)+1)
	for k, v := range m { result[k] = v }
	result[key] = val
	return result
}

func Sky_dict_Get(key any, dict any) any {
	m := dict.(map[any]any)
	val, ok := m[key]
	if !ok { return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil} } // Nothing
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: val}
}

func Sky_dict_Remove(key any, dict any) any {
	m := dict.(map[any]any)
	result := make(map[any]any, len(m))
	for k, v := range m {
		if k != key { result[k] = v }
	}
	return result
}

func Sky_dict_Keys(dict any) any {
	m := dict.(map[any]any)
	result := make([]any, 0, len(m))
	for k := range m { result = append(result, k) }
	return result
}

func Sky_dict_Values(dict any) any {
	m := dict.(map[any]any)
	result := make([]any, 0, len(m))
	for _, v := range m { result = append(result, v) }
	return result
}

func Sky_dict_Map(fn any, dict any) any {
	m := dict.(map[any]any)
	result := make(map[any]any, len(m))
	for k, v := range m {
		result[k] = fn.(func(any) any)(k).(func(any) any)(v)
	}
	return result
}

func Sky_dict_Foldl(fn any, acc any, dict any) any {
	m := dict.(map[any]any)
	result := acc
	for k, v := range m {
		result = fn.(func(any) any)(k).(func(any) any)(v).(func(any) any)(result)
	}
	return result
}

func Sky_dict_FromList(list any) any {
	lst := list.([]any)
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
	m := dict.(map[any]any)
	result := make([]any, 0, len(m))
	for k, v := range m {
		result = append(result, Tuple2{V0: k, V1: v})
	}
	return result
}

func Sky_dict_IsEmpty(dict any) any {
	m := dict.(map[any]any)
	return len(m) == 0
}

func Sky_dict_Size(dict any) any {
	m := dict.(map[any]any)
	return len(m)
}

func Sky_dict_Member(key any, dict any) any {
	m := dict.(map[any]any)
	_, ok := m[key]
	return ok
}

func Sky_dict_Update(key any, fn any, dict any) any {
	m := dict.(map[any]any)
	result := make(map[any]any, len(m))
	for k, v := range m { result[k] = v }
	val, ok := m[key]
	var maybeVal any
	if ok {
		maybeVal = struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: val}
	} else {
		maybeVal = struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil}
	}
	newMaybe := fn.(func(any) any)(maybeVal)
	newVal := reflect.ValueOf(newMaybe)
	if newVal.Kind() == reflect.Struct {
		tagField := newVal.FieldByName("Tag")
		if tagField.IsValid() && tagField.Interface().(int) == 0 {
			result[key] = newVal.FieldByName("JustValue").Interface()
		} else {
			delete(result, key)
		}
	}
	return result
}

// ============= JSON Operations =============

func Sky_json_Encode(indent any, value any) any {
	n := indent.(int)
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
	err := json.Unmarshal([]byte(s.(string)), &result)
	if err != nil { return SkyErr(err.Error()) }
	return SkyOk(result)
}

func Sky_json_EncodeString(s any) any { return s }
func Sky_json_EncodeInt(n any) any { return n }
func Sky_json_EncodeFloat(f any) any { return f }
func Sky_json_EncodeBool(b any) any { return b }
func Sky_json_EncodeNull() any { return nil }

func Sky_json_EncodeList(fn any, list any) any {
	lst := list.([]any)
	result := make([]any, len(lst))
	for i, item := range lst {
		result[i] = fn.(func(any) any)(item)
	}
	return result
}

func Sky_json_EncodeObject(pairs any) any {
	lst := pairs.([]any)
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
	val, exists := m[field.(string)]
	if !exists { return SkyErr("field not found: " + field.(string)) }
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
	if val == nil { return SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil}) } // Ok Nothing
	return SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: val})
}

func Sky_json_At(keys any, obj any) any {
	lst := keys.([]any)
	current := obj
	for _, key := range lst {
		m, ok := current.(map[string]any)
		if !ok { return SkyErr("not an object at path") }
		val, exists := m[key.(string)]
		if !exists { return SkyErr("field not found: " + key.(string)) }
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
			if tag.IsValid() && tag.Interface().(int) == 1 {
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
	if v1.Kind() == reflect.Struct && v1.FieldByName("Tag").Interface().(int) == 1 {
		return r1
	}
	if v2.Kind() == reflect.Struct && v2.FieldByName("Tag").Interface().(int) == 1 {
		return r2
	}
	val1 := v1.FieldByName("OkValue").Interface()
	val2 := v2.FieldByName("OkValue").Interface()
	result := fn.(func(any) any)(val1).(func(any) any)(val2)
	return SkyOk(result)
}

func Sky_json_Map3(fn any, r1 any, r2 any, r3 any) any {
	v1 := reflect.ValueOf(r1)
	v2 := reflect.ValueOf(r2)
	v3 := reflect.ValueOf(r3)
	if v1.Kind() == reflect.Struct && v1.FieldByName("Tag").Interface().(int) == 1 { return r1 }
	if v2.Kind() == reflect.Struct && v2.FieldByName("Tag").Interface().(int) == 1 { return r2 }
	if v3.Kind() == reflect.Struct && v3.FieldByName("Tag").Interface().(int) == 1 { return r3 }
	val1 := v1.FieldByName("OkValue").Interface()
	val2 := v2.FieldByName("OkValue").Interface()
	val3 := v3.FieldByName("OkValue").Interface()
	result := fn.(func(any) any)(val1).(func(any) any)(val2).(func(any) any)(val3)
	return SkyOk(result)
}

func Sky_json_Map4(fn any, r1 any, r2 any, r3 any, r4 any) any {
	v1 := reflect.ValueOf(r1)
	v2 := reflect.ValueOf(r2)
	v3 := reflect.ValueOf(r3)
	v4 := reflect.ValueOf(r4)
	if v1.Kind() == reflect.Struct && v1.FieldByName("Tag").Interface().(int) == 1 { return r1 }
	if v2.Kind() == reflect.Struct && v2.FieldByName("Tag").Interface().(int) == 1 { return r2 }
	if v3.Kind() == reflect.Struct && v3.FieldByName("Tag").Interface().(int) == 1 { return r3 }
	if v4.Kind() == reflect.Struct && v4.FieldByName("Tag").Interface().(int) == 1 { return r4 }
	val1 := v1.FieldByName("OkValue").Interface()
	val2 := v2.FieldByName("OkValue").Interface()
	val3 := v3.FieldByName("OkValue").Interface()
	val4 := v4.FieldByName("OkValue").Interface()
	result := fn.(func(any) any)(val1).(func(any) any)(val2).(func(any) any)(val3).(func(any) any)(val4)
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
			return SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil})
		}
		result := decoder.(func(any) any)(val)
		r := reflect.ValueOf(result)
		if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 1 {
			return result
		}
		inner := r.FieldByName("OkValue").Interface()
		return SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: inner})
	}
}

func Sky_json_decoder_Field(fieldName any, decoder any) any {
	return func(val any) any {
		m, ok := val.(map[string]any)
		if !ok { return SkyErr("Expecting an OBJECT with a field named '" + fieldName.(string) + "'") }
		v, exists := m[fieldName.(string)]
		if !exists {
			return SkyErr("Expecting an OBJECT with a field named '" + fieldName.(string) + "'")
		}
		return decoder.(func(any) any)(v)
	}
}

func Sky_json_decoder_At(path any, decoder any) any {
	keys := path.([]any)
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
		i := idx.(int)
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
		for i, item := range lst {
			decoded := decoder.(func(any) any)(item)
			r := reflect.ValueOf(decoded)
			if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 1 {
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
		for k, v := range m {
			decoded := decoder.(func(any) any)(v)
			r := reflect.ValueOf(decoded)
			if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 1 {
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
		for k, v := range m {
			decoded := decoder.(func(any) any)(v)
			r := reflect.ValueOf(decoded)
			if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 1 {
				return SkyErr(fmt.Sprintf("Problem at field '%s': %v", k, r.FieldByName("ErrValue").Interface()))
			}
			result = append(result, Tuple2{V0: k, V1: r.FieldByName("OkValue").Interface()})
		}
		return SkyOk(result)
	}
}

func sky_json_decoder_runAndCheck(decoder any, val any) (any, bool) {
	result := decoder.(func(any) any)(val)
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 1 {
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2))
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2).(func(any) any)(v3))
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2).(func(any) any)(v3).(func(any) any)(v4))
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2).(func(any) any)(v3).(func(any) any)(v4).(func(any) any)(v5))
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2).(func(any) any)(v3).(func(any) any)(v4).(func(any) any)(v5).(func(any) any)(v6))
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2).(func(any) any)(v3).(func(any) any)(v4).(func(any) any)(v5).(func(any) any)(v6).(func(any) any)(v7))
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
		return SkyOk(fn.(func(any) any)(v1).(func(any) any)(v2).(func(any) any)(v3).(func(any) any)(v4).(func(any) any)(v5).(func(any) any)(v6).(func(any) any)(v7).(func(any) any)(v8))
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
		lst := decoders.([]any)
		for _, d := range lst {
			result := d.(func(any) any)(val)
			r := reflect.ValueOf(result)
			if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 0 {
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
		if r.Kind() == reflect.Struct && r.FieldByName("Tag").Interface().(int) == 1 {
			return SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil})
		}
		inner := r.FieldByName("OkValue").Interface()
		return SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: inner})
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
	err := json.Unmarshal([]byte(s.(string)), &parsed)
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
	r := reflect.ValueOf(maybe)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if r.FieldByName("Tag").Interface().(int) == 0 {
			return r.FieldByName("JustValue").Interface()
		}
	}
	return defaultVal
}

func Sky_maybe_Map(fn any, maybe any) any {
	r := reflect.ValueOf(maybe)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if r.FieldByName("Tag").Interface().(int) == 1 {
			return maybe
		}
		inner := r.FieldByName("JustValue").Interface()
		return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: fn.(func(any) any)(inner)}
	}
	return maybe
}

func Sky_maybe_AndThen(fn any, maybe any) any {
	r := reflect.ValueOf(maybe)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if r.FieldByName("Tag").Interface().(int) == 1 {
			return maybe
		}
		inner := r.FieldByName("JustValue").Interface()
		return fn.(func(any) any)(inner)
	}
	return maybe
}

// ============= Result Operations =============

func Sky_result_WithDefault(defaultVal any, result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if r.FieldByName("Tag").Interface().(int) == 0 {
			return r.FieldByName("OkValue").Interface()
		}
	}
	return defaultVal
}

func Sky_result_Map(fn any, result any) any {
	r := reflect.ValueOf(result)
	if r.Kind() == reflect.Struct && r.FieldByName("Tag").IsValid() {
		if r.FieldByName("Tag").Interface().(int) == 1 {
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
		if r.FieldByName("Tag").Interface().(int) == 1 {
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
		if r.FieldByName("Tag").Interface().(int) == 0 {
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
		if r.FieldByName("Tag").Interface().(int) == 0 {
			return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: r.FieldByName("OkValue").Interface()}
		}
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil}
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
			name += " \\"" + a + "\\""
		case int:
			name += " " + fmt.Sprintf("%d", a)
		default:
			argStr := Sky_msgToString(a)
			name += " " + argStr.(string)
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
	s := c.(string)
	if len(s) == 0 { return false }
	r := []rune(s)[0]
	return r >= 'A' && r <= 'Z'
}

func Sky_char_IsLower(c any) any {
	s := c.(string)
	if len(s) == 0 { return false }
	r := []rune(s)[0]
	return r >= 'a' && r <= 'z'
}

func Sky_char_IsAlpha(c any) any {
	s := c.(string)
	if len(s) == 0 { return false }
	r := []rune(s)[0]
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func Sky_char_IsDigit(c any) any {
	s := c.(string)
	if len(s) == 0 { return false }
	r := []rune(s)[0]
	return r >= '0' && r <= '9'
}

func Sky_char_IsAlphaNum(c any) any {
	s := c.(string)
	if len(s) == 0 { return false }
	r := []rune(s)[0]
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

func Sky_char_ToUpper(c any) any {
	s := c.(string)
	return strings.ToUpper(s)
}

func Sky_char_ToLower(c any) any {
	s := c.(string)
	return strings.ToLower(s)
}

func Sky_char_ToCode(c any) any {
	s := c.(string)
	if len(s) == 0 { return 0 }
	return int([]rune(s)[0])
}

func Sky_char_FromCode(n any) any {
	return string(rune(n.(int)))
}

// ============= Bitwise Operations =============

func Sky_bitwise_And(a any, b any) any { return a.(int) & b.(int) }
func Sky_bitwise_Or(a any, b any) any { return a.(int) | b.(int) }
func Sky_bitwise_Xor(a any, b any) any { return a.(int) ^ b.(int) }
func Sky_bitwise_Complement(a any) any { return ^a.(int) }
func Sky_bitwise_ShiftLeftBy(amount any, val any) any { return val.(int) << uint(amount.(int)) }
func Sky_bitwise_ShiftRightBy(amount any, val any) any { return val.(int) >> uint(amount.(int)) }
func Sky_bitwise_ShiftRightZfBy(amount any, val any) any { return int(uint(val.(int)) >> uint(amount.(int))) }

// ============= Array Operations =============

func Sky_array_Empty() any { return []any{} }

func Sky_array_FromList(list any) any { return list }

func Sky_array_ToList(arr any) any { return arr }

func Sky_array_Get(index any, arr any) any {
	a := arr.([]any)
	i := index.(int)
	if i < 0 || i >= len(a) {
		return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing"} // Nothing
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: a[i]} // Just
}

func Sky_array_Set(index any, val any, arr any) any {
	a := arr.([]any)
	i := index.(int)
	if i < 0 || i >= len(a) { return arr }
	newArr := make([]any, len(a))
	copy(newArr, a)
	newArr[i] = val
	return newArr
}

func Sky_array_Push(val any, arr any) any {
	a := arr.([]any)
	return append(a, val)
}

func Sky_array_Length(arr any) any {
	return len(arr.([]any))
}

func Sky_array_Slice(start any, end any, arr any) any {
	a := arr.([]any)
	s := start.(int)
	e := end.(int)
	if s < 0 { s = 0 }
	if e > len(a) { e = len(a) }
	if s > e { return []any{} }
	return a[s:e]
}

func Sky_array_Map(f any, arr any) any {
	a := arr.([]any)
	fn := f.(func(any) any)
	result := make([]any, len(a))
	for i, v := range a { result[i] = fn(v) }
	return result
}

func Sky_array_Foldl(f any, acc any, arr any) any {
	a := arr.([]any)
	fn := f.(func(any) any)
	result := acc
	for _, v := range a { result = fn(v).(func(any) any)(result) }
	return result
}

func Sky_array_Foldr(f any, acc any, arr any) any {
	a := arr.([]any)
	fn := f.(func(any) any)
	result := acc
	for i := len(a) - 1; i >= 0; i-- { result = fn(a[i]).(func(any) any)(result) }
	return result
}

func Sky_array_Append(a any, b any) any {
	return append(a.([]any), b.([]any)...)
}

func Sky_array_IndexedMap(f any, arr any) any {
	a := arr.([]any)
	fn := f.(func(any) any)
	result := make([]any, len(a))
	for i, v := range a { result[i] = fn(i).(func(any) any)(v) }
	return result
}

// ============= File Operations =============

func Sky_file_ReadFile(path any) any {
	data, err := os.ReadFile(path.(string))
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: string(data)}
}

func Sky_file_WriteFile(path any, content any) any {
	err := os.WriteFile(path.(string), []byte(content.(string)), 0644)
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

func Sky_file_Exists(path any) any {
	_, err := os.Stat(path.(string))
	return err == nil
}

func Sky_file_Remove(path any) any {
	err := os.Remove(path.(string))
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

func Sky_file_MkdirAll(path any) any {
	err := os.MkdirAll(path.(string), 0755)
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}

func Sky_file_ReadDir(path any) any {
	entries, err := os.ReadDir(path.(string))
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
	info, err := os.Stat(path.(string))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// ============= Process Operations =============

func Sky_process_Run(command any, args any) any {
	argList := args.([]any)
	strArgs := make([]string, len(argList))
	for i, a := range argList {
		strArgs[i] = a.(string)
	}
	cmd := exec.Command(command.(string), strArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: fmt.Errorf("%s: %s", err.Error(), string(output))}
	}
	return SkyResult{Tag: 0, OkValue: string(output)}
}

func Sky_process_Exit(code any) any {
	os.Exit(code.(int))
	return struct{}{}
}

func Sky_process_GetEnv(key any) any {
	val, ok := os.LookupEnv(key.(string))
	if !ok {
		return struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing"} // Nothing
	}
	return struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: val} // Just
}

func Sky_process_GetCwd() any {
	dir, err := os.Getwd()
	if err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: dir}
}

func Sky_process_LoadEnv(filePath any) any {
	p := filePath.(string)
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
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		os.Setenv(key, val)
	}
	if err := scanner.Err(); err != nil {
		return SkyResult{Tag: 1, ErrValue: err}
	}
	return SkyResult{Tag: 0, OkValue: struct{}{}}
}
`;
      fs.writeFileSync(helperPath, helperCode);


    const imports = new Set<string>();

    const extractImports = (t: string) => {
        // ... previous implementation ...
        const matches = [...t.matchAll(/([a-zA-Z0-9_\/\.-]+)\.[a-zA-Z0-9_]+/g)];
        for (const m of matches) {
            const p = m[1];
            if (p.includes("/")) {
                imports.add(p);
            } else if (p !== pkg.name) {
                if (["io", "fmt", "time", "os", "context", "net", "http", "bufio", "log", "hash", "crypto", "syscall", "reflect", "strconv", "strings", "sort", "sync", "math", "errors", "image", "unicode", "bytes"].includes(p)) {
                    imports.add(p);
                }
            }
        }
    };

    // Always import the package we are wrapping
    imports.add(pkgName);

    // Resolve a Go import path to the package identifier used in code.
    // For the main package we have the actual name from the inspector.
    // For other packages, Go uses the last path segment, except for
    // major version suffixes (v2, v3, ...) which are skipped.
    const resolveGoPackageId = (importPath: string): string => {
        if (importPath === pkgName) return pkg.name;
        const parts = importPath.split("/");
        const last = parts[parts.length - 1];
        if (/^v\d+$/.test(last) && parts.length >= 2) {
            return parts[parts.length - 2];
        }
        return last;
    };

    const cleanType = (t: string) => {
        extractImports(t);
        const res = t.replace(/([a-zA-Z0-9_\/\.-]+)\.([a-zA-Z0-9_]+)/g, (_match, p1, p2) => {
            // If it's interface{}, it might be a sanitized unexported type
            if (p2 === "interface{}") return "any";
            return resolveGoPackageId(p1) + "." + p2;
        });
        if (res.includes("interface{}")) return res.replace(/interface\{\}/g, "any");
        return res;
    };

    const pkgBase = pkg.name;
    let goCode = "";
    const emittedWrappers = new Set<string>();

    // Strip Go parameter names from a type string, keeping only the type.
    // e.g. "shortcut fyne.Shortcut" -> "fyne.Shortcut"
    //      "*widget.Label" -> "*widget.Label"
    //      "int" -> "int"
    const stripParamName = (s: string): string => {
        const t = s.trim();
        // If it contains a space and the part after the last space looks like a type
        // (starts with *, [, or uppercase letter, or is a known primitive), strip the prefix.
        const spaceIdx = t.lastIndexOf(" ");
        if (spaceIdx > 0) {
            const afterSpace = t.substring(spaceIdx + 1);
            // Type indicators: pointer, slice, map, interface, func, or package-qualified
            if (/^[*\[\(]/.test(afterSpace) || afterSpace.includes(".") || /^(int|float|string|bool|byte|rune|error|any|uint|uintptr)/.test(afterSpace)) {
                return afterSpace;
            }
        }
        return t;
    };

    // Generate a Go adapter that bridges a Sky callback (func(any) any) to a
    // concrete Go function signature. Returns the cast code string, or null if
    // the type is not a function type or is too complex to bridge.
    const generateFuncBridge = (goType: string, argIdx: number): string | null => {
        // Match func(...) with optional return
        const funcMatch = goType.match(/^func\((.*?)\)\s*(.*)$/);
        if (!funcMatch) return null;

        const paramStr = funcMatch[1].trim();
        let retStr = funcMatch[2].trim();

        // Skip multi-return functions like (int, int) — too complex to bridge
        if (retStr.startsWith("(")) return null;

        // Parse parameter types, stripping any Go parameter names
        const paramTypes = paramStr
            ? paramStr.split(",").map(s => stripParamName(s))
            : [];

        // Build the adapter function
        const goParams = paramTypes.map((t, j) => `p${j} ${t}`).join(", ");

        // Sky functions are curried: func(any) any
        // For N params: f(p0).(func(any) any)(p1).(func(any) any)(p2)...
        // For 0 params: f(nil)
        let callChain: string;
        if (paramTypes.length === 0) {
            callChain = `_skyFn${argIdx}(nil)`;
        } else {
            callChain = `_skyFn${argIdx}(p0)`;
            for (let j = 1; j < paramTypes.length; j++) {
                callChain = `${callChain}.(func(any) any)(p${j})`;
            }
        }

        // If the Go callback has a return type, cast the result
        let body: string;
        if (retStr) {
            body = `return ${callChain}.(${retStr})`;
        } else {
            body = callChain;
        }

        return `\t_skyFn${argIdx} := arg${argIdx}.(func(any) any)\n\t_arg${argIdx} := func(${goParams})${retStr ? " " + retStr : ""} {\n\t\t${body}\n\t}`;
    };

    const generateFuncWrapper =(skyName: string, goName: string, params: Param[], results: Param[], isMethod = false, isField = false, recvType = "", variadic = false) => {
        const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
        let wrapperName = `Sky_${safePkg}_${skyNamePascal}`;
        
        /* Disable tree-shaking for now
        if (usedSymbols && !usedSymbols.has(wrapperName)) {
            return; // Skip unused wrapper
        }
        */
        
        imports.add(pkgName);

        let goParams = params.map((p, i) => {
            return `arg${i} any`;
        }).join(", ");
        
        let casts = params.map((p, i) => {
            let t = cleanType(p.type);
            // Replace net/http with just http if imported that way
            if (t.includes("net/http.ResponseWriter")) {
                t = t.replace(/net\/http\./g, "http.");
            }
            // Pointer-to-primitive: Sky passes Maybe, unwrap to Go pointer
            if (isGoPointerToPrimitive(p.type)) {
                const baseGoType = cleanType(p.type.replace(/^\*+/, ""));
                imports.add("reflect");
                return [
                    `\tvar _arg${i} ${t}`,
                    `\t_mv${i} := reflect.ValueOf(arg${i})`,
                    `\tif _mv${i}.Kind() == reflect.Struct {`,
                    `\t\t_tag${i} := _mv${i}.FieldByName("Tag")`,
                    `\t\tif _tag${i}.IsValid() && _tag${i}.Interface().(int) == 0 {`,
                    `\t\t\t_v${i} := _mv${i}.FieldByName("JustValue").Interface().(${baseGoType})`,
                    `\t\t\t_arg${i} = &_v${i}`,
                    `\t\t}`,
                    `\t}`,
                ].join("\n");
            }
            if (variadic && i === params.length - 1) {
                return `\tvar _arg${i} []${t.substring(2)}\n\tfor _, v := range arg${i}.([]any) {\n\t\t_arg${i} = append(_arg${i}, v.(${t.substring(2)}))\n\t}`;
            }
            // Bridge Sky callbacks (func(any) any) to Go callback signatures.
            // Sky lambdas always compile to func(any) any (curried).
            // We generate adapter functions that unwrap the curried calls.
            const funcBridge = generateFuncBridge(t, i);
            if (funcBridge) return funcBridge;
            return `\t_arg${i} := arg${i}.(${t})`;
        }).join("\n");
        
        if (recvType && (isMethod || isField)) {
            const recvArg = `this any`;
            goParams = goParams ? `${recvArg}, ${goParams}` : recvArg;
            casts = `\t_this := this.(${cleanType(recvType)})\n` + casts;
        }

        let goReturns = " ";
        let retTypes = results.map(r => cleanType(r.type));
        
        // Wrap in SkyResult if a function OR method returns an error as the last return value
        // Variables and fields should be returned as-is
        const shouldWrap = !isField && retTypes.length >= 1 && retTypes[retTypes.length - 1] === "error";

        // (T, bool) comma-ok pattern → Maybe T in Sky
        const isCommaOk = !isField && !shouldWrap && retTypes.length === 2 && retTypes[1] === "bool";

        // Check if the first result is a pointer-to-primitive (returns Maybe in Sky)
        const firstResultPtrPrimitive = results.length > 0 && isGoPointerToPrimitive(results[0].type);

        // Multi-return without error or comma-ok → wrap as Tuple
        const isMultiReturn = !isField && !shouldWrap && !isCommaOk && retTypes.length >= 2;

        if (shouldWrap) {
            goReturns = ` SkyResult `;
        } else if (isCommaOk || isMultiReturn) {
            goReturns = ` any `;
        } else if (retTypes.length > 0) {
            if (retTypes.length === 1) {
                if (firstResultPtrPrimitive) {
                    // Returns Maybe struct, not the raw Go pointer
                    goReturns = ` any `;
                } else {
                    // Fields/variables returning typed slices will be converted to []any
                    const needsSliceConv = isField && retTypes[0].startsWith("[]") && retTypes[0] !== "[]any";
                    goReturns = needsSliceConv ? ` any ` : ` ${retTypes[0]} `;
                }
            } else {
                goReturns = ` (${retTypes.join(", ")}) `;
            }
        } else if (!isField) {
            // Void Go functions still need to return any for Sky (all expressions are values)
            goReturns = ` any `;
        }

        // Skip duplicate wrapper names (e.g. const and method field with same name)
        if (emittedWrappers.has(wrapperName)) return;
        emittedWrappers.add(wrapperName);

        goCode += `func ${wrapperName}(${goParams})${goReturns}{\n`;
        if (casts.trim()) {
            goCode += `${casts}\n`;
        }
        
        const callArgs = params.map((p, i) => {
            if (p.variadic || (variadic && i === params.length - 1)) return `_arg${i}...`;
            return `_arg${i}`;
        }).join(", ");
        
        if (isField) {
            const fieldExpr = recvType ? `_this.${goName}` : `${pkgBase}.${goName}`;
            // Convert typed slices (e.g., []string) to []any for Sky compatibility
            const fieldRetType = retTypes.length === 1 ? retTypes[0] : "";
            if (firstResultPtrPrimitive) {
                // Pointer-to-primitive field: wrap in Maybe (Just/Nothing)
                goCode += `\t_fv := ${fieldExpr}\n`;
                goCode += `\tif _fv == nil {\n\t\treturn struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil}\n\t}\n`;
                goCode += `\treturn struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: *_fv}\n`;
            } else if (fieldRetType.startsWith("[]") && fieldRetType !== "[]any") {
                goCode += `\t_val := ${fieldExpr}\n`;
                goCode += `\t_result := make([]any, len(_val))\n`;
                goCode += `\tfor _i, _v := range _val { _result[_i] = _v }\n`;
                goCode += `\treturn _result\n`;
            } else {
                goCode += `\treturn ${fieldExpr}\n`;
            }
        } else {
            let callExpr = `${pkgBase}.${goName}(${callArgs})`;
            if (recvType) {
                callExpr = `_this.${goName}(${callArgs})`;
            }

            if (retTypes.length === 0) {
                goCode += `\t${callExpr}\n\treturn struct{}{}\n`;
            } else if (shouldWrap) {
                if (retTypes.length === 1) {
                    goCode += `\terr := ${callExpr}\n\tif err != nil {\n\t\treturn SkyErr(err)\n\t}\n\treturn SkyOk(struct{}{})\n`;
                } else if (retTypes.length === 2 && firstResultPtrPrimitive) {
                    // (*primitive, error) → Result Error (Maybe T)
                    goCode += `\t_res, err := ${callExpr}\n`;
                    goCode += `\tif err != nil {\n\t\treturn SkyErr(err)\n\t}\n`;
                    goCode += `\tif _res == nil {\n\t\treturn SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil})\n\t}\n`;
                    goCode += `\treturn SkyOk(struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: *_res})\n`;
                } else if (retTypes.length === 2) {
                    // (T, error) → Result Error T
                    goCode += `\tres, err := ${callExpr}\n\tif err != nil {\n\t\treturn SkyErr(err)\n\t}\n\treturn SkyOk(res)\n`;
                } else {
                    // (T1, T2, ..., error) → Result Error (Tuple of non-error values)
                    const valueCount = retTypes.length - 1;
                    const varNames = Array.from({length: valueCount}, (_, i) => `_r${i}`);
                    goCode += `\t${varNames.join(", ")}, err := ${callExpr}\n`;
                    goCode += `\tif err != nil {\n\t\treturn SkyErr(err)\n\t}\n`;
                    const tupleFields = varNames.map((v, i) => `V${i}: ${v}`).join(", ");
                    goCode += `\treturn SkyOk(Tuple${valueCount}{${tupleFields}})\n`;
                }
            } else if (isCommaOk) {
                // (T, bool) comma-ok → Maybe T
                goCode += `\t_val, _ok := ${callExpr}\n`;
                goCode += `\tif !_ok {\n\t\treturn struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil}\n\t}\n`;
                goCode += `\treturn struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: _val}\n`;
            } else if (isMultiReturn) {
                // (T1, T2, ...) without error → Tuple
                const varNames = retTypes.map((_, i) => `_r${i}`);
                goCode += `\t${varNames.join(", ")} := ${callExpr}\n`;
                const tupleFields = varNames.map((v, i) => `V${i}: ${v}`).join(", ");
                goCode += `\treturn Tuple${retTypes.length}{${tupleFields}}\n`;
            } else if (firstResultPtrPrimitive) {
                // Single *primitive return → Maybe T
                goCode += `\t_res := ${callExpr}\n`;
                goCode += `\tif _res == nil {\n\t\treturn struct{ Tag int; SkyName string; JustValue any }{Tag: 1, SkyName: "Nothing", JustValue: nil}\n\t}\n`;
                goCode += `\treturn struct{ Tag int; SkyName string; JustValue any }{Tag: 0, SkyName: "Just", JustValue: *_res}\n`;
            } else {
                goCode += `\treturn ${callExpr}\n`;
            }
        }
        
        goCode += `}\n\n`;
    }

    for (const f of pkg.funcs || []) {
        generateFuncWrapper(lowerCamelCase(f.name), f.name, f.params || [], f.results || [], false, false, "", f.variadic);
    }

    for (const v of pkg.vars || []) {
        // Generate variable wrappers as zero-arg Go functions for proper call semantics
        const skyName = lowerCamelCase(v.name);
        const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
        const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;
        const rawType = cleanType(v.type);
        const isSlice = rawType.startsWith("[]") && rawType !== "[]any";

        if (emittedWrappers.has(wrapperName)) continue;
        emittedWrappers.add(wrapperName);

        if (isSlice) {
            goCode += `func ${wrapperName}() any {\n`;
            goCode += `\t_val := ${pkgBase}.${v.name}\n`;
            goCode += `\t_result := make([]any, len(_val))\n`;
            goCode += `\tfor _i, _v := range _val { _result[_i] = _v }\n`;
            goCode += `\treturn _result\n`;
            goCode += `}\n\n`;
        } else {
            goCode += `func ${wrapperName}() any {\n`;
            goCode += `\treturn ${pkgBase}.${v.name}\n`;
            goCode += `}\n\n`;
        }
    }

    // Generate constant wrappers (same pattern as vars — zero-arg functions)
    for (const c of pkg.consts || []) {
        const skyName = lowerCamelCase(c.name);
        const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
        const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;

        if (emittedWrappers.has(wrapperName)) continue;
        emittedWrappers.add(wrapperName);

        goCode += `func ${wrapperName}() any {\n`;
        goCode += `\treturn ${pkgBase}.${c.name}\n`;
        goCode += `}\n\n`;
    }

    for (const t of pkg.types || []) {
        // Skip generic types (e.g., sql.Null[T]) — can't instantiate without type params
        if ((t as any).typeParams && (t as any).typeParams.length > 0) continue;
        if (t.methods) {
            for (const m of t.methods) {
                // Interfaces and map-based types (kind "other") use value receivers, not pointers
                const isValueRecv = t.kind === "interface" || t.kind === "other";
                const recv = isValueRecv ? `${pkg.name}.${t.name}` : `*${pkg.name}.${t.name}`;
                generateFuncWrapper(lowerCamelCase(t.name + m.name), m.name, m.params || [], m.results || [], true, false, recv, m.variadic);
            }
        }
        if (t.fields) {
            for (const f of t.fields) {
                const isInterface = t.kind === "interface";
                const recv = isInterface ? `${pkg.name}.${t.name}` : `*${pkg.name}.${t.name}`;
                generateFuncWrapper(lowerCamelCase(t.name + f.name), f.name, [], [{name: "", type: f.type}], false, true, recv);
            }
        }
    }

    // ============= Pattern-based convenience wrappers =============
    // Detect types with iterator+scan patterns (e.g., sql.Rows) and generate
    // high-level helpers that handle pointer allocation and iteration in Go,
    // returning Sky-friendly data structures.
    for (const t of pkg.types || []) {
        if ((t as any).typeParams && (t as any).typeParams.length > 0) continue;
        if (!t.methods) continue;

        const methodNames = new Set(t.methods.map(m => m.name));

        // Pattern: Iterator with Scan (e.g., sql.Rows)
        // Requires: Next() bool, Scan(...any) error, Columns() ([]string, error), Close() error
        const hasNext = methodNames.has("Next");
        const hasScan = methodNames.has("Scan");
        const hasColumns = methodNames.has("Columns");
        const hasClose = methodNames.has("Close");

        if (hasNext && hasScan && hasColumns && hasClose) {
            const recv = `*${pkg.name}.${t.name}`;
            const cleanRecv = cleanType(recv);
            const skyName = lowerCamelCase(t.name + "ToMaps");
            const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
            const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;

            imports.add("fmt");

            goCode += `// Auto-generated convenience wrapper: iterates ${t.name}, scans all rows into list of dicts
func ${wrapperName}(rows any) any {
	r := rows.(${cleanRecv})
	defer r.Close()
	cols, err := r.Columns()
	if err != nil {
		return SkyErr(err.Error())
	}
	var results []any
	for r.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := r.Scan(ptrs...); err != nil {
			return SkyErr(err.Error())
		}
		row := make(map[any]any)
		for i, col := range cols {
			switch v := values[i].(type) {
			case int64:
				row[col] = fmt.Sprintf("%d", v)
			case float64:
				row[col] = fmt.Sprintf("%g", v)
			case []byte:
				row[col] = string(v)
			case string:
				row[col] = v
			case nil:
				row[col] = ""
			default:
				row[col] = fmt.Sprintf("%v", v)
			}
		}
		results = append(results, row)
	}
	if results == nil {
		results = []any{}
	}
	return SkyOk(results)
}\n\n`;
        }

        // Pattern: DB-like type with Exec(string, ...any) + Query(string, ...any) methods
        // Only match when Exec takes a string first param (DB, Tx, Conn — not Stmt which takes just ...any)
        const execMethod = t.methods.find(m => m.name === "Exec");
        const queryMethod = t.methods.find(m => m.name === "Query");
        const execTakesQuery = execMethod && execMethod.params && execMethod.params.length >= 1 && execMethod.params[0].type === "string";
        const queryTakesQuery = queryMethod && queryMethod.params && queryMethod.params.length >= 1 && queryMethod.params[0].type === "string";
        if (execTakesQuery && queryTakesQuery) {
            const recv = `*${pkg.name}.${t.name}`;
            const cleanRecv = cleanType(recv);
            const skyName = lowerCamelCase(t.name + "ExecResult");
            const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
            const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;

            goCode += `// Auto-generated convenience wrapper: exec on ${t.name} returning rows affected
func ${wrapperName}(db any, query any, args any) any {
	_db := db.(${cleanRecv})
	_query := query.(string)
	var _args []any
	if args != nil {
		if lst, ok := args.([]any); ok {
			_args = lst
		}
	}
	result, err := _db.Exec(_query, _args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	affected, _ := result.RowsAffected()
	return SkyOk(affected)
}\n\n`;

            // Also generate a QueryToMaps convenience wrapper on DB/Tx types
            const skyNameQ = lowerCamelCase(t.name + "QueryToMaps");
            const skyNameQPascal = skyNameQ.charAt(0).toUpperCase() + skyNameQ.slice(1);
            const wrapperNameQ = `Sky_${safePkg}_${skyNameQPascal}`;

            imports.add("fmt");

            goCode += `// Auto-generated convenience wrapper: query on ${t.name} returning list of dicts
func ${wrapperNameQ}(db any, query any, args any) any {
	_db := db.(${cleanRecv})
	_query := query.(string)
	var _args []any
	if args != nil {
		if lst, ok := args.([]any); ok {
			_args = lst
		}
	}
	rows, err := _db.Query(_query, _args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return SkyErr(err.Error())
	}
	var results []any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return SkyErr(err.Error())
		}
		row := make(map[any]any)
		for i, col := range cols {
			switch v := values[i].(type) {
			case int64:
				row[col] = fmt.Sprintf("%d", v)
			case float64:
				row[col] = fmt.Sprintf("%g", v)
			case []byte:
				row[col] = string(v)
			case string:
				row[col] = v
			case nil:
				row[col] = ""
			default:
				row[col] = fmt.Sprintf("%v", v)
			}
		}
		results = append(results, row)
	}
	if results == nil {
		results = []any{}
	}
	return SkyOk(results)
}\n\n`;
        }
    }

    const wrapperPath = path.join(wrapperDir, `${safePkg}.go`);
    if (fs.existsSync(wrapperPath)) {
        fs.unlinkSync(wrapperPath);
    }

    if (goCode.trim() === "") {
        return; // No wrappers needed
    }

    // Clean goCode: remove functions that reference uninstantiated generic type parameters
    let cleanedGoCode = "";
    const funcBlocks = goCode.split(/(?=^func )/m);
    for (const block of funcBlocks) {
        // Skip functions that use sql.Null without type params (Go generics)
        if (block.includes("sql.Null)") || block.includes("sql.Null[")) {
            continue;
        }
        // Skip functions that reference unresolved Go generic type parameters
        // These appear as bare T, K, V, E etc. in type assertions or return types
        if (/\barg\d+\.\((?:\[\])?\*?[A-Z]\)/.test(block) ||       // .(T), .([]T), .(*T)
            /\) (?:\[\])?\*?[A-Z]\s*\{/.test(block) ||              // ) T {, ) *T {, ) []T {
            /\) \(\*?[A-Z],/.test(block)) {                          // ) (T, bool) {
            continue;
        }
        cleanedGoCode += block;
    }
    // Remove any imports whose package identifier isn't actually used in the generated code
    for (const imp of imports) {
        if (imp === pkgName) continue; // Always keep the main package import
        const base = resolveGoPackageId(imp);
        if (!cleanedGoCode.includes(base + ".")) {
            imports.delete(imp);
        }
    }
    const importsStr = Array.from(imports).map(i => `\t"${i}"`).join("\n");
    const finalCode = `package sky_wrappers\n\nimport (\n${importsStr}\n)\n\n` + cleanedGoCode;
    fs.writeFileSync(wrapperPath, finalCode);
}
