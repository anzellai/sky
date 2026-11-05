package rt

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func spaClosedCanonical(model any) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	return AsString(JsonEnc_encode(0, Codec_autoEnc(true, model)))
}

func spaClosedTestEncode(model, old any, prior *spaClosedEncodingNode, previousEpoch *codecEnumRegistrySnapshot, options spaClosedEncodingOptions, budget *spaClosedEncodingBudget) spaClosedEncodingResult {
	return spaEncodeClosed(reflect.ValueOf(model), reflect.ValueOf(old), prior, "", previousEpoch, codecEnumRegistry(), options, budget)
}

func TestSpaClosedRecursiveNestedEditorTypingReusesSiblingProducts(t *testing.T) {
	type product struct {
		ID   int    `sky:"id,int"`
		Name string `sky:"name,string"`
	}
	type form struct {
		Brand string `sky:"brand,string"`
		Note  string `sky:"note,string"`
	}
	type editor struct {
		Products   []product `sky:"products,[]product"`
		Treatments []string  `sky:"treatments,[]string"`
		Form       form      `sky:"productForm,form"`
		Title      string    `sky:"title,string"`
	}
	visits := -1
	for _, n := range []int{1000, 10000, 100000} {
		old := Just(editor{Products: make([]product, n), Treatments: []string{"synthetic"}, Title: "fixed"})
		for i := range old.JustValue.Products {
			old.JustValue.Products[i] = product{ID: i, Name: strings.Repeat("x", 60)}
		}
		epoch := codecEnumRegistry()
		options := spaClosedEncodingOptions{NeedRaw: true, AllowOversizeWithoutRaw: true}
		warm := spaClosedTestEncode(old, nil, nil, nil, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
		if !warm.Supported || warm.ConversionErr != nil || warm.MarshalErr != nil || warm.Cache == nil {
			t.Fatal("cold editor summary failed")
		}
		next := old
		next.JustValue.Form.Brand = "changed"
		got := spaClosedTestEncode(next, old, warm.Cache, epoch, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
		if !got.Supported || got.ConversionErr != nil || got.MarshalErr != nil || got.Work.Conversions != 1 || got.Work.Marshals != 1 || got.Work.EncodedBytes > 256 {
			t.Fatal("typing reencoded unchanged products or treatments")
		}
		if visits == -1 {
			visits = got.Work.ComparedNodes
		}
		if got.Work.ComparedNodes != visits || got.Work.RetainedBytes > spaPersistMaxBytes || got.Work.Nodes > spaClosedEncodingNodeLimit {
			t.Fatal("unrelated data grew witness/cache work")
		}
		if n < 100000 {
			payload, ok := got.materialize(spaPersistMaxBytes)
			if !ok || payload != spaClosedCanonical(next) {
				t.Fatal("nested editor JSON changed")
			}
		} else {
			if got.Size <= spaPersistMaxBytes {
				t.Fatal("fixture is not oversized")
			}
			if _, ok := got.materialize(spaPersistMaxBytes); ok {
				t.Fatal("oversized editor was materialized")
			}
		}
	}
}

func TestSpaClosedRecursiveMaybeNilSentinelAndInactiveOwnership(t *testing.T) {
	type record struct {
		Nested SkyMaybe[SkyMaybe[int]] `sky:"nested,rt.SkyMaybe[rt.SkyMaybe[int]]"`
	}
	for _, nested := range []SkyMaybe[SkyMaybe[int]]{Nothing[SkyMaybe[int]](), Just(Nothing[int]()), Just(Just(3))} {
		m := record{Nested: nested}
		got := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
		payload, ok := got.materialize(spaPersistMaxBytes)
		if !ok || payload != spaClosedCanonical(m) {
			t.Fatal("Maybe wire behavior changed")
		}
		kind := got.Cache.children[0].rawKind
		if nested.Tag != 0 && kind != spaClosedRawNil {
			t.Fatal("Nothing lost raw nil distinction")
		}
		if nested.Tag == 0 && nested.JustValue.Tag != 0 && kind != spaClosedRawJustNull {
			t.Fatal("Just Nothing lost raw sentinel distinction")
		}
	}
	old := Just(strings.Repeat("x", 1000000))
	epoch := codecEnumRegistry()
	warm := spaClosedTestEncode(old, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	next := Nothing[string]()
	got := spaClosedTestEncode(next, old, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if len(got.Cache.children) != 0 || got.Work.RetainedBytes != 0 {
		t.Fatal("Nothing retained old active child/cache bytes")
	}
}

func TestSpaClosedRecursiveDeclaredEnumAndTupleDispatch(t *testing.T) {
	const enum = "sky.test.recursive.enum"
	oldNames, existed := enumVariantsForType(enum)
	defer setEnumRegistration(enum, oldNames, existed)
	RegisterEnum(enum, []string{"old"})
	type record struct {
		Ordinal int `sky:"ordinal,sky.test.recursive.enum"`
	}
	m := record{}
	epoch := codecEnumRegistry()
	warm := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	// A later unknown effect changes metadata BEFORE byte materialization.
	RegisterEnum(enum, []string{"new"})
	payload, ok := warm.materialize(spaPersistMaxBytes)
	if !ok || !strings.Contains(payload, `"old"`) {
		t.Fatal("byte plan reread metadata after Phase A")
	}
	next := spaClosedTestEncode(m, m, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	newPayload, ok := next.materialize(spaPersistMaxBytes)
	if !ok || !strings.Contains(newPayload, `"new"`) || next.Work.Reuses != 0 {
		t.Fatal("old epoch certificate reused")
	}
	// A declared tuple on an otherwise closed tagged record overrides its
	// children's own declared tags. It MUST use canonical tuple dispatch.
	type tupleShape struct {
		V0 int    `sky:"v0,int"`
		V1 string `sky:"v1,string"`
	}
	v := tupleShape{V1: "synthetic"}
	declared := "rt.T2[sky.test.recursive.enum,string]"
	got := spaEncodeClosed(reflect.ValueOf(v), reflect.Value{}, nil, declared, nil, codecEnumRegistry(), spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	raw, err := codecAutoEncodeTyped(reflect.ValueOf(v), declared, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := got.materialize(spaPersistMaxBytes)
	if !ok || actual != string(want) || got.Cache.kind != spaClosedAtomic {
		t.Fatal("declared tuple dispatch changed")
	}
}

func TestSpaClosedRecursiveMissingSmallRawUsesPinnedReconstruction(t *testing.T) {
	type record struct {
		First  string `sky:"first,string"`
		Second string `sky:"second,string"`
	}
	m := record{First: strings.Repeat("a", 1000000), Second: strings.Repeat("b", 1000000)}
	epoch := codecEnumRegistry()
	options := spaClosedEncodingOptions{NeedRaw: true, AllowOversizeWithoutRaw: true}
	warm := spaClosedTestEncode(m, nil, nil, nil, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if warm.Cache.children[1].raw != "" {
		t.Fatal("fixture retained every raw leaf")
	}
	next := m
	next.First = "small"
	deferredBudget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	got := spaClosedTestEncode(next, m, warm.Cache, epoch, options, deferredBudget)
	if got.Work.Conversions != 1 || deferredBudget.Deferred.Conversions != 0 {
		t.Fatal("missing possible-small leaf was reconstructed eagerly")
	}
	payload, ok := got.materialize(spaPersistMaxBytes)
	if !ok || payload != spaClosedCanonical(next) {
		t.Fatal("now-small nested JSON changed")
	}
	if deferredBudget.Deferred.Conversions != 1 || deferredBudget.Deferred.Marshals != 1 || warm.Cache.children[1].raw != "" || got.Cache.children[1].raw == "" {
		t.Fatal("deferred reconstruction did not fill exactly the reserved NEW node")
	}
}

type spaClosedPhaseFX struct {
	V0  any
	log *[]string
}

func (v spaClosedPhaseFX) SkyVariantTag() int { *v.log = append(*v.log, "tag"); return 0 }
func (v spaClosedPhaseFX) SkyVariantName() string {
	*v.log = append(*v.log, "convert")
	return "Fixture"
}

type spaClosedPhaseMarshal struct{ log *[]string }

func (v spaClosedPhaseMarshal) MarshalJSON() ([]byte, error) {
	*v.log = append(*v.log, "marshal")
	return []byte("true"), nil
}

func TestSpaClosedRecursiveNestedMarshalFailureStillRunsLaterRootConversions(t *testing.T) {
	type inner struct {
		First float64 `sky:"first,float64"`
		Last  string  `sky:"last,string"`
	}
	type root struct {
		Editor SkyMaybe[inner] `sky:"editor,rt.SkyMaybe[inner]"`
		FX     any             `sky:"fx,any"`
	}
	refLog, gotLog := []string{}, []string{}
	create := func(log *[]string) root {
		return root{Editor: Just(inner{First: math.NaN(), Last: "converted"}), FX: spaClosedPhaseFX{log: log, V0: JsonValue{raw: spaClosedPhaseMarshal{log: log}}}}
	}
	if spaClosedCanonical(create(&refLog)) != "" {
		t.Fatal("reference unexpectedly encoded NaN")
	}
	m := create(&gotLog)
	result := spaClosedTestEncode(m.Editor, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if result.ConversionErr != nil || result.MarshalErr == nil || result.Work.Conversions != 2 {
		t.Fatal("nested marshal error terminated recursive conversion")
	}
	// Integration seam: remaining unknown root conversions happen in A.
	_, err := codecAutoEncodeTyped(reflect.ValueOf(m.FX), "any", true)
	if err != nil {
		t.Fatal(err)
	}
	// B observes the Editor error before unknown FX's MarshalJSON.
	if !reflect.DeepEqual(refLog, gotLog) || !reflect.DeepEqual(gotLog, []string{"tag", "convert"}) {
		t.Fatal("nested failure changed root effect order")
	}
	if result.Cache != nil {
		t.Fatal("failed subtree produced a successful cache")
	}
}

func TestSpaClosedRecursiveGlobalBudgetsAndConservativeFallback(t *testing.T) {
	type inner struct {
		Text string `sky:"text,string"`
	}
	type outer struct {
		A inner `sky:"a,inner"`
		B inner `sky:"b,inner"`
	}
	m := outer{A: inner{Text: "a"}, B: inner{Text: "b"}}
	for _, limit := range []int{0, 1, 2, 8} {
		budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
		budget.NodesLeft = limit
		budget.DepthLimit = 0
		got := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, budget)
		payload, ok := got.materialize(spaPersistMaxBytes)
		if !ok || payload != spaClosedCanonical(m) || got.Work.Nodes > limit {
			t.Fatal("node/depth fallback changed canonical result or exceeded budget")
		}
	}
	budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	for i := 0; i < 3; i++ {
		got := spaClosedTestEncode(strings.Repeat("x", 1000000), nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, budget)
		if got.ConversionErr != nil || got.MarshalErr != nil {
			t.Fatal("shared raw-budget fixture failed")
		}
	}
	if budget.BytesLeft < 0 || budget.NodesLeft < 0 {
		t.Fatal("global ledgers were exceeded")
	}
	// Unknown domain is rejected WITHOUT method calls; enclosing engine owns
	// its ordered conversion and marshaling path.
	log := []string{}
	unknown := spaClosedPhaseFX{log: &log, V0: "synthetic"}
	got := spaClosedTestEncode(unknown, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if got.Supported || len(log) != 0 {
		t.Fatal("unknown foreign method was invoked early")
	}
}

func TestSpaClosedRecursiveLaterUnknownConversionWinsBeforeNestedMarshalFailure(t *testing.T) {
	type inner struct {
		Number float64 `sky:"number,float64"`
	}
	type root struct {
		Editor inner `sky:"editor,inner"`
		FX     any   `sky:"fx,any"`
		Bad    any   `sky:"bad,any"`
	}
	refLog, gotLog := []string{}, []string{}
	create := func(log *[]string) root {
		return root{Editor: inner{Number: math.NaN()}, FX: spaClosedPhaseFX{log: log, V0: JsonValue{raw: spaClosedPhaseMarshal{log: log}}}, Bad: func() {}}
	}
	if spaClosedCanonical(create(&refLog)) != "" {
		t.Fatal("reference unexpectedly converted function")
	}
	m := create(&gotLog)
	result := spaClosedTestEncode(m.Editor, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if result.ConversionErr != nil || result.MarshalErr == nil {
		t.Fatal("NaN failure was observed in conversion phase")
	}
	if _, err := codecAutoEncodeTyped(reflect.ValueOf(m.FX), "any", true); err != nil {
		t.Fatal(err)
	}
	if _, err := codecAutoEncodeTyped(reflect.ValueOf(m.Bad), "any", true); err == nil {
		t.Fatal("fixture lost true conversion failure")
	}
	// Do not observe result.MarshalErr or run the FX marshaler after Phase A failed.
	if !reflect.DeepEqual(refLog, gotLog) || !reflect.DeepEqual(gotLog, []string{"tag", "convert"}) {
		t.Fatal("deferred nested failure suppressed later root conversion")
	}
}

func TestSpaClosedRecursiveProtectedAndExcludedMissingRawPolicies(t *testing.T) {
	type record struct {
		Data []string `sky:"data,[]string"`
	}
	m := record{Data: []string{strings.Repeat("x", spaPersistMaxBytes+1)}}
	epoch := codecEnumRegistry()
	warm := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true, AllowOversizeWithoutRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if warm.Cache.children[0].raw != "" {
		t.Fatal("oversized leaf raw retained")
	}
	protectedBudget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	protected := spaClosedTestEncode(m, m, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: true}, protectedBudget)
	if protected.Work.Conversions != 0 || protected.Plan.children[0].lazy == nil {
		t.Fatal("protected identity did not retain a pinned lazy reconstruction")
	}
	encoded, ok := protected.materialize(protected.Size)
	if !ok || encoded != spaClosedCanonical(m) || protectedBudget.Deferred.Conversions != 1 || protectedBudget.Deferred.Marshals != 1 || protected.Cache.children[0].raw != "" || warm.Cache.children[0].raw != "" {
		t.Fatal("protected pinned reconstruction changed bytes/order or exceeded raw retention")
	}
	excluded := spaClosedTestEncode(m, m, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: false, DiscardRawRetention: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if excluded.Work.Conversions != 0 || excluded.Work.RetainedBytes != 0 || !excluded.Plan.children[0].missing {
		t.Fatal("excluded/previous-only work rebuilt unnecessary raw")
	}
}

func TestSpaClosedRecursiveSpecialStructDispatchNeverBecomesOrdinaryRecord(t *testing.T) {
	log := []string{}
	values := []any{SkyADT{}, SkyDict[int]{}, SkySet{}, JsonValue{}, struct{ private int }{}, spaClosedPhaseFX{log: &log, V0: "synthetic"}}
	for _, value := range values {
		kind, _ := spaClosedDispatch(reflect.ValueOf(value), "")
		if kind != spaClosedUnsupported || spaClosedOwnDomain(reflect.TypeOf(value), map[reflect.Type]uint8{}, 0) {
			t.Fatal("canonical special struct gained recursive record eligibility")
		}
	}
	if len(log) != 0 {
		t.Fatal("special dispatch classification invoked foreign methods")
	}
}

func spaClosedRetainedUnits(node *spaClosedEncodingNode) int {
	if node == nil {
		return 0
	}
	units := 1 + cap(node.children)
	for _, child := range node.children {
		units += spaClosedRetainedUnits(child)
	}
	return units
}

func TestSpaClosedRecursiveWideRecordsBoundActualRetainedSlots(t *testing.T) {
	type leaf struct {
		Text string `sky:"text,string"`
	}
	for _, nested := range []bool{false, true} {
		fields := make([]reflect.StructField, 100)
		fieldType := reflect.TypeOf("")
		if nested {
			fieldType = reflect.TypeOf(leaf{})
		}
		for i := range fields {
			fields[i] = reflect.StructField{Name: fmt.Sprintf("F%d", i), Type: fieldType, Tag: reflect.StructTag(fmt.Sprintf(`sky:"f%d,string"`, i))}
		}
		typ := reflect.StructOf(fields)
		value := reflect.New(typ).Elem().Interface()
		for _, limit := range []int{20, 150} {
			budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
			budget.NodesLeft = limit
			got := spaClosedTestEncode(value, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, budget)
			payload, ok := got.materialize(spaPersistMaxBytes)
			if !ok || payload != spaClosedCanonical(value) {
				t.Fatal("wide-record budget fallback changed bytes")
			}
			if units := spaClosedRetainedUnits(got.Cache); units > limit {
				t.Fatalf("retained headers/slots=%d exceeded budget=%d", units, limit)
			}
			if limit == 20 && (got.Cache != nil || got.Work.BudgetFallbacks == 0) {
				t.Fatal("wide parent did not choose atomic fallback before retaining children")
			}
		}
	}
}
