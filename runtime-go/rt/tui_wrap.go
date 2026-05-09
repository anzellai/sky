// Sky.Tui — word-wrap engine.
//
// Pure functions used by paragraph / wrappedRow / textColumn renderers
// to break text into lines that fit a width constraint. Designed to be
// straightforward and testable; no Unicode line-break-rules subtlety
// (UAX #14) — we break on whitespace runs, falling back to char-break
// for words longer than the line width.
//
// Width is in character cells (not bytes). All lengths are
// rune-counted; the heavier grapheme-aware mode is a future polish
// pass when uniseg integration lands.

package rt

import (
	"strings"
)

// wrapText breaks `text` into lines fitting within `width` cells.
// Soft-breaks at runs of whitespace; hard-breaks (mid-word) only when
// a word exceeds the line width. Newlines in the input force a break.
//
// Returns at least one line (empty list of words → [""]).
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		out = append(out, wrapParagraph(paragraph, width)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// wrapParagraph wraps a single paragraph (no embedded newlines) into
// lines fitting `width`.
func wrapParagraph(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	words := splitOnWhitespace(text)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	var cur strings.Builder
	curWidth := 0
	for _, w := range words {
		wWidth := runeLen(w)
		// Word longer than line width: hard-break it into chunks.
		if wWidth > width {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				curWidth = 0
			}
			out = append(out, hardBreakChunks(w, width)...)
			continue
		}
		needed := wWidth
		if cur.Len() > 0 {
			needed++ // +1 for the space separator
		}
		if curWidth+needed > width {
			out = append(out, cur.String())
			cur.Reset()
			curWidth = 0
			cur.WriteString(w)
			curWidth = wWidth
		} else {
			if cur.Len() > 0 {
				cur.WriteByte(' ')
				curWidth++
			}
			cur.WriteString(w)
			curWidth += wWidth
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// splitOnWhitespace returns the non-empty word runs between whitespace.
// Treats runs of multiple spaces / tabs as a single separator.
func splitOnWhitespace(s string) []string {
	return strings.Fields(s)
}

// hardBreakChunks splits a word that exceeds `width` into chunks of at
// most `width` cells. Used as a fallback when soft-break can't fit a
// long word.
func hardBreakChunks(word string, width int) []string {
	if width <= 0 || word == "" {
		return []string{word}
	}
	runes := []rune(word)
	var out []string
	for len(runes) > 0 {
		end := width
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[:end]))
		runes = runes[end:]
	}
	return out
}
