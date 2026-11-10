package rt

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

const spaLazyTestEnum = "sky.test.closed.lazy.enum"

type spaLazyRow struct {
	Ordinal int    `sky:"ordinal,sky.test.closed.lazy.enum"`
	Text    string `sky:"text,string"`
}

func spaLazyManyLists() (reflect.Type, reflect.Value) {
	fields := make([]reflect.StructField, 0, 102)
	for i := 0; i < 100; i++ {
		fields = append(fields, reflect.StructField{Name: fmt.Sprintf("List%d", i), Type: reflect.TypeOf([]spaLazyRow{}), Tag: reflect.StructTag(fmt.Sprintf(`sky:"list%d,[]row"`, i))})
	}
	fields = append(fields, reflect.StructField{Name: "Tick", Type: reflect.TypeOf(""), Tag: reflect.StructTag(`sky:"tick,string"`)}, reflect.StructField{Name: "FX", Type: reflect.TypeOf((*any)(nil)).Elem(), Tag: reflect.StructTag(`sky:"fx,any"`)})
	typ := reflect.StructOf(fields)
	value := reflect.New(typ).Elem()
	data := make([]spaLazyRow, 500)
	for i := range data {
		data[i].Text = strings.Repeat("x", 80)
	}
	for i := 0; i < 100; i++ {
		value.Field(i).Set(reflect.ValueOf(data))
	}
	return typ, value
}

func TestSpaClosedLazyManySubcapListsAvoidAllWarmReconstructionAndShrinkExactly(t *testing.T) {
	oldNames, existed := enumVariantsForType(spaLazyTestEnum)
	defer setEnumRegistration(spaLazyTestEnum, oldNames, existed)
	for _, mutation := range []string{"none", "A", "B"} {
		run := func(incremental bool) (string, []string, spaMixedEncoded, *spaMixedModelCache) {
			RegisterEnum(spaLazyTestEnum, []string{"old"})
			_, value := spaLazyManyLists()
			log := []string{}
			makeFX := func(stage string) any {
				var a, b func()
				change := func() { RegisterEnum(spaLazyTestEnum, []string{"new-longer"}) }
				if stage == "A" {
					a = change
				}
				if stage == "B" {
					b = change
				}
				return spaMixedTestFX{log: &log, action: a, V0: JsonValue{raw: spaMixedTestMarshal{log: &log, name: "marshal", action: b}}}
			}
			value.Field(101).Set(reflect.ValueOf(makeFX("none")))
			e := spaMixedTestEncoder(&log)
			options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Profile: true}
			warm := spaEncodeMixed(e, nil, value.Interface(), options)
			if !warm.Valid || warm.Payload != "" || warm.NextCache.closed[99].raw != "" {
				t.Fatal("many-list fixture did not overflow global raw and payload budgets")
			}
			next := reflect.New(value.Type()).Elem()
			next.Set(value)
			next.Field(100).SetString("typed")
			log = nil
			step := spaEncodeMixed(e, warm.NextCache, next.Interface(), options)
			if !step.Valid || step.Payload != "" || step.Work.Conversions != 2 || step.Work.DeferredConversions != 0 || step.Work.EncodedBytes > 512 {
				t.Fatal("aggregate-overcap typing reconstructed missing subcap lists")
			}
			if !reflect.DeepEqual(log, []string{"prepare", "tag", "convert", "marshal"}) {
				t.Fatal("warm missing-raw deferral skipped unknown effects")
			}
			prior := step.NextCache
			shrink := reflect.New(value.Type()).Elem()
			shrink.Set(next)
			for i := 0; i < 99; i++ {
				shrink.Field(i).Set(reflect.Zero(shrink.Field(i).Type()))
			}
			shrink.Field(101).Set(reflect.ValueOf(makeFX(mutation)))
			log = nil
			if !incremental {
				return e.encode(shrink.Interface()), log, spaMixedEncoded{}, nil
			}
			got := spaEncodeMixed(e, prior, shrink.Interface(), options)
			if !got.Valid || got.Payload == "" || got.Work.DeferredConversions != 1 || got.Work.DeferredMarshals != 1 || got.Work.DeferredEncodedBytes == 0 {
				t.Fatal("shrink did not resolve precisely the one required missing leaf")
			}
			if prior.closed[99].raw != "" {
				t.Fatal("deferred reconstruction mutated a prior node")
			}
			if mutation == "none" {
				if got.NextCache.closed[99] == nil || got.NextCache.closed[99].raw == "" {
					t.Fatal("reserved new node did not receive reconstructed bytes")
				}
				ignored := spaEncodeMixed(e, got.NextCache, shrink.Interface(), options)
				if !ignored.Valid || ignored.Work.DeferredConversions != 0 || ignored.Work.MaterializedBytes != 0 {
					t.Fatal("resolved new node failed subsequent raw/payload reuse")
				}
				if len(log) != 8 || !reflect.DeepEqual(log[4:], []string{"prepare", "tag", "convert", "marshal"}) {
					t.Fatal("resolved no-op skipped unknown effects")
				}
			} else if got.NextCache.closed[99] != nil {
				t.Fatal("older pinned metadata was attached to a newer epoch cache")
			}
			return got.Payload, log, got, got.NextCache
		}
		want, wlog, _, _ := run(false)
		got, glog, result, _ := run(true)
		// run(true)'s optional second no-op adds another trace only in none case.
		if mutation == "none" {
			glog = glog[:len(wlog)]
		}
		if got != want || !reflect.DeepEqual(glog, wlog) || !strings.Contains(got, `"ordinal":"old"`) {
			t.Fatal("pinned shrink changed canonical bytes or unknown A/B effect order")
		}
		if result.Work.BuildNanos < result.Work.DeferredNanos || result.Work.DeferredNanos < 0 {
			t.Fatal("deferred timing was not a Build subrange")
		}
	}
}

func TestSpaClosedLazyPinBudgetOverflowUsesImmediateCanonicalA(t *testing.T) {
	previous, existed := enumVariantsForType(spaLazyTestEnum)
	defer setEnumRegistration(spaLazyTestEnum, previous, existed)
	RegisterEnum(spaLazyTestEnum, []string{"old"})
	epoch := codecEnumRegistry()
	m := []spaLazyRow{{Text: "synthetic"}}
	coldBudget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	coldBudget.BytesLeft = 0
	warm := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, coldBudget)
	if warm.Cache == nil || warm.Cache.raw != "" {
		t.Fatal("missing raw fixture failed")
	}
	budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	for i := 0; i < spaClosedMaxPinnedEpochs; i++ {
		if !budget.Pins.claim(&codecEnumRegistrySnapshot{names: map[string][]string{fmt.Sprint(i): {"synthetic"}}}) {
			t.Fatal("pin fixture could not fill budget")
		}
	}
	got := spaClosedTestEncode(m, m, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: true}, budget)
	if got.Work.Conversions != 1 || budget.Deferred.Conversions != 0 || budget.Pins.count != spaClosedMaxPinnedEpochs {
		t.Fatal("pin overflow did not fall back to A")
	}
	RegisterEnum(spaLazyTestEnum, []string{"new-longer"})
	payload, ok := got.materialize(spaPersistMaxBytes)
	if !ok || !strings.Contains(payload, `"old"`) || budget.Deferred.Conversions != 0 {
		t.Fatal("overflow fallback reread metadata after FX")
	}
}

func TestSpaClosedLazyChangedNaNIsNeverDeferred(t *testing.T) {
	m := []float64{1}
	epoch := codecEnumRegistry()
	budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	budget.BytesLeft = 0
	warm := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, budget)
	next := []float64{math.NaN()}
	current := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	got := spaClosedTestEncode(next, m, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: true}, current)
	if got.MarshalErr == nil || got.ConversionErr != nil || got.Work.Conversions != 1 || current.Pins.count != 0 || current.Deferred.Conversions != 0 {
		t.Fatal("changed invalid leaf borrowed successful prior validity")
	}
}

func TestSpaClosedLazyResolutionClearsInvocationOwnersAndCacheCannotReachPlans(t *testing.T) {
	type row struct {
		Value string `sky:"value,string"`
	}
	m := []row{{Value: "synthetic"}}
	epoch := codecEnumRegistry()
	budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	budget.BytesLeft = 0
	warm := spaClosedTestEncode(m, nil, nil, nil, spaClosedEncodingOptions{NeedRaw: true}, budget)
	current := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	got := spaClosedTestEncode(m, m, warm.Cache, epoch, spaClosedEncodingOptions{NeedRaw: true}, current)
	lazy := got.Plan.lazy
	if lazy == nil || !lazy.value.IsValid() {
		t.Fatal("fixture did not defer")
	}
	actual, ok := got.materialize(spaPersistMaxBytes)
	if !ok || actual != spaClosedCanonical(m) || lazy.value.IsValid() || lazy.snapshot != nil || lazy.target != nil || lazy.budget != nil {
		t.Fatal("resolved plan retained invocation owners or changed bytes")
	}
	if warm.Cache.raw != "" || got.Cache.raw == "" {
		t.Fatal("raw fill ownership is not new-node-only")
	}
	if again, ok := got.materialize(spaPersistMaxBytes); !ok || again != actual || current.Deferred.Conversions != 1 || current.Deferred.Marshals != 1 {
		t.Fatal("repeated byte-plan append repeated canonical reconstruction")
	}
	// Inspect actual cache-node type reachability. Only immutable type metadata,
	// scalar certificates and recursive node pointers/slices are permitted.
	nodeType := reflect.TypeOf(spaClosedEncodingNode{})
	dictType := reflect.TypeOf(spaClosedDictSummary{})
	typeInterface := reflect.TypeOf((*reflect.Type)(nil)).Elem()
	seen := map[reflect.Type]bool{}
	var inspect func(reflect.Type) bool
	inspect = func(typ reflect.Type) bool {
		if typ == typeInterface {
			return true
		}
		if seen[typ] {
			return true
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Uint8, reflect.Uint32, reflect.Uint64:
			return true
		case reflect.Pointer:
			return (typ.Elem() == nodeType || typ.Elem() == dictType) && inspect(typ.Elem())
		case reflect.Slice:
			return (typ.Elem() == reflect.PointerTo(nodeType) || typ.Elem() == reflect.PointerTo(dictType)) && inspect(typ.Elem())
		case reflect.Struct:
			if typ != nodeType && typ != dictType {
				return false
			}
			for i := 0; i < typ.NumField(); i++ {
				if !inspect(typ.Field(i).Type) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	if !inspect(nodeType) {
		t.Fatal("cache-node graph can retain model/plan/value/snapshot state")
	}
	// The new exact certificate type is permitted; arbitrary pointer/interface
	// escape hatches remain forbidden. Reset traversal state for every root.
	for _, forbidden := range []reflect.Type{
		reflect.TypeOf((*spaClosedBytePlan)(nil)),
		reflect.TypeOf((*spaClosedLazyLeaf)(nil)),
		reflect.TypeOf((*codecEnumRegistrySnapshot)(nil)),
		reflect.TypeOf((*reflect.Value)(nil)),
		reflect.TypeOf((*dslot[string])(nil)),
		reflect.TypeOf((*SkyDict[string])(nil)),
		reflect.TypeOf((*any)(nil)).Elem(),
		reflect.TypeOf([]any{}),
		reflect.TypeOf([]*dslot[string]{}),
	} {
		seen = map[reflect.Type]bool{}
		if inspect(forbidden) {
			t.Fatal("cache reachability guard admitted arbitrary model/plan/value/snapshot state")
		}
	}
}

func TestSpaClosedLazyControllerMetadataAndPublicationMatchOriginalFull(t *testing.T) {
	previous, existed := enumVariantsForType(spaLazyTestEnum)
	defer setEnumRegistration(spaLazyTestEnum, previous, existed)
	type model struct {
		A       string           `sky:"a,string"`
		B       []spaLazyRow     `sky:"b,[]row"`
		FX      any              `sky:"fx,any"`
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
	}
	for _, where := range []string{"none", "A", "B", "publication"} {
		run := func(incremental bool) ([]string, map[string]string, spaPersistLife, []bool) {
			RegisterEnum(spaLazyTestEnum, []string{"old"})
			log := []string{}
			data := make([]spaLazyRow, 500)
			for i := range data {
				data[i].Text = strings.Repeat("x", 80)
			}
			makeFX := func(stage string) any {
				var a, b func()
				change := func() { RegisterEnum(spaLazyTestEnum, []string{"new-longer"}) }
				if stage == "A" {
					a = change
				}
				if stage == "B" {
					b = change
				}
				return spaMixedTestFX{log: &log, action: a, V0: JsonValue{raw: spaMixedTestMarshal{log: &log, name: "marshal", action: b}}}
			}
			m := model{A: strings.Repeat("a", spaPersistMaxBytes-10000), B: data, FX: makeFX("none"), Session: Just("synthetic")}
			// Prove the intended eviction geometry from actual canonical bytes:
			// A itself fits, leaving less room than B, while B alone fits.
			aEncoded, err := json.Marshal(m.A)
			if err != nil {
				t.Fatal(err)
			}
			bRaw, err := codecAutoEncodeTyped(reflect.ValueOf(m.B), "[]row", true)
			if err != nil {
				t.Fatal(err)
			}
			bEncoded, err := json.Marshal(bRaw)
			if err != nil {
				t.Fatal(err)
			}
			remaining := spaPersistMaxBytes - len(aEncoded)
			if remaining < 0 || len(bEncoded) <= remaining || len(bEncoded) > spaPersistMaxBytes {
				t.Fatal("fixture does not establish subcap B eviction by retained subcap A")
			}
			e := spaMixedTestEncoder(&log)
			e.InputType = SpaModelInputType[model]()
			controller := spaMixedPersistence{encoder: e}
			life := spaPersistLife{}
			kv := &spaClosedIntegrationKV{values: map[string]string{}, trace: &log}
			var original *spaClosedEncodingNode
			kv.afterSet = func() {
				if incremental {
					node := controller.cache.closed[1]
					if where == "none" || where == "publication" {
						if node == nil || node.raw == "" {
							t.Fatal("published before reserved new-node raw fill")
						}
					}
					if original != nil && original.raw != "" {
						t.Fatal("publication mutated original certificate")
					}
				}
				if where == "publication" {
					RegisterEnum(spaLazyTestEnum, []string{"after-publication"})
				}
			}
			clears := []bool{}
			for step := 0; step < 2; step++ {
				next := m
				if step == 1 {
					next.A = "small"
					next.FX = makeFX(where)
				}
				if incremental {
					clear, _ := controller.step(m, next, e.encode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
					clears = append(clears, clear)
				} else {
					clear := spaPersistStep(m, next, e.encode, func(blob string) { spaPersistWrite(kv, &life, blob, []string{"session"}, spaPersistMaxBytes) }, []string{"session"}, nil)
					clears = append(clears, clear)
				}
				if step == 0 && incremental {
					original = controller.cache.closed[1]
					if original == nil || original.raw != "" {
						t.Fatal("warm controller retained missing leaf")
					}
				}
				m = next
			}
			return log, kv.values, life, clears
		}
		a, av, al, ac := run(false)
		b, bv, bl, bc := run(true)
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(av, bv) || al != bl || !reflect.DeepEqual(ac, bc) {
			t.Fatalf("pinned controller diverged under %s mutation", where)
		}
	}
}

func TestSpaClosedLazyProtectedIdentityAndPayloadResolveOnce(t *testing.T) {
	type model struct {
		A string `sky:"a,string"`
		B string `sky:"b,string"`
	}
	log := []string{}
	e := spaMixedTestEncoder(&log)
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Protected: []string{"b"}}
	m := model{A: strings.Repeat("a", spaPersistMaxBytes*2/3), B: strings.Repeat("b", spaPersistMaxBytes*2/3)}
	warm := spaEncodeMixed(e, nil, m, options)
	if !warm.Valid || warm.NextCache.closed[1].raw != "" {
		t.Fatal("protected raw-miss fixture failed")
	}
	next := m
	next.A = "small"
	got := spaEncodeMixed(e, warm.NextCache, next, options)
	want := spaClosedCanonical(next)
	identity, ok := spaIdentityOf(want, options.Protected)
	if !ok || !got.Valid || got.Payload != want || got.Identity != identity || got.Work.DeferredConversions != 1 || got.Work.DeferredMarshals != 1 {
		t.Fatal("identity+payload duplicated pinned reconstruction or changed bytes")
	}
	if got.Work.MaterializedBytes != uint64(len(want)) {
		t.Fatal("deferred work changed whole-payload materialization metric")
	}
}

func TestSpaClosedLazyDeferredFailureReportsAttemptedWorkBeforeRecovery(t *testing.T) {
	type model struct {
		A  string `sky:"a,string"`
		B  string `sky:"b,string"`
		FX any    `sky:"fx,any"`
	}
	log := []string{}
	e := spaMixedTestEncoder(&log)
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes}
	m := model{A: strings.Repeat("a", spaPersistMaxBytes*2/3), B: strings.Repeat("b", spaPersistMaxBytes*2/3), FX: spaMixedTestFX{log: &log, V0: "synthetic"}}
	warm := spaEncodeMixed(e, nil, m, options)
	if !warm.Valid || warm.NextCache.closed[1].raw != "" {
		t.Fatal("corrupt-certificate fixture failed")
	}
	// Deliberate test-only invariant violation: guarded failure must not replay
	// unknown effects or omit already-attempted deferred work from its metrics.
	warm.NextCache.closed[1].size++
	next := m
	next.A = "small"
	log = nil
	got := spaEncodeMixed(e, warm.NextCache, next, options)
	if got.Valid || got.NextCache != nil || got.Work.DeferredConversions != 1 || got.Work.DeferredMarshals != 1 || got.Work.DeferredEncodedBytes == 0 {
		t.Fatal("deferred invariant failure omitted attempted work or published cache")
	}
	if !reflect.DeepEqual(log, []string{"prepare", "tag", "convert"}) {
		t.Fatal("deferred failure replayed unknown effects")
	}
}
