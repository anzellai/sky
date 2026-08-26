package rt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

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

// eventPair is a rendered event binding (event name → Sky Msg value).
// Produced by the Sky-source Std.Html.Events module through the
// htmlAttrToString FFI path and consumed by renderVNode + the TUI
// renderer (tui_ui.go).
type eventPair struct {
	name string
	msg  any
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

// HtmlToVNode converts a Sky `Html` ADT value to a VNode.  An
// actual VNode is passed through unchanged (Std.Ui's `Raw` escape
// hatch, and any value already in VNode form).
func HtmlToVNode(node any) VNode {
	node = unwrapAny(node)
	if vn, ok := node.(VNode); ok {
		return vn
	}
	// Fast path: a legacy SkyADT Html value (the shape every Std.Html /
	// Std.Ui builder produces). Skips unwrapADTShape's interface
	// re-dispatch and, below, the reflect-boxing of typed child/attr
	// slices — the single largest render-path allocation site.
	if adt, ok := node.(SkyADT); ok {
		return htmlShapeToVNode(adt.SkyName, adt.Fields)
	}
	name, _, fields, ok := unwrapADTShape(node)
	if !ok {
		// Defensive: a non-Html value reached the converter — render
		// it as text rather than panicking.
		return vtext(fmt.Sprintf("%v", node))
	}
	return htmlShapeToVNode(name, fields)
}

// htmlShapeToVNode lowers an already-introspected Html ADT (its variant
// name + fields) into a VNode. Shared by HtmlToVNode's fast and reflect
// paths so both produce byte-identical output.
func htmlShapeToVNode(name string, fields []any) VNode {
	switch name {
	case "HText":
		if len(fields) > 0 {
			return vtext(AsString(fields[0]))
		}
		return vtext("")
	case "HRaw":
		if len(fields) > 0 {
			return VNode{Kind: "raw", Text: AsString(fields[0])}
		}
		return VNode{Kind: "raw"}
	case "HElement":
		if len(fields) < 3 {
			return vtext("")
		}
		// Attrs/Events are deliberately left nil here and created on
		// first write by applyHtmlAttr. Two maps were being allocated
		// for EVERY element whether or not it had any attribute or
		// event, and both are retained for the lifetime of the session
		// inside prevTree — 30% of the 336 kB a session holds
		// (`docs/perf/skylive-interaction-cost.md`, "The attribution").
		//
		// Every reader is nil-safe by construction: an index read, a
		// comma-ok read, `range`, `len` and `delete` all behave on a nil
		// map exactly as they do on an empty one. Text and raw nodes
		// have shipped with nil Attrs/Events through this same pipeline
		// since `vtext` was written, so the nil case is not a new one.
		// The only nil-unsafe operation is a map ASSIGN, and every
		// assign lives in applyHtmlAttr below.
		vn := VNode{
			Kind: "element",
			Tag:  AsString(fields[0]),
		}
		appendHtmlAttrs(&vn, fields[1])
		appendHtmlChildren(&vn, fields[2])
		return vn
	default:
		return vtext("")
	}
}

// appendHtmlAttrs folds an HElement's attribute field into a VNode.
//
// Std.Html / Std.Ui emit the attribute list as a typed `[]SkyADT`
// (`Std_Html_Attributes_Attribute` is a type alias for `rt.SkyADT`), which
// arrives here boxed in a single `any`. Iterating it directly — rather
// than through `asList` — avoids allocating a `[]any` copy AND avoids
// `reflect.Value.Interface` boxing every element (`reflect.unsafe_New`),
// which the render profile attributed ~22% of all objects to. The `[]any`
// and reflect branches preserve behaviour for the erased/mixed case.
func appendHtmlAttrs(vn *VNode, attrsField any) {
	switch attrs := attrsField.(type) {
	case []SkyADT:
		for i := range attrs {
			applyHtmlAttrADT(vn, attrs[i])
		}
	case []any:
		for _, a := range attrs {
			applyHtmlAttr(vn, a)
		}
	default:
		for _, a := range asList(attrsField) {
			applyHtmlAttr(vn, a)
		}
	}
}

// appendHtmlChildren lowers an HElement's child field into vn.Children.
// Mirrors appendHtmlAttrs: the typed `[]SkyADT` path recurses without
// boxing, and pre-sizes Children (each child yields exactly one VNode) so
// the slice does not grow through repeated reallocation.
func appendHtmlChildren(vn *VNode, kidsField any) {
	switch kids := kidsField.(type) {
	case []SkyADT:
		if len(kids) == 0 {
			return
		}
		vn.Children = make([]VNode, 0, len(kids))
		for i := range kids {
			vn.Children = append(vn.Children, htmlShapeToVNode(kids[i].SkyName, kids[i].Fields))
		}
	case []any:
		if len(kids) == 0 {
			return
		}
		vn.Children = make([]VNode, 0, len(kids))
		for _, c := range kids {
			vn.Children = append(vn.Children, HtmlToVNode(c))
		}
	default:
		for _, c := range asList(kidsField) {
			vn.Children = append(vn.Children, HtmlToVNode(c))
		}
	}
}

// setAttr writes one attribute, creating the map on first use.
// HtmlToVNode leaves Attrs nil; this is the only place it is filled.
func (vn *VNode) setAttr(k, v string) {
	if vn.Attrs == nil {
		vn.Attrs = make(map[string]string, 1)
	}
	vn.Attrs[k] = v
}

// setEvent writes one event binding, creating the map on first use.
func (vn *VNode) setEvent(name string, msg any) {
	if vn.Events == nil {
		vn.Events = make(map[string]any, 1)
	}
	vn.Events[name] = msg
}

// applyHtmlAttr folds one Sky `Attribute` ADT value into a VNode.
func applyHtmlAttr(vn *VNode, a any) {
	a = unwrapAny(a)
	name, _, fields, ok := unwrapADTShape(a)
	if !ok {
		return
	}
	applyHtmlAttrShape(vn, name, fields)
}

// applyHtmlAttrADT is applyHtmlAttr's fast path for a legacy SkyADT
// attribute — the shape every Std.Html / Std.Ui builder produces. It skips
// unwrapAny (a no-op for a plain SkyADT: that struct has no OkValue /
// JustValue field to unwrap) and unwrapADTShape's interface re-dispatch,
// reading .SkyName / .Fields directly. Behaviour is identical to
// applyHtmlAttr for SkyADT inputs.
func applyHtmlAttrADT(vn *VNode, adt SkyADT) {
	applyHtmlAttrShape(vn, adt.SkyName, adt.Fields)
}

// applyHtmlAttrShape folds one introspected attribute (variant name +
// fields) into a VNode. Shared by applyHtmlAttr and applyHtmlAttrADT.
func applyHtmlAttrShape(vn *VNode, name string, fields []any) {
	switch name {
	case "Attr":
		if len(fields) >= 2 {
			k := AsString(fields[0])
			// A URL-bearing attribute whose scheme executes script is
			// neutralised HERE, the one place every attribute enters a VNode —
			// so `Std.Ui`, `Std.Html` and `Std.Markdown` are all covered by one
			// guard, on the server-render path and the diff/patch path alike.
			// See `SafeAttrURL`.
			v := SafeAttrURL(k, AsString(fields[1]))
			// `class` and `style` are HTML's space- and
			// semicolon-separated multi-valued attributes — multiple
			// `class "foo bar"` + `class "baz"` calls on the same
			// element should produce `class="foo bar baz"` not
			// `class="baz"` (which would silently drop the earlier
			// values).  Same shape: `Border.shadow {…}` + `Border.glow`
			// each emit a `style` attr that need joining.  Other attrs
			// retain the last-wins semantics (Sky users writing two
			// `href` or two `value` would expect override, not
			// concatenation).
			if existing, ok := vn.Attrs[k]; ok && existing != "" {
				switch k {
				case "class":
					vn.setAttr(k, existing+" "+v)
					return
				case "style":
					sep := "; "
					if strings.HasSuffix(existing, ";") {
						sep = " "
					}
					vn.setAttr(k, existing+sep+v)
					return
				}
			}
			vn.setAttr(k, v)
		}
	case "BoolAttr":
		if len(fields) >= 2 && AsBool(fields[1]) {
			k := AsString(fields[0])
			vn.setAttr(k, k)
		}
	case "EventAttr":
		if len(fields) >= 1 {
			ev := unwrapAny(fields[0])
			if _, _, evFields, ok := unwrapADTShape(ev); ok && len(evFields) >= 2 {
				// OnMsg / OnString / OnBool: Fields[0] = event name,
				// Fields[1] = Msg value (OnMsg) or handler fn.
				vn.setEvent(AsString(evFields[0]), evFields[1])
			}
		}
	case "NoAttr":
		// no-op sentinel — skip
	}
}

// HtmlRender serialises a Sky `Html` ADT to an HTML string.
func HtmlRender(node any) string {
	return renderVNode(HtmlToVNode(node), map[string]any{})
}

// HtmlRenderWithHandlers serialises a Sky `Html` ADT to an HTML string
// AND returns the per-hid typed-Msg lookup table populated by the
// internal renderer. Caller-owned alternative to HtmlRender for paths
// that need to dispatch hid-keyed events (e.g. the inline Sky Console
// mount in console_app/mount.go).
//
// idPrefix is the stable namespace anchor for assignSkyIDs. Use "r"
// to match the host Sky.Live convention; the console plane MAY pick
// a different prefix ("console") so its sky-ids never collide with
// the host's when both surfaces run in the same page (the console
// scopes click capture to [data-sky-console] so this only matters
// for diagnostic clarity).
//
// The function ALSO runs the Std.Ui style-marker rewriters
// (applyStyleInjections — media-query / pseudo-class / transition /
// animation hoisting) so the emitted HTML matches what Sky.Live's
// commitRender path emits. Without this, dynamic styles wouldn't
// hydrate on the inline mount's first paint.
//
// Wire shape for the returned map:
//
//	"<sky-id>.<event>" → typed Msg (Sky-side Msg constructor value)
//
// matches the host's `data-sky-hid="<id>"` attribute the client JS
// reads. dispatchConsoleMsg's hid-keyed lookup consumes this map to
// resolve a Msg without re-deriving it from the wire payload.
func HtmlRenderWithHandlers(node any, idPrefix string) (string, map[string]any) {
	if idPrefix == "" {
		idPrefix = "r"
	}
	handlers := map[string]any{}
	vn := HtmlToVNode(node)
	assignSkyIDs(&vn, idPrefix)
	applyStyleInjections(&vn)
	body := renderVNode(vn, handlers)
	return body, handlers
}

func init() {
	RegisterPure("htmlRender", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		return HtmlRender(args[0])
	})
	RegisterPure("htmlEscapeText", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		return htmlEscapeText(AsString(args[0]))
	})
	RegisterPure("htmlEscapeAttr", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		return htmlEscapeAttr(AsString(args[0]))
	})
	RegisterPure("htmlAttrToString", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		a := unwrapAny(args[0])
		name, _, fields, ok := unwrapADTShape(a)
		if !ok {
			return ""
		}
		switch name {
		case "Attr":
			if len(fields) >= 2 {
				// `Html.attrToString` is a second, independent path from an
				// `Attribute` to markup, so it needs the same guard — a check
				// applied on only one of two renderers is not a check.
				k := AsString(fields[0])
				return k + "=\"" +
					htmlEscapeAttr(SafeAttrURL(k, AsString(fields[1]))) + "\""
			}
		case "BoolAttr":
			if len(fields) >= 2 && AsBool(fields[1]) {
				return AsString(fields[0])
			}
		}
		return ""
	})
}

// renderVNode serialises a VNode subtree to HTML and, as it goes,
// registers every event binding it emits into `handlers`.
//
// It is a thin wrapper over renderVNodeInto, which is where the work
// happens. The recursion threads ONE builder rather than returning a
// string per node. The previous shape gave every element its own
// strings.Builder, grew it through several doublings, produced a string,
// and had the parent copy those bytes into ITS builder — so a leaf's bytes
// were copied once per level of nesting above it, and each of the ~390
// elements of a reference page paid its own allocation series.
//
// Measured on the 389-element gate fixture (`live_alloc_gate_test.go`),
// Apple M1, go1.26.1: 2,632 -> 692 allocations and 380 kB -> 176 kB per
// render for the builder change, then 692 -> 212 and 176 kB -> 162 kB once
// the per-event-binding fmt.Sprintf below went with it. Output is
// byte-identical: the walk order, the escaping and the emission order are
// untouched, which is what `xtask repro` and `build-run --golden` pin.
func renderVNode(n VNode, handlers map[string]any) string {
	return renderVNodeSized(n, handlers, 0)
}

// renderVNodeSized is renderVNode with a capacity hint for the builder.
//
// A page body is tens of kilobytes and the builder starts at nothing, so it
// reaches that size through a dozen doublings, each of which allocates a
// buffer and copies everything written so far into it. On a 37.5 kB
// reference page that cost 162 kB of allocation to produce 37.5 kB of
// output — 4.3x the bytes, all of it copying.
//
// The hint is the length of the body this session rendered LAST time, which
// is the best available predictor: a view's size is stable across the
// interactions of a session, and being wrong only costs the growth that
// would have happened anyway (too small) or one oversized buffer that is
// immediately released (too large). A fixed constant was rejected for the
// second reason — it would make every small page allocate as if it were a
// large one.
//
// hint <= 0 means "no idea", which is what the subtree renders inside the
// diff pass use; those are small and have no session to ask.
func renderVNodeSized(n VNode, handlers map[string]any, hint int) string {
	var sb strings.Builder
	if hint > 0 {
		sb.Grow(hint)
	}
	renderVNodeInto(&sb, n, handlers)
	return sb.String()
}

// renderChildrenHTML serialises a node's children as the innerHTML of a
// subtree-replace patch.
//
// It passes a nil handler table on purpose. Every id inside this subtree
// was registered by the whole-tree render that this diff runs against, so
// there is nothing here to record; the three call sites used to say that
// by handing renderVNode a fresh `map[string]any{}` and dropping it on the
// floor, which allocated a map per patch and read as though the
// registrations were wanted.
func renderChildrenHTML(children []VNode) string {
	var sb strings.Builder
	for _, c := range children {
		renderVNodeInto(&sb, c, nil)
	}
	return sb.String()
}

func renderVNodeInto(sb *strings.Builder, n VNode, handlers map[string]any) {
	if n.Kind == "text" {
		sb.WriteString(html.EscapeString(n.Text))
		return
	}
	if n.Kind == "raw" {
		sb.WriteString(n.Text)
		return
	}
	// Html.doctype wraps children in a pseudo-element; render as
	// <!DOCTYPE html> followed by the children directly.
	if n.Tag == "!doctype-wrapper" {
		sb.WriteString("<!DOCTYPE html>")
		for _, c := range n.Children {
			renderVNodeInto(sb, c, handlers)
		}
		return
	}
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
	// Deterministic attribute order — Go map iteration is randomised,
	// so without sorting the same VNode emits attrs in a different
	// order across renders.  That doesn't affect the diff's correctness
	// (diffNodes walks new.Attrs and key-looks-up in old.Attrs, which
	// is order-independent) BUT it does mean:
	//   * Two identical states produce byte-different HTML strings
	//     — golden/snapshot tests can flake, log diffs are noisy.
	//   * Browsers parse innerHTML into DOM in source order; when a
	//     parent's subtree gets replaced via the focus-preserving
	//     splicer, deterministic attr order on the re-parsed nodes
	//     lets future attribute-level patches target stable property
	//     positions (modern browsers don't care, but tooling that
	//     inspects the serialised HTML does).
	//   * Server-side caching (ETag of rendered HTML, Sky.Doc HTML
	//     diffing in CI) collapses to a no-op when the rendered bytes
	//     are stable across runs.
	// Sort by key — alphabetical is fine; the only authority-controlled
	// attrs (value/checked/selected) are still routed via the diff's
	// alignment path, not via render order.
	attrKeys := make([]string, 0, len(n.Attrs))
	for k := range n.Attrs {
		attrKeys = append(attrKeys, k)
	}
	sort.Strings(attrKeys)
	for _, k := range attrKeys {
		if (isTextarea || n.Tag == "select") && k == "value" {
			continue
		}
		sb.WriteString(" ")
		sb.WriteString(k)
		sb.WriteString(`="`)
		sb.WriteString(html.EscapeString(n.Attrs[k]))
		sb.WriteString(`"`)
	}
	// Same determinism for event attributes — also a Go map, also
	// previously emitted in randomised order.
	evKeys := make([]string, 0, len(n.Events))
	for ev := range n.Events {
		evKeys = append(evKeys, ev)
	}
	sort.Strings(evKeys)
	for _, ev := range evKeys {
		msg := n.Events[ev]
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
		// A nil map is the caller saying "render the bytes, drop the
		// registrations" — what diffNodes wants when it re-serialises a
		// subtree for a replace patch, since the ids in that subtree were
		// already registered by the whole-tree render this diff follows.
		// It used to say that by passing a throwaway `map[string]any{}`
		// and discarding it, which allocated a map per replace patch and
		// read as though the registrations mattered.
		if handlers != nil {
			handlers[id] = msg
		}
		msgName := msgDisplayName(msg)
		// Event names starting with `sky-` are side-channel meta-events
		// (onImage, onFile) — not real DOM events that __skyBindOne
		// would addEventListener on. Render them as `data-sky-ev-<name>`
		// so the file/image driver can pick them up via the standard
		// HTML5 data-attribute convention. Plain DOM events (click,
		// input, change, …) keep the legacy `sky-<eventName>` naming
		// since __skyBindOne queries by that selector.
		//
		// Written out rather than composed through fmt.Sprintf: the format
		// string ran once per event binding on every render, and each run
		// allocated the result plus the []any of its arguments. The bytes
		// emitted are the same; only the route to the builder changed.
		sb.WriteString(" ")
		if strings.HasPrefix(ev, "sky-") {
			sb.WriteString("data-sky-ev-")
		} else {
			sb.WriteString("sky-")
		}
		sb.WriteString(ev)
		sb.WriteString(`="`)
		sb.WriteString(html.EscapeString(msgName))
		sb.WriteString(`" data-sky-hid="`)
		sb.WriteString(id)
		sb.WriteString(`"`)
	}
	if isVoidTag(n.Tag) {
		sb.WriteString(" />")
		return
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
			renderVNodeInto(sb, picked, handlers)
		} else {
			renderVNodeInto(sb, c, handlers)
		}
	}
	sb.WriteString("</")
	sb.WriteString(n.Tag)
	sb.WriteString(">")
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
//   - ADT struct values (e.g. Msg{Tag: 1, SkyName: "Increment"}) expose
//     their constructor name via the SkyName field the compiler emits.
//   - Function values are Msg constructors whose name is discoverable
//     via runtime.FuncForPC — we pull the last `_`-segment so
//     `main.Msg_UpdateEmail` → "UpdateEmail".
//   - Anything else falls back to "" so the client knows to treat it
//     as an opaque handler-id only.
func msgDisplayName(msg any) string {
	if msg == nil {
		return ""
	}
	// v0.17 sealed-iface ADT: variant structs have a SkyVariantName()
	// method instead of a SkyName field. Check the SkyVariant interface
	// FIRST so codegen-emitted variants resolve cleanly. Falls through
	// to legacy SkyADT.SkyName + reflect-based FieldByName for rt-side
	// builders and pre-v0.17 codegen.
	if sv, ok := msg.(SkyVariant); ok {
		return sv.SkyVariantName()
	}
	// Legacy SkyADT carries SkyName as a plain field, so a type assertion
	// reads it directly. Without this the value fell through to the
	// reflect.ValueOf + FieldByName("SkyName") path below — a by-name field
	// search, on a function the render loop calls once per event binding
	// and the diff calls twice more. Measured 46 ns -> 3 ns per call on an
	// Apple M1. The reflect path stays for struct shapes that are neither
	// (rt-side builders that embed SkyName in a bespoke struct).
	if adt, ok := msg.(SkyADT); ok {
		return adt.SkyName
	}
	rv := reflect.ValueOf(msg)
	if rv.Kind() == reflect.Struct {
		if f := rv.FieldByName("SkyName"); f.IsValid() && f.Kind() == reflect.String {
			return f.String()
		}
	}
	if rv.Kind() == reflect.Func {
		name := runtime.FuncForPC(rv.Pointer()).Name()
		// #532 — reflect.MakeFunc-wrapped closures report
		// "reflect.makeFuncStub" as their PC name. Naively trimming on
		// the last `_` returns "makeFuncStub" and routes it onto the
		// wire as the Msg name; the server's LookupAdtTag then fails
		// silently and `{"patches":[]}` comes back. Returning "" here
		// makes the dispatcher fall back to the per-binding-site
		// handlerId — the canonical robust path for reflect-MakeFunc
		// closures (typical when a Msg constructor is partial-applied
		// onto a form onSubmit). The compiler-side fix is tracked
		// separately; this is the runtime-side defence.
		if strings.HasPrefix(name, "reflect.") {
			return ""
		}
		// An anonymous closure (Go names these `pkg.Outer.funcN`) is an
		// eta-expanded handler (`onSubmit SendMessage` lowered to
		// `func(p){ return Msg_SendMessage(p) }`). Its name is NOT a Msg
		// name, so trimming it (`…SendMessage.func1` → "func1") would route a
		// bogus name onto the wire and LookupAdtTag would fail silently. Fall
		// back to the per-binding-site handlerId — same defence as the
		// reflect.MakeFunc case above (#532).
		if strings.Contains(name, ".func") {
			return ""
		}
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

// injectMediaQueryStyles walks the tree after assignSkyIDs and rewrites
// every element that carries a `data-sky-mq-q` + `data-sky-mq-rules`
// marker pair (set by `Std.Ui.mediaQuery` / `Ui.breakpoint`, issue
// #376) into a base wrapper with a sky-id-scoped `<style>` child:
//
//	<div sky-id="r.0.2#div" ...>
//	    <style data-sky-mq="r.0.2#div">
//	        @media (max-width: 767px) {
//	            [sky-id="r.0.2#div"] { padding: 8px; flex-direction: column; }
//	        }
//	    </style>
//	    <child ... />
//	</div>
//
// The marker attrs are stripped from the wire output (the runtime
// has fully consumed them); the `<style>` block is scoped per-
// element so multiple `Ui.breakpoint`s on the same page cannot
// cross-contaminate each other's selectors. The browser's CSS
// engine handles reactivity natively — instant, no JS round-trip,
// no re-render needed when the viewport crosses the breakpoint.
//
// Composition: nested `Ui.breakpoint` calls produce nested
// wrappers, each with its own scoped style block.
//
// Pre-condition: assignSkyIDs has already stamped n.SkyID on every
// element. Post-condition: marker attrs removed; style child
// prepended where present.
// mediaQuerySpec and its three siblings are package-level values, built
// once, NOT struct literals rebuilt inside the walk.
//
// They used to be constructed inside the inject* function -- which is also
// the function the walk recursed through, so every element in the tree
// re-allocated a `markerAttrs` slice and a `build` closure, on each of the
// four passes. That, not the children-slice rebuild, was the dominant
// per-element allocation in style injection: 7.0 allocations per element
// against the 0.02 the slice rebuild costs.
//
// None of the four `build` funcs captures anything, so hoisting them is a
// pure lifetime change. The walk is byte-for-byte the same walk.
var mediaQuerySpec = styleMarkerSpec{
	markerAttrs: []string{"data-sky-mq-q", "data-sky-mq-rules"},
	styleAttr:   "data-sky-mq",
	build: func(skyID string, attrs map[string]string) string {
		query := attrs["data-sky-mq-q"]
		rules := attrs["data-sky-mq-rules"]
		if query == "" || rules == "" {
			return ""
		}
		selector := `[sky-id="` + skyID + `"]`
		safeRules := strings.ReplaceAll(rules, "</style", "")
		safeRules = strings.ReplaceAll(safeRules, "</STYLE", "")
		safeQuery := strings.ReplaceAll(query, "</style", "")
		safeQuery = strings.ReplaceAll(safeQuery, "</STYLE", "")
		return "@media " + safeQuery + " { " + selector +
			" { " + safeRules + " } }"
	},
}

func injectMediaQueryStyles(n *VNode) {
	injectStyleMarker(n, mediaQuerySpec)
}

// injectPseudoClassStyles walks the tree after assignSkyIDs and
// rewrites every element that carries a `data-sky-pc-rules` marker
// (set by `Std.Ui.onPseudo` and its sub-module sugar
// `Background.hoverColor`, `Font.focusColor`, etc. — issue #377)
// into a base wrapper with a sky-id-scoped `<style>` child:
//
//	<button sky-id="r.0.2#button" ...>
//	    <style data-sky-pc="r.0.2#button">
//	        @media (hover: hover) {
//	            [sky-id="r.0.2#button"]:hover { background-color: …; }
//	        }
//	        [sky-id="r.0.2#button"]:focus-visible { border-color: …; }
//	    </style>
//	    <!-- original children -->
//	</button>
//
// Per-pseudo rules are emitted in deterministic order (h, f, v, a,
// d — see `pseudoClassTag` in Std.Ui.sky); `:hover` rules are
// auto-wrapped in `@media (hover: hover)` so they don't fire as
// sticky-hover on touch devices.
//
// The marker attr is stripped from the wire output (the runtime
// has fully consumed it). Composition with
// `injectMediaQueryStyles` is order-independent: nested
// `Ui.breakpoint` wrappers don't see this marker (it lives on the
// inner element, not the wrapper), and pseudo-rules attach to
// their element regardless of which breakpoint wraps it. Since
// pseudo-rules don't open their own `@media` block they nest
// naturally under the breakpoint's `@media` block via CSS
// inheritance.
//
// Pre-condition: assignSkyIDs has already stamped n.SkyID on every
// element. Post-condition: marker attr removed; style child
// prepended where present.
var pseudoClassSpec = styleMarkerSpec{
	markerAttrs: []string{"data-sky-pc-rules"},
	styleAttr:   "data-sky-pc",
	build: func(skyID string, attrs map[string]string) string {
		return buildPseudoClassStyleText(skyID, attrs["data-sky-pc-rules"])
	},
}

func injectPseudoClassStyles(n *VNode) {
	injectStyleMarker(n, pseudoClassSpec)
}

// styleMarkerSpec describes one style-injection pass. All four passes
// (media-query / pseudo-class / transition / animation) share the
// same shape: locate a marker attr on an element with a sky-id, build
// a CSS block scoped to that id, drop the marker, attach a <style>
// element carrying the CSS block.
//
// v0.15.57 #409 — the canonical "attach as first child" path silently
// drops the <style> when the element is a VOID HTML element (<input>,
// <img>, <br>, …) because renderVNode skips children for void tags.
// The shared injector hoists the <style> to a sibling slot in that
// case (handled by the parent's child-loop pass).
type styleMarkerSpec struct {
	// markerAttrs is the list of data-* attrs the pass consumes (all
	// stripped from the wire output after processing, even on
	// no-match — so an empty marker doesn't leak as inert data-*).
	markerAttrs []string
	// styleAttr is the data-* attr stamped on the emitted <style>
	// element (e.g. "data-sky-pc" / "data-sky-mq" / "data-sky-tr" /
	// "data-sky-anim"), keyed to the element's sky-id.
	styleAttr string
	// build builds the CSS body. Returns "" if there's nothing to
	// emit (the marker was empty / malformed).
	build func(skyID string, attrs map[string]string) string
}

// injectStyleMarker applies a single style-injection spec to a VNode
// + its descendants. Handles both the non-void case (attach style as
// first child) and the void case (hoist to sibling after).
func injectStyleMarker(n *VNode, spec styleMarkerSpec) {
	if n.Kind != "element" {
		return
	}
	if !isVoidTag(n.Tag) {
		// Non-void self: prepend style child if marker present.
		applyMarkerAsFirstChild(n, spec)
	}
	// Walk children, splicing sibling style blocks after any void
	// child that still carries a marker (because the self-handler
	// above bailed for void).
	n.Children = walkChildrenWithVoidSiblingHoist(n.Children, spec)
}

// applyMarkerAsFirstChild handles the canonical case: build the
// style body, prepend as first child, strip marker(s). Caller must
// already have decided n is non-void.
func applyMarkerAsFirstChild(n *VNode, spec styleMarkerSpec) {
	if n.SkyID == "" {
		// No id → no scope. Strip markers anyway so they don't leak.
		for _, ma := range spec.markerAttrs {
			delete(n.Attrs, ma)
		}
		return
	}
	hasAny := false
	for _, ma := range spec.markerAttrs {
		if v, ok := n.Attrs[ma]; ok && v != "" {
			hasAny = true
			break
		}
	}
	if !hasAny {
		// Strip empty markers regardless.
		for _, ma := range spec.markerAttrs {
			delete(n.Attrs, ma)
		}
		return
	}
	styleText := spec.build(n.SkyID, n.Attrs)
	for _, ma := range spec.markerAttrs {
		delete(n.Attrs, ma)
	}
	if styleText == "" {
		return
	}
	styleNode := VNode{
		Kind: "element",
		Tag:  "style",
		Attrs: map[string]string{
			spec.styleAttr: n.SkyID,
		},
		Children: []VNode{{Kind: "raw", Text: styleText}},
	}
	n.Children = append([]VNode{styleNode}, n.Children...)
}

// walkChildrenWithVoidSiblingHoist recurses into each child + splices
// a sibling <style> immediately after any VOID child whose marker
// survived the self-handler's bail. See #409.
// It rebuilds the slice ONLY when a hoist actually happens. The
// previous version allocated `make([]VNode, 0, len(children))` and
// re-copied every child for every element, on each of the four
// injection passes, whether or not the tree contained a single style
// marker — 4 full tree-copies per render, and 17% of what a session
// retains (`docs/perf/skylive-interaction-cost.md`, "The attribution").
// A hoist is rare: it needs a VOID child carrying a live marker.
//
// The recursive call mutates through the pointer either way, so when
// nothing is hoisted the input slice already holds exactly the values
// the old code copied out, and returning it is the same result.
func walkChildrenWithVoidSiblingHoist(children []VNode, spec styleMarkerSpec) []VNode {
	var out []VNode // nil until the first hoist forces a rebuild
	for i := range children {
		child := &children[i]
		injectStyleMarker(child, spec)
		// Capture the void-child's marker BEFORE we append (the recurse
		// call may have stripped non-void markers from deep descendants
		// but a void child's marker still sits on the child).
		var hoist *VNode
		if child.Kind == "element" && isVoidTag(child.Tag) && child.SkyID != "" {
			hasAny := false
			for _, ma := range spec.markerAttrs {
				if v, ok := child.Attrs[ma]; ok && v != "" {
					hasAny = true
					break
				}
			}
			if hasAny {
				styleText := spec.build(child.SkyID, child.Attrs)
				if styleText != "" {
					hoist = &VNode{
						Kind: "element",
						Tag:  "style",
						Attrs: map[string]string{
							spec.styleAttr: child.SkyID,
						},
						Children: []VNode{{Kind: "raw", Text: styleText}},
					}
				}
				for _, ma := range spec.markerAttrs {
					delete(child.Attrs, ma)
				}
			}
		}
		if hoist == nil {
			if out != nil {
				out = append(out, *child)
			}
			continue
		}
		if out == nil {
			// First hoist in this child list: materialise the prefix
			// (already recursed, so these are the same values the old
			// code would have copied) and switch to the rebuilt slice.
			out = make([]VNode, 0, len(children)+1)
			out = append(out, children[:i+1]...)
		} else {
			out = append(out, *child)
		}
		out = append(out, *hoist)
	}
	if out == nil {
		return children
	}
	return out
}

// buildPseudoClassStyleText parses the `data-sky-pc-rules` marker
// string and produces a CSS block scoped to the given sky-id.
//
// Marker grammar (mirror of `encodePseudoRules` in Std.Ui.sky):
//
//	rules    = entry ("||" entry)*
//	entry    = tag "|" css
//	tag      = "h" | "f" | "v" | "a" | "d"
//	css      = arbitrary CSS property string
//
// Unknown tags are skipped (forward-compat: a future Sky compiler
// can emit new pseudo-class tags without breaking older
// runtimes). `</style` sequences in the css portion are stripped
// defensively — they'd otherwise terminate the <style> element
// prematurely.
func buildPseudoClassStyleText(skyID, encoded string) string {
	if encoded == "" {
		return ""
	}
	selector := `[sky-id="` + skyID + `"]`
	var sb strings.Builder
	for _, entry := range strings.Split(encoded, "||") {
		sep := strings.IndexByte(entry, '|')
		if sep < 0 {
			continue
		}
		tag := entry[:sep]
		css := entry[sep+1:]
		if css == "" {
			continue
		}
		pseudo, hoverGated, knownTag := pseudoSelectorForTag(tag)
		if !knownTag {
			continue
		}
		safeCSS := strings.ReplaceAll(css, "</style", "")
		safeCSS = strings.ReplaceAll(safeCSS, "</STYLE", "")
		// Std.Ui sets an element's BASE styles as an inline `style=""`
		// attribute (specificity 1,0,0,0 — the maximum). A pseudo-class rule
		// selects via `[sky-id="…"]:hover`, which loses to inline every time —
		// so a `:hover` / `:active` colour would emit but never apply. Mark each
		// declaration `!important` so it overrides the inline base (the standard
		// elm-ui-style inline-first fix).
		safeCSS = markImportant(safeCSS)
		// One rule per pseudo. `:hover` wrapped in `@media (hover:
		// hover)` to suppress sticky-hover on touch devices.
		if hoverGated {
			sb.WriteString("@media (hover: hover) { ")
			sb.WriteString(selector)
			sb.WriteString(pseudo)
			sb.WriteString(" { ")
			sb.WriteString(safeCSS)
			sb.WriteString(" } } ")
		} else {
			sb.WriteString(selector)
			sb.WriteString(pseudo)
			sb.WriteString(" { ")
			sb.WriteString(safeCSS)
			sb.WriteString(" } ")
		}
	}
	return strings.TrimSpace(sb.String())
}

// markImportant appends `!important` to every declaration in a CSS property
// string, so a pseudo-class rule (`[sky-id]:hover { … }`) overrides the
// element's inline base `style=""` (which otherwise wins by specificity).
// Declarations are `;`-separated; rgba()/hsl() values contain no `;`, so a
// naive split is safe. Idempotent — a declaration already carrying `!important`
// is left as-is.
func markImportant(css string) string {
	var b strings.Builder
	for _, decl := range strings.Split(css, ";") {
		t := strings.TrimSpace(decl)
		if t == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(t)
		if !strings.Contains(t, "!important") {
			b.WriteString(" !important")
		}
		b.WriteByte(';')
	}
	return b.String()
}

// pseudoSelectorForTag maps a wire-format pseudo-class tag (single
// letter) to its CSS pseudo-class selector + whether `:hover`-style
// `@media (hover: hover)` gating applies. Keep in lock-step with
// `pseudoClassTag` / `pseudoClassSelector` in Std.Ui.sky.
func pseudoSelectorForTag(tag string) (selector string, hoverGated bool, known bool) {
	switch tag {
	case "h":
		return ":hover", true, true
	case "f":
		return ":focus", false, true
	case "v":
		return ":focus-visible", false, true
	case "a":
		return ":active", false, true
	case "d":
		return ":disabled", false, true
	}
	return "", false, false
}

// applyStyleInjections runs every Std.Ui style-marker rewriter on
// the rendered tree in a fixed order:
//  1. injectMediaQueryStyles — `@media`-scoped CSS (issue #376)
//  2. injectPseudoClassStyles — `:hover`/`:focus` etc. (issue #377)
//  3. injectTransitionStyles — CSS `transition` shorthand (issue #378)
//  4. injectAnimationStyles  — CSS `@keyframes` + `animation` shorthand (issue #378)
//
// Single funnel so future style-injection passes (container
// queries, …) add ONE call site here instead of hunting down every
// render hook. All passes are idempotent on already-processed
// elements (they strip their marker attrs on first run) so
// re-invoking is safe.
//
// Pre-condition: assignSkyIDs has already stamped n.SkyID.
// liveBaseCSS is the base reset the server splices into <head> (live.go) AND the
// Sky.Spa wasm client injects at boot (live_wasm.go) — one shared source, so the
// client renders with the SAME box-sizing / flex-fill root / form resets as the
// server. Shared (no build tag) precisely so both the //go:build !js server and
// the //go:build js client can reference it. The root rule targets BOTH mounts:
// `#sky-root` (Sky.Live / Sky.Webview) and `#app` (Sky.Spa) — otherwise a
// `Ui.height Ui.fill` root has no resolvable parent height and collapses to
// content height (issue #63).
const liveBaseCSS = `*,*::before,*::after{box-sizing:border-box}` +
	`html,body{margin:0;padding:0;min-height:100%}` +
	`body{min-height:100vh;display:flex;flex-direction:column;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;line-height:1.4}` +
	`#sky-root,#app{display:flex;flex-direction:column;flex:1 0 auto;min-height:0}` +
	`h1,h2,h3,h4,h5,h6,p,ul,ol,li,figure,blockquote,pre,dl,dd{margin:0;padding:0;font-weight:inherit;font-size:inherit}` +
	`button,input,select,textarea{font:inherit;color:inherit}` +
	`button{background:none;border:0;padding:0;cursor:pointer;text-align:inherit}` +
	`a{color:inherit;text-decoration:none}` +
	`img,video,canvas,svg{display:block;max-width:100%}`

func applyStyleInjections(n *VNode) {
	present := scanStyleMarkers(n)
	if present == 0 {
		return
	}
	for _, p := range styleMarkerPasses {
		if present&p.bit != 0 {
			p.run(n)
		}
	}
}

// The four passes each walked the WHOLE tree, unconditionally, on every
// render. A page using no `Ui.hover` and no `Ui.transition` — most pages,
// and every page for at least three of the four passes — paid four full
// traversals to find nothing and delete nothing.
//
// scanStyleMarkers replaces that with one traversal reporting which passes
// have work. It reads KEY PRESENCE, not value: a marker attr present with
// an EMPTY value still needs its pass to run, because stripping empty
// markers so they cannot leak into the wire output is part of what the
// pass does (`applyMarkerAsFirstChild` deletes them on the no-match path
// too). Skipping a pass whose key appears on no element is exactly
// equivalent, because every effect a pass has is keyed on that attr.
//
// The marker sets are disjoint from the `styleAttr` names the passes stamp
// on the <style> elements they emit (`data-sky-mq` vs `data-sky-mq-q` /
// `data-sky-mq-rules`), so no pass can see a marker another pass created
// and running one cannot invalidate the scan.
type styleMarkerPass struct {
	bit  int
	spec *styleMarkerSpec
	run  func(*VNode)
}

// The order here IS the documented pass order above; the scan does not
// reorder anything, it only drops passes with nothing to do.
var styleMarkerPasses = []styleMarkerPass{
	{markerMediaQuery, &mediaQuerySpec, injectMediaQueryStyles},
	{markerPseudoClass, &pseudoClassSpec, injectPseudoClassStyles},
	{markerTransition, &transitionSpec, injectTransitionStyles},
	{markerAnimation, &animationSpec, injectAnimationStyles},
}

const (
	markerMediaQuery = 1 << iota
	markerPseudoClass
	markerTransition
	markerAnimation
)

// styleMarkerBits is DERIVED from the specs rather than restating their
// marker names. A second hand-written copy of the attr list is exactly how
// a pass gets silently skipped later: someone adds a marker attr to a spec,
// the scan does not know it, and the pass stops running for the trees that
// need it — with no test failing, because the pass still works whenever
// some OTHER marker of the same spec is also present.
var styleMarkerBits = func() map[string]int {
	m := make(map[string]int, 8)
	for _, p := range styleMarkerPasses {
		for _, a := range p.spec.markerAttrs {
			m[a] |= p.bit
		}
	}
	return m
}()

func scanStyleMarkers(n *VNode) int {
	found := 0
	scanStyleMarkersInto(n, &found)
	return found
}

func scanStyleMarkersInto(n *VNode, found *int) {
	if n.Kind == "element" {
		for k := range n.Attrs {
			// Every marker starts with this prefix and almost no ordinary
			// attribute does, so one prefix test rejects `class`, `id`,
			// `href`, … before the map is touched at all.
			if !strings.HasPrefix(k, "data-sky-") {
				continue
			}
			*found |= styleMarkerBits[k]
		}
	}
	for i := range n.Children {
		scanStyleMarkersInto(&n.Children[i], found)
	}
}

// injectTransitionStyles walks the tree after assignSkyIDs and
// rewrites every element that carries a `data-sky-tr-rules` marker
// (set by `Transition.attribute` / `Ui.transitionRaw`, issue #378)
// into a base wrapper with a sky-id-scoped `<style>` child:
//
//	<button sky-id="r.0#button" ...>
//	    <style data-sky-tr="r.0#button">
//	        @media (prefers-reduced-motion: no-preference) {
//	            [sky-id="r.0#button"] {
//	                transition: background-color 200ms ease-out;
//	            }
//	        }
//	    </style>
//	    <!-- original children -->
//	</button>
//
// `data-sky-tr-respect="0"` opts OUT of the `prefers-reduced-motion`
// gate — the rule is emitted unwrapped. Default is "1" (respect).
//
// The marker attrs are stripped from the wire output (the runtime
// has fully consumed them). Composes with `injectMediaQueryStyles`
// + `injectPseudoClassStyles` naturally: the transition CSS lives
// on the BASE selector while pseudo-class rules target the same
// selector with `:hover` / `:focus-visible` suffixes — the browser
// animates the change between the base and pseudo state without
// further coordination.
//
// Pre-condition: assignSkyIDs has already stamped n.SkyID.
var transitionSpec = styleMarkerSpec{
	markerAttrs: []string{"data-sky-tr-rules", "data-sky-tr-respect"},
	styleAttr:   "data-sky-tr",
	build: func(skyID string, attrs map[string]string) string {
		rules := attrs["data-sky-tr-rules"]
		respectRaw := attrs["data-sky-tr-respect"]
		if rules == "" {
			return ""
		}
		respect := respectRaw != "0"
		safeRules := strings.ReplaceAll(rules, "</style", "")
		safeRules = strings.ReplaceAll(safeRules, "</STYLE", "")
		selector := `[sky-id="` + skyID + `"]`
		if respect {
			return "@media (prefers-reduced-motion: no-preference) { " +
				selector + " { transition: " + safeRules + "; } }"
		}
		return selector + " { transition: " + safeRules + "; }"
	},
}

func injectTransitionStyles(n *VNode) {
	injectStyleMarker(n, transitionSpec)
}

// injectAnimationStyles walks the tree after assignSkyIDs and
// rewrites every element that carries a `data-sky-anim-rules`
// marker (set by `Animation.attribute` / `Ui.animateRaw`, issue
// #378) into a base wrapper with a sky-id-scoped `<style>` child:
//
//	<div sky-id="r.0#div" ...>
//	    <style data-sky-anim="r.0#div">
//	        @keyframes fadeIn__r_0_div { 0% { ... } 100% { ... } }
//	        @media (prefers-reduced-motion: no-preference) {
//	            [sky-id="r.0#div"] {
//	                animation: fadeIn__r_0_div 300ms ease-out 0ms 1 forwards;
//	            }
//	        }
//	    </style>
//	    <!-- original children -->
//	</div>
//
// Wire format (mirror of `encodeAnimations` in Std.Ui.sky):
//
//	rules = entry ("@@" entry)*
//	entry = name "||" shorthandTail "||" keyframesBody "||" respect
//
// `respect` is "1" (default) / "0" (opt out of reduced-motion gate).
//
// The @keyframes name is auto-suffixed with a sky-id-derived
// disambiguator so two elements naming their animation `"fadeIn"`
// with DIFFERENT keyframes don't collide globally. The sky-id is
// already structurally unique within a page; we strip the
// non-CSS-ident chars to produce a safe @keyframes name suffix.
var animationSpec = styleMarkerSpec{
	markerAttrs: []string{"data-sky-anim-rules"},
	styleAttr:   "data-sky-anim",
	build: func(skyID string, attrs map[string]string) string {
		return buildAnimationStyleText(skyID, attrs["data-sky-anim-rules"])
	},
}

func injectAnimationStyles(n *VNode) {
	injectStyleMarker(n, animationSpec)
}

// skyIDToCSSIdent rewrites a sky-id (`r.0.2#div`) into a CSS-safe
// identifier suffix (`r_0_2_div`) for use in @keyframes names.
// Replaces `.` and `#` (the sky-id structural separators) with `_`;
// drops anything else outside [A-Za-z0-9_-] defensively.
func skyIDToCSSIdent(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-':
			sb.WriteByte(c)
		case c == '.' || c == '#':
			sb.WriteByte('_')
		default:
			// Drop unknown chars — keeps the result safe to splice
			// into @keyframes <name> and into a CSS animation
			// shorthand.
		}
	}
	return sb.String()
}

// buildAnimationStyleText parses the `data-sky-anim-rules` marker
// and produces a CSS block scoped to the given sky-id. Emits ONE
// @keyframes block per animation entry + ONE animation rule
// applying them all to the element (CSS `animation: a, b, c`
// shorthand). The reduced-motion gate wraps the animation rule
// (NOT the @keyframes — those are inert definitions).
//
// Per-entry `respect` flags are honoured: if ANY entry opts out,
// the entire animation rule is split into a gated portion + an
// always-on portion. Most elements have a single animation so this
// rare case is handled correctly without complicating the common
// path.
func buildAnimationStyleText(skyID, encoded string) string {
	if encoded == "" {
		return ""
	}
	ident := skyIDToCSSIdent(skyID)
	selector := `[sky-id="` + skyID + `"]`
	var keyframesPart strings.Builder
	var gatedAnimRefs []string
	var ungatedAnimRefs []string

	for _, entry := range strings.Split(encoded, "@@") {
		parts := strings.SplitN(entry, "||", 4)
		if len(parts) < 4 {
			continue
		}
		name := parts[0]
		tail := parts[1]
		body := parts[2]
		respectRaw := parts[3]
		if name == "" || body == "" {
			continue
		}
		// Defensive `</style>` strip.
		safeBody := strings.ReplaceAll(body, "</style", "")
		safeBody = strings.ReplaceAll(safeBody, "</STYLE", "")
		safeTail := strings.ReplaceAll(tail, "</style", "")
		safeTail = strings.ReplaceAll(safeTail, "</STYLE", "")
		// Strip any chars from the user-supplied name that would
		// break a CSS @keyframes ident. Keep letters/digits/_/-.
		safeName := sanitiseAnimationName(name)
		if safeName == "" {
			continue
		}
		effective := safeName + "__" + ident
		keyframesPart.WriteString("@keyframes ")
		keyframesPart.WriteString(effective)
		keyframesPart.WriteString(" { ")
		keyframesPart.WriteString(safeBody)
		keyframesPart.WriteString(" } ")
		ref := effective + " " + safeTail
		if respectRaw == "0" {
			ungatedAnimRefs = append(ungatedAnimRefs, ref)
		} else {
			gatedAnimRefs = append(gatedAnimRefs, ref)
		}
	}

	if keyframesPart.Len() == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(keyframesPart.String())
	if len(gatedAnimRefs) > 0 {
		sb.WriteString("@media (prefers-reduced-motion: no-preference) { ")
		sb.WriteString(selector)
		sb.WriteString(" { animation: ")
		sb.WriteString(strings.Join(gatedAnimRefs, ", "))
		sb.WriteString("; } } ")
	}
	if len(ungatedAnimRefs) > 0 {
		sb.WriteString(selector)
		sb.WriteString(" { animation: ")
		sb.WriteString(strings.Join(ungatedAnimRefs, ", "))
		sb.WriteString("; } ")
	}
	return strings.TrimSpace(sb.String())
}

// sanitiseAnimationName strips chars that would break a CSS
// @keyframes ident. CSS allows [a-zA-Z0-9_-]+ (Unicode escapes are
// supported in spec but rare; keep ASCII for simplicity); a leading
// digit is illegal so we prefix with an underscore in that case.
func sanitiseAnimationName(s string) string {
	if s == "" {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('_')
		}
	}
	out := sb.String()
	if out == "" {
		return ""
	}
	first := out[0]
	if first >= '0' && first <= '9' {
		return "_" + out
	}
	return out
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
	ID     string            `json:"id"` // target element's sky-id
	Text   *string           `json:"text,omitempty"`
	HTML   *string           `json:"html,omitempty"`
	Attrs  map[string]string `json:"attrs,omitempty"` // value "" => remove
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
		html := renderVNode(*new_, nil)
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
	// Events diff — VNode.Events stores DOM event handlers separately
	// from Attrs, but renderVNode emits them as `sky-<event>` /
	// `data-sky-ev-<event>` attributes plus a `data-sky-hid` companion.
	// Without diffing Events, an element that toggles handlers (a
	// canvas-wrap gaining `Events.onKeyDown` when an edit overlay
	// closes, a button losing its onClick when a permission changes)
	// produces no patch for those attributes — the previous bound
	// listeners stay attached but the runtime's per-event lookup via
	// `target.getAttribute("sky-<event>")` returns null, so no Msg is
	// dispatched and the user's keypress / click is silently dropped.
	//
	// Repro that surfaced this: sky-diagram's canvas-wrap conditionally
	// includes `Events.onKeyDown (keyDown model)` only when
	// `editingShapeId == Nothing`. After CommitText flips back to
	// Nothing, the HTTP diff used to emit only a `p.html` patch on the
	// canvas child div (overlay removed), never touching canvas-wrap's
	// own attrs. Result: every subsequent keypress on canvas-wrap was a
	// no-op. Pre-v0.15.13 the full-body SSE frame following Cmd
	// completions accidentally re-rendered #sky-root and restored the
	// attribute; v0.15.13's Tick suppression + v0.15.14's runPerform
	// suppression both peeled away that safety net and exposed the
	// genuine diff bug.
	for ev, newMsg := range new_.Events {
		attrName := "sky-" + ev
		if strings.HasPrefix(ev, "sky-") {
			attrName = "data-sky-ev-" + ev
		}
		newMsgName := msgDisplayName(newMsg)
		if oldMsg, ok := old.Events[ev]; !ok || msgDisplayName(oldMsg) != newMsgName {
			if attrChanges == nil {
				attrChanges = map[string]string{}
			}
			attrChanges[attrName] = newMsgName
			// data-sky-hid encodes the sky-id + event suffix the runtime
			// expects when routing the user gesture back to its handler.
			// Re-emit it on any event change so a stale hid (from a
			// previous render that bound a different handler) can't
			// outlive the new wiring.
			attrChanges["data-sky-hid"] = new_.SkyID + "." + ev
		}
	}
	for ev := range old.Events {
		if _, ok := new_.Events[ev]; !ok {
			attrName := "sky-" + ev
			if strings.HasPrefix(ev, "sky-") {
				attrName = "data-sky-ev-" + ev
			}
			if attrChanges == nil {
				attrChanges = map[string]string{}
			}
			attrChanges[attrName] = ""
			// If the element has lost ALL its events the data-sky-hid
			// companion is now stale; clear it. When other events
			// remain, the new_.Events loop above will have rewritten
			// data-sky-hid to one of them already (last-write wins,
			// matching renderVNode's HTML emission order over the
			// sorted event keys).
			if len(new_.Events) == 0 {
				attrChanges["data-sky-hid"] = ""
			}
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
			html := renderChildrenHTML(new_.Children)
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
				html := renderChildrenHTML(new_.Children)
				*out = append(*out, Patch{ID: old.SkyID, HTML: &html})
				return
			}
			continue
		}
		if oc.Tag != nc.Tag || oc.Kind != nc.Kind {
			// Tag mismatch: replace subtree at the parent.
			if old.SkyID != "" {
				html := renderChildrenHTML(new_.Children)
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

type cmdT struct {
	// kind values:
	//   "none"          — Cmd.none — no-op
	//   "batch"         — Cmd.batch — fan out a list of Cmds
	//   "perform"       — Cmd.perform task toMsg — spawn task in goroutine
	//   "publish"       — Cmd.publish topic payload (Cycle 3 P46/P48 —
	//                     echo-by-default: publisher's own subscription
	//                     receives the broadcast)
	//   "publishNoEcho" — Cmd.publishNoEcho topic payload (Cycle 4 NE,
	//                     issue #359 — broker skips delivery to
	//                     subscribers whose ownerSid matches the
	//                     publisher's sid)
	kind  string
	task  any
	toMsg any
	batch []any
	// Pub/sub fields (kind = "publish" or "publishNoEcho").
	topic   string
	payload any
}

// SkyCmd is the public type for Sky's Cmd msg type.
type SkyCmd = cmdT

// SkyCmd_T is the v0.17 C15 dual-alias for Sky's Cmd msg type.
// Transparent at runtime (same underlying cmdT), but lets the
// compiler emit `rt.SkyCmd_T[Msg]` at Sky-side typed call sites
// (`update : Msg -> Model -> (Model, Cmd Msg)`) instead of bare
// `rt.SkyCmd` — Sky source becomes more legible without changing
// any runtime code.  Matches the C13/C14 pattern for Std.Ui's
// Element/Attribute dual-alias.
type SkyCmd_T[T any] = cmdT

type subT struct {
	// kind values:
	//   "none"            — Sub.none — no subscription
	//   "every"           — Sub.every intervalMs toMsg — periodic tick
	//   "batch"           — Sub.batch — combine multiple Subs
	//   "subscribeTopic"  — Sub.subscribeTopic topic toMsg
	//                       (Cycle 3 P46 stub; P48 wires setupSubscriptions
	//                       to spawn the subscriber goroutine)
	//   "subscribeStream" — Http.Stream.chunks streamId toMsg
	//                       (Cycle 4 HS — Sub leaf reads streamHandle.ch
	//                       and dispatches ChunkEvent values to update)
	kind  string
	ms    int
	toMsg any
	batch []any
	// Pub/sub field (kind = "subscribeTopic"). Cycle 3 P46.
	topic string
	// Streaming-HTTP field (kind = "subscribeStream"). Cycle 4 HS.
	streamID int64
	// WebSocket fields (kind = "subscribeWebSocket"). v0.15.46.
	// wsKind selects which event class this subscription receives:
	// "message" | "open" | "close" | "error".
	socketID int64
	wsKind   string
}

// SkySub is the public type for Sky's Sub msg type.
type SkySub = subT

// SkySub_T is the v0.17 C15 dual-alias for Sky's Sub msg type.
// Same shape as SkyCmd_T — transparent at runtime, lets the
// compiler emit `rt.SkySub_T[Msg]` at typed call sites.
type SkySub_T[T any] = subT

func Cmd_none() SkyCmd { return cmdT{kind: "none"} }

func Cmd_batch(list any) SkyCmd { return cmdT{kind: "batch", batch: asList(list)} }

func Cmd_perform(task, to any) SkyCmd { return cmdT{kind: "perform", task: task, toMsg: to} }

func Sub_none() SkySub { return subT{kind: "none"} }

func Sub_every(ms any, to any) SkySub {
	return subT{kind: "every", ms: AsInt(ms), toMsg: to}
}

// Sub_batch combines a list of Sub values into one. Used by Sky.Tui /
// Sky.Cli when a model needs to subscribe to multiple sources at once
// (e.g. a stopwatch ticking every 100 ms AND a quit-signal watcher).
// Sky.Live's setupSubscriptions currently only honours a single Sub.every —
// calling Sub.batch from a Live program collapses to the first non-none
// entry. Lifting that is independent work (SSE diff loop needs to handle
// multiple ticker frames per session); the non-Live backends use
// tea_subs.go's subManager which iterates over the batch list.
func Sub_batch(list any) SkySub {
	return subT{kind: "batch", batch: asList(list)}
}

// Time.every is an alias of Sub.every in Sky code
func Time_every(ms any, to any) SkySub { return Sub_every(ms, to) }

// Cmd_publish builds a "publish" Cmd. Sky-side surface:
//
//	Std.Cmd.publish : String -> any -> Cmd msg
//
// Fire-and-forget; no result feedback to the publisher (per design
// doc §2.1). topic is the wire channel id (exact-match string;
// pattern subs out of scope per design doc §1.2 non-goal 4).
//
// Echo-by-default: the publisher's own subscription on the same
// topic receives the broadcast — matches Redis / NATS / MQTT
// semantics. Use Cmd_publishNoEcho to opt out (issue #359).
func Cmd_publish(topic, payload any) SkyCmd {
	return cmdT{
		kind:    "publish",
		topic:   fmt.Sprintf("%v", topic),
		payload: payload,
	}
}

// Cmd_publishNoEcho builds a "publishNoEcho" Cmd. Sky-side surface:
//
//	Std.Cmd.publishNoEcho : String -> any -> Cmd msg
//
// Cycle 4 NE / issue #359 — opt out of echo-by-default. The broker
// suppresses delivery to any subscriber whose ownerSid matches the
// publisher's sid; every OTHER subscriber receives the broadcast
// normally.
//
// Use this when the publisher updates its own model directly (in
// `update`) and wants the broker to skip the round-trip back to
// itself. In v0.16+ cross-process broker tiers (Redis / Cloud
// Pub/Sub / NATS) the saved hop becomes 10-100ms+ of latency.
func Cmd_publishNoEcho(topic, payload any) SkyCmd {
	return cmdT{
		kind:    "publishNoEcho",
		topic:   fmt.Sprintf("%v", topic),
		payload: payload,
	}
}

// Sub_subscribeTopic builds a "subscribeTopic" Sub. Sky-side surface:
//
//	Std.Sub.subscribeTopic : String -> (any -> msg) -> Sub msg
//
// The toMsg function is the user-supplied decoder; the subscriber
// goroutine (P48) calls it with each incoming SessionEvent.Payload
// to produce a Msg for `update`.
func Sub_subscribeTopic(topic, toMsg any) SkySub {
	return subT{
		kind:  "subscribeTopic",
		topic: fmt.Sprintf("%v", topic),
		toMsg: toMsg,
	}
}

// Sub_subscribeStream builds a "subscribeStream" Sub. Sky-side surface:
//
//	Http.Stream.chunks : StreamId -> (ChunkEvent -> msg) -> Sub msg
//
// The toMsg function receives a ChunkEvent ADT value (Chunk String /
// Done / Errored Error); the runtime constructs the ADT before
// invoking it. The Sky wrapper unwraps `StreamId Int` to the inner
// int before passing here.
//
// Cycle 4 HS / docs/skylive/http-streaming.md.
func Sub_subscribeStream(streamID, toMsg any) SkySub {
	return subT{
		kind:     "subscribeStream",
		streamID: asInt64(streamID),
		toMsg:    toMsg,
	}
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
	//
	// EXCEPTION (v0.17 #4 fix): Number→String IS coerced via
	// strconv.FormatFloat / strconv.FormatInt. Range/number inputs
	// (Input.slider, type=number) send their values as wire Float per
	// the documented wire-event shape; the matching stdlib Input.slider
	// declares `onChange : String -> msg` because it round-trips
	// through the DOM as text.  Stringifying Float→String here closes
	// the contract gap without forcing every range-using app to
	// declare a Float-typed Msg ctor.  Bool→String stays rejected
	// (the radio "[true] into String" case in the comment above) —
	// only numeric types coerce.
	switch {
	case paramT.Kind() == reflect.String && (srcV.Kind() == reflect.Float64 ||
		srcV.Kind() == reflect.Float32):
		f := srcV.Float()
		// Integer-valued floats print without trailing ".0" (matches
		// browser behaviour and Sky's ToString.fromInt convention) —
		// "55" not "55.0".
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	case paramT.Kind() == reflect.String && (srcV.Kind() == reflect.Int ||
		srcV.Kind() == reflect.Int8 || srcV.Kind() == reflect.Int16 ||
		srcV.Kind() == reflect.Int32 || srcV.Kind() == reflect.Int64):
		return strconv.FormatInt(srcV.Int(), 10)
	case paramT.Kind() == reflect.String && (srcV.Kind() == reflect.Uint ||
		srcV.Kind() == reflect.Uint8 || srcV.Kind() == reflect.Uint16 ||
		srcV.Kind() == reflect.Uint32 || srcV.Kind() == reflect.Uint64):
		return strconv.FormatUint(srcV.Uint(), 10)
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

// tupleFirst / tupleSecond extract V0 / V1 from a Sky-emitted 2-tuple.
//
// v0.13 codegen erases all tuples to `rt.SkyTuple2 = T2[any, any]` — see
// the design comment in `Sky.Build.Compile.solvedTypeToGo`'s TTuple
// arm. The TEA dispatch path (`update` returning `(Model, Cmd msg)`)
// is the hot caller. Fast-path the common case via direct type
// assertion, falling back to reflect for shape-erased values arriving
// from generic kernels (`AsTuple2`-style wideners).
func tupleFirst(v any) any {
	if t, ok := v.(SkyTuple2); ok {
		return t.V0
	}
	if t, ok := v.(SkyTuple3); ok {
		return t.V0
	}
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
	if t, ok := v.(SkyTuple2); ok {
		return t.V1
	}
	if t, ok := v.(SkyTuple3); ok {
		return t.V1
	}
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
	// Map-to-struct: Sky's untyped record rep is map[string]any, but a
	// typed function parameter (e.g. an empty-record Model) wants the
	// struct. Build it, pulling each field by name — the map key may be
	// the lowercase Sky field name or the exported Go name, so try both.
	// Extra keys are ignored and missing fields stay zero, so this also
	// covers the empty-record case (struct{}) — the reflective dispatch
	// previously panicked there ("Call using map[string]interface {}").
	if av.Kind() == reflect.Map && want.Kind() == reflect.Struct &&
		av.Type().Key().Kind() == reflect.String {
		dst := reflect.New(want).Elem()
		for i := 0; i < want.NumField(); i++ {
			df := dst.Field(i)
			if !df.CanSet() {
				continue
			}
			name := want.Field(i).Name
			mv := av.MapIndex(reflect.ValueOf(name))
			if !mv.IsValid() && name != "" {
				mv = av.MapIndex(reflect.ValueOf(strings.ToLower(name[:1]) + name[1:]))
			}
			if !mv.IsValid() {
				continue
			}
			sv := mv
			for sv.Kind() == reflect.Interface && !sv.IsNil() {
				sv = sv.Elem()
			}
			if sv.Type().AssignableTo(df.Type()) {
				df.Set(sv)
			} else if df.Kind() == reflect.Interface {
				df.Set(sv)
			} else {
				narrowed := coerceReflectArg(sv, df.Type())
				if narrowed.IsValid() && narrowed.Type().AssignableTo(df.Type()) {
					df.Set(narrowed)
				}
			}
		}
		return dst
	}
	// Map-to-map with a differing element (or key) type. Sky's Dict is
	// map[string]any, but a typed `Dict String String` parameter/field wants
	// map[string]string (and likewise map[string]Int → map[string]int64, etc.).
	// A plain assign/convert cannot bridge these — Go maps are invariant — so
	// rebuild element-wise, coercing each value (and key) recursively. Without
	// this a request record delivered to `init`/`withRequest` arrives with its
	// headers/params/cookies Dicts EMPTY (only the String fields survive).
	if av.Kind() == reflect.Map && want.Kind() == reflect.Map &&
		av.Type().Key().Kind() == reflect.String && want.Key().Kind() == reflect.String {
		dst := reflect.MakeMapWithSize(want, av.Len())
		for _, mk := range av.MapKeys() {
			ck := mk
			if !mk.Type().AssignableTo(want.Key()) {
				ck = coerceReflectArg(mk, want.Key())
			}
			ev := av.MapIndex(mk)
			for ev.Kind() == reflect.Interface && !ev.IsNil() {
				ev = ev.Elem()
			}
			cv := coerceReflectArg(ev, want.Elem())
			if ck.IsValid() && cv.IsValid() &&
				ck.Type().AssignableTo(want.Key()) && cv.Type().AssignableTo(want.Elem()) {
				dst.SetMapIndex(ck, cv)
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
	// Reflection-free fast paths for the boxed-Sky-closure convention: a
	// first-class Sky function value is emitted as `func(any) any` (see
	// lower::Ctx::widen). Type-assert + call directly so event dispatch,
	// task-completion `toMsg`, route-param fill, and sub mapping carry no
	// reflect.Value — TinyGo implements neither reflect.Value.Call nor
	// reflect.Type.NumIn, so this is also what lets sky_call run there.
	if g, ok := f.(func(any) any); ok {
		return g(arg)
	}
	if g, ok := f.(func() any); ok {
		return g()
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
	// v0.17 Phase 4 Stage 6 — typed-arm CONSUMPTION at sky_call2.
	//
	// Stage 5 shipped a pure probe (lookup + counters; always fell
	// through).  Stage 6 makes the fast-path observation actionable:
	// when 'a' is a registered SkyADT message AND the dispatch
	// table exists, we KNOW the Msg parameter is already a typed
	// SkyADT struct value — no map→struct narrowing, no struct→
	// struct field copy is needed at coerceReflectArg time.  The
	// model param ('b') still goes through coerceReflectArg in case
	// the call site passed an untyped map (e.g. session restore).
	//
	// Why this is correctness-safe (no rt.Coerce regression):
	//   * The eligibility predicate ('table != nil' for the SkyADT's
	//     ADT name) is checked structurally — any SkyADT whose
	//     SkyName is registered.  Pre-Stage-6 every such call went
	//     through coerceReflectArg(av, rv.Type().In(0)) which, for
	//     a SkyADT value vs an interface-typed In(0), already takes
	//     the AssignableTo fast-path (line 7811).  Skipping the
	//     coerceReflectArg call for 'a' when av is already a typed
	//     SkyADT therefore produces a byte-identical reflect.Value
	//     to what coerceReflectArg returned — zero behaviour change
	//     beyond bypassing the 4-branch check.
	//   * The model 'b' still routes through coerceReflectArg so
	//     map-restored sessions (Cmd.perform completion + sub-app
	//     model rehydration) keep their map→struct narrowing path.
	//
	// rt.Coerce drop expectation: Stage 6 is a runtime perf lever
	// (fewer reflect operations on the steady-state event loop) and
	// does NOT directly drop rt.Coerce wrap emission in codegen.
	// The user-facing rt.Coerce count is dominated by view-side
	// typed-record returns + record-field initialisation; Msg
	// dispatch contributes negligibly.  Stage 7+ (typed UPDATE arms
	// per case-arm) is the lever that would let us replace the
	// reflect.Call entirely on the steady-state event loop AND let
	// codegen drop wraps around Msg-routing call sites.  Stage 6
	// is the necessary CONSUMER scaffold for that future close.
	_, fastPathOk := tryFastPathMsgUpdate(a)
	rv := reflect.ValueOf(f)
	if rv.Kind() != reflect.Func {
		return f
	}
	if rv.Type().NumIn() == 2 {
		var av reflect.Value
		if fastPathOk {
			// Typed SkyADT — bypass coerceReflectArg's branch chain.
			// 'a' is already the concrete struct value, so its
			// reflect.Value is directly assignable to the function's
			// In(0) (which is rt.SkyADT or any).  Symmetric with the
			// AssignableTo / Interface-target fast paths inside
			// coerceReflectArg.
			av = reflect.ValueOf(a)
			if !av.IsValid() {
				av = reflect.Zero(rv.Type().In(0))
			} else if want := rv.Type().In(0); want.Kind() != reflect.Interface &&
				!av.Type().AssignableTo(want) {
				// Defensive fallback: assignability mismatch (e.g.
				// generic SkyADT_T[Msg] vs SkyADT) — route through
				// the full coerce path so we don't regress correctness.
				av = coerceReflectArg(av, want)
			}
		} else {
			av = reflect.ValueOf(a)
			if !av.IsValid() {
				av = reflect.Zero(rv.Type().In(0))
			}
			av = coerceReflectArg(av, rv.Type().In(0))
		}
		bv := reflect.ValueOf(b)
		if !bv.IsValid() {
			bv = reflect.Zero(rv.Type().In(1))
		}
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
