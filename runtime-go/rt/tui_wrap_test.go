package rt

import (
	"reflect"
	"testing"
)

func TestWrapText_Basic(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{
			name:  "single short line",
			text:  "hello",
			width: 10,
			want:  []string{"hello"},
		},
		{
			name:  "two words fit",
			text:  "hello world",
			width: 11,
			want:  []string{"hello world"},
		},
		{
			name:  "two words wrap",
			text:  "hello world",
			width: 6,
			want:  []string{"hello", "world"},
		},
		{
			name:  "three words two lines",
			text:  "the quick fox",
			width: 9,
			want:  []string{"the quick", "fox"},
		},
		{
			name:  "long word hard-break",
			text:  "supercalifragilistic",
			width: 5,
			want:  []string{"super", "calif", "ragil", "istic"},
		},
		{
			name:  "embedded newline forces break",
			text:  "line one\nline two",
			width: 20,
			want:  []string{"line one", "line two"},
		},
		{
			name:  "empty input gives one empty line",
			text:  "",
			width: 10,
			want:  []string{""},
		},
		{
			name:  "zero width returns single empty line",
			text:  "anything",
			width: 0,
			want:  []string{""},
		},
		{
			name:  "negative width returns single empty line",
			text:  "anything",
			width: -3,
			want:  []string{""},
		},
		{
			name:  "leading and trailing whitespace collapsed",
			text:  "   hello   world   ",
			width: 11,
			want:  []string{"hello world"},
		},
		{
			name:  "collapse multiple spaces between words",
			text:  "a    b",
			width: 10,
			want:  []string{"a b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapText(tt.text, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapText(%q, %d) = %#v\nwant %#v", tt.text, tt.width, got, tt.want)
			}
		})
	}
}

func TestHardBreakChunks(t *testing.T) {
	tests := []struct {
		word  string
		width int
		want  []string
	}{
		{"abcdef", 3, []string{"abc", "def"}},
		{"abcdef", 6, []string{"abcdef"}},
		{"abcdef", 100, []string{"abcdef"}},
		{"a", 5, []string{"a"}},
		{"", 5, []string{""}},
	}
	for _, tt := range tests {
		got := hardBreakChunks(tt.word, tt.width)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("hardBreakChunks(%q, %d) = %#v\nwant %#v", tt.word, tt.width, got, tt.want)
		}
	}
}

// runeLen now returns DISPLAY WIDTH (terminal cells), not rune count.
// This is the right semantic for layout — a row containing CJK or
// emoji has more cells than the rune count would suggest, and the
// renderer needs the display width to lay it out correctly.
func TestRuneLen(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"hello", 5},
		{"héllo", 5},        // é is 1 cell (BMP, narrow)
		{"日本語", 6},          // 3 CJK chars × 2 cells each
		{"emoji😀here", 11},  // 5 + 😀(2) + 4
	}
	for _, tt := range tests {
		got := runeLen(tt.s)
		if got != tt.want {
			t.Errorf("runeLen(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}
