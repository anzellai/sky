package rt

// Regression test for #544 — String.dropLeft + String.dropRight
// were used in v0.17.2 SkyDeploy control-plane code but not
// registered as kernels (and therefore no runtime fn existed).
// Closing the gap: add both kernels with Elm-shaped semantics —
// rune-based, negative n returns s unchanged, n >= length returns "".

import "testing"

func TestString_dropLeft(t *testing.T) {
	tests := []struct {
		name string
		n    any
		s    any
		want string
	}{
		{"zero", 0, "hello", "hello"},
		{"normal", 2, "hello", "llo"},
		{"all", 5, "hello", ""},
		{"past end", 10, "hello", ""},
		{"negative", -1, "hello", "hello"},
		{"empty", 3, "", ""},
		{"rune boundary", 2, "héllo", "llo"},
		{"emoji", 1, "★hi", "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String_dropLeft(tt.n, tt.s).(string)
			if got != tt.want {
				t.Errorf("String_dropLeft(%v, %q) = %q, want %q", tt.n, tt.s, got, tt.want)
			}
		})
	}
}

func TestString_dropRight(t *testing.T) {
	tests := []struct {
		name string
		n    any
		s    any
		want string
	}{
		{"zero", 0, "hello", "hello"},
		{"normal", 2, "hello", "hel"},
		{"all", 5, "hello", ""},
		{"past end", 10, "hello", ""},
		{"negative", -1, "hello", "hello"},
		{"empty", 3, "", ""},
		{"rune boundary", 2, "héllo", "hél"},
		{"emoji", 1, "hi★", "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String_dropRight(tt.n, tt.s).(string)
			if got != tt.want {
				t.Errorf("String_dropRight(%v, %q) = %q, want %q", tt.n, tt.s, got, tt.want)
			}
		})
	}
}
