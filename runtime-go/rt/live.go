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
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
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
			// Empty-key attrPair is the "no-op" sentinel returned by
			// bool-conditional helpers like `disabled False` so
			// False-valued booleans don't render the attribute.
			if v.key == "" {
				continue
			}
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
// Void HTML elements accept an optional empty `[]` children argument
// because elm-format convention writes them as `input [attrs] []`.
// The runtime ignores the children and emits the void tag regardless;
// the single-arg call site (`input [attrs]`) still works because
// Sky's variadic FFI dispatch treats the missing arg as implicit nil.
func Html_input(a any, _ ...any) any { return htmlElem("input")(a, nil) }
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
// Void HTML elements — same variadic trick as Html_input so both
// `img [attrs]` and `img [attrs] []` compile. The second arg is
// discarded.
func Html_img(a any, _ ...any) any { return htmlElem("img")(a, nil) }
func Html_br(a any, _ ...any) any  { return htmlElem("br")(a, nil) }
func Html_hr(a any, _ ...any) any  { return htmlElem("hr")(a, nil) }
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
// Boolean HTML attributes honour two calling conventions:
//
//   * `disabled model.loading` — typed bool, Elm-style. True renders,
//     False omits. sky-chat's compose form needs this.
//   * `Attr.required ()` / `Attr.checked ()` — unit-arg presence style
//     used by many Sky projects (notes-app, skyvote, skyshop) where
//     the user just wants the attribute present. Anything non-bool
//     is treated as True.
//
// Before this logic was added, every call emitted the attribute
// regardless of value, so `disabled False` still disabled the input;
// the first fix made them strict-bool, which crashed any call-site
// that passed `()`. This lenient form accepts both.
func boolAttrPresent(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	// Unit `()` → struct{}{} in Go. Treat as "present".
	if _, ok := v.(struct{}); ok {
		return true
	}
	// Any other non-nil value: treat as present (True-ish). This
	// covers Sky's `Bool`-alias case where the runtime hands us an
	// `any` carrying a bool through a typed slot.
	return true
}
func Attr_checked(v any) any {
	if boolAttrPresent(v) {
		return attr("checked", "checked")
	}
	return attr("", "")
}
func Attr_disabled(v any) any {
	if boolAttrPresent(v) {
		return attr("disabled", "disabled")
	}
	return attr("", "")
}
func Attr_readonly(v any) any {
	if boolAttrPresent(v) {
		return attr("readonly", "readonly")
	}
	return attr("", "")
}
func Attr_required(v any) any {
	if boolAttrPresent(v) {
		return attr("required", "required")
	}
	return attr("", "")
}
func Attr_autofocus(v any) any {
	if boolAttrPresent(v) {
		return attr("autofocus", "autofocus")
	}
	return attr("", "")
}
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
// Css.hex accepts both "#fff" and "fff" forms — the leading '#' is
// idempotent so users can paste palette values either way without
// accidentally emitting ##fff which browsers treat as an unknown
// colour and silently ignore.
func Css_hex(s any) any {
	str := fmt.Sprintf("%v", s)
	if strings.HasPrefix(str, "#") {
		return str
	}
	return "#" + str
}

func Css_hexT(s string) string {
	if strings.HasPrefix(s, "#") {
		return s
	}
	return "#" + s
}

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
func Html_meta(a any, _ ...any) any { return htmlElem("meta")(a, nil) }
func Html_link(a any) any        { return htmlElem("link")(a, nil) }
func Html_script(a, c any) any   { return htmlElem("script")(a, c) }

// Html_titleNode — takes a raw string and wraps it in <title>.
func Html_titleNode(s any) any {
	return htmlElem("title")(nil, []any{Html_text(s)})
}

// Html_render: serialise a VNode to HTML string (for server-side rendering).
func Html_render(node any) any {
	if vn, ok := node.(VNode); ok {
		return renderVNode(vn, map[string]any{})
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
	// <textarea> has no `value` attribute in the HTML spec — its
	// displayed value is the TEXT CONTENT between the tags. Emitting
	// `<textarea value="...">` renders empty in every browser, which
	// means any server re-render (full-body fallback or innerHTML
	// patch at an ancestor) wipes the user's text out of the DOM.
	// Strip the value attr here and splice it in as child content
	// further down. A redundant `value="..."` kept on <select>
	// similarly has no effect (selection lives on <option selected>),
	// so strip there too.
	textareaValue := ""
	isTextarea := n.Tag == "textarea"
	if isTextarea || n.Tag == "select" {
		if v, ok := n.Attrs["value"]; ok {
			textareaValue = v
		}
	}
	for k, v := range n.Attrs {
		if (isTextarea || n.Tag == "select") && k == "value" {
			continue
		}
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
		// Event names starting with `sky-` are side-channel meta-events
		// (onImage, onFile) — not real DOM events that __skyBindOne
		// would addEventListener on. Render them as `data-sky-ev-<name>`
		// so the file/image driver can pick them up via the standard
		// HTML5 data-attribute convention. Plain DOM events (click,
		// input, change, …) keep the legacy `sky-<eventName>` naming
		// since __skyBindOne queries by that selector.
		var attr string
		if strings.HasPrefix(ev, "sky-") {
			attr = "data-sky-ev-" + ev
		} else {
			attr = "sky-" + ev
		}
		sb.WriteString(fmt.Sprintf(` %s="%s" data-sky-hid="%s"`,
			attr, html.EscapeString(msgName), id))
	}
	if isVoidTag(n.Tag) {
		sb.WriteString(" />")
		return sb.String()
	}
	sb.WriteString(">")
	// Textarea special-case: write the captured value as text content.
	// If the VNode already has text children (user wrote `textarea []
	// [ text "hi" ]`), those take precedence and the attr-derived
	// value is ignored — preserves existing behaviour.
	if isTextarea && textareaValue != "" && len(n.Children) == 0 {
		sb.WriteString(html.EscapeString(textareaValue))
	}
	// <script> and <style> bodies are raw text in HTML (CDATA-like):
	// escaping `'` to `&#39;` breaks the JS at parse time. Sky users
	// pass the body as a plain string (`script [] "code here"`), which
	// becomes a text VNode. Emit text children verbatim under these
	// tags; sub-elements still render normally (rare but valid for
	// <style> @import chains). Matches html/template's behaviour for
	// JSStr / CSSText contexts.
	rawBody := n.Tag == "script" || n.Tag == "style"
	// <select> uses child <option selected> to indicate the chosen
	// value. Mark the matching option inline — less invasive than
	// rebuilding the children tree.
	selectValue := ""
	if n.Tag == "select" && textareaValue != "" {
		selectValue = textareaValue
	}
	for _, c := range n.Children {
		if rawBody && c.Kind == "text" {
			sb.WriteString(c.Text)
		} else if selectValue != "" && c.Kind == "element" && c.Tag == "option" {
			// Copy the option, flipping `selected` on the matching value.
			// Shallow copy of Attrs so we don't mutate the caller's VNode.
			picked := c
			picked.Attrs = copyAttrs(c.Attrs)
			if picked.Attrs["value"] == selectValue {
				picked.Attrs["selected"] = "selected"
			} else {
				delete(picked.Attrs, "selected")
			}
			sb.WriteString(renderVNode(picked, handlers))
		} else {
			sb.WriteString(renderVNode(c, handlers))
		}
	}
	sb.WriteString("</")
	sb.WriteString(n.Tag)
	sb.WriteString(">")
	return sb.String()
}

func copyAttrs(src map[string]string) map[string]string {
	if src == nil {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
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
// a deterministic structural path id. Each non-root segment is
// `.<index>#<tag>[:<key>]` — the embedded tag means two structurally
// different subtrees never share an id at the same positional depth
// (e.g. a signIn `<input>` and a signUp `<fieldset>` at index 3 get
// different ids), so the diff walker cannot accidentally merge them.
// When an element carries a stable key (explicit `sky-key` attribute,
// or implicit from `name` on form-bearing tags), it's appended so
// keyed list items and named form fields keep identity across reorder.
// See docs/skylive/input-authority-protocol.md §Sky-id grammar.
func assignSkyIDs(n *VNode, path string) {
	if n.Kind != "element" {
		return
	}
	n.SkyID = path
	for i := range n.Children {
		child := &n.Children[i]
		if child.Kind != "element" {
			// Text/raw children don't get sky-ids; skip the tag lookup but
			// keep their positional index as-is so element siblings get the
			// same index they'd have had under the old scheme.
			continue
		}
		seg := path + "." + itoa(i) + "#" + child.Tag
		if k := skyIDKey(child); k != "" {
			seg += ":" + k
		}
		assignSkyIDs(child, seg)
	}
}

// skyIDKey returns a stable disambiguator for `n`, or "" if none applies.
// Priority: explicit `sky-key` attribute (set by `Html.keyed`) first,
// then `name` on form-bearing tags. Any matched value is sanitised to
// `[A-Za-z0-9_-]+` so it can't corrupt the sky-id grammar.
func skyIDKey(n *VNode) string {
	if k, ok := n.Attrs["sky-key"]; ok && k != "" {
		return sanitiseSkyIDKey(k)
	}
	switch n.Tag {
	case "input", "textarea", "select", "form", "button", "fieldset":
		if k, ok := n.Attrs["name"]; ok && k != "" {
			return sanitiseSkyIDKey(k)
		}
	}
	return ""
}

// sanitiseSkyIDKey replaces anything outside `[A-Za-z0-9_-]` with `_`.
// Prevents the key from breaking sky-id parsing, CSS selector escaping,
// or HTML attribute quoting.
func sanitiseSkyIDKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
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

// inputStateEntry carries the client's current idea of a dirty input.
// Sent inside eventRequest.InputState so the server can reconcile the
// rendered tree against the actual DOM before diffing. See
// docs/skylive/input-authority-protocol.md §Wire format.
type inputStateEntry struct {
	Value string `json:"value"`
	Seq   int64  `json:"seq"`
}

// batchedEvent is one entry inside eventRequest.Batch (set by
// navigator.sendBeacon on tab unload). Shape mirrors the top-level
// single-event fields minus SessionID / InputState, both of which
// live on the outer envelope so the server ingests them once before
// processing the batch.
type batchedEvent struct {
	Seq       int64             `json:"seq,omitempty"`
	Msg       string            `json:"msg"`
	Args      []json.RawMessage `json:"args"`
	HandlerID string            `json:"handlerId,omitempty"`
	Value     string            `json:"value,omitempty"`
}


// diffTrees: produce patches to transform `old` into `new_`. If either
// tree is missing (first render) the caller should fall back to a full
// innerHTML replace — diffTrees returns a single patch with the full
// new HTML.
//
// clientState is an optional per-sky-id map of "what the DOM actually
// shows right now" reported by the client in its last inputState
// snapshot. When present and a new_ element is a form field (input /
// textarea / select) whose value/checked/selected matches the client-
// reported value, we skip emitting the attr patch — the server
// re-deriving the user's own typing and shipping it back to them
// would otherwise race against ongoing keystrokes. See
// docs/skylive/input-authority-protocol.md §I5.
func diffTrees(old, new_ *VNode, clientState map[string]string) []Patch {
	var out []Patch
	diffNodes(old, new_, clientState, &out)
	return out
}


func diffNodes(old, new_ *VNode, clientState map[string]string, out *[]Patch) {
	if old == nil || new_ == nil {
		return
	}
	// Tag / kind change → replace subtree via HTML patch.
	if old.Tag != new_.Tag || old.Kind != new_.Kind {
		html := renderVNode(*new_, map[string]any{})
		*out = append(*out, Patch{ID: old.SkyID, HTML: &html})
		return
	}
	// Attrs diff — with client-value alignment for form fields so the
	// diff can't emit a value attr that reverts the user's typing.
	var attrChanges map[string]string
	inputTag := isFormInputTag(new_.Tag)
	clientVal, hasClient := "", false
	if inputTag && clientState != nil && new_.SkyID != "" {
		clientVal, hasClient = clientState[new_.SkyID]
	}
	for k, nv := range new_.Attrs {
		if ov, ok := old.Attrs[k]; !ok || ov != nv {
			if hasClient && isAuthorityControlledAttr(k) && nv == clientVal {
				// Server's intended value matches what the DOM actually
				// shows — no patch needed. Any keystrokes in flight stay
				// unclobbered; the client already has this value.
				continue
			}
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
		diffNodes(oc, nc, clientState, out)
	}
}

// isFormInputTag — tags whose value/checked/selected attrs are
// directly driven by the user rather than the server's model. A
// diff targeting these must defer to the client's in-flight typing
// (client-value alignment in diffNodes).
func isFormInputTag(t string) bool {
	return t == "input" || t == "textarea" || t == "select"
}

// isAuthorityControlledAttr — attrs the user drives directly on
// input/textarea/select. These get filtered through the client-
// value alignment check; everything else (class, style, aria-*,
// disabled, placeholder) diffs normally.
func isAuthorityControlledAttr(k string) bool {
	return k == "value" || k == "checked" || k == "selected"
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

// SkyCmd is the public type for Sky's Cmd msg type.
type SkyCmd = cmdT

type subT struct {
	kind   string // "none", "every"
	ms     int
	toMsg  any
}

// SkySub is the public type for Sky's Sub msg type.
type SkySub = subT

func Cmd_none() SkyCmd             { return cmdT{kind: "none"} }
func Cmd_batch(list any) SkyCmd    { return cmdT{kind: "batch", batch: asList(list)} }
func Cmd_perform(task, to any) SkyCmd { return cmdT{kind: "perform", task: task, toMsg: to} }

func Sub_none() SkySub { return subT{kind: "none"} }
func Sub_every(ms any, to any) SkySub {
	return subT{kind: "every", ms: AsInt(ms), toMsg: to}
}

// Time.every is an alias of Sub.every in Sky code
func Time_every(ms any, to any) SkySub { return Sub_every(ms, to) }

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
// Falls back to the legacy single-value form (sky_call(msg, value)) when
// the client didn't supply structured args — keeps older inputs working.
//
// A type-mismatch between the argument the client sent and the constructor's
// declared parameter type (e.g. a radio's onInput sending [true] into a
// String -> Msg constructor) used to panic deep inside reflect.Call. The
// guard below detects the mismatch before the call, logs a useful message
// with the msg/tag/expected-type/actual-type, and returns (msgDecodeError)
// so dispatch can drop the event without mutating model state.
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
		return safeSkyCall(msg, fallbackValue)
	}
	cur := msg
	for _, raw := range args {
		v := decodeMsgArg(cur, raw)
		if !argAssignableToFunc(cur, v) {
			logMsgDecodeError(cur, v, raw)
			return msgDecodeError{}
		}
		cur = safeSkyCall(cur, v)
		if _, ok := cur.(msgDecodeError); ok {
			return cur
		}
		if reflect.ValueOf(cur).Kind() != reflect.Func {
			break
		}
	}
	return cur
}

// decodeMsgArg JSON-decodes a wire arg directly into the concrete Go
// type the Msg constructor's first parameter declares (looked up
// via reflect on the function value). When the typed-codegen
// emits `func StateMsg_DoSignIn(c State_AuthCreds_R) any`, the
// wire bytes `{"email":"...","password":"..."}` decode straight
// into `State_AuthCreds_R{Email, Password}` — Go's
// json.Unmarshal does case-insensitive field matching, so Sky's
// lowercase source field names land in the PascalCase Go fields
// without any runtime guesswork.
//
// Falls back to the generic `var v any` decode when:
//   - The function's first param is `interface{}` (untyped Msg ctor —
//     most curried Sky lambdas land here, since the lowerer emits
//     `func(any) any` for them and reflect can't see a concrete
//     param type at the boundary).
//   - The typed decode fails (wire shape doesn't match the target —
//     dispatch then surfaces a structured msgDecodeError).
//
// Replaces the previous "decode to any then reshape via reflect"
// strategy: that approach worked but pushed type knowledge into
// runtime guessing; this one uses the type information that's
// already in scope at the dispatch boundary.
func decodeMsgArg(fn any, raw json.RawMessage) any {
	rv := reflect.ValueOf(fn)
	if rv.Kind() == reflect.Func && rv.Type().NumIn() > 0 {
		paramT := rv.Type().In(0)
		if paramT.Kind() != reflect.Interface {
			ptr := reflect.New(paramT)
			if err := json.Unmarshal(raw, ptr.Interface()); err == nil {
				return ptr.Elem().Interface()
			}
			// Typed decode failed — fall through to the any-decode
			// path; narrowMsgArg handles the cases where the wire
			// JSON shape needs reshaping (typed slices, Sky generic
			// container cross-instantiation) before reflect.Call.
		}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		v = string(raw)
	}
	return narrowMsgArg(fn, v)
}

// narrowMsgArg attempts to narrow a wire-decoded `arg` to the first
// parameter type of `fn` for structural reshapes only (map[K]any →
// map[K]X, []any → []X, SkyResult/Maybe/Tuple cross-instantiation).
// Lossy any-to-primitive conversions (the `target.Kind() == String`
// fmt.Sprintf path inside narrowReflectValue) are intentionally NOT
// applied here — a radio's onInput sending [true] into a
// `String -> Msg` constructor must still return msgDecodeError, not
// silently coerce to "true".
//
// The shape this fixes: `<form onSubmit=...>` extracts formData and
// JSON-decodes the wire arg as `map[string]interface {}`, but the
// user's Msg constructor is typed `Dict String String -> Msg` so
// the typed-codegen lowers it to `map[string]string`. The plain
// reflect AssignableTo check rejects the assignment without this
// narrowing; same map-narrowing logic the rest of the runtime uses
// at FFI / record-update boundaries (rt.AsMapT, narrowReflectValue).
func narrowMsgArg(fn any, arg any) any {
	if arg == nil {
		return arg
	}
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func || rv.Type().NumIn() == 0 {
		return arg
	}
	paramT := rv.Type().In(0)
	if paramT.Kind() == reflect.Interface {
		return arg
	}
	srcV := reflect.ValueOf(arg)
	if !srcV.IsValid() || srcV.Type().AssignableTo(paramT) {
		return arg
	}
	// Only structural reshapes: map / slice / Sky-container struct /
	// map → record-alias struct. Skip the fmt.Sprintf-into-string
	// fallback in narrowReflectValue — that would silently turn a
	// wrong-type radio bool into the string "true" and pass it to a
	// String-typed Msg constructor.
	switch {
	case paramT.Kind() == reflect.Map && srcV.Kind() == reflect.Map:
		out := coerceMapValue(srcV, paramT)
		if out.IsValid() {
			return out.Interface()
		}
	case paramT.Kind() == reflect.Slice && srcV.Kind() == reflect.Slice:
		out := coerceSliceValue(srcV, paramT)
		if out.IsValid() {
			return out.Interface()
		}
	case paramT.Kind() == reflect.Struct && srcV.Kind() == reflect.Struct:
		if out, ok := narrowSkyContainer(srcV, paramT); ok {
			return out.Interface()
		}
	case paramT.Kind() == reflect.Struct && srcV.Kind() == reflect.Map:
		// Record-alias Msg arg fed by form data: the wire payload is
		// `map[string]any` (JSON-decoded form fields), but the Sky
		// constructor takes a typed record alias which lowers to a
		// named Go struct (e.g. `State_AuthCreds_R{Email, Password}`).
		// Walk the target struct's fields and look up each by lower-
		// camel name in the source map (Sky's field naming becomes Go
		// PascalCase via capitaliseFirst on emit, so "email" in the
		// form maps to the "Email" struct field).
		if out, ok := mapToRecordStruct(srcV, paramT); ok {
			return out.Interface()
		}
	}
	return arg
}

// mapToRecordStruct narrows a map[string]any (or map[string]string)
// payload to a typed record-alias struct (the Go shape Sky emits
// for `type alias X = { ... }`). Field lookup is case-insensitive
// on the first character so Sky's lowercase field names match Go's
// PascalCase struct field names. Each value is narrowed to its
// target field type via narrowReflectValue (which handles
// nested maps / slices / Sky-container struct reshaping).
//
// Returns (zero, false) when the source isn't a string-keyed map,
// when no fields could be populated, or when any required field
// has an incompatible value type — caller falls back to the
// existing decode-error path so the user still sees a structured
// log line.
func mapToRecordStruct(src reflect.Value, target reflect.Type) (reflect.Value, bool) {
	if src.Kind() != reflect.Map || src.Type().Key().Kind() != reflect.String {
		return reflect.Value{}, false
	}
	out := reflect.New(target).Elem()
	matched := 0
	for i := 0; i < target.NumField(); i++ {
		fname := target.Field(i).Name
		// Lookup variants: PascalCase (struct field), lowercase
		// first letter (Sky source convention), exact match.
		var srcField reflect.Value
		for _, k := range []string{fname, lowerFirst(fname)} {
			if v := src.MapIndex(reflect.ValueOf(k)); v.IsValid() {
				srcField = v
				break
			}
		}
		if !srcField.IsValid() {
			continue
		}
		// Map values come out as reflect.Value wrapping `any`;
		// unwrap before narrowing to the target field type.
		if srcField.Kind() == reflect.Interface {
			if srcField.IsNil() {
				continue
			}
			srcField = srcField.Elem()
		}
		outF := out.Field(i)
		if !outF.CanSet() {
			continue
		}
		if srcField.Type().AssignableTo(outF.Type()) {
			outF.Set(srcField)
			matched++
			continue
		}
		narrowed := narrowReflectValue(srcField, outF.Type())
		if narrowed.IsValid() {
			outF.Set(narrowed)
			matched++
		}
	}
	if matched == 0 {
		return reflect.Value{}, false
	}
	return out, true
}

// lowerFirst lowercases the first rune of s using Unicode rules,
// preserving the rest of the string unchanged. Used to map Go's
// PascalCase struct field names back to Sky's lowerCamelCase source
// convention so map-decoded form data finds the right struct field
// regardless of script (Latin, Greek, Cyrillic, etc.). ASCII char
// comparison would have silently mishandled non-Latin field names.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	first, size := utf8.DecodeRuneInString(s)
	if first == utf8.RuneError {
		return s
	}
	lo := unicode.ToLower(first)
	if lo == first {
		return s
	}
	return string(lo) + s[size:]
}

// msgDecodeError — sentinel value returned from applyMsgArgs when the
// client's wire-level arguments can't be coerced onto the Msg
// constructor's parameters. dispatch() recognises it and drops the
// event cleanly (no model mutation, no view re-render). Not a Go
// error because it flows through the Msg pipeline and has to be
// distinguished from legitimate Msg ADT values.
type msgDecodeError struct{}

// argAssignableToFunc — reports whether the first parameter of `fn`
// will accept `arg` via reflect.Call. Returns true for interface
// params (the common Sky case — most curried constructors take
// `any`) and for exact-type matches. The check is intentionally
// conservative: we'd rather let a near-miss through to reflect's own
// error handling than reject legitimate dispatches.
func argAssignableToFunc(fn any, arg any) bool {
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return true
	}
	ft := rv.Type()
	if ft.NumIn() == 0 {
		return true
	}
	paramT := ft.In(0)
	if paramT.Kind() == reflect.Interface {
		// `any` (or any interface type the arg satisfies) — defer to
		// runtime. Nearly every Sky lambda lands here.
		if arg == nil {
			return true
		}
		return reflect.TypeOf(arg).Implements(paramT)
	}
	if arg == nil {
		// Typed param can't accept a nil for most kinds; let reflect
		// surface the specific error if we're wrong.
		switch paramT.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			return true
		}
		return false
	}
	argT := reflect.TypeOf(arg)
	return argT.AssignableTo(paramT)
}

// safeSkyCall wraps sky_call with a panic recover so a reflect-level
// type mismatch that slips past argAssignableToFunc (custom func shapes,
// variadics, etc.) still surfaces as a logged msgDecodeError rather than
// crashing the dispatch goroutine. The outer panic-recover in /_sky/event
// would otherwise catch it too, but with less context.
func safeSkyCall(fn any, arg any) (result any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr,
				"[sky.live] Msg dispatch recovered from panic: %v "+
					"(fn kind=%s, arg=%T %v)\n",
				r, reflect.ValueOf(fn).Kind(), arg, arg)
			result = msgDecodeError{}
		}
	}()
	return sky_call(fn, arg)
}

// logMsgDecodeError — structured message to stderr when a client-sent
// argument doesn't fit the Msg constructor's parameter. Gives the
// developer enough to find the mis-bound handler in their view.
func logMsgDecodeError(fn any, arg any, raw json.RawMessage) {
	rv := reflect.ValueOf(fn)
	expected := "<unknown>"
	if rv.Kind() == reflect.Func && rv.Type().NumIn() > 0 {
		expected = rv.Type().In(0).String()
	}
	fnName := ""
	if rv.Kind() == reflect.Func {
		fnName = runtime.FuncForPC(rv.Pointer()).Name()
	}
	fmt.Fprintf(os.Stderr,
		"[sky.live] Msg decode error: %s expected %s but got %T (%v); "+
			"raw=%s. Likely fix: check the view binding — e.g. onInput on a "+
			"radio sends [checked:bool], not the value. Use onClick with a "+
			"fully-applied Msg per radio instead.\n",
		fnName, expected, arg, arg, string(raw))
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
	// SSE outbound channel: any writer goroutine may push a frame.
	// Frame contents are JSON envelopes produced by encodeSSEFrame
	// (carry seq + ackInputs alongside the body), not raw HTML.
	sseCh chan string
	// Cancel function for any active subscription ticker
	cancelSub chan struct{}

	// Single session-wide monotonic counter for EVERY outgoing frame
	// (event reply OR SSE patch). Bumped under sess.mu so the value
	// reflects this session's true mutation order. The client keys its
	// stale-drop / cross-channel ordering off this number.
	outSeq int64
	// Per input sky-id → largest req.InputState[id].Seq observed. Used
	// to populate response.ackInputs so the client can retire "dirty"
	// flags once the server has caught up. Stale ids (not present in
	// prevTree) are evicted on each ack build; see ackInputsForPrevTree.
	inputSeqs map[string]int64
}

// nextOutSeq advances and returns the session-wide outgoing seq.
// MUST be called with sess.mu held.
func (s *liveSession) nextOutSeq() int64 {
	s.outSeq++
	return s.outSeq
}

// ingestInputState absorbs the client's dirty-input snapshot into
// sess.inputSeqs, retaining the larger seq per id. No state is lost
// on concurrent events because every caller holds sess.mu.
func (s *liveSession) ingestInputState(state map[string]inputStateEntry) {
	if len(state) == 0 {
		return
	}
	if s.inputSeqs == nil {
		s.inputSeqs = make(map[string]int64, len(state))
	}
	for id, e := range state {
		if e.Seq > s.inputSeqs[id] {
			s.inputSeqs[id] = e.Seq
		}
	}
}

// clientStateFromRequest projects inputStateEntry.Value only, for
// feeding into diffNodes (Step 3 consumer). Step 2 only builds this
// projection for forward compatibility — no caller uses it yet.
func clientStateFromRequest(state map[string]inputStateEntry) map[string]string {
	if len(state) == 0 {
		return nil
	}
	out := make(map[string]string, len(state))
	for id, e := range state {
		out[id] = e.Value
	}
	return out
}

// ackInputsForPrevTree returns the subset of sess.inputSeqs whose ids
// still appear in prevTree. Entries whose element has unmounted are
// evicted as a side effect so the map doesn't accumulate dead ids.
// Returns nil if nothing to ack (client's __skyInputs map reads nil as
// "no updates"). MUST be called with sess.mu held.
func ackInputsForPrevTree(s *liveSession) map[string]int64 {
	if len(s.inputSeqs) == 0 {
		return nil
	}
	present := map[string]struct{}{}
	if s.prevTree != nil {
		var walk func(*VNode)
		walk = func(n *VNode) {
			if n.Kind == "element" && n.SkyID != "" {
				present[n.SkyID] = struct{}{}
			}
			for i := range n.Children {
				walk(&n.Children[i])
			}
		}
		walk(s.prevTree)
	}
	out := make(map[string]int64, len(s.inputSeqs))
	for id, seq := range s.inputSeqs {
		if _, ok := present[id]; ok {
			out[id] = seq
		} else {
			delete(s.inputSeqs, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// encodeSSEFrame serialises a body plus the session-wide seq + ack
// inputs into a JSON envelope. The consumer-side EventSource listener
// parses it back out. MUST be called with sess.mu held.
func encodeSSEFrame(sess *liveSession, body string) string {
	frame := map[string]any{
		"seq":  sess.nextOutSeq(),
		"body": body,
	}
	if ack := ackInputsForPrevTree(sess); ack != nil {
		frame["ackInputs"] = ack
	}
	b, err := json.Marshal(frame)
	if err != nil {
		// Marshalling a map of primitives can't fail in practice, but
		// fall back to a bare seq+body frame just in case so the
		// channel never carries a garbage string.
		return fmt.Sprintf(`{"seq":%d,"body":%q}`, sess.outSeq, body)
	}
	return string(b)
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
//
// Single-page apps (`routes = []`) treat "/" as the implicit root.
// Without this, an existing-session refresh on "/" hits the
// not-routed-AND-existing 404 guard in handleInitial — every refresh
// returns 404 even though "/" is the only page the app has. Other
// paths still 404 (browser noise like /favicon.ico shouldn't render
// the SPA), so handler-state protection survives.
func matchAnyRoute(app *liveApp, urlPath string) ([]string, bool) {
	for _, rt := range app.routes {
		if params, ok := matchRoute(rt.path, urlPath); ok {
			return params, true
		}
	}
	if len(app.routes) == 0 && urlPath == "/" {
		return nil, true
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
// Live_app: Task-shaped per Task-everywhere (2026-04-24+). The
// whole "set up routes + handlers + sessions + bind port" sequence
// is wrapped in a thunk so the server start defers to the entry-
// point Task.run boundary. Calling `Live_app(cfg)` returns the
// thunk; Task.run forces it and then http.Server.ListenAndServe
// blocks (or returns Err on bind failure).
func Live_app(cfg any) any {
	return func() any {
		return liveAppRun(cfg)
	}
}

func liveAppRun(cfg any) any {
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
	} else if v := os.Getenv("SKY_LIVE_STATIC_DIR"); v != "" {
		// SKY_LIVE_STATIC_DIR is the documented name (matches the
		// SKY_LIVE_* env var convention). SKY_STATIC_DIR is kept as
		// a backward-compat alias so existing deployments don't
		// break — read it only when the canonical name is unset.
		app.staticDir = v
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
	// can decode existing sessions on restart.
	// Two passes:
	//   1. Type-graph walk: registers SkyMaybe[User_R] etc. even when
	//      init returns Nothing/[]/empty — walks the struct DEFINITION,
	//      not the runtime value, so concrete generic instantiations
	//      in struct fields are caught.
	//   2. Value walk: catches anything the type walker misses (e.g.
	//      dynamically-typed map entries).
	func() {
		defer func() { recover() }()
		req := map[string]any{"path": "/"}
		res := sky_call(app.init, req)
		model := tupleFirst(res)
		GobRegisterTypeGraph(reflect.TypeOf(model))
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
	// No third-party font is loaded and no font-family is forced.
	// Apps choose their own typography via their own stylesheet (e.g.
	// styleNode in their view, or static-served self-hosted webfonts).
	// Privacy: no Google Fonts request. Accessibility: no !important
	// override fighting app-level type choices.
	fmt.Fprintf(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"></head><body><div id=\"sky-root\">%s</div><script>%s</script></body></html>", body, liveJS(sid))
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
	// TEA wire format — see docs/skylive/input-authority-protocol.md
	// §Wire format. Fields added in the v0.9.3+ protocol upgrade are
	// all optional: old clients keep working, new clients opt into
	// sequenced authority by populating seq + inputState + batch.
	var req struct {
		SessionID  string                     `json:"sessionId"`
		Msg        string                     `json:"msg"`
		Args       []json.RawMessage          `json:"args"`
		HandlerID  string                     `json:"handlerId"`
		Value      string                     `json:"value"` // legacy fallback
		Seq        int64                      `json:"seq,omitempty"`
		InputState map[string]inputStateEntry `json:"inputState,omitempty"`
		Batch      []batchedEvent             `json:"batch,omitempty"`
	}
	// Bound event payload. Default 5 MiB (was 1 MiB hardcoded) —
	// tiny JSON envelopes need almost nothing, but `Event.onFile` /
	// `Event.onImage` ship the file as a base64 data URL through
	// this same channel, so a 4 MiB image (~5.4 MiB base64) needs
	// the bigger headroom. Override via SKY_LIVE_MAX_BODY_BYTES (or
	// sky.toml [live] maxBodyBytes) — the fileMaxSize attr on the
	// input is the client-side guard but isn't load-bearing for the
	// server cap. Server-side validation in `update` is the
	// authoritative check; this is the upper bound on what reaches
	// the runtime at all.
	maxBody := int64(5 << 20)
	if n, ok := parsePositiveInt(os.Getenv("SKY_LIVE_MAX_BODY_BYTES")); ok {
		maxBody = int64(n)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
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

	// Batch path — sendBeacon flushes a sequence of pending-debounce
	// events on tab unload. Each entry is processed as if it had
	// arrived on its own, under the single sess.mu held by each
	// dispatch. The outer InputState is ingested once before the
	// batch runs so all dispatches see the final DOM values.
	if len(req.Batch) > 0 {
		sess.mu.Lock()
		sess.ingestInputState(req.InputState)
		sess.mu.Unlock()
		for _, ev := range req.Batch {
			app.dispatchBatched(sess, ev)
		}
		// sendBeacon can't read the response — 204 just signals OK.
		w.WriteHeader(http.StatusNoContent)
		return
	}

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
	// Reconcile the client's view of dirty inputs into sess.inputSeqs
	// before dispatch. Step 3 activates the diff-level client-value
	// alignment that uses this state; Step 2 only records it so the
	// ackInputs response field reflects what the server has observed.
	sess.ingestInputState(req.InputState)
	// Keep a reference to the previous tree BEFORE dispatch mutates it.
	prev := sess.prevTree
	body2 := app.dispatch(sess, msg)
	newTree := sess.prevTree
	// Capture outgoing protocol metadata before releasing the lock so
	// the seq reflects this session's true mutation order. Bumped once
	// per reply (including no-op replies) so the client's cross-channel
	// ordering works uniformly.
	respSeq := sess.nextOutSeq()
	respAck := ackInputsForPrevTree(sess)
	sess.mu.Unlock()
	// Persist the mutated session so DB-backed stores see the new
	// state. Memory store is a no-op on Set for an already-tracked sid.
	app.store.Set(req.SessionID, sess)

	// dispatch returns "" when the event produced a byte-identical
	// view (no-op update). Reply with an empty patch list so the
	// client acknowledges the event without the server shipping a
	// redundant HTML frame.
	if body2 == "" {
		writeEventJSON(w, respSeq, req.Seq, respAck, nil)
		return
	}
	// When we have a prior tree we can reply with a minimal patch set
	// (preserving unrelated DOM state client-side). On first interaction
	// (prev == nil) or when the tree shape changed so drastically that
	// every patch is a full-HTML replace anyway, fall back to the full
	// innerHTML body.
	if prev != nil && newTree != nil {
		patches := diffTrees(prev, newTree, clientStateFromRequest(req.InputState))
		if len(patches) > 0 && !patchesAreFullReplace(patches) {
			writeEventJSON(w, respSeq, req.Seq, respAck, patches)
			return
		}
	}
	writeEventHTML(w, respSeq, respAck, body2)
}

// writeEventJSON emits the structured /_sky/event response envelope:
// {seq, respondingTo, ackInputs, patches}. patches may be nil/empty.
// The three protocol fields survive alongside the legacy `patches` key
// so pre-upgrade clients continue to deserialise cleanly.
func writeEventJSON(w http.ResponseWriter, seq, respondingTo int64, ackInputs map[string]int64, patches []Patch) {
	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{
		"seq":     seq,
		"patches": patches,
	}
	if patches == nil {
		payload["patches"] = []any{}
	}
	if respondingTo > 0 {
		payload["respondingTo"] = respondingTo
	}
	if ackInputs != nil {
		payload["ackInputs"] = ackInputs
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// writeEventHTML emits the full-body fallback. Protocol metadata rides
// in headers so the client can update its seq bookkeeping without
// parsing the HTML — X-Sky-Seq (single counter) and X-Sky-Ack-Inputs
// (JSON-encoded map, absent when empty).
func writeEventHTML(w http.ResponseWriter, seq int64, ackInputs map[string]int64, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html")
	h.Set("X-Sky-Seq", strconv.FormatInt(seq, 10))
	if ackInputs != nil {
		if b, err := json.Marshal(ackInputs); err == nil {
			h.Set("X-Sky-Ack-Inputs", string(b))
		}
	}
	_, _ = w.Write([]byte(body))
}

// dispatchBatched processes one entry from eventRequest.Batch. The
// locking discipline and handler-lookup rules mirror the single-event
// path; the only difference is no response is produced (sendBeacon
// discards it), and any SSE side effects flow through sess.sseCh.
// Failures are swallowed — a batch arrives on tab-unload so there's
// no user-visible place to surface them.
func (app *liveApp) dispatchBatched(sess *liveSession, ev batchedEvent) {
	sess.mu.Lock()
	if len(sess.handlers) == 0 && sess.model != nil {
		sess.handlers = map[string]any{}
		vn := sky_call(app.view, sess.model).(VNode)
		assignSkyIDs(&vn, "r")
		_ = renderVNode(vn, sess.handlers)
		sess.prevTree = &vn
	}
	msg, ok := sess.handlers[ev.HandlerID]
	if !ok && ev.Msg != "" && ev.HandlerID == "" {
		tag := -1
		if t, found := LookupAdtTag(ev.Msg); found {
			tag = t
		} else {
			app.msgTagsMu.Lock()
			if t2, ok2 := app.msgTags[ev.Msg]; ok2 {
				tag = t2
			}
			app.msgTagsMu.Unlock()
		}
		var fields []any
		for _, raw := range ev.Args {
			var v any
			if err := json.Unmarshal(raw, &v); err == nil {
				fields = append(fields, v)
			}
		}
		msg = SkyADT{Tag: tag, SkyName: ev.Msg, Fields: fields}
		ok = true
	}
	if !ok {
		sess.mu.Unlock()
		return
	}
	if _, isSkyAdt := msg.(SkyADT); !isSkyAdt {
		msg = applyMsgArgs(msg, ev.Args, ev.Value)
	}
	body2 := app.dispatch(sess, msg)
	// Bump outSeq once per batched entry so any SSE frame pushed as a
	// side effect carries a unique seq. Each dispatch that mutates the
	// view is its own observable event.
	var frame string
	if body2 != "" {
		frame = encodeSSEFrame(sess, body2)
	}
	sess.mu.Unlock()
	// Push to other subscribers (other tabs, SSE listeners). The
	// originating tab has already unloaded so the frame is for anyone
	// else observing the session.
	if frame != "" {
		select {
		case sess.sseCh <- frame:
		default:
		}
	}
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
//
// A msgDecodeError value arriving here (from applyMsgArgs rejecting a
// wire-level type mismatch, e.g. a radio's onInput sending a boolean
// into a String -> Msg constructor) drops the event: no update runs,
// no model mutation, no re-render. The error has already been logged
// with useful context at the dispatch boundary.
//
// update/view/guard panics are recovered here as a last-line defence
// so one malformed handler can't crash the session; the view simply
// falls back to its last rendered body.
func (app *liveApp) dispatch(sess *liveSession, msg any) (body string) {
	if _, bad := msg.(msgDecodeError); bad {
		// applyMsgArgs already logged the specific mismatch. Return "" so
		// the client sees an empty patch list (no visible change) and
		// session state stays consistent.
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr,
				"[sky.live] dispatch panic recovered, dropping event: %v\n%s\n",
				r, debug.Stack())
			body = ""
		}
	}()
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
	body = renderVNode(vn, sess.handlers)
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
	// Push update through locked dispatch, then emit an SSE frame
	// carrying the session-wide seq. Keeping frame construction under
	// the same lock as dispatch means the seq reflects the actual
	// mutation order even when other goroutines dispatch concurrently.
	sess.mu.Lock()
	body := app.dispatch(sess, msg)
	var frame string
	if body != "" {
		frame = encodeSSEFrame(sess, body)
	}
	sess.mu.Unlock()
	if frame == "" {
		return
	}
	select {
	case sess.sseCh <- frame:
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
				var frame string
				if body != "" {
					frame = encodeSSEFrame(sess, body)
				}
				sess.mu.Unlock()
				// Suppress SSE write when the tick didn't change
				// the view — prevents Time.every from pushing an
				// identical HTML frame every interval.
				if frame == "" {
					continue
				}
				select {
				case sess.sseCh <- frame:
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

// liveBannerConfig collects the SKY_LIVE_* env vars that influence
// the connection-status banner so they can be templated into the
// init script. Each var has a sensible default; users override via
// shell env or .env.
type liveBannerConfig struct {
	Enabled     bool
	BaseMs      int
	MaxMs       int
	MaxAttempts int
	QueueMax    int
}

func loadLiveBannerConfig() liveBannerConfig {
	cfg := liveBannerConfig{
		Enabled:     true,
		BaseMs:      500,
		MaxMs:       16000,
		MaxAttempts: 10,
		QueueMax:    50,
	}
	// SKY_LIVE_BANNER=off disables the banner entirely (still queues
	// + retries POSTs — just no chrome). Useful when an app wants to
	// render its own connection UI in the user's view.
	if v := os.Getenv("SKY_LIVE_BANNER"); v == "off" || v == "0" || v == "false" {
		cfg.Enabled = false
	}
	if n, ok := parsePositiveInt(os.Getenv("SKY_LIVE_RETRY_BASE_MS")); ok {
		cfg.BaseMs = n
	}
	if n, ok := parsePositiveInt(os.Getenv("SKY_LIVE_RETRY_MAX_MS")); ok {
		cfg.MaxMs = n
	}
	if n, ok := parsePositiveInt(os.Getenv("SKY_LIVE_RETRY_MAX_ATTEMPTS")); ok {
		cfg.MaxAttempts = n
	}
	if n, ok := parsePositiveInt(os.Getenv("SKY_LIVE_QUEUE_MAX")); ok {
		cfg.QueueMax = n
	}
	return cfg
}

func parsePositiveInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func liveJS(sid string) string {
	cfg := loadLiveBannerConfig()
	return fmt.Sprintf(`
var __skySid = %q;
var __skyBannerEnabled = %t;
var __skyRetryBaseMs = %d;
var __skyRetryMaxMs = %d;
var __skyRetryMaxAttempts = %d;
var __skyEventQueueMax = %d;

// ── Input authority protocol state ───────────────────────────
// See docs/skylive/input-authority-protocol.md §Client state.
// Step 2 populates these counters + per-input table on every send
// and response; Step 3 activates the patch filter that reads them;
// Step 4 activates the stale-drop test against __skyLastAppliedSeq.
var __skyClientSeq = 0;       // monotonic, client-owned; bumped on every __skySend
var __skyLastAppliedSeq = 0;  // server-owned; largest seq already applied
var __skyInputs = {};         // sky-id → InputEntry (populated by __skyBindOne)

function __skyInputEntry(sid) {
  var e = __skyInputs[sid];
  if (!e) {
    e = __skyInputs[sid] = {
      liveValue: "", lastSentSeq: 0, lastAckedSeq: 0,
      pendingDebounceId: null, pendingSend: null
    };
  }
  return e;
}

// __skyInputsSnapshot — dirty-input projection bundled into every
// outgoing event. Only entries whose user-typed value is newer than
// the server's latest ack are included, so the wire stays compact
// when the client and server agree.
function __skyInputsSnapshot() {
  var out = null;
  var ids = Object.keys(__skyInputs);
  for (var i = 0; i < ids.length; i++) {
    var e = __skyInputs[ids[i]];
    if (e.lastSentSeq <= e.lastAckedSeq) continue;
    if (!out) out = {};
    out[ids[i]] = {value: e.liveValue, seq: e.lastSentSeq};
  }
  return out;
}

// __skyIngestSeq — fold a response or SSE frame's {seq, ackInputs}
// into client state. seq advances __skyLastAppliedSeq monotonically;
// ackInputs retires per-input dirty flags so the next snapshot omits
// caught-up fields.
// __skyIsDirty — a typable form field (input / textarea / select)
// whose DOM state is authoritative over the server's view. The check
// is scoped to those tags ONLY: buttons, anchors, divs and other
// focused-but-non-typable elements have no keystrokes to preserve,
// so treating them as dirty would wrongly block patches that wipe
// their containing subtree (e.g. navigating from a "new game"
// screen into a board view, where the focused button legitimately
// disappears). Scope signals: focus, pending debounce keyed by
// data-sky-hid, or an unacked typed value at the input's sky-id.
function __skyIsDirty(el) {
  if (!el || el.nodeType !== 1) return false;
  var tag = el.tagName;
  if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT") return false;
  if (el === document.activeElement) return true;
  var hid = el.getAttribute && el.getAttribute("data-sky-hid");
  if (hid && __skyInputPending[hid]) return true;
  var sid = el.getAttribute && el.getAttribute("sky-id");
  if (sid) {
    var e = __skyInputs[sid];
    if (e && e.lastSentSeq > e.lastAckedSeq) return true;
  }
  return false;
}

function __skyIngestSeq(seq, ackInputs) {
  if (typeof seq === "number" && seq > __skyLastAppliedSeq) {
    __skyLastAppliedSeq = seq;
  }
  if (ackInputs) {
    var ids = Object.keys(ackInputs);
    for (var i = 0; i < ids.length; i++) {
      var e = __skyInputs[ids[i]];
      if (!e) continue;
      var n = ackInputs[ids[i]];
      if (n > e.lastAckedSeq) e.lastAckedSeq = n;
    }
  }
}

// __skyHandleResponse — gate DOM-mutating work behind the monotonic
// seq check (Step 4 / I2). An out-of-order or replayed frame with
// seq ≤ __skyLastAppliedSeq is dropped entirely: a newer frame has
// already landed with a later view, and applying the stale payload
// would regress the DOM. Legacy frames that omit seq (or report 0)
// always apply — pre-upgrade servers keep working.
function __skyHandleResponse(seq, ackInputs, applyFn) {
  if (typeof seq === "number" && seq > 0 && seq <= __skyLastAppliedSeq) {
    return; // stale — a newer frame already landed
  }
  __skyIngestSeq(seq, ackInputs);
  applyFn();
}

// ── Focus preservation via node identity ────────────────────
// Sky.Live renders subtrees via innerHTML replacement (both on JSON
// patches that carry p.html and on full-HTML navigations). Plain
// innerHTML DESTROYS the focused input element — even though JS is
// single-threaded, the browser's internal input-method editor (IME),
// autofill popover, undo stack, composition state, pointer-cursor
// blink, password manager affordances, and native caret are all
// tied to the live DOM NODE. Destroying it and recreating a clone
// with the same .value loses every one of those.
//
// The correct fix is to preserve node identity through the swap:
// before the replacement, locate the focused INPUT / TEXTAREA /
// SELECT, find its placeholder in the new HTML (by sky-id → name),
// then SPLICE the live node into the new tree in place of the
// placeholder. Server-side attrs (class, type, placeholder, ...)
// get copied onto the live node, EXCEPT value/checked/selected —
// those stay under user authority.
//
// The live node never gets "destroyed" — it only moves between
// parents. .value, .selectionStart, IME state, composition buffer,
// autofill state all survive. Keystrokes in flight land on the
// same node regardless of where the browser has currently attached
// it in the DOM tree.
//
// Re-focus at the end because replaceChild on a focused element
// temporarily blurs it (focus isn't a property of the node, it's
// a property of the document). Selection is lost and must be
// restored too.

// __skyReplaceHTMLPreservingFocus — the authoritative swap.
// Drop-in for plain innerHTML assignment that keeps focused input
// state across the replacement. Used by both __skyPatch (full body)
// and __skyApplyPatches (p.html patches).
function __skyReplaceHTMLPreservingFocus(container, newHTML) {
  // Find the focused input inside this container (if any).
  var focused = document.activeElement;
  var focusedInside = focused && focused !== document.body &&
      container.contains(focused) &&
      (focused.tagName === "INPUT" ||
       focused.tagName === "TEXTAREA" ||
       focused.tagName === "SELECT");
  if (!focusedInside) {
    // Fast path — no live input to preserve.
    container.innerHTML = newHTML;
    return;
  }

  // Capture selection + scroll on the LIVE node before any swap
  // (selection read throws on some input types, so catch it).
  var selStart = null, selEnd = null;
  try {
    selStart = focused.selectionStart;
    selEnd   = focused.selectionEnd;
  } catch (_) {}
  var scrollTop = focused.scrollTop;
  var focusSid  = focused.getAttribute && focused.getAttribute("sky-id");
  var focusName = focused.getAttribute && focused.getAttribute("name");
  var focusTag  = focused.tagName.toLowerCase();

  // Parse the new HTML into a detached element so we can surgery
  // its tree before attaching it to the DOM.
  var tmp = document.createElement("div");
  tmp.innerHTML = newHTML;

  // Locate the placeholder for the live focused input in the new
  // tree. sky-id first (structural + keyed, uniquely stable), fall
  // back to tag+name if the structure shifted but the named form
  // field is still present.
  var placeholder = null;
  if (focusSid) {
    placeholder = tmp.querySelector('[sky-id="' + focusSid.replace(/"/g, '\\"') + '"]');
  }
  if (!placeholder && focusName) {
    placeholder = tmp.querySelector(focusTag + '[name="' + focusName + '"]');
  }

  if (!placeholder) {
    // The server's new view unmounts this input. User's typed value
    // and focus are legitimately gone — honour the server. Fast path.
    container.innerHTML = newHTML;
    return;
  }

  // Copy server-side attrs onto the live input, except the three
  // the user owns (value / checked / selected). class, type,
  // placeholder, disabled, aria-*, etc. all propagate.
  __skyCopyAttrsExceptAuthority(placeholder, focused);

  // Splice: move the live node into the new tree in the placeholder's
  // slot. replaceChild detaches focused from container (implicit)
  // and attaches it to tmp.
  placeholder.parentNode.replaceChild(focused, placeholder);

  // Commit: container's current children (minus the focused input,
  // which we already moved into tmp) are thrown away; tmp's children
  // (which include the focused input in its new position) become
  // container's children.
  while (container.firstChild) container.removeChild(container.firstChild);
  while (tmp.firstChild) container.appendChild(tmp.firstChild);

  // Focus was lost during the move (replaceChild + removeChild +
  // appendChild all drop focus). Restore it on the SAME node — so
  // .value, IME state, composition buffer, etc. are still the ones
  // the user was interacting with.
  try { focused.focus({preventScroll: true}); } catch (_) { focused.focus(); }
  if (typeof focused.setSelectionRange === "function" &&
      selStart !== null && selEnd !== null) {
    try { focused.setSelectionRange(selStart, selEnd); } catch (_) {}
  }
  if (scrollTop) focused.scrollTop = scrollTop;
}

// __skyCopyAttrsExceptAuthority — mirror attrs from src onto dst,
// skipping the three the user drives directly. Removes attrs on
// dst that aren't in src (same "skip" rule). Used when splicing a
// live focused input into a server-rendered placeholder.
function __skyCopyAttrsExceptAuthority(src, dst) {
  if (!src || !dst || !src.attributes || !dst.attributes) return;
  var isAuthority = function(n) {
    return n === "value" || n === "checked" || n === "selected";
  };
  // Drop attrs that aren't present in src.
  var toRemove = [];
  for (var i = 0; i < dst.attributes.length; i++) {
    var n = dst.attributes[i].name;
    if (isAuthority(n)) continue;
    if (!src.hasAttribute(n)) toRemove.push(n);
  }
  for (var r = 0; r < toRemove.length; r++) dst.removeAttribute(toRemove[r]);
  // Add / update attrs from src.
  for (var j = 0; j < src.attributes.length; j++) {
    var a = src.attributes[j];
    if (isAuthority(a.name)) continue;
    if (dst.getAttribute(a.name) !== a.value) dst.setAttribute(a.name, a.value);
  }
}

// __skyPatch: full-body replacement for sky-nav clicks, popstate,
// and the server's full-HTML fallback path. Routes through the
// node-preservation splicer so keystrokes never land on a destroyed
// DOM node.
function __skyPatch(t) {
  var root = document.getElementById("sky-root");
  if (!root) return;
  // Strip the full-document envelope when present (sky-nav fetches
  // return <!doctype><html>...</html>). The regex captures exactly
  // the rendered body, same as before.
  var m = t.match(/<div id="sky-root">([\s\S]*?)<\/div><script>/);
  if (m) t = m[1];
  var scrollX = window.scrollX, scrollY = window.scrollY;
  __skyReplaceHTMLPreservingFocus(root, t);
  window.scrollTo(scrollX, scrollY);
  __skyBindEvents(document);
  __skyRunEvals(root);
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

// ── I3: flush on unmount ─────────────────────────────────────
// Any pending debounce that hasn't fired by the time the user
// navigates or closes the tab would normally be discarded — the
// setTimeout is torn down with the page. These handlers flush
// synchronously so the final keystroke always reaches the server.
// See docs/skylive/input-authority-protocol.md §I3.

// __skyCollectPendingBatch — snapshot every pending-debounce entry
// into a batch array, bumping __skyClientSeq per entry so each gets
// its own order in the batch processed server-side. Clears the
// pending map as a side effect so the regular debounce callback
// can't double-fire after a beacon.
function __skyCollectPendingBatch() {
  var keys = Object.keys(__skyInputPending);
  if (keys.length === 0) return null;
  var batch = [];
  for (var i = 0; i < keys.length; i++) {
    var k = keys[i];
    clearTimeout(__skyInputTimers[k]);
    var p = __skyInputPending[k];
    delete __skyInputPending[k];
    __skyClientSeq++;
    batch.push({
      seq: __skyClientSeq,
      msg: p.msgName || "",
      args: p.args || [],
      handlerId: p.hid || ""
    });
  }
  return batch;
}

// __skyFlushPendingBeacon — POST pending debounces via sendBeacon so
// the request survives page unload. Single beacon carries the whole
// batch + the latest inputState snapshot so the server ingests the
// final DOM values before dispatching. Silent no-op when there's
// nothing pending or the browser lacks sendBeacon support.
function __skyFlushPendingBeacon() {
  if (!navigator || typeof navigator.sendBeacon !== "function") return;
  var batch = __skyCollectPendingBatch();
  var snapshot = __skyInputsSnapshot();
  if (!batch && !snapshot) return;
  var body = { sessionId: __skySid };
  if (batch)    body.batch = batch;
  if (snapshot) body.inputState = snapshot;
  try {
    var blob = new Blob([JSON.stringify(body)], {type: "application/json"});
    navigator.sendBeacon("/_sky/event", blob);
  } catch (_) {}
}

// __skyFlushPendingSync — synchronous variant for same-page
// transitions where sendBeacon is overkill. Calls __skySend for
// each pending entry; the fetch requests are fire-and-forget and
// the browser keeps them alive across same-origin navigation.
function __skyFlushPendingSync() {
  var batch = __skyCollectPendingBatch();
  if (!batch) return;
  for (var i = 0; i < batch.length; i++) {
    var b = batch[i];
    __skySend(b.msg, b.args, b.handlerId, {noLoader: true});
  }
}

// Capture-phase click listener inside sky-root: before a link click
// leaves the current page, drain any pending debounce so the final
// typed value reaches the server in the same origin as the
// outgoing navigation. Beacon path handles cross-page; sync path
// handles SPA-style internal routing.
document.addEventListener("click", function(ev) {
  var a = ev.target && ev.target.closest && ev.target.closest("a[href]");
  if (!a) return;
  var root = document.getElementById("sky-root");
  if (!root || !root.contains(a)) return;
  var href = a.getAttribute("href") || "";
  // External or cross-origin → beacon (browser will tear down the
  // page, fetch would be cancelled). Same-origin navigation inside
  // SPA-style routing → sync flush (fetch survives).
  var isExternal = /^(https?:)?\/\//.test(href) && a.host !== location.host;
  if (isExternal || href === "") {
    __skyFlushPendingBeacon();
  } else {
    __skyFlushPendingSync();
  }
}, true);

// Tab close / navigate away: sendBeacon is the only path that
// survives the teardown. Listen on both events because iOS Safari
// + bfcache fire pagehide instead of beforeunload.
window.addEventListener("beforeunload", __skyFlushPendingBeacon);
window.addEventListener("pagehide", __skyFlushPendingBeacon);

// ── Core send ────────────────────────────────────────────────
// Wire format (see docs/skylive/input-authority-protocol.md §Request):
//   {sessionId, seq, msg, args, handlerId, inputState?}
//   * seq is client-monotonic — server uses it to match responses to
//     the inputState snapshot that produced them.
//   * inputState carries the user's current DOM values for every
//     dirty input so the server's diff can align against reality
//     before emitting patches.
function __skySend(msgName, args, handlerId, opts) {
  opts = opts || {};
  if (!opts.noLoader) __skyLoaderStart();
  __skyClientSeq++;
  var mySeq = __skyClientSeq;
  // Stamp every currently-dirty input with this seq. The server's
  // ack (for a future response) will clear them back to parity.
  var dirtyIds = Object.keys(__skyInputs);
  for (var di = 0; di < dirtyIds.length; di++) {
    var de = __skyInputs[dirtyIds[di]];
    if (de.liveValue !== "" || de.pendingDebounceId !== null) {
      de.lastSentSeq = mySeq;
    }
  }
  var snapshot = __skyInputsSnapshot();
  var body = {
    sessionId: __skySid,
    seq: mySeq,
    msg: msgName || "",
    args: args || [],
    handlerId: handlerId || ""
  };
  if (snapshot) body.inputState = snapshot;
  __skyPostEvent(body);
}

// ── POST retry queue ─────────────────────────────────────────
// Wire-protocol POSTs are cheap (small JSON, idempotent on the
// server's seq-ordered state machine), so a transient network blip
// shouldn't lose the click. Failures push the body onto __skyEventQueue;
// retries fire on exponential backoff (500ms, 1s, 2s, … cap 16s);
// the SSE 'open' handler drains the queue eagerly when the server
// comes back. Cap at 50 entries — beyond that the user has been
// offline so long that replay isn't useful, drop oldest with a
// console warn so the page doesn't accumulate megabytes of state.
var __skyEventQueue = [];
var __skyRetryTimer = null;
var __skyRetryAttempts = 0;
// __skyRetryBaseMs / __skyRetryMaxMs / __skyRetryMaxAttempts /
// __skyEventQueueMax are templated at the top of this script from
// the SKY_LIVE_RETRY_* / SKY_LIVE_QUEUE_MAX env vars (see
// loadLiveBannerConfig).
function __skyPostEvent(body) {
  fetch("/_sky/event", {
    method: "POST",
    headers: {"Content-Type":"application/json"},
    body: JSON.stringify(body),
    credentials: "same-origin"
  }).then(function(r){
    if (!r.ok && r.status >= 500) {
      // Server is up but rejecting (502/503/504 from a deploying LB,
      // or 500 from a panic that survived the recover guard). Treat
      // as transient — same retry path as a network failure.
      throw new Error("server " + r.status);
    }
    var ct = r.headers.get("Content-Type") || "";
    if (ct.indexOf("application/json") >= 0) {
      return r.json().then(function(data) {
        __skyLoaderEnd();
        __skyOnPostSuccess();
        if (!data) return;
        __skyHandleResponse(data.seq, data.ackInputs, function() {
          if (data.patches) __skyApplyPatches(data.patches);
        });
      });
    }
    return r.text().then(function(t) {
      __skyLoaderEnd();
      __skyOnPostSuccess();
      var seqStr = r.headers.get("X-Sky-Seq");
      var seq = seqStr ? parseInt(seqStr, 10) : 0;
      var ackRaw = r.headers.get("X-Sky-Ack-Inputs");
      var ack = null;
      if (ackRaw) { try { ack = JSON.parse(ackRaw); } catch(_) {} }
      __skyHandleResponse(seq, ack, function() { __skyPatch(t); });
    });
  }).catch(function() {
    __skyLoaderEnd();
    __skyOnPostFailure(body);
  });
}
function __skyOnPostSuccess() {
  // A successful POST proves the server reachable — clear any
  // backoff state and drain queued events behind this one. If the
  // SSE was the trigger that drained the queue, this is a no-op.
  __skyRetryAttempts = 0;
  if (__skyRetryTimer !== null) {
    clearTimeout(__skyRetryTimer);
    __skyRetryTimer = null;
  }
  if (__skyStatus !== "connected") {
    __skySetStatus("connected", "");
  }
  __skyDrainQueue();
}
function __skyOnPostFailure(body) {
  // FIFO drop when the queue is at the cap — bail on the oldest
  // pending event rather than the new one, so the user's most
  // recent intent is preserved.
  if (__skyEventQueue.length >= __skyEventQueueMax) {
    var dropped = __skyEventQueue.shift();
    if (window.console && console.warn) {
      console.warn("[sky.live] event queue at cap; dropped oldest", dropped);
    }
  }
  __skyEventQueue.push(body);
  __skyShowReconnecting();
  __skyScheduleRetry();
}
function __skyShowReconnecting() {
  if (__skyStatus === "offline") return;
  if (__skyStatus === "connected") {
    __skySetStatus("reconnecting", "Reconnecting…");
  }
}
function __skyScheduleRetry() {
  if (__skyRetryTimer !== null) return;  // already pending
  if (__skyRetryAttempts >= __skyRetryMaxAttempts) {
    __skySetStatus("offline", "Connection lost — refresh to retry");
    return;
  }
  __skyRetryAttempts++;
  // 500, 1000, 2000, 4000, 8000, 16000, 16000, … (capped)
  var delay = Math.min(__skyRetryBaseMs * Math.pow(2, __skyRetryAttempts - 1), __skyRetryMaxMs);
  __skyRetryTimer = setTimeout(function() {
    __skyRetryTimer = null;
    __skyDrainQueue();
  }, delay);
}
function __skyDrainQueue() {
  if (__skyEventQueue.length === 0) return;
  // Send the head of the queue. If it succeeds, __skyOnPostSuccess
  // recurses into __skyDrainQueue to send the next one. If it
  // fails, the body re-enters the queue and the retry loop kicks
  // back in. Order is preserved (FIFO) — the server's seq matching
  // tolerates late deliveries via __skyHandleResponse.
  var head = __skyEventQueue.shift();
  __skyPostEvent(head);
}

// Apply a list of sky-id addressed patches with input authority (I1):
// value/checked/selected attrs on dirty inputs are dropped so the
// user's DOM wins; innerHTML patches route through
// __skyReplaceHTMLPreservingFocus which splices the live focused
// input (same DOM node, same .value, same IME/composition state)
// through the new HTML so it's never destroyed. Per-attr and
// textContent updates are fine as-is — they don't regenerate nodes.
function __skyApplyPatches(patches) {
  if (!patches || patches.length === 0) return;
  for (var i = 0; i < patches.length; i++) {
    var p = patches[i];
    var el = document.querySelector('[sky-id="' + p.id.replace(/"/g, '\\"') + '"]');
    if (!el) continue;
    if (p.text !== undefined && p.text !== null) {
      // textContent on a container that contains the focused input
      // would also wipe the input (replaces all children with one
      // text node). Guard the same way as innerHTML.
      if (__skyContainsFocusedInput(el)) {
        __skyReplaceHTMLPreservingFocus(el, __skyEscapeHTML(p.text));
      } else {
        el.textContent = p.text;
      }
    }
    if (p.html !== undefined && p.html !== null) {
      __skyReplaceHTMLPreservingFocus(el, p.html);
    }
    if (p.attrs) {
      var dirty = __skyIsDirty(el);
      var keys = Object.keys(p.attrs);
      for (var j = 0; j < keys.length; j++) {
        var k = keys[j], v = p.attrs[k];
        // Authority filter: the user is currently editing this
        // field, so the server's proposed value/checked/selected
        // would stomp in-flight keystrokes. Drop them and let the
        // next event round-trip settle the state.
        if (dirty && (k === "value" || k === "checked" || k === "selected")) {
          continue;
        }
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

function __skyContainsFocusedInput(el) {
  var a = document.activeElement;
  if (!a || a === document.body) return false;
  var tag = a.tagName;
  if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT") return false;
  return el === a || el.contains(a);
}

function __skyEscapeHTML(s) {
  var d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
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
        // Track live value against sky-id so the snapshot bundled in
        // the next __skySend reflects the user's actual DOM state,
        // and so Step 3's patch filter can recognise dirty inputs.
        var sid = target.getAttribute("sky-id");
        if (sid) {
          var e = __skyInputEntry(sid);
          e.liveValue = args && args.length > 0 ? String(args[0]) : "";
        }
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
  // Client-side size guard via fileMaxSize. Saves the round-trip when
  // the user picks a 100MB file: drop with a console.warn rather than
  // streaming the bytes server-side just to reject them. Server-side
  // validation should still happen — this is a UX nicety, not a
  // security boundary.
  var maxSize = parseInt(el.getAttribute("data-sky-ev-sky-file-max-size") || "0");
  if (maxSize > 0 && f.size > maxSize) {
    if (window.console && console.warn) {
      console.warn(
        "[sky.live] file " + f.name + " (" + f.size +
        " bytes) exceeds fileMaxSize " + maxSize + "; dispatch dropped"
      );
    }
    el.value = "";  // clear the input so the user can pick another
    return;
  }
  if (fileId) {
    var r = new FileReader();
    // __skySend's args param is List a on the wire (server expects
    // []json.RawMessage); a bare string would unmarshal-fail. Wrap
    // the data URL in a single-element array — the Sky-side Msg
    // constructor declared as 'String -> Msg' reads args[0].
    r.onload = function(e) { __skySend(fileId, [e.target.result]); };
    r.readAsDataURL(f);
  }
  if (imageId) {
    var maxW = parseInt(el.getAttribute("data-sky-ev-sky-file-max-width")  || "1200");
    var maxH = parseInt(el.getAttribute("data-sky-ev-sky-file-max-height") || "1200");
    __skyResizeImage(f, maxW, maxH, function(dataUrl) {
      // Same wire-format reason as the onFile branch — wrap in array.
      __skySend(imageId, [dataUrl]);
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
// ── Status banner (connection state) ─────────────────────────
// Single bottom-pinned element rendered by the runtime (NOT by the
// user's view) showing connection health. State machine:
//   "connected"     → invisible
//   "reconnecting"  → amber bar, "Reconnecting…" + attempt counter
//   "offline"       → red bar, "Connection lost — refresh to retry"
// State transitions land in commits 2 + 3; this commit just wires
// the DOM + setter so the rest of the JS can flip states without
// touching the HTML directly. Hidden via display:none until a real
// reconnect attempt fires (no flicker on initial page load).
var __skyStatus = "connected";          // current state
var __skyStatusEl = null;               // banner root, set on DOMContentLoaded
var __skyStatusMsgEl = null;            // text node child
var __skyStatusGraceTimer = null;       // 500ms anti-flicker timer
function __skySetStatus(state, msg) {
  __skyStatus = state;
  if (!__skyStatusEl) return;           // banner not yet injected
  // Strip the previous state class, add the current one.
  var classes = __skyStatusEl.className.split(" ").filter(function(c) {
    return c.indexOf("sky-status--") !== 0;
  });
  classes.push("sky-status--" + state);
  __skyStatusEl.className = classes.join(" ");
  if (__skyStatusMsgEl && msg !== undefined) {
    __skyStatusMsgEl.textContent = msg;
  }
}
function __skyInjectStatusBanner() {
  if (__skyStatusEl) return;            // idempotent
  if (!__skyBannerEnabled) return;      // SKY_LIVE_BANNER=off
  var el = document.createElement("div");
  el.id = "__sky-status";
  el.className = "sky-status sky-status--connected";
  el.setAttribute("role", "status");
  el.setAttribute("aria-live", "polite");
  // Inline styles — no global stylesheet leak. Max z-index puts the
  // banner above any user fixed-position element. Fixed position
  // bottom-center; transitions for fade in/out feel less jarring.
  el.style.cssText = [
    "position:fixed",
    "left:50%%",
    "bottom:16px",
    "transform:translateX(-50%%)",
    "padding:8px 16px",
    "border-radius:6px",
    "font:13px/1.4 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif",
    "color:#fff",
    "box-shadow:0 2px 8px rgba(0,0,0,0.25)",
    "z-index:2147483647",
    "pointer-events:none",            // never intercept clicks
    "transition:opacity 200ms",
    "opacity:1"
  ].join(";");
  // State-specific styles applied via inline style overrides on
  // each setStatus call would be cleaner, but overriding via class
  // on a <style> tag keeps the inline cssText readable. Append a
  // tiny <style> with the variant rules.
  var style = document.createElement("style");
  style.textContent = "" +
    "#__sky-status.sky-status--connected{display:none}" +
    "#__sky-status.sky-status--reconnecting{background:#b45309}" +
    "#__sky-status.sky-status--offline{background:#b91c1c}";
  document.head.appendChild(style);
  var msgEl = document.createElement("span");
  msgEl.className = "sky-status__msg";
  el.appendChild(msgEl);
  document.body.appendChild(el);
  __skyStatusEl = el;
  __skyStatusMsgEl = msgEl;
  // Replay current state in case it changed before DOM was ready.
  __skySetStatus(__skyStatus, "");
}

// Server-Sent Events: push updates from server (subscriptions, Cmd.perform results).
// Frame envelope since v0.9.3+: {seq, body, ackInputs?}. Falls back to
// treating e.data as a raw HTML body when JSON parsing fails, so a
// mixed-version rollout doesn't break the open-SSE connection.
var __skySSE = new EventSource("/_sky/sse");
__skySSE.addEventListener("patch", function(e) {
  var frame;
  try { frame = JSON.parse(e.data); } catch (_) {
    // Legacy frame (pre-v0.9.3 server) — raw HTML, no seq to gate on.
    return __skyPatch(e.data.replace(/\\n/g, "\n"));
  }
  if (frame && typeof frame === "object") {
    __skyHandleResponse(frame.seq, frame.ackInputs, function() {
      if (frame.body) __skyPatch(frame.body.replace(/\\n/g, "\n"));
    });
  }
});

// ── SSE → status state transitions ───────────────────────────
// Browser EventSource auto-reconnects with its own backoff; we just
// observe the open/error events to flip __skyStatus and surface a
// banner. 500ms grace timer avoids flicker on transient blips:
// a quick error+reopen burst (e.g. wifi handoff) shouldn't paint the
// banner. Only show "reconnecting" if disconnect persists past the
// grace window.
__skySSE.addEventListener("open", function() {
  if (__skyStatusGraceTimer !== null) {
    clearTimeout(__skyStatusGraceTimer);
    __skyStatusGraceTimer = null;
  }
  if (__skyStatus !== "connected") {
    __skySetStatus("connected", "");
  }
  // Server is reachable again — drain any POSTs that failed during
  // the outage. Successful drains will recurse via __skyOnPostSuccess.
  // Reset the backoff counter so a fresh failure starts at 500ms not
  // 16s.
  __skyRetryAttempts = 0;
  if (__skyRetryTimer !== null) {
    clearTimeout(__skyRetryTimer);
    __skyRetryTimer = null;
  }
  if (__skyEventQueue.length > 0) __skyDrainQueue();
});
__skySSE.addEventListener("error", function() {
  // Don't flip state if we're already showing a banner — repeated
  // error events fire during the browser's reconnect loop.
  if (__skyStatus !== "connected") return;
  if (__skyStatusGraceTimer !== null) return;
  __skyStatusGraceTimer = setTimeout(function() {
    __skyStatusGraceTimer = null;
    // Re-check the EventSource state in case it reopened during
    // the grace window — readyState OPEN(1) means we're back.
    if (__skySSE.readyState === 1) return;
    __skySetStatus("reconnecting", "Reconnecting…");
  }, 500);
});

// ── Init ─────────────────────────────────────────────────────
// Bind initial DOM event listeners + inject the status banner once
// the HTML is parsed. Banner needs document.body to exist, so it
// goes through the same gate as event binding.
function __skyInit() {
  __skyBindEvents(document);
  __skyInjectStatusBanner();
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", __skyInit);
} else {
  __skyInit();
}
`, sid, cfg.Enabled, cfg.BaseMs, cfg.MaxMs, cfg.MaxAttempts, cfg.QueueMax)
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
