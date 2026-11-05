package rt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Exact preallocation baseline, including current schema-key-only lookup.
func codecObjectPreallocReference(o jsonOrderedObject) ([]byte, error) {
	out := []byte{'{'}
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

type codecObjectPreallocFX struct {
	effect     func()
	err        error
	panicValue any
}

func (x codecObjectPreallocFX) MarshalJSON() ([]byte, error) {
	if x.effect != nil {
		x.effect()
	}
	if x.panicValue != nil {
		panic(x.panicValue)
	}
	if x.err != nil {
		return nil, x.err
	}
	return []byte(`"effect"`), nil
}
func codecObjectPreallocRun(o jsonOrderedObject, planned bool) (data []byte, err string, panicValue string) {
	defer func() {
		if p := recover(); p != nil {
			panicValue = fmt.Sprint(p)
		}
	}()
	var e error
	if planned {
		data, e = o.MarshalJSON()
	} else {
		data, e = codecObjectPreallocReference(o)
	}
	if e != nil {
		err = e.Error()
	}
	return
}
func TestCodecObjectPreallocPreservesAllCanonicalBytesAndObjectShapes(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	cases := []jsonOrderedObject{
		{}, {keys: []string{}, vals: []any{}},
		{keys: []string{"x"}, vals: []any{1}},
		{keys: []string{"v0", "v1"}, vals: []any{100000, "x"}},
		{keys: []string{"<script>&\u2028\u2029", string([]byte{0xff, 'x'})}, vals: []any{"\"\\\n😀", nil}},
		{keys: []string{strings.Repeat("long", 1000)}, vals: []any{strings.Repeat("value", 10000)}},
		{keys: []string{"bad"}, vals: []any{math.NaN()}},
		{keys: []string{"cycle"}, vals: []any{cycle}},
		{keys: []string{"missing"}},
		{keys: []string{"one"}, vals: []any{1, 2}},
	}
	for _, o := range cases {
		a, e1, p1 := codecObjectPreallocRun(o, false)
		b, e2, p2 := codecObjectPreallocRun(o, true)
		if !bytes.Equal(a, b) || e1 != e2 || p1 != p2 {
			t.Fatal("fixed buffer changed bytes/error/panic")
		}
	}
}
func TestCodecObjectPreallocPreservesLaterKeyAndValueReadsAndFailureOrder(t *testing.T) {
	for _, failure := range []string{"none", "error", "panic", "nan", "short"} {
		var wantBytes []byte
		var wantErr, wantPanic string
		var wantEvents []string
		for _, planned := range []bool{false, true} {
			var events []string
			o := jsonOrderedObject{keys: []string{"first", "old", "last"}, vals: make([]any, 3)}
			x := codecObjectPreallocFX{effect: func() { events = append(events, "first"); o.keys[1] = "new<>&"; o.vals[1] = "after" }}
			switch failure {
			case "error":
				x.err = errors.New("synthetic failure")
			case "panic":
				x.panicValue = "synthetic panic"
			}
			if failure == "nan" {
				x.effect = func() { events = append(events, "first"); o.keys[1] = "bad"; o.vals[1] = math.NaN() }
			}
			o.vals[0] = x
			o.vals[1] = "before"
			o.vals[2] = codecObjectPreallocFX{effect: func() { events = append(events, "last") }}
			if failure == "short" {
				o.vals = o.vals[:1]
				x.effect = func() { events = append(events, "first"); o.keys[1] = "after" }
				o.vals[0] = x
			}
			b, e, p := codecObjectPreallocRun(o, planned)
			if !planned {
				wantBytes = b
				wantErr = e
				wantPanic = p
				wantEvents = events
			} else if !bytes.Equal(b, wantBytes) || e != wantErr || p != wantPanic || !reflect.DeepEqual(events, wantEvents) {
				t.Fatal("preallocation moved reads, marshal FX or failures")
			}
			if failure == "none" && (strings.Join(events, ",") != "first,last" || !strings.Contains(string(b), `"new\u003c\u003e\u0026":"after"`)) {
				t.Fatal("later key/value was read before first effect")
			}
			if failure != "none" && strings.Join(events, ",") != "first" {
				t.Fatal("later marshal ran after failure")
			}
		}
	}
}
func TestCodecObjectPreallocReducesTinyTupleAllocations(t *testing.T) {
	// Warm only canonical static field-name metadata. Mutable object buffers
	// and every individual value marshal remain per invocation.
	codecAutoFields(reflect.TypeOf(T2[int, string]{}), true)
	o := jsonOrderedObject{keys: []string{"v0", "v1"}, vals: []any{int64(100000), "x"}}
	var retained []byte
	baseline := testing.AllocsPerRun(100, func() {
		b, e := codecObjectPreallocReference(o)
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
	t.Logf("tiny tuple allocations baseline=%.0f proposed=%.0f saved=%.0f", baseline, proposed, baseline-proposed)
	if baseline-proposed < 1 {
		t.Fatal("fixed small buffer did not eliminate a growth allocation")
	}
}
