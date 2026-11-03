package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type codecReviewColor int

type codecReviewTree interface {
	SkyVariant
	isCodecReviewTree()
}

type codecReviewTreeNode struct{ V0 SkyMaybe[codecReviewTree] }

func (codecReviewTreeNode) SkyVariantTag() int     { return 0 }
func (codecReviewTreeNode) SkyVariantName() string { return "Node" }
func (codecReviewTreeNode) isCodecReviewTree()     {}

func TestCodecAutoDepthWalksKeepRecursiveVariants(t *testing.T) {
	RegisterAdtVariant("rt.codecReviewTree", "Node", func([]JsonRawMessage) any { return codecReviewTreeNode{} })
	if problem := codecAutoTypeProblem(reflect.TypeOf(SkyMaybe[codecReviewTree]{}), "rt.SkyMaybe[codecReviewTree]"); problem != "" {
		t.Fatalf("valid recursive variants were refused: %s", problem)
	}
}

type codecReviewDictTupleRow struct {
	Pairs SkyDict[T2[codecReviewColor, int]] `sky:"pairs,rt.SkyDict[rt.T2[CodecReview_Color, int]]"`
}

type codecReviewNestedDictTupleRow struct {
	Pairs SkyDict[SkyDict[T2[codecReviewColor, int]]] `sky:"pairs,rt.SkyDict[rt.SkyDict[rt.T2[CodecReview_Color, int]]]"`
}

type codecReviewMapTupleRow struct {
	Pairs map[string]T2[codecReviewColor, int] `sky:"pairs,map[string]rt.T2[CodecReview_Color, int]"`
}

func TestCodecAutoDictTupleEnumRoundTrip(t *testing.T) {
	RegisterEnum("CodecReview_Color", []string{"Red", "Blue"})
	d := (SkyDict[T2[codecReviewColor, int]]{}).Insert("x", T2[codecReviewColor, int]{V0: 0, V1: 1})
	values := []any{
		codecReviewDictTupleRow{Pairs: d},
		codecReviewNestedDictTupleRow{Pairs: (SkyDict[SkyDict[T2[codecReviewColor, int]]]{}).Insert("outer", d)},
		codecReviewMapTupleRow{Pairs: map[string]T2[codecReviewColor, int]{"x": {V0: 0, V1: 1}}},
	}
	for _, value := range values {
		rv := reflect.ValueOf(value)
		encoded, err := codecAutoEncodeTyped(rv, "", true)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codecAutoDecodeValW(rv.Type(), codecJSONRoundTrip(t, encoded), true, reflect.Zero(rv.Type()))
		if err != nil {
			t.Errorf("%s: written Dict tuple cannot be read: %v", rv.Type(), err)
			continue
		}
		if !deepEq(value, decoded.Interface()) {
			t.Errorf("%s: Dict tuple changed on round trip", rv.Type())
		}
	}
}

func codecReviewNest(t reflect.Type, layers int) reflect.Type {
	for i := 0; i < layers; i++ {
		t = reflect.StructOf([]reflect.StructField{{Name: "Child", Type: t, Tag: `sky:"child,CodecReviewLayer"`}})
	}
	return t
}

func TestCodecAutoDeepChecksDoNotSilentlyStop(t *testing.T) {
	currency := reflect.StructOf([]reflect.StructField{{Name: "Cur", Type: reflect.TypeOf(SkyADT{}), Tag: `sky:"cur,Std_Money_Currency"`}})
	jsonSlot := reflect.TypeOf((*any)(nil)).Elem()
	unnamedStd := reflect.TypeOf(struct{ Value SkyADT }{})
	for _, depth := range []int{0, 1, 63, 64, 65, 80} {
		if !codecAutoTypePartial(codecReviewNest(currency, depth), "", 0) {
			t.Errorf("depth %d: Currency partiality disappeared", depth)
		}
		if p := codecAutoNestedDuplicateKey(codecReviewNest(reflect.TypeOf(acDup{}), depth), true, 0, map[reflect.Type]bool{}); p == "" {
			t.Errorf("depth %d: duplicate-key problem disappeared", depth)
		}
		if p := codecAutoBlankProblem(codecReviewNest(jsonSlot, depth), reflect.Value{}, 0); p == "" {
			t.Errorf("depth %d: missing JSON witness problem disappeared", depth)
		}
		if p := codecAutoTypeProblem(codecReviewNest(unnamedStd, depth), ""); p == "" {
			t.Errorf("depth %d: unnamed standard-library type problem disappeared", depth)
		}
		valid := codecReviewNest(reflect.TypeOf(0), depth)
		if p := codecAutoTypeProblem(valid, ""); p != "" {
			t.Errorf("depth %d: valid finite record refused: %s", depth, p)
		}
		if codecAutoTypePartial(valid, "", 0) {
			t.Errorf("depth %d: total Int record became partial", depth)
		}
		for _, c := range AsList(Codec_autoCols(true, reflect.Zero(valid).Interface())) {
			if pair, ok := c.(T2[any, any]); ok && strings.Contains(AsString(pair.V1), "|problem=") {
				t.Errorf("depth %d: valid finite codec has a builder problem", depth)
			}
		}
	}
}

type codecReviewRecursiveRecord struct{ Next *codecReviewRecursiveRecord }

func TestCodecAutoRecursiveGoBlankTerminates(t *testing.T) {
	ty := reflect.TypeOf(codecReviewRecursiveRecord{})
	if p := codecAutoBlankProblem(ty, reflect.Value{}, 0); !strings.Contains(p, "recursive Go record") {
		t.Errorf("missing recursive-blank problem: %q", p)
	}
	// The type walkers terminate independently of the blank check.
	codecAutoTypePartial(ty, "", 0)
	codecAutoTypeProblem(ty, "")
	codecAutoNestedDuplicateKey(ty, true, 0, map[reflect.Type]bool{})
}

func TestCodecAutoNativeMapSortsEncodedKeys(t *testing.T) {
	RegisterEnum("CodecReview_MapKey", []string{"Zebra", "Alpha"})
	encoded, err := codecAutoEncodeTyped(reflect.ValueOf(map[codecReviewColor]int{0: 1, 1: 2}), "map[CodecReview_MapKey]int", true)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"Alpha":2,"Zebra":1}` {
		t.Errorf("map wire order: %s", wire)
	}
}
