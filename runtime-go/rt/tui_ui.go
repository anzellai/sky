// Sky.Tui — Element-shape variant.
//
// Tui.app accepts a `view : Model -> Element Msg` (typed Std.Ui tree)
// and renders it to character cells, instead of Tui.program's
// `view : Model -> String` (raw frame the user assembles).
//
// This is the "write once, render anywhere" path: the same `view`
// function that produces an HTML rendering under Sky.Live can produce
// a TUI rendering under Tui.app, with explicit lossy fallbacks for
// visual decoration that doesn't carry to a character grid (font size,
// background images, drop shadows — see docs/design/std-ui-cross-
// platform.md).
//
// Logical-pixel canvas (Px N is a 1280×720-canvas pixel by default,
// configurable via cfg.canvas). Each renderer converts to native
// units; for TUI:
//
//   pxPerCellX = canvas_width  / term_cols
//   pxPerCellY = canvas_height / term_rows
//
// Recomputed on SIGWINCH so the layout reflows on terminal resize.

package rt

import (
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/term"
)

// ─── Public entry point ──────────────────────────────────────────────

func Tui_app(cfg any) any {
	return func() any {
		return tuiAppRun(cfg)
	}
}

// ─── Renderer state ──────────────────────────────────────────────────

type tuiCanvas struct{ width, height int }

type tuiCell struct {
	ch        string
	fg, bg    tuiColor
	bold      bool
	italic    bool
	underline bool
	reverse   bool
}

type tuiColor struct {
	set     bool
	r, g, b uint8
}

// focusable is one element with a click handler. The runtime tracks
// these in tab order so Tab/Enter can activate them by index.
type focusable struct {
	clickEvt any // pre-wrapped event payload (Std.Live.Events.onClick value)
	row, col int // top-left corner of the focused element's box
	w, h     int
}

// ─── Main loop ──────────────────────────────────────────────────────

func tuiAppRun(cfg any) any {
	initFn := Field(cfg, "Init")
	updateFn := Field(cfg, "Update")
	viewFn := Field(cfg, "View")
	subsFn := Field(cfg, "Subscriptions")
	onKeyFn := Field(cfg, "OnKey") // optional — for global hotkeys
	if initFn == nil || updateFn == nil || viewFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Tui.app: cfg must define init / update / view"))
	}

	canvas := tuiCanvas{width: 1280, height: 720}
	if cw := Field(cfg, "CanvasWidth"); cw != nil {
		if v := AsInt(cw); v > 0 {
			canvas.width = v
		}
	}
	if ch := Field(cfg, "CanvasHeight"); ch != nil {
		if v := AsInt(ch); v > 0 {
			canvas.height = v
		}
	}

	stdin := os.Stdin
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		msg := "Tui.app: stdin is not a terminal — use a real TTY"
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		msg := "Tui.app: cannot enter raw mode: " + err.Error()
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}
	defer func() {
		_ = term.Restore(fd, oldState)
		fmt.Print(tuiShowCursor)
		fmt.Print(tuiAltScreenExit)
	}()

	fmt.Print(tuiAltScreenEnter)
	fmt.Print(tuiHideCursor)

	msgCh := make(chan any, 32)
	doneCh := make(chan struct{})

	// Initial state.
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	if cmd := tupleSecond(initRes); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}

	subMgr := newSubManager(msgCh)
	subMgr.update(subsFn, model)

	// Focus state — runtime-managed, hidden from user code.
	focusIdx := 0

	// First render.
	cols, rows := tuiTermSize(fd)
	frame, focusables := renderElementFrame(viewFn, model, cols, rows, canvas, focusIdx)
	tuiPaint(frame)
	focusIdx = clampFocus(focusIdx, len(focusables))

	// Key reader goroutine. Three categories of keys:
	//   - Tab / Shift-Tab    → focus navigation (handled by runtime)
	//   - Enter on focused   → dispatch focused element's onClick
	//   - Anything else      → forward to user's onKey if defined
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := stdin.Read(buf)
			if err != nil {
				close(doneCh)
				return
			}
			if n == 0 {
				continue
			}
			i := 0
			for i < n {
				ev, consumed := tuiDecodeKey(buf[i:n])
				if consumed == 0 {
					break
				}
				i += consumed
				select {
				case msgCh <- tuiKeyMsg{ev: ev}:
				case <-doneCh:
					return
				}
			}
		}
	}()

	for {
		var msg any
		select {
		case msg = <-msgCh:
		case <-doneCh:
			subMgr.stopAll()
			return Ok[any, any](struct{}{})
		}

		// Intercept tuiKeyMsg before dispatching to update — Tab /
		// Shift-Tab handle focus locally, Enter activates focused
		// element, anything else falls through to user's onKey.
		if km, ok := msg.(tuiKeyMsg); ok {
			handled := false
			switch km.ev.kind {
			case "tab":
				if len(focusables) > 0 {
					focusIdx = (focusIdx + 1) % len(focusables)
					handled = true
				}
			case "other":
				// Shift-Tab arrives as ESC [ Z — tuiDecodeKey emits
				// it as kind "other" with value "\x1b[Z".
				if km.ev.value == "\x1b[Z" {
					if len(focusables) > 0 {
						focusIdx = (focusIdx - 1 + len(focusables)) % len(focusables)
						handled = true
					}
				}
			case "enter":
				if focusIdx >= 0 && focusIdx < len(focusables) {
					if clickMsg := tuiExtractClickMsg(focusables[focusIdx].clickEvt); clickMsg != nil {
						msg = clickMsg
						goto applyMsg
					}
				}
			}
			if handled {
				// Re-render with new focus, no model change.
				frame, focusables = renderElementFrame(viewFn, model, cols, rows, canvas, focusIdx)
				tuiPaint(frame)
				focusIdx = clampFocus(focusIdx, len(focusables))
				continue
			}
			if onKeyFn != nil {
				key := tuiKeyToSky(onKeyFn, km.ev)
				if key != nil {
					if userMsg := SkyCall(onKeyFn, key); userMsg != nil {
						msg = userMsg
						goto applyMsg
					}
				}
			}
			continue
		}

	applyMsg:
		model = cliApplyUpdate(updateFn, msg, model, msgCh)
		subMgr.update(subsFn, model)

		// On resize, recompute terminal dims.
		newCols, newRows := tuiTermSize(fd)
		if newCols != cols || newRows != rows {
			cols, rows = newCols, newRows
		}

		frame, focusables = renderElementFrame(viewFn, model, cols, rows, canvas, focusIdx)
		tuiPaint(frame)
		focusIdx = clampFocus(focusIdx, len(focusables))
	}
}

// tuiKeyMsg is a private message type the runtime uses to ferry
// keypresses from the reader goroutine to the main loop. User code
// never sees this.
type tuiKeyMsg struct {
	ev keyEvent
}

func clampFocus(idx, n int) int {
	if n == 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

func tuiTermSize(fd int) (int, int) {
	w, h, err := term.GetSize(fd)
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// tuiExtractClickMsg pulls the Msg out of a Std.Live.Events event
// value. Event_onClick returns an `eventPair{name, msg}` (see live.go);
// we just read its msg field. We also tolerate tuple-shaped values for
// forward compatibility with future event payload shapes.
func tuiExtractClickMsg(evt any) any {
	if evt == nil {
		return nil
	}
	if ep, ok := evt.(eventPair); ok {
		return ep.msg
	}
	if t, ok := evt.(SkyTuple2); ok {
		return t.V1
	}
	if pair, ok := evt.([]any); ok && len(pair) == 2 {
		return pair[1]
	}
	return nil
}

// ─── Rendering ───────────────────────────────────────────────────────

// renderElementFrame is the top-level render. Walks the Element ADT,
// computes layout for the available terminal size + logical canvas,
// produces a 2D cell grid + focusable list (in tab order).
func renderElementFrame(viewFn, model any, cols, rows int, canvas tuiCanvas, focusIdx int) (string, []focusable) {
	elem := SkyCall(viewFn, model)
	pxPerCellX := float64(canvas.width) / float64(cols)
	pxPerCellY := float64(canvas.height) / float64(rows)
	if pxPerCellX <= 0 {
		pxPerCellX = 1
	}
	if pxPerCellY <= 0 {
		pxPerCellY = 1
	}
	ctx := tuiLayoutCtx{
		cols:       cols,
		rows:       rows,
		pxPerCellX: pxPerCellX,
		pxPerCellY: pxPerCellY,
	}
	grid := newCellGrid(cols, rows)
	var focusables []focusable
	box := layoutElement(elem, ctx, cols, rows, layoutAxisColumn)
	paintBox(grid, box, 0, 0, cols, rows, focusIdx, &focusables, layoutAxisColumn, 0)
	return cellsToANSI(grid), focusables
}

type tuiLayoutCtx struct {
	cols, rows             int
	pxPerCellX, pxPerCellY float64
}

type layoutAxis int

const (
	layoutAxisColumn layoutAxis = iota
	layoutAxisRow
)

// layoutBox is the result of measuring an Element. It carries enough
// information for the paint pass to actually emit cells.
type layoutBox struct {
	kind     string // "empty" | "text" | "node"
	text     string // for "text"
	tag      string // for tagged nodes ("h1", "button", "a", "input"…) — empty for default
	width    int
	height   int
	axis     layoutAxis // for "node" — children are laid out in this direction
	padding  [4]int     // top, right, bottom, left in cells
	spacing  int        // cells between siblings
	fg, bg   tuiColor
	bold     bool
	italic   bool
	underline bool
	clickEvt any // Std.Live.Events.onClick value, if any
	children []layoutBox
}

// layoutElement walks one Element node + computes its box for the
// given parent constraints. Recursive.
//
//   maxW, maxH: parent-imposed upper bounds in cells
//   parentAxis: how this element is being laid out by its parent
func layoutElement(elem any, ctx tuiLayoutCtx, maxW, maxH int, parentAxis layoutAxis) layoutBox {
	adt, ok := elem.(SkyADT)
	if !ok {
		return layoutBox{kind: "empty"}
	}
	switch adt.Tag {
	case 0: // Empty
		return layoutBox{kind: "empty"}
	case 1: // Text s
		s := ""
		if len(adt.Fields) > 0 {
			if str, ok := adt.Fields[0].(string); ok {
				s = str
			}
		}
		return layoutBox{kind: "text", text: s, width: runeLen(s), height: 1}
	case 2: // Node desc attrs children
		return layoutNode("", adt.Fields, ctx, maxW, maxH, parentAxis)
	case 3: // TaggedNode tag desc attrs children
		tag := ""
		if len(adt.Fields) > 0 {
			if s, ok := adt.Fields[0].(string); ok {
				tag = s
			}
		}
		// Skip first field (tag); the rest mirror Node's layout.
		return layoutNode(tag, adt.Fields[1:], ctx, maxW, maxH, parentAxis)
	case 4: // Raw _
		return layoutBox{kind: "text", text: "[raw]", width: 5, height: 1}
	}
	return layoutBox{kind: "empty"}
}

// layoutNode handles both Node and TaggedNode (after stripping the tag).
// Fields layout: [desc, attrsList, childrenList]
func layoutNode(tag string, fields []any, ctx tuiLayoutCtx, maxW, maxH int, parentAxis layoutAxis) layoutBox {
	if len(fields) < 3 {
		return layoutBox{kind: "empty"}
	}
	attrsList := asList(fields[1])
	childrenList := asList(fields[2])

	// Walk attrs to extract layout-relevant values.
	la := walkAttrs(attrsList, ctx)

	// Determine axis (row vs column from sentinel attrs).
	axis := layoutAxisColumn
	if la.isRow {
		axis = layoutAxisRow
	}

	// Apply tag-specific styling defaults.
	switch tag {
	case "h1":
		la.bold = true
	case "h2", "h3", "h4", "h5", "h6":
		la.bold = true
	case "button":
		// Buttons get bracket framing in the paint pass.
	}

	// Compute available space inside padding.
	innerMaxW := maxW - la.padding[1] - la.padding[3]
	innerMaxH := maxH - la.padding[0] - la.padding[2]
	if innerMaxW < 0 {
		innerMaxW = 0
	}
	if innerMaxH < 0 {
		innerMaxH = 0
	}

	// Resolve explicit width/height first.
	width, hasExplicitW := resolveLengthCells(la.width, "x", innerMaxW, ctx)
	height, hasExplicitH := resolveLengthCells(la.height, "y", innerMaxH, ctx)
	if !hasExplicitW {
		width = innerMaxW
	}
	if !hasExplicitH {
		height = innerMaxH
	}

	// Lay out children.
	childBoxes := layoutChildren(childrenList, ctx, width, height, axis, la.spacing)

	// If width or height was unspecified (Content-like fallback), shrink
	// to children's intrinsic size.
	if !hasExplicitW {
		intrinsic := 0
		if axis == layoutAxisRow {
			for i, c := range childBoxes {
				intrinsic += c.width
				if i > 0 {
					intrinsic += la.spacing
				}
			}
		} else {
			for _, c := range childBoxes {
				if c.width > intrinsic {
					intrinsic = c.width
				}
			}
		}
		if intrinsic < width {
			width = intrinsic
		}
	}
	if !hasExplicitH {
		intrinsic := 0
		if axis == layoutAxisColumn {
			for i, c := range childBoxes {
				intrinsic += c.height
				if i > 0 {
					intrinsic += la.spacing
				}
			}
		} else {
			for _, c := range childBoxes {
				if c.height > intrinsic {
					intrinsic = c.height
				}
			}
		}
		if intrinsic < height {
			height = intrinsic
		}
	}

	// Final box dimensions include padding.
	finalW := width + la.padding[1] + la.padding[3]
	finalH := height + la.padding[0] + la.padding[2]
	if finalW > maxW {
		finalW = maxW
	}
	if finalH > maxH {
		finalH = maxH
	}

	return layoutBox{
		kind:      "node",
		tag:       tag,
		width:     finalW,
		height:    finalH,
		axis:      axis,
		padding:   la.padding,
		spacing:   la.spacing,
		fg:        la.fg,
		bg:        la.bg,
		bold:      la.bold,
		italic:    la.italic,
		underline: la.underline,
		clickEvt:  la.clickEvt,
		children:  childBoxes,
	}
}

// layoutChildren distributes the available main-axis space (width for
// row, height for column) using flex-style portion division. Children
// with explicit sizes get them; remaining space is split among Fill
// children proportional to their portions.
func layoutChildren(children []any, ctx tuiLayoutCtx, availW, availH int, axis layoutAxis, spacing int) []layoutBox {
	n := len(children)
	if n == 0 {
		return nil
	}

	mainAxis := availW
	if axis == layoutAxisColumn {
		mainAxis = availH
	}

	// First pass — measure non-Fill children at intrinsic / explicit size.
	totalSpacing := spacing * (n - 1)
	if totalSpacing < 0 {
		totalSpacing = 0
	}

	type entry struct {
		idx      int
		fillN    int // 0 if not fill
		measured int // main-axis size before fill expansion
		box      layoutBox
	}
	entries := make([]entry, n)
	used := 0
	totalFill := 0

	for i, c := range children {
		// Measure with potentially generous bounds; we'll adjust if needed.
		var box layoutBox
		if axis == layoutAxisRow {
			box = layoutElement(c, ctx, availW, availH, axis)
		} else {
			box = layoutElement(c, ctx, availW, availH, axis)
		}
		// Detect Fill via the resolved Length on the main axis. We need
		// to peek into the Element's attrs to know — simpler heuristic:
		// re-walk attrs for Fill-on-main-axis. For now we treat any
		// child whose intrinsic main-axis size hits availW/availH as
		// having claimed it; finer grain comes when AttrFill is wired
		// via a flag in walkAttrs (see TODO in walkAttrs).
		fillN := childFillPortion(c, axis)
		entries[i] = entry{idx: i, fillN: fillN, box: box}
		if fillN > 0 {
			totalFill += fillN
			entries[i].measured = 0
		} else {
			if axis == layoutAxisRow {
				entries[i].measured = box.width
			} else {
				entries[i].measured = box.height
			}
			used += entries[i].measured
		}
	}

	remaining := mainAxis - used - totalSpacing
	if remaining < 0 {
		remaining = 0
	}

	// Distribute remaining among Fill children.
	if totalFill > 0 {
		distributed := 0
		for i, e := range entries {
			if e.fillN <= 0 {
				continue
			}
			share := remaining * e.fillN / totalFill
			if i == n-1 {
				// Last fill child claims remainder to avoid losing
				// cells to integer division.
				share = remaining - distributed
			}
			distributed += share
			entries[i].measured = share
			// Re-layout the child with the allocated main-axis size.
			if axis == layoutAxisRow {
				entries[i].box = layoutElement(children[i], ctx, share, availH, axis)
				entries[i].box.width = share
			} else {
				entries[i].box = layoutElement(children[i], ctx, availW, share, axis)
				entries[i].box.height = share
			}
		}
	}

	out := make([]layoutBox, n)
	for i, e := range entries {
		out[i] = e.box
	}
	return out
}

// childFillPortion peeks inside an Element's attrs for Fill on the main
// axis. Returns the portion (1 for `Fill 1`, N for `Fill N`), or 0 if
// the child doesn't claim Fill on this axis.
//
// A v0 simplification: we look for AttrWidth/AttrHeight = Fill N. The
// finer-grained "Fill is mediated via Length" walk would integrate
// with walkAttrs.
func childFillPortion(child any, axis layoutAxis) int {
	adt, ok := child.(SkyADT)
	if !ok {
		return 0
	}
	if adt.Tag != 2 && adt.Tag != 3 {
		return 0
	}
	var attrs []any
	switch adt.Tag {
	case 2:
		if len(adt.Fields) >= 2 {
			attrs = asList(adt.Fields[1])
		}
	case 3:
		if len(adt.Fields) >= 3 {
			attrs = asList(adt.Fields[2])
		}
	}
	for _, a := range attrs {
		ad, ok := a.(SkyADT)
		if !ok {
			continue
		}
		// AttrWidth = tag 1, AttrHeight = tag 2 (per Std.Ui.Attribute order)
		switch {
		case axis == layoutAxisRow && ad.Tag == 1:
			if p := lengthFillPortion(ad.Fields); p > 0 {
				return p
			}
		case axis == layoutAxisColumn && ad.Tag == 2:
			if p := lengthFillPortion(ad.Fields); p > 0 {
				return p
			}
		}
	}
	return 0
}

func lengthFillPortion(fields []any) int {
	if len(fields) == 0 {
		return 0
	}
	l, ok := fields[0].(SkyADT)
	if !ok {
		return 0
	}
	// Length.Fill = tag 2 (per Length ADT order: Px=0, Content=1, Fill=2, Min=3, Max=4, Vh=5, Vw=6)
	if l.Tag == 2 && len(l.Fields) > 0 {
		if n, ok := l.Fields[0].(int); ok {
			return n
		}
	}
	return 0
}

// walkAttrs extracts layout-relevant values from a Std.Ui attribute list.
type walkedAttrs struct {
	width      any // raw Length value
	height     any
	padding    [4]int // top, right, bottom, left in cells
	spacing    int
	fg, bg     tuiColor
	bold       bool
	italic     bool
	underline  bool
	isRow      bool
	clickEvt   any
}

func walkAttrs(attrs []any, ctx tuiLayoutCtx) walkedAttrs {
	out := walkedAttrs{}
	for _, a := range attrs {
		adt, ok := a.(SkyADT)
		if !ok {
			continue
		}
		// Tag numbers from Std.Ui's Attribute ADT order (verified
		// against the codegen — see sky-stdlib/Std/Ui.sky).
		// 0:NoAttribute 1:Width 2:Height 3:AlignX 4:AlignY 5:Nearby
		// 6:Padding 7:Spacing 8:Style 9:Describe 10:Class 11:Event
		// 12:Attribute 13:FontSize 14:FontColor 15:FontFamily
		// 16:FontWeight 17:FontItalic 18:FontUnderline 19:FontDecoration
		// 20:FontLetterSpacing 21:FontWordSpacing 22:FontAlign
		// 23:BgColor 24:BgImage 25:BgGradient 26:BorderWidth …
		switch adt.Tag {
		case 0: // NoAttribute
			continue
		case 1: // AttrWidth Length
			if len(adt.Fields) > 0 {
				out.width = adt.Fields[0]
			}
		case 2: // AttrHeight Length
			if len(adt.Fields) > 0 {
				out.height = adt.Fields[0]
			}
		case 6: // AttrPadding T R B L
			if len(adt.Fields) >= 4 {
				out.padding[0] = pxToCellsY(intOf(adt.Fields[0]), ctx)
				out.padding[1] = pxToCellsX(intOf(adt.Fields[1]), ctx)
				out.padding[2] = pxToCellsY(intOf(adt.Fields[2]), ctx)
				out.padding[3] = pxToCellsX(intOf(adt.Fields[3]), ctx)
			}
		case 7: // AttrSpacing N
			if len(adt.Fields) > 0 {
				out.spacing = pxToCellsX(intOf(adt.Fields[0]), ctx)
			}
		case 8: // AttrStyle "k" "v" — sentinel for row/col detection
			if len(adt.Fields) >= 2 {
				k, _ := adt.Fields[0].(string)
				if k == "__row" {
					out.isRow = true
				}
			}
		case 11: // AttrEvent — pre-built event payload
			if len(adt.Fields) > 0 {
				out.clickEvt = adt.Fields[0]
			}
		case 13: // AttrFontSize — IGNORED in TUI
		case 14: // AttrFontColor Color
			if len(adt.Fields) > 0 {
				out.fg = colorOf(adt.Fields[0])
			}
		case 16: // AttrFontWeight
			if len(adt.Fields) > 0 {
				if w, ok := adt.Fields[0].(int); ok && w >= 600 {
					out.bold = true
				}
			}
		case 17: // AttrFontItalic
			out.italic = true
		case 18: // AttrFontUnderline
			out.underline = true
		case 23: // AttrBgColor Color
			if len(adt.Fields) > 0 {
				out.bg = colorOf(adt.Fields[0])
			}
			// Other attrs (BgImage, BgGradient, BorderShadow, font family
			// etc.) are explicitly IGNORED in TUI per the cross-platform
			// mapping doc. A v1 polish pass adds Border-* support
			// (unicode box drawing) when the spike validates the
			// architecture.
		}
	}
	return out
}

func intOf(v any) int {
	if v == nil {
		return 0
	}
	if n, ok := v.(int); ok {
		return n
	}
	return AsInt(v)
}

// colorOf reads a Std.Ui.Color value — Color = Rgba Int Int Int Float.
func colorOf(v any) tuiColor {
	adt, ok := v.(SkyADT)
	if !ok {
		return tuiColor{}
	}
	if adt.Tag != 0 || len(adt.Fields) < 3 {
		return tuiColor{}
	}
	r, _ := adt.Fields[0].(int)
	g, _ := adt.Fields[1].(int)
	b, _ := adt.Fields[2].(int)
	return tuiColor{set: true, r: uint8(r & 0xff), g: uint8(g & 0xff), b: uint8(b & 0xff)}
}

// resolveLengthCells maps a Std.Ui Length value to character cells on
// the given axis, given the available cells in the parent. Returns
// (cells, hasExplicitSize). If the input isn't a recognised Length,
// returns (0, false) and the caller falls back to Content / parent-fill.
func resolveLengthCells(v any, axis string, available int, ctx tuiLayoutCtx) (int, bool) {
	if v == nil {
		return 0, false
	}
	adt, ok := v.(SkyADT)
	if !ok {
		return 0, false
	}
	switch adt.Tag {
	case 0: // Px Int
		if len(adt.Fields) == 0 {
			return 0, false
		}
		px, _ := adt.Fields[0].(int)
		if axis == "x" {
			return pxToCellsX(px, ctx), true
		}
		return pxToCellsY(px, ctx), true
	case 1: // Content
		return 0, false // caller measures children
	case 2: // Fill _
		// Fill is handled by the parent's distribution pass; here it
		// claims "as much as possible" if asked directly.
		return available, true
	case 3: // Min N Length
		if len(adt.Fields) >= 2 {
			minN, _ := adt.Fields[0].(int)
			inner, hasExpl := resolveLengthCells(adt.Fields[1], axis, available, ctx)
			if !hasExpl {
				return minN, true
			}
			if inner < minN {
				return minN, true
			}
			return inner, true
		}
	case 4: // Max N Length
		if len(adt.Fields) >= 2 {
			maxN, _ := adt.Fields[0].(int)
			inner, hasExpl := resolveLengthCells(adt.Fields[1], axis, available, ctx)
			if !hasExpl {
				return available, true
			}
			if inner > maxN {
				return maxN, true
			}
			return inner, true
		}
	case 5: // Vh N (viewport-height percent)
		if len(adt.Fields) > 0 {
			pct, _ := adt.Fields[0].(int)
			return ctx.rows * pct / 100, true
		}
	case 6: // Vw N (viewport-width percent)
		if len(adt.Fields) > 0 {
			pct, _ := adt.Fields[0].(int)
			return ctx.cols * pct / 100, true
		}
	}
	return 0, false
}

// pxToCellsX / pxToCellsY — logical-pixel canvas conversion.
func pxToCellsX(px int, ctx tuiLayoutCtx) int {
	if ctx.pxPerCellX <= 0 {
		return px
	}
	return int(math.Round(float64(px) / ctx.pxPerCellX))
}

func pxToCellsY(px int, ctx tuiLayoutCtx) int {
	if ctx.pxPerCellY <= 0 {
		return px
	}
	return int(math.Round(float64(px) / ctx.pxPerCellY))
}

// runeLen counts visible characters in a UTF-8 string. For v0 we use
// rune count; uniseg (already a runtime dep) gives proper grapheme
// clusters when polish lands.
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// ─── Paint pass ──────────────────────────────────────────────────────

func newCellGrid(cols, rows int) [][]tuiCell {
	g := make([][]tuiCell, rows)
	for i := range g {
		row := make([]tuiCell, cols)
		for j := range row {
			row[j].ch = " "
		}
		g[i] = row
	}
	return g
}

// paintBox writes a layoutBox into the grid starting at (col0, row0).
// Recurses through children, applying axis + spacing for sibling
// placement. Collects focusable elements in tab order.
func paintBox(grid [][]tuiCell, box layoutBox, col0, row0, maxW, maxH, focusIdx int, focusables *[]focusable, parentAxis layoutAxis, idxInParent int) {
	w := box.width
	if w > maxW {
		w = maxW
	}
	h := box.height
	if h > maxH {
		h = maxH
	}

	// Background fill.
	if box.bg.set {
		fillRect(grid, col0, row0, w, h, box.bg)
	}

	// If this box is focusable, register it.
	if box.clickEvt != nil {
		focIdx := len(*focusables)
		*focusables = append(*focusables, focusable{
			clickEvt: box.clickEvt,
			row:      row0, col: col0, w: w, h: h,
		})
		// Visual cue when focused: reverse video over the whole box.
		if focIdx == focusIdx {
			applyReverse(grid, col0, row0, w, h)
		}
	}

	// Recurse into content area (after padding).
	innerCol := col0 + box.padding[3]
	innerRow := row0 + box.padding[0]
	innerW := w - box.padding[1] - box.padding[3]
	innerH := h - box.padding[0] - box.padding[2]
	if innerW < 0 {
		innerW = 0
	}
	if innerH < 0 {
		innerH = 0
	}

	switch box.kind {
	case "text":
		paintText(grid, box.text, innerCol, innerRow, innerW, box.fg, box.bg, box.bold, box.italic, box.underline)
	case "node":
		// Tag-specific framing (button: bracket the label).
		// Skipped for v0 to keep the spike honest about scope.
		if box.axis == layoutAxisRow {
			x := innerCol
			for i, c := range box.children {
				if i > 0 {
					x += box.spacing
				}
				paintBox(grid, c, x, innerRow, innerW-(x-innerCol), innerH, focusIdx, focusables, layoutAxisRow, i)
				x += c.width
			}
		} else {
			y := innerRow
			for i, c := range box.children {
				if i > 0 {
					y += box.spacing
				}
				paintBox(grid, c, innerCol, y, innerW, innerH-(y-innerRow), focusIdx, focusables, layoutAxisColumn, i)
				y += c.height
			}
		}
	}

	// Inherit fg/bold/italic/underline to children would be done by
	// passing them through layoutCtx — for v0, the Std.Ui pattern is
	// "set the style on the leaf" so we don't propagate.
}

func paintText(grid [][]tuiCell, text string, col, row, maxW int, fg, bg tuiColor, bold, italic, underline bool) {
	if row < 0 || row >= len(grid) {
		return
	}
	rowCells := grid[row]
	x := col
	for _, r := range text {
		if x >= col+maxW || x >= len(rowCells) {
			break
		}
		if x < 0 {
			x++
			continue
		}
		c := &rowCells[x]
		c.ch = string(r)
		if fg.set {
			c.fg = fg
		}
		if bg.set {
			c.bg = bg
		}
		if bold {
			c.bold = true
		}
		if italic {
			c.italic = true
		}
		if underline {
			c.underline = true
		}
		x++
	}
}

func fillRect(grid [][]tuiCell, col, row, w, h int, bg tuiColor) {
	for r := row; r < row+h && r < len(grid); r++ {
		if r < 0 {
			continue
		}
		rowCells := grid[r]
		for c := col; c < col+w && c < len(rowCells); c++ {
			if c < 0 {
				continue
			}
			rowCells[c].bg = bg
		}
	}
}

func applyReverse(grid [][]tuiCell, col, row, w, h int) {
	for r := row; r < row+h && r < len(grid); r++ {
		if r < 0 {
			continue
		}
		rowCells := grid[r]
		for c := col; c < col+w && c < len(rowCells); c++ {
			if c < 0 {
				continue
			}
			rowCells[c].reverse = true
		}
	}
}

// ─── ANSI emission ───────────────────────────────────────────────────

// cellsToANSI walks a 2D cell grid and produces an ANSI-encoded string.
// v0 emits naively — every cell is preceded by its style's SGR
// sequence. A diff-based emitter is the obvious follow-up perf win.
func cellsToANSI(grid [][]tuiCell) string {
	var sb strings.Builder
	sb.WriteString(tuiCursorHome)
	sb.WriteString(tuiClearScreen)
	sb.WriteString(tuiCursorHome)
	for r, row := range grid {
		if r > 0 {
			sb.WriteString("\r\n")
		}
		var lastStyle string
		for _, c := range row {
			s := cellStyleSGR(c)
			if s != lastStyle {
				sb.WriteString("\x1b[0m")
				if s != "" {
					sb.WriteString(s)
				}
				lastStyle = s
			}
			sb.WriteString(c.ch)
		}
	}
	sb.WriteString("\x1b[0m")
	return sb.String()
}

func cellStyleSGR(c tuiCell) string {
	var parts []string
	if c.bold {
		parts = append(parts, "1")
	}
	if c.italic {
		parts = append(parts, "3")
	}
	if c.underline {
		parts = append(parts, "4")
	}
	if c.reverse {
		parts = append(parts, "7")
	}
	if c.fg.set {
		parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", c.fg.r, c.fg.g, c.fg.b))
	}
	if c.bg.set {
		parts = append(parts, fmt.Sprintf("48;2;%d;%d;%d", c.bg.r, c.bg.g, c.bg.b))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func tuiPaint(frame string) {
	fmt.Print(frame)
}
