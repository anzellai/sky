// Sky.Tui — display-width + grapheme-cluster helpers.
//
// All Sky.Tui code that asks "how wide is this string in cells?" or
// "how do I iterate this string by visible character?" routes through
// this file. The implementation currently uses `github.com/rivo/uniseg`
// (MIT-licensed; already a Sky runtime dep — see NOTICE.md). Keeping
// the call sites behind these helpers makes it a one-file swap if we
// ever want to roll our own width table or move to `golang.org/x/text/
// width` (BSD-3-Clause).
//
// Why we need this:
//
//   - CJK characters (日本語), most emoji (🚀), some box-drawing
//     ranges occupy 2 terminal cells but 1 Unicode code-point.
//     Without width awareness, every column right of such a character
//     drifts out of alignment in our cell grid.
//
//   - Grapheme clusters like 👨‍👩‍👧‍👦 (4 emoji + 3 ZWJ joiners +
//     possibly skin-tone modifiers) display as a single cell but
//     contain ~7 code points. A naive rune-by-rune loop would treat
//     them as 7 separate cells.
//
// The helpers below give us:
//
//   - displayWidth(s)        — number of cells s occupies on a
//                              standard terminal (uses East Asian
//                              Width spec internally)
//   - displayWidthRune(r)    — same for a single rune (1 or 2 in
//                              practice; 0 for combining marks)
//   - iterGraphemes(s, fn)   — visit each grapheme cluster + its
//                              display width; preserves the cluster
//                              as a single "logical character"

package rt

import "github.com/rivo/uniseg"

// displayWidth returns the number of terminal cells s occupies. For
// pure ASCII, equal to len(s). For text containing CJK / emoji /
// combining marks, this differs from rune count.
func displayWidth(s string) int {
	return uniseg.StringWidth(s)
}

// displayWidthRune is the single-rune variant. Useful where iteration
// already produces runes and we just need each one's width without
// allocating a string per call.
//
// Special cases:
//   - 0 for combining marks (they don't advance the cursor)
//   - 1 for printable ASCII and most BMP characters
//   - 2 for CJK and East Asian Wide chars
//
// Control characters (0x00-0x1F, 0x7F) return 0 — but our renderer
// sanitises those upstream via sanitiseRune, so they shouldn't reach
// here in practice.
func displayWidthRune(r rune) int {
	// uniseg reports rune width via StringWidth; for a single rune
	// the small allocation is unavoidable in this version of uniseg's
	// API. The renderer doesn't call this in tight loops — paint
	// passes use iterGraphemes which is allocation-free per cluster.
	return uniseg.StringWidth(string(r))
}

// iterGraphemes invokes fn for each grapheme cluster in s, passing
// the cluster as a string and its display width. Returns when fn
// returns false (early-exit pattern, like `range` over a chan).
//
// This is the canonical iteration shape for Sky.Tui's paint pass —
// each cluster lands in exactly one cell, the next paint position
// advances by `width`. Wide clusters (width 2) leave the cell to
// their right empty — see paintText for how the renderer marks the
// continuation cell so paintDiff handles emission correctly.
func iterGraphemes(s string, fn func(cluster string, width int) bool) {
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		if !fn(g.Str(), g.Width()) {
			return
		}
	}
}

// graphemeCount returns the number of grapheme clusters in s. Unlike
// rune count, treats 👨‍👩‍👧 as one cluster.
func graphemeCount(s string) int {
	return uniseg.GraphemeClusterCount(s)
}
