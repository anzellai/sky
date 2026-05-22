package rt

import (
	"reflect"
	"testing"
)

// Regression: Int-declared Time kernels must box a Go `int`, never an
// `int64`. An int64 trips the strict path in coerceInner when the
// result reaches a typed SkyResult / SkyTask boundary — e.g.
// `Time.unixMillis () |> Task.andThen f` panicked before this fix
// because Time_unixMillis boxed `time.Now().UnixMilli()` (int64)
// straight into Ok[any,any]. Every Int-returning Time kernel now
// narrows with int(...). This test is the discovery artefact for the
// class — a new kernel that reintroduces an un-narrowed int64 fails
// here.
func TestTimeKernelsBoxInt(t *testing.T) {
	// Task-shaped — Time_now / Time_unixMillis return a thunk that,
	// when forced, yields Ok[any,any](payload).
	for name, fn := range map[string]func(any) any{
		"Time_now":        Time_now,
		"Time_unixMillis": Time_unixMillis,
	} {
		thunk, ok := fn(nil).(func() any)
		if !ok {
			t.Fatalf("%s did not return a task thunk", name)
		}
		res := thunk().(SkyResult[any, any])
		if k := reflect.TypeOf(res.OkValue).Kind(); k != reflect.Int {
			t.Errorf("%s boxes %v, want int", name, k)
		}
	}

	// Result-shaped — Time_parseISO8601 / Time_parse return Ok directly.
	iso := Time_parseISO8601("2026-05-22T12:00:00Z").(SkyResult[any, any])
	if iso.Tag != 0 {
		t.Fatalf("Time_parseISO8601 failed: %v", iso.ErrValue)
	}
	if k := reflect.TypeOf(iso.OkValue).Kind(); k != reflect.Int {
		t.Errorf("Time_parseISO8601 boxes %v, want int", k)
	}

	pr := Time_parse("2006-01-02", "2026-05-22").(SkyResult[any, any])
	if pr.Tag != 0 {
		t.Fatalf("Time_parse failed: %v", pr.ErrValue)
	}
	if k := reflect.TypeOf(pr.OkValue).Kind(); k != reflect.Int {
		t.Errorf("Time_parse boxes %v, want int", k)
	}
}

// coerceInner must bridge numeric widths rather than panicking — the
// defence-in-depth net beneath the kernel fix above.
func TestCoerceInnerNumericWidth(t *testing.T) {
	if got := coerceInner[int](int64(1747900800000)); got != 1747900800000 {
		t.Errorf("coerceInner[int](int64) = %d", got)
	}
	if got := coerceInner[int64](int(42)); got != 42 {
		t.Errorf("coerceInner[int64](int) = %d", got)
	}
	if got := coerceInner[float64](int(7)); got != 7.0 {
		t.Errorf("coerceInner[float64](int) = %v", got)
	}
}
