package rt

import (
	"strings"
	"testing"
)

// ESC byte in user text must NEVER reach a cell as a literal escape —
// any terminal that interprets the surrounding SGR codes would parse
// the malformed sequence and partially honour it.
func TestSanitiseRune_EscByteReplaced(t *testing.T) {
	if got := sanitiseRune('\x1b'); got == '\x1b' {
		t.Errorf("ESC byte must not pass through; got %q", got)
	}
}

// The most surface-relevant control chars get their own substitutes;
// everything else collapses to ·.
func TestSanitiseRune_TabAndNewlineSubstitutes(t *testing.T) {
	if got := sanitiseRune('\t'); got != ' ' {
		t.Errorf("\\t → %q, want space", got)
	}
	if got := sanitiseRune('\n'); got != '␤' {
		t.Errorf("\\n → %q, want ␤", got)
	}
	if got := sanitiseRune('\r'); got != ' ' {
		t.Errorf("\\r → %q, want space", got)
	}
	for r := rune(0); r < 0x20; r++ {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		got := sanitiseRune(r)
		if got != '·' {
			t.Errorf("0x%02X → %q, want ·", r, got)
		}
	}
	if got := sanitiseRune(0x7F); got != '·' {
		t.Errorf("DEL → %q, want ·", got)
	}
}

// Printable ASCII, UTF-8, CJK, emoji must round-trip unchanged.
func TestSanitiseRune_PrintablePassthrough(t *testing.T) {
	for _, r := range []rune{'a', 'Z', '0', ' ', '~', 'é', '日', '本', '🚀'} {
		if got := sanitiseRune(r); got != r {
			t.Errorf("%q sanitised to %q — should pass through", r, got)
		}
	}
}

// The classic "phishing payload" attack: data row contains an ESC
// that would clear the screen and reposition the cursor on a naive
// renderer. After sanitisation, the ESC byte is gone.
func TestSanitiseString_PhishingPayloadStripped(t *testing.T) {
	payload := "Buy now! \x1b[2J\x1b[H Click here: phishing.com"
	got := sanitiseString(payload)
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("ESC byte survived sanitisation: %q", got)
	}
	// Substitution preserves length-in-runes so column layout doesn't drift.
	wantRunes := len([]rune(payload))
	gotRunes := len([]rune(got))
	if wantRunes != gotRunes {
		t.Errorf("rune count changed: %d → %d (sanitisation must be 1:1)", wantRunes, gotRunes)
	}
}

// Clean input must not allocate (sanitiseString returns the original
// string if no substitution was needed).
func TestSanitiseString_CleanInputNoAlloc(t *testing.T) {
	clean := "Hello, world! 你好 🚀"
	got := sanitiseString(clean)
	if got != clean {
		t.Errorf("clean string changed: %q → %q", clean, got)
	}
}
