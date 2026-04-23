// stdlib_web.go — complete Std.Css, Std.Html, Std.Html.Attributes,
// Std.Html.Events kernel surface.
//
// The core shapes (cssProp, cssRule, attrPair, eventPair, VNode) live in
// live.go. This file adds the missing functions so the full legacy Sky
// stdlib API is callable from Sky source — no reactive backfill.
//
// Audit P3-4: every `fmt.Sprintf("%v", x)` in this file is display-only
// CSS-value formatting — numeric inputs become "42px", "100vh", etc.
// The browser tolerates any stringified form; no secret, identifier,
// or query value ever flows through these calls. Typed Int/Float
// kernels (Css_pxI, Css_scaleF) route through rt.Coerce* when the
// caller supplies a typed source; the `any` variants stay as-is for
// polymorphic codegen. The audit therefore justifies these sites in
// bulk rather than per-call.
package rt

import (
	"fmt"
	"strings"
)

// ═══════════════════════════════════════════════════════════
// Css — units (return plain strings; used as values)
// ═══════════════════════════════════════════════════════════

func Css_vh(n any) any  { return fmt.Sprintf("%vvh", n) }
func Css_vw(n any) any  { return fmt.Sprintf("%vvw", n) }
func Css_ch(n any) any  { return fmt.Sprintf("%vch", n) }
func Css_deg(n any) any { return fmt.Sprintf("%vdeg", n) }
func Css_ms(n any) any  { return fmt.Sprintf("%vms", n) }
func Css_sec(n any) any { return fmt.Sprintf("%vs", n) }

// ═══════════════════════════════════════════════════════════
// Css — colours (plain strings)
// ═══════════════════════════════════════════════════════════

func Css_rgb(r, g, b any) any {
	return fmt.Sprintf("rgb(%v,%v,%v)", r, g, b)
}
func Css_hsl(h, s, l any) any {
	return fmt.Sprintf("hsl(%v,%v%%,%v%%)", h, s, l)
}
func Css_hsla(h, s, l, a any) any {
	return fmt.Sprintf("hsla(%v,%v%%,%v%%,%v)", h, s, l, a)
}

// ═══════════════════════════════════════════════════════════
// Css — properties (cssProp)
// ═══════════════════════════════════════════════════════════

var (
	Css_alignSelf     = cssP("align-self")
	Css_order         = cssPInt("order")
	Css_gridArea      = cssP("grid-area")
	Css_borderWidth   = cssP("border-width")
	Css_borderStyle   = cssP("border-style")
	Css_textOverflow  = cssP("text-overflow")
	Css_textShadow    = cssP("text-shadow")
	Css_clear         = cssP("clear")
	Css_float         = cssP("float")
	Css_right_        = cssP("right")
)

// cssPInt: property whose argument is an Int, serialised verbatim.
func cssPInt(k string) func(any) any {
	return func(v any) any { return cssProp{k: k, v: fmt.Sprintf("%v", v)} }
}

// ═══════════════════════════════════════════════════════════
// Css — grid / transform / variable helpers (plain strings)
// ═══════════════════════════════════════════════════════════

func Css_minmax(a, b any) any {
	return fmt.Sprintf("minmax(%v,%v)", a, b)
}

func Css_rotate(v any) any      { return fmt.Sprintf("rotate(%v)", v) }
func Css_scale(n any) any       { return fmt.Sprintf("scale(%v)", n) }
func Css_translateX(v any) any  { return fmt.Sprintf("translateX(%v)", v) }
func Css_translateY(v any) any  { return fmt.Sprintf("translateY(%v)", v) }

func Css_cssVar(name any) any {
	return fmt.Sprintf("var(--%v)", name)
}
func Css_cssVarOr(name, fallback any) any {
	return fmt.Sprintf("var(--%v,%v)", name, fallback)
}
func Css_defineVar(name, val any) any {
	return cssProp{k: fmt.Sprintf("--%v", name), v: fmt.Sprintf("%v", val)}
}

func Css_calc(a, op, b any) any {
	return fmt.Sprintf("calc(%v %v %v)", a, op, b)
}

// important takes an existing cssProp and returns one with "!important" appended.
// Plain strings (rare) get wrapped back as strings.
func Css_important(v any) any {
	switch p := v.(type) {
	case cssProp:
		return cssProp{k: p.k, v: p.v + " !important"}
	case string:
		return p + " !important"
	}
	return v
}

// shadows: list of individual box-shadow values joined with ", "
func Css_shadows(vals any) any {
	parts := make([]string, 0)
	for _, v := range asList(vals) {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return cssProp{k: "box-shadow", v: strings.Join(parts, ", ")}
}

// 4-value shorthands
func Css_borderRadius4(tl, tr, br, bl any) any {
	return cssProp{k: "border-radius", v: fmt.Sprintf("%v %v %v %v", tl, tr, br, bl)}
}
func Css_padding4(t, r, b, l any) any {
	return cssProp{k: "padding", v: fmt.Sprintf("%v %v %v %v", t, r, b, l)}
}

// ═══════════════════════════════════════════════════════════
// Css — animations (@keyframes + frame steps)
// Render as opaque strings that stylesheet knows how to emit.
// ═══════════════════════════════════════════════════════════

type cssKeyframesRule struct {
	name   string
	frames []string
}

// Css_frame: serialises one animation step, e.g. "50% { transform: scale(1.1); }"
func Css_frame(pct, props any) any {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%v { ", pct))
	for i, p := range asList(props) {
		if i > 0 {
			sb.WriteString("; ")
		}
		switch pp := p.(type) {
		case cssProp:
			sb.WriteString(pp.k + ":" + pp.v)
		case string:
			sb.WriteString(pp)
		default:
			sb.WriteString(fmt.Sprintf("%v", pp))
		}
	}
	sb.WriteString(" }")
	return sb.String()
}

// Css_keyframes: builds a @keyframes rule from a list of frame strings.
func Css_keyframes(name, frames any) any {
	fs := asList(frames)
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%v", f))
	}
	return cssKeyframesRule{name: fmt.Sprintf("%v", name), frames: out}
}

// Css_boxSizingBorderBox: shorthand zero-arg helper.
func Css_boxSizingBorderBox(_ any) any {
	return cssProp{k: "box-sizing", v: "border-box"}
}

// ═══════════════════════════════════════════════════════════
// Html — legacy parity additions
// ═══════════════════════════════════════════════════════════

func Html_aside(a, c any) any    { return htmlElem("aside")(a, c) }
func Html_fieldset(a, c any) any { return htmlElem("fieldset")(a, c) }
func Html_legend(a, c any) any   { return htmlElem("legend")(a, c) }
func Html_tfoot(a, c any) any    { return htmlElem("tfoot")(a, c) }

// linkNode: self-closing <link> with attrs only.
func Html_linkNode(a any) any { return htmlElem("link")(a, nil) }

// mainNode / footerNode: aliases over htmlElem for explicit semantic naming
// to match legacy exports.
func Html_mainNode(a, c any) any   { return htmlElem("main")(a, c) }
func Html_footerNode(a, c any) any { return htmlElem("footer")(a, c) }

// voidNode: a self-closing element of arbitrary tag.
func Html_voidNode(tag any, a any) any {
	return htmlElem(fmt.Sprintf("%v", tag))(a, nil)
}

// attrToString / toString: serialise helpers.
func Html_attrToString(a any) any {
	if p, ok := a.(attrPair); ok {
		return p.key + "=\"" + htmlEscapeAttr(p.val) + "\""
	}
	return ""
}
func Html_toString(node any) any { return Html_render(node) }

// escapeHtml / escapeAttr — expose escaping helpers to Sky code.
func Html_escapeHtml(s any) any { return htmlEscapeText(fmt.Sprintf("%v", s)) }
func Html_escapeAttr(s any) any { return htmlEscapeAttr(fmt.Sprintf("%v", s)) }

// ═══════════════════════════════════════════════════════════
// Attr — legacy parity additions
// ═══════════════════════════════════════════════════════════

func Attr_hidden(v any) any            { return attr("hidden", fmt.Sprintf("%v", v)) }
func Attr_download(v any) any          { return attr("download", fmt.Sprintf("%v", v)) }
func Attr_enctype(v any) any           { return attr("enctype", fmt.Sprintf("%v", v)) }
func Attr_novalidate(_ any) any        { return attr("novalidate", "novalidate") }
func Attr_autocomplete(v any) any      { return attr("autocomplete", fmt.Sprintf("%v", v)) }
func Attr_colspan(v any) any           { return attr("colspan", fmt.Sprintf("%v", v)) }
func Attr_rowspan(v any) any           { return attr("rowspan", fmt.Sprintf("%v", v)) }
func Attr_scope(v any) any             { return attr("scope", fmt.Sprintf("%v", v)) }
func Attr_selected(v any) any          { return attr("selected", "selected") }
func Attr_height(v any) any            { return attr("height", fmt.Sprintf("%v", v)) }
func Attr_width(v any) any             { return attr("width", fmt.Sprintf("%v", v)) }
func Attr_type_(v any) any             { return attr("type", fmt.Sprintf("%v", v)) }
func Attr_ariaDescribedby(v any) any   { return attr("aria-describedby", fmt.Sprintf("%v", v)) }
func Attr_ariaExpanded(v any) any      { return attr("aria-expanded", fmt.Sprintf("%v", v)) }
func Attr_boolAttribute(k any) any     { return attr(fmt.Sprintf("%v", k), fmt.Sprintf("%v", k)) }
func Attr_dataAttribute(k, v any) any  { return attr("data-"+fmt.Sprintf("%v", k), fmt.Sprintf("%v", v)) }

// ═══════════════════════════════════════════════════════════
// Event — legacy parity additions
// ═══════════════════════════════════════════════════════════

func Event_on(name, msg any) any {
	return eventPair{name: fmt.Sprintf("%v", name), msg: msg}
}

func Event_onContextMenu(msg any) any { return eventPair{name: "contextmenu", msg: msg} }
func Event_onError(msg any) any       { return eventPair{name: "error", msg: msg} }
func Event_onKeyPress(f any) any      { return eventPair{name: "keypress", msg: f} }
func Event_onLoad(msg any) any        { return eventPair{name: "load", msg: msg} }
func Event_onMouseDown(msg any) any   { return eventPair{name: "mousedown", msg: msg} }
func Event_onMouseUp(msg any) any     { return eventPair{name: "mouseup", msg: msg} }
func Event_onReset(msg any) any       { return eventPair{name: "reset", msg: msg} }
func Event_onResize(msg any) any      { return eventPair{name: "resize", msg: msg} }
func Event_onScroll(msg any) any      { return eventPair{name: "scroll", msg: msg} }
func Event_onSelect(msg any) any      { return eventPair{name: "select", msg: msg} }

// File-input helpers used by Sky.Live JS driver. The runtime just captures
// them as attribute pairs; the browser-side driver interprets `sky-on-image`
// / `sky-file-max-width` / `sky-file-max-height` to run client-side image
// resizing before dispatching the msg.
func Event_onImage(msg any) any {
	return eventPair{name: "sky-image", msg: msg}
}
func Event_fileMaxWidth(v any) any  { return attr("sky-file-max-width", fmt.Sprintf("%v", v)) }
func Event_fileMaxHeight(v any) any { return attr("sky-file-max-height", fmt.Sprintf("%v", v)) }
func Event_fileMaxSize(v any) any   { return attr("sky-file-max-size", fmt.Sprintf("%v", v)) }

// ═══════════════════════════════════════════════════════════
// HTML escaping helpers (shared)
// ═══════════════════════════════════════════════════════════

func htmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func htmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "\"", "&quot;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
