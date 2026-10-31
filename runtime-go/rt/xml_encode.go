package rt

// xml_encode.go — Std.Xml.Encode: build an XML value and write it as text.
//
// An `Xml` is the stdlib ADT `Xml__Internal` holding an `*xmlOut`, and an
// `Attribute` is `Attribute__Internal` holding an `xmlOutAttr`. Both are
// opaque in Sky.
//
// Characters XML cannot carry. `encode` (and `document`) is total: a
// character XML 1.0 forbids (U+0000 to U+0008, U+000B, U+000C, U+000E to
// U+001F, U+FFFE, U+FFFF, a lone surrogate, a byte that is not UTF-8) is
// written as U+FFFD, and a character that cannot be in a name is written as
// `_`. `encodeStrict` returns an Err (domain "xml", code `invalid_char` or
// `invalid_name`) instead. The total form exists because Codec field names and
// tags are constants in the program.

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	xmlOutADTName  = "Xml__Internal"
	xmlAttrADTName = "Attribute__Internal"

	xmlOutElement = 0
	xmlOutText    = 1
	xmlOutCdata   = 2

	xmlCodeInvalidName = "invalid_name"
	xmlCodeInvalidChar = "invalid_char"
)

type xmlOut struct {
	kind     int
	name     string
	attrs    []xmlOutAttr
	children []*xmlOut
	text     string
}

type xmlOutAttr struct {
	name  string
	value string
}

func xmlOutBox(x *xmlOut) SkyADT {
	return SkyADT{Tag: 0, SkyName: xmlOutADTName, Fields: []any{x}}
}

func xmlOutOf(v any) *xmlOut {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok && adt.SkyName == xmlOutADTName && len(adt.Fields) == 1 {
		if x, ok := adt.Fields[0].(*xmlOut); ok && x != nil {
			return x
		}
	}
	if x, ok := v.(*xmlOut); ok && x != nil {
		return x
	}
	return &xmlOut{kind: xmlOutText}
}

func xmlAttrOf(v any) xmlOutAttr {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok && adt.SkyName == xmlAttrADTName && len(adt.Fields) == 1 {
		if a, ok := adt.Fields[0].(xmlOutAttr); ok {
			return a
		}
	}
	if a, ok := v.(xmlOutAttr); ok {
		return a
	}
	return xmlOutAttr{}
}

// Xml_element is `Encode.element name attributes children`.
func Xml_element(name, attrs, children any) any {
	x := &xmlOut{kind: xmlOutElement, name: AsString(name)}
	for _, a := range asList(attrs) {
		x.attrs = append(x.attrs, xmlAttrOf(a))
	}
	for _, c := range asList(children) {
		x.children = append(x.children, xmlOutOf(c))
	}
	return xmlOutBox(x)
}

// Xml_text is `Encode.text s`: escaped character data.
func Xml_textNode(s any) any { return xmlOutBox(&xmlOut{kind: xmlOutText, text: AsString(s)}) }

// Xml_cdata is `Encode.cdata s`: a CDATA section. A "]]>" in s is split
// across two sections, so the text reads back exactly.
func Xml_cdata(s any) any { return xmlOutBox(&xmlOut{kind: xmlOutCdata, text: AsString(s)}) }

// Xml_attr is `Encode.attribute name value`.
func Xml_attr(name, value any) any {
	return SkyADT{Tag: 0, SkyName: xmlAttrADTName, Fields: []any{xmlOutAttr{name: AsString(name), value: AsString(value)}}}
}

// Xml_xmlns is `Encode.xmlns uri`: the default namespace declaration.
func Xml_xmlns(uri any) any { return Xml_attr("xmlns", uri) }

// Xml_xmlnsPrefix is `Encode.xmlnsPrefix prefix uri`.
func Xml_xmlnsPrefix(prefix, uri any) any { return Xml_attr("xmlns:"+AsString(prefix), uri) }

// Xml_encode is `Encode.encode indent xml`.
func Xml_encode(indent, x any) any { return Xml_encodeT(indent, x) }
func Xml_encodeT(indent, x any) string {
	w := xmlWriter{indent: AsInt(indent)}
	w.node(xmlOutOf(x), 0)
	return w.b.String()
}

const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>`

// Xml_document is `Encode.document indent xml`: the XML declaration, a
// newline, then the element.
func Xml_document(indent, x any) any { return Xml_documentT(indent, x) }
func Xml_documentT(indent, x any) string {
	return xmlDeclaration + "\n" + Xml_encodeT(indent, x)
}

// Xml_encodeStrict is `Encode.encodeStrict indent xml`: Err when a name or a
// character cannot be written as XML, instead of replacing it.
func Xml_encodeStrict(indent, x any) any {
	w := xmlWriter{indent: AsInt(indent), strict: true}
	w.node(xmlOutOf(x), 0)
	if w.fail != nil {
		return Err[any, any](w.fail.skyError())
	}
	return Ok[any, any](w.b.String())
}

type xmlWriter struct {
	b      strings.Builder
	indent int
	strict bool
	fail   *xmlFailure
}

func (w *xmlWriter) newline(depth int) {
	if w.indent <= 0 {
		return
	}
	w.b.WriteByte('\n')
	for i := 0; i < depth*w.indent; i++ {
		w.b.WriteByte(' ')
	}
}

func (w *xmlWriter) node(x *xmlOut, depth int) {
	switch x.kind {
	case xmlOutText:
		w.escape(x.text, false)
	case xmlOutCdata:
		w.cdata(x.text)
	default:
		w.element(x, depth)
	}
}

func (w *xmlWriter) element(x *xmlOut, depth int) {
	name := w.name(x.name)
	w.b.WriteByte('<')
	w.b.WriteString(name)
	for _, a := range x.attrs {
		w.b.WriteByte(' ')
		w.b.WriteString(w.name(a.name))
		w.b.WriteString(`="`)
		w.escape(a.value, true)
		w.b.WriteByte('"')
	}
	if len(x.children) == 0 {
		w.b.WriteString("/>")
		return
	}
	w.b.WriteByte('>')
	// Mixed content (any text child) is written as it is: indentation would
	// change the text.
	mixed := false
	for _, c := range x.children {
		if c.kind != xmlOutElement {
			mixed = true
			break
		}
	}
	for _, c := range x.children {
		if !mixed {
			w.newline(depth + 1)
		}
		w.node(c, depth+1)
	}
	if !mixed {
		w.newline(depth)
	}
	w.b.WriteString("</")
	w.b.WriteString(name)
	w.b.WriteByte('>')
}

func (w *xmlWriter) failWith(code, msg string) {
	if w.fail == nil {
		w.fail = xmlFail(code, msg)
	}
}

// xmlNameStart / xmlNameChar follow the XML 1.0 (fifth edition) Name
// production, with ':' allowed for a prefixed name.
func xmlNameStart(r rune) bool {
	return r == ':' || r == '_' || unicode.IsLetter(r) ||
		(r >= 0xC0 && r <= 0x2FF && r != 0xD7 && r != 0xF7) ||
		(r >= 0x370 && r <= 0x1FFF && r != 0x37E) ||
		(r >= 0x2C00 && r <= 0x2FEF) || (r >= 0x3001 && r <= 0xD7FF) ||
		(r >= 0xF900 && r <= 0xFDCF) || (r >= 0xFDF0 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0xEFFFF)
}

func xmlNameChar(r rune) bool {
	return xmlNameStart(r) || r == '-' || r == '.' || (r >= '0' && r <= '9') ||
		r == 0xB7 || (r >= 0x300 && r <= 0x36F) || (r >= 0x203F && r <= 0x2040)
}

// name returns a name XML can carry: each character a name cannot hold is
// `_`, and an empty name is `_`.
func (w *xmlWriter) name(s string) string {
	ok := s != ""
	for i, r := range s {
		if (i == 0 && !xmlNameStart(r)) || (i > 0 && !xmlNameChar(r)) || r == utf8.RuneError {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	if w.strict {
		w.failWith(xmlCodeInvalidName, "Xml.Encode.encodeStrict: "+strconv.Quote(s)+" is not a valid XML name")
	}
	if s == "" {
		return "_"
	}
	var b strings.Builder
	first := true
	for _, r := range s {
		if (first && xmlNameStart(r)) || (!first && xmlNameChar(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		first = false
	}
	return b.String()
}

// xmlCharOK is the XML 1.0 Char production.
func xmlCharOK(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// escape writes character data (or an attribute value when attr) with the
// markup characters escaped and each character XML cannot carry replaced.
func (w *xmlWriter) escape(s string, attr bool) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if (r == utf8.RuneError && size == 1) || !xmlCharOK(r) {
			if w.strict {
				w.failWith(xmlCodeInvalidChar, "Xml.Encode.encodeStrict: the character "+strconv.QuoteRune(r)+" cannot be written in XML 1.0")
			}
			w.b.WriteRune('�')
			continue
		}
		switch r {
		case '&':
			w.b.WriteString("&amp;")
		case '<':
			w.b.WriteString("&lt;")
		case '>':
			w.b.WriteString("&gt;")
		case '"':
			if attr {
				w.b.WriteString("&quot;")
			} else {
				w.b.WriteByte('"')
			}
		case '\n':
			if attr {
				w.b.WriteString("&#xA;")
			} else {
				w.b.WriteByte('\n')
			}
		case '\r':
			w.b.WriteString("&#xD;")
		case '\t':
			if attr {
				w.b.WriteString("&#x9;")
			} else {
				w.b.WriteByte('\t')
			}
		default:
			w.b.WriteRune(r)
		}
	}
}

// cdata writes a CDATA section, splitting it at each "]]>".
func (w *xmlWriter) cdata(s string) {
	var clean strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if (r == utf8.RuneError && size == 1) || !xmlCharOK(r) {
			if w.strict {
				w.failWith(xmlCodeInvalidChar, "Xml.Encode.encodeStrict: the character "+strconv.QuoteRune(r)+" cannot be written in XML 1.0")
			}
			clean.WriteRune('�')
			continue
		}
		clean.WriteRune(r)
	}
	w.b.WriteString("<![CDATA[")
	body := strings.ReplaceAll(clean.String(), "]]>", "]]]]><![CDATA[>")
	// A carriage return would read back as a line feed (XML line-end
	// normalisation), so it is written as a character reference between
	// two sections.
	body = strings.ReplaceAll(body, "\r", "]]>&#xD;<![CDATA[")
	w.b.WriteString(body)
	w.b.WriteString("]]>")
}
