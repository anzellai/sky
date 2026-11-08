package rt

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func codecTupleFieldReference(rv reflect.Value, args []string, snake bool, pin *codecEnumRegistrySnapshot) (any, error) {
	if isSkyAdtType(rv.Type()) {
		return codecAutoEncodeTupleAt(rv, args, snake, pin)
	}
	plan := codecAutoFields(rv.Type(), snake)
	obj := plan.newObject()
	for ordinal, field := range plan.fields {
		raw, err := codecAutoEncodeTypedAt(rv.Field(field.index), args[field.index], snake, pin)
		if err != nil {
			return nil, err
		}
		obj.keys[ordinal] = field.key
		obj.vals[ordinal] = raw
	}
	return obj, nil
}

var codecTupleFieldEffect func()

type codecTupleFieldVariant struct{ V0 string }

func (codecTupleFieldVariant) SkyVariantTag() int { return 0 }
func (codecTupleFieldVariant) SkyVariantName() string {
	if codecTupleFieldEffect != nil {
		codecTupleFieldEffect()
	}
	return "TupleFieldMemoFX"
}
func TestCodecTupleFieldMemoKeysTypeAndSnakeWithoutCachingArgsOrValues(t *testing.T) {
	const before = "codec.fieldmemo.before"
	const after = "codec.fieldmemo.after"
	RegisterEnum(before, []string{"before0", "before1"})
	RegisterEnum(after, []string{"after0", "after1"})
	t.Cleanup(func() {
		codecTupleFieldEffect = nil
		setEnumRegistration(before, nil, false)
		setEnumRegistration(after, nil, false)
	})
	type tuple struct {
		FirstField codecTupleFieldVariant
		hidden     int
		LastField  int
	}
	memo := codecTupleFieldMemo{}
	var want string
	for _, planned := range []bool{false, true, true} {
		value := tuple{}
		args := []string{"any", "", before}
		calls := 0
		codecTupleFieldEffect = func() { calls++; args[2] = after; value.LastField = 1 }
		rv := reflect.ValueOf(&value).Elem()
		var raw any
		var err error
		if planned {
			raw, err = codecAutoEncodeTupleMemoAt(rv, args, true, nil, &memo)
		} else {
			raw, err = codecTupleFieldReference(rv, args, true, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(string(b), `"last_field":"after1"`) {
			t.Fatal("tuple memo cached args, value or exported ordinal")
		}
		if !planned {
			want = string(b)
		} else if string(b) != want {
			t.Fatal("tuple memo changed canonical bytes")
		}
	}
	codecTupleFieldEffect = nil
	camel := codecAutoTupleFieldsMemo(reflect.TypeOf(tuple{}), false, &memo)
	if camel.fields[0].key != "firstField" || memo.snake {
		t.Fatal("memo reused wrong snake mode")
	}
	other := codecAutoTupleFieldsMemo(reflect.TypeOf(T2[int, string]{}), false, &memo)
	if other == camel || memo.type_ != reflect.TypeOf(T2[int, string]{}) {
		t.Fatal("memo reused wrong actual type")
	}
}
func TestCodecTupleFieldMemoKeepsShortArgsAndADTGuardOrder(t *testing.T) {
	t.Cleanup(func() { codecTupleFieldEffect = nil })
	type tuple struct {
		First  codecTupleFieldVariant
		hidden int
		Last   string
	}
	memo := codecTupleFieldMemo{}
	for _, planned := range []bool{false, true, true} {
		calls := 0
		codecTupleFieldEffect = func() { calls++ }
		panicked := func() (panicked bool) {
			defer func() { panicked = recover() != nil }()
			if planned {
				_, _ = codecAutoEncodeTupleMemoAt(reflect.ValueOf(tuple{}), []string{"any", ""}, true, nil, &memo)
			} else {
				_, _ = codecTupleFieldReference(reflect.ValueOf(tuple{}), []string{"any", ""}, true, nil)
			}
			return
		}()
		if !panicked || calls != 1 {
			t.Fatal("schema memo prevalidated args or skipped earlier FX")
		}
	}
	oldPlan := memo.plan
	_, err := codecAutoEncodeTupleMemoAt(reflect.ValueOf(SkyADT{}), nil, true, nil, &memo)
	if err == nil || memo.plan != oldPlan {
		t.Fatal("ADT rejection happened after metadata memo lookup")
	}
}
func TestCodecTupleFieldMemoListKeepsLivePinnedEnumAndLateElementReads(t *testing.T) {
	const alias = "codec.fieldmemo.live"
	t.Cleanup(func() { codecTupleFieldEffect = nil; setEnumRegistration(alias, nil, false) })
	type tuple struct {
		V0 codecTupleFieldVariant
		V1 int
	}
	const declared = "rt.T2[any,codec.fieldmemo.live]"
	for _, pinned := range []bool{false, true} {
		var want string
		for _, planned := range []bool{false, true, true} {
			RegisterEnum(alias, []string{"before0", "before1"})
			pin := codecEnumRegistry()
			if !pinned {
				pin = nil
			}
			rows := []tuple{{}, {}}
			calls := 0
			codecTupleFieldEffect = func() { calls++; rows[0].V1 = 1; rows[1].V1 = 1; RegisterEnum(alias, []string{"after0", "after1"}) }
			var raw any
			var err error
			if planned {
				raw, err = codecAutoEncodeTypedAt(reflect.ValueOf(rows), "[]"+declared, true, pin)
			} else {
				out := make([]any, len(rows))
				for i := range rows {
					out[i], err = codecAutoEncodeTypedAt(reflect.ValueOf(&rows[i]).Elem(), declared, true, pin)
					if err != nil {
						break
					}
				}
				raw = out
			}
			if err != nil {
				t.Fatal(err)
			}
			b, e := json.Marshal(raw)
			if e != nil {
				t.Fatal(e)
			}
			expected := `"v1":"after1"`
			if pinned {
				expected = `"v1":"before1"`
			}
			if calls != 2 || strings.Count(string(b), expected) != 2 {
				t.Fatal("schema memo changed live/pinned registry positions or late reads")
			}
			if !planned {
				want = string(b)
			} else if string(b) != want {
				t.Fatal("planned list differs from original per-element dispatch")
			}
		}
	}
}
func TestCodecTupleFieldMemoFailurePositionsAndFreshOwnedBuffers(t *testing.T) {
	t.Cleanup(func() { codecTupleFieldEffect = nil })
	memo := codecTupleFieldMemo{}
	for _, bad := range []any{func() {}, math.NaN()} {
		var wantErr string
		wantCalls := 0
		for _, planned := range []bool{false, true} {
			calls := 0
			codecTupleFieldEffect = func() { calls++ }
			value := T3[any, any, any]{V0: codecTupleFieldVariant{}, V1: bad, V2: codecTupleFieldVariant{}}
			var raw any
			var err error
			if planned {
				raw, err = codecAutoEncodeTupleMemoAt(reflect.ValueOf(value), []string{"any", "any", "any"}, true, nil, &memo)
			} else {
				raw, err = codecTupleFieldReference(reflect.ValueOf(value), []string{"any", "any", "any"}, true, nil)
			}
			if err == nil {
				_, err = json.Marshal(raw)
			}
			if err == nil {
				t.Fatal("invalid synthetic value encoded")
			}
			if !planned {
				wantErr = err.Error()
				wantCalls = calls
			} else if err.Error() != wantErr || calls != wantCalls {
				t.Fatal("conversion/marshal error moved across FX")
			}
		}
	}
	codecTupleFieldEffect = nil
	encode := func() jsonOrderedObject {
		raw, e := codecAutoEncodeTupleMemoAt(reflect.ValueOf(T2[int, string]{V0: 1, V1: "x"}), []string{"int", "string"}, true, nil, &memo)
		if e != nil {
			t.Fatal(e)
		}
		return raw.(jsonOrderedObject)
	}
	a, b := encode(), encode()
	a.keys[0] = "changed"
	a.vals[0] = int64(99)
	if b.keys[0] != "v0" || b.vals[0] != int64(1) || len(b.keys) != 2 || cap(b.keys) != 2 || len(b.vals) != 2 || cap(b.vals) != 2 {
		t.Fatal("schema memo shared/coupled mutable object buffers")
	}
	empty, e := codecAutoEncodeTupleMemoAt(reflect.ValueOf(struct{ hidden int }{}), nil, true, nil, &memo)
	if e != nil || empty.(jsonOrderedObject).keys != nil || empty.(jsonOrderedObject).vals != nil {
		t.Fatal("empty schema changed nil representation")
	}
}
