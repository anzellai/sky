package rt

// Attributes of the root <html> element (Std.Html.Document).
//
// An app sets them with App.withDocument / Live.withDocument /
// Spa.withDocument: a function `model -> List Document.Attribute`. Every web
// target uses this one file. The server writes the attributes on the first
// page, Sky.Live sends a changed set to the browser in the patch protocol
// (the "doc" field of a frame), the Sky.Spa client sets them on
// document.documentElement, and the webview shell writes them.
//
// The values come from the app, so they are checked here, at one place:
//   - The name must be lang, dir, class, translate or a data-* name. The
//     Std.Html.Document constructors only make these names. This check is a
//     second line of defence.
//   - A lang value must be a well-formed BCP 47 tag (RFC 5646 syntax).
//   - A data-* name must be lower case and must not start with data-sky-,
//     which the runtime uses for its own markers (data-sky-hydrating).
//   - A dir value must be ltr, rtl or auto. A translate value must be yes or
//     no.
//
// A value that fails a check is dropped and logged once (WARN). It is never
// rendered and it never panics. A repeated name keeps its first position and
// takes the value of the last occurrence ("the last one wins").

import (
	"html"
	"strings"
	"sync"
)

// docAttr is one checked attribute of the <html> element. The JSON form is
// the wire form of the Sky.Live "doc" operation.
type docAttr struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// documentAttrs runs the app's document function on the model and returns
// the checked, de-duplicated attributes. A nil function, a panic in the
// function or a value that is not a list gives nil.
func documentAttrs(fn, model any) (out []docAttr) {
	if fn == nil || !isFunc(fn) {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			docWarnOnce("panic", "", "Std.Html.Document: the document function failed, so the <html> attributes are not changed")
			out = nil
		}
	}()
	items := asList(sky_call(fn, model))
	if len(items) == 0 {
		return []docAttr{}
	}
	out = make([]docAttr, 0, len(items))
	index := make(map[string]int, len(items))
	for _, item := range items {
		name, value, ok := docAttrShape(item)
		if !ok {
			continue
		}
		value, ok = checkDocAttr(name, value)
		if !ok {
			continue
		}
		if at, seen := index[name]; seen {
			out[at].Value = value
			continue
		}
		index[name] = len(out)
		out = append(out, docAttr{Name: name, Value: value})
	}
	return out
}

// docAttrShape reads one `Attribute name value` value of Std.Html.Document.
func docAttrShape(item any) (string, string, bool) {
	ctor, _, fields, ok := unwrapADTShape(unwrapAny(item))
	if !ok || ctor != "Attribute" || len(fields) != 2 {
		return "", "", false
	}
	name, nameOK := fields[0].(string)
	value, valueOK := fields[1].(string)
	if !nameOK || !valueOK {
		return "", "", false
	}
	return name, value, true
}

// checkDocAttr applies the rules in the file comment. It returns the value
// to write and false when the attribute must be dropped.
func checkDocAttr(name, value string) (string, bool) {
	switch name {
	case "lang":
		if !wellFormedLangTag(value) {
			docWarnOnce("lang", value, "Std.Html.Document: lang is not a well-formed BCP 47 tag, so it is not written")
			return "", false
		}
		return value, true
	case "dir":
		if value != "ltr" && value != "rtl" && value != "auto" {
			docWarnOnce("dir", value, "Std.Html.Document: dir must be ltr, rtl or auto, so it is not written")
			return "", false
		}
		return value, true
	case "translate":
		if value != "yes" && value != "no" {
			docWarnOnce("translate", value, "Std.Html.Document: translate must be yes or no, so it is not written")
			return "", false
		}
		return value, true
	case "class":
		return value, true
	}
	if strings.HasPrefix(name, "data-") && validDocDataName(name[len("data-"):]) {
		return value, true
	}
	docWarnOnce("name", name, "Std.Html.Document: the attribute name is not allowed on <html> (data-sky-* is reserved), so it is not written")
	return "", false
}

// validDocDataName checks the part of a data-* name after "data-". It must
// not be empty, it uses only a-z, 0-9, '-', '_' and '.', it starts with a
// letter or a digit, and it must not start with "sky-" (or be "sky").
func validDocDataName(s string) bool {
	if s == "" || s == "sky" || strings.HasPrefix(s, "sky-") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_' || c == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

// docWarned keeps the problems already logged, so each one is logged once
// per process.
var docWarned sync.Map

func docWarnOnce(kind, value, msg string) {
	key := kind + "\x00" + value
	if _, loaded := docWarned.LoadOrStore(key, true); loaded {
		return
	}
	logEmit(logLevelWarn, "warn", msg, map[string]any{
		"class": "DocumentAttributeRejected",
		"kind":  kind,
		"value": value,
	})
}

// renderDocAttrs writes the attributes as they go inside the <html> start
// tag: each one is a space, the name, and the HTML-escaped value in double
// quotes. The names are checked (checkDocAttr), so only the values need
// escaping. html.EscapeString escapes <, >, &, ' and ". A newline in a
// quoted value is safe and stays as it is.
func renderDocAttrs(attrs []docAttr) string {
	if len(attrs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(a.Value))
		b.WriteByte('"')
	}
	return b.String()
}

// sameDocAttrs reports whether two sets are equal, in order.
func sameDocAttrs(a, b []docAttr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// withDefaultLang returns attrs with lang="en" added when attrs has no lang.
// The Sky.Spa page always had lang="en", so an app that does not set lang
// keeps it.
func withDefaultLang(attrs []docAttr) []docAttr {
	for _, a := range attrs {
		if a.Name == "lang" {
			return attrs
		}
	}
	out := make([]docAttr, 0, len(attrs)+1)
	out = append(out, docAttr{Name: "lang", Value: "en"})
	return append(out, attrs...)
}

// wellFormedLangTag reports whether s is a well-formed language tag under
// the syntax of RFC 5646 section 2.1 (not a check against the registry).
// Subtags are case-insensitive.
func wellFormedLangTag(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	lower := strings.ToLower(s)
	if bcp47Irregular[lower] {
		return true
	}
	parts := strings.Split(lower, "-")
	for _, p := range parts {
		if len(p) == 0 || len(p) > 8 || !isAlnumASCII(p) {
			return false
		}
	}
	if parts[0] == "x" {
		return privateUseTail(parts[1:])
	}
	i := 0
	// language: 2-3 ALPHA [extlang], or 4 ALPHA (reserved), or 5-8 ALPHA.
	lang := parts[i]
	if !isAlphaASCII(lang) || len(lang) < 2 {
		return false
	}
	i++
	if len(lang) <= 3 {
		// extlang: up to three 3-ALPHA subtags.
		for n := 0; n < 3 && i < len(parts) && len(parts[i]) == 3 && isAlphaASCII(parts[i]); n++ {
			i++
		}
	}
	// script: 4 ALPHA.
	if i < len(parts) && len(parts[i]) == 4 && isAlphaASCII(parts[i]) {
		i++
	}
	// region: 2 ALPHA or 3 DIGIT.
	if i < len(parts) && ((len(parts[i]) == 2 && isAlphaASCII(parts[i])) ||
		(len(parts[i]) == 3 && isDigitASCII(parts[i]))) {
		i++
	}
	// variants: 5-8 alphanum, or a digit followed by 3 alphanum.
	for i < len(parts) && (len(parts[i]) >= 5 || (len(parts[i]) == 4 && isDigitASCII(parts[i][:1]))) {
		i++
	}
	// extensions: a singleton (not x) followed by one or more 2-8 subtags.
	seen := map[string]bool{}
	for i < len(parts) && len(parts[i]) == 1 && parts[i] != "x" {
		single := parts[i]
		if seen[single] {
			return false
		}
		seen[single] = true
		i++
		n := 0
		for i < len(parts) && len(parts[i]) >= 2 {
			i++
			n++
		}
		if n == 0 {
			return false
		}
	}
	if i < len(parts) && parts[i] == "x" {
		return privateUseTail(parts[i+1:])
	}
	return i == len(parts)
}

func privateUseTail(parts []string) bool {
	// Each subtag is 1-8 alphanum (already checked); at least one.
	return len(parts) > 0
}

func isAlnumASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isAlphaASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func isDigitASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// bcp47Irregular holds the irregular grandfathered tags of RFC 5646, which
// do not follow the main syntax. The regular grandfathered tags (for example
// "zh-min-nan") already match the main syntax.
var bcp47Irregular = map[string]bool{
	"en-gb-oed": true, "i-ami": true, "i-bnn": true, "i-default": true,
	"i-enochian": true, "i-hak": true, "i-klingon": true, "i-lux": true,
	"i-mingo": true, "i-navajo": true, "i-pwn": true, "i-tao": true,
	"i-tay": true, "i-tsu": true, "sgn-be-fr": true, "sgn-be-nl": true,
	"sgn-ch-de": true,
}
