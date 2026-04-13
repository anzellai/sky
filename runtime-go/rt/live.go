package rt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════
// VNode — virtual DOM
// ═══════════════════════════════════════════════════════════

type VNode struct {
	Kind     string // "element" | "text" | "raw"
	Tag      string
	Text     string
	Attrs    map[string]string
	Events   map[string]any // event name -> Sky Msg value
	Children []VNode
	// SkyID is a per-element stable key assigned by assignSkyIDs before
	// rendering. Used by the diff protocol to address patch targets.
	SkyID string
}

func vtext(s string) VNode {
	return VNode{Kind: "text", Text: s}
}

func velement(tag string, attrs []any, children []any) VNode {
	node := VNode{
		Kind:   "element",
		Tag:    tag,
		Attrs:  map[string]string{},
		Events: map[string]any{},
	}
	for _, a := range attrs {
		switch v := a.(type) {
		case attrPair:
			node.Attrs[v.key] = v.val
		case eventPair:
			node.Events[v.name] = v.msg
		}
	}
	for _, c := range children {
		switch v := c.(type) {
		case VNode:
			node.Children = append(node.Children, v)
		case string:
			node.Children = append(node.Children, vtext(v))
		}
	}
	return node
}

type attrPair struct{ key, val string }
type eventPair struct {
	name string
	msg  any
}

// ═══════════════════════════════════════════════════════════
// HTML element builders (Std.Html)
// ═══════════════════════════════════════════════════════════

func htmlElem(tag string) func(any, any) any {
	return func(attrs any, children any) any {
		return velement(tag, asList(attrs), asList(children))
	}
}

func asList(v any) []any {
	if v == nil {
		return nil
	}
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

func Html_text(s any) any   { return vtext(fmt.Sprintf("%v", s)) }
func Html_div(a, c any) any { return htmlElem("div")(a, c) }
func Html_span(a, c any) any {
	return htmlElem("span")(a, c)
}
func Html_p(a, c any) any      { return htmlElem("p")(a, c) }
func Html_h1(a, c any) any     { return htmlElem("h1")(a, c) }
func Html_h2(a, c any) any     { return htmlElem("h2")(a, c) }
func Html_h3(a, c any) any     { return htmlElem("h3")(a, c) }
func Html_h4(a, c any) any     { return htmlElem("h4")(a, c) }
func Html_h5(a, c any) any     { return htmlElem("h5")(a, c) }
func Html_h6(a, c any) any     { return htmlElem("h6")(a, c) }
func Html_a(a, c any) any      { return htmlElem("a")(a, c) }
func Html_button(a, c any) any { return htmlElem("button")(a, c) }
// input is void in HTML — no children. Sky API takes attrs only.
func Html_input(a any) any { return htmlElem("input")(a, nil) }
func Html_form(a, c any) any   { return htmlElem("form")(a, c) }
func Html_label(a, c any) any  { return htmlElem("label")(a, c) }
func Html_nav(a, c any) any    { return htmlElem("nav")(a, c) }
func Html_section(a, c any) any {
	return htmlElem("section")(a, c)
}
func Html_article(a, c any) any { return htmlElem("article")(a, c) }
func Html_header(a, c any) any  { return htmlElem("header")(a, c) }
func Html_footer(a, c any) any  { return htmlElem("footer")(a, c) }
func Html_main(a, c any) any    { return htmlElem("main")(a, c) }
func Html_ul(a, c any) any      { return htmlElem("ul")(a, c) }
func Html_ol(a, c any) any      { return htmlElem("ol")(a, c) }
func Html_li(a, c any) any      { return htmlElem("li")(a, c) }
// img is a void element — emit as self-closing, attrs only.
func Html_img(a any) any        { return htmlElem("img")(a, nil) }
func Html_br(a any) any         { return htmlElem("br")(a, nil) }
func Html_hr(a any) any         { return htmlElem("hr")(a, nil) }
func Html_table(a, c any) any   { return htmlElem("table")(a, c) }
func Html_thead(a, c any) any   { return htmlElem("thead")(a, c) }
func Html_tbody(a, c any) any   { return htmlElem("tbody")(a, c) }
func Html_tr(a, c any) any      { return htmlElem("tr")(a, c) }
func Html_th(a, c any) any      { return htmlElem("th")(a, c) }
func Html_td(a, c any) any      { return htmlElem("td")(a, c) }
func Html_textarea(a, c any) any {
	return htmlElem("textarea")(a, c)
}
func Html_select(a, c any) any { return htmlElem("select")(a, c) }
func Html_option(a, c any) any { return htmlElem("option")(a, c) }
func Html_pre(a, c any) any    { return htmlElem("pre")(a, c) }
func Html_code(a, c any) any   { return htmlElem("code")(a, c) }
func Html_strong(a, c any) any { return htmlElem("strong")(a, c) }
func Html_em(a, c any) any     { return htmlElem("em")(a, c) }
func Html_small(a, c any) any  { return htmlElem("small")(a, c) }

// styleNode: render CSS text inside a <style> tag
func Html_styleNode(attrs any, css any) any {
	txt := fmt.Sprintf("%v", css)
	// CSS inside <style> is parsed by the browser's CSS engine, which does
	// NOT decode HTML entities. Wrap as raw so renderVNode emits literal
	// characters (including single quotes, `<`, `>` — none of which can
	// terminate a <style> block except the literal text `</style>`).
	return VNode{
		Kind:     "element",
		Tag:      "style",
		Attrs:    map[string]string{},
		Children: []VNode{{Kind: "raw", Text: txt}},
	}
}

// node: generic element builder for tags that don't have a dedicated helper
// (e.g. "svg", "polyline").
func Html_node(tag any, attrs any, children any) any {
	return velement(fmt.Sprintf("%v", tag), asList(attrs), asList(children))
}

// raw: insert unescaped HTML — used for trusted content like pre-rendered markdown
func Html_raw(s any) any {
	return VNode{
		Kind: "raw",
		Text: fmt.Sprintf("%v", s),
	}
}

// headerNode: specialised header tag with attrs + children (same as Html_header,
// kept as a distinct entry for legacy-stdlib compat).
func Html_headerNode(attrs any, children any) any {
	return htmlElem("header")(attrs, children)
}

// Extra Html elements used by some legacy stdlib code.
func Html_codeNode(a, c any) any    { return htmlElem("code")(a, c) }
func Html_blockquote(a, c any) any  { return htmlElem("blockquote")(a, c) }
func Html_figure(a, c any) any      { return htmlElem("figure")(a, c) }
func Html_figcaption(a, c any) any  { return htmlElem("figcaption")(a, c) }
func Html_details(a, c any) any     { return htmlElem("details")(a, c) }
func Html_summary(a, c any) any     { return htmlElem("summary")(a, c) }
func Html_dialog(a, c any) any      { return htmlElem("dialog")(a, c) }
func Html_video(a, c any) any       { return htmlElem("video")(a, c) }
func Html_audio(a, c any) any       { return htmlElem("audio")(a, c) }
func Html_canvas(a, c any) any      { return htmlElem("canvas")(a, c) }
func Html_iframe(a, c any) any      { return htmlElem("iframe")(a, c) }
func Html_progress(a, c any) any    { return htmlElem("progress")(a, c) }
func Html_meter(a, c any) any       { return htmlElem("meter")(a, c) }

// ═══════════════════════════════════════════════════════════
// Attributes (Std.Html.Attributes)
// ═══════════════════════════════════════════════════════════

func attr(k, v string) any          { return attrPair{key: k, val: v} }
func Attr_class(v any) any          { return attr("class", fmt.Sprintf("%v", v)) }
func Attr_id(v any) any             { return attr("id", fmt.Sprintf("%v", v)) }
func Attr_style(v any) any          { return attr("style", fmt.Sprintf("%v", v)) }
func Attr_type(v any) any           { return attr("type", fmt.Sprintf("%v", v)) }
func Attr_value(v any) any          { return attr("value", fmt.Sprintf("%v", v)) }
func Attr_href(v any) any           { return attr("href", fmt.Sprintf("%v", v)) }
func Attr_src(v any) any            { return attr("src", fmt.Sprintf("%v", v)) }
func Attr_alt(v any) any            { return attr("alt", fmt.Sprintf("%v", v)) }
func Attr_name(v any) any           { return attr("name", fmt.Sprintf("%v", v)) }
func Attr_placeholder(v any) any    { return attr("placeholder", fmt.Sprintf("%v", v)) }
func Attr_title(v any) any          { return attr("title", fmt.Sprintf("%v", v)) }
func Attr_for(v any) any            { return attr("for", fmt.Sprintf("%v", v)) }
func Attr_checked(v any) any        { return attr("checked", "checked") }
func Attr_disabled(v any) any       { return attr("disabled", "disabled") }
func Attr_readonly(v any) any       { return attr("readonly", "readonly") }
func Attr_required(v any) any       { return attr("required", "required") }
func Attr_autofocus(v any) any      { return attr("autofocus", "autofocus") }
func Attr_rel(v any) any            { return attr("rel", fmt.Sprintf("%v", v)) }
func Attr_target(v any) any         { return attr("target", fmt.Sprintf("%v", v)) }
func Attr_method(v any) any         { return attr("method", fmt.Sprintf("%v", v)) }
func Attr_action(v any) any         { return attr("action", fmt.Sprintf("%v", v)) }

// ═══════════════════════════════════════════════════════════
// Events (Std.Live.Events)
// ═══════════════════════════════════════════════════════════

func Event_onClick(msg any) any  { return eventPair{name: "click", msg: msg} }
func Event_onInput(f any) any    { return eventPair{name: "input", msg: f} }
func Event_onChange(f any) any   { return eventPair{name: "change", msg: f} }
func Event_onSubmit(msg any) any { return eventPair{name: "submit", msg: msg} }
func Event_onDblClick(msg any) any { return eventPair{name: "dblclick", msg: msg} }
func Event_onMouseOver(msg any) any { return eventPair{name: "mouseover", msg: msg} }
func Event_onMouseOut(msg any) any  { return eventPair{name: "mouseout", msg: msg} }
func Event_onKeyDown(f any) any     { return eventPair{name: "keydown", msg: f} }
func Event_onKeyUp(f any) any       { return eventPair{name: "keyup", msg: f} }
func Event_onFocus(msg any) any     { return eventPair{name: "focus", msg: msg} }
func Event_onBlur(msg any) any      { return eventPair{name: "blur", msg: msg} }

// Attr_attribute: generic attribute builder for tags with non-standard attrs
// (e.g. SVG viewBox).
func Attr_attribute(k any, v any) any {
	return attr(fmt.Sprintf("%v", k), fmt.Sprintf("%v", v))
}

// Form / number / a11y / data attributes.
func Attr_rows(v any) any        { return attr("rows", fmt.Sprintf("%v", v)) }
func Attr_cols(v any) any        { return attr("cols", fmt.Sprintf("%v", v)) }
func Attr_maxlength(v any) any   { return attr("maxlength", fmt.Sprintf("%v", v)) }
func Attr_minlength(v any) any   { return attr("minlength", fmt.Sprintf("%v", v)) }
func Attr_step(v any) any        { return attr("step", fmt.Sprintf("%v", v)) }
func Attr_min(v any) any         { return attr("min", fmt.Sprintf("%v", v)) }
func Attr_max(v any) any         { return attr("max", fmt.Sprintf("%v", v)) }
func Attr_pattern(v any) any     { return attr("pattern", fmt.Sprintf("%v", v)) }
func Attr_accept(v any) any      { return attr("accept", fmt.Sprintf("%v", v)) }
func Attr_multiple(v any) any    { return attr("multiple", fmt.Sprintf("%v", v)) }
func Attr_size(v any) any        { return attr("size", fmt.Sprintf("%v", v)) }
func Attr_tabindex(v any) any    { return attr("tabindex", fmt.Sprintf("%v", v)) }
func Attr_ariaLabel(v any) any   { return attr("aria-label", fmt.Sprintf("%v", v)) }
func Attr_ariaHidden(v any) any  { return attr("aria-hidden", fmt.Sprintf("%v", v)) }
func Attr_role(v any) any        { return attr("role", fmt.Sprintf("%v", v)) }
func Attr_dataAttr(k, v any) any { return attr("data-"+fmt.Sprintf("%v", k), fmt.Sprintf("%v", v)) }
func Attr_spellcheck(v any) any  { return attr("spellcheck", fmt.Sprintf("%v", v)) }
func Attr_dir(v any) any         { return attr("dir", fmt.Sprintf("%v", v)) }
func Attr_lang(v any) any        { return attr("lang", fmt.Sprintf("%v", v)) }
func Attr_translate(v any) any   { return attr("translate", fmt.Sprintf("%v", v)) }

// ═══════════════════════════════════════════════════════════
// CSS (Std.Css)
// ═══════════════════════════════════════════════════════════

type cssRule struct {
	selector string
	props    []cssProp
}
type cssProp struct {
	k, v string
}

func Css_stylesheet(rules any) any {
	rs := asList(rules)
	var sb strings.Builder
	for _, r := range rs {
		renderCssRule(&sb, r)
	}
	return sb.String()
}

// renderCssRule handles the three rule shapes (cssRule, cssMediaRule,
// cssKeyframesRule) plus plain strings (already-rendered fragments from
// legacy-style APIs) and nested []any lists.
func renderCssRule(sb *strings.Builder, r any) {
	switch cr := r.(type) {
	case cssRule:
		sb.WriteString(cr.selector)
		sb.WriteString(" {\n")
		for _, p := range cr.props {
			sb.WriteString("  ")
			sb.WriteString(p.k)
			sb.WriteString(": ")
			sb.WriteString(p.v)
			sb.WriteString(";\n")
		}
		sb.WriteString("}\n")
	case cssMediaRule:
		sb.WriteString("@media ")
		sb.WriteString(cr.query)
		sb.WriteString(" {\n")
		for _, inner := range asList(cr.rules) {
			renderCssRule(sb, inner)
		}
		sb.WriteString("}\n")
	case cssKeyframesRule:
		sb.WriteString("@keyframes ")
		sb.WriteString(cr.name)
		sb.WriteString(" { ")
		for _, f := range cr.frames {
			sb.WriteString(f)
			sb.WriteString(" ")
		}
		sb.WriteString("}\n")
	case string:
		sb.WriteString(cr)
		if !strings.HasSuffix(cr, "\n") {
			sb.WriteString("\n")
		}
	case []any:
		for _, inner := range cr {
			renderCssRule(sb, inner)
		}
	}
}

func Css_rule(selector any, props any) any {
	ps := asList(props)
	var out []cssProp
	for _, p := range ps {
		if cp, ok := p.(cssProp); ok {
			out = append(out, cp)
		}
	}
	return cssRule{selector: fmt.Sprintf("%v", selector), props: out}
}

func Css_property(k any, v any) any {
	return cssProp{k: fmt.Sprintf("%v", k), v: fmt.Sprintf("%v", v)}
}

// Unit helpers
func Css_px(n any) any  { return fmt.Sprintf("%vpx", n) }
func Css_rem(n any) any { return fmt.Sprintf("%vrem", n) }
func Css_em(n any) any  { return fmt.Sprintf("%vem", n) }
func Css_pct(n any) any { return fmt.Sprintf("%v%%", n) }
func Css_hex(s any) any { return fmt.Sprintf("#%v", s) }

// Common property shortcuts (name in Sky = lowerCamel → Css_<name>)
func cssP(k string) func(any) any {
	return func(v any) any { return cssProp{k: k, v: fmt.Sprintf("%v", v)} }
}
func cssP2(k string) func(any, any) any {
	return func(a, b any) any { return cssProp{k: k, v: fmt.Sprintf("%v %v", a, b)} }
}

var (
	Css_color           = cssP("color")
	Css_background      = cssP("background")
	Css_backgroundColor = cssP("background-color")
	Css_padding         = cssP("padding")
	Css_padding2        = cssP2("padding")
	Css_margin          = cssP("margin")
	Css_margin2         = cssP2("margin")
	Css_fontSize        = cssP("font-size")
	Css_fontWeight      = cssP("font-weight")
	Css_fontFamily      = cssP("font-family")
	Css_lineHeight      = cssP("line-height")
	Css_textAlign       = cssP("text-align")
	Css_border          = cssP("border")
	Css_borderRadius    = cssP("border-radius")
	Css_borderBottom    = cssP("border-bottom")
	Css_display         = cssP("display")
	Css_cursor          = cssP("cursor")
	Css_gap             = cssP("gap")
	Css_justifyContent  = cssP("justify-content")
	Css_alignItems      = cssP("align-items")
	Css_width           = cssP("width")
	Css_height          = cssP("height")
	Css_maxWidth        = cssP("max-width")
	Css_minWidth        = cssP("min-width")
	Css_transform       = cssP("transform")
	Css_textDecoration  = cssP("text-decoration")
	Css_zIndex          = cssP("z-index")
	Css_opacity         = cssP("opacity")
	Css_overflow        = cssP("overflow")
	Css_overflowY       = cssP("overflow-y")
	Css_overflowX       = cssP("overflow-x")
	Css_top             = cssP("top")
	Css_bottom          = cssP("bottom")
	Css_left            = cssP("left")
	Css_right           = cssP("right")
	Css_position        = cssP("position")
	Css_transition      = cssP("transition")
	Css_animation       = cssP("animation")
	Css_boxShadow       = cssP("box-shadow")
	Css_outline         = cssP("outline")
	Css_backgroundImage = cssP("background-image")
	Css_whiteSpace      = cssP("white-space")
	Css_wordBreak       = cssP("word-break")
	Css_lineClamp       = cssP("line-clamp")
	Css_flexDirection   = cssP("flex-direction")
	Css_flexWrap        = cssP("flex-wrap")
	Css_alignContent    = cssP("align-content")
	Css_gridTemplateColumns = cssP("grid-template-columns")
	Css_gridTemplateRows    = cssP("grid-template-rows")
	Css_gridGap             = cssP("grid-gap")
	Css_borderTop    = cssP("border-top")
	Css_borderLeft   = cssP("border-left")
	Css_borderRight  = cssP("border-right")
	Css_letterSpacing = cssP("letter-spacing")
	Css_userSelect   = cssP("user-select")
	Css_fontStyle    = cssP("font-style")
	Css_maxHeight    = cssP("max-height")
	Css_minHeight    = cssP("min-height")
	Css_borderColor  = cssP("border-color")
	Css_flex         = cssP("flex")
	Css_flexGrow     = cssP("flex-grow")
	Css_flexShrink   = cssP("flex-shrink")
	Css_flexBasis    = cssP("flex-basis")
	Css_gridColumn   = cssP("grid-column")
	Css_gridRow      = cssP("grid-row")
	Css_rowGap       = cssP("row-gap")
	Css_columnGap   = cssP("column-gap")
	Css_borderCollapse = cssP("border-collapse")
	Css_borderSpacing  = cssP("border-spacing")
	Css_marginTop     = cssP("margin-top")
	Css_marginBottom  = cssP("margin-bottom")
	Css_marginLeft    = cssP("margin-left")
	Css_marginRight   = cssP("margin-right")
	Css_paddingTop    = cssP("padding-top")
	Css_paddingBottom = cssP("padding-bottom")
	Css_paddingLeft   = cssP("padding-left")
	Css_paddingRight  = cssP("padding-right")
	Css_visibility    = cssP("visibility")
	Css_content       = cssP("content")
	Css_auto          = cssP("auto")
	Css_none          = func(_ any) any { return "none" }
	Css_transparent   = func(_ any) any { return "transparent" }
	Css_inherit       = func(_ any) any { return "inherit" }
	Css_initial       = func(_ any) any { return "initial" }
	Css_monoFont      = func(_ any) any { return "ui-monospace, 'SF Mono', Monaco, 'Cascadia Code', monospace" }
	Css_transitionDuration = cssP("transition-duration")
	Css_transitionTimingFunction = cssP("transition-timing-function")
	Css_outlineOffset = cssP("outline-offset")
	Css_filter        = cssP("filter")
	Css_backdropFilter = cssP("backdrop-filter")
	Css_pointerEvents = cssP("pointer-events")
	Css_userSelectNone = func(_ any) any { return "none" }
	Css_objectFit     = cssP("object-fit")
	Css_objectPosition = cssP("object-position")
	Css_backgroundSize = cssP("background-size")
	Css_backgroundPosition = cssP("background-position")
	Css_backgroundRepeat = cssP("background-repeat")
	Css_listStyle     = cssP("list-style")
	Css_listStyleType = cssP("list-style-type")
	Css_listStylePosition = cssP("list-style-position")
	Css_verticalAlign = cssP("vertical-align")
	Css_boxSizing    = cssP("box-sizing")
)

// Zero-arg CSS values take a unit param to match Sky's `Css.zero ()` call form.
func Css_borderBox(_ any) any  { return "border-box" }
func Css_zero(_ any) any       { return "0" }
func Css_systemFont(_ any) any { return "-apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif" }

// rgba(r,g,b,a) -> "rgba(r, g, b, a)"
func Css_rgba(r, g, b, a any) any {
	return fmt.Sprintf("rgba(%v, %v, %v, %v)", r, g, b, a)
}

// transitionProp(property, duration, timing) -> "property duration timing"
func Css_transitionProp(p, d, t any) any {
	return cssProp{k: "transition", v: fmt.Sprintf("%v %vs %v", p, d, t)}
}

// linearGradient(angle, stops) -> "linear-gradient(angle, stop1, stop2, ...)"
func Css_linearGradient(angle, stops any) any {
	var parts []string
	if xs, ok := stops.([]any); ok {
		for _, s := range xs {
			parts = append(parts, fmt.Sprintf("%v", s))
		}
	}
	return fmt.Sprintf("linear-gradient(%v, %s)", angle, strings.Join(parts, ", "))
}

// repeat(n, template) -> "repeat(n, template)"
func Css_repeat(n, t any) any {
	return fmt.Sprintf("repeat(%v, %v)", n, t)
}

// fr(n) -> "Nfr" (grid template unit)
func Css_fr(n any) any {
	return fmt.Sprintf("%vfr", n)
}

// textTransform alias for Css_property("text-transform", v)
func Css_textTransform(v any) any {
	return cssProp{k: "text-transform", v: fmt.Sprintf("%v", v)}
}

// Css_margin4(top, right, bottom, left)
func Css_margin4(t, r, b, l any) any {
	return cssProp{k: "margin", v: fmt.Sprintf("%v %v %v %v", t, r, b, l)}
}

// Css_fontStyle(v)
func Css_fontStyle2(v any) any {
	return cssProp{k: "font-style", v: fmt.Sprintf("%v", v)}
}

// Css_styles: bulk merge — takes a list of property pairs, serialises them
// as a single style="a:b;c:d;" string for placement on an element.
func Css_styles(rules any) any {
	var parts []string
	if xs, ok := rules.([]any); ok {
		for _, r := range xs {
			if cp, ok := r.(cssProp); ok {
				parts = append(parts, cp.k+":"+cp.v)
			}
		}
	}
	return strings.Join(parts, ";")
}

// Html_doctype — emit a plain <!DOCTYPE html> root that wraps children.
func Html_doctype(children any) any {
	return velement("!doctype-wrapper", nil, asList(children))
}

func Html_htmlNode(a, c any) any { return htmlElem("html")(a, c) }
func Html_headNode(a, c any) any { return htmlElem("head")(a, c) }
func Html_body(a, c any) any     { return htmlElem("body")(a, c) }
func Html_title(a, c any) any    { return htmlElem("title")(a, c) }
func Html_meta(a any) any        { return htmlElem("meta")(a, nil) }
func Html_link(a any) any        { return htmlElem("link")(a, nil) }
func Html_script(a, c any) any   { return htmlElem("script")(a, c) }

// Html_titleNode — takes a raw string and wraps it in <title>.
func Html_titleNode(s any) any {
	return htmlElem("title")(nil, []any{Html_text(s)})
}

// Html_render: serialise a VNode to HTML string (for server-side rendering).
func Html_render(node any) any {
	if vn, ok := node.(VNode); ok {
		return renderVNode(vn, nil)
	}
	return ""
}

// Attr_charset / httpEquiv / content / rel — meta-tag friends.
func Attr_charset(v any) any   { return attr("charset", fmt.Sprintf("%v", v)) }
func Attr_httpEquiv(v any) any { return attr("http-equiv", fmt.Sprintf("%v", v)) }
func Attr_content(v any) any   { return attr("content", fmt.Sprintf("%v", v)) }

// shadow(offX, offY, blur, colour) -> a short-hand box-shadow value string
func Css_shadow(offX, offY, blur, colour any) any {
	return fmt.Sprintf("%v %v %v %v", offX, offY, blur, colour)
}

// media("(max-width: 640px)", rules) -> wraps rules under a media query
func Css_media(query any, rules any) any {
	return cssMediaRule{query: fmt.Sprintf("%v", query), rules: rules}
}

type cssMediaRule struct {
	query string
	rules any
}

// ═══════════════════════════════════════════════════════════
// VNode rendering
// ═══════════════════════════════════════════════════════════

func renderVNode(n VNode, handlers map[string]any) string {
	if n.Kind == "text" {
		return html.EscapeString(n.Text)
	}
	if n.Kind == "raw" {
		return n.Text
	}
	var sb strings.Builder
	sb.WriteString("<")
	sb.WriteString(n.Tag)
	// Stamp the element with its sky-id so diff patches can address it.
	if n.SkyID != "" {
		sb.WriteString(` sky-id="`)
		sb.WriteString(html.EscapeString(n.SkyID))
		sb.WriteString(`"`)
	}
	for k, v := range n.Attrs {
		sb.WriteString(" ")
		sb.WriteString(k)
		sb.WriteString(`="`)
		sb.WriteString(html.EscapeString(v))
		sb.WriteString(`"`)
	}
	for ev, msg := range n.Events {
		id := randID()
		handlers[id] = msg
		// Only emit as a native DOM event handler when the name is a
		// valid identifier. Custom driver events (e.g. `sky-image`,
		// `file-uploaded`) get stamped as a `data-sky-ev-<name>` hook
		// attribute for client JS to wire up.
		if isDOMEventName(ev) {
			sb.WriteString(fmt.Sprintf(` on%s="skyEvent(event,'%s')"`, ev, id))
		} else {
			sb.WriteString(fmt.Sprintf(` data-sky-ev-%s="%s"`,
				html.EscapeString(ev), id))
		}
	}
	if isVoidTag(n.Tag) {
		sb.WriteString(" />")
		return sb.String()
	}
	sb.WriteString(">")
	for _, c := range n.Children {
		sb.WriteString(renderVNode(c, handlers))
	}
	sb.WriteString("</")
	sb.WriteString(n.Tag)
	sb.WriteString(">")
	return sb.String()
}

// isDOMEventName: true when `ev` is a plain lowercase identifier safe
// to embed in `on<name>=`. Rejects hyphens, dots, digits-first, etc.
func isDOMEventName(ev string) bool {
	if ev == "" {
		return false
	}
	for i := 0; i < len(ev); i++ {
		c := ev[i]
		if !(c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}


// assignSkyIDs walks a tree and stamps every element (not text/raw) with
// a deterministic structural path id. Having stable IDs means the diff
// algorithm can address a specific element between renders without us
// having to rely on React-style key props.
func assignSkyIDs(n *VNode, path string) {
	if n.Kind != "element" {
		return
	}
	n.SkyID = path
	for i := range n.Children {
		assignSkyIDs(&n.Children[i], path+"."+itoa(i))
	}
}


func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}


// VNode equality — compare without recursing on SkyID (since that's
// assigned per render). Two nodes are attribute-equal if their tag,
// attributes, and events match; children are compared structurally.
func vnodeEqualShallow(a, b *VNode) bool {
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Text != b.Text {
		return false
	}
	if len(a.Attrs) != len(b.Attrs) {
		return false
	}
	for k, v := range a.Attrs {
		if b.Attrs[k] != v {
			return false
		}
	}
	return true
}


// Patch describes one DOM mutation the client will apply.
type Patch struct {
	ID     string            `json:"id"`               // target element's sky-id
	Text   *string           `json:"text,omitempty"`
	HTML   *string           `json:"html,omitempty"`
	Attrs  map[string]string `json:"attrs,omitempty"`  // value "" => remove
	Remove bool              `json:"remove,omitempty"`
}


// diffTrees: produce patches to transform `old` into `new_`. If either
// tree is missing (first render) the caller should fall back to a full
// innerHTML replace — diffTrees returns a single patch with the full
// new HTML.
func diffTrees(old, new_ *VNode) []Patch {
	var out []Patch
	diffNodes(old, new_, &out)
	return out
}


func diffNodes(old, new_ *VNode, out *[]Patch) {
	if old == nil || new_ == nil {
		return
	}
	// Tag / kind change → replace subtree via HTML patch.
	if old.Tag != new_.Tag || old.Kind != new_.Kind {
		html := renderVNode(*new_, map[string]any{})
		*out = append(*out, Patch{ID: old.SkyID, HTML: &html})
		return
	}
	// Attrs diff
	var attrChanges map[string]string
	for k, nv := range new_.Attrs {
		if ov, ok := old.Attrs[k]; !ok || ov != nv {
			if attrChanges == nil {
				attrChanges = map[string]string{}
			}
			attrChanges[k] = nv
		}
	}
	for k := range old.Attrs {
		if _, ok := new_.Attrs[k]; !ok {
			if attrChanges == nil {
				attrChanges = map[string]string{}
			}
			attrChanges[k] = ""
		}
	}
	if attrChanges != nil && old.SkyID != "" {
		*out = append(*out, Patch{ID: old.SkyID, Attrs: attrChanges})
	}

	// Single-text-child fast path — common for buttons / spans.
	if len(old.Children) == 1 && len(new_.Children) == 1 &&
		old.Children[0].Kind == "text" && new_.Children[0].Kind == "text" {
		if old.Children[0].Text != new_.Children[0].Text && old.SkyID != "" {
			txt := new_.Children[0].Text
			*out = append(*out, Patch{ID: old.SkyID, Text: &txt})
		}
		return
	}

	// Structural diff of children: if counts differ OR any child pair
	// has mismatched tag/kind, replace the whole subtree's innerHTML.
	if len(old.Children) != len(new_.Children) {
		if old.SkyID != "" {
			var sb strings.Builder
			dummy := map[string]any{}
			for _, c := range new_.Children {
				sb.WriteString(renderVNode(c, dummy))
			}
			html := sb.String()
			*out = append(*out, Patch{ID: old.SkyID, HTML: &html})
		}
		return
	}

	for i := range old.Children {
		oc := &old.Children[i]
		nc := &new_.Children[i]
		if oc.Kind == "text" && nc.Kind == "text" {
			if oc.Text != nc.Text && old.SkyID != "" {
				// Single-text is above; mixed children = replace subtree.
				var sb strings.Builder
				dummy := map[string]any{}
				for _, c := range new_.Children {
					sb.WriteString(renderVNode(c, dummy))
				}
				html := sb.String()
				*out = append(*out, Patch{ID: old.SkyID, HTML: &html})
				return
			}
			continue
		}
		if oc.Tag != nc.Tag || oc.Kind != nc.Kind {
			// Tag mismatch: replace subtree at the parent.
			if old.SkyID != "" {
				var sb strings.Builder
				dummy := map[string]any{}
				for _, c := range new_.Children {
					sb.WriteString(renderVNode(c, dummy))
				}
				html := sb.String()
				*out = append(*out, Patch{ID: old.SkyID, HTML: &html})
			}
			return
		}
		diffNodes(oc, nc, out)
	}
}


func isVoidTag(t string) bool {
	switch t {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func randID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ═══════════════════════════════════════════════════════════
// Std.Cmd / Std.Sub
// ═══════════════════════════════════════════════════════════

type cmdT struct {
	kind string // "none", "perform", "batch"
	task any
	toMsg any
	batch []any
}

type subT struct {
	kind   string // "none", "every"
	ms     int
	toMsg  any
}

func Cmd_none() any             { return cmdT{kind: "none"} }
func Cmd_batch(list any) any    { return cmdT{kind: "batch", batch: asList(list)} }
func Cmd_perform(task, to any) any { return cmdT{kind: "perform", task: task, toMsg: to} }

func Sub_none() any { return subT{kind: "none"} }
func Sub_every(ms any, to any) any {
	return subT{kind: "every", ms: AsInt(ms), toMsg: to}
}

// Time.every is an alias of Sub.every in Sky code
func Time_every(ms any, to any) any { return Sub_every(ms, to) }

// ═══════════════════════════════════════════════════════════
// Std.Live — HTTP-first server-driven UI with TEA architecture
// ═══════════════════════════════════════════════════════════

type liveSession struct {
	model    any
	handlers map[string]any
	prevTree *VNode // Last rendered tree; used by the diff protocol.
	lastSeen time.Time
	mu       sync.Mutex
	// SSE outbound channel: any writer goroutine may push an HTML patch
	sseCh chan string
	// Cancel function for any active subscription ticker
	cancelSub chan struct{}
}

type liveApp struct {
	init          any // req -> (Model, Cmd Msg)
	update        any // Msg -> Model -> (Model, Cmd Msg)
	view          any // Model -> VNode
	subscriptions any // Model -> Sub Msg
	routes        []liveRoute
	notFound      any
	guard         any // Maybe (Msg -> Model -> Result String ()) — nil = no guard
	api           []apiRoute  // REST-style custom handlers alongside Live pages
	staticDir     string      // Serves files from this directory under /static/…
	staticURL     string      // URL mount prefix (default "/static")
	store         SessionStore // sessionID -> *liveSession (memory, sqlite, or postgres)
}


// apiRoute represents a custom handler mounted outside the TEA cycle.
// Created from Sky code via `Live.api "GET /webhook/stripe" handleStripe`.
// The Sky-side handler has signature `Request -> Task String Response`
// (the same shape Sky.Http.Server uses). The runtime constructs the
// request map and serialises the response.
type apiRoute struct {
	method  string // "GET", "POST", ...  or "" for any
	pattern string // /path with :param placeholders
	handler any    // Sky function Request -> Task String Response
}

type liveRoute struct {
	path string
	page any
}

// Route constructor
func Live_route(path any, page any) any {
	return liveRoute{path: fmt.Sprintf("%v", path), page: page}
}


// Live_api registers a custom HTTP handler outside the TEA cycle. Used
// for OAuth callbacks, webhooks, REST endpoints that coexist with a
// Live app. The Sky-side handler has signature
//   Request -> Task String Response
// mirroring Sky.Http.Server.
//
// `spec` is a pattern string like "GET /webhook/stripe" or
// "POST /api/upload". No method prefix = match any method.
func Live_api(spec any, handler any) any {
	s := fmt.Sprintf("%v", spec)
	method, pattern := "", s
	if idx := strings.Index(s, " "); idx > 0 {
		method = s[:idx]
		pattern = strings.TrimSpace(s[idx+1:])
	}
	return apiRoute{method: method, pattern: pattern, handler: handler}
}


// dispatchRoot routes a request to:
//   1. a matching apiRoute (REST handler), OR
//   2. handleInitial (Live page render).
func (app *liveApp) dispatchRoot(w http.ResponseWriter, r *http.Request) {
	for _, ar := range app.api {
		if ar.method != "" && !strings.EqualFold(ar.method, r.Method) {
			continue
		}
		if params, ok := matchRoute(ar.pattern, r.URL.Path); ok {
			app.serveAPI(ar, params, w, r)
			return
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		app.handleInitial(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}


// serveAPI calls the Sky handler with a Request-like map and renders
// the returned Response.
func (app *liveApp) serveAPI(ar apiRoute, params []string, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	req := map[string]any{
		"method": r.Method,
		"path":   r.URL.Path,
		"query":  r.URL.RawQuery,
		"body":   string(body),
		"params": params,
		"headers": func() map[string]any {
			m := map[string]any{}
			for k, v := range r.Header {
				if len(v) > 0 {
					m[k] = v[0]
				}
			}
			return m
		}(),
	}
	result := sky_call(ar.handler, req)
	// Accept either a rendered response map {status, headers, body} or
	// a bare string body (defaults to 200 text/plain).
	status, headers, respBody := unpackResponse(result)
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(status)
	w.Write([]byte(respBody))
}


func unpackResponse(v any) (int, map[string]string, string) {
	// Sky.Http.Server Response shape:
	//   record { status : Int, headers : Dict String String, body : String }
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Struct {
		status := 200
		headers := map[string]string{}
		body := ""
		if f := rv.FieldByName("Status"); f.IsValid() {
			status = AsInt(f.Interface())
		}
		if f := rv.FieldByName("Body"); f.IsValid() {
			body = fmt.Sprintf("%v", f.Interface())
		}
		if f := rv.FieldByName("Headers"); f.IsValid() {
			if m, ok := f.Interface().(map[string]any); ok {
				for k, val := range m {
					headers[k] = fmt.Sprintf("%v", val)
				}
			}
		}
		return status, headers, body
	}
	// Fallback: treat as raw body.
	return 200, nil, fmt.Sprintf("%v", v)
}


// applyRoute matches `urlPath` against app.routes and returns a new
// model with its Page field set to the matching route's page (or
// app.notFound when no route matches).
//
// Route patterns support `:name` segments (e.g. `/product/:id`). When
// a pattern has any path params, the matched page value is an ADT
// constructor function; we reflect-call it with the captured values
// in declaration order. Static routes just take the page as-is.
func applyRoute(app *liveApp, model any, urlPath string) any {
	for _, rt := range app.routes {
		if params, ok := matchRoute(rt.path, urlPath); ok {
			page := fillRoutePage(rt.page, params)
			return RecordUpdate(model, map[string]any{"Page": page})
		}
	}
	if app.notFound != nil {
		return RecordUpdate(model, map[string]any{"Page": app.notFound})
	}
	return model
}


// matchRoute compares a pattern like `/product/:id` against an incoming
// path. Returns the ordered list of captured segment values on success.
func matchRoute(pattern, path string) ([]string, bool) {
	patSegs := splitPath(pattern)
	pathSegs := splitPath(path)
	if len(patSegs) != len(pathSegs) {
		return nil, false
	}
	var params []string
	for i, ps := range patSegs {
		if strings.HasPrefix(ps, ":") {
			params = append(params, pathSegs[i])
		} else if ps != pathSegs[i] {
			return nil, false
		}
	}
	return params, true
}


func splitPath(p string) []string {
	// Trim leading/trailing `/` so `/a/b/` and `/a/b` match the same.
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}


// If a route page is a function (ADT constructor expecting URL params),
// apply the captured params via sky_call; otherwise pass through.
func fillRoutePage(page any, params []string) any {
	if len(params) == 0 || !isFunc(page) {
		return page
	}
	curr := page
	for _, p := range params {
		if !isFunc(curr) {
			break
		}
		curr = sky_call(curr, p)
	}
	return curr
}

// Live.app — reads a record-shaped config and starts the HTTP server.
// Blocks until the server exits.
func Live_app(cfg any) any {
	app := &liveApp{
		init:          Field(cfg, "Init"),
		update:        Field(cfg, "Update"),
		view:          Field(cfg, "View"),
		subscriptions: Field(cfg, "Subscriptions"),
		notFound:      Field(cfg, "NotFound"),
		guard:         Field(cfg, "Guard"),
	}
	for _, r := range asList(Field(cfg, "Routes")) {
		if lr, ok := r.(liveRoute); ok {
			app.routes = append(app.routes, lr)
		}
	}
	// Custom REST-style routes (OAuth callbacks, webhooks, API endpoints).
	for _, r := range asList(Field(cfg, "Api")) {
		if ar, ok := r.(apiRoute); ok {
			app.api = append(app.api, ar)
		}
	}
	// Static file serving. Sky-side: `static = "public"` → serve
	// <cwd>/public/* at /static/*. Mount URL can be overridden with
	// `staticUrl = "/assets"`.
	if sd := Field(cfg, "Static"); sd != nil {
		app.staticDir = fmt.Sprintf("%v", sd)
	} else if v := os.Getenv("SKY_STATIC_DIR"); v != "" {
		app.staticDir = v
	}
	app.staticURL = "/static"
	if su := Field(cfg, "StaticUrl"); su != nil {
		if s := fmt.Sprintf("%v", su); s != "" {
			app.staticURL = s
		}
	}
	// Session store selection. Config fields `store` and `storePath`
	// override the defaults; env vars SKY_LIVE_STORE / SKY_LIVE_STORE_PATH
	// take precedence over config; final fallback is memory.
	storeKind := stringField(cfg, "Store")
	storePath := stringField(cfg, "StorePath")
	ttl := 30 * time.Minute
	if v := os.Getenv("SKY_LIVE_TTL"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			ttl = time.Duration(secs) * time.Second
		}
	}
	app.store = chooseStore(storeKind, storePath, ttl)

	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/_sky/sse", app.handleSSE)
	mux.HandleFunc("/_sky/config", app.handleConfig)
	// Static assets (if configured) mounted first so api/page routing
	// doesn't shadow them.
	if app.staticDir != "" {
		prefix := app.staticURL
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		mux.Handle(prefix,
			http.StripPrefix(prefix, http.FileServer(http.Dir(app.staticDir))))
	}
	// API handler dispatcher — matches method + pattern before page handler.
	mux.HandleFunc("/", app.dispatchRoot)

	port := 8080
	if p := Field(cfg, "Port"); p != nil {
		port = AsInt(p)
	}
	// Allow SKY_LIVE_PORT env var to override (set in .env or shell).
	if v := os.Getenv("SKY_LIVE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}

	// Wrap the mux with panic recovery so one bad handler can't crash the process.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// Log to stderr so `go run` / tailing the server surfaces
				// the actual cause. Client still gets a generic 500.
				fmt.Fprintf(os.Stderr,
					"[sky.live] panic handling %s %s: %v\n%s\n",
					r.Method, r.URL.Path, rec, debugStack())
				w.WriteHeader(500)
				fmt.Fprint(w, "Internal Server Error")
			}
		}()
		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           wrapped,
		ReadHeaderTimeout: 10 * time.Second,
		// IMPORTANT: do not set ReadTimeout or WriteTimeout here — the SSE
		// endpoint needs to stream indefinitely. Per-handler deadlines can be
		// enforced via r.Context() when needed.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	fmt.Printf("Sky.Live listening on :%d\n", port)
	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return Err[any, any](err.Error())
	}
	return Ok[any, any](struct{}{})
}

// setSecurityHeaders applies safe-by-default security headers.
// Callers can still override via SkyResponse.Headers where applicable.
func setSecurityHeaders(h http.Header) {
	if h.Get("X-Content-Type-Options") == "" {
		h.Set("X-Content-Type-Options", "nosniff")
	}
	if h.Get("X-Frame-Options") == "" {
		h.Set("X-Frame-Options", "SAMEORIGIN")
	}
	if h.Get("Referrer-Policy") == "" {
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	}
}

func (app *liveApp) handleInitial(w http.ResponseWriter, r *http.Request) {
	// Build an initial request value for init
	req := map[string]any{"path": r.URL.Path}

	// Run init — returns (Model, Cmd Msg)
	res := sky_call(app.init, req)
	model := tupleFirst(res)
	cmd := tupleSecond(res)

	// Route dispatch: pick the page ADT value for this URL path and
	// splice it into model.Page via RecordUpdate. Without this, every
	// request renders whatever page the user's `init` hard-codes.
	model = applyRoute(app, model, r.URL.Path)

	// Get or create session
	sid := sessionID(r, w)
	sess := &liveSession{
		model:     model,
		handlers:  map[string]any{},
		sseCh:     make(chan string, 16),
		cancelSub: make(chan struct{}),
	}
	app.store.Set(sid, sess)

	// Process any initial Cmd from init
	app.runCmd(sess, cmd)
	// Set up subscriptions
	app.setupSubscriptions(sess)

	// Render view (assign sky-ids so the client diff protocol works)
	vn := sky_call(app.view, model).(VNode)
	assignSkyIDs(&vn, "r")
	body := renderVNode(vn, sess.handlers)
	sess.prevTree = &vn

	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"></head><body><div id=\"sky-root\">%s</div><script>%s</script></body></html>", body, liveJS(sid))
}

// handleConfig exposes client-facing runtime config (no secrets) so the
// JS driver can adjust behaviour without recompilation. Served at
// /_sky/config.
func (app *liveApp) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"inputMode":    "debounce", // or "blur"
		"pollInterval": 0,          // 0 = SSE only
	})
}


func (app *liveApp) handleEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionId"`
		HandlerID string `json:"handlerId"`
		Value     string `json:"value"`
	}
	// Bound event payload to 1 MiB — these are tiny JSON envelopes.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "payload too large", 413)
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sess, ok := app.store.Get(req.SessionID)
	if !ok {
		http.Error(w, "session not found", 404)
		return
	}
	sess.mu.Lock()
	msg, ok := sess.handlers[req.HandlerID]
	if !ok {
		sess.mu.Unlock()
		http.Error(w, "handler not found", 404)
		return
	}
	// If msg is a function (onInput), call with value
	if isFunc(msg) {
		msg = sky_call(msg, req.Value)
	}
	// Keep a reference to the previous tree BEFORE dispatch mutates it.
	prev := sess.prevTree
	body2 := app.dispatch(sess, msg)
	newTree := sess.prevTree
	sess.mu.Unlock()
	// Persist the mutated session so DB-backed stores see the new
	// state. Memory store is a no-op on Set for an already-tracked sid.
	app.store.Set(req.SessionID, sess)

	// When we have a prior tree we can reply with a minimal patch set
	// (preserving unrelated DOM state client-side). On first interaction
	// (prev == nil) or when the tree shape changed so drastically that
	// every patch is a full-HTML replace anyway, fall back to the full
	// innerHTML body.
	if prev != nil && newTree != nil {
		patches := diffTrees(prev, newTree)
		if len(patches) > 0 && !patchesAreFullReplace(patches) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"patches": patches})
			return
		}
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(body2))
}


// patchesAreFullReplace: a single Patch targeting the root that just
// replaces HTML is no better than returning the body directly — keep the
// HTML fast-path for those cases.
func patchesAreFullReplace(patches []Patch) bool {
	return len(patches) == 1 && patches[0].HTML != nil && patches[0].ID == "r"
}

// dispatch: run update with msg, process cmd, reset subs, re-render view.
// MUST be called with sess.mu held.
//
// When the Live.app config includes a `guard : Msg -> Model -> Result String ()`
// function, we run it BEFORE update. An `Err reason` short-circuits the
// update and surfaces `reason` on model.Notification so the user sees
// why their action was rejected. `Ok ()` proceeds normally.
func (app *liveApp) dispatch(sess *liveSession, msg any) string {
	if app.guard != nil && isFunc(app.guard) {
		g := sky_call2(app.guard, msg, sess.model)
		// guard returns Result: Ok _ (allow) or Err "reason" (reject).
		if isErrResult(g) {
			reason := extractErrResultValue(g)
			sess.model = RecordUpdate(sess.model, map[string]any{
				"Notification":     reason,
				"NotificationType": "error",
			})
			return app.renderView(sess)
		}
	}
	result := sky_call2(app.update, msg, sess.model)
	sess.model = tupleFirst(result)
	cmd := tupleSecond(result)
	sess.handlers = map[string]any{}
	vn := sky_call(app.view, sess.model).(VNode)
	assignSkyIDs(&vn, "r")
	body := renderVNode(vn, sess.handlers)
	sess.prevTree = &vn
	// Process Cmds (may spawn goroutines)
	app.runCmd(sess, cmd)
	// Re-evaluate subscriptions based on new model
	app.setupSubscriptions(sess)
	return body
}

// renderView: re-render from current session model without updating
// the model (used by dispatch when guard short-circuits).
func (app *liveApp) renderView(sess *liveSession) string {
	sess.handlers = map[string]any{}
	vn := sky_call(app.view, sess.model).(VNode)
	assignSkyIDs(&vn, "r")
	body := renderVNode(vn, sess.handlers)
	sess.prevTree = &vn
	return body
}


// isErrResult: True when v is a SkyResult with Tag == 1 (Err).
func isErrResult(v any) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return false
	}
	tag := rv.FieldByName("Tag")
	if !tag.IsValid() || tag.Kind() != reflect.Int {
		return false
	}
	return tag.Int() == 1
}

// extractErrResultValue: read the Err side's payload (usually String).
func extractErrResultValue(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return ""
	}
	// Sky's SkyResult carries OkValue/ErrValue fields.
	fv := rv.FieldByName("ErrValue")
	if !fv.IsValid() {
		return ""
	}
	return fv.Interface()
}


// runCmd processes a Cmd value, spawning goroutines for Cmd.perform.
// Goroutines dispatch their result back through dispatch via SSE.
func (app *liveApp) runCmd(sess *liveSession, cmd any) {
	c, ok := cmd.(cmdT)
	if !ok {
		return
	}
	switch c.kind {
	case "none":
		return
	case "batch":
		for _, sub := range c.batch {
			app.runCmd(sess, sub)
		}
	case "perform":
		go app.runPerform(sess, c.task, c.toMsg)
	}
}

func (app *liveApp) runPerform(sess *liveSession, task any, toMsg any) {
	// task is a Sky Task — a zero-arg func() any returning SkyResult
	result := sky_call(task, nil)
	// toMsg : Result err a -> Msg — convert result to Msg
	msg := sky_call(toMsg, result)
	// Push update through locked dispatch
	sess.mu.Lock()
	body := app.dispatch(sess, msg)
	sess.mu.Unlock()
	// Notify SSE listeners
	select {
	case sess.sseCh <- body:
	default:
		// channel full, drop
	}
}

// setupSubscriptions: cancel any prior ticker, then re-evaluate subscriptions for new model.
func (app *liveApp) setupSubscriptions(sess *liveSession) {
	// Cancel existing ticker
	close(sess.cancelSub)
	sess.cancelSub = make(chan struct{})

	if app.subscriptions == nil {
		return
	}
	subResult := sky_call(app.subscriptions, sess.model)
	sub, ok := subResult.(subT)
	if !ok || sub.kind != "every" {
		return
	}
	interval := time.Duration(sub.ms) * time.Millisecond
	if interval <= 0 {
		return
	}
	cancel := sess.cancelSub
	toMsg := sub.toMsg
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case t := <-ticker.C:
				sess.mu.Lock()
				msg := toMsg
				// If toMsg is a function, call it with current time millis
				if isFunc(msg) {
					msg = sky_call(toMsg, t.UnixMilli())
				}
				body := app.dispatch(sess, msg)
				sess.mu.Unlock()
				select {
				case sess.sseCh <- body:
				default:
				}
			}
		}
	}()
}

// handleSSE: Server-Sent Events endpoint. Pushes view patches as they arrive.
func (app *liveApp) handleSSE(w http.ResponseWriter, r *http.Request) {
	sid := ""
	if c, err := r.Cookie("sky_sid"); err == nil {
		sid = c.Value
	}
	if sid == "" {
		http.Error(w, "no session", 400)
		return
	}
	sess, ok := app.store.Get(sid)
	if !ok {
		http.Error(w, "session not found", 404)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	// Send an initial ping
	fmt.Fprintf(w, ": connected\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case body := <-sess.sseCh:
			// Escape newlines for SSE data lines
			escaped := strings.ReplaceAll(body, "\n", "\\n")
			fmt.Fprintf(w, "event: patch\ndata: %s\n\n", escaped)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func sessionID(r *http.Request, w http.ResponseWriter) string {
	if c, err := r.Cookie("sky_sid"); err == nil {
		return c.Value
	}
	b := make([]byte, 16)
	rand.Read(b)
	sid := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "sky_sid", Value: sid, Path: "/", HttpOnly: true})
	return sid
}

func liveJS(sid string) string {
	return fmt.Sprintf(`
var __skySid = %q;

// __skyPatch: replace sky-root's content with the fragment in `+"`"+`t`+"`"+`,
// preserving the active element (focus + caret/selection) and scroll
// position across the swap. Without this, typing in an input that
// triggers onInput would lose focus on every keystroke.
function __skyPatch(t) {
  var root = document.getElementById("sky-root");
  if (!root) return;
  // If the response contains a full <div id="sky-root"> wrapper (from a
  // navigation request), strip the wrapper.
  var m = t.match(/<div id="sky-root">([\s\S]*?)<\/div><script>/);
  if (m) t = m[1];
  var active = document.activeElement;
  var key = __skyElementKey(active);
  var selStart = null, selEnd = null;
  if (active && "selectionStart" in active) {
    try { selStart = active.selectionStart; selEnd = active.selectionEnd; } catch (e) {}
  }
  var scrollX = window.scrollX, scrollY = window.scrollY;
  root.innerHTML = t;
  window.scrollTo(scrollX, scrollY);
  if (key) {
    var newEl = __skyFindByKey(root, key);
    if (newEl) {
      newEl.focus();
      if (selStart !== null && "selectionStart" in newEl) {
        try { newEl.selectionStart = selStart; newEl.selectionEnd = selEnd; } catch (e) {}
      }
    }
  }
}

// __skyElementKey: stable key used to re-locate the focused element in
// the patched DOM. Priority: id > name > tag+position-path.
function __skyElementKey(el) {
  if (!el || el === document.body) return null;
  if (el.id) return {kind: "id", v: el.id};
  if (el.name) return {kind: "name", v: el.name, tag: el.tagName};
  return null;
}
function __skyFindByKey(scope, key) {
  if (key.kind === "id") return document.getElementById(key.v);
  if (key.kind === "name") {
    return scope.querySelector(key.tag.toLowerCase() + '[name="' + key.v + '"]');
  }
  return null;
}

// ── Loading indicator ────────────────────────────────────────
// Call __skyLoaderStart() before network, __skyLoaderEnd() after. An element
// with id="sky-loader" gets the sky-loading class added/removed. Small
// 80ms delay so fast responses don't flash the indicator.
var __skyLoaderEl = null;
var __skyLoaderTimer = null;
function __skyLoaderStart() {
  __skyLoaderEl = __skyLoaderEl || document.getElementById("sky-loader");
  if (!__skyLoaderEl) return;
  clearTimeout(__skyLoaderTimer);
  __skyLoaderTimer = setTimeout(function() {
    __skyLoaderEl.classList.add("sky-loading");
  }, 80);
}
function __skyLoaderEnd() {
  clearTimeout(__skyLoaderTimer);
  if (__skyLoaderEl) __skyLoaderEl.classList.remove("sky-loading");
}

// ── Debounce ─────────────────────────────────────────────────
var __skyInputTimers = {};
function __skyDebouncedSend(id, value, delay) {
  clearTimeout(__skyInputTimers[id]);
  __skyInputTimers[id] = setTimeout(function() {
    __skySend(id, value, { noLoader: true });
  }, delay);
}

// ── Core send ────────────────────────────────────────────────
function __skySend(id, value, opts) {
  opts = opts || {};
  if (!opts.noLoader) __skyLoaderStart();
  fetch("/_sky/event", {
    method: "POST",
    headers: {"Content-Type":"application/json"},
    body: JSON.stringify({sessionId: __skySid, handlerId: id, value: value}),
    credentials: "same-origin"
  }).then(function(r){
    var ct = r.headers.get("Content-Type") || "";
    if (ct.indexOf("application/json") >= 0) {
      return r.json().then(function(data) {
        __skyLoaderEnd();
        if (data && data.patches) __skyApplyPatches(data.patches);
      });
    }
    return r.text().then(function(t) {
      __skyLoaderEnd();
      __skyPatch(t);
    });
  }).catch(function() { __skyLoaderEnd(); });
}

// Apply a list of sky-id addressed patches without reflowing the whole
// document. Preserves input focus + caret naturally, so typing keeps
// its state through any number of updates.
function __skyApplyPatches(patches) {
  for (var i = 0; i < patches.length; i++) {
    var p = patches[i];
    var el = document.querySelector('[sky-id="' + p.id.replace(/"/g, '\\"') + '"]');
    if (!el) continue;
    if (p.text !== undefined && p.text !== null) el.textContent = p.text;
    if (p.html !== undefined && p.html !== null) el.innerHTML = p.html;
    if (p.attrs) {
      var keys = Object.keys(p.attrs);
      for (var j = 0; j < keys.length; j++) {
        var k = keys[j], v = p.attrs[k];
        if (v === "") { el.removeAttribute(k); }
        else {
          el.setAttribute(k, v);
          // Sync DOM properties that don't reflect from attrs.
          if (k === "value" && ("value" in el)) el.value = v;
          if (k === "checked") el.checked = v !== "" && v !== "false";
          if (k === "selected") el.selected = v !== "" && v !== "false";
          if (k === "disabled") el.disabled = v !== "" && v !== "false";
        }
      }
    }
    if (p.remove) el.remove();
  }
}

function skyEvent(ev, id) {
  ev.preventDefault();
  var t = ev.target;
  // Input events debounce so typing doesn't round-trip every keystroke.
  if (ev.type === "input" && t && typeof t.value === "string") {
    __skyDebouncedSend(id, t.value, 150);
    return;
  }
  var v = "";
  if (t) {
    // Forms: serialise every [name]=value into a JSON object.
    if (ev.type === "submit" && t.tagName === "FORM") {
      var data = {};
      for (var i = 0; i < t.elements.length; i++) {
        var el = t.elements[i];
        if (!el.name) continue;
        if (el.type === "checkbox" || el.type === "radio") {
          if (el.checked) data[el.name] = el.value;
        } else if (el.type === "file") {
          // File handling left to specific drivers (sky-image etc.).
        } else {
          data[el.name] = el.value;
        }
      }
      v = JSON.stringify(data);
    } else if (typeof t.value === "string") {
      v = t.value;
    } else if (t.checked !== undefined) {
      v = t.checked ? "true" : "false";
    }
  }
  __skySend(id, v);
}

// ── File / Image drivers ─────────────────────────────────────
// onFile / onImage register via data-sky-ev-sky-file / -sky-image
// attributes. The client reads the chosen file, optionally resizes
// (for images), and sends a base64 data URL as the event value.
document.addEventListener("change", function(ev) {
  var el = ev.target;
  if (!el || el.tagName !== "INPUT" || el.type !== "file") return;
  var fileId  = el.getAttribute("data-sky-ev-sky-file");
  var imageId = el.getAttribute("data-sky-ev-sky-image");
  var f = el.files && el.files[0];
  if (!f) return;
  if (fileId) {
    var r = new FileReader();
    r.onload = function(e) { __skySend(fileId, e.target.result); };
    r.readAsDataURL(f);
  }
  if (imageId) {
    var maxW = parseInt(el.getAttribute("data-sky-ev-sky-file-max-width")  || "1200");
    var maxH = parseInt(el.getAttribute("data-sky-ev-sky-file-max-height") || "1200");
    __skyResizeImage(f, maxW, maxH, function(dataUrl) {
      __skySend(imageId, dataUrl);
    });
  }
});

function __skyResizeImage(file, maxW, maxH, cb) {
  var img = new Image();
  var url = URL.createObjectURL(file);
  img.onload = function() {
    URL.revokeObjectURL(url);
    var w = img.width, h = img.height;
    if (w > maxW) { h = Math.round(h * maxW / w); w = maxW; }
    if (h > maxH) { w = Math.round(w * maxH / h); h = maxH; }
    var canvas = document.createElement("canvas");
    canvas.width = w; canvas.height = h;
    canvas.getContext("2d").drawImage(img, 0, 0, w, h);
    cb(canvas.toDataURL("image/jpeg", 0.85));
  };
  img.src = url;
}

// Expose programmatic dispatch for custom JS integrations (e.g. Firebase
// auth callbacks that need to send a Msg after the SDK resolves).
window.__sky_send = function(id, value, opts) { __skySend(id, value, opts); };
// sky-nav: intercept clicks on <a sky-nav ...> links so navigation is a
// client-side fetch + innerHTML swap instead of a full page reload.
// Falls back to normal navigation on modifier keys (cmd/ctrl/shift/alt),
// middle-click, and non-GET targets.
document.addEventListener("click", function(ev) {
  if (ev.defaultPrevented) return;
  if (ev.button !== 0) return;
  if (ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
  var el = ev.target;
  while (el && el.tagName !== "A") el = el.parentElement;
  if (!el) return;
  if (!el.hasAttribute("sky-nav")) return;
  var href = el.getAttribute("href");
  if (!href || href.charAt(0) === "#") return;
  // External links are left to the browser.
  try {
    var u = new URL(href, window.location.href);
    if (u.origin !== window.location.origin) return;
  } catch (e) { return; }
  ev.preventDefault();
  fetch(href, { headers: { "X-Sky-Nav": "1" }, credentials: "same-origin" })
    .then(function(r) { return r.text(); })
    .then(function(t) {
      __skyPatch(t);
      window.history.pushState({}, "", href);
    })
    .catch(function() { window.location.href = href; });
});
window.addEventListener("popstate", function() {
  fetch(window.location.href, { headers: { "X-Sky-Nav": "1" }, credentials: "same-origin" })
    .then(function(r) { return r.text(); })
    .then(__skyPatch);
});
// Server-Sent Events: push updates from server (subscriptions, Cmd.perform results)
var __skySSE = new EventSource("/_sky/sse");
__skySSE.addEventListener("patch", function(e) {
  var html = e.data.replace(/\\n/g, "\n");
  __skyPatch(html);
});
`, sid)
}

// ═══════════════════════════════════════════════════════════
// Helpers: tuple access, sky_call dispatch
// ═══════════════════════════════════════════════════════════

func tupleFirst(v any) any {
	r := reflect.ValueOf(v)
	if r.Kind() == reflect.Struct {
		f := r.FieldByName("V0")
		if f.IsValid() {
			return f.Interface()
		}
	}
	if s, ok := v.([2]any); ok {
		return s[0]
	}
	if s, ok := v.([]any); ok && len(s) >= 1 {
		return s[0]
	}
	return v
}

func tupleSecond(v any) any {
	r := reflect.ValueOf(v)
	if r.Kind() == reflect.Struct {
		f := r.FieldByName("V1")
		if f.IsValid() {
			return f.Interface()
		}
	}
	if s, ok := v.([2]any); ok {
		return s[1]
	}
	if s, ok := v.([]any); ok && len(s) >= 2 {
		return s[1]
	}
	return nil
}

func isFunc(v any) bool {
	if v == nil {
		return false
	}
	return reflect.ValueOf(v).Kind() == reflect.Func
}

func sky_call(f any, arg any) any {
	if f == nil {
		return nil
	}
	rv := reflect.ValueOf(f)
	if rv.Kind() != reflect.Func {
		return f
	}
	if rv.Type().NumIn() == 0 {
		out := rv.Call(nil)
		if len(out) > 0 {
			return out[0].Interface()
		}
		return nil
	}
	av := reflect.ValueOf(arg)
	if !av.IsValid() {
		av = reflect.Zero(rv.Type().In(0))
	}
	out := rv.Call([]reflect.Value{av})
	if len(out) > 0 {
		return out[0].Interface()
	}
	return nil
}

func sky_call2(f any, a, b any) any {
	rv := reflect.ValueOf(f)
	if rv.Kind() != reflect.Func {
		return f
	}
	if rv.Type().NumIn() == 2 {
		av := reflect.ValueOf(a)
		bv := reflect.ValueOf(b)
		if !av.IsValid() {
			av = reflect.Zero(rv.Type().In(0))
		}
		if !bv.IsValid() {
			bv = reflect.Zero(rv.Type().In(1))
		}
		out := rv.Call([]reflect.Value{av, bv})
		if len(out) > 0 {
			return out[0].Interface()
		}
		return nil
	}
	// Curried: f(a)(b)
	return sky_call(sky_call(f, a), b)
}

// avoid unused-import linter noise for time if not otherwise referenced
var _ = time.Now
