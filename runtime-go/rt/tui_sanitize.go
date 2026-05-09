// Sky.Tui — control-byte sanitisation for user-supplied text.
//
// Why this matters:
//
// Sky.Tui's render path takes user strings (from Ui.text, Ui.input
// values, log lines, database fields, network responses) and writes
// them into terminal cells. The cells are then emitted to stdout as
// raw bytes by paintDiff. If a string contains an ESC (0x1B) byte,
// that byte goes verbatim to the terminal — the surrounding SGR
// codes we emit make the result a malformed escape sequence that
// some terminals partially honour.
//
// Concrete attack: a database row containing
//
//   "Buy now! \x1b[2J\x1b[H Click here: phishing.com"
//
// rendered via Ui.text would (on terminals that handle the partial
// sequence) clear the screen and reposition the cursor. Sky.Live
// gets HTML-escape protection for free; Sky.Tui needs explicit
// sanitisation.
//
// Strategy: replace every control byte (0x00-0x1F except where the
// renderer handles it explicitly) and DEL (0x7F) with a single
// printable substitute. Tab and newline get context-specific
// treatment — paintText renders a single line so \t becomes space
// and \n becomes ␤ (symbol for newline); multiline inputs and
// paragraphs split on \n BEFORE reaching the cell, so they never
// see this path. Other control chars become · (middle dot).
//
// We don't drop control chars — replacing them keeps the column
// count stable (important for layout) and signals to the user
// "something non-printable was here, please investigate the data".

package rt

// sanitiseRune returns a safe substitute for a control character or
// the rune itself if it's printable. The substitution table:
//
//   \t (0x09)        →  ' ' (single space; tabs don't have a fixed
//                       cell width in our grid)
//   \n (0x0A)        →  '␤' (SYMBOL FOR NEWLINE — visible cue
//                       that a newline appeared in single-line text)
//   \r (0x0D)        →  '' (return ' ' — \r is "carriage return",
//                       has no place in a cell-painted line)
//   \x1B (ESC)       →  '·' (middle dot; signals control char)
//   0x00-0x1F other  →  '·'
//   0x7F (DEL)       →  '·'
//   everything else  →  unchanged (incl. printable ASCII, UTF-8,
//                       CJK, emoji — width handling is separate)
//
// The replacement keeps the rune-count of the source intact (one
// rune in, one rune out) so existing layout / wrap / cursor logic
// stays correct.
func sanitiseRune(r rune) rune {
	switch r {
	case '\t':
		return ' '
	case '\n':
		return '␤' // ␤ — symbol for newline
	case '\r':
		return ' '
	}
	if r < 0x20 || r == 0x7F {
		return '·' // · — middle dot
	}
	return r
}

// sanitiseString applies sanitiseRune to every rune in s. Returns
// the original string if no substitution was needed (avoids the
// allocation for the common case of clean text).
func sanitiseString(s string) string {
	clean := true
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' || r < 0x20 || r == 0x7F {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		out = append(out, sanitiseRune(r))
	}
	return string(out)
}
