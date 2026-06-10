// live.go — Sky.Live runtime (session store, VDom, SSE, routing).
//
// Audit P3-4: every `fmt.Sprintf("%v", x)` in this file is bound
// to VNode rendering or error-message composition. None of them
// flow secret material, session IDs, cookie values, or auth
// tokens: the session-id path passes string directly to
// http.SetCookie (see Server_setCookie), and CSRF/rate-limit
// tokens use the constant-time compare helpers in rt.go. Callers
// at the Sky layer pass String values; the %v sites tolerate any
// stringifiable input for codegen-uniformity. All text / attribute
// values emitted into HTML route through html.EscapeString in
// renderVNode — never raw string interpolation. The justification
// therefore applies file-wide.
package rt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"sky-app/rt/telemetry"
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

// ─── Sky-source Std.Html → VNode converter (v0.13 Layer 3) ──────
//
// Std.Html / Std.Html.Attributes / Std.Html.Events are Sky-source
// stdlib modules: their builders produce a typed `Html` ADT (an
// rt.SkyADT) rather than calling Go kernels.  HtmlToVNode is the
// single FFI boundary that lowers that ADT into the runtime VNode
// the renderer + diff layer consume — those layers are unchanged.

// HtmlToVNode converts a Sky `Html` ADT value to a VNode.  An
// actual VNode is passed through unchanged (Std.Ui's `Raw` escape
// hatch, and any value already in VNode form).
func HtmlToVNode(node any) VNode {
	node = unwrapAny(node)
	if vn, ok := node.(VNode); ok {
		return vn
	}
	adt, ok := node.(SkyADT)
	if !ok {
		// Defensive: a non-Html value reached the converter — render
		// it as text rather than panicking.
		return vtext(fmt.Sprintf("%v", node))
	}
	switch adt.SkyName {
	case "HText":
		if len(adt.Fields) > 0 {
			return vtext(AsString(adt.Fields[0]))
		}
		return vtext("")
	case "HRaw":
		if len(adt.Fields) > 0 {
			return VNode{Kind: "raw", Text: AsString(adt.Fields[0])}
		}
		return VNode{Kind: "raw"}
	case "HElement":
		if len(adt.Fields) < 3 {
			return vtext("")
		}
		vn := VNode{
			Kind:   "element",
			Tag:    AsString(adt.Fields[0]),
			Attrs:  map[string]string{},
			Events: map[string]any{},
		}
		for _, a := range asList(adt.Fields[1]) {
			applyHtmlAttr(&vn, a)
		}
		for _, c := range asList(adt.Fields[2]) {
			vn.Children = append(vn.Children, HtmlToVNode(c))
		}
		return vn
	default:
		return vtext("")
	}
}

// applyHtmlAttr folds one Sky `Attribute` ADT value into a VNode.
func applyHtmlAttr(vn *VNode, a any) {
	a = unwrapAny(a)
	adt, ok := a.(SkyADT)
	if !ok {
		return
	}
	switch adt.SkyName {
	case "Attr":
		if len(adt.Fields) >= 2 {
			k := AsString(adt.Fields[0])
			v := AsString(adt.Fields[1])
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
					vn.Attrs[k] = existing + " " + v
					return
				case "style":
					sep := "; "
					if strings.HasSuffix(existing, ";") {
						sep = " "
					}
					vn.Attrs[k] = existing + sep + v
					return
				}
			}
			vn.Attrs[k] = v
		}
	case "BoolAttr":
		if len(adt.Fields) >= 2 && AsBool(adt.Fields[1]) {
			k := AsString(adt.Fields[0])
			vn.Attrs[k] = k
		}
	case "EventAttr":
		if len(adt.Fields) >= 1 {
			ev := unwrapAny(adt.Fields[0])
			if evADT, ok := ev.(SkyADT); ok && len(evADT.Fields) >= 2 {
				// OnMsg / OnString / OnBool: Fields[0] = event name,
				// Fields[1] = Msg value (OnMsg) or handler fn.
				vn.Events[AsString(evADT.Fields[0])] = evADT.Fields[1]
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
		adt, ok := a.(SkyADT)
		if !ok {
			return ""
		}
		switch adt.SkyName {
		case "Attr":
			if len(adt.Fields) >= 2 {
				return AsString(adt.Fields[0]) + "=\"" +
					htmlEscapeAttr(AsString(adt.Fields[1])) + "\""
			}
		case "BoolAttr":
			if len(adt.Fields) >= 2 && AsBool(adt.Fields[1]) {
				return AsString(adt.Fields[0])
			}
		}
		return ""
	})
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

// injectMediaQueryStyles walks the tree after assignSkyIDs and rewrites
// every element that carries a `data-sky-mq-q` + `data-sky-mq-rules`
// marker pair (set by `Std.Ui.mediaQuery` / `Ui.breakpoint`, issue
// #376) into a base wrapper with a sky-id-scoped `<style>` child:
//
//   <div sky-id="r.0.2#div" ...>
//       <style data-sky-mq="r.0.2#div">
//           @media (max-width: 767px) {
//               [sky-id="r.0.2#div"] { padding: 8px; flex-direction: column; }
//           }
//       </style>
//       <child ... />
//   </div>
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
func injectMediaQueryStyles(n *VNode) {
	injectStyleMarker(n, styleMarkerSpec{
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
		recurse: injectMediaQueryStyles,
	})
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
func injectPseudoClassStyles(n *VNode) {
	injectStyleMarker(n, styleMarkerSpec{
		markerAttrs: []string{"data-sky-pc-rules"},
		styleAttr:   "data-sky-pc",
		build: func(skyID string, attrs map[string]string) string {
			return buildPseudoClassStyleText(skyID, attrs["data-sky-pc-rules"])
		},
		recurse: injectPseudoClassStyles,
	})
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
	// recurse is the entry point used to recursively walk children
	// (passed in so each pass keeps its own identity for tracing).
	recurse func(*VNode)
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
func walkChildrenWithVoidSiblingHoist(children []VNode, spec styleMarkerSpec) []VNode {
	out := make([]VNode, 0, len(children))
	for i := range children {
		child := &children[i]
		spec.recurse(child)
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
		out = append(out, *child)
		if hoist != nil {
			out = append(out, *hoist)
		}
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
func applyStyleInjections(n *VNode) {
	injectMediaQueryStyles(n)
	injectPseudoClassStyles(n)
	injectTransitionStyles(n)
	injectAnimationStyles(n)
}

// injectTransitionStyles walks the tree after assignSkyIDs and
// rewrites every element that carries a `data-sky-tr-rules` marker
// (set by `Transition.attribute` / `Ui.transitionRaw`, issue #378)
// into a base wrapper with a sky-id-scoped `<style>` child:
//
//   <button sky-id="r.0#button" ...>
//       <style data-sky-tr="r.0#button">
//           @media (prefers-reduced-motion: no-preference) {
//               [sky-id="r.0#button"] {
//                   transition: background-color 200ms ease-out;
//               }
//           }
//       </style>
//       <!-- original children -->
//   </button>
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
func injectTransitionStyles(n *VNode) {
	injectStyleMarker(n, styleMarkerSpec{
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
		recurse: injectTransitionStyles,
	})
}

// injectAnimationStyles walks the tree after assignSkyIDs and
// rewrites every element that carries a `data-sky-anim-rules`
// marker (set by `Animation.attribute` / `Ui.animateRaw`, issue
// #378) into a base wrapper with a sky-id-scoped `<style>` child:
//
//   <div sky-id="r.0#div" ...>
//       <style data-sky-anim="r.0#div">
//           @keyframes fadeIn__r_0_div { 0% { ... } 100% { ... } }
//           @media (prefers-reduced-motion: no-preference) {
//               [sky-id="r.0#div"] {
//                   animation: fadeIn__r_0_div 300ms ease-out 0ms 1 forwards;
//               }
//           }
//       </style>
//       <!-- original children -->
//   </div>
//
// Wire format (mirror of `encodeAnimations` in Std.Ui.sky):
//
//   rules = entry ("@@" entry)*
//   entry = name "||" shorthandTail "||" keyframesBody "||" respect
//
// `respect` is "1" (default) / "0" (opt out of reduced-motion gate).
//
// The @keyframes name is auto-suffixed with a sky-id-derived
// disambiguator so two elements naming their animation `"fadeIn"`
// with DIFFERENT keyframes don't collide globally. The sky-id is
// already structurally unique within a page; we strip the
// non-CSS-ident chars to produce a safe @keyframes name suffix.
func injectAnimationStyles(n *VNode) {
	injectStyleMarker(n, styleMarkerSpec{
		markerAttrs: []string{"data-sky-anim-rules"},
		styleAttr:   "data-sky-anim",
		build: func(skyID string, attrs map[string]string) string {
			return buildAnimationStyleText(skyID, attrs["data-sky-anim-rules"])
		},
		recurse: injectAnimationStyles,
	})
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

func Cmd_none() SkyCmd                { return cmdT{kind: "none"} }
func Cmd_batch(list any) SkyCmd       { return cmdT{kind: "batch", batch: asList(list)} }
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

// ─── Pub/sub stubs (Cycle 3 P46) ───────────────────────────────────
//
// These kernels MINT the typed Cmd/Sub envelope but don't yet
// dispatch through the registry — P48 wires runCmd's "publish" arm
// + setupSubscriptions' "subscribeTopic" arm. P46's job is to land
// the runtime registry + interface seam; the Sky-side surface
// (Std.Cmd.publish / Std.Sub.subscribeTopic) lands in P48 alongside
// the dispatch wiring.
//
// Kept here (next to the existing Cmd_/Sub_ family) so the codegen
// kernel table — once P48 adds them — has the symbols pre-existing
// at the runtime layer. Calling these from hand-written Go code
// today builds a well-formed value; runCmd / setupSubscriptions
// currently no-op on the new kinds (no behaviour leak).

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
	// sid: the session id this session belongs to. Stored here so
	// the dispatch path can include it in observability logs
	// without having to thread sid through every helper signature.
	// Populated when the session is loaded/created via getOrInit.
	sid string
	// identity / identityValid — v0.16.5 #493 session-identity bridge.
	// At mint time dispatchRoot reads IdentityFromContext(r.Context())
	// and, if a gate populated it, stashes the result here.  Kernels
	// downstream (notably the hub's Hub_currentIdentity) read it back
	// via SessionIdentity(sess) and use the claims to filter queries.
	//
	// `identityValid` is a separate bool because the zero
	// ConsoleIdentity is ambiguous — "no gate ran" and "gate allowed
	// an anonymous identity with empty claims" look the same on the
	// wire. The bool disambiguates without forcing every Sky.Live app
	// to know about Identity types.
	//
	// Persistence: both fields are included in storableSession so
	// DB-backed session stores round-trip identity across restarts
	// and replicas (sqlite / postgres / redis / firestore).
	identity      ConsoleIdentity
	identityValid bool
	model         any
	handlers      map[string]any
	prevTree *VNode // Last rendered tree; used by the diff protocol.
	// View-body bookkeeping for the SSE no-op suppression contract
	// (Cycle 3 P39 / Gap C2 — split out from the historical single
	// `prevBody` field whose dual meaning had bitten v0.15.14).
	//
	// Two distinct invariants are tracked:
	//
	//   * lastComputedBody — every dispatch / renderView / initial-
	//     mount writes this with the just-rendered HTML. Mirrors the
	//     `prevTree` field. dispatch's contract is to ALWAYS update
	//     it (rolled back on a render panic so a partial render
	//     doesn't poison the next dispatch's diff baseline).
	//
	//   * lastShippedBody — only the SSE-producing call sites
	//     (dispatchBatched, runPerformBody, the Time.every tick
	//     subscription, handleInitial's HTTP-response push, the SSE
	//     reconnect-resync push) write this — and ONLY when they
	//     actually emit a frame to the client (sseCh enqueue or
	//     direct write to the SSE/HTTP response writer).
	//
	// Suppression checks ("a tick whose view byte-equals the last
	// thing the client saw is a wasted frame — drop it") compare
	// against `lastShippedBody`. Comparing against `lastComputedBody`
	// would be a tautology: dispatch wrote it post-render.
	//
	// Why the split matters: a future cleanup that made dispatch
	// skip the write (e.g. "only update on actual ship") would
	// silently break suppression of every byte-identical view —
	// because every check would see the SAME value before and
	// after. Splitting names the two invariants so the contract is
	// resilient to that refactor class.
	lastComputedBody string
	lastShippedBody  string
	// lastSeen — UnixNano timestamp of the most recent store touch
	// (Get / Set / decodeSession seed). Stored as atomic.Int64 so the
	// store-level RWMutex (memoryStore.mu et al.) can keep using RLock
	// in the read path without the touch-update racing with a sibling
	// Get on the SAME session. Read via lastSeenTime(), written via
	// touchLastSeen() / setLastSeenTime().
	//
	// Race history (v0.15.x hardening task #326): `memoryStore.Get`
	// holds s.mu.RLock and writes `sess.lastSeen = time.Now()` — two
	// concurrent /_sky/event requests for the same session both pass
	// the RLock-only gate and race on the struct field. Equivalent
	// pattern present in sqliteStore / postgresStore / redisStore Set
	// + decodeSession. atomic.Int64 fixes the lot uniformly.
	lastSeen atomic.Int64
	mu       sync.Mutex
	// SSE outbound channel: any writer goroutine may push a frame.
	// Frame contents are typed envelopes — `event` names the SSE event
	// type ("patch" for the legacy full-body shape; "patches" for the
	// Cycle 3 P50 structural-diff shape) and `data` is the JSON
	// payload the writer in handleSSE escapes + emits verbatim.
	//
	// Cycle 3 P50a / Gap C11: the channel previously carried `chan
	// string` and the writer always emitted `event: patch`. Switching
	// to a typed envelope lets the SSE producer choose patches-vs-body
	// per render without changing the channel/buffer plumbing.
	sseCh chan sseFrame
	// Cancel function for any active subscription ticker. Re-created
	// by setupSubscriptions on every dispatch (the old one is closed
	// first); see also the session-wide `done` field which signals
	// TERMINAL teardown (TTL eviction / Delete) regardless of how
	// many setupSubscriptions cycles have run.
	//
	// Bug #339: the close-then-reassign pair MUST be serialised so
	// concurrent setupSubscriptions callers (test fixtures that don't
	// hold sess.mu, or any future code path that fires from a
	// non-dispatch goroutine) can't double-close the channel. In
	// production all setupSubscriptions paths fan through
	// dispatch-under-sess.mu, but enforcing the invariant on the
	// field itself with a dedicated mutex makes the contract
	// crash-safe regardless of caller discipline. The mutex is also
	// taken by markDone before closing `done`, so a terminal
	// teardown can't race with an in-flight setupSubscriptions
	// midway through the (close, reassign) pair.
	cancelSub   chan struct{}
	cancelSubMu sync.Mutex

	// Terminal teardown signal — closed exactly once when the session
	// is evicted from its Store (Delete or TTL cleanup). Any goroutine
	// holding a reference to this session (Time.every Tick loop,
	// in-flight runPerformBody, future broadcast handlers) MUST select
	// on `done` so they exit promptly when the session is dead. Closing
	// is gated by `doneOnce` so concurrent Delete calls (or test
	// teardown that fires both Delete + Close on the store) are safe.
	//
	// Cycle 3 P36 / Gap C4: the previous implementation only signalled
	// the per-subscription `cancelSub`, which is replaced on every
	// dispatch — so a session deleted between dispatches kept its
	// Time.every goroutine alive forever, pushing to `sseCh` with no
	// reader. The leak persisted for the lifetime of the process.
	done     chan struct{}
	doneOnce sync.Once

	// Single session-wide monotonic counter for EVERY outgoing frame
	// (event reply OR SSE patch). Bumped under sess.mu so the value
	// reflects this session's true mutation order. The client keys its
	// stale-drop / cross-channel ordering off this number.
	//
	// Cycle 3 P47 (Phase 3g pub/sub prereq 2 — see
	// docs/skylive/pubsub-design.md §3.2): renamed from outSeq → localSeq
	// to disambiguate from the new app-wide `liveApp.globalSeq` that
	// tags broadcast-induced frames. Per-session dispatch + SSE replies
	// still bump localSeq; broadcast Publish bumps app.globalSeq once
	// before fan-out so every subscriber sees the same globalSeq for
	// one publish. Both seqs travel together in the SSE envelope; the
	// client guards on each independently. Single counter would have
	// forced every per-session dispatch to contend with broadcasts on
	// one atomic — the split keeps per-session dispatch lock-free.
	localSeq int64
	// Per input sky-id → largest req.InputState[id].Seq observed. Used
	// to populate response.ackInputs so the client can retire "dirty"
	// flags once the server has caught up. Stale ids (not present in
	// prevTree) are evicted on each ack build; see ackInputsForPrevTree.
	inputSeqs map[string]int64

	// activeSubs — pub/sub subscriptions currently bound to this
	// session, keyed by topic. Cycle 3 P46 / P48. Populated by
	// setupSubscriptions (diff-mode): on every dispatch, the runtime
	// builds the DESIRED topic set from the model, computes
	// (added, removed) vs activeSubs, opens NEW subscriptions for
	// `added`, cancels REMOVED, and leaves the intersection untouched
	// (the existing channel + goroutine carry over with no broadcast
	// loss). markDone walks activeSubs and calls each cancel so a
	// Deleted / TTL-evicted session releases its broker refcounts.
	//
	// Cycle 3 P48: protected by activeSubsMu — NOT by sess.mu. The
	// subscriber goroutine takes sess.mu around its dispatch call,
	// and dispatch internally calls setupSubscriptions; if activeSubs
	// were under sess.mu the inner setupSubscriptions would recurse
	// on it. A dedicated mutex decouples the registration map from
	// the view-state lock and keeps both deadlock-free.
	activeSubs   map[string]*subRegistration
	activeSubsMu sync.Mutex

	// streams — Sky.Core.Http.Stream open handles owned by this
	// session, keyed by stream id (Cycle 4 HS). HttpStream_open
	// registers each new handle here; HttpStream_close deletes;
	// markDone walks the map and closes every entry so a
	// session disconnect can't leak a body connection.
	//
	// Protected by streamsMu — dedicated mutex (NOT sess.mu) so
	// the subscriber loop's drainStreamSub can take sess.mu around
	// its dispatch call without recursing on the registry lock.
	// Mirrors the activeSubs / activeSubsMu split rationale.
	streams   map[int64]*streamHandle
	streamsMu sync.Mutex

	// activeStreamSubs — Http.Stream.chunks subscriptions currently
	// bound to this session, keyed by stream id (Cycle 4 HS). The
	// shape mirrors activeSubs (pub/sub) — diff-mode update on every
	// setupSubscriptions call: open new drain goroutines for added
	// stream ids, cancel removed ones, keep the intersection's
	// goroutines untouched (so no chunk falls in the gap when
	// `subscriptions` re-evaluates while a stream is mid-flight).
	//
	// Protected by activeStreamSubsMu (NOT sess.mu — same recursion
	// concern as activeSubsMu).
	activeStreamSubs   map[int64]*streamSubReg
	activeStreamSubsMu sync.Mutex

	// sockets — Sky.Core.WebSocket open handles owned by this session
	// (v0.15.46). WebSocket_connect registers; WebSocket_close
	// deletes; markDone walks the map and closes every entry so a
	// session disconnect can't leak an open WebSocket.
	//
	// Protected by socketsMu — dedicated mutex (mirrors streamsMu's
	// rationale).
	sockets   map[int64]*wsHandle
	socketsMu sync.Mutex

	// activeWsSubs — Sub.subscribeWebSocket entries currently bound
	// to this session, keyed by `<socketID>:<kind>` so onMessage +
	// onOpen + onClose + onError can coexist per socket.
	//
	// Protected by activeWsSubsMu (mirrors activeStreamSubsMu).
	activeWsSubs   map[string]*wsSubReg
	activeWsSubsMu sync.Mutex
}

// touchLastSeen — stamp the lastSeen counter with the current wall
// clock. Race-free under concurrent readers holding the store's
// RLock (the field is atomic.Int64, not a struct copy).
func (s *liveSession) touchLastSeen() {
	s.lastSeen.Store(time.Now().UnixNano())
}

// setLastSeenTime — write a specific time (used by decodeSession to
// restore a persisted timestamp, and by tests that need to backdate
// for TTL eviction). A zero `time.Time` lands as `Store(0)`, which
// lastSeenTime() reads back as a zero time.Time (`now.Sub(0)` is a
// huge positive duration → looks expired, matching the prior
// semantics of an uninitialised `time.Time{}` field).
func (s *liveSession) setLastSeenTime(t time.Time) {
	if t.IsZero() {
		s.lastSeen.Store(0)
		return
	}
	s.lastSeen.Store(t.UnixNano())
}

// lastSeenTime — read the lastSeen counter as a time.Time. A stored
// value of 0 returns the zero time (`time.Time{}`) so existing
// `time.Time.IsZero()` / `now.Sub(t)` callers keep working.
func (s *liveSession) lastSeenTime() time.Time {
	ns := s.lastSeen.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// markDone signals terminal teardown for this session. Idempotent;
// safe to call from multiple stores or from concurrent Delete calls.
// Goroutines that hold a reference to the session (Time.every Tick,
// runPerformBody, future broadcast handlers) MUST select on
// `sess.done` so they exit promptly once this fires.
//
// Sessions constructed by tests that never enter a Store keep
// `done == nil`; that's intentional — those sessions are never
// Deleted, so no signal is required. A `nil` channel in a select
// blocks forever, which is the desired semantics (Time.every keeps
// running until the test cancels its own context).
func (s *liveSession) markDone() {
	s.doneOnce.Do(func() {
		if s.done == nil {
			// Lazily provision the channel so a session that was
			// constructed without one (test fixtures, decoded-from-blob
			// sessions whose store-write path didn't initialise it) can
			// still receive a terminal signal once it lands in a Store.
			s.done = make(chan struct{})
		}
		close(s.done)
		// Cycle 3 P46 + P48: release every pub/sub subscription
		// bound to this session so its refcount on the broker
		// drops to zero. Each cancel is idempotent (sync.Once on
		// the broker side) so a setupSubscriptions cancel racing
		// with markDone is safe.
		//
		// Take activeSubsMu (NOT sess.mu — markDone can be invoked
		// from any goroutine that's evicting the session; sess.mu
		// is owned by the per-session dispatch path). Snapshot the
		// registration list under the lock, then call cancel funcs
		// AFTER releasing — keeps the critical section small in
		// case the broker's cancel briefly contends.
		s.activeSubsMu.Lock()
		regs := make([]*subRegistration, 0, len(s.activeSubs))
		for _, r := range s.activeSubs {
			if r != nil {
				regs = append(regs, r)
			}
		}
		s.activeSubs = nil
		s.activeSubsMu.Unlock()
		for _, reg := range regs {
			if reg.cancel != nil {
				reg.cancel()
			}
		}

		// Cycle 4 HS: close every open Http.Stream owned by this
		// session so the spool goroutines exit + the body
		// connections release. Mirrors the activeSubs sweep above.
		// closeAllStreams is idempotent + safe under markDone's
		// sync.Once gate.
		if n := closeAllStreams(s); n > 0 {
			fmt.Fprintf(os.Stderr,
				"[sky.stream] cleaned %d orphaned streams on session close (sid=%q)\n",
				n, s.sid)
		}
		// v0.15.46: same sweep for Sky.Core.WebSocket open sockets.
		// closeAllSockets is idempotent.
		if n := closeAllSockets(s); n > 0 {
			fmt.Fprintf(os.Stderr,
				"[sky.websocket] cleaned %d orphaned sockets on session close (sid=%q)\n",
				n, s.sid)
		}
		// Release ws subscription registrations (drain goroutines)
		// so they don't linger pushing to dead sessions.
		s.activeWsSubsMu.Lock()
		wsRegs := make([]*wsSubReg, 0, len(s.activeWsSubs))
		for _, r := range s.activeWsSubs {
			if r != nil {
				wsRegs = append(wsRegs, r)
			}
		}
		s.activeWsSubs = nil
		s.activeWsSubsMu.Unlock()
		for _, reg := range wsRegs {
			if reg.cancel != nil {
				reg.cancel()
			}
		}
	})
}

// nextLocalSeq advances and returns the session-wide outgoing seq.
// MUST be called with sess.mu held.
//
// Cycle 3 P47: renamed from nextOutSeq. See the `localSeq` field comment
// on liveSession for the global+local seq split rationale.
func (s *liveSession) nextLocalSeq() int64 {
	s.localSeq++
	return s.localSeq
}

// commitRender writes both `prevTree` and `lastComputedBody` as a single
// atomic step. Caller MUST hold sess.mu so the two fields are observed
// in lockstep by any concurrent reader holding the same mutex.
//
// Cycle 3 P40 / Gap C7: prior to extraction, this 2-field invariant was
// fanned out across FIVE call sites (handleInitial, dispatch's late
// write at the end of the success path, dispatch's handler-rebuild
// branch in handleEvent, the same rebuild in dispatchBatched, the SSE
// reconnect-resync in handleSSE, and renderView's guard-rejected path).
// Each site re-implemented "render → set prevTree → maybe set body"
// with subtle variations:
//
//   - dispatch wrote prevTree EARLY (pre-runCmd) and lastComputedBody
//     LATE (post-setupSubscriptions), with a panic-rollback snapshot
//     bracketing both writes.
//   - renderView wrote ONLY prevTree, leaving lastComputedBody stale
//     against the freshly-rendered tree — a silent contract break that
//     the audit (Cycle 3 C7) called out specifically.
//   - The handler-rebuild branches in handleEvent + dispatchBatched
//     discarded the rebuilt body entirely, leaving lastComputedBody
//     pointing at whatever the prior dispatch (potentially in a prior
//     process, decoded from sqlite/redis/postgres) had written. After
//     P40 these write the rebuilt body too, strengthening the invariant.
//
// `lastShippedBody` is INTENTIONALLY NOT touched here — it tracks "last
// body the client actually received" (post-P39 / Gap C2), an invariant
// owned by the SSE-producing call sites (dispatchBatched, runPerformBody,
// the Time.every tick goroutine, handleInitial's HTTP-response write,
// handleSSE's reconnect-resync push). Those sites still write
// `lastShippedBody` explicitly after commitRender so the "computed vs
// shipped" split stays explicit at every callsite that ships.
//
// vn MUST NOT be nil; passing nil would corrupt the diff baseline used
// by every subsequent dispatch.
func (s *liveSession) commitRender(vn *VNode, body string) {
	s.prevTree = vn
	s.lastComputedBody = body
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

// frameSnapshot captures every piece of session state encodeSSEFrame
// reads — bumped seq, body, ackInputs — so the JSON marshal can run
// AFTER sess.mu has been released. Cycle 3 P41 / Gap C6: prior to
// this, encodeSSEFrame was called under sess.mu at four call sites
// (dispatchBatched, runPerformBody, the Time.every Tick goroutine,
// the SSE reconnect-resync in handleSSE). Marshalling a ~14 KB body
// while holding the session mutex blocked every other dispatch on
// the same session for the duration of the encode (~200µs for 50 KB
// on M1). The snapshot makes that block strictly the bump+ack-build
// + map-copy cost; the marshal moves outside.
//
// Cycle 3 P47 (pub/sub prereq 2 — docs/skylive/pubsub-design.md §3.2):
// `seq` is the session-local monotonic counter (renamed from outSeq).
// `globalSeq` is the new app-wide counter populated by broadcast-
// derived frames so subscribers can detect dropped broadcasts via gap-
// check. Non-broadcast frames leave it 0; the JSON envelope omits it
// (omitempty) so old clients ignore it; new clients treat 0 as
// "no global ordering constraint" and never block on it.
type frameSnapshot struct {
	seq       int64
	globalSeq int64
	body      string
	ackInputs map[string]int64
}

// prepareFrameSnapshot captures (seq, body, ackInputs) under sess.mu so
// the JSON marshal can run after the caller releases the lock. Caller
// MUST already hold sess.mu — both nextLocalSeq and ackInputsForPrevTree
// mutate session state (localSeq counter; inputSeqs eviction of unmounted
// ids). After this returns, the caller is free to Unlock and then call
// encodeSSEFrameFromSnapshot on the returned value without re-locking;
// the snapshot is pure data with no aliasing back to sess.
//
// seq monotonicity across concurrent producers is preserved by the
// existing sess.mu serialisation of nextLocalSeq: two dispatch paths
// that race for the lock take the seq in the order they acquire the
// lock, regardless of how their post-unlock marshal interleaves. The
// SSE channel reader writes frames to the wire in arrival order; the
// client's __skyHandleResponse applies frames in seq order (already
// the contract — see live_adversarial_test.go for the lock-out test).
//
// Cycle 3 P47: prepareFrameSnapshot returns globalSeq=0 by default; the
// broadcast fan-out path uses prepareFrameSnapshotWithGlobalSeq to
// stamp the captured globalSeq so subscriber-derived frames carry it.
func (s *liveSession) prepareFrameSnapshot(body string) frameSnapshot {
	return frameSnapshot{
		seq:       s.nextLocalSeq(),
		body:      body,
		ackInputs: ackInputsForPrevTree(s),
	}
}

// prepareFrameSnapshotWithGlobalSeq is the broadcast variant of
// prepareFrameSnapshot — same contract for localSeq + body + ackInputs,
// plus stamps the supplied globalSeq from the publish event. The caller
// (broadcast-driven dispatch path, P48's job to wire up) captures the
// globalSeq from SessionEvent.GlobalSeq BEFORE acquiring sess.mu, then
// passes it through here so the SSE envelope shipped to the client
// carries both the per-session localSeq AND the app-wide globalSeq.
// MUST be called with sess.mu held (same as prepareFrameSnapshot).
//
// Why a separate function rather than an optional parameter: the
// snapshot is the cheap-to-call hot path (every SSE producer hits it),
// and a single zero-globalSeq sentinel-arm in the common case is
// strictly cheaper than a closure or option-struct allocation.
func (s *liveSession) prepareFrameSnapshotWithGlobalSeq(body string, globalSeq int64) frameSnapshot {
	return frameSnapshot{
		seq:       s.nextLocalSeq(),
		globalSeq: globalSeq,
		body:      body,
		ackInputs: ackInputsForPrevTree(s),
	}
}

// encodeSSEFrameFromSnapshot serialises a snapshot to the SSE wire
// envelope. Pure function — safe to call without holding sess.mu.
// The fallback branch uses snap.seq (not sess.localSeq) so a marshal
// failure post-unlock still names the frame's true seq.
//
// Cycle 3 P47: globalSeq rides as an OPTIONAL field on the envelope.
// Zero → omitted (legacy client + non-broadcast frames stay byte-
// identical to pre-P47). Non-zero → "globalSeq":N attached so the
// client can dedupe replayed broadcasts via __skyLastGlobalSeq.
func encodeSSEFrameFromSnapshot(snap frameSnapshot) string {
	frame := map[string]any{
		"seq":  snap.seq,
		"body": snap.body,
	}
	if snap.globalSeq > 0 {
		frame["globalSeq"] = snap.globalSeq
	}
	if snap.ackInputs != nil {
		frame["ackInputs"] = snap.ackInputs
	}
	b, err := json.Marshal(frame)
	if err != nil {
		// Marshalling a map of primitives can't fail in practice, but
		// fall back to a bare seq+body frame just in case so the
		// channel never carries a garbage string.
		return fmt.Sprintf(`{"seq":%d,"body":%q}`, snap.seq, snap.body)
	}
	return string(b)
}

// encodeSSEFrame is the legacy lock-held shape kept for callers that
// still need the synchronous "snapshot + marshal under one lock"
// behaviour. Internally it now defers to prepareFrameSnapshot +
// encodeSSEFrameFromSnapshot so the wire envelope is bit-identical to
// the unlocked path. New call sites SHOULD use the snapshot-then-
// marshal-outside-lock pattern instead (see runPerformBody / Tick /
// dispatchBatched for the canonical shape post-P41); this wrapper
// stays for handleSSE's reconnect-resync, which writes directly to
// the HTTP response writer rather than enqueueing onto sess.sseCh,
// and benefits from a single synchronous call shape.
//
// MUST be called with sess.mu held.
func encodeSSEFrame(sess *liveSession, body string) string {
	return encodeSSEFrameFromSnapshot(sess.prepareFrameSnapshot(body))
}

// sseFrame names the SSE event type alongside the serialised data.
// Cycle 3 P50a / Gap C11: SSE producers now choose between two
// transports per render:
//
//   - event="patch"  — legacy full-HTML-body envelope produced by
//     encodeSSEFrameFromSnapshot. Used when there is no previous
//     tree to diff against (first render, post-reconnect resync) and
//     as a fallback when the structural diff degenerates to a full
//     root-replace (patchesAreFullReplace).
//   - event="patches" — structural diff envelope produced by
//     encodePatchesEventFromSnapshot. Wire shape mirrors the HTTP
//     /_sky/event reply (writeEventJSON): {seq, ackInputs, patches}.
//     The client reuses __skyApplyPatches to apply.
//
// The channel writer in handleSSE emits `event: <event>\n` + the
// escaped data line; the SSE protocol on the client picks the event
// up via addEventListener for the matching name. Old clients with
// no `patches` listener silently ignore them — P50b adds the
// listener so they take effect on the wire.
type sseFrame struct {
	event string
	data  string
}

// patchesEventEnvelope mirrors writeEventJSON's body so the wire
// shape is identical between the HTTP /_sky/event reply path and the
// SSE event:patches push path. Reusing the shape means
// __skyApplyPatches consumes both routes uniformly.
//
// Cycle 3 P47: GlobalSeq rides as an optional field on the envelope —
// zero → omitted (legacy clients + non-broadcast frames stay byte-
// identical to pre-P47); non-zero → consumed by the client's
// __skyLastGlobalSeq guard so a replayed broadcast event drops at the
// boundary rather than mutating state twice.
type patchesEventEnvelope struct {
	Seq       int64            `json:"seq"`
	GlobalSeq int64            `json:"globalSeq,omitempty"`
	AckInputs map[string]int64 `json:"ackInputs,omitempty"`
	Patches   []Patch          `json:"patches"`
}

// encodePatchesEventFromSnapshot serialises a frameSnapshot + patches
// list into the JSON envelope shipped as `event: patches`. Pure
// function — safe to call without holding sess.mu.
//
// Patches MUST be the diff between the snapshot's logical prev tree
// and the just-computed new tree; the caller decides via diffTrees
// and patchesAreFullReplace whether this route or the legacy
// full-body route applies. The snapshot's `body` field is unused here
// (the structural diff has already captured the change); we keep the
// shared snapshot shape so seq + ackInputs allocate once per render.
//
// Empty-patches case: the slice is encoded as `"patches":[]` (not
// omitted), matching writeEventJSON's contract.
//
// Cycle 3 P47: snap.globalSeq travels in the envelope when non-zero
// (broadcast-derived frame); zero leaves the field omitted.
func encodePatchesEventFromSnapshot(snap frameSnapshot, patches []Patch) string {
	if patches == nil {
		patches = []Patch{}
	}
	env := patchesEventEnvelope{
		Seq:       snap.seq,
		GlobalSeq: snap.globalSeq,
		AckInputs: snap.ackInputs,
		Patches:   patches,
	}
	b, err := json.Marshal(env)
	if err != nil {
		// Marshal of a slice of typed structs + a map of primitives
		// can't fail in practice; degrade to the bare seq frame so
		// the channel never carries a truncated envelope.
		return fmt.Sprintf(`{"seq":%d,"patches":[]}`, snap.seq)
	}
	return string(b)
}

// chooseSSEFrame picks the SSE transport for a given render. Cycle 3
// P50a / Gap C11: when a structural diff exists and isn't a full
// root-replace, ship the small patch envelope (typically 200-1000 B);
// otherwise fall back to the legacy full-body envelope (~14 KB
// typical).
//
// The fall-back triggers in three cases:
//   - prevTreeBeforeDispatch == nil — first render after session
//     creation; the client has nothing to diff against client-side.
//   - patches == nil — diffTrees was not run (caller couldn't capture
//     prev/new trees safely; treat as "no diff available" rather
//     than "empty patch list"). nil here is a distinct signal from
//     len(patches)==0 (which would mean the diff ran and produced
//     no changes — but the caller already gated body != "" so that
//     path doesn't reach here).
//   - patchesAreFullReplace(patches) — the diff degenerated to a
//     single root-level innerHTML replace; the patches envelope is
//     no smaller than the full body, so the legacy event is the
//     simpler shape.
func chooseSSEFrame(snap frameSnapshot, prevTreeBeforeDispatch *VNode, patches []Patch) sseFrame {
	if prevTreeBeforeDispatch != nil && patches != nil && !patchesAreFullReplace(patches) {
		return sseFrame{event: "patches", data: encodePatchesEventFromSnapshot(snap, patches)}
	}
	return sseFrame{event: "patch", data: encodeSSEFrameFromSnapshot(snap)}
}

type liveApp struct {
	init          any // req -> (Model, Cmd Msg)
	update        any // Msg -> Model -> (Model, Cmd Msg)
	view          any // Model -> VNode
	subscriptions any // Model -> Sub Msg
	routes        []liveRoute
	notFound      any
	guard         any          // Maybe (Msg -> Model -> Result String ()) — nil = no guard
	// head : Model -> List (Html msg) — optional. When set, the
	// returned list is rendered to HTML and spliced into <head> on
	// the initial full-page response, after the baseline meta tags
	// and before <style>. Use for per-page <title>, SEO meta tags,
	// canonical URLs, Open Graph, Twitter Card, JSON-LD structured
	// data, theme-color, RSS, favicons, etc. nil → no extra head
	// content (default). Helpers live in Std.Live.Head.
	//
	// Only the initial GET (`handleInitial`) honours this — SSE
	// patches scope to <body>, so a head change does NOT re-emit
	// until a full reload. That matches the typical case (head is
	// derived from page identity, which changes via in-app
	// navigation that already triggers a sky-nav fetch +
	// full-body patch + history push).
	head          any
	// consoleAuth : Request -> Task Error (Maybe Identity) — optional.
	// When the embedded console mounts in `app`-mode (env
	// SKY_CONSOLE_AUTH=app), the framework calls this callback BEFORE
	// every /_sky/console request. `Nothing` → 403 + structured
	// `console.auth.denied` audit log. `Just identity` → the request
	// proceeds; the identity is attached to a __Host-sky_console
	// session cookie for the duration of consoleAuthSessionTTL.
	//
	// nil → no callback; mode falls back to token-mode (or off in
	// production when SKY_CONSOLE_AUTH is unset, per the production
	// gate in evaluateConsoleAuth). Same row-poly pattern as v0.15.58
	// `head` field — apps that omit `consoleAuth` build byte-identical.
	consoleAuth   any
	// v0.16.7 #418 — onNavigate : Page -> msg — optional callback.
	// When set, the framework dispatches the resulting Msg through
	// `update` AFTER every URL-driven `applyRoute` call (initial
	// mount, sky-nav click, popstate Back/Forward).  The Msg lets
	// the app react to a route change uniformly — typically to fire
	// a Cmd that fetches data for the new page — without having to
	// duplicate the logic between `init` (first-load) and an
	// explicit `Navigate` Msg arm (client-driven).
	//
	// nil → no Msg dispatched after route updates (the pre-v0.16.7
	// behaviour).  Same row-poly extension pattern as `head` /
	// `consoleAuth` — apps that omit `onNavigate` build
	// byte-identical.
	onNavigate    any
	api           []apiRoute   // REST-style custom handlers alongside Live pages
	staticDir     string       // Serves files from this directory under /static/…
	staticURL     string       // URL mount prefix (default "/static")
	store         SessionStore // sessionID -> *liveSession (memory, sqlite, or postgres)
	sessionTTL    time.Duration // session cookie MaxAge — kept in lock-step with the store TTL
	locker        *sessionLocker
	msgTags       map[string]int // SkyName → Tag cache for direct-send events
	msgTagsMu     sync.Mutex
	bannerCfg     liveBannerConfig // resolved env-vars + cfg.status overrides
	// basePath: URL prefix this app is mounted under when running as
	// a sub-app (e.g. "/_sky/console" when reverse-proxied behind a
	// parent Sky.Live runtime). Empty for root-mount (the common
	// case). Read from SKY_LIVE_BASE_PATH at startup. Surfaced to
	// the browser via a <meta name="sky-base"> tag in the page wrap
	// so the inlined JS prefixes its fetch/EventSource URLs
	// correctly — without this, a sub-app's wire calls would hit
	// the PARENT mux instead of the sub-app's.
	basePath string
	// cookieName: the session cookie name. Defaults to "sky_sid" for
	// root-mounted apps. Sub-apps mounted via MountLiveSubAppInProcess
	// MUST use a distinct name (e.g. "sky_console_sid") so the
	// parent's session cookie and the sub-app's session cookie don't
	// collide on the same browser origin. v0.16.1 PR10.
	cookieName string
	// skyIDPrefix: the prefix prepended to every assignSkyIDs walk.
	// Defaults to "r" for root-mounted apps. Sub-apps use a distinct
	// prefix (e.g. "sky-console") so logs / diffs / handler lookups
	// stay unambiguous when both parent + sub-app render into the
	// same browser tab. v0.16.1 PR10.
	skyIDPrefix string
	// topics — pub/sub registry (Cycle 3 P46). Same pointer the
	// app.store.Broker() returns; cached here so subscribe / publish
	// call sites don't have to indirect through the store on every
	// hot-path call. v0.15.x: in-process *topicRegistry. v0.16+
	// cross-process backends (Redis Pub/Sub, Cloud Pub/Sub, NATS,
	// Postgres LISTEN/NOTIFY — see docs/skylive/pubsub-design.md
	// §11.2.5) implement the same Broker interface.
	topics Broker

	// globalSeq — app-wide monotonic counter (Cycle 3 P47 / pub/sub
	// prereq 2; see docs/skylive/pubsub-design.md §3.2). Bumped ONCE
	// per Publish, BEFORE fan-out, so every subscriber sees the SAME
	// globalSeq value for one logical publish. The captured value
	// rides on SessionEvent.GlobalSeq, the subscriber goroutine in
	// P48 will thread it through prepareFrameSnapshotWithGlobalSeq,
	// and the SSE envelope's `globalSeq` field carries it to the
	// client. Per-session dispatch (the non-broadcast common case)
	// leaves globalSeq at zero — `localSeq` alone suffices for the
	// stale-drop guard, and the JSON envelope omits the zero field
	// (byte-identical to pre-P47 frames). Atomic so a flood of
	// concurrent Publish calls across goroutines remains lock-free.
	//
	// Why a SEPARATE counter from per-session localSeq:
	// docs/skylive/pubsub-design.md §3.2 — a single counter would
	// force every per-session dispatch to contend with broadcasts on
	// one atomic; the split keeps per-session dispatch lock-free
	// (no cross-session contention) and pays the atomic cost only on
	// broadcast.
	//
	// v0.16+ cross-process pub/sub will prepend a `ProcessId` field
	// to (origin, globalSeq) tuples; the v0.15.x design does NOT
	// preclude that — SessionEvent.GlobalSeq is already process-
	// scoped (the cross-process layer prepends its own id at the
	// backbone). §5.4 of the design doc.
	globalSeq atomic.Int64
}

// nextGlobalSeq advances the app-wide broadcast counter and returns
// the new value. Lock-free (atomic.Int64.Add). Used by the broadcast
// fan-out path (P48 wires this end-to-end) — Publish bumps globalSeq
// ONCE, then stamps the same value into every subscriber's
// SessionEvent so all subscribers observe one publish as one
// globalSeq.
//
// Cycle 3 P47 / pub/sub prereq 2 — docs/skylive/pubsub-design.md §3.2.
func (a *liveApp) nextGlobalSeq() int64 {
	return a.globalSeq.Add(1)
}

// skyIDPrefixOrDefault returns the per-app sky-id namespace prefix,
// falling back to "r" for app instances that pre-date the v0.16.1
// PR10 field. Centralising the default here keeps the historic
// behaviour for any *liveApp constructed outside liveAppRun (e.g.
// test fixtures that field-init the struct directly).
func (a *liveApp) skyIDPrefixOrDefault() string {
	if a == nil || a.skyIDPrefix == "" {
		return "r"
	}
	return a.skyIDPrefix
}

// cookieNameOrDefault returns the per-app session cookie name,
// falling back to "sky_sid" for app instances that pre-date the
// v0.16.1 PR10 field. Used by sessionIDNamed-aware call sites that
// otherwise would have hard-coded the legacy name.
func (a *liveApp) cookieNameOrDefault() string {
	if a == nil || a.cookieName == "" {
		return "sky_sid"
	}
	return a.cookieName
}

// Publish is the app-level fan-out entry point that all broadcast
// call sites use. It performs the two locked-in invariants of the
// pub/sub seq split (Cycle 3 P47 / docs/skylive/pubsub-design.md §3.2):
//
//  1. Bump `app.globalSeq` ONCE per publish, BEFORE fan-out.
//  2. Stamp the captured globalSeq into the outgoing event so EVERY
//     subscriber sees the SAME globalSeq for one logical publish.
//
// Returns the number of subscribers the event reached (passed through
// from topicRegistry.Publish — see Broker.Publish doc for the
// drop-vs-deliver contract).
//
// Why the helper rather than open-coding the bump at every call site:
// the "one bump per publish" contract is load-bearing for the client-
// side __skyLastGlobalSeq dedupe — if two call sites raced and the
// stamp happened AFTER fan-out, two subscribers could see different
// globalSeq for the same publish. Routing every Publish through this
// helper makes the invariant a function-boundary guarantee.
//
// P48 will wire this in via `Std.Cmd.publish` → runtime; for now the
// helper exists so the seq-split tests can drive a publish end-to-end
// without P48's Sky-side surface.
func (a *liveApp) Publish(topic string, event SessionEvent) int {
	event.GlobalSeq = a.nextGlobalSeq()
	return a.topics.Publish(topic, event)
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
//
//	Request -> Task String Response
//
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
	// `api` routes are raw developer HTTP endpoints — OAuth
	// callbacks, webhooks, REST, server-to-server callbacks. They
	// carry their own auth (Bearer / HMAC) and are NOT framework-
	// cookie-authenticated, so the double-submit CSRF guard (a
	// browser-form-forgery defence for the cookie-authed TEA event
	// path) does not apply. Without this, a server-to-server POST
	// to an api route — e.g. a build-job status callback — is
	// 403'd for lacking a CSRF token it could never have.
	WithoutCsrf(pattern)
	return apiRoute{method: method, pattern: pattern, handler: handler}
}

// dispatchRoot routes a request to:
//  1. a matching apiRoute (REST handler), OR
//  2. handleInitial (Live page render).
//
// Framework namespace guard: /_sky/* is reserved for the Sky runtime
// (event POST, SSE, console, metrics, healthz, readyz, buildinfo, etc).
// Specific /_sky/* endpoints are registered EXACT-match on the mux and
// never reach dispatchRoot. Anything that DOES reach here under /_sky/*
// is an unmounted framework path — we must return a plain 404 rather
// than fall through to the user's notFound page (which would leak the
// app's UI for typoed/probed framework URLs like /_sky/conslole).
//
// v0.16.1 PR10-F: when this *liveApp is itself a sub-app mounted under
// `/_sky/*` (e.g. the inline console at `/_sky/console`), the guard
// must NOT 404 on requests whose path matches the sub-app's own
// basePath — that prefix IS the sub-app's home. We only reject paths
// that fall under /_sky/ AND OUTSIDE the sub-app's basePath; or for
// root-mounted apps (basePath == "") any /_sky/ path.
func (app *liveApp) dispatchRoot(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_sky/") {
		if app.basePath == "" || !pathInBasePath(r.URL.Path, app.basePath) {
			http.NotFound(w, r)
			return
		}
	}
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
	// v0.16.1 PR10-F — sub-apps see urlPath that still includes the
	// basePath prefix (because the parent's mux dispatches the full
	// path through). For route matching we compare against the
	// "logical" path INSIDE the sub-app, which is whatever sits after
	// basePath. So /_sky/console/about routes against /about inside
	// the sub-app; /_sky/console (bare) and /_sky/console/ both route
	// against /.
	urlPath = trimBasePathPrefix(urlPath, app.basePath)
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
	model, _ = applyRouteWithParams(app, model, urlPath)
	return model
}

// v0.16.7 #417 — applyRouteWithParams is the params-returning variant
// used by dispatchRoot to extend the init `req` map with a Sky-shaped
// `params : Dict String String` keyed by the route pattern's `:name`
// segments.  The legacy `applyRoute` thin-wrapper keeps the pre-v0.16.7
// call-sites byte-identical for tests + sub-app re-entry paths.
//
// Returns the new model + the param Dict (Sky_Dict) that matched the
// route, or `nil` when no route matched.
func applyRouteWithParams(app *liveApp, model any, urlPath string) (any, any) {
	urlPath = trimBasePathPrefix(urlPath, app.basePath)
	for _, rt := range app.routes {
		if params, ok := matchRoute(rt.path, urlPath); ok {
			page := fillRoutePage(rt.page, params)
			model := RecordUpdate(model, map[string]any{"Page": page})
			return model, buildRouteParamsDict(rt.path, params)
		}
	}
	if app.notFound != nil {
		return RecordUpdate(model, map[string]any{"Page": app.notFound}),
			buildRouteParamsDict("", nil)
	}
	return model, buildRouteParamsDict("", nil)
}

// buildRouteParamsDict pairs each `:name` segment in the pattern with
// the captured value at the same position.  Returns an empty Dict when
// the pattern has no `:name` segments (notFound routing, exact-match
// routes).  The Dict is the runtime representation of `Dict String
// String` — same shape as `init`'s pre-existing `req.query` field.
func buildRouteParamsDict(pattern string, values []string) any {
	d := Dict_empty()
	if pattern == "" || len(values) == 0 {
		return d
	}
	names := []string{}
	for _, seg := range splitPath(pattern) {
		if strings.HasPrefix(seg, ":") {
			names = append(names, strings.TrimPrefix(seg, ":"))
		}
	}
	for i := range names {
		if i >= len(values) {
			break
		}
		d = Dict_insert(names[i], values[i], d)
	}
	return d
}

// v0.16.8 #423 — headersToDict folds an http.Header (case-canonicalised
// by Go's net/http stack already) into a Sky `Dict String String`.  We
// take the first value for each key — Sky-side iteration over multi-
// valued headers is exotic enough that callers who need it can hit
// `Live.api` or look at raw request shape from Sky.Http.Server.
// Empty Header → empty Dict.
func headersToDict(h http.Header) any {
	d := Dict_empty()
	for k, vs := range h {
		if len(vs) == 0 {
			continue
		}
		d = Dict_insert(k, vs[0], d)
	}
	return d
}

// v0.16.8 #423 — cookiesToDict folds a request's parsed cookies into a
// Sky `Dict String String`.  Duplicate names take the last value to
// match Go's net/http.Request.Cookie() lookup order.
func cookiesToDict(cs []*http.Cookie) any {
	d := Dict_empty()
	for _, c := range cs {
		if c == nil {
			continue
		}
		d = Dict_insert(c.Name, c.Value, d)
	}
	return d
}

// v0.16.7 #418 — dispatchOnNavigate fires the optional `onNavigate :
// Page -> msg` callback after every URL-driven `applyRoute` call.
// Returns the new model + any cmd the resulting Msg's update produces;
// caller batches that cmd with whatever else it has in hand.  When
// `app.onNavigate` is nil (the default), this is a no-op pass-through.
func dispatchOnNavigate(app *liveApp, model any) (any, any) {
	if app.onNavigate == nil || app.update == nil {
		return model, nil
	}
	page := Field(model, "Page")
	if page == nil {
		return model, nil
	}
	msg := sky_call(app.onNavigate, page)
	if msg == nil {
		return model, nil
	}
	// Sky.Live invokes update as a 2-arg function (Msg -> Model ->
	// (Model, Cmd)).  sky_call2 mirrors the normal Msg dispatch
	// site at line ~4322 — sky_call(sky_call(...)) panics here
	// because the typed-codegen path packs both params into a
	// single reflect.Call.
	res := sky_call2(app.update, msg, model)
	return tupleFirst(res), tupleSecond(res)
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
		head:          Field(cfg, "Head"),
		consoleAuth:   Field(cfg, "ConsoleAuth"),
		onNavigate:    Field(cfg, "OnNavigate"),
		locker:        newSessionLocker(),
		msgTags:       make(map[string]int),
		bannerCfg:     resolveBannerStrings(loadLiveBannerConfig(), cfg),
		basePath:      normaliseBasePath(skyGetenv("LIVE_BASE_PATH")),
		cookieName:    "sky_sid",
		skyIDPrefix:   "r",
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
	} else if v := skyGetenv("LIVE_STATIC_DIR"); v != "" {
		// <PREFIX>_LIVE_STATIC_DIR is the documented name (matches
		// the <PREFIX>_LIVE_* env var convention). <PREFIX>_STATIC_DIR
		// is kept as a backward-compat alias so existing deployments
		// don't break — read it only when the canonical name is
		// unset. Both honour the configured env-prefix.
		app.staticDir = v
	} else if v := skyGetenv("STATIC_DIR"); v != "" {
		app.staticDir = v
	}
	app.staticURL = "/static"
	if su := Field(cfg, "StaticUrl"); su != nil {
		if s := fmt.Sprintf("%v", su); s != "" {
			app.staticURL = s
		}
	}
	// Session store selection. Config fields `store` and `storePath`
	// override the defaults; env vars <PREFIX>_LIVE_STORE /
	// <PREFIX>_LIVE_STORE_PATH take precedence over config; final
	// fallback is memory.
	storeKind := stringField(cfg, "Store")
	storePath := stringField(cfg, "StorePath")
	// TTL resolution order:  env > sky.toml > default (30m).
	// Two value shapes accepted at BOTH layers, per CLAUDE.md
	// docs ("30m" default form):
	//
	//   1. Go-duration string — "30m", "24h", "1h30m", "45s"
	//      (anything time.ParseDuration handles, the documented
	//      shape).
	//   2. Bare integer — interpreted as SECONDS for backward-
	//      compatibility with the original env-only path.
	//
	// Empty / unparseable values fall through to the next layer;
	// the final fallback is 30m.  The previous implementation only
	// read the env var AND only accepted bare-integer seconds, so
	// `SKY_LIVE_TTL=24h` and any `ttl = "24h"` in sky.toml were
	// both silently ignored.
	ttl := parseTTL(skyGetenv("LIVE_TTL"), stringField(cfg, "Ttl"), 30*time.Minute)
	app.store = chooseStore(storeKind, storePath, ttl)
	app.sessionTTL = ttl
	// Cycle 3 P46: cache the store-bound broker on the app for
	// hot-path Subscribe/Publish call sites (the broker is shared
	// app-wide; the store owns the binding so v0.16+ cross-process
	// backends can swap implementations without touching call sites).
	app.topics = app.store.Broker()
	// Cycle 4 PT: register as the process-global broker so
	// Std.PubSub.publish (Task-shaped, callable from raw api
	// handlers / post-init goroutines / scheduled jobs) can find a
	// *liveApp without an update-tuple context.
	registerProcessBroker(app)

	// Resolve listen port early. cfg.Port wins over env; both fall
	// back to 8080. (Pre-v0.16.0 this was needed to seed
	// SKY_PARENT_URL on subprocess-spawned console children; the
	// inline console doesn't run as a child process so the port is
	// just for the listener.)
	port := 8080
	if p := Field(cfg, "Port"); p != nil {
		port = AsInt(p)
	}
	if v := skyGetenv("LIVE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	_ = port // referenced again below; keep the name in scope

	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/_sky/sse", app.handleSSE)
	mux.HandleFunc("/_sky/config", app.handleConfig)
	// v0.16.0: in-process inline Sky Console mount. Replaces the
	// v0.15.x subprocess + reverse-proxy mount. The function
	// internally gates on production-mode + sub-app context, so we
	// can call it unconditionally. Must run BEFORE
	// MountObservabilityEndpoints so the legacy HTML shell inside
	// the latter doesn't collide on /_sky/console (safeMount's
	// dedup catches that case anyway, but the explicit order
	// documents intent).
	//
	// PR 3 (v0.16.0): the app's optional `consoleAuth` field rides
	// in as an opaque `any` — `MountEmbeddedConsole` interprets it
	// inside the `app`-mode gate. nil → token-mode / production-mode
	// fallback per evaluateConsoleAuth.
	SetConsoleAuthCallback(app.consoleAuth)
	// v0.16.1 PR7 — seed SKY_PARENT_URL so the inline console_app's
	// init_ reads OUR OWN listener's loopback when it builds the
	// initial Model. The /_sky/console/api/* endpoints serve real
	// telemetry via MountConsoleEndpoints (mounted later as part of
	// MountObservabilityEndpoints). Without this, init_ falls back to
	// `State_mockOverview()` + `State_mockLogs()` and the deployed
	// console UI renders "Standalone mode — no parent URL configured"
	// with all-zero stats.
	//
	// SAFE: StartPushExporter gates on BOTH SKY_PARENT_URL +
	// SKY_LIVE_NAMESPACE being set. We only seed SKY_PARENT_URL, so
	// the push-exporter stays a no-op for the parent app (only
	// MountSubApp children set both).
	//
	// Only seed when UNSET — never overwrite a user-supplied value
	// (legacy v0.15 subprocess apps may still set this in env).
	if os.Getenv("SKY_PARENT_URL") == "" {
		os.Setenv("SKY_PARENT_URL", fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	MountEmbeddedConsole(mux)
	// If THIS process is a sub-app (env vars from MountSubApp set),
	// kick the push exporter — Log.* / counter / span writes flow
	// to the parent. No-op for standalone (parent) runs.
	StartPushExporter()
	// Observability endpoints — healthz / readyz / metrics / buildinfo.
	// Default-on (per docs/v1-rfc/1-observability.md); opt-out via
	// OBSERVABILITY_DISABLED=1. Mounted BEFORE the catch-all "/" route
	// so the dispatchRoot handler doesn't shadow them.
	//
	// Skipped when this app is running AS a sub-app (basePath set)
	// to avoid polluting the parent's observability namespace with
	// nested /_sky/console/_sky/{healthz,readyz,metrics,buildinfo}
	// duplicates. A console DOESN'T need its own metrics — its job
	// is to read the parent's.
	if app.basePath == "" {
		MountObservabilityEndpoints(mux)
	}
	// v0.16.1 PR 2 — boot-time mount-precedence invariant. When the
	// user EXPLICITLY asked for a console (SKY_CONSOLE_AUTH=token|app,
	// not a sub-app, SKY_CONSOLE_EMBED not off) but neither the
	// inline nor the legacy mount actually claimed /_sky/console,
	// this prints a FATAL stderr line + os.Exit(1). Catches the
	// hand-edited main.go that lost the console_app blank import.
	// No-op when shouldHaveConsole is false (off / unset / sub-app).
	AssertConsoleInvariantOrExit()
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
		// v0.16.9 — keys are LOWERCASE for backward-compat with apps
		// that read fields via Sky's `Dict.get "path" req` (literal
		// lowercase strings — matched case-sensitively by Dict_get).
		// Typed-codegen `req.path` access still works because
		// rt.Field falls back to case-insensitive map lookup.  See
		// the comment in Field for the full rationale.
		req := map[string]any{
			"path":    "/",
			"query":   "",
			"params":  Dict_empty(),
			"method":  "GET",
			"headers": Dict_empty(),
			"cookies": Dict_empty(),
		}
		res := sky_call(app.init, req)
		model := tupleFirst(res)
		GobRegisterTypeGraph(reflect.TypeOf(model))
		gobRegisterAll(model)
	}()

	// (port was resolved earlier so sub-app spawn could use it)

	// Production-mode detection — gates /_sky/metrics auth. Two
	// signals (RFC §"Resolved question 1"):
	//   1. Explicit env: SKY_ENV=production (highest priority).
	//   2. Heuristic: binding to all interfaces (":PORT" form, no
	//      explicit host, or 0.0.0.0). Containers, fly.io, k8s,
	//      cloud VMs all bind 0.0.0.0; local dev binds 127.0.0.1.
	//
	// Production-mode gate for /_sky/console + /_sky/metrics auth.
	// Rule: ENV (or SKY_ENV) is SET to anything OTHER than the
	// dev-marker set {"dev", "development", "local"} → gate.
	// ENV unset OR matching a dev marker → open.
	//
	// This is intentionally bias-to-gate: if you bother setting
	// ENV at all (staging, qa, production, prod, etc.), you mean
	// it's not a casual dev session and the gate should apply.
	// Default-open for unset ENV keeps dev workflows friction-free
	// (Docker / proxy / sidecar deploys all bind to varying
	// addresses, so the previous addr-based heuristic was
	// unreliable in both directions and has been removed).
	SetProductionMode(productionFromEnv())

	// Step 7 — OTel tracer init. Honours OTEL_EXPORTER_OTLP_ENDPOINT.
	// Non-fatal: any failure logs + falls back to noop tracer
	// (every span call becomes a zero-cost no-op).
	if err := InitTracingFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "[sky.live] OTel init failed (continuing without trace export): %v\n", err)
	}

	// Wrap the mux with panic recovery so one bad handler can't crash the process.
	// Layer order (outermost → innermost):
	//   1. panic recovery     — turn handler panics into 500s
	//   2. observability      — req-id, access log, metrics, OTel span
	//   3. CSRF middleware    — Phase 1.2; double-submit cookie; default ON
	//   4. user mux           — the actual handlers
	// Putting CSRF inside observability means rejected CSRF requests
	// STILL produce an access-log line + counter bump (you want to
	// see CSRF rejection rates as a metric — sudden spike = attack
	// or misconfiguration).
	csrfed := CSRFMiddleware(mux)
	observed := ObservabilityMiddleware(csrfed)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is Go's sentinel panic value
			// that handlers use to abort cleanly without logging
			// (httputil.ReverseProxy panics with it when the
			// client disconnects mid-stream — typical for SSE).
			// Re-panic so net/http's own handler-recover (which
			// special-cases this value) finishes the abort
			// cleanly, instead of us logging it as a 500.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			// Real panic — log to stderr so `go run` / tailing the
			// server surfaces the cause. Client still gets a
			// generic 500.
			fmt.Fprintf(os.Stderr,
				"[sky.live] panic handling %s %s: %v\n%s\n",
				r.Method, r.URL.Path, rec, debugStack())
			w.WriteHeader(500)
			fmt.Fprint(w, "Internal Server Error")
		}()
		observed.ServeHTTP(w, r)
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
	// Shutdown on SIGINT / SIGTERM / SIGHUP. SSE connections are
	// long-lived (heartbeat every 15 s, otherwise idle) so the
	// graceful `srv.Shutdown` would block forever waiting for them
	// to return to idle — even with a context timeout it returns
	// ctx.DeadlineExceeded WITHOUT actually closing the connections,
	// leaving the goroutines alive and the process unable to exit.
	// `srv.Close` forcibly closes the listener and every active
	// connection; SSE writers see ErrConnClosed on their next Write
	// and exit. Browsers see the dropped EventSource and the in-
	// page banner flips to "Reconnecting…" — same UX as a deploy.
	//
	// Two-press escalation: a second SIGINT triggers os.Exit(130),
	// which kills the process immediately even if something inside
	// srv.Close is wedged. Familiar Ctrl-C-twice idiom.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigCh
		fmt.Println("\nSky.Live shutting down…")
		// Flip readyz to 503 immediately so orchestrators (k8s /
		// fly.io / ECS) stop routing new traffic while in-flight
		// requests drain. healthz stays 200 — the process IS still
		// alive, just refusing new work.
		SetReady(false)
		// Flush pending OTel spans BEFORE killing the server so
		// in-flight requests' spans reach the collector. Bounded
		// timeout (2s VM, 500ms serverless) so we don't hang past
		// the orchestrator grace window.
		ShutdownTracing()
		// Stop the Std.Jobs worker (if started) so in-flight jobs
		// finish + the goroutine exits cleanly. Idempotent —
		// safe to call when no worker was ever spawned.
		JobsShutdown()
		// v0.16.1: drain the in-process HubExporter (and any other
		// registered shutdown hook) BEFORE srv.Close. 8 s budget
		// leaves 2 s safety within Cloud Run's 10 s grace window.
		// LIFO order — HubExporter (registered last, during boot)
		// runs first; future v0.17+ hooks fan out from here. No-op
		// when no exporter / no hooks are registered.
		RunShutdownHooks(8 * time.Second)
		// v0.16.0: the inline console runs in-process, so there's
		// no child to tear down. Pre-v0.16.0 this section closed
		// srv.Close() FIRST (to drain in-flight reverse-proxy
		// requests) then ShutdownSubApps() to signal the console
		// child. Now the console handler runs on the same mux, so
		// closing the server is sufficient.
		_ = srv.Close()
		// If srv.Close completes the listener teardown, ListenAndServe
		// returns and the function exits naturally. If something hangs,
		// a second Ctrl-C escapes via os.Exit. Without this watchdog,
		// a wedged goroutine could leave the user stuck.
		go func() {
			<-sigCh
			fmt.Fprintln(os.Stderr, "Sky.Live: forcing exit (second SIGINT)")
			os.Exit(130) // 128 + SIGINT(2)
		}()
	}()
	fmt.Printf("Sky.Live listening on :%d\n", port)
	err := srv.ListenAndServe()
	signal.Stop(sigCh)
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
	// Framing: SAMEORIGIN by default. SKY_LIVE_FRAME_ANCESTORS opts a
	// deploy into being embedded by specific origins (e.g. a control
	// plane's app-preview iframe) — emitted as a CSP `frame-ancestors`
	// directive, the only header that can scope framing to a
	// cross-origin allow-list (X-Frame-Options has no such value).
	if h.Get("X-Frame-Options") == "" && h.Get("Content-Security-Policy") == "" {
		if fa := os.Getenv("SKY_LIVE_FRAME_ANCESTORS"); fa != "" {
			h.Set("Content-Security-Policy", "frame-ancestors "+fa)
		} else {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
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
		// If staticDir is configured and the requested file exists at the
		// root of it (favicon.ico, robots.txt, apple-touch-icon.png, …),
		// serve it before 404'ing. Browsers always request /favicon.ico
		// from root, never from /static/, so without this serve-from-root
		// shortcut the user has no way to suppress the 404 short of adding
		// a `<link rel="icon">` to every page's head AND ensuring the
		// browser honours it instead of also probing the root.
		if app.staticDir != "" {
			candidate := filepath.Join(app.staticDir,
				filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")))
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				http.ServeFile(w, r, candidate)
				return
			}
		}
		http.NotFound(w, r)
		return
	}

	// Reuse the existing session when the cookie maps to one. Calling
	// init() on every GET (devtools previews, prefetch, second tabs)
	// would otherwise wipe sess.handlers and break the very next event
	// POST with "handler not found". Per-session lock prevents
	// concurrent re-renders racing each other's handlers.
	sid := sessionIDNamed(r, w, app.sessionTTL, app.cookieName)
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
		// v0.16.7 #417 — extend init's `req` with `params : Dict
		// String String`, keyed by the route pattern's `:name`
		// segments.  No-arg page constructors can now read URL
		// params directly from `req.params`; function-typed page
		// constructors continue to receive them positionally via
		// fillRoutePage.  Routes with no `:name` segments get an
		// empty Dict (same shape as pre-v0.16.7's missing field
		// when accessed via `Dict.get` — Maybe-typed read path).
		_, initParams := applyRouteWithParams(app, model, r.URL.Path)
		// v0.16.9 — keys lowercase for backward-compat with apps
		// doing `Dict.get "path" req` (case-sensitive Dict_get
		// kernel).  rt.Field's case-insensitive map fallback keeps
		// typed `req.path` access working.
		//
		// v0.16.8 #423 — init's `req` carries `Method` + `Headers` +
		// `Cookies` alongside #417's `Params`.  Apps bootstrap their
		// model from a session cookie at first render via
		// `Dict.get "sky_sid" req.cookies` — no Cmd.perform
		// round-trip needed.
		req := map[string]any{
			"path":    r.URL.Path,
			"query":   r.URL.RawQuery,
			"params":  initParams,
			"method":  r.Method,
			"headers": headersToDict(r.Header),
			"cookies": cookiesToDict(r.Cookies()),
		}
		res := sky_call(app.init, req)
		model = tupleFirst(res)
		cmd = tupleSecond(res)
		// Register model types for gob encoding so DB-backed
		// session stores can decode them on future Get calls.
		gobRegisterAll(model)
		sess = &liveSession{
			sseCh:     make(chan sseFrame, sseChanBuffer),
			cancelSub: make(chan struct{}),
			done:      make(chan struct{}),
		}
		// v0.16.5 #493 — session-identity bridge. If the gate that
		// preceded this handler (MountLiveSubAppInProcessWithGate's
		// `gate` callback, e.g. hub.consoleGateApp) wrote an Identity
		// to r.Context(), stash it on the fresh session so downstream
		// kernels can read it via SessionIdentity / currentLiveSession.
		// Survives encode/decode round-trips for DB-backed stores —
		// see storableSession in live_store.go.
		if id, ok := IdentityFromContext(r.Context()); ok {
			sess.identity = id
			sess.identityValid = true
		}
	}
	// Always set sid — both on fresh sessions AND on resumes from
	// persistent stores (which load `sess` without the sid field
	// populated). Cheap; idempotent on equal sids.
	sess.sid = sid

	// Cycle 4 HS: stamp the session on the handler goroutine for the
	// init + view + runCmd + setupSubscriptions block so synchronous
	// kernel calls (notably Http.Stream.open invoked directly from
	// init) can resolve `currentLiveSession()`. The cleared-on-exit
	// discipline mirrors RunWithTraceContext.
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	// Route dispatch: pick the page ADT value for this URL path and
	// splice it into model.Page via RecordUpdate. Always run so the
	// returning visitor lands on the URL they requested.
	model = applyRoute(app, model, r.URL.Path)
	// v0.16.7 #418 — onNavigate Msg dispatch after every URL-driven
	// route update.  No-op when cfg.onNavigate is nil (the default).
	// Fires on initial mount, sky-nav fetches, and popstate
	// Back/Forward — every code path that calls applyRoute reaches
	// here, so the app sees a uniform Msg on every route change.
	model, navCmd := dispatchOnNavigate(app, model)
	if navCmd != nil {
		if cmd == nil {
			cmd = navCmd
		} else {
			cmd = Cmd_batch([]any{cmd, navCmd})
		}
	}
	sess.model = model
	sess.handlers = map[string]any{}

	if cmd != nil {
		app.runCmd(sess, cmd)
	}
	app.setupSubscriptions(sess)

	vn, _ := app.safeViewCall(model)
	assignSkyIDs(&vn, app.skyIDPrefixOrDefault())
	applyStyleInjections(&vn)
	body := renderVNode(vn, sess.handlers)
	// Initial mount writes the full HTML directly into the HTTP
	// response below — the client receives this body as the page,
	// so it counts as BOTH "last computed" and "last shipped".
	sess.commitRender(&vn, body)
	sess.lastShippedBody = body
	app.store.Set(sid, sess)

	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// No third-party font is loaded and no font-family is forced.
	// Apps choose their own typography via their own stylesheet (e.g.
	// styleNode in their view, or static-served self-hosted webfonts).
	// Privacy: no Google Fonts request. Accessibility: no !important
	// override fighting app-level type choices.
	//
	// Minimal CSS reset (`liveBaseCSS` below) zeroes out the worst
	// browser-default offenders that interact badly with Std.Ui's
	// flex-based layout: <p>/<h1>-<h6>/<button>/<input>/<a> default
	// margins + font sizes that would push everything out of position
	// otherwise. The reset is deliberately minimal — it does NOT
	// impose font choice, line-height, or colour scheme. Apps that
	// need a "designed" look still attach their own typography via
	// view-level style attrs or a styleNode at the top of view.
	csrfToken := CurrentCsrfToken(r)
	// devBanner is "" in production; injected as a sibling of sky-root
	// so it survives every diff/patch cycle (root replacement won't
	// blow it away) and stays pinned bottom-right via position:fixed.
	// Also suppressed when this app IS itself running as a sub-app
	// (basePath != "") — the bundled Sky Console is the canonical
	// case: rendering a "🔍 Console" link inside the console itself
	// would be recursive and confusing.
	var devBanner string
	if app.basePath == "" {
		devBanner = devBannerHTML()
	}
	// When this app is mounted as a sub-app under a URL prefix
	// (e.g. /_sky/console), the inlined JS needs to know to prefix
	// its /_sky/event / /_sky/sse / /_sky/config URLs with that
	// base. We surface the prefix via a <meta> tag rather than a JS
	// variable so it's also visible to non-JS clients (e.g. SSR
	// debugging) and survives any future templating layer. Empty
	// content for root-mounted apps — the JS treats "" as "no
	// prefix" (the historical default).
	baseMeta := fmt.Sprintf(`<meta name="sky-base" content=%q>`, app.basePath)
	// App-supplied head content (Live.app cfg.head : Model -> List
	// (Html msg)). Sits AFTER baseMeta + the runtime's required
	// charset / viewport tags, BEFORE the inline <style> reset, so
	// app overrides (custom favicon, canonical URL, JSON-LD,
	// per-page title) win against the defaults and the runtime's
	// own reset still wins against any clashing inline-style
	// override in the app's head. Empty string when app didn't
	// supply `head` — byte-identical to pre-v0.15.58 output.
	headExtra := renderAppHead(app.head, model)
	fmt.Fprintf(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">%s%s<style>%s</style></head><body><div id=\"sky-root\">%s</div>%s<script>%s</script></body></html>", baseMeta, headExtra, liveBaseCSS, body, devBanner, liveJSWithCfgAndCsrfWithBase(sid, app.bannerCfg, csrfToken, app.basePath))
}

// renderAppHead invokes the optional `head : Model -> List (Html
// msg)` callback and serialises the returned list to a single HTML
// string ready to splice into <head>. Returns "" when `head` is
// nil (the optional-field default), when the callback returns
// nil, when the result isn't a list, or when the list is empty —
// matching the pre-feature output byte-for-byte.
//
// Each element is rendered via the same renderVNode pipeline the
// body uses, with a discarded handlers map (head nodes never have
// event bindings — `onClick`-style attrs on a <title>/<meta>/
// <link>/<script> would be a user bug, but we don't enforce it
// here; the renderer emits the sky-event attr and the JS driver
// simply never finds the element in the body).
func renderAppHead(head any, model any) string {
	if head == nil {
		return ""
	}
	result := sky_call(head, model)
	if result == nil {
		return ""
	}
	nodes := asList(result)
	if len(nodes) == 0 {
		return ""
	}
	var sb strings.Builder
	// Discardable handlers map — head elements should not produce
	// wire-event bindings (no JS driver to listen on them), and
	// each <head> serialisation is one-shot per full GET.
	handlers := map[string]any{}
	for _, n := range nodes {
		vn := HtmlToVNode(n)
		sb.WriteString(renderVNode(vn, handlers))
	}
	return sb.String()
}

// liveBaseCSS is the minimal reset injected into every Sky.Live page.
// Goals:
//  1. Zero out browser-default margins on <p>, <h1>-<h6>, <ul>, <ol>,
//     <li>, <body>, <html> so flex layout from Std.Ui isn't fighting
//     legacy editorial CSS that wants 1em vertical spacing.
//  2. Inherit font on form controls — <button> / <input> / <select>
//     / <textarea> default to a smaller browser font, which makes
//     Std.Ui buttons look out of place next to surrounding text.
//  3. box-sizing: border-box so padding adds to the slot's content
//     area rather than expanding the box. Std.Ui generates explicit
//     width/height in cells; border-box keeps that math correct.
//  4. min-height: 100vh on body so a dark-themed view fills the
//     viewport instead of leaving a white strip below the content.
//  5. Sensible default font-family (system stack, no web fetch) so
//     apps that don't set their own typography don't get Times New
//     Roman.
//
// The reset is ~600 bytes — negligible compared to the typical page
// body. NO !important is used; user view styles always win.
const liveBaseCSS = `*,*::before,*::after{box-sizing:border-box}` +
	`html,body{margin:0;padding:0;min-height:100%}` +
	`body{min-height:100vh;display:flex;flex-direction:column;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;line-height:1.4}` +
	// #sky-root must grow to fill <body> and be a flex column itself,
	// otherwise a Std.Ui `Ui.height Ui.fill` root has no resolvable
	// parent height to flex against and collapses to content height
	// (issue #63). flex:1 0 auto fills the viewport; min-height:0 lets
	// inner scroll regions shrink below content size.
	`#sky-root{display:flex;flex-direction:column;flex:1 0 auto;min-height:0}` +
	`h1,h2,h3,h4,h5,h6,p,ul,ol,li,figure,blockquote,pre,dl,dd{margin:0;padding:0;font-weight:inherit;font-size:inherit}` +
	`button,input,select,textarea{font:inherit;color:inherit}` +
	`button{background:none;border:0;padding:0;cursor:pointer;text-align:inherit}` +
	`a{color:inherit;text-decoration:none}` +
	`img,video,canvas,svg{display:block;max-width:100%}`

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
	// the bigger headroom. Override via <PREFIX>_LIVE_MAX_BODY_BYTES
	// (or sky.toml [live] maxBodyBytes) — the fileMaxSize attr on
	// the input is the client-side guard but isn't load-bearing for
	// the server cap. Server-side validation in `update` is the
	// authoritative check; this is the upper bound on what reaches
	// the runtime at all.
	maxBody := int64(5 << 20)
	if n, ok := parsePositiveInt(skyGetenv("LIVE_MAX_BODY_BYTES")); ok {
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
		// Mark this 404 as a real Sky.Live response so the client's
		// probe (in __skyForceReopenSSE) can distinguish "session gone,
		// reload to recover" from a generic proxy-rewritten 404.
		w.Header().Set("X-Sky-Live", "1")
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
		// X-Sky-Live header is harmless here (sendBeacon ignores it) but
		// keeps the response signature consistent across all _sky/event
		// success paths.
		w.Header().Set("X-Sky-Live", "1")
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
		vn, _ := app.safeViewCall(sess.model)
		assignSkyIDs(&vn, app.skyIDPrefixOrDefault())
		applyStyleInjections(&vn)
		body := renderVNode(vn, sess.handlers)
		// Route through commitRender (Cycle 3 P40 / Gap C7) so
		// the rebuilt-handlers branch keeps prevTree +
		// lastComputedBody coherent. Previously the body was
		// discarded, so lastComputedBody pointed at whatever the
		// prior process (or prior dispatch) had written.
		sess.commitRender(&vn, body)
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
		// Unknown Msg name: refuse to dispatch instead of building a
		// SkyADT with Tag=-1 and letting the user's `case` fall
		// through to the exhaustiveness `Unreachable`. Caller gets a
		// clear error; the user's update never sees a malformed Msg.
		// Internal `__sky*` sentinels (e.g. `__skySessionPing` —
		// liveness probe sent by the client) are silently accepted as
		// no-ops so they don't pollute the log; the client only cares
		// about session-existence (404 vs anything else).
		if tag < 0 {
			sess.mu.Unlock()
			w.Header().Set("X-Sky-Live", "1")
			if strings.HasPrefix(req.Msg, "__sky") {
				w.WriteHeader(200)
				return
			}
			fmt.Fprintf(os.Stderr, "[sky.live] unknown Msg constructor %q (direct-send); dropping event\n", req.Msg)
			http.Error(w, "unknown Msg constructor: "+req.Msg, 400)
			return
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
	// /_sky/event ships its reply directly down the POST response
	// (writeEventJSON / writeEventHTML below), so for the suppression
	// contract this counts as "the client received the new body".
	// Advance lastShippedBody under sess.mu so any concurrent SSE
	// producer (tick subscription, runPerformBody) reading
	// lastShippedBody picks up the post-click value and correctly
	// suppresses a redundant frame on its next tick. Skip the empty-
	// body case (no-op dispatch — body2 == "") so the field continues
	// to reflect whatever the client genuinely last received.
	if body2 != "" {
		sess.lastShippedBody = body2
	}
	// Capture outgoing protocol metadata before releasing the lock so
	// the seq reflects this session's true mutation order. Bumped once
	// per reply (including no-op replies) so the client's cross-channel
	// ordering works uniformly.
	respSeq := sess.nextLocalSeq()
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
	//
	// IMPORTANT: an empty patch list is a valid + correct outcome of
	// input-authority alignment — the model advanced (e.g. controlled
	// `value` attr now matches what the user already has), the server's
	// view differs from the client's last DOM, but `clientStateFromRequest`
	// (the I5 alignment) recognises every diff as already-known and drops
	// it. We MUST NOT treat empty patches as "diff failed, send full
	// HTML" — doing so would replace the entire sky-root, recreating
	// every input and blanking uncontrolled fields like password. Empty
	// patches → empty JSON ack with up-to-date seq/ackInputs metadata,
	// same shape as the byte-identical (body2 == "") branch above.
	// `patchesAreFullReplace([])` returns false (length-1 check), so
	// empty patches pass through to writeEventJSON.
	if prev != nil && newTree != nil {
		patches := diffTrees(prev, newTree, clientStateFromRequest(req.InputState))
		if !patchesAreFullReplace(patches) {
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
	w.Header().Set("X-Sky-Live", "1")
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
	h.Set("X-Sky-Live", "1")
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
		vn, _ := app.safeViewCall(sess.model)
		assignSkyIDs(&vn, app.skyIDPrefixOrDefault())
		applyStyleInjections(&vn)
		body := renderVNode(vn, sess.handlers)
		// Route through commitRender (Cycle 3 P40 / Gap C7) so
		// the rebuilt-handlers branch keeps prevTree +
		// lastComputedBody coherent — same shape as the
		// handleEvent rebuild above.
		sess.commitRender(&vn, body)
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
		// Unknown Msg name — same defence as the single-event path
		// above. Silently drop (this is the batched/tab-unload path
		// so there's no response channel to surface the error).
		// `__sky*` sentinels are silently accepted as no-ops too.
		if tag < 0 {
			sess.mu.Unlock()
			if !strings.HasPrefix(ev.Msg, "__sky") {
				fmt.Fprintf(os.Stderr, "[sky.live] unknown Msg constructor %q (batched); dropping event\n", ev.Msg)
			}
			return
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
	// Capture lastShippedBody BEFORE dispatch so we can suppress a
	// byte-identical view (symmetric with runPerformBody + the
	// Time.every SSE producer — v0.15.14 / v0.15.17). Comparing
	// against lastShippedBody (not lastComputedBody) means the
	// suppression contract is about the wire, not about dispatch's
	// internal post-render write. Without this gate a beacon-driven
	// tab-unload dispatch that produces no view change still ships
	// a redundant SSE frame to other observers of the session.
	prevShipped := sess.lastShippedBody
	// Cycle 3 P50a / Gap C11: capture prevTree BEFORE dispatch so the
	// SSE producer can diff against the tree the client last saw.
	prevTreeBeforeDispatch := sess.prevTree
	body2 := app.dispatch(sess, msg)
	newTreeAfterDispatch := sess.prevTree
	// Bump localSeq once per batched entry so any SSE frame pushed as a
	// side effect carries a unique seq. Each dispatch that mutates the
	// view is its own observable event.
	//
	// Cycle 3 P41 / Gap C6: snapshot the wire metadata (seq +
	// ackInputs + body) under sess.mu, then release the lock BEFORE
	// the JSON marshal. The marshal is CPU-bound and was previously
	// blocking every other dispatcher on this session for the entire
	// encode. Advancing lastShippedBody stays under the lock so the
	// suppression decision is atomic with the seq bump — two
	// concurrent dispatches can never both decide "ship" on the same
	// prior-shipped body.
	var snap frameSnapshot
	var patches []Patch
	var haveFrame bool
	if body2 != "" && body2 != prevShipped {
		snap = sess.prepareFrameSnapshot(body2)
		sess.lastShippedBody = body2
		if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
			// Cycle 3 P50a / Gap C11: structural diff for SSE
			// transport. clientState is nil here — batched tab-
			// unload dispatch happens without fresh inputState
			// from the now-unloading tab; observers in OTHER tabs
			// rely on __skyApplyPatches' dirty-input authority
			// filter to preserve their own in-flight typing.
			patches = diffTrees(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	// Push to other subscribers (other tabs, SSE listeners). The
	// originating tab has already unloaded so the frame is for anyone
	// else observing the session. Marshal happens here, outside the
	// lock — see frameSnapshot doc above for the rationale.
	if haveFrame {
		// Cycle 3 P50a / Gap C11: ship event:patches when the diff
		// is small, falling back to event:patch for first-render or
		// full-replace shapes.
		frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
		select {
		case sess.sseCh <- frame:
		default:
			// Cycle 3 P42 / Gap C14: buffer full; drop + count.
			// Buffer capacity is SKY_LIVE_SSE_BUFFER (default 16).
			recordSseDrop(sess.sid)
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
	// Cycle 4 HS: stamp the session pointer onto the calling
	// goroutine so kernels invoked from `update` / `view` /
	// `runCmd` (notably Http.Stream.open / close) can resolve the
	// CURRENT session for streams registry lookup. Cleared on exit
	// — symmetric with RunWithTraceContext's discipline.
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()
	// Step 5 — diff-based Msg logging. Snapshot the pre-update
	// model + start time so ObserveMsgLog (called near the end of
	// dispatch) can decide whether to emit a log line. Lifecycle
	// marker (Step 6) detected here too.
	msgLogCtx := BeginMsgLogForSession(msg, sess.model, sess.sid)
	// Step 6 — unwrap Std.Live.lifecycle so the user's update
	// receives the inner Msg, not the wrapper.
	msg = UnwrapLifecycle(msg)

	var dispatchErr error
	var finalCmd any
	// Snapshot the pre-dispatch view invariants so a panic anywhere in
	// the body (update, view-render, runCmd, setupSubscriptions) can
	// roll them back. Without this, a panic AFTER `sess.prevTree = &vn`
	// (line ~2594) but BEFORE `sess.lastComputedBody = body` (line
	// ~2618) leaves the two fields desynced: prevTree pointing at the
	// new (possibly partial) render and lastComputedBody still on the
	// prior-good body. The next dispatch's diff baseline would then
	// use a stale tree — typically harmless, but the asymmetry is
	// fragile and the audit (Cycle 3 P35 residual c) flagged it as
	// obscuring the intent of the recovery path. We restore both
	// fields so the failed dispatch is observably a no-op for the
	// suppression + diff layers.
	//
	// lastShippedBody is NOT snapshotted here because dispatch never
	// writes it — its contract is "last thing sent to the wire",
	// owned by the SSE producer callers (dispatchBatched,
	// runPerformBody, the tick goroutine). A panic mid-dispatch has
	// definitionally not shipped anything, so lastShippedBody stays
	// at whatever the last successful SSE emission set it to.
	prevTreeOnEntry := sess.prevTree
	prevComputedOnEntry := sess.lastComputedBody
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr,
				"[sky.live] dispatch panic recovered, dropping event: %v\n%s\n",
				r, debug.Stack())
			body = ""
			dispatchErr = fmt.Errorf("dispatch panic: %v", r)
			// Roll back the view invariants. The current dispatch has
			// failed; the prior valid prevTree / lastComputedBody must
			// remain the source of truth for the next dispatch's
			// suppression and diff baseline. Route through commitRender
			// (Cycle 3 P40 / Gap C7) so the rollback path follows the
			// same atomic-pair contract as every other write site.
			sess.commitRender(prevTreeOnEntry, prevComputedOnEntry)
		}
		ObserveMsgLog(msgLogCtx, sess.model, finalCmd, dispatchErr)
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
			// Mark as error outcome for the Msg log so guard
			// rejections surface as warnings (caller-visible
			// auth/permission failures).
			dispatchErr = fmt.Errorf("guard rejected: %v", reason)
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
	// Tier 1 auto-trace: wrap the TEA update in a Msg span. This is
	// the causal middle layer — DB / Http / Auth child spans opened
	// by the update body nest under "msg <Name>", so a trace shows
	// which Msg triggered which queries. The span's trace id is
	// stamped onto the Msg log entry (msgLogCtx.TraceID) so the log
	// line and its trace share one correlation id.
	result, msgTraceID := WithMsgSpanTraced(msgDisplayName(msg),
		func() any { return sky_call2(app.update, msg, sess.model) })
	msgLogCtx.TraceID = msgTraceID
	sess.model = tupleFirst(result)
	cmd := tupleSecond(result)
	finalCmd = cmd
	sess.handlers = map[string]any{}
	vn, _ := app.safeViewCall(sess.model)
	assignSkyIDs(&vn, app.skyIDPrefixOrDefault())
	applyStyleInjections(&vn)
	body = renderVNode(vn, sess.handlers)
	// Commit prevTree + lastComputedBody as one atomic step (Cycle 3
	// P40 / Gap C7). Previously this was two separate writes — prevTree
	// here, lastComputedBody after runCmd + setupSubscriptions — which
	// left a window where the two fields could be observed desynced
	// (handled by the panic-rollback snapshot above, but fragile).
	//
	// runCmd + setupSubscriptions don't read prevTree or
	// lastComputedBody synchronously (they spawn goroutines that
	// acquire sess.mu later), so consolidating the write here is
	// behaviourally equivalent to the prior split and tightens the
	// invariant window.
	//
	// No-op suppression: previously we short-circuited on
	// `body == sess.prevBody` (byte-equality) so a Time.every tick
	// without view-reachable state changes wouldn't push a redundant
	// HTML frame. That was load-bearing on map iteration order being
	// random — once renderVNode's attr/event loops were sorted
	// (v0.15.x deterministic-HTML fix) the byte-check started firing
	// for cases where the diff path's input-authority alignment
	// expected to see a patch flow, freezing live keypress dispatch.
	//
	// The HTTP /_sky/event path uses the diff result (diffTrees, with
	// I5 client-state alignment) to decide whether to ship patches at
	// all — a true no-op produces an empty patch list, which already
	// routes through writeEventJSON without an SSE frame. The Time.every
	// SSE producer at setupSubscriptions checks `body != ""` to skip
	// pushing a frame, so we keep that contract: returning the body
	// unconditionally here means the SSE callsite continues to ship
	// frames per tick, but the client's __skyHandleResponse uses the
	// seq guard + focus-preserving splice so a redundant frame is a
	// bandwidth cost (~150 bytes/tick), not a correctness break.
	//
	// lastShippedBody is intentionally NOT written here — that's the
	// SSE producer's job (post-P39 / Gap C2). Suppression callers
	// compare against the last value the client actually received,
	// not against this just-computed value.
	sess.commitRender(&vn, body)
	// Process Cmds (may spawn goroutines)
	app.runCmd(sess, cmd)
	// Re-evaluate subscriptions based on new model
	app.setupSubscriptions(sess)
	return body
}

// renderView: re-render from current session model without updating
// the model (used by dispatch when guard short-circuits).
//
// Routes through commitRender (Cycle 3 P40 / Gap C7) so the guard-
// rejected path keeps prevTree + lastComputedBody coherent.
// Previously this wrote ONLY prevTree, leaving lastComputedBody
// pointing at the prior dispatch's body even though the just-
// rendered tree had just replaced prevTree — a silent contract
// break the audit explicitly flagged.
func (app *liveApp) renderView(sess *liveSession) string {
	sess.handlers = map[string]any{}
	vn, _ := app.safeViewCall(sess.model)
	assignSkyIDs(&vn, app.skyIDPrefixOrDefault())
	applyStyleInjections(&vn)
	body := renderVNode(vn, sess.handlers)
	sess.commitRender(&vn, body)
	return body
}

// safeViewCall wraps the user's `view` function with a defer/recover
// so a single panic (e.g. rt.Coerce mismatch on a record-update
// result, missing field on a polymorphic any, divide-by-zero in a
// view-time computation) doesn't drop the whole Sky.Live session.
//
// Hardens the navigation/view path: panics in user code are caught,
// logged structurally, and the session continues with a degraded
// "render error" notice. The user's next dispatch (re-click, browser
// reload) runs view again from a clean state.
//
// Before this defer/recover, a single bad Coerce in a typed-record
// codegen site (e.g. RecordUpdate-then-Coerce[T]) would crash the
// dispatch goroutine — Sky.Live's session was effectively destroyed
// until full page reload AND the user had no diagnostic. With this
// wrapper the panic becomes a structured log + a visible-but-
// recoverable error notice on the page.
//
// Returns the rendered VNode and `panicked=true` when the wrapper
// caught a panic. Callers don't need to special-case the panicked
// path — the fallback VNode renders cleanly through the same
// assignSkyIDs / applyStyleInjections / renderVNode pipeline.
//
// See memory/sky_navigation_panic_class.md for the structural
// root cause (Sky's reflect-based Coerce sites grow per-render +
// any one panicking takes down the whole session).
func (app *liveApp) safeViewCall(model any) (VNode, bool) {
	var vn VNode
	var panicked bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				reason := fmt.Sprintf("%v", r)
				stack := string(debug.Stack())
				// Structured log for ops dashboards (Sky Console, OTel).
				// Stack is included so the panic site is grep-able from
				// the journalctl / Cloud Logging stream. logEmit fires
				// immediately (no Task wrap) since we're already inside
				// the deferred recover.
				logEmit(logLevelError, "error", "sky.live.view.panic",
					[]any{
						"reason", reason,
						"stack_head", firstLines(stack, 8),
					},
				)
				vn = renderViewPanicFallback(reason)
			}
		}()
		vn = HtmlToVNode(sky_call(app.view, model))
	}()
	return vn, panicked
}

// renderViewPanicFallback produces a self-contained VNode that
// renders a small "Render error" banner inline with the page chrome.
// The fallback CANNOT call any user code — it only emits literal
// VNode values built from Go strings. The recovery is local to ONE
// dispatch; the next dispatch re-runs view normally.
//
// Visible to the user as a dark-mode-aware error notice with the
// panic reason embedded (truncated to 200 chars to keep the UI
// readable when the reason is a long stack frame).
func renderViewPanicFallback(reason string) VNode {
	short := reason
	if len(short) > 200 {
		short = short[:200] + "…"
	}
	style := "background:#3a1f24;color:#ffb3b3;" +
		"border:1px solid #6b3438;border-radius:6px;" +
		"padding:14px 18px;margin:16px;" +
		"font:13px/1.5 ui-monospace,Menlo,monospace;"
	return VNode{
		Kind: "element",
		Tag:  "div",
		Attrs: map[string]string{
			"style": style,
			"role":  "alert",
		},
		Children: []VNode{
			{
				Kind: "element",
				Tag:  "strong",
				Attrs: map[string]string{
					"style": "color:#ff7a7a;",
				},
				Children: []VNode{vtext("Render error")},
			},
			vtext("  "),
			vtext(short),
			{
				Kind: "element",
				Tag:  "div",
				Attrs: map[string]string{
					"style": "margin-top:8px;color:#a0a0aa;font-size:11px;",
				},
				Children: []VNode{
					vtext("This dispatch's view panicked — the session " +
						"survived. Refresh the page or trigger a new " +
						"action to retry."),
				},
			},
		},
	}
}

// firstLines returns the first N lines of s — used to keep log
// stack-head fields compact in the structured log.
func firstLines(s string, n int) string {
	count := 0
	for i, c := range s {
		if c == '\n' {
			count++
			if count >= n {
				return s[:i]
			}
		}
	}
	return s
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
		// Capture the triggering request's FULL trace context from
		// the CURRENT goroutine (the dispatch path about to spawn
		// the Task goroutine). The ctx carries both the OTEL span
		// (so the Task's spans nest under the request) and the Sky
		// request-id (so logs correlate). Without this the spawned
		// goroutine is untracked.
		parentCtx := CurrentTraceContext()
		go app.runPerform(sess, c.task, c.toMsg, parentCtx)
	case "publish":
		// Cycle 3 P48: Std.Cmd.publish dispatch. Route every publish
		// through app.Publish so the "one bump per publish, BEFORE
		// fan-out" invariant (design doc §3.2) is a function-boundary
		// guarantee — never open-coded at the call site.
		//
		// app.topics may be nil for tests that build a bare liveApp
		// (no store), in which case publish is a no-op. Production
		// apps always have a store + cached broker (see liveApp wiring
		// in newLiveApp / Live_app).
		if app.topics == nil {
			return
		}
		app.Publish(c.topic, SessionEvent{
			Payload: c.payload,
			Origin:  sess.sid,
		})
	case "publishNoEcho":
		// Cycle 4 NE / issue #359 — same dispatch as "publish" but
		// the broker SKIPS delivery to subscribers whose ownerSid
		// matches sess.sid (i.e. the publisher's own session). Pair
		// with a direct model update for the "instant feedback for
		// publisher" pattern without paying the broker round-trip.
		if app.topics == nil {
			return
		}
		app.Publish(c.topic, SessionEvent{
			Payload:    c.payload,
			Origin:     sess.sid,
			SkipOrigin: true,
		})
	}
}

func (app *liveApp) runPerform(sess *liveSession, task any, toMsg any, parentCtx context.Context) {
	// Stamp the parent request's trace context on this goroutine so
	// kernels running inside the Task (Db.query, Http.get, Log.info,
	// …) emit spans + logs correlated to the user's request. Cleared
	// on exit so the sync.Map entry doesn't leak past goroutine
	// recycling.
	//
	// Cycle 4 HS: also stamp the live session so Http.Stream.open
	// invoked INSIDE the Task (the canonical pattern — open in the
	// Task that Cmd.perform spawns, dispatch returned StreamId via
	// the result Msg) registers the stream handle on the OWNING
	// session. Without this stamp the stream becomes orphaned and
	// markDone can't sweep it.
	RunWithTraceContext(parentCtx, func() {
		runWithLiveSession(sess, func() {
			app.runPerformBody(sess, task, toMsg)
		})
	})
}

func (app *liveApp) runPerformBody(sess *liveSession, task any, toMsg any) {
	// task is a Sky Task — a zero-arg func() any returning SkyResult.
	// Wrap its execution in a cmd.perform span (Tier 1 auto-trace).
	result := WithCmdSpan("perform", func() any { return sky_call(task, nil) })
	// toMsg : Result err a -> Msg — convert result to Msg
	msg := sky_call(toMsg, result)
	// Push update through locked dispatch, then emit an SSE frame
	// carrying the session-wide seq. Keeping frame construction under
	// the same lock as dispatch means the seq reflects the actual
	// mutation order even when other goroutines dispatch concurrently.
	sess.mu.Lock()
	// Compare against lastShippedBody — the last body the client
	// actually received — so a perform whose dispatch byte-equals
	// what the client already has doesn't push a redundant frame.
	prevShipped := sess.lastShippedBody
	// Capture prevTree BEFORE dispatch so the structural diff can run
	// against the tree the client last saw, not the post-dispatch one.
	// Cycle 3 P50a / Gap C11: the SSE channel now ships either a full
	// HTML body OR a structural patch envelope depending on what
	// diffTrees produces (see chooseSSEFrame below).
	prevTreeBeforeDispatch := sess.prevTree
	body := app.dispatch(sess, msg)
	newTreeAfterDispatch := sess.prevTree
	// Cycle 3 P41 / Gap C6: capture (seq, body, ackInputs) under
	// sess.mu via prepareFrameSnapshot, then release the lock and
	// run JSON marshal outside. The previous shape held the mutex
	// across encodeSSEFrame's marshal (~200µs for a 50 KB body on
	// M1), stalling every other dispatcher on the session for the
	// full encode. lastShippedBody is advanced under the lock so
	// concurrent producers (Tick goroutine, dispatchBatched) see a
	// coherent prior-shipped value when they take their snapshot.
	var snap frameSnapshot
	var patches []Patch
	var haveFrame bool
	if body != "" && body != prevShipped {
		snap = sess.prepareFrameSnapshot(body)
		sess.lastShippedBody = body
		if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
			// Cycle 3 P50a / Gap C11: compute the structural diff
			// against the tree the client last saw. clientState is
			// nil here — SSE-pushed renders are server-initiated
			// (Cmd.perform completion / Time.every tick), no fresh
			// inputState arrives from the client. The client-side
			// authority filter in __skyApplyPatches still drops
			// value/checked/selected attrs on dirty inputs, so
			// in-flight typing is preserved without server-side
			// clientState alignment.
			patches = diffTrees(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	if !haveFrame {
		return
	}
	// Marshal outside the lock. chooseSSEFrame picks event:patches
	// (structural diff) vs event:patch (legacy full body) based on
	// the diff result. Both paths run JSON marshalling outside the
	// lock — wire-format equivalence with the pre-P50a shape is
	// preserved for the fallback (legacy) path.
	frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
	select {
	case sess.sseCh <- frame:
	default:
		// Cycle 3 P42 / Gap C14: channel full; drop + count.
		// Buffer capacity is SKY_LIVE_SSE_BUFFER (default 16).
		recordSseDrop(sess.sid)
	}
}

// flattenSubs walks a Sub value, recursing into "batch", and appends
// every non-batch leaf (kind = "every", "subscribeTopic", "none", …)
// to `out`. Pure walk — no I/O, no registry touch. Used by
// setupSubscriptions to demux multi-shape Sub.batch results into the
// per-kind dispatch arms.
//
// Cycle 3 P48 / docs/skylive/pubsub-design.md §3.3 + §4.1: pub/sub
// subscriptions arrive co-mingled with Time.every via Sub.batch, so
// the runtime now walks the batch list instead of insisting on a
// single Sub.every. Pre-P48 behaviour pinned by
// live_store_delete_test.go continues to work because a bare
// Sub.every (the existing common case) lands in flatSubs verbatim.
func flattenSubs(s any, out []subT) []subT {
	sub, ok := s.(subT)
	if !ok {
		return out
	}
	if sub.kind == "batch" {
		for _, child := range sub.batch {
			out = flattenSubs(child, out)
		}
		return out
	}
	out = append(out, sub)
	return out
}

// setupSubscriptions: re-evaluate subscriptions for the new model.
//
// Two subscription kinds (post-P48):
//
//   - "every" — Time.every interval Msg. Cancel-and-replace each
//     dispatch via sess.cancelSub (the goroutine selects on it).
//     A bare Sub.every (the existing common case) keeps its pre-P48
//     shape.
//
//   - "subscribeTopic" — Std.Sub.subscribeTopic topic toMsg. Diff-mode:
//     compute (added, removed) vs sess.activeSubs; cancel only
//     `removed`, subscribe only `added`. Topics in the intersection
//     keep their existing channel + goroutine so no broadcast falls
//     in the gap (design doc §4.1).
//
// Sub.batch composes them — `subscriptions = \model -> Sub.batch [
// Sub.every 1000 Tick, Sub.subscribeTopic "chat" ChatMsg ]` is the
// canonical multi-source shape.
func (app *liveApp) setupSubscriptions(sess *liveSession) {
	// Cancel existing ticker (Time.every always rebuilds; pub/sub
	// uses its own per-topic cancels stored in sess.activeSubs).
	//
	// Bug #339: serialise the close-then-reassign under
	// cancelSubMu so two concurrent callers can't both observe the
	// same `sess.cancelSub` pointer and double-close it. In
	// production the outer dispatch sites hold sess.mu so they
	// already serialise, but test fixtures and any future
	// non-dispatch caller need the contract enforced on the field
	// itself. The crit-section is tiny (one close, one make) so
	// contention is negligible even under tight Time.every ticks.
	sess.cancelSubMu.Lock()
	close(sess.cancelSub)
	sess.cancelSub = make(chan struct{})
	sess.cancelSubMu.Unlock()

	if app.subscriptions == nil {
		// No subscriptions at all → tear down anything that was
		// previously active (e.g. user returned Sub.subscribeTopic
		// last dispatch, returns Sub.none / no subscriptions fn now).
		app.applyTopicSubsDiff(sess, nil)
		return
	}
	subResult := sky_call(app.subscriptions, sess.model)
	leaves := flattenSubs(subResult, nil)

	// Partition leaves by kind. We honour ONE Sub.every per dispatch
	// (the existing contract — see Sub_batch doc); any number of
	// Sub.subscribeTopic entries; any number of Sub.subscribeStream
	// entries (one per active Http.Stream).
	var everyLeaf *subT
	desired := map[string]subT{}
	desiredStreams := map[int64]subT{}
	// v0.15.46: WebSocket subs keyed by `<socketID>:<wsKind>` so the
	// four onMessage/onOpen/onClose/onError variants coexist per
	// socket.
	desiredWs := map[string]subT{}
	for i := range leaves {
		leaf := leaves[i]
		switch leaf.kind {
		case "every":
			if everyLeaf == nil {
				everyLeaf = &leaves[i]
			}
		case "subscribeTopic":
			// Last-write-wins per topic — a user binding two decoders
			// to the same topic in one dispatch is a misuse; we take
			// the last entry deterministically rather than panicking.
			desired[leaf.topic] = leaf
		case "subscribeStream":
			// Last-write-wins per streamID — same rationale as topics.
			desiredStreams[leaf.streamID] = leaf
		case "subscribeWebSocket":
			key := fmt.Sprintf("%d:%s", leaf.socketID, leaf.wsKind)
			desiredWs[key] = leaf
		}
	}

	// Apply pub/sub diff BEFORE spawning the Time.every goroutine
	// so a single dispatch's worth of work touches the registry
	// once + lands on a coherent activeSubs map.
	app.applyTopicSubsDiff(sess, desired)
	app.applyStreamSubsDiff(sess, desiredStreams)
	app.applyWsSubsDiff(sess, desiredWs)

	// Time.every — keep the existing goroutine shape verbatim.
	if everyLeaf == nil {
		return
	}
	sub := *everyLeaf
	interval := time.Duration(sub.ms) * time.Millisecond
	if interval <= 0 {
		return
	}
	// Bug #339: read sess.cancelSub under the same mutex that
	// guards its mutation. Without this the race detector flags a
	// read/write race between this snapshot and a concurrent
	// setupSubscriptions's close+reassign. The captured `cancel`
	// is local to the goroutine for the rest of its lifetime, so
	// the lock can be released immediately after the load.
	sess.cancelSubMu.Lock()
	cancel := sess.cancelSub
	sess.cancelSubMu.Unlock()
	// Cycle 3 P36 / Gap C4: also listen on the session-wide terminal
	// `done` channel so the Tick goroutine exits when the session is
	// evicted from its Store. `cancelSub` alone is insufficient
	// because it's recreated by every setupSubscriptions call — a
	// session deleted BETWEEN dispatches kept this goroutine alive
	// pushing to an unread `sseCh` for the lifetime of the process.
	// A `nil` done (test-constructed sessions that never enter a Store)
	// is safe: select on a nil channel blocks forever.
	done := sess.done
	toMsg := sub.toMsg
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case <-done:
				return
			case t := <-ticker.C:
				sess.mu.Lock()
				msg := toMsg
				// If toMsg is a function, call it with current time millis
				if isFunc(msg) {
					msg = sky_call(toMsg, t.UnixMilli())
				}
				// Capture lastShippedBody BEFORE dispatch so we can
				// detect a tick whose update produced a view that
				// byte-equals what the client already has (the
				// typical Time.every shape — heartbeat polling,
				// once-per-second refresh, etc.). Suppression lives
				// at the SSE callsite (not inside dispatch) because
				// the HTTP /_sky/event response path needs the body
				// to compute structural patches; only the SSE tick
				// can safely silence-drop when the view didn't move.
				prevShipped := sess.lastShippedBody
				// Cycle 3 P50a / Gap C11: capture prevTree BEFORE
				// dispatch so the structural diff can run against
				// the tree the client last saw.
				prevTreeBeforeDispatch := sess.prevTree
				body := app.dispatch(sess, msg)
				newTreeAfterDispatch := sess.prevTree
				// Cycle 3 P41 / Gap C6: snapshot under the lock, then
				// release before the JSON marshal. Time.every ticks
				// on every session that subscribes — the lock-held
				// marshal previously serialised through sess.mu at
				// every interval, amplifying contention on busy
				// sessions. Advancing lastShippedBody stays under
				// the lock so the next tick's prevShipped read sees
				// the up-to-date value.
				var snap frameSnapshot
				var patches []Patch
				var haveFrame bool
				if body != "" && body != prevShipped {
					snap = sess.prepareFrameSnapshot(body)
					sess.lastShippedBody = body
					if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
						// Cycle 3 P50a / Gap C11: structural diff
						// for Time.every ticks — the largest win,
						// because ticks fire periodically without
						// user interaction so server-driven body
						// shipping previously hit every connected
						// session at every interval. clientState
						// nil: the SSE tick has no fresh inputState
						// from the client.
						patches = diffTrees(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
					}
					haveFrame = true
				}
				sess.mu.Unlock()
				// Suppress SSE write when the tick didn't change
				// the view — prevents Time.every from pushing an
				// identical HTML frame every interval.
				if !haveFrame {
					continue
				}
				// Cycle 3 P50a / Gap C11: chooseSSEFrame picks
				// event:patches vs event:patch per render.
				frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
				select {
				case sess.sseCh <- frame:
				default:
					// Cycle 3 P42 / Gap C14: Time.every tick fired
					// but the SSE consumer is wedged or slow; drop
					// + count. Next tick's view-equality check (or
					// the next user dispatch) supersedes anyway.
					recordSseDrop(sess.sid)
				}
			}
		}
	}()
}

// applyTopicSubsDiff computes the diff between the session's current
// pub/sub subscription set and the desired set from this dispatch's
// `subscriptions model` evaluation, then:
//
//   - cancels every "removed" topic — releases its broker refcount,
//     signals the subscriber goroutine to exit.
//   - opens new broker subscriptions for "added" topics — spawns a
//     subscriber goroutine for each.
//   - leaves the intersection (topics in both old and desired)
//     untouched — the existing channel + goroutine keep running so
//     no broadcast falls in the cancel/re-subscribe gap (design
//     doc §4.1 diff-mode requirement).
//
// Mutates sess.activeSubs in place — protected by sess.activeSubsMu
// (NOT sess.mu, so a concurrent subscriber dispatch holding sess.mu
// + calling setupSubscriptions doesn't recurse on the registration
// lock).
//
// `desired` is nil-safe — a nil map signals "no subscriptions" and
// the diff cancels every existing entry, which is exactly what
// setupSubscriptions needs when app.subscriptions is nil OR returns
// Sub.none.
//
// Cycle 3 P48 / docs/skylive/pubsub-design.md §4.1.
func (app *liveApp) applyTopicSubsDiff(sess *liveSession, desired map[string]subT) {
	// Snapshot old + compute the diff under the lock; release before
	// invoking cancel funcs OR Subscribe / spawning goroutines so a
	// slow Subscribe path doesn't stall every other dispatcher on
	// the same session's activeSubs.
	sess.activeSubsMu.Lock()
	desiredAny := make(map[string]any, len(desired))
	for k := range desired {
		desiredAny[k] = struct{}{}
	}
	old := sess.activeSubs
	added, removed := diffSubscriptions(old, desiredAny)

	// Mutate the registration map in place under the lock — every
	// reader (markDone, the next applyTopicSubsDiff) sees a coherent
	// post-diff snapshot.
	removedRegs := make([]*subRegistration, 0, len(removed))
	for _, topic := range removed {
		reg := old[topic]
		if reg == nil {
			continue
		}
		removedRegs = append(removedRegs, reg)
		delete(sess.activeSubs, topic)
	}

	// Open new subscriptions — broker handle + per-topic goroutine.
	// We seed the activeSubs entries BEFORE releasing the lock so a
	// concurrent markDone snapshot sees the new registrations and
	// cancels them; without that ordering an added topic could
	// linger after markDone fires.
	type spawnEntry struct {
		reg   *subRegistration
		gDone <-chan struct{}
	}
	var spawn []spawnEntry
	if app.topics != nil {
		if sess.activeSubs == nil && len(added) > 0 {
			sess.activeSubs = make(map[string]*subRegistration, len(added))
		}
		for _, topic := range added {
			leaf := desired[topic]
			// Register with the session sid as ownerSid so the broker
			// can self-suppress on `Cmd.publishNoEcho` /
			// `PubSub.publishNoEcho` (issue #359). Legacy callers
			// who route through the bare Subscribe path are
			// unaffected — empty ownerSid never matches a non-empty
			// Origin.
			ch, brokerCancel := app.topics.SubscribeWithOwner(topic, sess.sid)
			// Wire the per-goroutine done channel HERE — before
			// releasing the lock + spawning — so the cancel func
			// stored in subRegistration is final + race-free
			// once the goroutine starts. A diff-mode cancel +
			// the loop's exit are both driven by the same closure.
			gDone := make(chan struct{})
			var gDoneOnce sync.Once
			wrappedCancel := func() {
				brokerCancel()
				gDoneOnce.Do(func() { close(gDone) })
			}
			reg := &subRegistration{
				topic:  topic,
				ch:     ch,
				cancel: wrappedCancel,
				toMsg:  leaf.toMsg,
			}
			sess.activeSubs[topic] = reg
			spawn = append(spawn, spawnEntry{reg: reg, gDone: gDone})
		}
	}
	sess.activeSubsMu.Unlock()

	// Cancel removed AFTER releasing the lock — the broker's cancel
	// can briefly contend on its own mutex; doing it under our lock
	// would stall a concurrent markDone.
	for _, reg := range removedRegs {
		if reg.cancel != nil {
			reg.cancel()
		}
	}

	// Spawn subscriber goroutines AFTER releasing the lock — go's
	// runtime can occasionally schedule a worker thread eagerly, so
	// keeping the registration lock during goroutine creation would
	// extend the critical section unnecessarily.
	for _, s := range spawn {
		parentCtx := CurrentTraceContext()
		go app.runSubscriberLoop(sess, s.reg, s.gDone, parentCtx)
	}
}

// runSubscriberLoop is the subscriber goroutine for one pub/sub
// topic registration. Cycle 3 P48 / docs/skylive/pubsub-design.md
// §3.3 + §4.3.
//
// Receives SessionEvents from the broker, decodes each via the
// user-supplied `toMsg : any -> Msg`, dispatches the Msg through
// app.dispatch (the same path Cmd.perform completions take), and
// ships an SSE frame stamped with the broadcast event's globalSeq
// so subscribers and the publisher see a consistent broadcast
// ordering at the wire layer.
//
// Exit conditions:
//
//   - `gDone` is closed by the reg.cancel func (which the caller
//     stores on the registration in applyTopicSubsDiff so both
//     diff-mode cancel + markDone trigger goroutine exit through
//     the same channel).
//   - `sess.done` fires — terminal session teardown (markDone). A
//     nil sess.done (test-constructed sessions) parks forever on
//     that arm; cancellation via gDone is the only signal.
//   - `reg.ch` closes — reserved for v0.16+ cross-process brokers
//     that close on backbone teardown; the in-process default
//     never closes (design doc §3.1).
func (app *liveApp) runSubscriberLoop(sess *liveSession, reg *subRegistration, gDone <-chan struct{}, parentCtx context.Context) {
	sessDone := sess.done

	RunWithTraceContext(parentCtx, func() {
		for {
			select {
			case <-gDone:
				return
			case <-sessDone:
				// Defensive — sessDone is nil for test-constructed
				// sessions; select on a nil channel blocks forever
				// so this arm is dormant in that case.
				return
			case ev, open := <-reg.ch:
				if !open {
					return
				}
				app.runSubscriberDispatch(sess, reg.toMsg, ev)
			}
		}
	})
}

// runSubscriberDispatch decodes one SessionEvent into a Msg via the
// user-supplied `toMsg` decoder and routes the dispatch + SSE frame
// production. Pulled out of runSubscriberLoop so the per-event work
// has its own scope for the panic-recover discipline.
//
// The decoder is wrapped in a defer-recover so a panicking decoder
// consumes the event without crashing the session (design doc §4.3
// "Open: what happens if the decoder panics?" — log + swallow).
func (app *liveApp) runSubscriberDispatch(sess *liveSession, toMsg any, ev SessionEvent) {
	var msg any
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr,
					"[sky.live] pub/sub decoder panic, dropping event topic=%q: %v\n%s\n",
					ev.Topic, r, debug.Stack())
				msg = nil
			}
		}()
		msg = sky_call(toMsg, ev.Payload)
	}()
	if msg == nil {
		return
	}

	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTreeBeforeDispatch := sess.prevTree
	body := app.dispatch(sess, msg)
	newTreeAfterDispatch := sess.prevTree
	var snap frameSnapshot
	var patches []Patch
	var haveFrame bool
	if body != "" && body != prevShipped {
		// Stamp the broadcast event's globalSeq onto the snapshot so
		// the SSE envelope carries it. The client guards on
		// __skyLastAppliedGlobalSeq independently of the per-session
		// localSeq — see live.go's frame snapshot path + the wire
		// envelope (Cycle 3 P47).
		snap = sess.prepareFrameSnapshotWithGlobalSeq(body, ev.GlobalSeq)
		sess.lastShippedBody = body
		if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
			patches = diffTrees(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	if !haveFrame {
		return
	}
	frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
	select {
	case sess.sseCh <- frame:
	default:
		// Channel full — broadcast frame drops are surfaced through
		// the same sky_live_sse_drops_total counter; the next user
		// dispatch supersedes anyway (design doc §6.1).
		recordSseDrop(sess.sid)
	}
}

// ═════════════════════════════════════════════════════════════════════
// Http.Stream.chunks subscriber wiring (Cycle 4 HS)
// ═════════════════════════════════════════════════════════════════════

// applyStreamSubsDiff opens drain goroutines for newly-added stream
// subscriptions, cancels removed ones, and leaves the intersection
// untouched. Mirrors applyTopicSubsDiff structurally — diff-mode +
// dedicated mutex (activeStreamSubsMu, NOT sess.mu) so the drain
// goroutine's dispatch path can take sess.mu without recursing.
func (app *liveApp) applyStreamSubsDiff(sess *liveSession, desired map[int64]subT) {
	sess.activeStreamSubsMu.Lock()
	desiredAny := make(map[int64]any, len(desired))
	for k := range desired {
		desiredAny[k] = struct{}{}
	}
	old := sess.activeStreamSubs
	added, removed := diffStreamSubs(old, desiredAny)

	removedRegs := make([]*streamSubReg, 0, len(removed))
	for _, id := range removed {
		reg := old[id]
		if reg == nil {
			continue
		}
		removedRegs = append(removedRegs, reg)
		delete(sess.activeStreamSubs, id)
	}

	type spawnEntry struct {
		reg   *streamSubReg
		sh    *streamHandle
		gDone <-chan struct{}
	}
	var spawn []spawnEntry
	if sess.activeStreamSubs == nil && len(added) > 0 {
		sess.activeStreamSubs = make(map[int64]*streamSubReg, len(added))
	}
	for _, id := range added {
		leaf := desired[id]
		// Look up the handle the user already opened via
		// Http.Stream.open. If the lookup fails the user passed an
		// unknown / stale id — silently skip (no drain goroutine,
		// no registration) so the next dispatch can pick up a
		// freshly-opened stream without an orphan reg.
		sh := lookupStream(sess, id)
		if sh == nil {
			continue
		}
		gDone := make(chan struct{})
		var gDoneOnce sync.Once
		wrappedCancel := func() {
			gDoneOnce.Do(func() { close(gDone) })
		}
		reg := &streamSubReg{
			streamID: id,
			toMsg:    leaf.toMsg,
			cancel:   wrappedCancel,
		}
		sess.activeStreamSubs[id] = reg
		spawn = append(spawn, spawnEntry{reg: reg, sh: sh, gDone: gDone})
	}
	sess.activeStreamSubsMu.Unlock()

	for _, reg := range removedRegs {
		if reg.cancel != nil {
			reg.cancel()
		}
	}

	for _, s := range spawn {
		parentCtx := CurrentTraceContext()
		go app.runStreamSubscriberLoop(sess, s.reg, s.sh, s.gDone, parentCtx)
	}
}

// runStreamSubscriberLoop is the drain goroutine for one
// Http.Stream.chunks subscription. Reads streamEvents from sh.ch,
// constructs the ChunkEvent ADT, decodes via the user's `toMsg`,
// dispatches the resulting Msg through app.dispatch (the same path
// Cmd.perform completions + topic subscribers take).
//
// Locked default #3: drains up to `streamDrainBatchMax` events per
// pass before yielding via a sleep-zero, so one fast stream can't
// starve other Subs on the same session.
//
// Exit conditions mirror runSubscriberLoop:
//
//   - `gDone` closed — diff-mode cancel OR markDone.
//   - `sess.done` closed — terminal session teardown.
//   - sh.done closed — Http.Stream.close fired (HttpStream_close
//     OR spool body-EOF + error path). We forward any final
//     events queued on sh.ch before exiting (consumer sees Done
//     OR Errored as the last Msg, then the goroutine retires).
func (app *liveApp) runStreamSubscriberLoop(sess *liveSession, reg *streamSubReg, sh *streamHandle, gDone <-chan struct{}, parentCtx context.Context) {
	sessDone := sess.done

	// Stamp BOTH the trace context AND the session on this goroutine
	// so toMsg-invoked kernels (Http.Stream.close / Http.Stream.open
	// from inside Chunked handlers) can resolve currentLiveSession()
	// and register / unregister on the owning session. Mirrors the
	// runPerform stamping pattern.
	RunWithTraceContext(parentCtx, func() {
		runWithLiveSession(sess, func() {
			for {
			// Locked default #3: drain up to streamDrainBatchMax
			// events per pass. The batch loop reads non-blocking
			// from sh.ch so a partially-full channel doesn't stall
			// a yield. After batchMax iterations OR an empty
			// channel, fall back to the blocking select below.
			drained := 0
			for drained < streamDrainBatchMax {
				select {
				case ev, open := <-sh.ch:
					if !open {
						return
					}
					app.runStreamSubscriberDispatch(sess, reg.toMsg, ev)
					drained++
					if ev.kind != streamChunkEv {
						// Done / Errored is terminal — retire the
						// goroutine; the consumer's handler can
						// call Http.Stream.close to unregister.
						return
					}
				default:
					goto blocking
				}
			}
		blocking:
			select {
			case <-gDone:
				return
			case <-sessDone:
				return
			case ev, open := <-sh.ch:
				if !open {
					return
				}
				app.runStreamSubscriberDispatch(sess, reg.toMsg, ev)
				if ev.kind != streamChunkEv {
					return
				}
			}
		}
		})
	})
}

// runStreamSubscriberDispatch_debugCounter — atomic counter of how
// many chunks we've dispatched, for SKY_STREAM_DEBUG tracing.
var runStreamSubscriberDispatch_debugCounter atomic.Int64

// runStreamSubscriberDispatch decodes one streamEvent into a Msg via
// the user-supplied `toMsg` decoder and routes the dispatch + SSE
// frame production. Mirrors runSubscriberDispatch (pub/sub) — wraps
// the decoder in defer-recover so a panicking decoder consumes the
// event without crashing the session.
func (app *liveApp) runStreamSubscriberDispatch(sess *liveSession, toMsg any, ev streamEvent) {
	if streamDebug {
		n := runStreamSubscriberDispatch_debugCounter.Add(1)
		fmt.Fprintf(os.Stderr, "[sky.stream-drain] #%d ev.kind=%d entering dispatch\n", n, ev.kind)
		defer fmt.Fprintf(os.Stderr, "[sky.stream-drain] #%d ev.kind=%d exit dispatch\n", n, ev.kind)
	}
	chunkVal := buildChunkEventValue(ev)
	if chunkVal == nil {
		return
	}
	var msg any
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr,
					"[sky.stream] chunk decoder panic, dropping event kind=%d: %v\n%s\n",
					ev.kind, r, debug.Stack())
				msg = nil
			}
		}()
		msg = sky_call(toMsg, chunkVal)
	}()
	if msg == nil {
		return
	}

	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTreeBeforeDispatch := sess.prevTree
	body := app.dispatch(sess, msg)
	newTreeAfterDispatch := sess.prevTree
	var snap frameSnapshot
	var patches []Patch
	var haveFrame bool
	if body != "" && body != prevShipped {
		snap = sess.prepareFrameSnapshot(body)
		sess.lastShippedBody = body
		if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
			patches = diffTrees(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	if !haveFrame {
		return
	}
	frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
	select {
	case sess.sseCh <- frame:
	default:
		recordSseDrop(sess.sid)
	}
}

// handleSSE: Server-Sent Events endpoint. Pushes view patches as they arrive.
//
// Reverse-proxy hardening:
//   - X-Sky-Live response header lets the client distinguish a real
//     Sky.Live response from a proxy-rewritten error page (e.g. some
//     edges turn upstream 502 into 200 + HTML body, which without the
//     marker would silently look like a successful but empty SSE).
//   - X-Accel-Buffering: no asks Nginx / Cloudflare / Vercel / fly.io
//     edges to disable response buffering for this stream.
//   - 2 KB padding comment up front defeats residual proxy buffers
//     (some won't honour X-Accel-Buffering; the SSE spec recommends
//     >2 KB initial chunk).
//   - "hello" event with a protocol version + sid lands as a
//     handshake; client treats absence-of-hello within helloTimeoutMs
//     as a wedge and force-reconnects.
//   - Periodic "heartbeat" event every sseHeartbeatInterval keeps the
//     watchdog satisfied and surfaces silently-dropped connections
//     (proxy holds socket open but no data flows) within 2× the
//     interval.
func (app *liveApp) handleSSE(w http.ResponseWriter, r *http.Request) {
	sid := ""
	cookieName := app.cookieName
	if cookieName == "" {
		cookieName = "sky_sid"
	}
	if c, err := r.Cookie(cookieName); err == nil {
		sid = c.Value
	}
	if sid == "" {
		w.Header().Set("X-Sky-Live", "1")
		http.Error(w, "no session", 400)
		return
	}
	sess, ok := app.store.Get(sid)
	if !ok {
		// X-Sky-Live: 1 marks this as a real Sky.Live response (not a
		// proxy-rewritten 404). The client uses this signal in
		// __skyForceReopenSSE's probe to decide "session gone → hard
		// page reload" vs "transient network error → keep retrying".
		// Critical for the memory-store-after-restart case (e.g. user
		// flipped sky.toml [live] store from sqlite to memory, watch
		// rebuilt, every browser session is now invalid).
		w.Header().Set("X-Sky-Live", "1")
		http.Error(w, "session not found", 404)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Sky-Live", "1")
	flusher, _ := w.(http.Flusher)

	// Padding line (≥2 KB of `:` comment chars + newlines) primes
	// proxy buffers that ignore X-Accel-Buffering. Sent BEFORE the
	// hello event so by the time hello arrives the proxy has already
	// flushed past its threshold.
	pad := make([]byte, 0, 2050)
	pad = append(pad, ':', ' ')
	for i := 0; i < 2048; i++ {
		pad = append(pad, '.')
	}
	pad = append(pad, '\n', '\n')
	if _, err := w.Write(pad); err != nil {
		return
	}
	// Handshake. v=1 lets future protocol versions tighten the
	// handshake (e.g. require an ack from the client) without
	// breaking older browsers. The sid echoes back the cookie so the
	// client can sanity-check it landed on the right session.
	helloPayload, _ := json.Marshal(map[string]any{
		"v":   1,
		"sid": sid,
		"ts":  time.Now().UnixMilli(),
	})
	if _, err := fmt.Fprintf(w, "event: hello\ndata: %s\n\n", helloPayload); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Reconnect-resync: every fresh SSE connection re-renders the current
	// view and pushes it as a full-body frame. Without this, a binary
	// restart (deploy / `sky watch` rebuild) plus a persistent session
	// store (sqlite / redis / postgres / firestore) leaves the browser's
	// DOM stuck on the old view code — nothing ever pushes through
	// sess.sseCh until the user dispatches a Msg, because subscriptions
	// fire from goroutines launched by the OLD process and aren't
	// recreated on session reload. The cost is one HTML body per SSE
	// connect (~10-50 KB typical); the existing input-authority +
	// __skyReplaceHTMLPreservingFocus rules keep the swap UX-neutral
	// (uncontrolled inputs preserved, focused element spliced).
	//
	// Skips when sess.model is nil, which only happens before
	// handleInitial has run for this session (defensive — the cookie
	// path normally pre-creates the session before SSE opens).
	sess.mu.Lock()
	if sess.model != nil {
		// Recover from any panic in view() so a bad render doesn't tear
		// down the SSE connection. The recovered SSE just enters its
		// for-select loop with the legacy prevTree / lastComputedBody /
		// lastShippedBody untouched.
		//
		// Cycle 3 P41 / Gap C6: snapshot under sess.mu, then release
		// the lock BEFORE the JSON marshal + HTTP write. The previous
		// shape held the mutex for the full render + marshal + write,
		// blocking every concurrent dispatcher on this session. The
		// for-select loop below runs in this same goroutine so there
		// is no race against sseCh-fed frames during the write; seq
		// ordering is preserved because nextLocalSeq runs inside the
		// lock-held prepareFrameSnapshot.
		var snap frameSnapshot
		var haveSnap bool
		func() {
			// v0.16.21: defer/recover absorbed into safeViewCall.
			// Previously this was a bare `defer func() { _ = recover() }()`
			// that silently swallowed panics with no log — admins couldn't
			// see why frames started looking wrong. safeViewCall emits
			// structured logs + renders a recoverable error notice.
			vn, _ := app.safeViewCall(sess.model)
			assignSkyIDs(&vn, app.skyIDPrefixOrDefault())
			applyStyleInjections(&vn)
			sess.handlers = map[string]any{}
			body := renderVNode(vn, sess.handlers)
			// Reconnect-resync writes the resync frame DIRECTLY to
			// the SSE response writer below — so this body is both
			// just-computed AND just-shipped, and the next tick's
			// suppression must compare against it.
			sess.commitRender(&vn, body)
			sess.lastShippedBody = body
			snap = sess.prepareFrameSnapshot(body)
			haveSnap = true
		}()
		sess.mu.Unlock()
		if haveSnap {
			frame := encodeSSEFrameFromSnapshot(snap)
			escaped := strings.ReplaceAll(frame, "\n", "\\n")
			_, _ = fmt.Fprintf(w, "event: patch\ndata: %s\n\n", escaped)
			if flusher != nil {
				flusher.Flush()
			}
		}
		// Persist the rebuilt prevTree + lastComputedBody +
		// lastShippedBody so future events diff against the
		// new-binary view and don't fall back to full-body.
		app.store.Set(sid, sess)
	} else {
		sess.mu.Unlock()
	}

	// Heartbeat ticker. Interval is intentionally LESS than the
	// client's heartbeat-timeout (35s) by a factor of 2 so a single
	// dropped frame doesn't trip the wedge detector. 15s is a
	// pragmatic mid-point between battery / data cost on mobile and
	// fast detection of a wedged connection. Test code can override
	// via the package-level sseHeartbeatInterval var.
	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case fr := <-sess.sseCh:
			// Escape newlines for SSE data lines. Cycle 3 P50a /
			// Gap C11: the event name now travels with the frame —
			// producers choose `event: patches` (structural diff)
			// or `event: patch` (legacy full body) via
			// chooseSSEFrame. Both consumers exist on the client:
			// the legacy `patch` listener is unchanged, and P50b
			// adds the `patches` listener that routes through
			// __skyApplyPatches.
			ev := fr.event
			if ev == "" {
				ev = "patch"
			}
			escaped := strings.ReplaceAll(fr.data, "\n", "\\n")
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, escaped); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case t := <-heartbeat.C:
			if _, err := fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":%d}\n\n", t.UnixMilli()); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func sessionID(r *http.Request, w http.ResponseWriter, ttl time.Duration) string {
	return sessionIDNamed(r, w, ttl, "sky_sid")
}

// sessionIDNamed is the per-app cookie-name-aware session ID resolver.
// Reads / writes the named cookie instead of the hard-coded "sky_sid".
// v0.16.1 PR10: sub-apps mounted via MountLiveSubAppInProcess use a
// distinct name so their cookie doesn't collide with the parent app's
// on the same origin.
func sessionIDNamed(r *http.Request, w http.ResponseWriter, ttl time.Duration, cookieName string) string {
	if cookieName == "" {
		cookieName = "sky_sid"
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	b := make([]byte, 16)
	rand.Read(b)
	sid := hex.EncodeToString(b)
	// Persistent cookie keyed to the session-store TTL so the cookie
	// survives tab-close + browser-restart up to the same window the
	// stored session is valid for.  Previously this was a session
	// cookie (no MaxAge) — browsers that drop session cookies on
	// last-tab-close (Chrome with "continue where you left off"
	// disabled, some Safari configurations) would invalidate the
	// cookie immediately, forcing `init` to fire on every reopen and
	// destroying the user's Model state even though the sqlite /
	// redis / postgres backing store still had it.  MaxAge matches
	// the store TTL so a server-side expiry and the cookie expiry
	// converge to the same time-of-death.
	maxAge := int(ttl.Seconds())
	if maxAge <= 0 {
		maxAge = 30 * 60 // 30 min sane default
	}
	// SameSite: when SKY_LIVE_FRAME_ANCESTORS opts this deploy into
	// being iframed cross-origin (e.g. a control plane's preview
	// pane), the browser would silently drop a Lax-default cookie on
	// every iframe request — Sky.Live's SSE + POST loop would
	// reconnect-and-reset every few seconds. None+Secure lets the
	// cookie ride along, and the existing CSRF check still gates
	// state-mutating POSTs. Outside that mode keep Lax (the right
	// floor for top-level nav + form posts on a same-origin app).
	sameSite, secure := http.SameSiteLaxMode, false
	if crossOriginIframeMode() {
		sameSite, secure = http.SameSiteNoneMode, true
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   maxAge,
		SameSite: sameSite,
		Secure:   secure,
	})
	return sid
}

// crossOriginIframeMode reports whether SKY_LIVE_FRAME_ANCESTORS is
// set — i.e. this deploy expects to be embedded by a different origin
// (typically a control plane's app-preview iframe). When true, the
// session + CSRF cookies must be SameSite=None; Secure to survive the
// browser's cross-site cookie policy. The CSP `frame-ancestors`
// directive set in setSecurityHeaders is the orthogonal gate that
// scopes WHICH origins may embed.
func crossOriginIframeMode() bool {
	return os.Getenv("SKY_LIVE_FRAME_ANCESTORS") != ""
}

// liveBannerConfig collects the <PREFIX>_LIVE_* env vars that
// influence the connection-status banner so they can be templated
// into the init script. Each var has a sensible default; users
// override via shell env or .env.
//
// Reconnecting / Offline are user-facing strings shown in the banner
// when the connection is degraded. Defaults are English; override via
// the `status` field on the Live.app config (see resolveBannerStrings)
// to localise the chrome in the app's language. Strings are templated
// in JSON-quoted form so any character is safe (newlines, quotes,
// non-ASCII, emoji); the DOM uses textContent, not innerHTML, so XSS
// is structurally impossible.
type liveBannerConfig struct {
	Enabled        bool
	BaseMs         int
	MaxMs          int
	MaxAttempts    int
	QueueMax       int
	Reconnecting   string
	Offline        string
	HelloTimeoutMs int
	HeartbeatTtlMs int
}

// sseHeartbeatInterval is the cadence at which handleSSE emits a
// `event: heartbeat\ndata: {"ts":N}\n\n` frame. Exposed as a var so
// tests can dial it down to milliseconds; production code never
// rewrites it.
var sseHeartbeatInterval = 15 * time.Second

const (
	defaultReconnectingMsg = "Reconnecting…"
	defaultOfflineMsg      = "Connection lost — refresh to retry"
	// Client must see the server's "hello" event within this many ms
	// of EventSource.open or it treats the connection as wedged
	// (proxy-rewritten 200-OK or buffered SSE response). 8s is well
	// past round-trip on slow mobile but tight enough that a stuck
	// proxy is detected before the user notices.
	defaultHelloTimeoutMs = 8000
	// Client treats absence-of-events for this many ms as a wedged
	// connection. Server's heartbeat fires every 15s, so 35s is just
	// over 2× the heartbeat interval — survives one missed heartbeat
	// (network blip, GC pause) but trips quickly on a real wedge.
	defaultHeartbeatTtlMs = 35000
)

func loadLiveBannerConfig() liveBannerConfig {
	cfg := liveBannerConfig{
		Enabled:        true,
		BaseMs:         500,
		MaxMs:          16000,
		MaxAttempts:    10,
		QueueMax:       50,
		Reconnecting:   defaultReconnectingMsg,
		Offline:        defaultOfflineMsg,
		HelloTimeoutMs: defaultHelloTimeoutMs,
		HeartbeatTtlMs: defaultHeartbeatTtlMs,
	}
	// <PREFIX>_LIVE_BANNER=off disables the banner entirely (still
	// queues + retries POSTs — just no chrome). Useful when an app
	// wants to render its own connection UI in the user's view.
	if v := skyGetenv("LIVE_BANNER"); v == "off" || v == "0" || v == "false" {
		cfg.Enabled = false
	}
	if n, ok := parsePositiveInt(skyGetenv("LIVE_RETRY_BASE_MS")); ok {
		cfg.BaseMs = n
	}
	if n, ok := parsePositiveInt(skyGetenv("LIVE_RETRY_MAX_MS")); ok {
		cfg.MaxMs = n
	}
	if n, ok := parsePositiveInt(skyGetenv("LIVE_RETRY_MAX_ATTEMPTS")); ok {
		cfg.MaxAttempts = n
	}
	if n, ok := parsePositiveInt(skyGetenv("LIVE_QUEUE_MAX")); ok {
		cfg.QueueMax = n
	}
	if n, ok := parsePositiveInt(skyGetenv("LIVE_HELLO_TIMEOUT_MS")); ok {
		cfg.HelloTimeoutMs = n
	}
	if n, ok := parsePositiveInt(skyGetenv("LIVE_HEARTBEAT_TTL_MS")); ok {
		cfg.HeartbeatTtlMs = n
	}
	return cfg
}

// resolveBannerStrings overlays the optional `status` record from a
// Live.app config onto the env-defaulted banner config. The kernel
// signature for Live.app is open via the appExt row variable, so adding
// `status = { reconnecting = "...", offline = "..." }` to a user's app
// type-checks without any signature change. Missing fields fall back
// to the defaults already in `cfg` — partial overrides are fine, and
// a typo just silently misses (a closed-record check would force users
// who only want one string in their language to write both, which is
// a worse trade-off than the typo cost).
func resolveBannerStrings(cfg liveBannerConfig, app any) liveBannerConfig {
	status := Field(app, "Status")
	if status == nil {
		return cfg
	}
	if s := stringField(status, "Reconnecting"); s != "" {
		cfg.Reconnecting = s
	}
	if s := stringField(status, "Offline"); s != "" {
		cfg.Offline = s
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

// ─── SSE outbound channel buffer (Cycle 3 P42 / Gap C14) ───────────
//
// `sess.sseCh` is the buffered chan<-string every SSE-producing site
// pushes frames onto. Its capacity gates how many in-flight frames
// can queue between the producer (dispatchBatched / runPerformBody /
// Time.every tick) and the consumer (handleSSE for-select). When
// the buffer fills, each writer's `select { default: }` arm DROPS
// the frame silently — a correctness loss the client never sees
// (until the next dispatch ships a fresher view that overrides it).
//
// Historical default: hardcoded 16, which is generous for steady
// state but a 5-second burst at 100 ms/tick under a Time.every
// subscription can fill it. The Cycle 3 audit (Gap C14) called for
// two changes: (i) make the capacity tunable via env; (ii) export a
// Prometheus counter so operators can observe drop rate.
//
// Env: `SKY_LIVE_SSE_BUFFER` (subject to the [env] prefix override).
// Default 16; clamp to [1, 1024]. Re-read via the
// `onEnvPrefixChange` hook so a compiler-generated
// `rt.SetEnvPrefix(...)` mid-init() picks up a prefixed value the
// init() block also set.
//
// Metric: `sky_live_sse_drops_total{session=<sid>}` increments at
// each `default:` arm. The per-session label is high-cardinality
// by construction; telemetry/store.go caps total label combinations
// at 10k per metric — past that, new sessions silently miss the
// label. Acceptable: the operator's interest is "are drops
// happening?" (yes/no answered by any non-zero series) and "are
// they concentrated on one session?" (answered by per-session
// labels up to the cap). For deployments expecting >10k sessions
// over the metric horizon, configure the [env] prefix + a
// shorter scrape retention.
const (
	sseChanBufferDefault = 16
	sseChanBufferMin     = 1
	sseChanBufferMax     = 1024
)

var sseChanBuffer = sseChanBufferDefault

// loadSseChanBuffer reads SKY_LIVE_SSE_BUFFER + clamp + assign.
// Idempotent; safe to call from init() + onEnvPrefixChange hooks.
func loadSseChanBuffer() {
	v := skyGetenv("LIVE_SSE_BUFFER")
	if v == "" {
		sseChanBuffer = sseChanBufferDefault
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		// Malformed or non-positive — fall back to default rather
		// than refusing to boot. Matches the pattern of
		// parsePositiveInt callers throughout live.go.
		sseChanBuffer = sseChanBufferDefault
		return
	}
	if n < sseChanBufferMin {
		n = sseChanBufferMin
	}
	if n > sseChanBufferMax {
		n = sseChanBufferMax
	}
	sseChanBuffer = n
}

func init() {
	loadSseChanBuffer()
	onEnvPrefixChange(loadSseChanBuffer)
}

// recordSseDrop increments the sky_live_sse_drops_total counter for
// the given session id. Called from the `default:` arm of every
// sseCh writer. `sid` MAY be empty (e.g. test-constructed sessions
// that never enter a Store) — telemetry/store.go handles empty
// label values fine; downstream dashboards group those under a
// single empty-string series.
func recordSseDrop(sid string) {
	telemetry.Default().Inc("sky_live_sse_drops_total", map[string]string{
		"session": sid,
	})
}

// liveJS keeps the historical signature (used by tests + any external
// callers that don't have a liveApp instance). Resolves the env-only
// banner config and forwards to liveJSWithCfg. Production callers go
// through liveJSWithCfg with the app's resolved cfg so the user's
// `status = { reconnecting = ..., offline = ... }` overrides apply.
func liveJS(sid string) string {
	return liveJSWithCfg(sid, loadLiveBannerConfig())
}

// jsString JSON-quotes s for safe embedding as a JS string literal.
// JSON string syntax is a subset of JS; escaped form (\uXXXX for
// non-ASCII) is portable across browsers without depending on the
// containing script's charset declaration.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func liveJSWithCfg(sid string, cfg liveBannerConfig) string {
	// Backwards-compat shim — older callers (tests, external probes)
	// pre-date the Phase 1.2 CSRF wire. Forwards with an empty
	// CSRF token; __skySend will simply not attach the header.
	return liveJSWithCfgAndCsrf(sid, cfg, "")
}

// liveJSWithCfgAndCsrf — the Phase 1.2 entry point that bundles the
// per-session CSRF token into the inlined JS. __skySend reads
// __skyCsrfToken and adds the X-Sky-Csrf header on every POST so
// the CSRF middleware accepts the request. Zero user code needed —
// AI-written Sky apps are CSRF-protected by construction.
// normaliseBasePath cleans up a SKY_LIVE_BASE_PATH value so the
// JS / meta-tag / proxy mount agree on a single canonical form.
// Rules: trim whitespace + trailing slashes, ensure a leading slash
// if non-empty. Empty input → empty output (root-mount; no prefix).
func normaliseBasePath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, "/")
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}

// pathInBasePath reports whether `path` falls under `basePath`. Both
// inputs already normalised (basePath via normaliseBasePath, path is
// whatever the browser sent). Matches:
//
//   - `path == basePath`                            ← exact (e.g. /_sky/console)
//   - `strings.HasPrefix(path, basePath + "/")`     ← inside subtree
//
// Excluded (so /_sky/consoleX doesn't match /_sky/console):
//
//   - bare-prefix-no-slash like "/_sky/consoleX".
//
// Empty basePath returns false (root-mounted apps don't claim any
// /_sky/ prefix — they're called from the OTHER dispatchRoot branch).
func pathInBasePath(path, basePath string) bool {
	if basePath == "" {
		return false
	}
	if path == basePath {
		return true
	}
	return strings.HasPrefix(path, basePath+"/")
}

// trimBasePathPrefix returns the "logical" path inside a sub-app's
// world by stripping its basePath prefix. Examples (basePath
// `/_sky/console`):
//
//	"/_sky/console"          → "/"
//	"/_sky/console/"         → "/"
//	"/_sky/console/about"    → "/about"
//	"/_sky/console/users/42" → "/users/42"
//	"/other"                 → "/other"   (unchanged when not in subtree)
//
// Empty basePath is the identity. Used by matchAnyRoute + applyRoute
// so sub-app routes stay basePath-agnostic.
func trimBasePathPrefix(path, basePath string) string {
	if basePath == "" {
		return path
	}
	if path == basePath {
		return "/"
	}
	if strings.HasPrefix(path, basePath+"/") {
		out := strings.TrimPrefix(path, basePath)
		if out == "" {
			return "/"
		}
		return out
	}
	return path
}

func liveJSWithCfgAndCsrf(sid string, cfg liveBannerConfig, csrfToken string) string {
	// Backwards-compat shim for callers that pre-date base-path
	// support. Defaults base to "" (root mount).
	return liveJSWithCfgAndCsrfWithBase(sid, cfg, csrfToken, "")
}

func liveJSWithCfgAndCsrfWithBase(sid string, cfg liveBannerConfig, csrfToken, basePath string) string {
	return fmt.Sprintf(`
var __skySid = %q;
var __skyBase = %q;
var __skyCsrfToken = %q;
var __skyBannerEnabled = %t;
var __skyRetryBaseMs = %d;
var __skyRetryMaxMs = %d;
var __skyRetryMaxAttempts = %d;
var __skyEventQueueMax = %d;
var __skyMsgReconnecting = %s;
var __skyMsgOffline = %s;
var __skyHelloTimeoutMs = %d;
var __skyHeartbeatTtlMs = %d;

// ── Input authority protocol state ───────────────────────────
// See docs/skylive/input-authority-protocol.md §Client state.
// Step 2 populates these counters + per-input table on every send
// and response; Step 3 activates the patch filter that reads them;
// Step 4 activates the stale-drop test against __skyLastAppliedSeq.
//
// Cycle 3 P47 (pub/sub global+local seq split — see
// docs/skylive/pubsub-design.md §3.2): __skyLastGlobalSeq is the
// app-wide broadcast counter. The server stamps it onto every
// broadcast-derived SSE frame (event:patches OR event:patch); the
// client dedupes against the largest value already applied so a
// replayed broadcast (e.g. SSE reconnect that re-delivers buffered
// frames) drops at the boundary without mutating state twice. Frames
// from per-session dispatch (the common case) carry globalSeq=0 OR
// omit the field; the guard treats 0 / missing as "no broadcast
// ordering constraint" and never blocks.
var __skyClientSeq = 0;       // monotonic, client-owned; bumped on every __skySend
var __skyLastAppliedSeq = 0;  // server-owned; largest local seq already applied
var __skyLastGlobalSeq = 0;   // server-owned; largest broadcast globalSeq already applied (P47)
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

function __skyIngestSeq(seq, ackInputs, globalSeq) {
  if (typeof seq === "number" && seq > __skyLastAppliedSeq) {
    __skyLastAppliedSeq = seq;
  }
  // Cycle 3 P47: monotonic-applied semantics on the broadcast counter,
  // mirroring the local-seq path. Missing / zero / non-numeric globalSeq
  // is treated as "no broadcast ordering constraint" and ignored.
  if (typeof globalSeq === "number" && globalSeq > __skyLastGlobalSeq) {
    __skyLastGlobalSeq = globalSeq;
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
//
// Cycle 3 P47 (pub/sub global+local seq split — see
// docs/skylive/pubsub-design.md §3.2): broadcast-derived frames also
// carry an OPTIONAL globalSeq. If supplied AND already applied (i.e.
// globalSeq > 0 && globalSeq <= __skyLastGlobalSeq) the frame is
// dropped — a replayed broadcast (e.g. an SSE reconnect re-delivering
// buffered frames) would otherwise mutate state twice. Both guards
// fire independently: a frame is dropped if EITHER counter has already
// passed it; the localSeq guard alone suffices for the legacy
// non-broadcast case (globalSeq omitted / 0 → broadcast guard always
// passes).
function __skyHandleResponse(seq, ackInputs, applyFn, globalSeq) {
  if (typeof seq === "number" && seq > 0 && seq <= __skyLastAppliedSeq) {
    return; // stale — a newer local-seq frame already landed
  }
  if (typeof globalSeq === "number" && globalSeq > 0 && globalSeq <= __skyLastGlobalSeq) {
    return; // stale — a newer broadcast frame already landed
  }
  __skyIngestSeq(seq, ackInputs, globalSeq);
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

// __skyPlaceholderUncontrolled — true when the server-rendered
// element has no authority attribute set (no value/checked/selected,
// no textarea content, no option[selected]). For these the user-
// owned client state is canonical; we splice the live node across
// the swap so the user's typing isn't blanked. See
// docs/skylive/input-authority-protocol.md §I6 (full-body
// preservation).
function __skyPlaceholderUncontrolled(placeholder) {
  if (!placeholder) return false;
  if (placeholder.hasAttribute("value")) return false;
  if (placeholder.hasAttribute("checked")) return false;
  if (placeholder.hasAttribute("selected")) return false;
  var tag = placeholder.tagName;
  if (tag === "TEXTAREA") {
    return (placeholder.textContent || "").length === 0;
  }
  if (tag === "SELECT") {
    return placeholder.querySelectorAll("option[selected]").length === 0;
  }
  // type=file: browsers refuse programmatic value assignment, the
  // user's selection is the only truth — always treat as uncontrolled.
  if (tag === "INPUT" && placeholder.getAttribute("type") === "file") return true;
  return true;
}

// __skyFindPlaceholder — locate a live input's slot in the new tree.
// Prefer sky-id (structurally stable + uniquely keyed). Fall back to
// tag+name only when the live element has no sky-id AND the new tree
// has exactly one match — preventing wrong-input collisions when
// names recur (e.g. multiple address forms with name="line1").
function __skyFindPlaceholder(tmp, live) {
  var sid = live.getAttribute && live.getAttribute("sky-id");
  if (sid) {
    var bySid = tmp.querySelector('[sky-id="' + sid.replace(/"/g, '\\"') + '"]');
    if (bySid) return bySid;
  }
  var name = live.getAttribute && live.getAttribute("name");
  if (!name) return null;
  var tag = live.tagName.toLowerCase();
  var matches = tmp.querySelectorAll(tag + '[name="' + name.replace(/"/g, '\\"') + '"]');
  if (matches.length === 1) return matches[0];
  return null;
}

// __skyReplaceHTMLPreservingFocus — the authoritative swap.
// Drop-in for plain innerHTML assignment that keeps:
//   1. The currently-focused input (.value, IME state, composition
//      buffer, selection range, scroll position).
//   2. EVERY uncontrolled input/textarea/select in the subtree
//      (anything the server didn't render an authority attribute for).
//      Without this, an unfocused password field gets recreated by the
//      innerHTML swap and the user's typed secret is blanked — see
//      Bug 2 in docs/skylive/architecture.md §Input preservation.
// Used by both __skyPatch (full body) and __skyApplyPatches (p.html
// and large p.text patches).
function __skyReplaceHTMLPreservingFocus(container, newHTML) {
  var focused = document.activeElement;
  var focusedInside = focused && focused !== document.body &&
      container.contains(focused) &&
      (focused.tagName === "INPUT" ||
       focused.tagName === "TEXTAREA" ||
       focused.tagName === "SELECT");

  // Parse the new HTML into a detached element so we can splice
  // preserved live nodes into it before committing.
  //
  // Namespace correctness: when the container element is in a foreign-
  // content namespace (SVG or MathML), parsing the new HTML via a
  // plain document.createElement("div") + .innerHTML = ... uses the
  // HTML insertion mode, so element names like <g>, <rect>, <text>
  // (which the diff emits as direct children when it replaces the
  // children of an <svg> element) end up in the XHTML namespace
  // rather than SVG. The elements appear in the DOM but the browser
  // doesn't lay them out as SVG primitives — the canvas silently goes
  // blank after a shape add/remove with no JS error to point at.
  //
  // Range.createContextualFragment parses HTML using the namespace
  // context of the range's container, preserving SVG/MathML element
  // namespaces correctly. The downstream code accepts either an
  // Element or a DocumentFragment via the same .firstChild /
  // .querySelectorAll / .parentNode.replaceChild surface, so no
  // other changes are needed.
  //
  // Repro before this fix: any Sky.Live view that emits an HTML
  // patch at a sky-id pointing at an <svg> element (the diff does
  // this whenever the SVG's children-count changes, or a child
  // tag/kind mismatches between renders) leaves the SVG with HTML-
  // namespaced children. Drawing tools, charts, and apps that swap
  // inline-SVG icon <path> children are the common victims.
  var tmp;
  if (container.namespaceURI && container.namespaceURI !== "http://www.w3.org/1999/xhtml") {
    var range = document.createRange();
    range.selectNodeContents(container);
    tmp = range.createContextualFragment(newHTML);
  } else {
    tmp = document.createElement("div");
    tmp.innerHTML = newHTML;
  }

  // Snapshot focused-state BEFORE any DOM mutation. Selection read
  // throws on some input types, so catch.
  var selStart = null, selEnd = null, scrollTop = 0;
  if (focusedInside) {
    try {
      selStart = focused.selectionStart;
      selEnd   = focused.selectionEnd;
    } catch (_) {}
    scrollTop = focused.scrollTop;
  }

  // Walk the LIVE container's inputs/textareas/selects and decide
  // which ones to splice. The focused element is ALWAYS spliced
  // (active typing wins). Other elements are spliced only when the
  // server-side placeholder is uncontrolled (no value/checked/
  // selected) — i.e. user state is canonical.
  var preservedFocus = null;
  var liveNodes = container.querySelectorAll("input, textarea, select");
  for (var i = 0; i < liveNodes.length; i++) {
    var live = liveNodes[i];
    var placeholder = __skyFindPlaceholder(tmp, live);
    if (!placeholder) continue; // server unmounted: honour the server
    var isFocused = (live === focused);
    if (!isFocused && !__skyPlaceholderUncontrolled(placeholder)) {
      // Controlled field with a server-supplied value — let the
      // server win. Default innerHTML swap will recreate it from
      // placeholder.
      continue;
    }
    // Mirror placeholder attrs (class, type, placeholder, disabled,
    // aria-*, …) onto the live node — except the three authority
    // attrs the user drives. The user's .value / .checked /
    // .selected DOM property survives untouched.
    __skyCopyAttrsExceptAuthority(placeholder, live);
    // Splice: replace the placeholder in tmp with the live node.
    // After this, the live node lives in tmp at the placeholder's
    // slot; the container still references it too (until the swap
    // below). DOM trees are tolerant of this — the upcoming
    // removeChild + appendChild commit moves it cleanly.
    placeholder.parentNode.replaceChild(live, placeholder);
    if (isFocused) preservedFocus = live;
  }

  // Splice IFRAMES across the swap (#568 second loop).  Without
  // this, an HTML-replace patch that touches the iframe's parent
  // subtree (sibling sky-id reorder, structural reorganisation)
  // destroys the live iframe via removeChild and creates a fresh
  // one from the placeholder markup.  The fresh iframe's src
  // triggers a navigation regardless of whether the value matches
  // the live one — every reload re-fires the embedded console's
  // handshake form, opens a new SSE, and pegs the tenant Cloud
  // Run instance.
  //
  // Same splice pattern as inputs.  The iframe's src is treated
  // as USER STATE AUTHORITY (the live document, internal SSE,
  // navigation history, scroll position are owned by the iframe).
  // Placeholder contributes non-authority attrs only — class,
  // style, sandbox, referrerpolicy — via the existing helper
  // (whose authority filter covers value/checked/selected; src
  // isn't in that list, so we strip it from the placeholder
  // before mirroring to avoid the same setAttribute-triggered
  // navigation the patch path's guard prevents).
  var liveFrames = container.querySelectorAll("iframe");
  for (var fi = 0; fi < liveFrames.length; fi++) {
    var liveFr = liveFrames[fi];
    var phFr = __skyFindPlaceholder(tmp, liveFr);
    if (!phFr) continue;
    // SRC-EQUALITY GATE.  sky-id is purely structural (tag +
    // position + form-name) and does NOT encode src.  So two
    // renders that emit <iframe src=A> then <iframe src=B> at
    // the same structural position share a sky-id, and naive
    // splicing would freeze the iframe at src=A forever — every
    // legitimate URL change would silently no-op.  Only splice
    // (preserve the live iframe) when the SERVER's intended src
    // matches the live src.  When they differ, fall through to
    // the default innerHTML path so the live iframe gets
    // destroyed and a fresh one navigates to the new URL.
    var liveSrc = liveFr.getAttribute("src") || "";
    var phSrc = phFr.getAttribute("src") || "";
    if (liveSrc !== phSrc) continue;
    // Strip src from placeholder so __skyCopyAttrsExceptAuthority
    // doesn't write it onto the live iframe (which would navigate
    // it even when the strings already match — assigning src
    // unconditionally re-fetches in some browsers).
    if (phFr.hasAttribute("src")) phFr.removeAttribute("src");
    __skyCopyAttrsExceptAuthority(phFr, liveFr);
    phFr.parentNode.replaceChild(liveFr, phFr);
  }

  // Commit: throw away container's current children (those we didn't
  // splice are stale; spliced ones already moved into tmp), then
  // attach tmp's children. Done.
  while (container.firstChild) container.removeChild(container.firstChild);
  while (tmp.firstChild) container.appendChild(tmp.firstChild);

  // Focus restoration on the SAME node — so .value, IME state,
  // composition buffer survive untouched. removeChild + appendChild
  // drop focus, so we re-set it now.
  if (preservedFocus) {
    try { preservedFocus.focus({preventScroll: true}); } catch (_) {
      try { preservedFocus.focus(); } catch (_) {}
    }
    if (typeof preservedFocus.setSelectionRange === "function" &&
        selStart !== null && selEnd !== null) {
      try { preservedFocus.setSelectionRange(selStart, selEnd); } catch (_) {}
    }
    if (scrollTop) preservedFocus.scrollTop = scrollTop;
  }
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
  __skyRunPaths(root);
  __skyReviveScripts(root);
}

// __skyReviveScripts: browsers DO NOT execute <script> tags inserted
// via innerHTML (or any HTML-string assignment). When Sky.Live
// swaps the body via __skyReplaceHTMLPreservingFocus (sky-nav, full-
// body patches) or applies an attribute/HTML patch via
// __skyApplyPatches, any <script src=...> or inline <script>
// element in the new content is added to the DOM but never
// executed. This breaks any app-level JS bundle injected via the
// Sky-side Ui.html (Html.node "script" [...]) pattern (notably
// sky-editor's Editor.scriptTag).
//
// The fix: walk the new subtree for <script> elements, replace
// each with a freshly-created one carrying a STRICT ALLOWLIST of
// attributes. Freshly-created script nodes execute on insertion.
//
// Security (Cycle 3 audit gap C9 / cycle 2 plan P31):
//   - Attribute copy is filtered through __skyScriptAttrAllowlist.
//     Event-handler attrs (onerror, onload, onclick, …) are NEVER
//     re-emitted — the original unfiltered loop allowed an attacker
//     who controlled WYSIWYG content rendered back into Ui.html to
//     ship <script onerror=alert(1)> and watch the handler fire on
//     the next patch.
//   - Inline script bodies (textContent) are DROPPED unless the
//     element also carries a src= attribute (a same-origin opt-in:
//     Sky-bundled scripts like sky-editor's Editor.scriptTag set
//     src=; user-supplied inline bodies are silently rejected with
//     a console.warn so the misuse is visible during dev).
//   - Rejected scripts STILL get the data-sky-script-revived
//     marker so a subsequent revival pass doesn't reprocess them
//     (i.e. silent-drop is idempotent — no infinite warning storm).
//
// Idempotency: each revived <script> gets a data-sky-script-revived
// attribute; subsequent calls skip it. This prevents the bundle
// from re-loading on every patch (which would re-run any
// DOMContentLoaded handlers and re-fire setInterval-driven
// bootstraps multiple times).
//
// Safety: only matches <script> nodes inside root (the sky-root
// container). Top-level page <script> tags (in <head> or outside
// sky-root) are left alone — they ran on initial load and need
// no revival.
var __skyScriptAttrAllowlist = {
  "src": 1,
  "type": 1,
  "async": 1,
  "defer": 1,
  "integrity": 1,
  "crossorigin": 1,
  "nomodule": 1,
  "referrerpolicy": 1,
  "data-sky-script-revived": 1
};
function __skyReviveScripts(root) {
  if (!root) return;
  var scripts = root.querySelectorAll("script:not([data-sky-script-revived])");
  for (var i = 0; i < scripts.length; i++) {
    var old = scripts[i];
    // Mark the source element revived FIRST so a rejection branch
    // (no-src + inline body) doesn't re-trip on the next pass.
    try { old.setAttribute("data-sky-script-revived", "1"); } catch (_) {}
    var hasSrc = old.hasAttribute("src");
    var hasInline = !!(old.textContent && old.textContent.length > 0);
    // Reject inline-only scripts (no src) — same-origin opt-in via
    // src= is the contract. Console.warn so the misuse is visible
    // during dev; never throws (one bad node mustn't kill the loop).
    if (!hasSrc && hasInline) {
      try {
        if (typeof console !== "undefined" && console.warn) {
          console.warn("[sky.live] script revival rejected an inline <script> without src= (XSS hardening, gap C9). Bundle via src= for Sky-side scripts.");
        }
      } catch (_) {}
      continue;
    }
    var fresh = document.createElement("script");
    // Copy ONLY allowlisted attributes. Event-handler attrs (anything
    // starting with "on…") and any non-allowlisted attribute are
    // silently dropped — see __skyScriptAttrAllowlist.
    var droppedAttrs = null;
    for (var j = 0; j < old.attributes.length; j++) {
      var a = old.attributes[j];
      var n = a.name.toLowerCase();
      if (__skyScriptAttrAllowlist[n] === 1) {
        try { fresh.setAttribute(a.name, a.value); } catch (_) {}
      } else {
        // Capture for a single dev-time warn at the end (a single
        // <script onerror=…> shouldn't fire one warn per attr).
        if (!droppedAttrs) droppedAttrs = [];
        droppedAttrs.push(a.name);
      }
    }
    if (droppedAttrs) {
      try {
        if (typeof console !== "undefined" && console.warn) {
          console.warn("[sky.live] script revival dropped non-allowlisted attrs (XSS hardening, gap C9):", droppedAttrs.join(", "));
        }
      } catch (_) {}
    }
    // Inline body is now ONLY admitted when src= is also present.
    // This stays compatible with <script src=...>// optional inline
    // bootstrapping comment <\/script> patterns; the body is included
    // verbatim, the src= drives the actual execution.
    // (The escaped </ above prevents the literal closing-script tag
    // from terminating the inline JS wrapper at the HTML parser.)
    if (hasSrc && hasInline) {
      fresh.textContent = old.textContent;
    }
    fresh.setAttribute("data-sky-script-revived", "1");
    // Replacing the old node with the fresh one triggers script
    // execution (for src= it fetches + runs; for inline it runs
    // the body).
    old.parentNode.replaceChild(fresh, old);
  }
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
    navigator.sendBeacon(__skyBase + "/_sky/event", blob);
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
  // Phase 1.2 — attach the per-session CSRF token. The server-side
  // middleware (runtime-go/rt/csrf_middleware.go) rejects POSTs
  // without a matching X-Sky-Csrf / __sky_csrf cookie pair. Empty
  // token means CSRF is disabled at the runtime level (sky.toml
  // [security] csrf = false) — header omitted, middleware skipped.
  var headers = {"Content-Type":"application/json"};
  if (__skyCsrfToken) headers["X-Sky-Csrf"] = __skyCsrfToken;
  fetch(__skyBase + "/_sky/event", {
    method: "POST",
    headers: headers,
    body: JSON.stringify(body),
    credentials: "same-origin"
  }).then(function(r){
    if (!r.ok && r.status >= 500) {
      // Server is up but rejecting (502/503/504 from a deploying LB,
      // or 500 from a panic that survived the recover guard). Treat
      // as transient — same retry path as a network failure.
      throw new Error("server " + r.status);
    }
    // Reverse-proxy wedge detection: a real Sky.Live response always
    // carries X-Sky-Live: 1. Without it, we're looking at a proxy-
    // rewritten response (e.g. some edges turn upstream 502 into 200
    // OK with an HTML error page). Applying that as a "patch" would
    // replace the user's DOM with the proxy's error page, so we refuse
    // it and route through the failure path instead.
    //
    // For JSON content-type we keep a backwards-compat shim during
    // rolling deploys: a pre-marker server still returns valid JSON
    // with seq + patches, structurally indistinguishable from the
    // marked form, so accept it. HTML / text responses without the
    // marker are always rejected — those are the proxy-wedge shape.
    var skyMark = r.headers.get("X-Sky-Live");
    var ct = r.headers.get("Content-Type") || "";
    var isJson = ct.indexOf("application/json") >= 0;
    if (skyMark !== "1" && !isJson) {
      throw new Error("non-sky response " + r.status);
    }
    if (isJson) {
      return r.json().then(function(data) {
        // Even JSON is rejected if it lacks the protocol shape (no
        // seq field): some proxies (Cloudflare access denied, fly.io
        // edge errors) return JSON error envelopes with 200 OK.
        if (skyMark !== "1" && (!data || typeof data.seq === "undefined")) {
          throw new Error("non-sky json response");
        }
        __skyLoaderEnd();
        __skyOnPostSuccess();
        if (!data) return;
        __skyHandleResponse(data.seq, data.ackInputs, function() {
          if (data.patches) __skyApplyPatches(data.patches);
        }, data.globalSeq);
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
  // SSE recovery: if the watchdog tore down the EventSource (offline
  // terminal state), a successful POST proves the network is back, so
  // reopen the stream too — otherwise subscriptions and Cmd.perform
  // results would silently not arrive even though clicks work. Cancel
  // any pending reopen-with-backoff and bring it forward.
  if (__skySSE === null) {
    if (__skySseReopenTimer !== null) {
      clearTimeout(__skySseReopenTimer);
      __skySseReopenTimer = null;
    }
    __skyOpenSSE();
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
    __skySetStatus("reconnecting", __skyMsgReconnecting);
  }
}
function __skyScheduleRetry() {
  if (__skyRetryTimer !== null) return;  // already pending
  if (__skyRetryAttempts >= __skyRetryMaxAttempts) {
    __skySetStatus("offline", __skyMsgOffline);
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
  // Open <select> defence: native dropdowns close on ANY DOM mutation
  // inside the open select OR any ancestor that would re-mount it.
  // There's no JS API for "is the dropdown open", so use focus as the
  // conservative proxy: if a SELECT is the active element, treat its
  // subtree (and ancestors that would re-mount it) as off-limits for
  // this patch cycle. The next user interaction (option click, blur)
  // triggers a fresh response and reconciliation. Sibling subtrees
  // and unrelated parts of the DOM apply normally — the dropdown is
  // unaffected. See Bug 3 in docs/skylive/architecture.md.
  var openSel = (document.activeElement && document.activeElement.tagName === "SELECT")
      ? document.activeElement : null;
  for (var i = 0; i < patches.length; i++) {
    var p = patches[i];
    var el = document.querySelector('[sky-id="' + p.id.replace(/"/g, '\\"') + '"]');
    if (!el) continue;
    if (openSel && (el === openSel || el.contains(openSel) || openSel.contains(el))) {
      // Skip: any mutation here would close the dropdown mid-pick.
      continue;
    }
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
      // Cursor preservation: when applying a "value" attr to a
      // focused INPUT or TEXTAREA, snapshot the selection range
      // BEFORE setting .value (which otherwise resets the cursor
      // to the end of the new string). Common case: user clicked
      // into a textarea, paused so their dirty flag cleared, and
      // the server pushes a fresh value via SSE. Without this,
      // the cursor jumps to the end mid-edit. Clamping handles
      // shorter new values (selectionStart > newLen -> newLen).
      var isInputLike = el.tagName === "INPUT" || el.tagName === "TEXTAREA";
      var hadFocus = isInputLike && el === document.activeElement;
      var savedSelStart = null, savedSelEnd = null, savedScrollTop = 0;
      if (hadFocus) {
        try {
          savedSelStart = el.selectionStart;
          savedSelEnd = el.selectionEnd;
        } catch (_) {}
        savedScrollTop = el.scrollTop;
      }
      var valueChanged = false;
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
          // Idempotent setAttribute (#568): some elements re-fetch or
          // re-navigate on ANY assignment to certain attributes, even
          // when the new value is identical to the existing one. The
          // poster child is the iframe src attribute — calling
          // setAttribute with the same value causes the browser to
          // re-navigate the iframe, dropping any SSE / cookie /
          // scroll state inside. Same class: img src refetches,
          // link href rebuilds the stylesheet, script src re-executes
          // (browsers vary). Skipping the no-op write costs one
          // getAttribute compare per attr and rules out a whole bug
          // class. Mirrors the guard in __skyCopyAttrsExceptAuthority.
          if (el.getAttribute(k) !== v) {
            el.setAttribute(k, v);
          }
          // Sync DOM properties that don't reflect from attrs.
          if (k === "value" && ("value" in el)) {
            el.value = v;
            valueChanged = true;
          }
          if (k === "checked") el.checked = v !== "" && v !== "false";
          if (k === "selected") el.selected = v !== "" && v !== "false";
          if (k === "disabled") el.disabled = v !== "" && v !== "false";
        }
      }
      // Restore selection on focused input/textarea after a value
      // update. Clamp to the new value length so a shorter server
      // value does not throw RangeError. Scroll restore matters
      // mostly for multi-line textarea where the user may have
      // scrolled below the visible area.
      if (hadFocus && valueChanged && savedSelStart !== null &&
          typeof el.setSelectionRange === "function") {
        var newLen = (el.value || "").length;
        var s = Math.min(savedSelStart, newLen);
        var e = Math.min(savedSelEnd === null ? s : savedSelEnd, newLen);
        try { el.setSelectionRange(s, e); } catch (_) {}
        if (savedScrollTop) el.scrollTop = savedScrollTop;
      }
    }
    if (p.remove) el.remove();
  }
  // Any new sky-* attribute in the patched DOM needs a listener.
  __skyBindEvents(document);
  // After SSE-driven patches the URL also needs reconciling — without
  // this, programmatic Navigate Msgs would only update the in-memory
  // model and leave the address bar pointing at the previous page.
  __skyRunPaths(document);
  // Any <script> in newly-patched HTML wouldn't execute via innerHTML
  // — revive them so JS bundles (e.g. sky-editor) bootstrap correctly
  // when their host element first appears via a patch (not the initial
  // SSR).  See __skyReviveScripts above for the full rationale.
  var skyRootForPatches = document.getElementById("sky-root");
  if (skyRootForPatches) __skyReviveScripts(skyRootForPatches);
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

// __skyRunPaths: safer, CSP-friendly alternative to data-sky-eval for
// the specific case of "update the address bar after a render." Looks
// for [data-sky-path] elements and pushes / replaces history if the
// value differs from location. No new Function(), no eval; the only
// DOM APIs touched are getAttribute and history.pushState /
// replaceState. Works under strict CSP (no 'unsafe-eval') and has no
// XSS surface (the value is a URL path, never executed).
//
// The element is intentionally NOT removed after running — Sky.Live's
// patches identify elements by sky-id and look them up via
// querySelector; removing the data-sky-path element would orphan its
// sky-id, and the next attribute patch (when the path changes) would
// silently skip. The path-check makes the call idempotent, so leaving
// the element in place is cheap — at most one comparison per patch.
function __skyRunPaths(root) {
  var els = (root || document).querySelectorAll("[data-sky-path]");
  for (var i = 0; i < els.length; i++) {
    var p = els[i].getAttribute("data-sky-path");
    if (!p) continue;
    if (location.pathname !== p) {
      try { history.pushState({}, "", p); } catch (_) {}
    } else if (location.search) {
      try { history.replaceState({}, "", p); } catch (_) {}
    }
  }
  // v0.16.18 #558-PR4 — sibling that manages the query string.
  // The value is the raw query (no leading '?'); empty value means
  // "no params, strip any existing query string". Always
  // replaceState (never push) — filter changes shouldn't grow the
  // back-button history. The path is preserved, so this composes
  // with data-sky-path: paths push, queries replace.
  var qels = (root || document).querySelectorAll("[data-sky-query]");
  for (var j = 0; j < qels.length; j++) {
    var q = qels[j].getAttribute("data-sky-query") || "";
    var current = (location.search || "").replace(/^\?/, "");
    if (q === current) continue;
    var target = location.pathname + (q ? "?" + q : "");
    try { history.replaceState({}, "", target); } catch (_) {}
  }
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
      // Form-data assembly. Two non-obvious rules:
      //
      // 1. SUBMITTER FILTER. <button type="submit"> and
      //    <input type="submit"> entries appear in form.elements.
      //    Spec: only the SUBMITTER (the button that actually
      //    triggered the submit) contributes its name/value to
      //    the payload — peer submit buttons MUST NOT. Editors
      //    routinely use multiple submit buttons sharing one
      //    name="action" (Save / Format / Check); the naive
      //    "iterate everything" loop lets later buttons clobber
      //    earlier ones, so the LAST button name=action wins
      //    regardless of which the user clicked. Honour
      //    ev.submitter (modern browsers; falls back to
      //    document.activeElement for old Safari).
      //
      // 2. Disabled fields are excluded by the spec — skip them
      //    too so a disabled-but-submittable field doesn't leak
      //    a stale value.
      var data = {};
      var submitter = ev.submitter ||
          (document.activeElement && t && t.contains(document.activeElement)
              ? document.activeElement : null);
      if (t && t.elements) {
        for (var i = 0; i < t.elements.length; i++) {
          var el = t.elements[i];
          if (!el.name || el.disabled) continue;
          if (el.type === "submit" || el.type === "button" ||
              el.type === "image" || el.type === "reset") {
            // Only the submitter button contributes its name/value.
            if (el === submitter) data[el.name] = el.value;
            continue;
          }
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
    .then(function(r) {
      // r.ok check is load-bearing. Without it, a 404 body like
      // "session not found" (server lost our session_id store
      // entry — TTL expiry, store-restart, store-config change,
      // cross-deploy cookie collision) would be passed verbatim
      // to __skyPatch and become the whole page body.
      // Non-OK → full-page reload, which triggers the runtime's
      // initial-page handler: creates a fresh session_id and
      // re-runs the app's init. Apps gate on session presence
      // (Maybe Session in Model) so the reload lands cleanly on
      // whatever surface their init is configured to render.
      if (!r.ok) { window.location.href = href; return; }
      return r.text().then(function(t) {
        __skyPatch(t);
        window.history.pushState({}, "", href);
      });
    })
    .catch(function() { window.location.href = href; });
});
window.addEventListener("popstate", function() {
  fetch(window.location.href, { headers: { "X-Sky-Nav": "1" }, credentials: "same-origin" })
    .then(function(r) {
      // Same r.ok gate as the sky-nav click path. Without it,
      // Back/Forward to a URL after the server lost our session
      // renders the 404 body as the whole page.
      if (!r.ok) { window.location.href = window.location.href; return; }
      return r.text().then(__skyPatch);
    })
    .catch(function() { /* Back/Forward fetch failed; leave URL alone. */ });
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

// ── Server-Sent Events ───────────────────────────────────────
// Frame envelope since v0.9.3+: {seq, body, ackInputs?}. Falls back to
// treating e.data as a raw HTML body when JSON parsing fails, so a
// mixed-version rollout doesn't break the open-SSE connection.
//
// Reverse-proxy hardening: the browser's EventSource has no
// application-level liveness check — if a misbehaving proxy holds the
// socket open with no body or rewrites an upstream 502 to 200 with a
// non-SSE HTML payload, EventSource will fire 'open' and never fire
// 'error', leaving the client silently wedged. The server now sends
// an immediate 'hello' event and a periodic 'heartbeat'; the client
// watchdog (below) treats absence of either as a wedge and force-
// reconnects with backoff. See docs/skylive/architecture.md
// §SSE wedge detection.
var __skySSE = null;
var __skyOpenAt = 0;          // ms timestamp of last EventSource.open
var __skyLastSseAt = 0;       // ms timestamp of any SSE event
var __skyHelloOk = false;     // server sent its handshake this connection
var __skyWatchdogTimer = null;
var __skySseReopenTimer = null;
var __skyForcedClose = false; // true while we're tearing down to reopen
function __skyOpenSSE() {
  __skyForcedClose = false;
  __skyHelloOk = false;
  __skyOpenAt = 0;
  __skySSE = new EventSource(__skyBase + "/_sky/sse");
  __skySSE.addEventListener("hello", function(e) {
    // Handshake received — we know we hit a real Sky.Live v2 server,
    // not a proxy that intercepted with a generic 200. Anything
    // before hello is suspect, so the connected-state flip happens
    // HERE, not on EventSource.open. Remember that THIS page's
    // server speaks v2 so future watchdog cycles can tighten the
    // wedge-detection threshold to the fast 8s hello timeout.
    __skyServerSpeaksV2 = true;
    __skyHelloOk = true;
    __skyLastSseAt = Date.now();
    if (__skyStatusGraceTimer !== null) {
      clearTimeout(__skyStatusGraceTimer);
      __skyStatusGraceTimer = null;
    }
    if (__skyStatus !== "connected") {
      __skySetStatus("connected", "");
    }
    __skyRetryAttempts = 0;
    if (__skyRetryTimer !== null) {
      clearTimeout(__skyRetryTimer);
      __skyRetryTimer = null;
    }
    if (__skyEventQueue.length > 0) __skyDrainQueue();
  });
  __skySSE.addEventListener("heartbeat", function(e) {
    __skyLastSseAt = Date.now();
  });
  __skySSE.addEventListener("patch", function(e) {
    __skyLastSseAt = Date.now();
    // Old servers (pre-handshake) only ever send "patch" events.
    // A real patch is itself proof we're talking to a Sky.Live server,
    // not a proxy-rewritten 200-OK, so treat first-patch-without-hello
    // as an implicit handshake. This keeps a new client from trapping
    // itself when a rolling deploy puts it in front of an old server.
    if (!__skyHelloOk) {
      __skyHelloOk = true;
      if (__skyStatusGraceTimer !== null) {
        clearTimeout(__skyStatusGraceTimer);
        __skyStatusGraceTimer = null;
      }
      if (__skyStatus !== "connected") {
        __skySetStatus("connected", "");
      }
      __skyRetryAttempts = 0;
      if (__skyRetryTimer !== null) {
        clearTimeout(__skyRetryTimer);
        __skyRetryTimer = null;
      }
    }
    var frame;
    try { frame = JSON.parse(e.data); } catch (_) {
      // Legacy frame (pre-v0.9.3 server) — raw HTML, no seq to gate on.
      // Open-<select> defence (Bug 3): same-cycle as the patches path.
      // SSE-pushed full-body re-renders during an open dropdown would
      // collapse it; skip the body, the next user interaction triggers
      // reconciliation. Active user paths (sky-nav, popstate, POST
      // text fallback) are NOT defended — those are user-initiated and
      // dropping them would be worse UX than the dropdown collapsing.
      if (document.activeElement && document.activeElement.tagName === "SELECT") return;
      return __skyPatch(e.data.replace(/\\n/g, "\n"));
    }
    if (frame && typeof frame === "object") {
      __skyHandleResponse(frame.seq, frame.ackInputs, function() {
        if (document.activeElement && document.activeElement.tagName === "SELECT") return;
        if (frame.body) __skyPatch(frame.body.replace(/\\n/g, "\n"));
      }, frame.globalSeq);
    }
  });
  // Cycle 3 P50b / Gap C11 — structural-patches SSE event.
  //
  // The producer (Cycle 3 P50a) now ships event:patches for any
  // render whose diff against the previous tree fits in a small
  // patch list (the typical 1-3 attribute/text node change at
  // ~200-1000 B, vs the ~14 KB full body). The legacy event:patch
  // handler above stays for first-renders, reconnect-resync,
  // full-replace fallbacks, and any pre-P50a server.
  //
  // Shape parity with the HTTP /_sky/event reply: frame is
  // {seq, ackInputs, patches} — identical to writeEventJSON's
  // envelope, so __skyApplyPatches consumes both routes without
  // divergence. seq-gating via __skyHandleResponse means out-of-
  // order frames (a stale patches frame arriving after a fresher
  // patch frame, e.g. across a brief network blip) are dropped at
  // the same monotonic guard the HTTP path uses.
  //
  // No open-<select> defence at this outer level — __skyApplyPatches
  // already has its own per-patch focus-restore + open-select skip
  // (live.go:4386+); applying it twice would surface as a no-op
  // either way, but the inner check is the canonical defence.
  // Focus / input-authority / dirty-input filtering all flow through
  // the same code path as the HTTP-side patches application, so
  // in-flight typing is preserved without server-side clientState
  // alignment (the SSE producer passes nil clientState to diffTrees;
  // the client's __skyIsDirty filter takes over).
  __skySSE.addEventListener("patches", function(e) {
    __skyLastSseAt = Date.now();
    // Same implicit-handshake defence as the legacy patch listener:
    // a real patches frame proves we're talking to a Sky.Live server,
    // so unstick the hello check even if the dedicated 'hello' event
    // got eaten by a misbehaving proxy.
    if (!__skyHelloOk) {
      __skyHelloOk = true;
      if (__skyStatusGraceTimer !== null) {
        clearTimeout(__skyStatusGraceTimer);
        __skyStatusGraceTimer = null;
      }
      if (__skyStatus !== "connected") {
        __skySetStatus("connected", "");
      }
      __skyRetryAttempts = 0;
      if (__skyRetryTimer !== null) {
        clearTimeout(__skyRetryTimer);
        __skyRetryTimer = null;
      }
    }
    var frame;
    try { frame = JSON.parse(e.data); }
    catch (_) {
      // Producer guarantees JSON for event:patches; a non-JSON
      // payload is impossible from a P50a+ server. Drop silently
      // rather than running __skyPatch on garbage.
      return;
    }
    if (!frame || typeof frame !== "object" || !frame.patches) return;
    __skyHandleResponse(frame.seq, frame.ackInputs, function() {
      __skyApplyPatches(frame.patches);
    }, frame.globalSeq);
  });
  __skySSE.addEventListener("open", function() {
    // EventSource fired open — but we don't trust this alone, since a
    // proxy can rewrite a non-SSE 200 OK into something that fires
    // open without ever delivering a frame. Wait for 'hello' to flip
    // to connected. Just record the open timestamp so the watchdog
    // can measure "how long have we been open without a hello".
    __skyOpenAt = Date.now();
    __skyLastSseAt = Date.now();
  });
  __skySSE.addEventListener("error", function() {
    // Suppress the banner when we triggered the close ourselves
    // (force-reopen path) — those errors are an artefact of our own
    // teardown, not a real outage signal.
    if (__skyForcedClose) return;
    // CLOSED (2) means the browser failed the connection permanently.
    // Per the EventSource spec, this happens for any non-200 HTTP
    // response (Caddy/Nginx 502 when upstream is down, 504 timeout,
    // 503 service unavailable) AND for the wrong Content-Type. The
    // browser will NOT retry on its own — we have to drive the
    // reconnect ourselves. Without this branch the whole reconnect
    // story collapses behind a reverse proxy that returns proper
    // 5xx codes during outages.
    if (__skySSE && __skySSE.readyState === 2) {
      __skyForceReopenSSE();
      return;
    }
    // CONNECTING (0): browser is auto-retrying (network blip, no HTTP
    // response received yet). Show the banner only if the situation
    // persists past the grace window — a quick error+reopen burst
    // shouldn't paint chrome.
    if (__skyStatus !== "connected") return;
    if (__skyStatusGraceTimer !== null) return;
    __skyStatusGraceTimer = setTimeout(function() {
      __skyStatusGraceTimer = null;
      if (__skySSE && __skySSE.readyState === 1 && __skyHelloOk) return;
      __skySetStatus("reconnecting", __skyMsgReconnecting);
    }, 500);
  });
}

// __skyForceReopenSSE — close the current EventSource and queue a
// fresh open with backoff. Each call bumps the retry counter; once
// it exceeds __skyRetryMaxAttempts the banner flips to "offline" but
// reconnect attempts CONTINUE in the background at the max delay so
// a healed proxy is picked up automatically (otherwise the user is
// permanently stuck unless they click something or refresh, which is
// surprising on push-driven UIs like dashboards or chat). Backoff
// matches the POST retry schedule so the user doesn't see two
// independent timers.
function __skyForceReopenSSE() {
  __skyForcedClose = true;
  try { if (__skySSE) __skySSE.close(); } catch (_) {}
  __skySSE = null;
  if (__skyStatus === "connected") {
    __skySetStatus("reconnecting", __skyMsgReconnecting);
  }
  __skyRetryAttempts++;
  // Session-loss probe: when the SSE is wedged (typically a server
  // restart with the memory store, or a sky.toml [live] store change
  // wiping the persistent session), no amount of reopen retries can
  // recover the lost session — the only path forward is a full page
  // reload, which fires handleInitial and creates a fresh session.
  // We probe with a fake POST: a 404 + X-Sky-Live: 1 + body
  // containing "session not found" is the unambiguous signal that the
  // server is up but doesn't know our cookie. Anything else (network
  // error, 5xx, healthy 200) keeps the normal retry path engaged so
  // we don't reload on a transient blip — full reload destroys
  // uncontrolled-input state that v0.11.7's preservation rules can't
  // bring back.
  __skyProbeSessionLost();
  if (__skyRetryAttempts >= __skyRetryMaxAttempts && __skyStatus !== "offline") {
    __skySetStatus("offline", __skyMsgOffline);
  }
  if (__skySseReopenTimer !== null) {
    clearTimeout(__skySseReopenTimer);
  }
  var delay = Math.min(__skyRetryBaseMs * Math.pow(2, __skyRetryAttempts - 1), __skyRetryMaxMs);
  __skySseReopenTimer = setTimeout(function() {
    __skySseReopenTimer = null;
    __skyOpenSSE();
  }, delay);
}

// __skyProbeSessionLost — fire-and-forget POST whose only purpose is
// to read the server's reaction to our existing sky_sid cookie. If
// the server is up AND has lost our session (memory-store restart,
// store-kind change, session TTL expiry), we get a 404 with the
// X-Sky-Live marker and a "session not found" body. That's the cue
// to hard-reload — every reopen attempt would otherwise loop on the
// same 404 forever.
//
// Must NOT trigger any user-visible side effects on the server. We
// send a Msg name that no real app registers and supply no
// handlerId, so handleEvent's code path goes:
//   session not found → 404 (the case we're probing for)
//   session found, handler not found → 404 with a different body
//   (we explicitly check the body string to avoid false positives).
var __skyProbedReload = false;  // one-shot guard so we don't trigger
                                // multiple reloads from a burst of
                                // failed reopen attempts.
function __skyProbeSessionLost() {
  if (__skyProbedReload) return;
  var headers = {"Content-Type": "application/json"};
  if (__skyCsrfToken) headers["X-Sky-Csrf"] = __skyCsrfToken;
  fetch(__skyBase + "/_sky/event", {
    method: "POST",
    headers: headers,
    body: JSON.stringify({sessionId: __skySid, msg: "__skySessionPing", args: []}),
    credentials: "same-origin"
  }).then(function(r) {
    if (r.status !== 404) return;
    if (r.headers.get("X-Sky-Live") !== "1") return;
    return r.text().then(function(body) {
      // Specifically "session not found" — distinguishes from
      // "handler not found" (which means the session is fine, just
      // our probe Msg name doesn't exist; that's expected and
      // doesn't warrant a reload).
      if (body.indexOf("session not found") < 0) return;
      __skyProbedReload = true;
      if (window.console && console.warn) {
        console.warn("[sky.live] server lost our session — reloading page to recover");
      }
      window.location.reload();
    });
  }).catch(function() {
    // Network error / server down. Keep retrying via normal path.
  });
}

// __skyWatchdog — runs every 5s. Two wedge detectors layered:
//   1. Connection has been quiet for longer than __skyHeartbeatTtlMs
//      (35s default). Catches every wedge shape — a proxy holding
//      the socket open with no body, an upstream 502 rewritten to
//      200 + HTML, mid-stream TCP stalls. The 35s threshold is
//      tuned to be just over 2× the server's 15s heartbeat; if the
//      server is new we miss at most one heartbeat before reacting.
//   2. Faster handshake check: once this PAGE has confirmed the
//      server speaks the v2 protocol (any session received a hello),
//      tighten the threshold to __skyHelloTimeoutMs (8s) on every
//      subsequent connection. Pre-v2 servers stay on the slower
//      heartbeat-ttl path so a rolling deploy doesn't wedge new
//      clients hitting old pods. The page-scoped flag survives SSE
//      teardowns + reopens within the same tab.
// Both paths increment the retry counter via __skyForceReopenSSE,
// so a wedge that persists reaches "offline" instead of looping
// forever — but reopen attempts continue at the max delay so a
// healed proxy reconnects automatically without a refresh.
var __skyServerSpeaksV2 = false;
function __skyWatchdog() {
  // If we have no live EventSource AND no reopen scheduled, the
  // 'error' handler must have missed (rare race) or some path tore
  // it down without re-arming. Drive the reopen here so the page
  // never gets permanently disconnected.
  if (!__skySSE && __skySseReopenTimer === null) {
    __skyForceReopenSSE();
    return;
  }
  if (!__skySSE) return;
  // CLOSED (2): browser failed the connection (non-200, wrong CT)
  // and won't retry. The 'error' handler should have caught this,
  // but cover the case where it didn't fire (e.g. error during
  // initial handshake before listeners attached, or a browser
  // implementation quirk). Single source of truth — both paths end
  // in __skyForceReopenSSE.
  if (__skySSE.readyState === 2) {
    if (!__skyForcedClose) {
      __skyForceReopenSSE();
    }
    return;
  }
  if (__skySSE.readyState !== 1) return;  // CONNECTING (0): browser is retrying, leave it
  var now = Date.now();
  // Effective threshold:
  //   - Brand-new SSE on a v2-confirmed server → fast hello timeout
  //     (8s) since we expect a hello promptly.
  //   - Otherwise → conservative heartbeat ttl (35s) so old servers
  //     and idle dashboards don't false-positive.
  var quietMs = now - __skyLastSseAt;
  var threshold = __skyHeartbeatTtlMs;
  if (__skyServerSpeaksV2 && !__skyHelloOk) {
    threshold = __skyHelloTimeoutMs;
  }
  if (quietMs > threshold) {
    if (window.console && console.warn) {
      console.warn("[sky.live] SSE quiet for " + quietMs +
        "ms (threshold " + threshold + "ms) — reopening");
    }
    __skyForceReopenSSE();
  }
}

// Kick off the SSE connection + watchdog. Watchdog interval is short
// enough (5s) that a wedge is detected within 5s + helloTimeout / ttl
// of the actual fault, and long enough to not be a measurable CPU cost.
__skyOpenSSE();
__skyWatchdogTimer = setInterval(__skyWatchdog, 5000);

// On tab visibility change, re-evaluate immediately — when a tab
// resumes from background the OS may have torn down the underlying
// TCP, but EventSource sometimes lags in detecting it. Eager check
// avoids the user staring at a stale UI for the full watchdog cycle.
document.addEventListener("visibilitychange", function() {
  if (document.visibilityState === "visible") {
    __skyWatchdog();
  }
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
`,
		sid, basePath, csrfToken, cfg.Enabled, cfg.BaseMs, cfg.MaxMs, cfg.MaxAttempts, cfg.QueueMax,
		jsString(cfg.Reconnecting), jsString(cfg.Offline),
		cfg.HelloTimeoutMs, cfg.HeartbeatTtlMs,
	)
}

// ═══════════════════════════════════════════════════════════
// Helpers: tuple access, sky_call dispatch
// ═══════════════════════════════════════════════════════════

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
