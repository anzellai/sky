//go:build js

package rt

import (
	"syscall/js"
	"testing"
)

func TestSpaProfileDetailActualWasmPublication(t *testing.T) {
	g := js.Global()
	oldEnabled, oldReference, oldProfile := g.Get("__skyStepProfileEnabled"), g.Get("__skyStepReference"), g.Get("__skyStepProfile")
	oldProfiling, oldRef, oldSteps, oldTotals := spaStepProfiling, spaStepReference, spaProfileSteps, spaProfileTotals
	t.Cleanup(func() {
		g.Set("__skyStepProfileEnabled", oldEnabled)
		g.Set("__skyStepReference", oldReference)
		g.Set("__skyStepProfile", oldProfile)
		spaStepProfiling, spaStepReference, spaProfileSteps, spaProfileTotals = oldProfiling, oldRef, oldSteps, oldTotals
		spaProfileDetailStart(false)
	})
	g.Set("__skyStepProfileEnabled", true)
	g.Set("__skyStepReference", false)
	spaProfileInit()
	builds, paints := 0, 0
	rows := []int{1, 2, 3}
	app := spaDetailTestApp(&builds, &paints)
	spaTransition(spaDetailTestMsg{}, rows, func(_, m any) SkyTuple2 { return SkyTuple2{V0: m} }, func(m any) { app.render(m, false, spaDetailTestPaint(&paints)) }, func(any) {}, func(string, any) { t.Fatal("observer affected transition") })
	app.render(rows, false, spaDetailTestPaint(&paints))
	metrics := spaStepMetrics{}
	metrics.Calls[0] = 7
	metrics.ViewMemo.Hits = 5
	spaProfilePublish(&metrics)
	profile := g.Get("__skyStepProfile")
	detail := profile.Get("detail")
	site := detail.Get("sites").Get("17")
	if builds != 1 || paints != 2 {
		t.Fatal("actual wasm semantics changed")
	}
	if profile.Get("steps").Int() != 1 || profile.Get("updateCalls").Int() != 7 || profile.Get("viewMemoHits").Int() != 5 {
		t.Fatal("existing aggregate changed")
	}
	if detail.Get("version").Int() != 1 || !detail.Get("complete").Bool() || detail.Get("messages").Get(spaDetailTestKey).Int() != 1 {
		t.Fatal("actual wasm root dispatch publication missing")
	}
	if site.Get("calls").Int() != 2 || site.Get("hits").Int() != 1 || site.Get("committedEntries").Int() != 1 || site.Get("lastInputRows").Int() != 3 || !site.Get("lastInputRowsKnown").Bool() {
		t.Fatal("actual wasm site publication missing")
	}
	spaProfileDetailResetCounters()
	spaProfilePublish(&spaStepMetrics{})
	resetSite := g.Get("__skyStepProfile").Get("detail").Get("sites").Get("17")
	if resetSite.Get("committedEntries").Int() != 1 || resetSite.Get("lastInputRows").Type() != js.TypeNull {
		t.Fatal("wasm reset lost live gauge/null policy")
	}
}
