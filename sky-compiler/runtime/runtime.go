package runtime

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ═══════════════════════════════════════════════════
// CORE TYPES
// ═══════════════════════════════════════════════════

type Tuple2 struct{ V0, V1 any }
type Tuple3 struct{ V0, V1, V2 any }

type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}

type SkyMaybe struct {
	Tag      int
	SkyName  string
	JustValue any
}

type SkyRef struct{ Value any }

// ═══════════════════════════════════════════════════
// CONSTRUCTORS
// ═══════════════════════════════════════════════════

func SkyOk(v any) SkyResult   { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }
func SkyErr(v any) SkyResult  { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }
func SkyJust(v any) SkyMaybe  { return SkyMaybe{Tag: 0, SkyName: "Just", JustValue: v} }
func SkyNothing() SkyMaybe    { return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

// ═══════════════════════════════════════════════════
// SAFE TYPE ASSERTIONS
// ═══════════════════════════════════════════════════

func AsInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}

func AsFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	default:
		return 0
	}
}

func AsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func AsBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func AsList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func AsMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func AsTuple2(v any) Tuple2 {
	if t, ok := v.(Tuple2); ok {
		return t
	}
	return Tuple2{}
}

func AsTuple3(v any) Tuple3 {
	if t, ok := v.(Tuple3); ok {
		return t
	}
	return Tuple3{}
}

func AsSkyResult(v any) SkyResult {
	if r, ok := v.(SkyResult); ok {
		return r
	}
	return SkyResult{}
}

func AsSkyMaybe(v any) SkyMaybe {
	if m, ok := v.(SkyMaybe); ok {
		return m
	}
	return SkyMaybe{Tag: 1, SkyName: "Nothing"}
}

// ═══════════════════════════════════════════════════
// EQUALITY
// ═══════════════════════════════════════════════════

func Equal(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// ═══════════════════════════════════════════════════
// STRING OPERATIONS
// ═══════════════════════════════════════════════════

func StringFromInt(v any) any    { return strconv.Itoa(AsInt(v)) }
func StringFromFloat(v any) any  { return strconv.FormatFloat(AsFloat(v), 'f', -1, 64) }
func StringToUpper(v any) any    { return strings.ToUpper(AsString(v)) }
func StringToLower(v any) any    { return strings.ToLower(AsString(v)) }
func StringLength(v any) any     { return len(AsString(v)) }
func StringTrim(v any) any       { return strings.TrimSpace(AsString(v)) }
func StringIsEmpty(v any) any    { return AsString(v) == "" }
func StringReplace(old any) any {
	return func(new_ any) any {
		return func(s any) any {
			return strings.ReplaceAll(AsString(s), AsString(old), AsString(new_))
		}
	}
}
func StringContains(sub any) any {
	return func(s any) any { return strings.Contains(AsString(s), AsString(sub)) }
}
func StringStartsWith(prefix any) any {
	return func(s any) any { return strings.HasPrefix(AsString(s), AsString(prefix)) }
}
func StringEndsWith(suffix any) any {
	return func(s any) any { return strings.HasSuffix(AsString(s), AsString(suffix)) }
}
func StringSlice(start any) any {
	return func(end any) any {
		return func(s any) any {
			str := AsString(s)
			a, b := AsInt(start), AsInt(end)
			if a < 0 { a = 0 }
			if b > len(str) { b = len(str) }
			if a > b { return "" }
			return str[a:b]
		}
	}
}
func StringJoin(sep any) any {
	return func(list any) any {
		parts := AsList(list)
		ss := make([]string, len(parts))
		for i, p := range parts {
			ss[i] = AsString(p)
		}
		return strings.Join(ss, AsString(sep))
	}
}
func StringSplit(sep any) any {
	return func(s any) any {
		parts := strings.Split(AsString(s), AsString(sep))
		result := make([]any, len(parts))
		for i, p := range parts {
			result[i] = p
		}
		return result
	}
}
func StringToInt(s any) any {
	n, err := strconv.Atoi(strings.TrimSpace(AsString(s)))
	if err != nil {
		return SkyNothing()
	}
	return SkyJust(n)
}
func StringToFloat(s any) any {
	f, err := strconv.ParseFloat(strings.TrimSpace(AsString(s)), 64)
	if err != nil {
		return SkyNothing()
	}
	return SkyJust(f)
}
func StringAppend(a any) any {
	return func(b any) any { return AsString(a) + AsString(b) }
}

// ═══════════════════════════════════════════════════
// LIST OPERATIONS
// ═══════════════════════════════════════════════════

func ListMap(fn any) any {
	return func(list any) any {
		items := AsList(list)
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = fn.(func(any) any)(item)
		}
		return result
	}
}

func ListFilter(fn any) any {
	return func(list any) any {
		items := AsList(list)
		var result []any
		for _, item := range items {
			if AsBool(fn.(func(any) any)(item)) {
				result = append(result, item)
			}
		}
		if result == nil {
			return []any{}
		}
		return result
	}
}

func ListFoldl(fn any) any {
	return func(init any) any {
		return func(list any) any {
			acc := init
			for _, item := range AsList(list) {
				acc = fn.(func(any) any)(item).(func(any) any)(acc)
			}
			return acc
		}
	}
}

func ListFoldr(fn any) any {
	return func(init any) any {
		return func(list any) any {
			items := AsList(list)
			acc := init
			for i := len(items) - 1; i >= 0; i-- {
				acc = fn.(func(any) any)(items[i]).(func(any) any)(acc)
			}
			return acc
		}
	}
}

func ListLength(list any) any { return len(AsList(list)) }

func ListHead(list any) any {
	items := AsList(list)
	if len(items) > 0 {
		return SkyJust(items[0])
	}
	return SkyNothing()
}

func ListTail(list any) any {
	items := AsList(list)
	if len(items) > 1 {
		return SkyJust(items[1:])
	}
	if len(items) == 1 {
		return SkyJust([]any{})
	}
	return SkyNothing()
}

func ListReverse(list any) any {
	items := AsList(list)
	result := make([]any, len(items))
	for i, item := range items {
		result[len(items)-1-i] = item
	}
	return result
}

func ListIsEmpty(list any) any { return len(AsList(list)) == 0 }

func ListAppend(a any) any {
	return func(b any) any { return append(AsList(a), AsList(b)...) }
}

func ListConcat(lists any) any {
	var result []any
	for _, l := range AsList(lists) {
		result = append(result, AsList(l)...)
	}
	if result == nil {
		return []any{}
	}
	return result
}

func ListConcatMap(fn any) any {
	return func(list any) any {
		var result []any
		for _, item := range AsList(list) {
			result = append(result, AsList(fn.(func(any) any)(item))...)
		}
		if result == nil {
			return []any{}
		}
		return result
	}
}

func ListDrop(n any) any {
	return func(list any) any {
		items := AsList(list)
		count := AsInt(n)
		if count >= len(items) {
			return []any{}
		}
		return items[count:]
	}
}

func ListTake(n any) any {
	return func(list any) any {
		items := AsList(list)
		count := AsInt(n)
		if count >= len(items) {
			return items
		}
		return items[:count]
	}
}

func ListMember(item any) any {
	return func(list any) any {
		for _, x := range AsList(list) {
			if Equal(x, item) {
				return true
			}
		}
		return false
	}
}

func ListIndexedMap(fn any) any {
	return func(list any) any {
		items := AsList(list)
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = fn.(func(any) any)(i).(func(any) any)(item)
		}
		return result
	}
}

func ListFilterMap(fn any) any {
	return func(list any) any {
		var result []any
		for _, item := range AsList(list) {
			r := fn.(func(any) any)(item)
			if m, ok := r.(SkyMaybe); ok && m.Tag == 0 {
				result = append(result, m.JustValue)
			}
		}
		if result == nil {
			return []any{}
		}
		return result
	}
}

func ListRange(start any) any {
	return func(end any) any {
		a, b := AsInt(start), AsInt(end)
		var result []any
		for i := a; i <= b; i++ {
			result = append(result, i)
		}
		if result == nil {
			return []any{}
		}
		return result
	}
}

func ListSort(list any) any {
	// Simple insertion sort for any-typed lists
	items := AsList(list)
	result := make([]any, len(items))
	copy(result, items)
	for i := 1; i < len(result); i++ {
		key := result[i]
		j := i - 1
		for j >= 0 && fmt.Sprintf("%v", result[j]) > fmt.Sprintf("%v", key) {
			result[j+1] = result[j]
			j--
		}
		result[j+1] = key
	}
	return result
}

// ═══════════════════════════════════════════════════
// DICT OPERATIONS
// ═══════════════════════════════════════════════════

func DictEmpty() any                { return map[string]any{} }
func DictSingleton(k any) any       { return func(v any) any { return map[string]any{AsString(k): v} } }

func DictInsert(k any) any {
	return func(v any) any {
		return func(d any) any {
			m := AsMap(d)
			result := make(map[string]any, len(m)+1)
			for key, val := range m {
				result[key] = val
			}
			result[AsString(k)] = v
			return result
		}
	}
}

func DictGet(k any) any {
	return func(d any) any {
		m := AsMap(d)
		if v, ok := m[AsString(k)]; ok {
			return SkyJust(v)
		}
		return SkyNothing()
	}
}

func DictRemove(k any) any {
	return func(d any) any {
		m := AsMap(d)
		result := make(map[string]any, len(m))
		key := AsString(k)
		for k2, v := range m {
			if k2 != key {
				result[k2] = v
			}
		}
		return result
	}
}

func DictKeys(d any) any {
	m := AsMap(d)
	keys := make([]any, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func DictValues(d any) any {
	m := AsMap(d)
	vals := make([]any, 0, len(m))
	for _, v := range m {
		vals = append(vals, v)
	}
	return vals
}

func DictToList(d any) any {
	m := AsMap(d)
	pairs := make([]any, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, Tuple2{k, v})
	}
	return pairs
}

func DictFromList(list any) any {
	result := make(map[string]any)
	for _, item := range AsList(list) {
		t := AsTuple2(item)
		result[AsString(t.V0)] = t.V1
	}
	return result
}

func DictMap(fn any) any {
	return func(d any) any {
		m := AsMap(d)
		result := make(map[string]any, len(m))
		for k, v := range m {
			result[k] = fn.(func(any) any)(k).(func(any) any)(v)
		}
		return result
	}
}

func DictFoldl(fn any) any {
	return func(init any) any {
		return func(d any) any {
			acc := init
			for k, v := range AsMap(d) {
				acc = fn.(func(any) any)(k).(func(any) any)(v).(func(any) any)(acc)
			}
			return acc
		}
	}
}

func DictUnion(a any) any {
	return func(b any) any {
		ma, mb := AsMap(a), AsMap(b)
		result := make(map[string]any, len(ma)+len(mb))
		for k, v := range mb {
			result[k] = v
		}
		for k, v := range ma {
			result[k] = v
		}
		return result
	}
}

func DictMember(k any) any {
	return func(d any) any {
		_, ok := AsMap(d)[AsString(k)]
		return ok
	}
}

func DictSize(d any) any { return len(AsMap(d)) }

func DictIsEmpty(d any) any { return len(AsMap(d)) == 0 }

// ═══════════════════════════════════════════════════
// SET OPERATIONS (backed by map[string]bool)
// ═══════════════════════════════════════════════════

func SetEmpty() any { return map[string]bool{} }

func SetSingleton(v any) any { return map[string]bool{AsString(v): true} }

func SetInsert(v any) any {
	return func(s any) any {
		m := asSet(s)
		result := make(map[string]bool, len(m)+1)
		for k := range m {
			result[k] = true
		}
		result[AsString(v)] = true
		return result
	}
}

func SetMember(v any) any {
	return func(s any) any { return asSet(s)[AsString(v)] }
}

func SetRemove(v any) any {
	return func(s any) any {
		m := asSet(s)
		result := make(map[string]bool, len(m))
		key := AsString(v)
		for k := range m {
			if k != key {
				result[k] = true
			}
		}
		return result
	}
}

func SetUnion(a any) any {
	return func(b any) any {
		ma, mb := asSet(a), asSet(b)
		result := make(map[string]bool, len(ma)+len(mb))
		for k := range mb {
			result[k] = true
		}
		for k := range ma {
			result[k] = true
		}
		return result
	}
}

func SetDiff(a any) any {
	return func(b any) any {
		ma, mb := asSet(a), asSet(b)
		result := make(map[string]bool)
		for k := range ma {
			if !mb[k] {
				result[k] = true
			}
		}
		return result
	}
}

func SetToList(s any) any {
	m := asSet(s)
	result := make([]any, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}

func SetFromList(list any) any {
	result := make(map[string]bool)
	for _, item := range AsList(list) {
		result[AsString(item)] = true
	}
	return result
}

func SetIsEmpty(s any) any { return len(asSet(s)) == 0 }

func asSet(v any) map[string]bool {
	if m, ok := v.(map[string]bool); ok {
		return m
	}
	return nil
}

// ═══════════════════════════════════════════════════
// REF (MUTABLE REFERENCE)
// ═══════════════════════════════════════════════════

func RefNew(v any) any  { return &SkyRef{Value: v} }
func RefGet(r any) any  { return r.(*SkyRef).Value }
func RefSet(v any) any {
	return func(r any) any {
		r.(*SkyRef).Value = v
		return struct{}{}
	}
}

// ═══════════════════════════════════════════════════
// I/O
// ═══════════════════════════════════════════════════

var stdinReader *bufio.Reader

func Println(v any) any {
	fmt.Println(AsString(v))
	return struct{}{}
}

func ReadLine() any {
	if stdinReader == nil {
		stdinReader = bufio.NewReader(os.Stdin)
	}
	line, err := stdinReader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return SkyNothing()
	}
	return SkyJust(strings.TrimRight(line, "\r\n"))
}

func ReadBytes(n any) any {
	if stdinReader == nil {
		stdinReader = bufio.NewReader(os.Stdin)
	}
	count := AsInt(n)
	buf := make([]byte, count)
	total := 0
	for total < count {
		nr, err := stdinReader.Read(buf[total:])
		total += nr
		if err != nil {
			break
		}
	}
	if total == 0 {
		return SkyNothing()
	}
	return SkyJust(string(buf[:total]))
}

func WriteStdout(s any) any {
	fmt.Print(AsString(s))
	return struct{}{}
}

func WriteStderr(s any) any {
	fmt.Fprint(os.Stderr, AsString(s))
	return struct{}{}
}

// ═══════════════════════════════════════════════════
// FILE OPERATIONS
// ═══════════════════════════════════════════════════

func FileRead(path any) any {
	data, err := os.ReadFile(AsString(path))
	if err != nil {
		return SkyErr(err.Error())
	}
	return SkyOk(string(data))
}

func FileWrite(path any) any {
	return func(content any) any {
		err := os.WriteFile(AsString(path), []byte(AsString(content)), 0644)
		if err != nil {
			return SkyErr(err.Error())
		}
		return SkyOk(struct{}{})
	}
}

func FileMkdirAll(path any) any {
	err := os.MkdirAll(AsString(path), 0755)
	if err != nil {
		return SkyErr(err.Error())
	}
	return SkyOk(struct{}{})
}

// ═══════════════════════════════════════════════════
// PROCESS
// ═══════════════════════════════════════════════════

func ProcessExit(code any) any {
	os.Exit(AsInt(code))
	return struct{}{}
}

func ProcessGetArgs() any {
	args := make([]any, len(os.Args))
	for i, a := range os.Args {
		args[i] = a
	}
	return args
}

func ProcessGetArg(n any) any {
	idx := AsInt(n)
	if idx < len(os.Args) {
		return SkyJust(os.Args[idx])
	}
	return SkyNothing()
}

// ═══════════════════════════════════════════════════
// RECORD UPDATE
// ═══════════════════════════════════════════════════

func RecordUpdate(base any, updates any) any {
	m := AsMap(base)
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	for k, v := range AsMap(updates) {
		result[k] = v
	}
	return result
}

// Ensure imports are used
var _ = strings.Contains
var _ = strconv.Itoa
var _ = os.Exit
