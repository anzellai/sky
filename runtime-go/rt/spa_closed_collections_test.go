package rt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type spaCollectionVariantScalar int

func (spaCollectionVariantScalar) SkyVariantTag() int     { panic("foreign scalar tag") }
func (spaCollectionVariantScalar) SkyVariantName() string { panic("foreign scalar name") }

type spaCollectionTupleLookalike struct {
	V0 int
	V1 string
}

func (spaCollectionTupleLookalike) spaClosedTuple() {}

type spaCollectionDictLookalike struct {
	kids    *dslot[int]
	n       int
	bm      uint32
	trie    bool
	foreign bool
}

func (spaCollectionDictLookalike) spaClosedDictElement() reflect.Type { return reflect.TypeOf(0) }
func (spaCollectionDictLookalike) spaClosedDictSame(any) bool         { panic("lookalike witness called") }

type spaCollectionTupleEmbed struct{ T2[int, string] }
type spaCollectionDictEmbed struct{ SkyDict[int] }
type spaCollectionDictAlias SkyDict[int]

func TestSpaMixedNominalCollectionsAdmissionIsPrivate(t *testing.T) {
	yes := []any{T2[int, string]{}, T3[int, SkyMaybe[string], SkyDict[int]]{}, SkyDict[int]{}, SkyDict[T2[int, string]]{}}
	for _, v := range yes {
		typ := reflect.TypeOf(v)
		if !spaClosedTypeEligible(typ) {
			t.Fatalf("runtime nominal collection rejected: %v", typ)
		}
		if spaProjectionTypeClosed(typ, map[reflect.Type]uint8{}, 0) {
			t.Fatalf("old whole-Full proof widened: %v", typ)
		}
	}
	no := []any{spaCollectionVariantScalar(0), spaCollectionTupleLookalike{}, spaCollectionDictLookalike{}, spaCollectionTupleEmbed{}, spaCollectionDictEmbed{}, spaCollectionDictAlias{}, T2[any, int]{}, SkyDict[any]{}, SkyDict[JsonValue]{}, SkySet{}, T4[int, int, int, int]{}}
	for _, v := range no {
		if spaClosedTypeEligible(reflect.TypeOf(v)) {
			t.Fatalf("unknown/lookalike admitted: %T", v)
		}
	}
	var nilSlice []T2[int, string]
	if !spaClosedTypeEligible(reflect.TypeOf(nilSlice)) {
		t.Fatal("closed nil tuple slice rejected")
	}
}

func spaCollectionEncode(v, old any, prior *spaClosedEncodingNode, declared string, epoch *codecEnumRegistrySnapshot, budget *spaClosedEncodingBudget) spaClosedEncodingResult {
	return spaEncodeClosed(reflect.ValueOf(v), reflect.ValueOf(old), prior, declared, epoch, codecEnumRegistry(), spaClosedEncodingOptions{NeedRaw: true}, budget)
}
func spaCollectionWant(t *testing.T, v any, declared string) string {
	t.Helper()
	raw, err := codecAutoEncodeTyped(reflect.ValueOf(v), declared, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func spaCollectionAssert(t *testing.T, r spaClosedEncodingResult, want string) {
	t.Helper()
	got, ok := r.materialize(spaPersistMaxBytes)
	if !r.Supported || r.ConversionErr != nil || r.MarshalErr != nil || !ok || got != want {
		t.Fatalf("collection differential mismatch: supported=%v materialized=%v", r.Supported, ok)
	}
}

func TestSpaMixedCollectionsCanonicalNilZeroFlatHAMTAndChanges(t *testing.T) {
	for _, n := range []int{0, 1, dictFlatMax, dictFlatMax + 1, 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			d := SkyDict[int]{}
			for i := 0; i < n; i++ {
				d = d.Insert(fmt.Sprintf("key-%03d", i), i)
			}
			epoch := codecEnumRegistry()
			warm := spaCollectionEncode(d, nil, nil, "rt.SkyDict[int]", nil, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			spaCollectionAssert(t, warm, spaCollectionWant(t, d, "rt.SkyDict[int]"))
			same := spaCollectionEncode(d, d, warm.Cache, "rt.SkyDict[int]", epoch, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			if same.Work.Reuses != 1 || same.Work.Conversions != 0 || !same.SameJSON {
				t.Fatal("unchanged root not reused")
			}
			noop := d.Remove("absent")
			got := spaCollectionEncode(noop, d, warm.Cache, "rt.SkyDict[int]", epoch, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			if got.Work.Reuses != 1 {
				t.Fatal("remove-absent header did not reuse")
			}
			changed := d.Insert("new", 99)
			got = spaCollectionEncode(changed, d, warm.Cache, "rt.SkyDict[int]", epoch, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			if got.Work.Conversions != 1 || got.Work.Marshals != 1 || got.Work.Reuses != 0 {
				t.Fatal("changed root did not use one full atomic encoding")
			}
			spaCollectionAssert(t, got, spaCollectionWant(t, changed, "rt.SkyDict[int]"))
			// Equal contents in a freshly allocated root still require canonical work.
			again := d.Insert("new", 99)
			got = spaCollectionEncode(again, changed, got.Cache, "rt.SkyDict[int]", epoch, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			if got.Work.Conversions != 1 || got.Work.Reuses != 0 {
				t.Fatal("equal contents confused with header identity")
			}
			marked := d
			marked.foreign = !marked.foreign
			got = spaCollectionEncode(marked, d, warm.Cache, "rt.SkyDict[int]", epoch, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			if got.Work.Reuses != 0 {
				t.Fatal("foreign header bit omitted")
			}
		})
	}
	var nilTuples []T2[int, string]
	for _, v := range []any{nilTuples, []T2[int, string]{}, []T2[int, string]{{V0: 1, V1: "x"}}, T2[int, string]{}, T3[int, string, SkyMaybe[int]]{}} {
		got := spaCollectionEncode(v, nil, nil, "", nil, spaNewClosedEncodingBudget(spaPersistMaxBytes))
		spaCollectionAssert(t, got, spaCollectionWant(t, v, ""))
	}
	// Same physical empty representation in another instantiation is not a witness.
	b := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	b.NodesLeft = 0
	d := SkyDict[int]{}.Insert("a", 1)
	fallback := spaCollectionEncode(d, nil, nil, "rt.SkyDict[int]", nil, b)
	spaCollectionAssert(t, fallback, spaCollectionWant(t, d, "rt.SkyDict[int]"))
	if fallback.Cache != nil || fallback.Work.BudgetFallbacks != 1 {
		t.Fatal("collection node budget fallback")
	}
	var budget = 16
	if spaClosedMixedValueSame(reflect.ValueOf(SkyDict[int]{}), reflect.ValueOf(SkyDict[string]{}), &budget) {
		t.Fatal("cross-instantiation witness")
	}
}

func TestSpaMixedCollectionsDeclaredEnumsAndLazyRawBudget(t *testing.T) {
	const enum = "sky.test.mixed.collections.enum"
	old, had := enumVariantsForType(enum)
	defer setEnumRegistration(enum, old, had)
	RegisterEnum(enum, []string{"old"})
	tuple := T2[int, string]{V1: strings.Repeat("x", 1000)}
	dict := SkyDict[int]{}.Insert("a", 0)
	for _, tc := range []struct {
		v        any
		declared string
	}{{tuple, "rt.T2[" + enum + ",string]"}, {dict, "rt.SkyDict[" + enum + "]"}} {
		epoch := codecEnumRegistry()
		want := spaCollectionWant(t, tc.v, tc.declared)
		b := spaNewClosedEncodingBudget(spaPersistMaxBytes)
		b.BytesLeft = 0
		warm := spaCollectionEncode(tc.v, nil, nil, tc.declared, nil, b)
		if warm.Cache == nil || warm.Cache.raw != "" {
			t.Fatal("raw budget not respected")
		}
		b = spaNewClosedEncodingBudget(spaPersistMaxBytes)
		b.BytesLeft = 0
		got := spaCollectionEncode(tc.v, tc.v, warm.Cache, tc.declared, epoch, b)
		if got.Work.Reuses != 1 || got.Plan.lazy == nil {
			t.Fatal("missing raw did not defer canonical materialization")
		}
		RegisterEnum(enum, []string{"new-longer"})
		spaCollectionAssert(t, got, want)
		if b.Deferred.Conversions != 1 || b.Deferred.Marshals != 1 || warm.Cache.raw != "" {
			t.Fatal("lazy canonical accounting/prior immutability")
		}
		changed := spaCollectionEncode(tc.v, tc.v, warm.Cache, tc.declared, epoch, spaNewClosedEncodingBudget(spaPersistMaxBytes))
		if changed.Work.Reuses != 0 {
			t.Fatal("enum epoch change reused old certificate")
		}
		spaCollectionAssert(t, changed, spaCollectionWant(t, tc.v, tc.declared))
		RegisterEnum(enum, []string{"old"})
	}
}

func TestSpaMixedCollectionsStrictPrepareAndSiblingReuse(t *testing.T) {
	type model struct {
		Pair  T2[int, string] `sky:"pair,rt.T2[int,string]"`
		Items SkyDict[int]    `sky:"items,rt.SkyDict[int]"`
		Tick  string          `sky:"tick,string"`
		FX    any             `sky:"fx,any"`
	}
	d := SkyDict[int]{}
	for i := 0; i < 64; i++ {
		d = d.Insert(fmt.Sprint(i), i)
	}
	log := []string{}
	fx := spaMixedTestFX{log: &log, V0: "synthetic"}
	old := model{Pair: T2[int, string]{V0: 2, V1: "fixed"}, Items: d, FX: fx}
	e := spaMixedTestEncoder(&log)
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes}
	warm := spaEncodeMixed(e, nil, old, options)
	next := old
	next.Tick = "typed"
	log = nil
	got := spaEncodeMixed(e, warm.NextCache, next, options)
	if !warm.Valid || !got.Valid || got.Payload != spaClosedCanonical(next) || got.Work.ClosedReuses < 2 {
		t.Fatal("mixed collection sibling differential/reuse")
	}
	// Full reference below has its own log, so inspect strict Prepare before it.
	if len(log) == 0 || log[0] != "prepare" {
		t.Fatal("strict Prepare skipped")
	}
	fail := e
	fail.Prepare = func(any) any { panic("strict constructor") }
	if spaEncodeMixed(fail, warm.NextCache, next, options).Valid {
		t.Fatal("reuse bypassed strict Prepare failure")
	}
}
