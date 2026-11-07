package rt

import (
	"reflect"
	"strings"
	"testing"
)

type codecDuplicateFactoryUnion interface {
	SkyVariant
	isCodecDuplicateFactoryUnion()
}

type codecDuplicateFactoryGood struct{ V0 struct{ Value int } }
type codecDuplicateFactoryBad struct {
	V0 struct {
		First  int `sky:"aB,Int"`
		Second int `sky:"a_b,Int"`
	}
}

func (codecDuplicateFactoryGood) SkyVariantTag() int            { return 0 }
func (codecDuplicateFactoryGood) SkyVariantName() string        { return "Only" }
func (codecDuplicateFactoryGood) isCodecDuplicateFactoryUnion() {}
func (codecDuplicateFactoryBad) SkyVariantTag() int             { return 0 }
func (codecDuplicateFactoryBad) SkyVariantName() string         { return "Only" }
func (codecDuplicateFactoryBad) isCodecDuplicateFactoryUnion()  {}

func TestCodecDuplicateFactsKeepLiveFactoryPreparation(t *testing.T) {
	const adt = "rt.codecDuplicateFactoryUnion"
	key := AdtCtorKey{Adt: adt, Ctor: "Only"}
	adtVariantRegistryMu.Lock()
	previous, existed := adtVariantRegistry[key]
	ctors, hadCtors := adtVariantCtors[adt]
	delete(adtVariantRegistry, key)
	delete(adtVariantCtors, adt)
	adtVariantRegistryMu.Unlock()
	t.Cleanup(func() {
		adtVariantRegistryMu.Lock()
		defer adtVariantRegistryMu.Unlock()
		if existed {
			adtVariantRegistry[key] = previous
		} else {
			delete(adtVariantRegistry, key)
		}
		if hadCtors {
			adtVariantCtors[adt] = ctors
		} else {
			delete(adtVariantCtors, adt)
		}
	})
	var trace []string
	bad := false
	panicFactory := false
	RegisterAdtVariant(adt, "Only", func([]JsonRawMessage) any {
		trace = append(trace, "factory")
		if panicFactory {
			panic("live factory failure")
		}
		if bad {
			return codecDuplicateFactoryBad{}
		}
		return codecDuplicateFactoryGood{}
	})
	type row struct{ Value codecDuplicateFactoryUnion }
	witness := row{Value: codecDuplicateFactoryGood{}}
	check := func() (any, []string) {
		trace = nil
		cols := Codec_autoCols(true, witness)
		return cols, append([]string(nil), trace...)
	}
	cold, coldTrace := check()
	warm, warmTrace := check()
	if len(coldTrace) == 0 || !reflect.DeepEqual(cold, warm) || !reflect.DeepEqual(coldTrace, warmTrace) {
		t.Fatal("warming immutable leaf facts changed factory preparation")
	}
	bad = true
	changed, changedTrace := check()
	if len(changedTrace) == 0 || reflect.DeepEqual(changed, cold) {
		t.Fatal("a warmed leaf suppressed a live factory change")
	}
	problem := codecAutoDuplicateKey(reflect.TypeOf(witness), true)
	if !strings.Contains(problem, `"aB" and "a_b"`) {
		t.Fatal("the recursive decoder check lost the changed variant's duplicate")
	}
	bad = false
	restored, restoredTrace := check()
	if !reflect.DeepEqual(restored, cold) || !reflect.DeepEqual(restoredTrace, coldTrace) {
		t.Fatal("preparation retained a previous factory result")
	}
	panicFactory = true
	trace = nil
	_, failure := codecDuplicateOutcome(func() string { Codec_autoCols(true, witness); return "" })
	if failure != "live factory failure" || !reflect.DeepEqual(trace, []string{"factory"}) {
		t.Fatal("warming a leaf changed the first live factory panic")
	}
}

func TestCodecDuplicateFactsKeepCurrentBlankChecks(t *testing.T) {
	type row struct{ Items []any }
	missing := row{}
	present := row{Items: []any{JsonValue{raw: nil}}}
	// The reflect.Type and all direct key facts are identical. Only the live
	// blank evidence changes, so a cached recursive preparation would be wrong.
	codecAutoRecordDuplicateKey(reflect.TypeOf(missing), true)
	for _, value := range []row{missing, present, missing, present} {
		problem := codecAutoBlankProblem(reflect.TypeOf(value), reflect.ValueOf(value), 0)
		if (problem == "") != (len(value.Items) != 0) {
			t.Fatal("a warmed type fact replaced current JSON blank evidence")
		}
		cols := AsList(Codec_autoCols(true, value))
		if len(cols) != 1 {
			t.Fatal("record column disappeared")
		}
		pair, ok := cols[0].(T2[any, any])
		if !ok {
			t.Fatal("column did not contain its typed name/kind pair")
		}
		kind := AsString(pair.V1)
		if strings.Contains(kind, "|problem=") != (len(value.Items) == 0) {
			t.Fatal("codec construction did not observe the current blank")
		}
	}
}
