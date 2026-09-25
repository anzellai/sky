package rt

import (
	"reflect"
	"testing"
)

func collect(xs []any) any { return append([]any(nil), xs...) }

func applyAll(f any, args ...any) any {
	for _, a := range args {
		f = f.(func(any) any)(a)
	}
	return f
}

func TestCurryNAppliesOneArgumentPerCall(t *testing.T) {
	got := applyAll(CurryN(4, collect), 1, "b", 3.0, true)
	want := []any{1, "b", 3.0, true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	// Each step is the boxed-closure shape the runtime appliers fast-path.
	step := CurryN(3, collect)(1)
	if _, ok := step.(func(any) any); !ok {
		t.Fatalf("a partial application is %T, want func(any) any", step)
	}
}

func TestCurryNPartialApplicationsAreIndependent(t *testing.T) {
	base := CurryN(3, collect)("a").(func(any) any)
	left := base("l").(func(any) any)
	right := base("r").(func(any) any)
	if got := left("1"); !reflect.DeepEqual(got, []any{"a", "l", "1"}) {
		t.Fatalf("left branch saw %#v", got)
	}
	if got := right("2"); !reflect.DeepEqual(got, []any{"a", "r", "2"}) {
		t.Fatalf("right branch saw %#v", got)
	}
	// Re-applying a finished prefix again gives a fresh result each time.
	if got := left("3"); !reflect.DeepEqual(got, []any{"a", "l", "3"}) {
		t.Fatalf("re-applied left branch saw %#v", got)
	}
}

func TestCurryNThroughSkyCall(t *testing.T) {
	// The runtime's generic applier walks the curried form one argument at a
	// time, so a flat-curried constructor applies exactly like the nest did.
	f := CurryN(3, func(xs []any) any { return xs[0].(int) + xs[1].(int)*10 + xs[2].(int)*100 })
	if got := SkyCall(f, 1, 2, 3); got != 321 {
		t.Fatalf("SkyCall(CurryN) = %#v, want 321", got)
	}
}

func TestCurryNGuardsANonPositiveArity(t *testing.T) {
	if got := CurryN(0, collect)("x"); !reflect.DeepEqual(got, []any{"x"}) {
		t.Fatalf("got %#v", got)
	}
}
