package rt

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
)

// Original MarshalJSON body is the differential oracle. No schema lookup and
// no metadata read from the object, unused capacity or values.
func codecSchemaKeysReference(o jsonOrderedObject) ([]byte, error) {
	out := []byte{'{'}
	for i, k := range o.keys {
		if i > 0 {
			out = append(out, ',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		out = append(out, key...)
		out = append(out, ':')
		value, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		out = append(out, value...)
	}
	return append(out, '}'), nil
}
func codecSchemaKeyRow(t *testing.T) jsonOrderedObject {
	t.Helper()
	type row struct {
		First string `sky:"first,string"`
		Later string `sky:"later,string"`
		Last  string `sky:"last,string"`
	}
	raw, err := codecAutoEncodeStructAt(reflect.ValueOf(row{"a", "b", "c"}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw.(jsonOrderedObject)
}
func TestCodecSchemaKeysHaveCanonicalEscapingFromTypeMetadata(t *testing.T) {
	for _, key := range []string{"plain", "camelName", "\"slash\\line\n\t\x00", "<script>&", "\u2028\u2029", "é😀", string([]byte{'x', 0xff, 0xfe, 'X'}), ""} {
		for _, snake := range []bool{false, true} {
			typ := reflect.StructOf([]reflect.StructField{{Name: "Field", Type: reflect.TypeOf(""), Tag: reflect.StructTag("sky:" + strconv.Quote(key+",string"))}})
			v := reflect.New(typ).Elem()
			v.Field(0).SetString("value")
			raw, err := codecAutoEncodeStructAt(v, snake, nil)
			if err != nil {
				t.Fatal(err)
			}
			obj := raw.(jsonOrderedObject)
			cached, ok := codecAutoSchemaKeys.Load(obj.keys[0])
			encoded, e := json.Marshal(obj.keys[0])
			if e != nil || !ok || cached.(string) != string(encoded) {
				t.Fatal("schema key was not cached canonically")
			}
			got, e1 := obj.MarshalJSON()
			want, e2 := codecSchemaKeysReference(obj)
			if e1 != nil || e2 != nil || !bytes.Equal(got, want) {
				t.Fatal("schema cache changed canonical escaped bytes")
			}
		}
	}
}

type codecSchemaKeyFX struct {
	effect     func()
	err        error
	panicValue any
}

func (x codecSchemaKeyFX) MarshalJSON() ([]byte, error) {
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
func TestCodecSchemaKeysReadCurrentKeyAfterEarlierValueEffects(t *testing.T) {
	var reference string
	for _, planned := range []bool{false, true, true} {
		o := codecSchemaKeyRow(t)
		calls := 0
		o.vals[0] = codecSchemaKeyFX{effect: func() { calls++; o.keys[1] = "uncached <changed>&" + string([]byte{0xff}); o.vals[1] = "new value" }}
		var b []byte
		var err error
		if planned {
			b, err = o.MarshalJSON()
		} else {
			b, err = codecSchemaKeysReference(o)
		}
		if err != nil || calls != 1 {
			t.Fatal("value effect count changed")
		}
		if !planned {
			reference = string(b)
		} else if string(b) != reference {
			t.Fatal("current key/value was read before preceding value effect")
		}
	}
}
func TestCodecSchemaKeysNeverCacheDynamicObjectOrDictKeys(t *testing.T) {
	for i := 0; i < 100; i++ {
		key := "codec.test.unregistered.user.data." + strconv.Itoa(i)
		if _, exists := codecAutoSchemaKeys.Load(key); exists {
			t.Fatal("test dynamic key unexpectedly registered")
		}
		o := jsonOrderedObject{keys: []string{key}, vals: []any{i}}
		if _, err := o.MarshalJSON(); err != nil {
			t.Fatal(err)
		}
		if _, exists := codecAutoSchemaKeys.Load(key); exists {
			t.Fatal("dynamic user key inserted into global schema cache")
		}
	}
}
func TestCodecSchemaKeysKeepValueErrorsPanicsAndCyclesInOrder(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, failure := range []any{math.NaN(), cycle, codecSchemaKeyFX{err: errors.New("value failure")}, codecSchemaKeyFX{panicValue: "value panic"}} {
		var referenceErr string
		var referencePanic bool
		var referenceCalls int
		for _, planned := range []bool{false, true, true} {
			o := codecSchemaKeyRow(t)
			calls := 0
			o.vals[0] = codecSchemaKeyFX{effect: func() { calls++ }}
			o.vals[1] = failure
			o.vals[2] = codecSchemaKeyFX{effect: func() { calls += 100 }}
			var err error
			panicked := func() (panicked bool) {
				defer func() { panicked = recover() != nil }()
				if planned {
					_, err = o.MarshalJSON()
				} else {
					_, err = codecSchemaKeysReference(o)
				}
				return
			}()
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			if !planned {
				referenceErr = errText
				referencePanic = panicked
				referenceCalls = calls
			} else if errText != referenceErr || panicked != referencePanic || calls != referenceCalls {
				t.Fatal("schema keys changed value failure/FX order")
			}
			if calls != 1 || (err == nil && !panicked) {
				t.Fatal("later value executed after reference failure")
			}
		}
	}
}
func TestCodecSchemaKeysPreserveObjectRepresentationAndLogicalOps(t *testing.T) {
	o := codecSchemaKeyRow(t)
	if reflect.TypeOf(o).NumField() != 2 || len(o.keys) != cap(o.keys) || len(o.vals) != cap(o.vals) {
		t.Fatal("schema cache changed object layout/owned capacities")
	}
	o.keys[0] = "<mutated>"
	plain := jsonOrderedObject{keys: append([]string(nil), o.keys...), vals: append([]any(nil), o.vals...)}
	got, e1 := o.MarshalJSON()
	want, e2 := codecSchemaKeysReference(plain)
	if e1 != nil || e2 != nil || !bytes.Equal(got, want) {
		t.Fatal("mutated keys differ from canonical bytes")
	}
	a, b := JsonValue{raw: o}, JsonValue{raw: plain}
	if !deepEq(a, b) || hashAny(a) != hashAny(b) || SkyShow(a) != SkyShow(b) {
		t.Fatal("schema cache changed logical equality/hash/show")
	}
	ca, oka := cmpSafe(a, a)
	cb, okb := cmpSafe(a, b)
	if ca != cb || oka != okb {
		t.Fatal("schema cache changed comparison classification")
	}
	ta, e1 := jsonDecodeTreeOf(a)
	tb, e2 := jsonDecodeTreeOf(b)
	if e1 != nil || e2 != nil || !reflect.DeepEqual(ta, tb) {
		t.Fatal("schema cache changed decoder tree")
	}
	ga, e1 := a.GobEncode()
	gb, e2 := b.GobEncode()
	if e1 != nil || e2 != nil || !bytes.Equal(ga, gb) {
		t.Fatal("schema cache changed Gob wire")
	}
	var restored JsonValue
	if err := restored.GobDecode(ga); err != nil {
		t.Fatal(err)
	}
	if !deepEq(a, restored) {
		t.Fatal("schema cache changed restored meaning")
	}
}
func TestCodecSchemaKeysDontReadLaterKeysBeforeValueSynchronization(t *testing.T) {
	for _, planned := range []bool{false, true} {
		o := codecSchemaKeyRow(t)
		done := make(chan struct{})
		go func() { o.keys[1] = "synchronized <later>"; close(done) }()
		o.vals[0] = codecSchemaKeyFX{effect: func() { <-done }}
		var b []byte
		var err error
		if planned {
			b, err = o.MarshalJSON()
		} else {
			b, err = codecSchemaKeysReference(o)
		}
		if err != nil || !bytes.Contains(b, []byte(`"synchronized \u003clater\u003e"`)) {
			t.Fatal("later key was read before synchronization")
		}
	}
}
