package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type spaDetailTestMsg struct{ Payload any }

func (spaDetailTestMsg) SkyVariantTag() int     { panic("profiler invoked variant tag") }
func (spaDetailTestMsg) SkyVariantName() string { panic("profiler invoked variant name") }

type spaDetailOtherMsg struct{}

func (spaDetailOtherMsg) SkyVariantTag() int     { panic("profiler invoked variant tag") }
func (spaDetailOtherMsg) SkyVariantName() string { panic("profiler invoked variant name") }

type spaDetailGenericMsg[T any] struct{}

func (spaDetailGenericMsg[T]) SkyVariantTag() int     { panic("profiler invoked variant tag") }
func (spaDetailGenericMsg[T]) SkyVariantName() string { panic("profiler invoked variant name") }

type spaDetailMethodSlice []int

func (spaDetailMethodSlice) String() string { panic("profiler invoked slice method") }

const spaDetailTestKey = "64e1d078c6a2a08525a8502c3795970f2f3971a17b774e978d8b240f3152ef40"

func spaDetailTestStart(t *testing.T) {
	spaProfileDetailStart(true)
	t.Cleanup(func() { spaProfileDetailStart(false) })
}
func spaDetailTestApp(builds, paints *int) *spaViewMemoApp {
	return newSpaViewMemoApp(SpaFns{ViewMemo: func(model, ctx any) any {
		return SpaViewMemoRegion(ctx, 17, []any{SpaViewMemoCapture[[]int](model.([]int))}, func() any { *builds++; return []string{"row"} })
	}})
}
func spaDetailTestPaint(paints *int) func(any) { return func(any) { *paints++ } }

func TestSpaProfileDetailRealDispatchNoReplayOrPayloadMethods(t *testing.T) {
	spaDetailTestStart(t)
	var cache spaViewMemoCache
	spaProfileDetailNewCache(&cache)
	trace := []string{}
	value := spaTransition(spaDetailTestMsg{Payload: func() { panic("payload forced") }}, 0,
		func(msg, model any) SkyTuple2 { trace = append(trace, "update"); return SkyTuple2{V0: 1, V1: nil} },
		func(any) { trace = append(trace, "paint") }, func(any) { trace = append(trace, "post") },
		func(string, any) { t.Fatal("observer interfered with transition") })
	if value != 1 || !reflect.DeepEqual(trace, []string{"update", "paint", "post"}) {
		t.Fatal("transition semantics changed")
	}
	// Actual compiled package metadata is a premise, not a guessed key.
	typ := reflect.TypeOf(spaDetailTestMsg{})
	if typ.PkgPath() != "sky-app/rt" || typ.Name() != "spaDetailTestMsg" {
		t.Fatal("run real module package, not file-list package")
	}
	snapshot := spaProfileDetailSnapshot()
	if snapshot.messages[spaDetailTestKey] != 1 {
		t.Fatal("actual dispatch counter missing")
	}
	if !snapshot.complete {
		t.Fatal("supported root message interval incomplete")
	}
}

func TestSpaProfileDetailRealSiteAdmissionCommitHitAndClear(t *testing.T) {
	spaDetailTestStart(t)
	rows := []int{1, 2, 3}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	app.render(rows, false, spaDetailTestPaint(&paints))
	app.render(rows, false, spaDetailTestPaint(&paints))
	if builds != 1 || paints != 2 {
		t.Fatal("cache/paint behavior changed before counter assertion")
	}
	s := spaProfileDetailSnapshot()
	v := s.sites[17]
	if v.calls != 2 || v.builds != 1 || v.hits != 1 {
		t.Fatal("actual site event counters missing")
	}
	if v.admissions != 2 || v.commits != 2 || s.gauge[17] != 1 || !s.gaugeKnown {
		t.Fatal("staging/actual publication distinguished incorrectly")
	}
	if !v.lastInputRowsKnown || v.lastInputRows != 3 || !s.complete {
		t.Fatal("exact last captured list length unavailable")
	}
	app.reset()
	s = spaProfileDetailSnapshot()
	v = s.sites[17]
	if s.gauge[17] != 0 || v.resets != 1 || v.commits != 2 {
		t.Fatal("reset entries mistaken for cumulative commits/gauge")
	}
}

func TestSpaProfileDetailCounterResetPreservesOldCacheHitsAndGauge(t *testing.T) {
	spaDetailTestStart(t)
	rows := []int{1}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	app.render(rows, false, spaDetailTestPaint(&paints))
	before := spaProfileDetailSnapshot()
	if !before.complete || before.gauge[17] != 1 {
		t.Fatal("actual publication premise")
	}
	spaProfileDetailResetCounters()
	middle := spaProfileDetailSnapshot()
	if middle.epoch == before.epoch || !middle.gaugeKnown || middle.gauge[17] != 1 || middle.sites[17].lastInputRowsKnown {
		t.Fatal("counter reset touched algorithm gauge or faked observed input")
	}
	site := middle.jsValue()["sites"].(map[string]any)["17"].(map[string]any)
	if site["lastInputRows"] != nil {
		t.Fatal("unknown rows must be null")
	}
	app.render(rows, false, spaDetailTestPaint(&paints))
	after := spaProfileDetailSnapshot()
	if builds != 1 || paints != 2 {
		t.Fatal("reset invalidated algorithm cache")
	}
	if after.sites[17].hits != 1 || after.sites[17].builds != 0 || after.sites[17].commits != 1 || after.gauge[17] != 1 || !after.complete {
		t.Fatal("new epoch lost old-entry hit/publication")
	}
}

func TestSpaProfileDetailLateOldFrameIsIncompleteWithActualGauge(t *testing.T) {
	spaDetailTestStart(t)
	rows := []int{1}
	built := 0
	app := newSpaViewMemoApp(SpaFns{ViewMemo: func(model, ctx any) any {
		return SpaViewMemoRegion(ctx, 21, []any{SpaViewMemoCapture[[]int](rows)}, func() any { built++; spaProfileDetailResetCounters(); return "value" })
	}})
	app.render(rows, false, func(any) {})
	s := spaProfileDetailSnapshot()
	if built != 1 || !s.unsupported || s.complete {
		t.Fatal("late old-epoch frame was silently certified")
	}
	if !s.gaugeKnown || s.gauge[21] != 1 {
		t.Fatal("late actual publication must still track current gauge")
	}
}

func TestSpaProfileDetailLateInitDoesNotRecoverByWalkingOrClearOldCache(t *testing.T) {
	spaProfileDetailStart(false)
	rows := []int{1}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	app.render(rows, false, spaDetailTestPaint(&paints))
	spaDetailTestStart(t)
	initial := spaProfileDetailSnapshot()
	if initial.gaugeKnown || initial.complete {
		t.Fatal("late init invented a zero gauge")
	}
	app.render(rows, false, spaDetailTestPaint(&paints))
	s := spaProfileDetailSnapshot()
	if builds != 1 || paints != 2 || s.sites[17].hits != 1 || s.sites[17].builds != 0 {
		t.Fatal("late init reset/replayed cached source")
	}
	if !s.complete || !s.gaugeKnown || s.gauge[17] != 1 {
		t.Fatal("tracked actual publication did not establish gauge")
	}
}

func TestSpaProfileDetailPaintAbortClearsCurrentGauge(t *testing.T) {
	spaDetailTestStart(t)
	rows := []int{1}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	app.render(rows, false, spaDetailTestPaint(&paints))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("paint panic swallowed")
			}
		}()
		app.render(rows, false, func(any) { panic("paint") })
	}()
	s := spaProfileDetailSnapshot()
	if builds != 1 || s.gauge[17] != 0 || s.sites[17].commits != 1 || s.sites[17].resets != 1 {
		t.Fatal("failed paint was counted as committed/current")
	}
}

func TestSpaProfileDetailNamedMetadataDomainAndNoPayloadExport(t *testing.T) {
	spaDetailTestStart(t)
	var cache spaViewMemoCache
	spaProfileDetailNewCache(&cache)
	typ := reflect.TypeOf(spaDetailTestMsg{})
	key, ok := spaDetailMessageKey(typ)
	if !ok || key != spaDetailTestKey {
		t.Fatal("compiled metadata key differs")
	}
	for _, v := range []any{nil, 42, &spaDetailTestMsg{}, spaDetailGenericMsg[int]{}, SkyADT{Tag: 0, SkyName: "private-payload-marker", Fields: []any{"private-payload-marker"}}} {
		spaProfileDetailResetCounters()
		spaProfileDetailDispatch(v)
		s := spaProfileDetailSnapshot()
		if !s.unsupported || s.complete {
			t.Fatal("unsupported message domain certified")
		}
		encoded, err := json.Marshal(s.jsValue())
		if err != nil || strings.Contains(string(encoded), "private-payload-marker") {
			t.Fatal("payload exported")
		}
	}
}

func TestSpaProfileDetailLastRowsUsesExistingSliceHeaderNotMethods(t *testing.T) {
	spaDetailTestStart(t)
	rows := spaDetailMethodSlice{1, 2, 3, 4}
	builds := 0
	app := newSpaViewMemoApp(SpaFns{ViewMemo: func(any, any) any { return nil }})
	frame := app.begin()
	SpaViewMemoRegion(frame, 29, []any{SpaViewMemoCapture[spaDetailMethodSlice](rows)}, func() any { builds++; return "value" })
	app.seal(frame)
	app.finish(frame, true)
	s := spaProfileDetailSnapshot()
	if builds != 1 || !s.sites[29].lastInputRowsKnown || s.sites[29].lastInputRows != 4 {
		t.Fatal("last captured slice metadata was not observed")
	}
}

func TestSpaProfileDetailBoundedWithoutSnapshots(t *testing.T) {
	spaDetailTestStart(t)
	rows := []int{1}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	for i := 0; i < spaDetailMaxPending+3; i++ {
		app.render(rows, false, spaDetailTestPaint(&paints))
	}
	spaDetailMu.Lock()
	pending := len(spaDetailStateNow.pending)
	spaDetailMu.Unlock()
	s := spaProfileDetailSnapshot()
	if builds != 1 || paints != spaDetailMaxPending+3 {
		t.Fatal("bounded profiling altered source execution")
	}
	if pending > spaDetailMaxPending || !s.overflow || s.complete || !s.gaugeKnown || s.gauge[17] != 1 {
		t.Fatal("unbounded or silently truncated pending shadows")
	}
	spaProfileDetailResetCounters()
	s = spaProfileDetailSnapshot()
	if !s.complete || s.gauge[17] != 1 {
		t.Fatal("counter reset lost live numeric gauge")
	}
}

func TestSpaProfileDetailCollisionOverflowAndUnsupportedSitesFailClosed(t *testing.T) {
	spaDetailTestStart(t)
	var cache spaViewMemoCache
	spaProfileDetailNewCache(&cache)
	spaDetailMu.Lock()
	s := spaDetailStateNow
	key := strings.Repeat("a", 64)
	if !spaDetailRegisterTypeLocked(s, reflect.TypeOf(spaDetailTestMsg{}), key) {
		t.Fatal("collision fixture first registration")
	}
	if spaDetailRegisterTypeLocked(s, reflect.TypeOf(spaDetailOtherMsg{}), key) {
		t.Fatal("digest collision accepted")
	}
	spaDetailMu.Unlock()
	snap := spaProfileDetailSnapshot()
	if !snap.collision || snap.complete {
		t.Fatal("collision flag missing")
	}
	spaProfileDetailResetCounters()
	if !spaProfileDetailSnapshot().collision {
		t.Fatal("reset erased witnessed type ambiguity")
	}
	spaProfileDetailStart(true)
	cache = spaViewMemoCache{}
	spaProfileDetailNewCache(&cache)
	frame := cache.begin(nil)
	spaDetailMu.Lock()
	site := spaDetailSiteLocked(spaDetailStateNow, 3)
	site.calls = spaDetailMaxCounter
	spaDetailMu.Unlock()
	spaProfileDetailSite(frame, 3, 0, reflect.Value{})
	snap = spaProfileDetailSnapshot()
	if !snap.overflow || snap.sites[3].calls != spaDetailMaxCounter || snap.complete {
		t.Fatal("counter wrapped/rounded")
	}
	spaProfileDetailSite(frame, spaDetailMaxSites, 0, reflect.Value{})
	if !spaProfileDetailSnapshot().unsupported {
		t.Fatal("unsupported static ID accepted")
	}
}

func TestSpaProfileDetailSnapshotCopiesAndConcurrentActualDispatch(t *testing.T) {
	spaDetailTestStart(t)
	var cache spaViewMemoCache
	spaProfileDetailNewCache(&cache)
	const workers = 4
	const count = 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < count; j++ {
				spaTransition(spaDetailTestMsg{}, 0, func(_, m any) SkyTuple2 { return SkyTuple2{V0: m} }, func(any) {}, func(any) {}, func(string, any) { panic("observer interfered") })
			}
		}()
	}
	for i := 0; i < 20; i++ {
		copy := spaProfileDetailSnapshot()
		copy.messages[spaDetailTestKey] = spaDetailMaxCounter
		copy.gauge[9] = 99
		copy.sites[9] = spaDetailSite{calls: 99}
	}
	wg.Wait()
	s := spaProfileDetailSnapshot()
	if s.messages[spaDetailTestKey] != workers*count || s.gauge[9] != 0 || s.sites[9].calls != 0 || !s.complete {
		t.Fatal("snapshot alias/race lost dispatches")
	}
}

func TestSpaProfileDetailDefaultOffAddsNoObserverAllocations(t *testing.T) {
	spaProfileDetailStart(false)
	var msg any = spaDetailTestMsg{}
	calls := testing.AllocsPerRun(100, func() {
		spaProfileDetailDispatch(msg)
		spaProfileDetailSite(nil, 0, 0, reflect.Value{})
		spaProfileDetailNewCache(nil)
		spaProfileDetailSnapshot()
	})
	if calls != 0 {
		t.Fatal("default-off observer allocated")
	}
}

func TestSpaProfileDetailOneCacheDomainAndObserverFailureDoNotReplay(t *testing.T) {
	spaDetailTestStart(t)
	a := newSpaViewMemoApp(SpaFns{})
	_ = a
	b := newSpaViewMemoApp(SpaFns{})
	_ = b
	if s := spaProfileDetailSnapshot(); !s.unsupported || s.complete || s.gaugeKnown {
		t.Fatal("multiple cache gauge silently combined")
	}
	spaProfileDetailStart(true)
	var cache spaViewMemoCache
	spaProfileDetailNewCache(&cache)
	spaDetailMu.Lock()
	spaDetailStateNow.messages = nil
	spaDetailMu.Unlock()
	updates := 0
	result := spaTransition(spaDetailTestMsg{}, 0, func(_, m any) SkyTuple2 { updates++; return SkyTuple2{V0: 1} }, func(any) {}, func(any) {}, func(string, any) { t.Fatal("diagnostic panic affected program") })
	if updates != 1 || result != 1 || !spaProfileDetailSnapshot().unsupported {
		t.Fatal("diagnostic failure escaped or replayed effects")
	}
}

func TestSpaProfileDetailUnknownGaugeAndRowsStayNull(t *testing.T) {
	spaProfileDetailStart(false)
	rows := []int{1}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	app.render(rows, false, spaDetailTestPaint(&paints))
	spaDetailTestStart(t)
	frame := app.begin()
	spaProfileDetailSite(frame, 7, 0, reflect.Value{})
	s := spaProfileDetailSnapshot()
	site := s.jsValue()["sites"].(map[string]any)["7"].(map[string]any)
	if s.complete || s.gaugeKnown || site["committedEntries"] != nil || site["lastInputRows"] != nil || site["lastInputRowsKnown"] != false {
		t.Fatal("unknown observation became numeric zero")
	}
}
func TestSpaProfileDetailBoundedTypesAndShadowAndEpoch(t *testing.T) {
	spaDetailTestStart(t)
	var cache spaViewMemoCache
	spaProfileDetailNewCache(&cache)
	spaDetailMu.Lock()
	s := spaDetailStateNow
	for i := 0; i < spaDetailMaxMessages; i++ {
		s.typeKeys[reflect.ArrayOf(i, reflect.TypeOf(byte(0)))] = "fixture"
	}
	spaDetailMu.Unlock()
	spaProfileDetailDispatch(spaDetailTestMsg{})
	if s := spaProfileDetailSnapshot(); !s.overflow || s.complete {
		t.Fatal("type registry cap silently truncated")
	}
	spaProfileDetailStart(true)
	cache = spaViewMemoCache{}
	spaProfileDetailNewCache(&cache)
	frame := cache.begin(nil)
	for i := 0; i < spaViewMemoMaxEntries+1; i++ {
		spaProfileDetailAdmission(frame, i)
	}
	if frame.detail.known || len(frame.detail.counts) > spaViewMemoMaxEntries || !spaProfileDetailSnapshot().overflow {
		t.Fatal("numeric shadow exceeded cache bound")
	}
	spaDetailMu.Lock()
	saved := spaDetailGeneration
	spaDetailGeneration = spaDetailMaxCounter
	spaDetailMu.Unlock()
	defer func() { spaDetailMu.Lock(); spaDetailGeneration = saved; spaDetailMu.Unlock() }()
	spaProfileDetailResetCounters()
	if s := spaProfileDetailSnapshot(); !s.overflow || s.complete {
		t.Fatal("epoch exhausted without explicit failure")
	}
}

func TestSpaProfileDetailMalformedCaptureFallbackIsIncomplete(t *testing.T) {
	spaDetailTestStart(t)
	rows := []int{1}
	builds, paints := 0, 0
	app := spaDetailTestApp(&builds, &paints)
	app.render(rows, false, spaDetailTestPaint(&paints))
	if !spaProfileDetailSnapshot().complete {
		t.Fatal("actual supported initial interval premise")
	}
	frame := app.begin()
	for _, captures := range []any{42, []any{42}, make([]any, spaViewMemoMaxCaptures+1)} {
		result := SpaViewMemoRegion(frame, 17, captures, func() any { builds++; return "fallback" })
		if result != "fallback" {
			t.Fatal("malformed capture changed original fallback result")
		}
	}
	app.seal(frame)
	app.finish(frame, true)
	if builds != 4 || paints != 1 {
		t.Fatal("malformed capture replayed/skipped builder")
	}
	if s := spaProfileDetailSnapshot(); !s.unsupported || s.complete {
		t.Fatal("malformed capture reused a certified old observation")
	}
}
