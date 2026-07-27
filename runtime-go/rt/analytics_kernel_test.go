package rt

import (
	"bytes"
	"strings"
	"testing"
)

func TestAnalyticsSnakeCase(t *testing.T) {
	cases := map[string]string{
		"ProductViewed": "product_viewed",
		"OrderId":       "order_id",
		"id":            "id",
		"AppOpened":     "app_opened",
		"Purchased":     "purchased",
	}
	for in, want := range cases {
		if got := analyticsSnakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnalyticsRenderGoValue_RedactsPiiAndKeepsScalars(t *testing.T) {
	// A Pii value renders to its KIND, never its value.
	pii := SkyADT{SkyName: "PiiEmail", Fields: []any{"secret@example.com"}}
	if got := analyticsRenderGoValue(pii); got != "<pii:email>" {
		t.Errorf("pii render = %v, want <pii:email>", got)
	}
	// A plain string is NOT treated as PII.
	if got := analyticsRenderGoValue("hello"); got != "hello" {
		t.Errorf("string render = %v, want hello", got)
	}
	if got := analyticsRenderGoValue(int64(7)); got != int64(7) {
		t.Errorf("int render = %v, want 7", got)
	}
	if got := analyticsRenderGoValue(true); got != true {
		t.Errorf("bool render = %v, want true", got)
	}
}

// TestAnalyticsTrackEvent_DerivesTypedProps — the reflective-derive path: an
// app event value (constructor + record payload) becomes a snake_case event
// name + typed props, with PII redacted and the plain field kept verbatim.
func TestAnalyticsTrackEvent_DerivesTypedProps(t *testing.T) {
	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	// ProductViewed { id : String, qty : Int, email : Pii } — as the SkyADT
	// representation (a record payload in Fields[0]).
	ev := SkyADT{Tag: 0, SkyName: "ProductViewed", Fields: []any{
		map[string]any{
			"id":    "SKU-42",
			"qty":   int64(2),
			"email": SkyADT{SkyName: "PiiEmail", Fields: []any{"alice@example.com"}},
		},
	}}
	anyTaskInvoke(Analytics_trackEvent(ev))

	out := buf.String()
	if !strings.Contains(out, `"event":"product_viewed"`) {
		t.Errorf("constructor not snake_cased into event name: %s", out)
	}
	if !strings.Contains(out, `"id":"SKU-42"`) || !strings.Contains(out, `"qty":2`) {
		t.Errorf("record fields not derived into props: %s", out)
	}
	if !strings.Contains(out, `"email":"<pii:email>"`) {
		t.Errorf("Pii field not redacted: %s", out)
	}
	if strings.Contains(out, "alice@example.com") {
		t.Errorf("PII VALUE LEAKED into the sink: %s", out)
	}
}

// TestAnalyticsTrackEvent_NullaryVariant — a nullary variant emits the name
// with empty props.
func TestAnalyticsTrackEvent_NullaryVariant(t *testing.T) {
	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	anyTaskInvoke(Analytics_trackEvent(SkyADT{Tag: 0, SkyName: "AppOpened"}))

	out := buf.String()
	if !strings.Contains(out, `"event":"app_opened"`) {
		t.Errorf("nullary event name wrong: %s", out)
	}
	if !strings.Contains(out, `"props":{}`) {
		t.Errorf("nullary variant should have empty props: %s", out)
	}
}
