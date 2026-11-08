package rt

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// This oracle retains the original list loop: each element parses its own
// tuple declaration through the public/default canonical helper.
func codecTupleParseReference(rows any, elem string, snapshot *codecEnumRegistrySnapshot) (any, error) {
	rv := reflect.ValueOf(rows)
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		raw, err := codecAutoEncodeTypedAt(rv.Index(i), elem, true, snapshot)
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}
func codecTupleParseEncode(rows any, elem string, snapshot *codecEnumRegistrySnapshot, planned bool) (any, error) {
	if !planned {
		return codecTupleParseReference(rows, elem, snapshot)
	}
	return codecAutoEncodeTypedAt(reflect.ValueOf(rows), "[]"+elem, true, snapshot)
}

var codecTupleParseEffect func()

type codecTupleParseVariant struct{ V0 string }

func (codecTupleParseVariant) SkyVariantTag() int { return 0 }
func (codecTupleParseVariant) SkyVariantName() string {
	if codecTupleParseEffect != nil {
		codecTupleParseEffect()
	}
	return "TupleParseFX"
}

type codecTupleParseMarshal struct{ effect func() }

func (m codecTupleParseMarshal) MarshalJSON() ([]byte, error) {
	m.effect()
	return []byte(`"foreign"`), nil
}

func TestCodecTupleParseReuseKeepsLivePinnedMetadataAndLaterElementReads(t *testing.T) {
	const alias = "codec.tupleparse.enum"
	t.Cleanup(func() { codecTupleParseEffect = nil; setEnumRegistration(alias, nil, false) })
	type tuple struct {
		V0 codecTupleParseVariant
		V1 int
	}
	for _, pinned := range []bool{false, true} {
		var reference string
		for _, planned := range []bool{false, true, true} {
			RegisterEnum(alias, []string{"before0", "before1"})
			snapshot := codecEnumRegistry()
			if !pinned {
				snapshot = nil
			}
			rows := []tuple{{}, {}}
			calls := 0
			codecTupleParseEffect = func() { calls++; rows[0].V1 = 1; rows[1].V1 = 1; RegisterEnum(alias, []string{"after0", "after1"}) }
			raw, err := codecTupleParseEncode(rows, "rt.T2[any,"+alias+"]", snapshot, planned)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			got := string(b)
			want := `"v1":"after1"`
			if pinned {
				want = `"v1":"before1"`
			}
			if strings.Count(got, want) != 2 || calls < 2 {
				t.Fatal("tuple parse moved registry reads, value reads or conversion FX")
			}
			if !planned {
				reference = got
			} else if got != reference {
				t.Fatal("planned tuple bytes differ from original loop")
			}
		}
	}
}
func TestCodecTupleParseReuseCapturesConversionBeforeMarshalEffects(t *testing.T) {
	const alias = "codec.tupleparse.phase"
	t.Cleanup(func() { setEnumRegistration(alias, nil, false) })
	for _, planned := range []bool{false, true} {
		RegisterEnum(alias, []string{"before"})
		var events []string
		rows := []T2[JsonValue, int]{
			{V0: JsonValue{raw: codecTupleParseMarshal{effect: func() { events = append(events, "B0"); RegisterEnum(alias, []string{"after"}) }}}},
			{V0: JsonValue{raw: codecTupleParseMarshal{effect: func() { events = append(events, "B1") }}}},
		}
		raw, err := codecTupleParseEncode(rows, "rt.T2[any,"+alias+"]", nil, planned)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 0 {
			t.Fatal("marshal effects ran during conversion")
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(b), `"v1":"before"`) != 2 || strings.Join(events, ",") != "B0,B1" {
			t.Fatal("tuple metadata reuse reordered A/B effects")
		}
	}
}
func TestCodecTupleParseReuseKeepsConversionAndMarshalFailurePositions(t *testing.T) {
	t.Cleanup(func() { codecTupleParseEffect = nil })
	for _, kind := range []string{"function", "nan"} {
		var referenceError string
		var referenceCalls int
		for _, planned := range []bool{false, true} {
			calls := 0
			codecTupleParseEffect = func() { calls++ }
			bad := any(func() {})
			if kind == "nan" {
				bad = math.NaN()
			}
			rows := []T2[any, string]{
				{V0: codecTupleParseVariant{}, V1: "first"},
				{V0: bad, V1: "bad"},
				{V0: codecTupleParseVariant{}, V1: "last"},
			}
			raw, err := codecTupleParseEncode(rows, "rt.T2[any,string]", nil, planned)
			if err == nil {
				_, err = json.Marshal(raw)
			}
			if err == nil {
				t.Fatal("invalid value silently encoded")
			}
			if !planned {
				referenceError = err.Error()
				referenceCalls = calls
			} else if err.Error() != referenceError || calls != referenceCalls {
				t.Fatal("failure or earlier/later conversion effects changed")
			}
			if calls == 0 {
				t.Fatal("earlier conversion FX skipped")
			}
			if kind == "function" && calls != 1 {
				t.Fatal("conversion error did not stop before later FX")
			}
			if kind == "nan" && calls != 2 {
				t.Fatal("marshal error incorrectly stopped Phase A")
			}
		}
	}
}
func TestCodecTupleParseReusePreservesNonTupleEmptyAndMalformedLists(t *testing.T) {
	for _, elem := range []string{"int", "rt.T2[int]", "rt.T2[int,string", "rt.T999999999999999999999[int,string]", ""} {
		for _, rows := range [][]int{nil, {}, {1, 2}} {
			want, err := codecTupleParseReference(rows, elem, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := codecAutoEncodeTypedAt(reflect.ValueOf(rows), "[]"+elem, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(want)
			b, _ := json.Marshal(got)
			if string(a) != string(b) {
				t.Fatal("negative parse or empty list differs")
			}
		}
	}
}
func TestCodecTupleParseReuseAvoidsPerElementArgumentAllocations(t *testing.T) {
	rows := make([]T2[int, string], 512)
	var retained any
	reference := testing.AllocsPerRun(3, func() {
		raw, err := codecTupleParseReference(rows, "rt.T2[int,string]", nil)
		if err != nil {
			panic(err)
		}
		retained = raw
	})
	planned := testing.AllocsPerRun(3, func() {
		raw, err := codecAutoEncodeTypedAt(reflect.ValueOf(rows), "[]rt.T2[int,string]", true, nil)
		if err != nil {
			panic(err)
		}
		retained = raw
	})
	if retained == nil || reference-planned < 500 {
		t.Fatal("tuple syntax still allocates argument slices per element")
	}
}

func TestCodecTupleParseReuseLeavesDirectMutableTupleArgsUntouched(t *testing.T) {
	const left = "codec.tupleparse.left"
	const right = "codec.tupleparse.right"
	RegisterEnum(left, []string{"left"})
	RegisterEnum(right, []string{"right"})
	t.Cleanup(func() {
		codecTupleParseEffect = nil
		setEnumRegistration(left, nil, false)
		setEnumRegistration(right, nil, false)
	})
	type tuple struct {
		V0     codecTupleParseVariant
		hidden int
		V2     int
	}
	args := []string{"any", "", left}
	calls := 0
	codecTupleParseEffect = func() { calls++; args[2] = right }
	raw, err := codecAutoEncodeTupleAt(reflect.ValueOf(tuple{}), args, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(string(b), `"v2":"right"`) {
		t.Fatal("direct mutable args or physical indexes changed")
	}
	calls = 0
	codecTupleParseEffect = func() { calls++ }
	panicked := func() (panicked bool) {
		defer func() { panicked = recover() != nil }()
		_, _ = codecAutoEncodeTupleAt(reflect.ValueOf(tuple{}), []string{"any", ""}, true, nil)
		return
	}()
	if !panicked || calls != 1 {
		t.Fatal("short direct args prevalidated before earlier conversion FX")
	}
}

func TestCodecTupleParseReusePreservesJustNullSentinelSuccess(t *testing.T) {
	t.Cleanup(func() { codecTupleParseEffect = nil })
	var want string
	for _, planned := range []bool{false, true} {
		calls := 0
		codecTupleParseEffect = func() { calls++ }
		rows := []T2[any, string]{
			{V0: codecTupleParseVariant{}, V1: "first"},
			{V0: SkyMaybe[any]{Tag: 0, JustValue: nil}, V1: "just-null"},
			{V0: codecTupleParseVariant{}, V1: "last"},
			{V0: SkyMaybe[any]{Tag: 1}, V1: "nothing"},
		}
		raw, err := codecTupleParseEncode(rows, "rt.T2[any,string]", nil, planned)
		if err != nil {
			t.Fatal(err)
		}
		converted := raw.([]any)
		if _, ok := converted[1].(jsonOrderedObject).vals[0].(codecJustNull); !ok {
			t.Fatal("Just-null conversion lost its sentinel")
		}
		if converted[3].(jsonOrderedObject).vals[0] != nil {
			t.Fatal("Nothing conversion changed raw nil")
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || !strings.Contains(string(b), `"v0":null,"v1":"just-null"`) {
			t.Fatal("Just-null success changed bytes or conversion effects")
		}
		if !planned {
			want = string(b)
		} else if string(b) != want {
			t.Fatal("Just-null bytes differ from original loop")
		}
	}
}
