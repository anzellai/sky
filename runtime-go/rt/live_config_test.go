package rt

import "testing"

// liveReqStub is the required-fields record `Live.config` receives. A map
// is a valid stand-in because rt.Field reads a map by exact key exactly as
// it reads the lowered Sky-record struct.
func liveReqStub() any {
	return map[string]any{
		"Init":          "init-fn",
		"Update":        "update-fn",
		"View":          "view-fn",
		"Subscriptions": "subs-fn",
		"Routes":        []any{},
		"NotFound":      "nf",
	}
}

// Guard 1: an unset optional is ABSENT, so rt.Field returns untyped nil and
// liveAppRun's `if X != nil` gates stay false. A regression here (e.g.
// pre-seeding optionals to a typed-nil) would fire an optional gate on a
// nil callback and crash Live_app.
func TestLiveConfigUnsetOptionalIsNil(t *testing.T) {
	cfg := Live_config(liveReqStub())
	for _, k := range []string{"Head", "ConsoleAuth", "OnNavigate", "Guard", "Static", "StaticUrl", "Port", "Store", "StorePath", "Ttl", "Analytics", "Status"} {
		if v := Field(cfg, k); v != nil {
			t.Fatalf("unset optional %q must read as nil, got %#v", k, v)
		}
	}
	for _, k := range []string{"Init", "Update", "View", "Subscriptions", "Routes", "NotFound"} {
		if Field(cfg, k) == nil {
			t.Fatalf("required field %q must be present after Live_config", k)
		}
	}
}

// Guard 3: liveCfgSet shallow-clones, so deriving two siblings from one base
// never aliases the base map.
func TestLiveConfigSiblingIsolation(t *testing.T) {
	base := Live_config(liveReqStub())
	c1 := Live_withHead("head-cb", base)
	c2 := Live_withPort(9000, base)

	if Field(base, "Head") != nil || Field(base, "Port") != nil {
		t.Fatal("guard 3 violated: base config was mutated by a withX derivation")
	}
	if Field(c1, "Head") == nil || Field(c1, "Port") != nil {
		t.Fatal("c1 must carry Head and NOT Port")
	}
	if Field(c2, "Port") == nil || Field(c2, "Head") != nil {
		t.Fatal("c2 must carry Port and NOT Head")
	}
}

// Guard 2: withX stores the callback verbatim (never asserts it to a Go
// func type). A non-func sentinel round-trips unchanged — proving no
// `.(func(any)any)` assertion mangles it.
func TestLiveWithStoresValueVerbatim(t *testing.T) {
	type sentinel struct{ tag string }
	cb := &sentinel{tag: "verbatim"}
	cfg := Live_withHead(cb, Live_config(liveReqStub()))
	if got := Field(cfg, "Head"); got != any(cb) {
		t.Fatalf("guard 2 violated: value not stored verbatim, got %#v", got)
	}
}

// Sub-record optionals (guard 4) are stored Field-readable, the way
// liveAppRun reads Analytics.PageViews / Status.Reconnecting.
func TestLiveWithAnalyticsSubRecordReadable(t *testing.T) {
	analytics := map[string]any{"PageViews": true}
	cfg := Live_withAnalytics(analytics, Live_config(liveReqStub()))
	a := Field(cfg, "Analytics")
	if a == nil {
		t.Fatal("Analytics sub-record must be present")
	}
	if pv := Field(a, "PageViews"); pv != true {
		t.Fatalf("Analytics.PageViews must read back true, got %#v", pv)
	}
}

// `[live] input` must actually reach the JS driver.
//
// It did not. handleConfig served a hardcoded `"inputMode": "debounce"` with
// the comment `// or "blur"` — naming an alternative the code gave no way to
// select — while `examples/19-skyforum` and `examples/37-composite-live-shop`
// both carried `[live] input = "debounce"` in their sky.toml. The compiler
// parsed no such key, so the setting existed on both sides of the seam and was
// connected at neither.
func TestLiveInputModeIsConfigurable(t *testing.T) {
	t.Setenv("SKY_LIVE_INPUT_MODE", "")
	if got := liveInputMode(); got != "debounce" {
		t.Fatalf("unset must default to debounce, got %q", got)
	}

	// The alternative the runtime named but could not serve.
	t.Setenv("SKY_LIVE_INPUT_MODE", "blur")
	if got := liveInputMode(); got != "blur" {
		t.Fatalf(`SKY_LIVE_INPUT_MODE=blur must select blur, got %q — if this `+
			`reads "debounce" the key is inert again`, got)
	}

	// A value the client cannot honour falls back rather than being served
	// through: the driver would ignore it and the operator would believe the
	// setting had taken.
	t.Setenv("SKY_LIVE_INPUT_MODE", "onKeystroke")
	if got := liveInputMode(); got != "debounce" {
		t.Fatalf("unrecognised mode must fall back to debounce, got %q", got)
	}
}
