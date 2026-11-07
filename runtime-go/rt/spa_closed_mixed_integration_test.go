package rt

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// This integration-only file requires prototype.patch AND integration.patch.
// It uses canonical mixed differential spies from spa_mixed_encoding_test.go.
type spaClosedIntegrationKV struct {
	values   map[string]string
	trace    *[]string
	afterSet func()
}

func (k *spaClosedIntegrationKV) Get(key string) (string, bool) { v, ok := k.values[key]; return v, ok }
func (k *spaClosedIntegrationKV) Set(key, value string) {
	*k.trace = append(*k.trace, "write")
	k.values[key] = value
	if k.afterSet != nil {
		k.afterSet()
	}
}
func (k *spaClosedIntegrationKV) Remove(key string) {
	*k.trace = append(*k.trace, "remove")
	delete(k.values, key)
}

func TestSpaClosedMixedNestedEditorGoldenPayloadAndNoDuplicateJoins(t *testing.T) {
	type form struct {
		Brand string `sky:"brand,string"`
	}
	type editor struct {
		Products []string `sky:"products,[]string"`
		Form     form     `sky:"productForm,form"`
	}
	type model struct {
		Editor  SkyMaybe[editor] `sky:"editor,rt.SkyMaybe[editor]"`
		Unknown JsonValue        `sky:"unknown,rt.JsonValue"`
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
	}
	log := []string{}
	e := spaMixedTestEncoder(&log)
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Protected: []string{"session"}}
	m := model{Editor: Just(editor{Products: []string{"<item>"}, Form: form{Brand: "old"}}), Unknown: JsonValue{raw: true}, Session: Nothing[string]()}
	warm := spaEncodeMixed(e, nil, m, options)
	if !warm.Valid {
		t.Fatal("warm integration failed")
	}
	next := m
	next.Editor.JustValue.Form.Brand = "<brand>"
	got := spaEncodeMixed(e, warm.NextCache, next, options)
	want := `{"editor":{"products":["\u003citem\u003e"],"product_form":{"brand":"\u003cbrand\u003e"}},"unknown":true,"session":null}`
	if !got.Valid || got.Payload != want || got.Payload != spaClosedCanonical(next) {
		t.Fatal("nested captured plan changed exact golden bytes")
	}
	if got.Work.Conversions != 2 || got.Work.MaterializedBytes != uint64(len(want)) {
		t.Fatal("typing reencoded siblings or joined Editor before final payload")
	}
	ignored := spaEncodeMixed(e, got.NextCache, next, options)
	if !ignored.Valid || ignored.Payload != want || ignored.Work.MaterializedBytes != 0 {
		t.Fatal("recursive SameJSON payload reuse did not preserve no-join metric")
	}
}

func TestSpaClosedMixedControllerNestedFailureAndMetadataEffectsMatchFull(t *testing.T) {
	const enum = "sky.test.recursive.controller.enum"
	registered, existed := enumVariantsForType(enum)
	defer setEnumRegistration(enum, registered, existed)
	type form struct {
		Brand  string  `sky:"brand,string"`
		Number float64 `sky:"number,float64"`
	}
	type editor struct {
		Products []string `sky:"products,[]string"`
		Form     form     `sky:"productForm,form"`
		Ordinal  int      `sky:"ordinal,sky.test.recursive.controller.enum"`
	}
	type model struct {
		Before  any              `sky:"before,any"`
		Editor  SkyMaybe[editor] `sky:"editor,rt.SkyMaybe[editor]"`
		After   any              `sky:"after,any"`
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
	}
	// Enum mutation can happen in root A, root B, or storage between logical
	// next/previous invocation. Nested NaN must stop B at Editor's position.
	for _, where := range []string{"none", "before-A", "after-A", "before-B", "after-B", "write"} {
		for _, failure := range []string{"", "nested-NaN", "before-conversion", "after-conversion"} {
			run := func(incremental bool) ([]string, map[string]string, spaPersistLife, []bool) {
				RegisterEnum(enum, []string{"old"})
				log := []string{}
				change := func() { RegisterEnum(enum, []string{"new"}) }
				fx := func(side string) any {
					conversionAction, marshalAction := (func())(nil), (func())(nil)
					if where == side+"-A" {
						conversionAction = change
					}
					if where == side+"-B" {
						marshalAction = change
					}
					return spaMixedTestFX{log: &log, action: conversionAction, V0: JsonValue{raw: spaMixedTestMarshal{log: &log, name: "marshal-" + side, action: marshalAction}}}
				}
				m := model{Before: fx("before"), Editor: Just(editor{Products: []string{strings.Repeat("x", 1000)}, Form: form{Brand: "old"}}), After: fx("after"), Session: Just("synthetic")}
				e := spaMixedTestEncoder(&log)
				e.InputType = SpaModelInputType[model]()
				controller := spaMixedPersistence{encoder: e}
				kv := &spaClosedIntegrationKV{values: map[string]string{}, trace: &log}
				if where == "write" {
					kv.afterSet = change
				}
				life := spaPersistLife{}
				clears := []bool{}
				for step := 0; step < 3; step++ {
					next := m
					if step == 1 {
						next.Editor.JustValue.Form.Brand = "changed"
						switch failure {
						case "nested-NaN":
							next.Editor.JustValue.Form.Number = math.NaN()
						case "before-conversion":
							next.Before = func() {}
						case "after-conversion":
							next.After = func() {}
						}
					}
					if step == 2 {
						next.Session = Nothing[string]()
					}
					var cleared bool
					if incremental {
						cleared, _ = controller.step(m, next, e.encode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
					} else {
						cleared = spaPersistStep(m, next, e.encode, func(blob string) { spaPersistWrite(kv, &life, blob, []string{"session"}, spaPersistMaxBytes) }, []string{"session"}, nil)
					}
					clears = append(clears, cleared)
					m = next
				}
				return log, kv.values, life, clears
			}
			a, av, al, ac := run(false)
			b, bv, bl, bc := run(true)
			if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(av, bv) || al != bl || !reflect.DeepEqual(ac, bc) {
				t.Fatalf("recursive controller diverged: metadata=%s failure=%s", where, failure)
			}
		}
	}
}

func TestSpaClosedMixedWideEditorRespectsGlobalCacheBudget(t *testing.T) {
	fields := make([]reflect.StructField, spaClosedEncodingNodeLimit+1)
	for i := range fields {
		fields[i] = reflect.StructField{Name: fmt.Sprintf("F%d", i), Type: reflect.TypeOf(""), Tag: reflect.StructTag(fmt.Sprintf(`sky:"f%d,string"`, i))}
	}
	editorType := reflect.StructOf(fields)
	rootType := reflect.StructOf([]reflect.StructField{
		{Name: "Editor", Type: editorType, Tag: reflect.StructTag(`sky:"editor,record"`)},
		{Name: "Unknown", Type: reflect.TypeOf(JsonValue{}), Tag: reflect.StructTag(`sky:"unknown,rt.JsonValue"`)},
	})
	value := reflect.New(rootType).Elem()
	value.Field(1).Set(reflect.ValueOf(JsonValue{raw: true}))
	log := []string{}
	got := spaEncodeMixed(spaMixedTestEncoder(&log), nil, value.Interface(), spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	if !got.Valid || got.Payload != spaClosedCanonical(value.Interface()) || got.NextCache.closed[0] != nil {
		t.Fatal("wide Editor fallback changed bytes or retained unbounded children")
	}
	if got.Work.MaterializedBytes != uint64(len(got.Payload)) {
		t.Fatal("wide atomic fallback added a hidden composite join")
	}
}

func TestSpaClosedPreparedRouteRetainsOnlyProvenNegativeSkip(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Number  float64          `sky:"number,float64"`
	}
	pure := SpaPreparedModelEncoder{InputType: SpaModelInputType[model](), Prepare: func(_ any) any { return nil }, Finish: func(_, v any) any { return JsonEnc_encode(0, Codec_autoEnc(true, v)) }}
	base := model{Session: Just("synthetic"), Number: 1}
	for _, proof := range []bool{false, true} {
		trace := []string{}
		kv := &spaClosedIntegrationKV{values: map[string]string{}, trace: &trace}
		life := spaPersistLife{}
		p := spaMixedPersistence{encoder: pure, generatedClosed: proof}
		clear, work := p.step(base, base, spaProjectionEncode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
		expected := 2
		if proof {
			expected = 1
		}
		if clear || work.PreparedEncodes != expected || work.FieldModelSteps != 1 {
			t.Fatal("previous preparation skipped without both static and runtime proofs, or supported step uncounted")
		}
	}
	for _, invalidPrev := range []bool{false, true} {
		prev := base
		if invalidPrev {
			prev.Number = math.NaN()
		}
		next := model{Session: Nothing[string](), Number: 0}
		trace := []string{}
		kv := &spaClosedIntegrationKV{values: map[string]string{}, trace: &trace}
		life := spaPersistLife{}
		p := spaMixedPersistence{encoder: pure, generatedClosed: true}
		clear, work := p.step(prev, next, spaProjectionEncode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
		if clear == invalidPrev || work.PreparedEncodes != 2 {
			t.Fatal("potential clear failed to independently validate previous model")
		}
	}
}

func TestSpaClosedHIRFlagCannotSkipMixedPreviousEffects(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		FX      any              `sky:"fx"`
	}
	trace := []string{}
	value := model{Session: Just("synthetic"), FX: spaMixedTestFX{log: &trace, V0: "synthetic"}}
	e := spaMixedTestEncoder(&trace)
	e.InputType = SpaModelInputType[model]()
	p := spaMixedPersistence{encoder: e, generatedClosed: true}
	kv := &spaClosedIntegrationKV{values: map[string]string{}, trace: &trace}
	life := spaPersistLife{}
	clear, work := p.step(value, value, spaProjectionEncode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
	conversions := 0
	for _, s := range trace {
		if s == "convert" {
			conversions++
		}
	}
	if clear || conversions != 2 || work.PreparedEncodes != 2 {
		t.Fatal("literal HIR provenance suppressed unknown previous effects")
	}
}
