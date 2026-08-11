// stdlib_web.go — shared HTML-escaping helpers.
//
// v0.13 Layer 3: Std.Css / Std.Html / Std.Html.Attributes /
// Std.Html.Events were migrated from Go runtime kernels to
// fully-typed Sky source (sky-stdlib/Std/{Css,Html}.sky and
// sky-stdlib/Std/Html/{Attributes,Events}.sky). The element /
// attribute / event / CSS builders that used to live here are
// gone; only the two escaping primitives remain, because the
// Sky-source modules call them across the FFI boundary via
// `Ffi.callPure "htmlEscapeText"` / `"htmlEscapeAttr"` (the
// registry entries are in live.go's init()).
//
// Both delegate to the standard library's `html.EscapeString`,
// which escapes `& ' < > "` — the full set required to make a
// string safe in both element-text and double-quoted-attribute
// contexts. Never hand-roll escaping with strings.Replace: a
// missed character (or wrong order) is an XSS hole.
package rt

import (
	"html"
	"strings"
)

func htmlEscapeText(s string) string {
	return html.EscapeString(s)
}

// SafeAttrURL neutralises a URL-bearing attribute whose SCHEME executes
// script, returning `about:blank` in its place. Every other value — and every
// attribute that is not URL-bearing — is returned UNCHANGED.
//
// # Why this is in the runtime and not in `Std.Ui`
//
// `Std.Ui`'s contract is "HTML-escaping is automatic; `data-sky-eval` is
// forbidden", and `Std.Markdown`'s is stronger: "Safe to feed UNTRUSTED
// markdown … No bluemonday-equivalent sanitiser needed." Neither held for a
// LINK. `Markdown.render "[x](javascript:alert(1))"` produced
// `<a href="javascript:alert(1)">x</a>` verbatim: nothing anywhere looked at
// the scheme, and HTML-escaping cannot help — the payload contains no
// metacharacter, so it survives escaping intact.
//
// It sits HERE, at `attrsFromAttribute`'s `Attr` case, because that is the ONE
// place every attribute enters a `VNode` — from `Std.Ui` (via `kernelAttr` ->
// `Attr.href`), from `Std.Html` directly, and from `Std.Markdown` through
// `Ui.link`. Putting it in `Std.Ui` instead would have covered only the first
// of those, and would have cost ten `rt.As*` narrowings per compiled program
// (every kernel call in Layer-3 Sky returns `any`), widening the coerce-floor
// census on twelve examples for a check that belongs to the renderer.
//
// BLOCKED, and only these:
//
//   - `javascript:` / `vbscript:` — navigation executes the body.
//   - `data:` other than `data:image/` — a `data:text/html` document runs
//     script in the page's origin; a `data:image/png` is inert and is what an
//     inline avatar or chart legitimately uses.
//
// Everything else passes through untouched: relative paths, `https:`,
// `mailto:`, `tel:`, `blob:`, `#fragment`, and any custom scheme an app
// registers. So this cannot break a link that was not an XSS.
//
// The comparison strips ASCII control characters and whitespace and lowercases
// before matching, because that is what a browser does to a URL before
// resolving its scheme: ` javascript:`, `JaVaScRiPt:` and `java<TAB>script:`
// all navigate, and a naive `strings.HasPrefix` catches none of them.
func SafeAttrURL(key, value string) string {
	if !urlBearingAttr(key) {
		return value
	}
	if executableURLScheme(value) {
		return "about:blank"
	}
	return value
}

// The attributes whose value a browser RESOLVES AS A URL and may navigate to.
// `formaction` and `xlink:href` are here because they are the two that get
// forgotten; `Std.Ui` does not emit them today, but `Ui.htmlAttribute` and
// `Std.Html.attribute` let a user write either.
func urlBearingAttr(key string) bool {
	switch key {
	case "href", "src", "action", "formaction", "xlink:href", "poster", "data":
		return true
	}
	return false
}

// executableURLScheme reports whether `raw` names a scheme that runs script on
// navigation, after the normalisation a browser applies.
func executableURLScheme(raw string) bool {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		// Strip ASCII whitespace and control characters, and stop at the first
		// character that cannot be part of a scheme — a scheme is
		// `ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ) ":"` (RFC 3986 §3.1), so
		// nothing after the colon can change which scheme this is.
		if c <= 0x20 || c == 0x7f {
			continue
		}
		b.WriteByte(c)
		if c == ':' {
			break
		}
	}
	s := strings.ToLower(b.String())
	switch {
	case strings.HasPrefix(s, "javascript:"), strings.HasPrefix(s, "vbscript:"):
		return true
	case strings.HasPrefix(s, "data:"):
		// The scheme scan stops at the colon, so re-read the MIME type from the
		// original value: only `data:image/…` is inert enough to keep.
		trimmed := strings.ToLower(strings.TrimLeft(raw, " \t\n\r\f\v\x00"))
		return !strings.HasPrefix(trimmed, "data:image/")
	}
	return false
}

func htmlEscapeAttr(s string) string {
	return html.EscapeString(s)
}
