package rt

import "testing"

func tuiReqStub() any {
	return map[string]any{
		"Init":          "init-fn",
		"Update":        "update-fn",
		"View":          "view-fn",
		"Subscriptions": "subs-fn",
	}
}

// Guard 1: unset Tui optionals read as untyped nil.
func TestTuiConfigUnsetOptionalIsNil(t *testing.T) {
	cfg := Tui_config(tuiReqStub())
	for _, k := range []string{"OnKey", "Guard", "CanvasWidth", "CanvasHeight"} {
		if v := Field(cfg, k); v != nil {
			t.Fatalf("unset Tui optional %q must read nil, got %#v", k, v)
		}
	}
	for _, k := range []string{"Init", "Update", "View", "Subscriptions"} {
		if Field(cfg, k) == nil {
			t.Fatalf("required Tui field %q must be present", k)
		}
	}
}

// Guard 3: sibling isolation via shallow clone.
func TestTuiConfigSiblingIsolation(t *testing.T) {
	base := Tui_config(tuiReqStub())
	c1 := Tui_withOnKey("onkey-cb", base)
	c2 := Tui_withCanvasWidth(1920, base)
	if Field(base, "OnKey") != nil || Field(base, "CanvasWidth") != nil {
		t.Fatal("guard 3 violated: base Tui config mutated by a withX derivation")
	}
	if Field(c1, "OnKey") == nil || Field(c1, "CanvasWidth") != nil {
		t.Fatal("c1 must carry OnKey and NOT CanvasWidth")
	}
	if Field(c2, "CanvasWidth") == nil || Field(c2, "OnKey") != nil {
		t.Fatal("c2 must carry CanvasWidth and NOT OnKey")
	}
}

// Guard 2: withX stores the callback verbatim.
func TestTuiWithStoresValueVerbatim(t *testing.T) {
	type sentinel struct{ tag string }
	cb := &sentinel{tag: "verbatim"}
	cfg := Tui_withOnKey(cb, Tui_config(tuiReqStub()))
	if got := Field(cfg, "OnKey"); got != any(cb) {
		t.Fatalf("guard 2 violated: value not stored verbatim, got %#v", got)
	}
}
