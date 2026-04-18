// live.go — Sky.Live runtime (session store, VDom, SSE, routing).
//
// Audit P3-4: every `fmt.Sprintf("%v", x)` in this file is bound
// to HTML/attribute value rendering (Attr_*, Html_text, velement)
// or error-message composition. None of them flow secret material,
// session IDs, cookie values, or auth tokens: the session-id path
// passes string directly to http.SetCookie (see Server_setCookie),
// and CSRF/rate-limit tokens use the constant-time compare helpers
// in rt.go. Callers at the Sky layer pass String values; the %v
// sites tolerate any stringifiable input for codegen-uniformity
// (Attr_value can accept a lowered Int literal and render "42").
// The justification therefore applies file-wide.
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
	"runtime"
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
		case SkyTuple2:
			node.Attrs[fmt.Sprintf("%v", v.V0)] = fmt.Sprintf("%v", v.V1)
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
	v = unwrapAny(v)
	if l, ok := v.([]any); ok {
		return l
	}
	// Handle typed slices ([]string, []int, etc.) via reflect
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out
	}
	return []any{v}
}

func Html_text(s any) any   { return vtext(fmt.Sprintf("%v", s)) }
func Html_textT(s string) any { return vtext(s) }
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
func Attr_classT(v string) any      { return attr("class", v) }
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
func Css_propertyT(k, v string) any {
	return cssProp{k: k, v: v}
}

// Unit helpers
func Css_px(n any) any  { return fmt.Sprintf("%vpx", n) }
func Css_rem(n any) any { return fmt.Sprintf("%vrem", n) }
// Css_pxT / Css_remT: take float64 so both `px 12` (int literal promoted)
// and `rem 0.9` (float literal) work without separate variants. Sky's
// dispatch coerces via AsFloat at the call site.
func Css_pxT(n float64) string  {
	if n == float64(int(n)) { return fmt.Sprintf("%dpx", int(n)) }
	return fmt.Sprintf("%gpx", n)
}
func Css_remT(n float64) string {
	if n == float64(int(n)) { return fmt.Sprintf("%drem", int(n)) }
	return fmt.Sprintf("%grem", n)
}
func Css_em(n any) any  { return fmt.Sprintf("%vem", n) }
func Css_pct(n any) any { return fmt.Sprintf("%v%%", n) }
func Css_hex(s any) any { return fmt.Sprintf("#%v", s) }
func Css_hexT(s string) string { return "#" + s }

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
	// Html.doctype wraps children in a pseudo-element; render as
	// <!DOCTYPE html> followed by the children directly.
	if n.Tag == "!doctype-wrapper" {
		var sb strings.Builder
		sb.WriteString("<!DOCTYPE html>")
		for _, c := range n.Children {
			sb.WriteString(renderVNode(c, handlers))
		}
		return sb.String()
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
		// Sky.Live TEA protocol:
		//   * Every event attribute is `sky-<event>="<MsgName>"` —
		//     MsgName is the Sky-side Msg constructor (e.g. "Increment",
		//     "UpdateEmail"). Derived from the Msg ADT's SkyName field
		//     (or from a Go function name for curried constructors).
		//   * Handler lookup table: <sky-id>.<event> → msg value. This
		//     stays deterministic per model state so re-rendering a view
		//     rebuilds the same table — required for DB-backed stores
		//     that can't serialise the handler map.
		id := n.SkyID + "." + ev
		handlers[id] = msg
		msgName := msgDisplayName(msg)
		attr := "sky-" + ev
		sb.WriteString(fmt.Sprintf(` %s="%s" data-sky-hid="%s"`,
			attr, html.EscapeString(msgName), id))
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

// msgDisplayName extracts a Sky Msg constructor name from its runtime
// representation.
//
//   * ADT struct values (e.g. Msg{Tag: 1, SkyName: "Increment"}) expose
//     their constructor name via the SkyName field the compiler emits.
//   * Function values are Msg constructors whose name is discoverable
//     via runtime.FuncForPC — we pull the last `_`-segment so
//     `main.Msg_UpdateEmail` → "UpdateEmail".
//   * Anything else falls back to "" so the client knows to treat it
//     as an opaque handler-id only.
func msgDisplayName(msg any) string {
	if msg == nil {
		return ""
	}
	rv := reflect.ValueOf(msg)
	if rv.Kind() == reflect.Struct {
		if f := rv.FieldByName("SkyName"); f.IsValid() && f.Kind() == reflect.String {
			return f.String()
		}
	}
	if rv.Kind() == reflect.Func {
		name := runtime.FuncForPC(rv.Pointer()).Name()
		// Trim main.Msg_UpdateEmail → UpdateEmail.
		if idx := strings.LastIndex(name, "_"); idx >= 0 {
			return name[idx+1:]
		}
		if idx := strings.LastIndex(name, "."); idx >= 0 {
			return name[idx+1:]
		}
		return name
	}
	return ""
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

// sessionLocker serialises concurrent event handlers for the SAME session
// while allowing different sessions to proceed in parallel. Ref-counted so
// idle sessions don't leak mutex entries.
type sessionLocker struct {
	mu    sync.Mutex
	locks map[string]*sessionLockEntry
}

type sessionLockEntry struct {
	mu   sync.Mutex
	refs int
}

func newSessionLocker() *sessionLocker {
	return &sessionLocker{locks: map[string]*sessionLockEntry{}}
}

func (s *sessionLocker) Lock(sid string) {
	s.mu.Lock()
	e, ok := s.locks[sid]
	if !ok {
		e = &sessionLockEntry{}
		s.locks[sid] = e
	}
	e.refs++
	s.mu.Unlock()
	e.mu.Lock()
}

func (s *sessionLocker) Unlock(sid string) {
	s.mu.Lock()
	e, ok := s.locks[sid]
	if !ok {
		s.mu.Unlock()
		return
	}
	e.refs--
	if e.refs <= 0 {
		delete(s.locks, sid)
	}
	s.mu.Unlock()
	e.mu.Unlock()
}

// applyMsgArgs consumes a resolved Msg-handler value from the handler map
// and, when it's a curried constructor (onInput: \s -> GotInput s), applies
// each wire-supplied argument in order to produce a concrete Msg ADT.
// Falls back to the legacy single-value form (`sky_call(msg, value)`) when
// the client didn't supply structured args — keeps older inputs working.
func applyMsgArgs(msg any, args []json.RawMessage, fallbackValue string) any {
	if msg == nil {
		return msg
	}
	rv := reflect.ValueOf(msg)
	isFunc := rv.Kind() == reflect.Func
	if !isFunc {
		return msg
	}
	if len(args) == 0 {
		return sky_call(msg, fallbackValue)
	}
	cur := msg
	for _, raw := range args {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			v = string(raw)
		}
		cur = sky_call(cur, v)
		if reflect.ValueOf(cur).Kind() != reflect.Func {
			break
		}
	}
	return cur
}

type liveSession struct {
	model    any
	handlers map[string]any
	prevTree *VNode // Last rendered tree; used by the diff protocol.
	// Last rendered body string. Any dispatch that produces a byte-
	// identical body is a no-op from the client's perspective; we
	// suppress the SSE push to avoid flooding the wire when a
	// Time.every subscription ticks but the model-derived view
	// hasn't actually changed.
	prevBody string
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
	locker        *sessionLocker
	msgTags       map[string]int // SkyName → Tag cache for direct-send events
	msgTagsMu     sync.Mutex
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
			switch m := f.Interface().(type) {
			case map[string]string:
				for k, val := range m {
					headers[k] = val
				}
			case map[string]any:
				for k, val := range m {
					headers[k] = fmt.Sprintf("%v", val)
				}
			default:
				// Reflect fallback for other map types
				if f.Kind() == reflect.Map {
					for _, key := range f.MapKeys() {
						headers[fmt.Sprintf("%v", key.Interface())] = fmt.Sprintf("%v", f.MapIndex(key).Interface())
					}
				}
			}
		}
		// Fall back to ContentType field when Headers doesn't set it.
		// SkyResponse uses ContentType as a convenience field set by
		// Server.html / Server.json / Server.text.
		if _, hasCT := headers["Content-Type"]; !hasCT {
			if f := rv.FieldByName("ContentType"); f.IsValid() {
				if s, ok := f.Interface().(string); ok && s != "" {
					headers["Content-Type"] = s
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
// matchAnyRoute reports whether `urlPath` matches a declared route.
// Used by handleInitial to distinguish real navigations from browser
// noise (favicons, devtools prefetch). Doesn't run the route — just
// answers "is this a known page?".
func matchAnyRoute(app *liveApp, urlPath string) ([]string, bool) {
	for _, rt := range app.routes {
		if params, ok := matchRoute(rt.path, urlPath); ok {
			return params, true
		}
	}
	return nil, false
}

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
		locker:        newSessionLocker(),
		msgTags:       make(map[string]int),
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

	// Pre-register model types with gob so DB-backed session stores
	// can decode existing sessions on restart. Without this, the first
	// Get after a restart fails with "gob: name not registered".
	func() {
		defer func() { recover() }()
		req := map[string]any{"path": "/"}
		res := sky_call(app.init, req)
		model := tupleFirst(res)
		gobRegisterAll(model)
	}()

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
		return Err[any, any](ErrFfi(err.Error()))
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

// isBrowserNoisePath reports whether `p` is a path a browser or crawler
// requests automatically (favicon, service-worker probe, source-map
// fetch, .well-known discovery, static asset by extension). These must
// never trigger app.init — otherwise a fresh page load races the real
// GET / against /favicon.ico before the sky_sid cookie is set, and both
// requests run init, double-firing user-visible "initialised" logging.
func isBrowserNoisePath(p string) bool {
	switch p {
	case "/favicon.ico", "/robots.txt", "/sitemap.xml",
		"/apple-touch-icon.png", "/apple-touch-icon-precomposed.png",
		"/service-worker.js", "/sw.js", "/manifest.json":
		return true
	}
	if strings.HasPrefix(p, "/.well-known/") {
		return true
	}
	// Requests for assets by well-known extension are browser noise —
	// real page routes never end in these suffixes.
	for _, ext := range []string{".ico", ".png", ".jpg", ".jpeg", ".gif",
		".svg", ".webp", ".css", ".js", ".map", ".woff", ".woff2", ".ttf"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}


func (app *liveApp) handleInitial(w http.ResponseWriter, r *http.Request) {
	// Browser-noise paths (favicons, devtools prefetch, static asset
	// probes, .well-known) 404 BEFORE session creation. Without this
	// guard, a cold page load races the real GET / against /favicon.ico:
	// both arrive before Set-Cookie is processed, both see "no session",
	// both run init — the user sees [APP] initialised twice.
	_, routed := matchAnyRoute(app, r.URL.Path)
	if !routed && isBrowserNoisePath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}

	// Reuse the existing session when the cookie maps to one. Calling
	// init() on every GET (devtools previews, prefetch, second tabs)
	// would otherwise wipe sess.handlers and break the very next event
	// POST with "handler not found". Per-session lock prevents
	// concurrent re-renders racing each other's handlers.
	sid := sessionID(r, w)
	app.locker.Lock(sid)
	defer app.locker.Unlock(sid)

	sess, existing := app.store.Get(sid)

	// If the URL doesn't match any registered route AND we already have
	// a live session, 404 without touching it — prevents an unknown
	// path wiping sess.handlers and breaking the next event POST.
	if !routed && existing && sess != nil && sess.model != nil {
		http.NotFound(w, r)
		return
	}

	var model any
	var cmd any
	if existing && sess != nil && sess.model != nil {
		model = sess.model
	} else {
		req := map[string]any{"path": r.URL.Path}
		res := sky_call(app.init, req)
		model = tupleFirst(res)
		cmd = tupleSecond(res)
		// Register model types for gob encoding so DB-backed
		// session stores can decode them on future Get calls.
		gobRegisterAll(model)
		sess = &liveSession{
			sseCh:     make(chan string, 16),
			cancelSub: make(chan struct{}),
		}
	}

	// Route dispatch: pick the page ADT value for this URL path and
	// splice it into model.Page via RecordUpdate. Always run so the
	// returning visitor lands on the URL they requested.
	model = applyRoute(app, model, r.URL.Path)
	sess.model = model
	sess.handlers = map[string]any{}

	if cmd != nil {
		app.runCmd(sess, cmd)
	}
	app.setupSubscriptions(sess)

	vn := sky_call(app.view, model).(VNode)
	assignSkyIDs(&vn, "r")
	body := renderVNode(vn, sess.handlers)
	sess.prevTree = &vn
	sess.prevBody = body
	app.store.Set(sid, sess)

	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><link rel=\"preconnect\" href=\"https://fonts.googleapis.com\"><link rel=\"preconnect\" href=\"https://fonts.gstatic.com\" crossorigin><link href=\"https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700;800;900&display=swap\" rel=\"stylesheet\"><style>body,.font-sans{font-family:'Inter',ui-sans-serif,system-ui,-apple-system,sans-serif!important}</style></head><body><div id=\"sky-root\">%s</div><script>%s</script></body></html>", body, liveJS(sid))
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
	// TEA wire format (legacy-compatible):
	//   * msg       — constructor name (e.g. "Increment", "UpdateEmail")
	//   * args      — positional args for the constructor (strings, bools,
	//                 numbers, JSON-encoded record for form submits)
	//   * handlerId — <sky-id>.<event> fallback used when msg is "" or
	//                 the constructor can't be found by name
	//   * sessionId — per-client session key
	var req struct {
		SessionID string            `json:"sessionId"`
		Msg       string            `json:"msg"`
		Args      []json.RawMessage `json:"args"`
		HandlerID string            `json:"handlerId"`
		Value     string            `json:"value"` // legacy fallback
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
	// Per-session serial mutex: prevents two concurrent event handlers
	// for the SAME session from racing each other's model updates.
	// Different sessions proceed in parallel.
	app.locker.Lock(req.SessionID)
	defer app.locker.Unlock(req.SessionID)

	sess.mu.Lock()
	// Handler maps aren't persisted across encode/decode (closures don't
	// round-trip via gob). When we get here with an empty map — a fresh
	// decode from SQLite/Postgres, or a server restart — we rebuild it
	// deterministically by re-running view() over the current model.
	// Handler IDs are <sky-id>.<event>, stable per model state.
	if len(sess.handlers) == 0 && sess.model != nil {
		sess.handlers = map[string]any{}
		vn := sky_call(app.view, sess.model).(VNode)
		assignSkyIDs(&vn, "r")
		_ = renderVNode(vn, sess.handlers)
		sess.prevTree = &vn
	}
	msg, ok := sess.handlers[req.HandlerID]
	if !ok && req.Msg != "" && req.HandlerID == "" {
		// Direct-send path: the frontend called __sky_send("MsgName", args)
		// without a handler ID (e.g. Firebase auth callback, subscription
		// timers, external JS integrations). Construct the ADT value
		// directly from the constructor name and arguments instead of
		// looking up a render-time handler closure.
		//
		// Tag resolution: look up the global ADT tag registry (populated
		// by codegen's init() block), then fall back to the per-app cache
		// built during previous dispatches.
		tag := -1
		if t, ok := LookupAdtTag(req.Msg); ok {
			tag = t
		} else {
			app.msgTagsMu.Lock()
			if t2, ok2 := app.msgTags[req.Msg]; ok2 {
				tag = t2
			}
			app.msgTagsMu.Unlock()
		}
		var fields []any
		for _, raw := range req.Args {
			var v any
			if err := json.Unmarshal(raw, &v); err == nil {
				fields = append(fields, v)
			}
		}
		msg = SkyADT{Tag: tag, SkyName: req.Msg, Fields: fields}
		ok = true
	}
	if !ok {
		sess.mu.Unlock()
		http.Error(w, "handler not found", 404)
		return
	}
	// TEA application: if msg is a curried constructor (for onInput /
	// onSubmit / onKeyDown etc.) apply each incoming arg in order to
	// produce a concrete Msg ADT value. Falls through to the legacy
	// single-value form when only `value` was sent.
	if _, isSkyAdt := msg.(SkyADT); !isSkyAdt {
		msg = applyMsgArgs(msg, req.Args, req.Value)
	}
	// Keep a reference to the previous tree BEFORE dispatch mutates it.
	prev := sess.prevTree
	body2 := app.dispatch(sess, msg)
	newTree := sess.prevTree
	sess.mu.Unlock()
	// Persist the mutated session so DB-backed stores see the new
	// state. Memory store is a no-op on Set for an already-tracked sid.
	app.store.Set(req.SessionID, sess)

	// dispatch returns "" when the event produced a byte-identical
	// view (no-op update). Reply with an empty patch list so the
	// client acknowledges the event without the server shipping a
	// redundant HTML frame.
	if body2 == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"patches": []any{}})
		return
	}
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
	// Cache the SkyName→Tag mapping from every dispatched message so
	// direct-send events (__sky_send) can construct correctly-tagged
	// ADTs at runtime. Normal handler-dispatched events always carry
	// the codegen-assigned tag; direct-send events arrive with Tag -1.
	if adt, ok := msg.(SkyADT); ok && adt.Tag >= 0 {
		app.msgTagsMu.Lock()
		app.msgTags[adt.SkyName] = adt.Tag
		app.msgTagsMu.Unlock()
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
	// No-op suppression: if the rendered body is byte-identical to
	// the last one we pushed, return "" so producer goroutines can
	// skip the SSE write. A Time.every subscription that ticks
	// without mutating any view-reachable state produces the same
	// HTML twice; there's no reason to ship a patch.
	if body == sess.prevBody {
		return ""
	}
	sess.prevBody = body
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
	// Empty body = dispatch determined the view is unchanged.
	if body == "" {
		return
	}
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
				// Suppress SSE write when the tick didn't change
				// the view — prevents Time.every from pushing an
				// identical HTML frame every interval.
				if body == "" {
					continue
				}
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
  var activeValue = null;
  var activeIsInput = active && (active.tagName === "INPUT" || active.tagName === "TEXTAREA" || active.tagName === "SELECT");
  if (activeIsInput) {
    activeValue = active.value;
  }
  // Snapshot ALL input values with pending debounces so they survive the patch.
  var pendingValues = {};
  var inputs = root.querySelectorAll("input, textarea, select");
  for (var pi = 0; pi < inputs.length; pi++) {
    var inp = inputs[pi];
    var inpHid = inp.getAttribute("data-sky-hid");
    if (inpHid && __skyInputPending[inpHid]) {
      var inpKey = inp.getAttribute("sky-id") || inpHid;
      pendingValues[inpKey] = inp.value;
    }
  }
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
      // Optimistic input: restore value + cursor position together.
      // Setting value resets selectionStart, so cursor must be
      // restored AFTER value assignment.
      if (activeIsInput && activeValue !== null) {
        newEl.value = activeValue;
        if (selStart !== null && "selectionStart" in newEl) {
          try { newEl.selectionStart = selStart; newEl.selectionEnd = selEnd; } catch (e) {}
        }
      } else if (selStart !== null && "selectionStart" in newEl) {
        try { newEl.selectionStart = selStart; newEl.selectionEnd = selEnd; } catch (e) {}
      }
    }
  }
  // Restore pending input values that were overwritten by innerHTML.
  var newInputs = root.querySelectorAll("input, textarea, select");
  for (var ri = 0; ri < newInputs.length; ri++) {
    var ni = newInputs[ri];
    var niKey = ni.getAttribute("sky-id") || ni.getAttribute("data-sky-hid");
    if (niKey && pendingValues[niKey] !== undefined && ni.value !== pendingValues[niKey]) {
      ni.value = pendingValues[niKey];
    }
  }
  // Re-bind sky-* events for the fresh DOM subtree.
  __skyBindEvents(document);
  // Process data-sky-eval attributes (e.g. skySignOut() on sign-out).
  __skyRunEvals(root);
}

// __skyElementKey: stable key used to re-locate the focused element in
// the patched DOM. Priority: id > name > sky-id (runtime-assigned).
// sky-id is the critical fallback — inputs without id/name (the common
// case for form fields) would otherwise lose focus on every patch.
function __skyElementKey(el) {
  if (!el || el === document.body) return null;
  if (el.id) return {kind: "id", v: el.id};
  if (el.name) return {kind: "name", v: el.name, tag: el.tagName};
  var skyId = el.getAttribute && el.getAttribute("sky-id");
  if (skyId) return {kind: "sky-id", v: skyId};
  return null;
}
function __skyFindByKey(scope, key) {
  if (key.kind === "id") return document.getElementById(key.v);
  if (key.kind === "name") {
    return scope.querySelector(key.tag.toLowerCase() + '[name="' + key.v + '"]');
  }
  if (key.kind === "sky-id") {
    return scope.querySelector('[sky-id="' + key.v.replace(/"/g, '\\"') + '"]');
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
var __skyInputPending = {};
function __skyDebouncedSend(msgName, args, hid, delay) {
  var key = hid || msgName;
  clearTimeout(__skyInputTimers[key]);
  __skyInputPending[key] = { msgName: msgName, args: args, hid: hid };
  __skyInputTimers[key] = setTimeout(function() {
    delete __skyInputPending[key];
    __skySend(msgName, args, hid, { noLoader: true });
  }, delay);
}
// Flush pending debounced input on blur (tab away / click elsewhere).
// Without this, typing fast then tabbing loses the last keystrokes
// because the debounce hasn't fired yet.
document.addEventListener("focusout", function(ev) {
  var t = ev.target;
  if (!t) return;
  var hid = t.getAttribute("data-sky-hid");
  var key = hid || t.getAttribute("sky-input");
  if (key && __skyInputPending[key]) {
    clearTimeout(__skyInputTimers[key]);
    var p = __skyInputPending[key];
    delete __skyInputPending[key];
    __skySend(p.msgName, p.args, p.hid, { noLoader: true });
  }
}, true);

// ── Core send ────────────────────────────────────────────────
// Wire format: {sid, msg: "MsgName", args: [...], handlerId: "..."}.
//   * msg + args drive the server's TEA update — the legacy protocol.
//   * handlerId is a stable-by-render tag used as a fallback when the
//     server can't locate the Msg constructor by name (anonymous ADTs).
function __skySend(msgName, args, handlerId, opts) {
  opts = opts || {};
  if (!opts.noLoader) __skyLoaderStart();
  fetch("/_sky/event", {
    method: "POST",
    headers: {"Content-Type":"application/json"},
    body: JSON.stringify({
      sessionId: __skySid,
      msg: msgName || "",
      args: args || [],
      handlerId: handlerId || ""
    }),
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
  // Any new sky-* attribute in the patched DOM needs a listener.
  __skyBindEvents(document);
}

// ── TEA event binding ────────────────────────────────────────
// Walks the DOM for sky-<event> attributes and binds a native listener
// that extracts args and dispatches through the TEA update cycle.
// Re-run after every DOM patch because new sky-* attrs may have appeared.
function __skyBindEvents(root) {
  root = root || document;
  var events = ["click", "dblclick", "input", "change", "submit", "focus", "blur",
                "keydown", "keyup", "keypress", "mouseover", "mouseout",
                "mousedown", "mouseup"];
  for (var i = 0; i < events.length; i++) {
    __skyBindOne(root, events[i]);
  }
}

function __skyRunEvals(root) {
  var el = (root || document).querySelector("[data-sky-eval]");
  if (el) { try { (new Function(el.getAttribute("data-sky-eval")))(); } catch(e) {} el.remove(); }
}

function __skyBindOne(root, eventName) {
  var selector = "[sky-" + eventName + "]";
  var nodes = root.querySelectorAll(selector);
  for (var i = 0; i < nodes.length; i++) {
    var el = nodes[i];
    if (el["__sky_" + eventName]) continue;
    el["__sky_" + eventName] = true;
    el.addEventListener(eventName, function(ev) {
      var target = ev.currentTarget;
      var msgName = target.getAttribute("sky-" + ev.type);
      var hid     = target.getAttribute("data-sky-hid");
      if (!msgName && !hid) return;
      // Some events want preventDefault (submit, form-link navigation);
      // click doesn't (we only intercept when the attribute is set).
      if (ev.type === "submit") ev.preventDefault();
      var args = __skyExtractArgs(ev);
      if (ev.type === "input") {
        __skyDebouncedSend(msgName, args, hid, 150);
        return;
      }
      __skySend(msgName, args, hid);
    });
  }
}

// Extract the args array for a DOM event following the legacy Sky.Live
// convention:
//   * click / focus / blur / mouse*    → []         (just the msg)
//   * input / change                   → [value]    (typed input value)
//   * submit                           → [formData] (plain object of [name]=value)
//   * keydown / keyup / keypress       → [key]      (event.key string)
function __skyExtractArgs(ev) {
  var t = ev.target;
  switch (ev.type) {
    case "input":
    case "change":
      if (!t) return [""];
      if (t.type === "checkbox" || t.type === "radio") return [t.checked];
      if (t.type === "number" || t.type === "range") return [t.valueAsNumber || 0];
      return [t.value == null ? "" : String(t.value)];
    case "submit":
      var data = {};
      if (t && t.elements) {
        for (var i = 0; i < t.elements.length; i++) {
          var el = t.elements[i];
          if (!el.name) continue;
          if (el.type === "checkbox" || el.type === "radio") {
            if (el.checked) data[el.name] = el.value;
          } else if (el.type === "file") {
            // File handling via sky-file / sky-image drivers (below).
          } else {
            data[el.name] = el.value;
          }
        }
      }
      return [data];
    case "keydown":
    case "keyup":
    case "keypress":
      return [ev.key || ""];
    default:
      return [];
  }
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

// ── Init ─────────────────────────────────────────────────────
// Bind initial DOM event listeners once the HTML is parsed.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", function() { __skyBindEvents(document); });
} else {
  __skyBindEvents(document);
}
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

// coerceReflectArg converts a reflect.Value to the target type when they
// are struct-layout-compatible but different generic instantiations.
// E.g. SkyResult[any, any] → SkyResult[any, Payload_R]. Copies fields
// by name so Tag, OkValue, ErrValue, JustValue, Fields, SkyName all
// transfer regardless of the generic parameters.
func coerceReflectArg(av reflect.Value, want reflect.Type) reflect.Value {
	if !av.IsValid() {
		return reflect.Zero(want)
	}
	// Unwrap interface values to their concrete type
	for av.Kind() == reflect.Interface && !av.IsNil() {
		av = av.Elem()
	}
	if av.Type().AssignableTo(want) {
		return av
	}
	if av.Type().ConvertibleTo(want) {
		return av.Convert(want)
	}
	// Struct-to-struct: copy fields by name (handles cross-generic SkyResult, SkyMaybe, SkyADT)
	if av.Kind() == reflect.Struct && want.Kind() == reflect.Struct {
		dst := reflect.New(want).Elem()
		for i := 0; i < av.NumField(); i++ {
			name := av.Type().Field(i).Name
			df := dst.FieldByName(name)
			sf := av.Field(i)
			if !df.IsValid() || !df.CanSet() {
				continue
			}
			// Unwrap interface-typed source fields
			for sf.Kind() == reflect.Interface && !sf.IsNil() {
				sf = sf.Elem()
			}
			if sf.Type().AssignableTo(df.Type()) {
				df.Set(sf)
			} else if df.Type().Kind() == reflect.Interface {
				df.Set(sf)
			} else if sf.Kind() == reflect.Struct && df.Kind() == reflect.Struct {
				df.Set(coerceReflectArg(sf, df.Type()))
			} else {
				// Last resort: set via interface boxing
				df.Set(reflect.ValueOf(sf.Interface()).Convert(df.Type()))
			}
		}
		return dst
	}
	// Interface target: wrap as-is
	if want.Kind() == reflect.Interface {
		return av
	}
	// Concrete target from interface value: try direct conversion
	if av.Type().ConvertibleTo(want) {
		return av.Convert(want)
	}
	return av
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
	av = coerceReflectArg(av, rv.Type().In(0))
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
		av = coerceReflectArg(av, rv.Type().In(0))
		bv = coerceReflectArg(bv, rv.Type().In(1))
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
