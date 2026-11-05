package rt

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

type spaViewMemoNamedFloat32 float32

type spaViewMemoTestForeign struct{ Value int }

func (spaViewMemoTestForeign) SkyVariantName() string { panic("foreign method") }
func (spaViewMemoTestForeign) SkyVariantTag() int     { panic("foreign method") }

type spaViewMemoTestCertified struct{ Value string }

func (spaViewMemoTestCertified) SkyVariantName() string { return "Known" }
func (spaViewMemoTestCertified) SkyVariantTag() int     { return 0 }

func spaViewMemoTestDeps(values ...any) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = SpaViewMemoCapture(v)
	}
	return out
}
func spaViewMemoTestCapture(value any) any {
	v, ok := spaViewMemoCaptured(value)
	if !ok {
		panic("invalid test capture")
	}
	return v.Interface()
}

func TestSpaViewMemoPhysicalWitnessNeverUsesJSONEquality(t *testing.T) {
	same := func(a, b any) bool {
		budget := spaViewMemoWitnessBudget
		av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
		if v, ok := spaViewMemoCaptured(a); ok {
			av = v
		}
		if v, ok := spaViewMemoCaptured(b); ok {
			bv = v
		}
		return spaViewMemoPhysicalSame(av, bv, &budget)
	}
	base := make([]int, 4, 9)
	if same(base[:2:2], base[:2:3]) || same([]int(nil), []int{}) {
		t.Fatal("slice cap/nil representation ignored")
	}
	a := SkyMaybe[string]{Tag: 1, JustValue: "old"}
	b := a
	b.JustValue = "new"
	if same(a, b) {
		t.Fatal("inactive Maybe fields were ignored")
	}
	x, y := strings.Clone("same-value"), strings.Clone("same-value")
	if unsafe.StringData(x) == unsafe.StringData(y) {
		t.Fatal("fixture must have distinct string owners")
	}
	if same(x, y) || same(SpaViewMemoCapture(x[:0]), SpaViewMemoCapture(y[:0])) {
		t.Fatal("string owner lost, including empty substring")
	}
	if !same(x, x) || !same(base, base) {
		t.Fatal("same physical immutable input rejected")
	}
	if same(0.0, math.Copysign(0, -1)) || !same(math.Float64frombits(0x7ff8000000000001), math.Float64frombits(0x7ff8000000000001)) {
		t.Fatal("float bits not preserved")
	}
	if same(func() {}, func() {}) {
		t.Fatal("function pointer accepted")
	}
	a32, b32 := math.Float32frombits(0x7f800001), math.Float32frombits(0x7fc00001)
	if same(SpaViewMemoCapture(a32), SpaViewMemoCapture(b32)) || !same(SpaViewMemoCapture(a32), SpaViewMemoCapture(a32)) {
		t.Fatal("float32 signaling/quiet NaN payload bits collapsed")
	}
	if same(SpaViewMemoCapture(spaViewMemoNamedFloat32(a32)), SpaViewMemoCapture(spaViewMemoNamedFloat32(b32))) {
		t.Fatal("named float32 signaling/quiet NaN payload bits collapsed")
	}

}

func TestSpaViewMemoExplicitContextLifecycleCapturesAndRepeatedVisits(t *testing.T) {
	var c spaViewMemoCache
	list := []int{1, 2, 3}
	builds := 0
	run := func(f *spaViewMemoFrame, capture int) any {
		return SpaViewMemoRegion(f, 7, spaViewMemoTestDeps(capture, list), func() any { builds++; return SkyADT{SkyName: "Text", Fields: []any{capture}} })
	}
	f := c.begin(nil)
	run(f, 1)
	run(f, 2)
	f.seal()
	f.finish(true)
	if builds != 2 {
		t.Fatal("first repeated site did not build")
	}
	f = c.begin(nil)
	run(f, 1)
	run(f, 2)
	f.seal()
	f.finish(true)
	if builds != 2 || f.hits != 2 {
		t.Fatal("fresh scaffolding bag or repeated visit lost reuse")
	}
	f = c.begin(nil)
	run(f, 2)
	run(f, 1)
	f.seal()
	f.finish(true)
	if builds != 4 {
		t.Fatal("reordered visit captures reused stale output")
	}
	// The last frame visits one slot; a subsequent two-slot frame must rebuild
	// its second visit. Unmounted/trailing entries are not retained.
	f = c.begin(nil)
	run(f, 2)
	f.seal()
	f.finish(true)
	f = c.begin(nil)
	run(f, 2)
	run(f, 1)
	f.seal()
	f.finish(true)
	if builds != 5 || len(c.current) != 2 {
		t.Fatal("unvisited trailing entry retained")
	}
	sealed := f
	run(sealed, 2)
	if builds != 6 {
		t.Fatal("sealed explicit context reused a prior frame")
	}
	f = c.begin(nil)
	run(f, 2)
	f.seal()
	f.finish(false)
	if c.current != nil {
		t.Fatal("abort retained old model captures")
	}
	f = c.begin(nil)
	run(f, 2)
	f.seal()
	c.reset()
	f.finish(true)
	if c.current != nil {
		t.Fatal("sign-out/reset allowed stale publication")
	}
	for _, ctx := range []any{nil, struct{}{}, (*spaViewMemoFrame)(nil)} {
		before := builds
		SpaViewMemoRegion(ctx, 7, spaViewMemoTestDeps(1), func() any { builds++; return 1 })
		if builds != before+1 {
			t.Fatal("unknown ctx skipped build")
		}
	}
}

func TestSpaViewMemoRejectsMutableOrCallableResultsWithoutCallingMethods(t *testing.T) {
	for _, bad := range []any{map[string]string{"x": "y"}, new(int), func() {}, spaViewMemoTestForeign{}, VNode{}, SkyADT{SkyName: "Raw", Fields: []any{map[string]int{"mutable": 1}}}, SkyADT{SkyName: "Photo", Fields: []any{func() {}}}} {
		var c spaViewMemoCache
		builds := 0
		for i := 0; i < 2; i++ {
			f := c.begin(nil)
			SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { builds++; return bad })
			f.seal()
			f.finish(true)
		}
		if builds != 2 || len(c.current) != 0 {
			t.Fatal("mutable/callable/foreign result was memoised")
		}
	}
	var c spaViewMemoCache
	builds := 0
	for i := 0; i < 2; i++ {
		f := c.begin([]any{SpaViewMemoType[spaViewMemoTestCertified]()})
		SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { builds++; return spaViewMemoTestCertified{Value: "known data"} })
		f.seal()
		f.finish(true)
	}
	if builds != 1 {
		t.Fatal("exact certified generated data rejected")
	}
	f := c.begin(nil)
	SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { builds++; return spaViewMemoTestCertified{Value: "known data"} })
	f.seal()
	f.finish(true)
	if builds != 2 || len(c.current) != 0 {
		t.Fatal("changed nominal grant reused an earlier certificate")
	}
}

func TestSpaViewMemoPanicBuildRunsOnceAndAbortsOldCache(t *testing.T) {
	var c spaViewMemoCache
	f := c.begin(nil)
	SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { return "old" })
	f.seal()
	f.finish(true)
	builds := 0
	func() {
		f = c.begin(nil)
		defer f.finish(false)
		defer func() {
			if recover() == nil {
				t.Fatal("build panic was hidden")
			}
		}()
		SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(2), func() any { builds++; panic("classified by surrounding application net") })
	}()
	if builds != 1 || c.current != nil || f.previous != nil || f.next != nil {
		t.Fatal("panic replayed work or retained obsolete captures")
	}
	f = c.begin(nil)
	SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(func() {}), func() any { return "data" })
	f.seal()
	f.finish(true)
	if len(c.current) != 0 {
		t.Fatal("function capture admitted")
	}
}

func TestSpaViewMemoSourceHtmlRawAndCurrentOwnerRetention(t *testing.T) {
	var c spaViewMemoCache
	list := []int{1}
	result := SkyADT{SkyName: "Raw", Fields: []any{SkyADT{SkyName: "HElement", Fields: []any{"img", []any{}, []any{}}}}}
	builds := 0
	deps := spaViewMemoTestDeps(list)
	f := c.begin(nil)
	SpaViewMemoRegion(f, 1, deps, func() any { builds++; return result })
	f.seal()
	f.finish(true)
	deps[0] = SpaViewMemoCapture([]int{9})
	f = c.begin(nil)
	SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(list), func() any { builds++; return result })
	f.seal()
	f.finish(true)
	if builds != 1 {
		t.Fatal("safe source Html Raw failed or dependency scaffolding was not owned")
	}
	if got := spaViewMemoTestCapture(c.current[spaViewMemoKey{1, 0}].captures[0]).([]int); len(got) != 1 || got[0] != 1 {
		t.Fatal("cache retained caller's mutable dependency bag")
	}
}

func TestSpaViewMemoBudgetIncludesHitsAndPrunesOverBudgetEntries(t *testing.T) {
	var c spaViewMemoCache
	f := c.begin(nil)
	large := strings.Repeat("x", spaViewMemoStringBudget/2+1)
	SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { return large })
	f.seal()
	f.finish(true)
	f = c.begin(nil)
	SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { t.Fatal("expected certified hit"); return nil })
	SpaViewMemoRegion(f, 2, spaViewMemoTestDeps(2), func() any { return large })
	f.seal()
	f.finish(true)
	if len(c.current) != 1 || f.stringsLeft < 0 {
		t.Fatal("hit did not consume shared retention admission budget")
	}
	f = c.begin(nil)
	f.valuesLeft = 1
	SpaViewMemoRegion(f, 3, spaViewMemoTestDeps(1), func() any { return make([]int, 1000) })
	f.seal()
	f.finish(true)
	if len(c.current) != 0 {
		t.Fatal("oversized result admitted")
	}
}

func TestSpaViewMemoReentryUsesExplicitFramesAndNoAmbientState(t *testing.T) {
	var a, b spaViewMemoCache
	fa, fb := a.begin(nil), b.begin(nil)
	SpaViewMemoRegion(fa, 1, spaViewMemoTestDeps(1), func() any {
		return SpaViewMemoRegion(fb, 1, spaViewMemoTestDeps(2), func() any { return "nested" })
	})
	fa.seal()
	fb.seal()
	fb.finish(true)
	fa.finish(true)
	if spaViewMemoTestCapture(a.current[spaViewMemoKey{1, 0}].captures[0]) != 1 || spaViewMemoTestCapture(b.current[spaViewMemoKey{1, 0}].captures[0]) != 2 {
		t.Fatal("explicit apps shared a frame")
	}
	old, newer := a.begin(nil), a.begin(nil)
	SpaViewMemoRegion(newer, 1, spaViewMemoTestDeps(3), func() any { return "new" })
	newer.seal()
	newer.finish(true)
	SpaViewMemoRegion(old, 1, spaViewMemoTestDeps(4), func() any { return "old" })
	old.seal()
	old.finish(true)
	if spaViewMemoTestCapture(a.current[spaViewMemoKey{1, 0}].captures[0]) != 3 {
		t.Fatal("older frame overwrote a newer commit")
	}
}

func TestSpaViewMemoResultDictCannotBypassValueOrStringBudgets(t *testing.T) {
	for _, oversizedStrings := range []bool{true, false} {
		var c spaViewMemoCache
		f := c.begin(nil)
		d := SkyDict[string]{}
		if oversizedStrings {
			d = d.Insert("key", strings.Repeat("x", spaViewMemoStringBudget+1))
		} else {
			for i := 0; i < 8; i++ {
				d = d.Insert(string(rune('a'+i)), "value")
			}
			f.valuesLeft = 8
		}
		SpaViewMemoRegion(f, 1, spaViewMemoTestDeps(1), func() any { return []SkyDict[string]{d} })
		f.seal()
		f.finish(true)
		if len(c.current) != 0 {
			t.Fatal("dictionary result bypassed shared admission budget")
		}
	}
}
