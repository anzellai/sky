package rt

import (
	"fmt"
	"testing"
)

// Regression: structured-log attrs must survive as fields regardless of the
// concrete Go slice type Sky lowers the list to. A homogeneous Sky list
// (`[ "k", "v" ]`, all strings) lowers to []string; a mixed list to []any.
// The pre-fix code only handled []any, so the common all-string case silently
// dropped every field (observed in prod: `{"msg":"move.img_found"}` with no
// attrs). logAttrsToMap must pair both shapes into the field map.
func TestLogAttrsToMap(t *testing.T) {
	cases := []struct {
		name  string
		attrs any
		want  map[string]any
	}{
		{"homogeneous []string", []string{"imgId", "abc", "dir", "-1"},
			map[string]any{"imgId": "abc", "dir": "-1"}},
		{"mixed []any", []any{"count", 3, "ok", true},
			map[string]any{"count": 3, "ok": true}},
		{"already a map", map[string]any{"a": "b"},
			map[string]any{"a": "b"}},
		{"empty slice", []string{}, nil},
		{"nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logAttrsToMap(c.attrs)
			if len(got) != len(c.want) {
				t.Fatalf("len mismatch: got %v want %v", got, c.want)
			}
			for k, wv := range c.want {
				gv, ok := got[k]
				if !ok {
					t.Fatalf("missing key %q in %v", k, got)
				}
				// stringify to compare across any/string/int
				if got, want := toStr(gv), toStr(wv); got != want {
					t.Errorf("key %q: got %q want %q", k, got, want)
				}
			}
		})
	}
}

// Odd trailing key gets an empty value (never dropped, never panics).
func TestLogAttrsToMapOddTrailing(t *testing.T) {
	got := logAttrsToMap([]string{"k1", "v1", "danglingKey"})
	if got["k1"] != "v1" {
		t.Errorf("k1: got %v want v1", got["k1"])
	}
	if v, ok := got["danglingKey"]; !ok || toStr(v) != "" {
		t.Errorf("danglingKey: got %v ok=%v want empty", v, ok)
	}
}

func toStr(v any) string {
	return fmt.Sprintf("%v", v)
}
