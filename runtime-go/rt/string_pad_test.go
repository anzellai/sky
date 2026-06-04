package rt

// Regression test for #462 — String.padLeft / padRight emitted decimal
// codepoint instead of the character (because fmt.Sprintf("%v", rune)
// renders a rune as its Int representation). padChar now type-switches
// on rune / int / string and renders correctly.

import "testing"

func TestPadChar_Rune(t *testing.T) {
	tests := []struct {
		name string
		ch   any
		want string
	}{
		{"space rune", rune(' '), " "},
		{"asterisk rune", rune('*'), "*"},
		{"zero rune", rune('0'), "0"},
		{"unicode rune", rune('★'), "★"},
		{"int from Char.toCode", 32, " "},
		{"string passthrough", "·", "·"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padChar(tt.ch)
			if got != tt.want {
				t.Errorf("padChar(%v) = %q, want %q", tt.ch, got, tt.want)
			}
		})
	}
}

func TestStringPadLeft_NonDigitChar(t *testing.T) {
	tests := []struct {
		name string
		n    any
		ch   any
		s    any
		want string
	}{
		{"space-pad to width 5", 5, rune(' '), "X", "    X"},
		{"asterisk-pad to width 3", 3, rune('*'), "ab", "*ab"},
		{"already-wider stays as-is", 2, rune('*'), "abcd", "abcd"},
		{"zero-pad numeric (digit char)", 4, rune('0'), "5", "0005"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String_padLeft(tt.n, tt.ch, tt.s).(string)
			if got != tt.want {
				t.Errorf("String_padLeft = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStringPadRight_NonDigitChar(t *testing.T) {
	got := String_padRight(4, rune('-'), "x").(string)
	if got != "x---" {
		t.Errorf("String_padRight = %q, want %q", got, "x---")
	}
}
