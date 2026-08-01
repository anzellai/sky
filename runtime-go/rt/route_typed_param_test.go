package rt

import "testing"

// L7 regression — a route Page ctor with a typed param (AppDetailPage Int) must
// not panic in reflect. Route params are captured as strings; pre-fix
// fillRoutePage passed the raw string → "reflect: Call using string as type
// int". Now the string is coerced to the ctor's param type, and any residual
// mismatch degrades (recover) instead of crashing the request.
func TestRouteTypedParamCoerced(t *testing.T) {
	ctor := func(id int) any { return map[string]any{"page": "detail", "id": id} }

	page := fillRoutePage(ctor, []string{"42"})
	m, ok := page.(map[string]any)
	if !ok {
		t.Fatalf("expected ctor called with a coerced int, got %T (%v)", page, page)
	}
	if m["id"] != 42 {
		t.Fatalf("route param should coerce to int 42, got %v", m["id"])
	}

	// A non-numeric value for an Int ctor must NOT panic — degrades gracefully.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("non-numeric typed route param must not panic, got: %v", r)
			}
		}()
		_ = fillRoutePage(ctor, []string{"abc"})
	}()
}
