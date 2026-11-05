package rt

import (
	"reflect"
	"testing"
)

func TestSpaViewMemoAppLifecycleKeepsSourceAndPaintCurrent(t *testing.T) {
	rows := []int{1, 2}
	sources, builds, paints := 0, 0, 0
	app := newSpaViewMemoApp(SpaFns{View: func(any) any { t.Fatal("memo path called reference view"); return nil }, ViewMemo: func(model, ctx any) any {
		sources++
		return SpaViewMemoRegion(ctx, 0, []any{SpaViewMemoCapture[[]int](model.([]int))}, func() any { builds++; return []string{"source"} })
	}})
	for i := 0; i < 2; i++ {
		app.render(rows, false, func(value any) {
			paints++
			if !reflect.DeepEqual(value, []string{"source"}) {
				t.Fatal("wrong source value")
			}
		})
	}
	if sources != 2 || paints != 2 || builds != 1 {
		t.Fatalf("source=%d paint=%d build=%d", sources, paints, builds)
	}
	// A separate boot/app has no shared cache, even with identical captures.
	second := newSpaViewMemoApp(SpaFns{ViewMemo: app.memo})
	second.render(rows, false, func(any) {})
	if builds != 2 {
		t.Fatal("separate app reused another app's entry")
	}
}

func TestSpaViewMemoAppPaintFailureAndResetDropOldOwners(t *testing.T) {
	rows := []int{1}
	builds := 0
	var leaked any
	app := newSpaViewMemoApp(SpaFns{ViewMemo: func(model, ctx any) any {
		leaked = ctx
		return SpaViewMemoRegion(ctx, 0, []any{SpaViewMemoCapture[[]int](model.([]int))}, func() any { builds++; return []string{"row"} })
	}})
	app.render(rows, false, func(any) {})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("paint failure swallowed")
			}
		}()
		app.render(rows, false, func(any) { panic("paint failure") })
	}()
	if len(app.cache.current) != 0 {
		t.Fatal("paint failure retained old entries")
	}
	app.render(rows, false, func(any) {
		// Source has returned: leaked contexts cannot skip work during DOM paint.
		before := builds
		SpaViewMemoRegion(leaked, 0, []any{SpaViewMemoCapture[[]int](rows)}, func() any { builds++; return nil })
		if builds != before+1 {
			t.Fatal("source frame remained open during paint")
		}
		app.reset()
	})
	if len(app.cache.current) != 0 {
		t.Fatal("reset permitted stale frame publication")
	}
	before := builds
	app.render(rows, false, func(any) {})
	if builds != before+1 {
		t.Fatal("reset retained stale result")
	}
}

func TestSpaViewMemoAppSourcePanicAndReferenceFallback(t *testing.T) {
	builds, references := 0, 0
	fail := false
	app := newSpaViewMemoApp(SpaFns{View: func(any) any { references++; return "reference" }, ViewMemo: func(_ any, ctx any) any {
		if fail {
			panic("source failure")
		}
		return SpaViewMemoRegion(ctx, 0, []any{}, func() any { builds++; return "memo" })
	}})
	app.render(nil, false, func(any) {})
	fail = true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("source failure swallowed")
			}
		}()
		app.render(nil, false, func(any) { t.Fatal("paint ran after source failure") })
	}()
	if len(app.cache.current) != 0 {
		t.Fatal("source failure retained old cache")
	}
	app.render(nil, true, func(value any) {
		if value != "reference" {
			t.Fatal("reference mode selected memo")
		}
	})
	if references != 1 || builds != 1 {
		t.Fatal("reference mode evaluated memo source")
	}
	plain := newSpaViewMemoApp(SpaFns{View: func(any) any { return "plain" }})
	plain.render(nil, false, func(value any) {
		if value != "plain" {
			t.Fatal("missing capability did not use reference")
		}
	})
}

type spaViewMemoForeignNamedList []int

func TestSpaViewMemoForeignNamedContainersHaveNoImmutableWitness(t *testing.T) {
	typ := reflect.TypeOf(spaViewMemoForeignNamedList{})
	if spaViewMemoClosedType(typ, nil, map[reflect.Type]uint8{}, 0) {
		t.Fatal("foreign named list received closed type proof")
	}
	cache := &spaViewMemoCache{}
	frame := cache.begin(nil)
	if frame.dataOnly(reflect.ValueOf(spaViewMemoForeignNamedList{1}), 0) {
		t.Fatal("foreign named result admitted")
	}
	frame.finish(false)
}
