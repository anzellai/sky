// runtime-go/rt/gui.go — Sky.Gui backend (gio-based native window)
//
// Status: Stage 2 MVP (v0.13 milestone, branch exp/sky-gui-gio)
//
// Sibling backend to Sky.Live (HTML/SSE) and Sky.Tui (ANSI cells).
// Same TEA shape: init / update / view / subscriptions; same Std.Ui
// source code.
//
// Why gio: see docs/design/sky-gui-gio-plan.md. TL;DR — only viable
// pure-Go cross-platform (Mac/Win/Linux + iOS/Android) UI library
// that fits TEA's pure-view shape cleanly. The cgo footprint is
// bounded to OS window/event interop (no cgo for the rendering
// pipeline itself), and end users don't need any runtime deps —
// gio links against system frameworks only.

package rt

import (
	"image/color"
	"os"
	"strings"
	"sync"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ─── Std.Ui ADT tags (kept in sync with sky-stdlib/Std/Ui.sky) ───────
//
// Sky's typed-codegen erases the SkyADT-vs-typed-record distinction
// at value sites by emitting an interface containing the SkyADT
// struct. The interpreter pattern-matches on the integer Tag.

const (
	tagElEmpty       = 0 // Empty
	tagElText        = 1 // Text String
	tagElNode        = 2 // Node Description attrs children
	tagElTaggedNode  = 3 // TaggedNode tag desc attrs children
	tagElRaw         = 4 // Raw any

	tagAttrNoAttribute      = 0
	tagAttrWidth            = 1
	tagAttrHeight           = 2
	tagAttrAlignX           = 3
	tagAttrAlignY           = 4
	tagAttrNearby           = 5
	tagAttrPadding          = 6
	tagAttrSpacing          = 7
	tagAttrStyle            = 8
	tagAttrDescribe         = 9
	tagAttrClass            = 10
	tagAttrEvent            = 11
	tagAttrAttribute        = 12
	tagAttrFontSize         = 13
	tagAttrFontColor        = 14
	tagAttrFontFamily       = 15
	tagAttrFontWeight       = 16
	tagAttrFontItalic       = 17
	tagAttrFontUnderline    = 18
	tagAttrFontDecoration   = 19
	tagAttrFontLetterSpace  = 20
	tagAttrFontWordSpace    = 21
	tagAttrFontAlign        = 22
	tagAttrBgColor          = 23
	tagAttrBgImage          = 24
	tagAttrBgGradient       = 25
	tagAttrBorderWidth      = 26
	tagAttrBorderWidthEach  = 27
	tagAttrBorderColor      = 28
	tagAttrBorderRounded    = 29
	tagAttrBorderStyle      = 30
	tagAttrBorderShadow     = 31
	tagAttrBorderInsetShdw  = 32
	tagAttrPointer          = 33
	tagAttrOverflow         = 34
)

// ─── Entry point ─────────────────────────────────────────────────────

// Gui_app — entry point matching Tui_app / Live_app shape. Takes the
// same {init, update, view, subscriptions, …} cfg record. Returns a
// Task (deferred until the user binds it to `main`).
func Gui_app(cfg any) any {
	return func() any {
		return guiAppRun(cfg)
	}
}

func guiAppRun(cfg any) any {
	initFn := Field(cfg, "Init")
	updateFn := Field(cfg, "Update")
	viewFn := Field(cfg, "View")
	if initFn == nil || updateFn == nil || viewFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Gui.app: cfg must define init / update / view"))
	}

	title := "Sky.Gui"
	if v := Field(cfg, "Title"); v != nil {
		if s, ok := v.(string); ok && s != "" {
			title = s
		}
	}
	width := 800
	if v := Field(cfg, "Width"); v != nil {
		if n := AsInt(v); n > 0 {
			width = n
		}
	}
	height := 600
	if v := Field(cfg, "Height"); v != nil {
		if n := AsInt(v); n > 0 {
			height = n
		}
	}

	// init: () -> (Model, Cmd Msg). We unwrap the tuple to get Model.
	initRes := safeSkyCall(initFn, struct{}{})
	var model any
	if tup, ok := initRes.(SkyTuple2); ok {
		model = tup.V0
	} else if t2, ok := initRes.(T2[any, any]); ok {
		model = t2.V0
	} else {
		model = initRes
	}

	go guiRunLoop(title, width, height, updateFn, viewFn, model)
	app.Main()
	return SkyResult[any, any]{Tag: 0, OkValue: struct{}{}}
}

// ─── TEA loop driving gio frame events ───────────────────────────────

// guiRunLoop owns the model, runs the gio event loop, and dispatches
// pointer/key events back into update via a Msg channel.
func guiRunLoop(title string, width, height int, updateFn, viewFn, model any) {
	w := new(app.Window)
	w.Option(
		app.Title(title),
		app.Size(unit.Dp(float32(width)), unit.Dp(float32(height))),
	)

	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

	ctx := &guiCtx{
		theme:    th,
		clickers: make(map[int]*widget.Clickable),
		dispatch: make(chan any, 32),
	}

	var ops op.Ops
	for {
		switch ev := w.Event().(type) {
		case app.DestroyEvent:
			if ev.Err != nil {
				os.Exit(1)
			}
			os.Exit(0)

		case app.FrameEvent:
			gtx := app.NewContext(&ops, ev)

			// 1. Drain pending Msgs (from prior frame's pointer/key
			// events) and apply via update.
			drained := false
			for {
				select {
				case msg := <-ctx.dispatch:
					if msg == nil {
						continue
					}
					updateRes := sky_call2(updateFn, msg, model)
					if tup, ok := updateRes.(SkyTuple2); ok {
						model = tup.V0
					} else if t2, ok := updateRes.(T2[any, any]); ok {
						model = t2.V0
					}
					drained = true
				default:
					goto drainDone
				}
			}
		drainDone:
			_ = drained

			// 2. Reset per-frame state (clicker assignment counters,
			// click handlers).
			ctx.frameReset()

			// 3. Call view(model) to get the Element tree.
			elem := safeSkyCall(viewFn, model)

			// 4. Render the Element into gio's ops.
			guiRenderElement(gtx, ctx, elem)

			// 5. After paint: scan registered clickers for clicks.
			// If clicked, send the Msg onto the dispatch channel.
			// gio invalidates the next frame automatically on click,
			// so the channel-drain at the top of the next frame
			// applies the Msg before the next view() call.
			ctx.collectClicks(gtx)

			ev.Frame(gtx.Ops)
		}
	}
}

// ─── Render context — per-window state that survives frames ──────────

type guiCtx struct {
	theme *material.Theme

	// clickerSeq is reset per-frame; it indexes the clickers slice
	// so the same button keeps its widget.Clickable across frames
	// (gio's widget package is stateful — we cache by index for
	// stable identity).
	clickerSeq int
	clickers   map[int]*widget.Clickable

	// onPressMsgs[i] = the Msg to dispatch when clickers[i] is
	// clicked. Populated during render; consumed in collectClicks.
	onPressMsgs []any

	dispatch chan any

	mu sync.Mutex
}

func (c *guiCtx) frameReset() {
	c.clickerSeq = 0
	c.onPressMsgs = c.onPressMsgs[:0]
}

// nextClicker returns the widget.Clickable for the i-th button in
// the current frame, allocating one on first use.
func (c *guiCtx) nextClicker(onPressMsg any) *widget.Clickable {
	i := c.clickerSeq
	c.clickerSeq++
	cl, ok := c.clickers[i]
	if !ok {
		cl = &widget.Clickable{}
		c.clickers[i] = cl
	}
	// Track the Msg slot by index. Slice resize on demand.
	for len(c.onPressMsgs) <= i {
		c.onPressMsgs = append(c.onPressMsgs, nil)
	}
	c.onPressMsgs[i] = onPressMsg
	return cl
}

// collectClicks: after the frame's render pass, check every clicker
// for a fresh click event. If found, dispatch its Msg.
func (c *guiCtx) collectClicks(gtx layout.Context) {
	_ = gtx
	for i := 0; i < c.clickerSeq; i++ {
		cl, ok := c.clickers[i]
		if !ok {
			continue
		}
		for cl.Clicked(gtx) {
			if i < len(c.onPressMsgs) {
				msg := c.onPressMsgs[i]
				if msg != nil {
					select {
					case c.dispatch <- msg:
					default:
						// dispatch channel full; drop (UI events
						// flooding faster than update can run)
					}
				}
			}
		}
	}
}

// ─── Element interpreter ─────────────────────────────────────────────

// guiRenderElement is the entry point. Pattern-matches on the Element
// ADT's Tag and dispatches to per-shape renderers. Falls back to
// a placeholder for unsupported shapes.
func guiRenderElement(gtx layout.Context, ctx *guiCtx, elem any) layout.Dimensions {
	adt, ok := elem.(SkyADT)
	if !ok {
		return layout.Dimensions{}
	}
	switch adt.Tag {
	case tagElEmpty:
		return layout.Dimensions{}
	case tagElText:
		s := ""
		if len(adt.Fields) > 0 {
			if str, ok := adt.Fields[0].(string); ok {
				s = str
			}
		}
		return material.Body1(ctx.theme, s).Layout(gtx)
	case tagElNode:
		// Fields: [Description, attrs, children]
		if len(adt.Fields) < 3 {
			return layout.Dimensions{}
		}
		return guiRenderNode(gtx, ctx, "", adt.Fields[1], adt.Fields[2])
	case tagElTaggedNode:
		// Fields: [tag, Description, attrs, children]
		if len(adt.Fields) < 4 {
			return layout.Dimensions{}
		}
		tag := ""
		if s, ok := adt.Fields[0].(string); ok {
			tag = s
		}
		return guiRenderNode(gtx, ctx, tag, adt.Fields[2], adt.Fields[3])
	case tagElRaw:
		// Web-only: a Std.Html VNode wrapped via Ui.html. Render a
		// placeholder + warn once. Stage 3+ may add gio.html-stub
		// rendering if there's a clear use case.
		guiWarn("ui.html", "Ui.html (Raw VNode) — Sky.Gui can't render HTML; using placeholder")
		return material.Caption(ctx.theme, "[Ui.html unsupported on Gui]").Layout(gtx)
	}
	return layout.Dimensions{}
}

// guiRenderNode handles both Node and TaggedNode. `tag` is the
// HTML hint (e.g. "h1", "button", "input"); empty for plain Node.
//
// MVP: parse attrs into a layoutAttrs struct, dispatch row vs column
// vs single-child based on layout sentinels, recurse into children,
// apply padding/spacing/background/border around the resulting box.
func guiRenderNode(gtx layout.Context, ctx *guiCtx, tag string, attrsAny, childrenAny any) layout.Dimensions {
	attrs := guiParseAttrs(attrsAny)
	children := asList(childrenAny)

	// Special-case button: TaggedNode "button" with onClick attr.
	if tag == "button" || attrs.tag == "button" {
		return guiRenderButton(gtx, ctx, attrs, children)
	}

	// Choose layout axis from sentinel markers.
	axis := layout.Vertical
	if attrs.axis == "row" || attrs.axis == "wrap" {
		axis = layout.Horizontal
	}

	// The core render: a flex layout for multi-child, single-child
	// passthrough otherwise.
	body := func(gtx layout.Context) layout.Dimensions {
		if len(children) == 0 {
			return layout.Dimensions{}
		}
		if len(children) == 1 {
			return guiRenderElement(gtx, ctx, children[0])
		}
		flexChildren := make([]layout.FlexChild, 0, len(children)*2)
		for i, c := range children {
			if i > 0 && attrs.spacing > 0 {
				if axis == layout.Vertical {
					flexChildren = append(flexChildren, layout.Rigid(layout.Spacer{Height: unit.Dp(float32(attrs.spacing))}.Layout))
				} else {
					flexChildren = append(flexChildren, layout.Rigid(layout.Spacer{Width: unit.Dp(float32(attrs.spacing))}.Layout))
				}
			}
			c := c
			flexChildren = append(flexChildren, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return guiRenderElement(gtx, ctx, c)
			}))
		}
		return layout.Flex{Axis: axis, Alignment: guiFlexAlignment(attrs, axis)}.Layout(gtx, flexChildren...)
	}

	// Apply padding (Inset wrapper) + background + border.
	return guiApplyDecoration(gtx, ctx, attrs, body)
}

// guiRenderButton: a TaggedNode "button" wraps its child label inside
// a clickable region. The onClick Msg comes from an AttrEvent payload.
func guiRenderButton(gtx layout.Context, ctx *guiCtx, attrs layoutAttrs, children []any) layout.Dimensions {
	clicker := ctx.nextClicker(attrs.onClickMsg)
	return material.Button(ctx.theme, clicker, guiExtractButtonLabel(children)).Layout(gtx)
}

// guiExtractButtonLabel walks button children to find the first text
// label. Std.Ui.Input.button passes `label` as a single text child;
// fallback is empty string.
func guiExtractButtonLabel(children []any) string {
	for _, c := range children {
		if s := guiElementText(c); s != "" {
			return s
		}
	}
	return ""
}

// guiElementText: recursive label extraction from an Element subtree.
// Returns the first Text node found.
func guiElementText(elem any) string {
	adt, ok := elem.(SkyADT)
	if !ok {
		return ""
	}
	switch adt.Tag {
	case tagElText:
		if len(adt.Fields) > 0 {
			if s, ok := adt.Fields[0].(string); ok {
				return s
			}
		}
	case tagElNode:
		if len(adt.Fields) >= 3 {
			for _, c := range asList(adt.Fields[2]) {
				if s := guiElementText(c); s != "" {
					return s
				}
			}
		}
	case tagElTaggedNode:
		if len(adt.Fields) >= 4 {
			for _, c := range asList(adt.Fields[3]) {
				if s := guiElementText(c); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// guiApplyDecoration wraps a body layout fn with padding, background
// paint, and border. Order matters: outermost is whole node; padding
// shrinks the child region; background fills behind children.
func guiApplyDecoration(gtx layout.Context, ctx *guiCtx, attrs layoutAttrs, body func(layout.Context) layout.Dimensions) layout.Dimensions {
	_ = ctx
	inner := body
	// Padding wrap (Inset)
	if attrs.paddingTop > 0 || attrs.paddingRight > 0 || attrs.paddingBottom > 0 || attrs.paddingLeft > 0 {
		prev := inner
		inner = func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top:    unit.Dp(float32(attrs.paddingTop)),
				Right:  unit.Dp(float32(attrs.paddingRight)),
				Bottom: unit.Dp(float32(attrs.paddingBottom)),
				Left:   unit.Dp(float32(attrs.paddingLeft)),
			}.Layout(gtx, prev)
		}
	}
	// Background paint (fill the entire macro before inner is drawn)
	if attrs.bgSet {
		prev := inner
		col := attrs.bgColor
		inner = func(gtx layout.Context) layout.Dimensions {
			macro := op.Record(gtx.Ops)
			dims := prev(gtx)
			callOp := macro.Stop()
			rect := clip.Rect{Max: dims.Size}.Op()
			paint.FillShape(gtx.Ops, col, rect)
			callOp.Add(gtx.Ops)
			return dims
		}
	}
	return inner(gtx)
}

// guiFlexAlignment picks a cross-axis alignment from attrs.
func guiFlexAlignment(attrs layoutAttrs, axis layout.Axis) layout.Alignment {
	// Cross axis is the OPPOSITE of the main axis. For vertical flex,
	// cross is horizontal; for horizontal flex, cross is vertical.
	if axis == layout.Vertical {
		switch attrs.alignX {
		case "center":
			return layout.Middle
		case "right":
			return layout.End
		default:
			return layout.Start
		}
	}
	switch attrs.alignY {
	case "center":
		return layout.Middle
	case "bottom":
		return layout.End
	default:
		return layout.Start
	}
}

// ─── Attribute parser ────────────────────────────────────────────────

// layoutAttrs is the parsed-attribute state for a single Element node.
// Fields are pre-resolved (e.g. paddingTop/Right/Bottom/Left vs the
// CSS-encoded T R B L tuple in AttrPadding).
type layoutAttrs struct {
	// Layout sentinels (axis comes from a marker attr like "__row")
	axis string // "row" | "column" | "wrap" | "grid" | ""
	tag  string // hint from a marker attr (e.g. "button")

	paddingTop, paddingRight, paddingBottom, paddingLeft int
	spacing                                              int

	alignX, alignY string // "left"|"center"|"right" / "top"|"center"|"bottom"

	bgSet bool
	bgColor color.NRGBA

	borderColor color.NRGBA
	borderWidth int
	borderRound int

	fontSize  int
	fontColor color.NRGBA
	fontSet   bool

	// Event: onClick Msg payload (extracted from AttrEvent)
	onClickMsg any
}

func guiParseAttrs(attrsAny any) layoutAttrs {
	var la layoutAttrs
	for _, a := range asList(attrsAny) {
		adt, ok := a.(SkyADT)
		if !ok {
			continue
		}
		switch adt.Tag {
		case tagAttrPadding:
			if len(adt.Fields) >= 4 {
				la.paddingTop = AsInt(adt.Fields[0])
				la.paddingRight = AsInt(adt.Fields[1])
				la.paddingBottom = AsInt(adt.Fields[2])
				la.paddingLeft = AsInt(adt.Fields[3])
			}
		case tagAttrSpacing:
			if len(adt.Fields) >= 1 {
				la.spacing = AsInt(adt.Fields[0])
			}
		case tagAttrAlignX:
			if len(adt.Fields) >= 1 {
				if v, ok := adt.Fields[0].(SkyADT); ok {
					la.alignX = guiAlignXName(v.Tag)
				}
			}
		case tagAttrAlignY:
			if len(adt.Fields) >= 1 {
				if v, ok := adt.Fields[0].(SkyADT); ok {
					la.alignY = guiAlignYName(v.Tag)
				}
			}
		case tagAttrBgColor:
			if len(adt.Fields) >= 1 {
				la.bgColor = guiColorFrom(adt.Fields[0])
				la.bgSet = true
			}
		case tagAttrBorderColor:
			if len(adt.Fields) >= 1 {
				la.borderColor = guiColorFrom(adt.Fields[0])
			}
		case tagAttrBorderWidth:
			if len(adt.Fields) >= 1 {
				la.borderWidth = AsInt(adt.Fields[0])
			}
		case tagAttrBorderRounded:
			if len(adt.Fields) >= 1 {
				la.borderRound = AsInt(adt.Fields[0])
			}
		case tagAttrFontSize:
			if len(adt.Fields) >= 1 {
				la.fontSize = AsInt(adt.Fields[0])
			}
		case tagAttrFontColor:
			if len(adt.Fields) >= 1 {
				la.fontColor = guiColorFrom(adt.Fields[0])
				la.fontSet = true
			}
		case tagAttrEvent:
			// AttrEvent any — the wrapped eventPair {name, msg}.
			// MVP: only handle "click" events; bind to a clicker.
			if len(adt.Fields) >= 1 {
				if name, msg, ok := guiUnwrapEventPair(adt.Fields[0]); ok {
					if name == "click" {
						la.onClickMsg = msg
					}
				}
			}
		case tagAttrClass:
			// Marker attrs use AttrClass with sentinel names like
			// "__row", "__col", "__wrap", "__grid". Same convention
			// as Sky.Tui's classAttrIs (see tui_ui.go).
			if len(adt.Fields) >= 1 {
				if s, ok := adt.Fields[0].(string); ok {
					switch s {
					case "__row":
						la.axis = "row"
					case "__col":
						la.axis = "column"
					case "__wrap":
						la.axis = "wrap"
					case "__grid":
						la.axis = "grid"
					}
				}
			}
		case tagAttrStyle:
			// Sky.Live-only style hooks (CSS k/v). Sky.Gui can't
			// honour CSS — warn once. Layout markers above use
			// AttrClass so AttrStyle here is always user-provided
			// raw CSS that doesn't translate.
			if len(adt.Fields) >= 2 {
				if k, ok := adt.Fields[0].(string); ok {
					guiWarn("style", "Ui.style \""+k+"\" — CSS not honoured on Gui backend")
				}
			}
		}
	}
	return la
}

func guiAlignXName(tag int) string {
	switch tag {
	case 0:
		return "left"
	case 1:
		return "center"
	case 2:
		return "right"
	}
	return ""
}

func guiAlignYName(tag int) string {
	switch tag {
	case 0:
		return "top"
	case 1:
		return "center"
	case 2:
		return "bottom"
	}
	return ""
}

// guiColorFrom: Color = Rgba R G B A. R/G/B are 0-255 ints; A is
// 0-1 float. We pack into color.NRGBA expected by gio.
func guiColorFrom(v any) color.NRGBA {
	adt, ok := v.(SkyADT)
	if !ok || len(adt.Fields) < 4 {
		return color.NRGBA{A: 0xff}
	}
	r := AsInt(adt.Fields[0])
	g := AsInt(adt.Fields[1])
	b := AsInt(adt.Fields[2])
	a := AsFloat(adt.Fields[3])
	if a < 0 {
		a = 0
	}
	if a > 1 {
		a = 1
	}
	return color.NRGBA{
		R: byte(r & 0xff),
		G: byte(g & 0xff),
		B: byte(b & 0xff),
		A: byte(a * 255),
	}
}

// guiUnwrapEventPair: eventPair is the runtime value produced by
// Std.Live.Events.* and Std.Ui.Events.*. Its struct shape carries
// {name string, msg any}. Returns (name, msg, ok).
func guiUnwrapEventPair(v any) (string, any, bool) {
	if p, ok := v.(eventPair); ok {
		return p.name, p.msg, true
	}
	return "", nil, false
}

// ─── Diagnostics ─────────────────────────────────────────────────────

var (
	guiWarnSeen = make(map[string]struct{})
	guiWarnMu   sync.Mutex
)

// guiWarn emits a deduplicated warning to stderr for unsupported
// primitives. Same pattern as tuiWarn — fires once per (category +
// detail) tuple so a high-frequency render path doesn't spam.
func guiWarn(category, detail string) {
	key := category + "\x00" + detail
	guiWarnMu.Lock()
	if _, seen := guiWarnSeen[key]; seen {
		guiWarnMu.Unlock()
		return
	}
	guiWarnSeen[key] = struct{}{}
	guiWarnMu.Unlock()
	// Print to stderr, but skip noisy spammers — same tone as tuiWarn.
	os.Stderr.WriteString("[sky.gui] " + category + ": " + detail + "\n")
}

// ─── Unused import elision ───────────────────────────────────────────
// Stage 2 scaffolding — strings.Builder used in upcoming label/text
// rendering paths.

var _ = strings.Builder{}
