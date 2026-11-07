package rt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// Exact pre-primitive-fastpath baseline, including preallocation/schema keys.
func codecPrimitiveJSONReference(o jsonOrderedObject) ([]byte, error) {
	// A fixed small starting buffer avoids repeated growth for tiny objects.
	// No keys, values, lengths or spare caller capacity are read ahead.
	out := make([]byte, 1, 32)
	out[0] = '{'
	for i, k := range o.keys {
		if i > 0 {
			out = append(out, ',')
		}
		// Read this current key ONLY at its original position, after all
		// preceding value effects. Dynamic misses never populate the cache.
		if encoded, ok := codecAutoSchemaKeys.Load(k); ok {
			out = append(out, encoded.(string)...)
		} else {
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
		}
		out = append(out, ':')
		vb, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		out = append(out, vb...)
	}
	out = append(out, '}')
	return out, nil
}

var codecPrimitiveJSONEffect func()

type codecPrimitiveNamedInt int64

func (v codecPrimitiveNamedInt) MarshalJSON() ([]byte, error) {
	if codecPrimitiveJSONEffect != nil {
		codecPrimitiveJSONEffect()
	}
	if v == -1 {
		return nil, errors.New("synthetic named integer error")
	}
	if v == -2 {
		panic("synthetic named integer panic")
	}
	return []byte(`"named-int"`), nil
}

type codecPrimitiveNamedString string

func (v codecPrimitiveNamedString) MarshalJSON() ([]byte, error) {
	if codecPrimitiveJSONEffect != nil {
		codecPrimitiveJSONEffect()
	}
	return []byte(`"named-string"`), nil
}

type codecPrimitiveNamedBool bool

func (v codecPrimitiveNamedBool) MarshalJSON() ([]byte, error) { return []byte(`"named-bool"`), nil }

type codecPrimitivePlainInt int64
type codecPrimitivePlainString string

func codecPrimitiveJSONRun(o jsonOrderedObject, planned bool) (data []byte, err string, panicValue string) {
	defer func() {
		if p := recover(); p != nil {
			panicValue = fmt.Sprint(p)
		}
	}()
	var e error
	if planned {
		data, e = o.MarshalJSON()
	} else {
		data, e = codecPrimitiveJSONReference(o)
	}
	if e != nil {
		err = e.Error()
	}
	return
}
func TestCodecPrimitiveJSONMatchesBuiltinAndFallbackStandardBytes(t *testing.T) {
	values := []any{nil, true, false, int64(0), int64(-1), int64(math.MinInt64), int64(math.MaxInt64), int(7), uint64(math.MaxUint64), math.Copysign(0, -1), math.NaN(), math.Inf(1), json.Number("1.25"), "", "plain ASCII", "\x7f", `quote"`, "slash\\", "<>&", "\x00\n\t", "é😀\u2028\u2029", string([]byte{0xff, 0xfe, 'x'}), codecPrimitiveNamedInt(8), codecPrimitiveNamedInt(-1), codecPrimitiveNamedInt(-2), codecPrimitiveNamedString("plain"), codecPrimitiveNamedBool(true), codecPrimitivePlainInt(8), codecPrimitivePlainString("plain")}
	var pointer *int
	values = append(values, pointer, func() {})
	random := rand.New(rand.NewSource(8102026))
	for i := 0; i < 1000; i++ {
		values = append(values, int64(random.Uint64()))
		raw := make([]byte, random.Intn(96))
		_, _ = random.Read(raw)
		values = append(values, string(raw))
		safe := make([]byte, random.Intn(96))
		for j := range safe {
			safe[j] = byte(0x20 + random.Intn(0x60))
		}
		values = append(values, string(safe))
	}
	for b := 0; b < 256; b++ {
		values = append(values, string([]byte{byte(b)}))
	}
	for _, v := range values {
		o := jsonOrderedObject{keys: []string{"value"}, vals: []any{v}}
		want, e1, p1 := codecPrimitiveJSONRun(o, false)
		got, e2, p2 := codecPrimitiveJSONRun(o, true)
		if !bytes.Equal(want, got) || e1 != e2 || p1 != p2 {
			t.Fatal("builtin fastpath changed canonical bytes/errors/panics")
		}
	}
}
func TestCodecPrimitiveJSONSafeASCIIAdmitsOnlyUnescapedJSONBytes(t *testing.T) {
	for b := 0; b < 256; b++ {
		s := string([]byte{byte(b)})
		want := b >= 0x20 && b < 0x80 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&'
		if codecJSONSafeASCII(s) != want {
			t.Fatal("ASCII predicate widened past audited exact domain")
		}
	}
	if !codecJSONSafeASCII("") {
		t.Fatal("empty string should be exact quoted empty string")
	}
}
func TestCodecPrimitiveJSONKeepsNamedMarshalerEffectsAndLaterReads(t *testing.T) {
	t.Cleanup(func() { codecPrimitiveJSONEffect = nil })
	for _, kind := range []string{"success", "error", "panic", "nan"} {
		var want []byte
		var wantError, wantPanic string
		var wantEvents []string
		for _, planned := range []bool{false, true} {
			var events []string
			o := jsonOrderedObject{keys: []string{"first", "old", "last"}, vals: []any{codecPrimitiveNamedString("first"), int64(1), codecPrimitiveNamedInt(8)}}
			codecPrimitiveJSONEffect = func() { events = append(events, "called"); o.keys[1] = "changed<>&"; o.vals[1] = int64(math.MinInt64) }
			switch kind {
			case "error":
				o.vals[0] = codecPrimitiveNamedInt(-1)
			case "panic":
				o.vals[0] = codecPrimitiveNamedInt(-2)
			case "nan":
				codecPrimitiveJSONEffect = func() { events = append(events, "called"); o.vals[1] = math.NaN() }
			}
			got, e, p := codecPrimitiveJSONRun(o, planned)
			if !planned {
				want = got
				wantError = e
				wantPanic = p
				wantEvents = events
			} else if !bytes.Equal(got, want) || e != wantError || p != wantPanic || !reflect.DeepEqual(events, wantEvents) {
				t.Fatal("named marshaler, error wrapper or later read position changed")
			}
			if kind == "success" && len(events) != 2 {
				t.Fatal("named primitive marshaler was bypassed")
			}
			if kind != "success" && len(events) != 1 {
				t.Fatal("later marshaler ran after failure")
			}
		}
	}
}
func TestCodecPrimitiveJSONRetainsOriginalShortValuePanicPosition(t *testing.T) {
	t.Cleanup(func() { codecPrimitiveJSONEffect = nil })
	for _, planned := range []bool{false, true} {
		calls := 0
		codecPrimitiveJSONEffect = func() { calls++ }
		_, err, panicked := codecPrimitiveJSONRun(jsonOrderedObject{keys: []string{"first", "missing"}, vals: []any{codecPrimitiveNamedString("first")}}, planned)
		if calls != 1 || err != "" || panicked == "" {
			t.Fatal("short values panicked before earlier marshaler")
		}
	}
}
func TestCodecPrimitiveJSONReducesPerTupleValueAllocations(t *testing.T) {
	codecAutoFields(reflect.TypeOf(T2[int, string]{}), true)
	o := jsonOrderedObject{keys: []string{"v0", "v1"}, vals: []any{int64(100000), "x"}}
	var retained []byte
	baseline := testing.AllocsPerRun(100, func() {
		b, e := codecPrimitiveJSONReference(o)
		if e != nil {
			panic(e)
		}
		retained = b
	})
	proposed := testing.AllocsPerRun(100, func() {
		b, e := o.MarshalJSON()
		if e != nil {
			panic(e)
		}
		retained = b
	})
	if string(retained) != `{"v0":100000,"v1":"x"}` {
		t.Fatal("tuple bytes changed")
	}
	t.Logf("tuple value allocations baseline=%.0f proposed=%.0f saved=%.0f", baseline, proposed, baseline-proposed)
	if baseline-proposed < 2 {
		t.Fatal("exact builtin tuple values still allocate individual marshal results")
	}
	// Unsafe strings continue through standard encoding/json.
	unsafe := jsonOrderedObject{keys: []string{"value"}, vals: []any{strings.Repeat("<é", 20)}}
	got, e := unsafe.MarshalJSON()
	want, e2 := codecPrimitiveJSONReference(unsafe)
	if e != nil || e2 != nil || !bytes.Equal(got, want) {
		t.Fatal("unsafe string fallback differs")
	}
}

func TestCodecPrimitiveJSONLongStringsStayOnCanonicalFallback(t *testing.T) {
	t.Cleanup(func() { codecPrimitiveJSONEffect = nil })
	if !codecJSONSafeASCII(strings.Repeat("x", 64)) || codecJSONSafeASCII(strings.Repeat("x", 65)) {
		t.Fatal("safe ASCII scan is not bounded at64bytes")
	}
	cases := []any{
		strings.Repeat("x", 64), strings.Repeat("x", 65), strings.Repeat("x", 1<<20),
		strings.Repeat("x", 1<<20) + "é", strings.Repeat("x", 1<<20) + "\"",
		codecPrimitiveNamedString(strings.Repeat("x", 1<<20)),
	}
	for _, value := range cases {
		o := jsonOrderedObject{keys: []string{"value"}, vals: []any{value}}
		calls := 0
		codecPrimitiveJSONEffect = func() { calls++ }
		want, e1, p1 := codecPrimitiveJSONRun(o, false)
		referenceCalls := calls
		calls = 0
		got, e2, p2 := codecPrimitiveJSONRun(o, true)
		if !bytes.Equal(want, got) || e1 != e2 || p1 != p2 || calls != referenceCalls {
			t.Fatal("long string fallback changed canonical bytes or named marshaler effects")
		}
		if _, named := value.(codecPrimitiveNamedString); named && calls != 1 {
			t.Fatal("long named string marshaler was bypassed")
		}
	}
}
