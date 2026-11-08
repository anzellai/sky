package rt

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Exact pre-plan loops are the differential oracle. Only immutable schema
// discovery changed; actual TypedAt calls remain the reference implementation.
func codecFieldPlanReference(rv reflect.Value, args []string, snake bool, snapshot *codecEnumRegistrySnapshot, tuple bool) (any, error) {
	obj := jsonOrderedObject{}
	typ := rv.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		var raw any
		var err error
		if tuple {
			raw, err = codecAutoEncodeTypedAt(rv.Field(i), args[i], snake, snapshot)
		} else {
			raw, err = codecAutoEncodeTypedAt(rv.Field(i), skyTagType(field), snake, snapshot)
		}
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, skyColKey(field, snake))
		obj.vals = append(obj.vals, raw)
	}
	return obj, nil
}

var codecFieldPlanEffect func()

type codecFieldPlanVariant struct{ V0 string }

func (codecFieldPlanVariant) SkyVariantTag() int { return 0 }
func (codecFieldPlanVariant) SkyVariantName() string {
	if codecFieldPlanEffect != nil {
		codecFieldPlanEffect()
	}
	return "FieldPlanEffect"
}

type codecFieldPlanMarshal struct{ effect func() }

func (m codecFieldPlanMarshal) MarshalJSON() ([]byte, error) {
	m.effect()
	return []byte(`"foreign"`), nil
}

func codecFieldPlanEncode(rv reflect.Value, args []string, snake bool, snapshot *codecEnumRegistrySnapshot, tuple, planned bool) (any, error) {
	if !planned {
		return codecFieldPlanReference(rv, args, snake, snapshot, tuple)
	}
	if tuple {
		return codecAutoEncodeTupleAt(rv, args, snake, snapshot)
	}
	return codecAutoEncodeStructAt(rv, snake, snapshot)
}
func codecFieldPlanJSON(t *testing.T, raw any, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes)
}

func TestCodecFieldPlanKeepsConversionFXLaterReadsAndMarshalPhase(t *testing.T) {
	t.Cleanup(func() { codecFieldPlanEffect = nil })
	type row struct {
		First   codecFieldPlanVariant `sky:"first"`
		Foreign JsonValue             `sky:"foreign"`
		Later   string                `sky:"later"`
	}
	var reference string
	var referenceEvents []string
	for _, planned := range []bool{false, true, true} {
		model := row{First: codecFieldPlanVariant{"payload"}, Later: "before"}
		events := []string{}
		codecFieldPlanEffect = func() { events = append(events, "convert"); model.Later = "after-conversion" }
		model.Foreign = JsonValue{raw: codecFieldPlanMarshal{effect: func() { events = append(events, "marshal"); model.Later = "after-marshal" }}}
		raw, err := codecFieldPlanEncode(reflect.ValueOf(&model).Elem(), nil, true, nil, false, planned)
		encoded := codecFieldPlanJSON(t, raw, err)
		if !strings.Contains(encoded, `"later":"after-conversion"`) || model.Later != "after-marshal" {
			t.Fatal("later value read was moved across conversion/marshal FX")
		}
		if !planned {
			reference = encoded
			referenceEvents = append([]string(nil), events...)
		} else if encoded != reference || !reflect.DeepEqual(events, referenceEvents) {
			t.Fatal("schema plan changed output or FX order")
		}
	}
	codecFieldPlanEffect = nil
}

func TestCodecFieldPlanReadsEnumMetadataLiveOrPinnedAtOriginalField(t *testing.T) {
	const alias = "codec.fieldplan.enum"
	t.Cleanup(func() { codecFieldPlanEffect = nil; setEnumRegistration(alias, nil, false) })
	type row struct {
		First codecFieldPlanVariant `sky:"first"`
		Enum  int                   `sky:"enum,codec.fieldplan.enum"`
	}
	for _, pinned := range []bool{false, true} {
		for _, planned := range []bool{false, true, true} {
			RegisterEnum(alias, []string{"before"})
			snapshot := codecEnumRegistry()
			if !pinned {
				snapshot = nil
			}
			codecFieldPlanEffect = func() { RegisterEnum(alias, []string{"after"}) }
			raw, err := codecFieldPlanEncode(reflect.ValueOf(row{}), nil, true, snapshot, false, planned)
			encoded := codecFieldPlanJSON(t, raw, err)
			want := `"enum":"after"`
			if pinned {
				want = `"enum":"before"`
			}
			if !strings.Contains(encoded, want) {
				t.Fatal("cached field plan captured live/pinned enum metadata")
			}
		}
	}
}

func TestCodecFieldPlanTupleReadsCurrentArgsAndPhysicalExportedIndex(t *testing.T) {
	const left = "codec.fieldplan.left"
	const right = "codec.fieldplan.right"
	RegisterEnum(left, []string{"left"})
	RegisterEnum(right, []string{"right"})
	t.Cleanup(func() {
		codecFieldPlanEffect = nil
		setEnumRegistration(left, nil, false)
		setEnumRegistration(right, nil, false)
	})
	type tuple struct {
		First  codecFieldPlanVariant `sky:"first"`
		hidden string
		Enum   int `sky:"enum,codec.fieldplan.left"`
	}
	for _, planned := range []bool{false, true, true} {
		args := []string{"", "", left}
		codecFieldPlanEffect = func() { args[2] = right }
		raw, err := codecFieldPlanEncode(reflect.ValueOf(tuple{}), args, true, nil, true, planned)
		if !strings.Contains(codecFieldPlanJSON(t, raw, err), `"enum":"right"`) {
			t.Fatal("tuple args were cached or indexed by exported ordinal")
		}
	}
	codecFieldPlanEffect = nil
	// Same immutable plan is shared safely: struct uses its static tag while
	// tuple uses THIS invocation's supplied args, not a prior tuple's args.
	raw, err := codecAutoEncodeStructAt(reflect.ValueOf(tuple{}), true, nil)
	if !strings.Contains(codecFieldPlanJSON(t, raw, err), `"enum":"left"`) {
		t.Fatal("tuple invocation polluted static declared field tags")
	}
}

func TestCodecFieldPlanDoesNotPrevalidateShortTupleArgs(t *testing.T) {
	t.Cleanup(func() { codecFieldPlanEffect = nil })
	type tuple struct {
		First  codecFieldPlanVariant
		hidden int
		Last   string
	}
	var referenceCalls int
	for _, planned := range []bool{false, true, true} {
		calls := 0
		codecFieldPlanEffect = func() { calls++ }
		panicked := func() (panicked bool) {
			defer func() { panicked = recover() != nil }()
			_, _ = codecFieldPlanEncode(reflect.ValueOf(tuple{}), []string{"", ""}, true, nil, true, planned)
			return
		}()
		if !panicked || calls == 0 {
			t.Fatal("short args changed reference panic/earlier FX order")
		}
		if !planned {
			referenceCalls = calls
		} else if calls != referenceCalls {
			t.Fatal("tuple read was prevalidated or replayed")
		}
	}
	codecFieldPlanEffect = nil
}

func TestCodecFieldPlanKeepsConversionAndMarshalFailureOrder(t *testing.T) {
	t.Cleanup(func() { codecFieldPlanEffect = nil })
	type conversionBad struct {
		Bad   func() int
		After codecFieldPlanVariant
	}
	type marshalBad struct {
		Bad     float64
		After   codecFieldPlanVariant
		Foreign JsonValue
	}
	for _, planned := range []bool{false, true, true} {
		calls := 0
		marshals := 0
		codecFieldPlanEffect = func() { calls++ }
		_, err := codecFieldPlanEncode(reflect.ValueOf(conversionBad{Bad: func() int { return 1 }}), nil, true, nil, false, planned)
		if err == nil || calls != 0 {
			t.Fatal("conversion error no longer stops before later FX")
		}
		model := marshalBad{Bad: math.NaN(), Foreign: JsonValue{raw: codecFieldPlanMarshal{effect: func() { marshals++ }}}}
		raw, err := codecFieldPlanEncode(reflect.ValueOf(model), nil, true, nil, false, planned)
		if err != nil || calls == 0 {
			t.Fatal("NaN marshal failure skipped later Phase-A conversion")
		}
		if _, err = json.Marshal(raw); err == nil || marshals != 0 {
			t.Fatal("marshal error ordering changed")
		}
	}
	codecFieldPlanEffect = nil
}

func TestCodecFieldPlanFreshOutputSnakeKeysAndEmptyRepresentation(t *testing.T) {
	type row struct {
		CamelName string `sky:"camelName,string"`
		hidden    int
		Other     string `sky:"other,string"`
	}
	for _, snake := range []bool{false, true} {
		first, err := codecAutoEncodeStructAt(reflect.ValueOf(row{CamelName: "one", Other: "two"}), snake, nil)
		if err != nil {
			t.Fatal(err)
		}
		obj := first.(jsonOrderedObject)
		obj.keys[0] = "mutated"
		obj.vals[0] = "mutated"
		second, err := codecAutoEncodeStructAt(reflect.ValueOf(row{CamelName: "new", Other: "value"}), snake, nil)
		want := `{"camelName":"new","other":"value"}`
		if snake {
			want = `{"camel_name":"new","other":"value"}`
		}
		if codecFieldPlanJSON(t, second, err) != want {
			t.Fatal("result aliases cached keys/values or snake mode")
		}
	}
	empty, err := codecAutoEncodeStructAt(reflect.ValueOf(struct{ hidden int }{}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.(jsonOrderedObject); got.keys != nil || got.vals != nil {
		t.Fatal("empty ordered-object representation changed")
	}
	tupleEmpty, err := codecAutoEncodeTupleAt(reflect.ValueOf(struct{ hidden int }{}), nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tupleEmpty.(jsonOrderedObject); got.keys != nil || got.vals != nil {
		t.Fatal("unexported-only tuple prevalidated unused args")
	}
}

func TestCodecFieldPlanRetainsSchemaOnly(t *testing.T) {
	// This invariant catches accidental insertion of per-invocation reflect.Value,
	// tuple args, models, funcs or raw results in the globally retained plan.
	var schema func(reflect.Type) bool
	schema = func(typ reflect.Type) bool {
		switch typ.Kind() {
		case reflect.String, reflect.Int:
			return true
		case reflect.Slice:
			return schema(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				if !schema(typ.Field(i).Type) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	if !schema(reflect.TypeOf(codecAutoFieldPlan{})) {
		t.Fatal("field-plan cache retains non-schema data")
	}
	key := reflect.TypeOf(codecAutoFieldPlanKey{})
	for i := 0; i < key.NumField(); i++ {
		field := key.Field(i)
		if field.Type != reflect.TypeOf((*reflect.Type)(nil)).Elem() && field.Type.Kind() != reflect.Bool {
			t.Fatal("cache key contains invocation data")
		}
	}
}
