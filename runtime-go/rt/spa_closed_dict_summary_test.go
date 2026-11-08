package rt

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func spaDictSummaryEncode(value, old any, prior *spaClosedEncodingNode, declared string, epoch *codecEnumRegistrySnapshot, capBytes int, budget *spaClosedEncodingBudget) spaClosedEncodingResult {
	if budget == nil {
		budget = spaNewClosedEncodingBudget(capBytes)
	}
	return spaEncodeClosed(reflect.ValueOf(value), reflect.ValueOf(old), prior, declared, epoch, codecEnumRegistry(), spaClosedEncodingOptions{NeedRaw: true, AllowOversizeWithoutRaw: true}, budget)
}
func spaDictSummaryWant(t *testing.T, value any, declared string) string {
	t.Helper()
	return spaCollectionWant(t, value, declared)
}
func spaDictSummaryFixture() SkyDict[string] {
	d := SkyDict[string]{}
	for i := 0; i < 256; i++ {
		d = d.Insert(fmt.Sprintf("k%04d", i), strings.Repeat("x", 128))
	}
	return d
}
func spaDictSummaryCheck(t *testing.T, r spaClosedEncodingResult, value any, declared string, capBytes int) {
	t.Helper()
	want := spaDictSummaryWant(t, value, declared)
	if !r.Supported || r.ConversionErr != nil || r.MarshalErr != nil || r.Size != uint64(len(want)) || r.Null || r.RawKind != spaClosedRawOther {
		t.Fatal("Dict summary changed canonical validity/length/null")
	}
	if len(want) <= capBytes {
		got, ok := r.materialize(uint64(capBytes))
		if !ok || got != want {
			t.Fatal("under-cap Dict did not use exact canonical output")
		}
	}
}

func TestSpaDictSummaryPrimitiveDeltasAndCapCrossing(t *testing.T) {
	const declared = "rt.SkyDict[string]"
	const capBytes = 4096
	old := spaDictSummaryFixture()
	epoch := codecEnumRegistry()
	warm := spaDictSummaryEncode(old, nil, nil, declared, nil, capBytes, nil)
	spaDictSummaryCheck(t, warm, old, declared, capBytes)
	if warm.Cache == nil || warm.Cache.dict == nil {
		t.Fatal("oversized warm Dict missing bounded summary")
	}
	next := old.Insert("k0128", strings.Repeat("x", 128)+"y")
	got := spaDictSummaryEncode(next, old, warm.Cache, declared, epoch, capBytes, nil)
	spaDictSummaryCheck(t, got, next, declared, capBytes)
	if got.Work.EncodedBytes != 131 || got.Work.Conversions != 1 || got.Work.Reuses != 0 || got.SameJSON || got.Cache == nil || got.Cache.raw != "" || got.Plan == nil || !got.Plan.missing {
		t.Fatal("changed oversized Dict did not encode one primitive with missing oversize bytes")
	}
	// Raising the cap on exactly the retained current root must recover bytes
	// through the already-existing pinned canonical lazy path, never old JSON.
	budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	restored := spaDictSummaryEncode(next, next, got.Cache, declared, epoch, spaPersistMaxBytes, budget)
	if restored.Plan == nil || restored.Plan.lazy == nil {
		t.Fatal("cap increase bypassed pinned canonical reconstruction")
	}
	spaDictSummaryCheck(t, restored, next, declared, spaPersistMaxBytes)
	if budget.Deferred.Conversions != 1 || budget.Deferred.Marshals != 1 || got.Cache.raw != "" {
		t.Fatal("cap-increase reconstruction work was not counted")
	}
	// Add/remove and eventually cross below the cap; exact ordinary output is
	// required regardless of hash-tree compression or lost subtree alignment.
	current, prior := next, got.Cache
	for i := 0; i < 255; i++ {
		modified := current.Remove(fmt.Sprintf("k%04d", i))
		result := spaDictSummaryEncode(modified, current, prior, declared, epoch, capBytes, nil)
		spaDictSummaryCheck(t, result, modified, declared, capBytes)
		current, prior = modified, result.Cache
	}
	changed := current.Insert("<\"\\\n", "\x00é<>&")
	result := spaDictSummaryEncode(changed, current, prior, declared, epoch, capBytes, nil)
	spaDictSummaryCheck(t, result, changed, declared, capBytes)
}

func TestSpaDictSummaryFlatTrieAndExactPrimitiveDomains(t *testing.T) {
	for _, n := range []int{0, 1, 16, 17, 64} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			d := SkyDict[string]{}
			for i := 0; i < n; i++ {
				d = d.Insert(fmt.Sprintf("k%04d", i), strings.Repeat("x", 128))
			}
			epoch := codecEnumRegistry()
			warm := spaDictSummaryEncode(d, nil, nil, "rt.SkyDict[string]", nil, 64, nil)
			next := d.Insert("new", strings.Repeat("z", 129))
			r := spaDictSummaryEncode(next, d, warm.Cache, "rt.SkyDict[string]", epoch, 64, nil)
			spaDictSummaryCheck(t, r, next, "rt.SkyDict[string]", 64)
			removed := next.Remove("new")
			r2 := spaDictSummaryEncode(removed, next, r.Cache, "rt.SkyDict[string]", epoch, 64, nil)
			spaDictSummaryCheck(t, r2, removed, "rt.SkyDict[string]", 64)
		})
	}
	cases := []struct {
		value    any
		declared string
	}{
		{SkyDict[int]{}.Insert("a", int(^uint(0)>>1)), "rt.SkyDict[int]"},
		{SkyDict[bool]{}.Insert("a", true), "rt.SkyDict[bool]"},
	}
	for _, tc := range cases {
		r := spaDictSummaryEncode(tc.value, nil, nil, tc.declared, nil, 1, nil)
		spaDictSummaryCheck(t, r, tc.value, tc.declared, 1)
		if r.Cache == nil || r.Cache.dict == nil {
			t.Fatal("exact primitive domain missing summary")
		}
	}
	type foreignInt int
	no := []struct {
		value    any
		declared string
	}{
		{SkyDict[float64]{}.Insert("a", 1), "rt.SkyDict[float64]"},
		{SkyDict[foreignInt]{}.Insert("a", 1), "rt.SkyDict[int]"},
		{spaDictSummaryFixture(), "rt.SkyDict[Alias]"},
	}
	for _, tc := range no {
		r := spaDictSummaryEncode(tc.value, nil, nil, tc.declared, nil, 1, nil)
		spaDictSummaryCheck(t, r, tc.value, tc.declared, 1)
		if r.Cache != nil && r.Cache.dict != nil {
			t.Fatal("nonprimitive/alias acquired summary")
		}
	}
	bad := spaDictSummaryEncode(SkyDict[float64]{}.Insert("a", math.NaN()), nil, nil, "rt.SkyDict[float64]", nil, 1, nil)
	if bad.MarshalErr == nil || bad.Cache != nil {
		t.Fatal("Float failure bypassed canonical marshal")
	}
}

func TestSpaDictSummaryBudgetDepthAndCurrentOwner(t *testing.T) {
	const declared = "rt.SkyDict[string]"
	old := spaDictSummaryFixture()
	epoch := codecEnumRegistry()
	warm := spaDictSummaryEncode(old, nil, nil, declared, nil, 4096, nil)
	if warm.Cache == nil || warm.Cache.dict == nil {
		t.Fatal("missing ownership fixture summary")
	}
	previous := warm.Cache.dict
	oldUnits := previous.units
	next := old.Insert("k0128", strings.Repeat("y", 128)).Remove("k0042")
	budget := spaNewClosedEncodingBudget(4096)
	r := spaDictSummaryEncode(next, old, warm.Cache, declared, epoch, 4096, budget)
	spaDictSummaryCheck(t, r, next, declared, 4096)
	if r.Cache == nil || r.Cache.dict == nil || r.Cache.dict.units+1 > spaClosedEncodingNodeLimit || budget.NodesLeft < 0 || budget.NodesLeft != spaClosedEncodingNodeLimit-1-r.Cache.dict.units {
		t.Fatal("retained summary bypassed global node/slot ledger")
	}
	if previous.units != oldUnits || warm.Cache.dict != previous {
		t.Fatal("old immutable summary mutated")
	}

	// Every retained node is scalar-only; no typed slot pointer is permitted
	// even when that slot would happen to be reachable from the current owner.
	var check func(*spaClosedDictSummary)
	check = func(s *spaClosedDictSummary) {
		typ := reflect.TypeOf(*s)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Name == "children" && field.Type == reflect.TypeOf([]*spaClosedDictSummary{}) {
				continue
			}
			switch field.Type.Kind() {
			case reflect.Bool, reflect.Int, reflect.Uint32, reflect.Uint64:
			default:
				t.Fatal("summary certificate can retain model/value/interface state")
			}
		}
		if s.leaf && !spaDictSummaryLeafValid(s) {
			t.Fatal("invalid scalar leaf certificate")
		}
		for _, child := range s.children {
			check(child)
		}
	}
	check(r.Cache.dict)
	small := spaNewClosedEncodingBudget(4096)
	small.NodesLeft = 2
	fallback := spaDictSummaryEncode(next, old, warm.Cache, declared, epoch, 4096, small)
	spaDictSummaryCheck(t, fallback, next, declared, 4096)
	if fallback.Cache == nil || fallback.Cache.dict != nil || fallback.Work.BudgetFallbacks == 0 || small.NodesLeft < 0 {
		t.Fatal("summary budget exhaustion did not retain canonical fallback")
	}
	small = spaNewClosedEncodingBudget(4096)
	small.DepthLimit = 1
	fallback = spaDictSummaryEncode(next, old, warm.Cache, declared, epoch, 4096, small)
	spaDictSummaryCheck(t, fallback, next, declared, 4096)
	if fallback.Cache != nil && fallback.Cache.dict != nil {
		t.Fatal("summary depth bound bypassed")
	}
	small = spaNewClosedEncodingBudget(4096)
	small.NodesLeft = 2
	same := spaDictSummaryEncode(old, old, warm.Cache, declared, epoch, 4096, small)
	if same.Cache == nil || same.Cache.dict != nil || same.Work.Reuses != 1 {
		t.Fatal("unchanged root did not detach unaffordable metadata")
	}
}

func TestSpaDictSummaryEpochOwnerAndCollisionFallback(t *testing.T) {
	oldRegistry := codecEnumRegistry()
	oldNames, hadNames := enumVariantsForType("int")
	oldOwner, hadOwner := oldRegistry.owners["rt.SkyDict[int]"]
	restore := func() {
		setEnumRegistration("int", oldNames, hadNames)
		enumRegistryWriter.Lock()
		current := codecEnumRegistry()
		owners := make(map[string]string, len(current.owners))
		for k, v := range current.owners {
			if k != "rt.SkyDict[int]" {
				owners[k] = v
			}
		}
		if hadOwner {
			owners["rt.SkyDict[int]"] = oldOwner
		}
		enumRegistry.Store(&codecEnumRegistrySnapshot{names: current.names, owners: owners})
		enumRegistryWriter.Unlock()
	}
	t.Cleanup(restore)
	d := SkyDict[int]{}.Insert("a", 0).Insert("b", 1)
	warm := spaDictSummaryEncode(d, nil, nil, "rt.SkyDict[int]", nil, 1, nil)
	if warm.Cache == nil || warm.Cache.dict == nil {
		t.Fatal("missing enum invalidation fixture")
	}
	RegisterEnum("int", []string{"zero-name", "one-name"})
	next := d.Insert("a", 1)
	changed := spaDictSummaryEncode(next, d, warm.Cache, "rt.SkyDict[int]", oldRegistry, 1, nil)
	spaDictSummaryCheck(t, changed, next, "rt.SkyDict[int]", 1)
	if changed.Cache != nil && changed.Cache.dict != nil {
		t.Fatal("registered primitive enum acquired numeric summary")
	}
	// Exact declared Dict owner dispatch has priority over collection summaries.
	owners := make(map[string]string, len(codecEnumRegistry().owners)+1)
	for k, v := range codecEnumRegistry().owners {
		owners[k] = v
	}
	owners["rt.SkyDict[int]"] = skyMoneyOwner
	enumRegistryWriter.Lock()
	enumRegistry.Store(&codecEnumRegistrySnapshot{names: codecEnumRegistry().names, owners: owners})
	enumRegistryWriter.Unlock()
	rejected := spaDictSummaryEncode(next, d, warm.Cache, "rt.SkyDict[int]", oldRegistry, 1, nil)
	if rejected.ConversionErr == nil || rejected.Cache != nil {
		t.Fatal("nominal declared owner error bypassed")
	}
	restore()
	// A synthetic private collision bucket exercises conservative fallback
	// without relying on process-random hashes or changing the global seed.
	leaves := []dslot[string]{{key: "a", val: strings.Repeat("x", 128)}, {key: "b", val: strings.Repeat("y", 128)}}
	roots := []dslot[string]{{coll: true, bm: 2, kids: &leaves[0]}}
	collision := SkyDict[string]{kids: &roots[0], n: 2, bm: 1, trie: true}
	result := spaDictSummaryEncode(collision, nil, nil, "rt.SkyDict[string]", nil, 1, nil)
	spaDictSummaryCheck(t, result, collision, "rt.SkyDict[string]", 1)
	if result.Cache == nil || result.Cache.dict != nil || result.Work.BudgetFallbacks == 0 {
		t.Fatal("collision bucket was summarized")
	}
}

func TestSpaDictSummaryStrictPrepareAndUnknownFailureOrder(t *testing.T) {
	type model struct {
		Before  any             `sky:"before,any"`
		Catalog SkyDict[string] `sky:"catalog,rt.SkyDict[string]"`
		After   any             `sky:"after,any"`
	}
	dict := spaDictSummaryFixture()
	for _, failure := range []string{"", "before-conversion", "after-conversion", "before-marshal", "after-marshal"} {
		t.Run(failure, func(t *testing.T) {
			refTrace, optTrace := []string{}, []string{}
			create := func(log *[]string, d SkyDict[string], failing bool) model {
				side := func(name string) any {
					action := (func())(nil)
					if failing && failure == name+"-conversion" {
						action = func() { panic("synthetic conversion failure") }
					}
					marshalFailure := ""
					if failing && failure == name+"-marshal" {
						marshalFailure = "error"
					}
					return spaMixedTestFX{log: log, action: action, V0: JsonValue{raw: spaMixedTestMarshal{log: log, name: name + "-marshal", failure: marshalFailure}}}
				}
				return model{Before: side("before"), Catalog: d, After: side("after")}
			}
			refEncoder, optEncoder := spaMixedTestEncoder(&refTrace), spaMixedTestEncoder(&optTrace)
			old := create(&optTrace, dict, false)
			warm := spaEncodeMixed(optEncoder, nil, old, spaMixedEncodeOptions{CapBytes: 4096})
			if !warm.Valid || warm.NextCache == nil || warm.NextCache.closed[1] == nil || warm.NextCache.closed[1].dict == nil {
				t.Fatal("missing mixed summary warm fixture")
			}
			modified := dict.Insert("k0128", strings.Repeat("x", 128)+"y")
			refTrace, optTrace = nil, nil
			full := refEncoder.encode(create(&refTrace, modified, true))
			result := spaEncodeMixed(optEncoder, warm.NextCache, create(&optTrace, modified, true), spaMixedEncodeOptions{CapBytes: 4096})
			if !reflect.DeepEqual(refTrace, optTrace) || result.Valid != (full != "") || result.Work.Prepares != 1 {
				t.Fatal("summary reordered strict prepare/conversion/marshal failure boundary")
			}
			if full != "" {
				if result.EncodedBytes != uint64(len(full)) || result.Payload != "" {
					t.Fatal("unknown sibling mixed exact length/NoWrite mismatch")
				}
			} else if result.NextCache != nil {
				t.Fatal("failed mixed encode retained current/prior certificates")
			}
			fail := optEncoder
			optTrace = nil
			fail.Prepare = func(any) any {
				optTrace = append(optTrace, "prepare-failure")
				panic("synthetic strict constructor failure")
			}
			if spaEncodeMixed(fail, warm.NextCache, create(&optTrace, modified, false), spaMixedEncodeOptions{CapBytes: 4096}).Valid || !reflect.DeepEqual(optTrace, []string{"prepare-failure"}) {
				t.Fatal("summary bypassed strict constructor failure")
			}
		})
	}
}

func TestSpaDictSummaryProtectedAndDiscardedRawStayCanonical(t *testing.T) {
	const declared = "rt.SkyDict[string]"
	old := spaDictSummaryFixture()
	epoch := codecEnumRegistry()
	warm := spaDictSummaryEncode(old, nil, nil, declared, nil, 4096, nil)
	next := old.Insert("k0128", strings.Repeat("x", 128)+"y")
	for _, options := range []spaClosedEncodingOptions{
		{NeedRaw: true, AllowOversizeWithoutRaw: false},
		{NeedRaw: false, AllowOversizeWithoutRaw: true, DiscardRawRetention: true},
	} {
		r := spaEncodeClosed(reflect.ValueOf(next), reflect.ValueOf(old), warm.Cache, declared, epoch, codecEnumRegistry(), options, spaNewClosedEncodingBudget(4096))
		want := spaDictSummaryWant(t, next, declared)
		raw, ok := r.materialize(spaPersistMaxBytes)
		if !ok || raw != want || r.Work.Conversions != 1 || r.Work.Marshals != 1 || r.Work.EncodedBytes != uint64(len(want)) || r.Cache != nil && r.Cache.dict != nil {
			t.Fatal("protected/excluded/previous Dict left canonical conversion boundary")
		}
	}
}

func TestSpaDictSummaryNoWriteStillFencesIdentityAndSignOut(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Catalog SkyDict[string]  `sky:"catalog,rt.SkyDict[string]"`
	}
	refTrace, optTrace := []string{}, []string{}
	refEncoder, optEncoder := spaMixedTestEncoder(&refTrace), spaMixedTestEncoder(&optTrace)
	optEncoder.InputType = SpaModelInputType[model]()
	current := model{Session: Just("synthetic-A"), Catalog: spaDictSummaryFixture()}
	reference := &spaPublicationKV{values: memKV{spaPersistKey: "previous"}}
	optimized := &spaPublicationKV{values: memKV{spaPersistKey: "previous"}}
	refLife, optLife := spaPersistLife{}, spaPersistLife{}
	controller := spaMixedPersistence{encoder: optEncoder, profiling: true}
	for step := 0; step < 3; step++ {
		next := current
		if step == 1 {
			next.Session = Just("synthetic-B")
			next.Catalog = next.Catalog.Insert("k0128", strings.Repeat("y", 129))
		}
		if step == 2 {
			next.Session = Nothing[string]()
			next.Catalog = next.Catalog.Remove("k0000")
		}
		refTrace, optTrace = nil, nil
		a := spaPersistStep(current, next, refEncoder.encode, func(blob string) { spaPersistWrite(reference, &refLife, blob, []string{"session"}, 4096) }, []string{"session"}, nil)
		b, _ := controller.step(current, next, optEncoder.encode, optimized, &optLife, nil, []string{"session"}, 4096)
		if a != b || refLife != optLife || !reflect.DeepEqual(refTrace, optTrace) || !reflect.DeepEqual(reference.calls, optimized.calls) || !reflect.DeepEqual(reference.values, optimized.values) {
			t.Fatal("changed Dict NoWrite reordered identity/session-clear effects")
		}
		if step == 2 && !b {
			t.Fatal("NoWrite summary suppressed required sign-out clear")
		}
		current = next
	}
	if len(optimized.calls) != 2 || optimized.calls[0] != "remove" || optimized.calls[1] != "remove" {
		t.Fatal("identity fencing publication calls changed")
	}
}

func TestSpaDictSummaryRejectsContradictoryOldCertificateShape(t *testing.T) {
	const declared = "rt.SkyDict[string]"
	old := spaDictSummaryFixture()
	epoch := codecEnumRegistry()
	warm := spaDictSummaryEncode(old, nil, nil, declared, nil, 4096, nil)
	if warm.Cache == nil || warm.Cache.dict == nil {
		t.Fatal("missing shape fixture")
	}
	next := old.Insert("k0128", strings.Repeat("x", 128)+"y")
	changes := []struct {
		name   string
		change func(*spaClosedDictSummary)
	}{
		{"count", func(s *spaClosedDictSummary) { s.count-- }},
		{"bitmap", func(s *spaClosedDictSummary) { s.bitmap ^= 1 }},
		{"units", func(s *spaClosedDictSummary) { s.units++ }},
		{"height", func(s *spaClosedDictSummary) { s.height++ }},
		{"bytes", func(s *spaClosedDictSummary) { s.entryBytes++ }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			certificate := *warm.Cache.dict
			tc.change(&certificate)
			node := *warm.Cache
			node.dict = &certificate
			r := spaDictSummaryEncode(next, old, &node, declared, epoch, 4096, nil)
			spaDictSummaryCheck(t, r, next, declared, 4096)
			if r.Work.BudgetFallbacks == 0 || r.Work.EncodedBytes < uint64(len(spaDictSummaryWant(t, next, declared))) {
				t.Fatal("contradictory old certificate was trusted")
			}
			same := spaDictSummaryEncode(old, old, &node, declared, epoch, 4096, nil)
			if same.Cache == nil || same.Cache.dict != nil || same.Work.Reuses != 1 {
				t.Fatal("unchanged root retained contradictory scalar metadata")
			}
		})
	}
	if warm.Cache.dict.count != old.n {
		t.Fatal("shape test mutated original certificate")
	}
}
