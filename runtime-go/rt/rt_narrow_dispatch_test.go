package rt

// Regression: List_foldlAnyT (and any other reflection-based
// higher-order kernel) invokes user reducers via reflect.Call. When
// the reducer is annotated with a concrete Go-typed param like
// map[string]string or []string and the list element runtime value
// is the any-shaped equivalent (map[string]interface{} / []interface{}),
// reflect panics with "Call using X as Y". Typed codegen inserts
// rt.AsListT/rt.AsMapT coercions at direct call sites, but the
// reflection kernel path didn't — so List.foldl over an annotated
// DB row slice blew up before this fix.
//
// skyCallDirect now falls back to narrowReflectValue when the exact
// type / interface / convertible cases all miss, letting reflection
// dispatch absorb the same structural narrowing the direct path
// already does.

import (
	"testing"
)

func TestSkyCallDirect_NarrowsMapAnyToMapString(t *testing.T) {
	// Reducer-like shape: takes a typed map, returns anything.
	fn := func(row map[string]string) string {
		return row["name"]
	}
	// Wire-runtime value is map[string]interface{} (what Db.query
	// actually returns) — each value stringify-compatible.
	arg := map[string]interface{}{"name": "Alice", "role": "guardian"}
	got := SkyCall(fn, arg)
	if s, ok := got.(string); !ok || s != "Alice" {
		t.Errorf("expected \"Alice\", got %T %v", got, got)
	}
}

func TestSkyCallDirect_NarrowsSliceAnyToSliceString(t *testing.T) {
	fn := func(items []string) int {
		return len(items)
	}
	arg := []interface{}{"a", "b", "c"}
	got := SkyCall(fn, arg)
	if n, ok := got.(int); !ok || n != 3 {
		t.Errorf("expected 3, got %T %v", got, got)
	}
}

func TestSkyCallDirect_NarrowsNestedSliceOfMaps(t *testing.T) {
	// The sendcrafts / 08-notes-app shape: DB returns []any where
	// each element is map[string]any, but the reducer wants
	// []map[string]string (each row narrowed). Whole-value narrow
	// walks nested containers.
	fn := func(rows []map[string]string) string {
		if len(rows) == 0 {
			return "empty"
		}
		return rows[0]["status"]
	}
	arg := []interface{}{
		map[string]interface{}{"status": "pending", "user": "alice"},
		map[string]interface{}{"status": "verified", "user": "bob"},
	}
	got := SkyCall(fn, arg)
	if s, ok := got.(string); !ok || s != "pending" {
		t.Errorf("expected \"pending\", got %T %v", got, got)
	}
}

func TestSkyCallDirect_StillPanicsOnGenuineMismatch(t *testing.T) {
	// When the types genuinely can't be narrowed (e.g. bool → string,
	// the radio-onInput case), skyCallDirect still panics. The dispatch
	// layer's defer/recover catches it and drops the event.
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic for bool → string mismatch; none occurred")
		}
	}()
	fn := func(s string) string { return s }
	_ = SkyCall(fn, true)
}
