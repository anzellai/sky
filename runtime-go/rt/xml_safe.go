package rt

// xml_safe.go — the safe XML reader behind Std.Xml (and Codec.fromXml).
//
// Input is untrusted, so the reader is built to refuse what the classic XML
// attacks need:
//
//   - Any directive (`<!DOCTYPE …>`, `<!ENTITY …>`) is refused with the code
//     `doctype`. With no DTD there is no entity definition, so billion-laughs
//     expansion and external entities (XXE) cannot happen.
//   - `encoding/xml` runs with Strict = true. Entity is nil (only the five XML
//     entities and numeric references) or xml.HTMLEntity in HtmlEntities mode.
//   - CharsetReader accepts UTF-8, US-ASCII, ISO-8859-1 and Windows-1252 only.
//   - The size, element depth, node count and attributes per element are
//     bounded while the tree is built (xmlLimits), so a hostile document fails
//     fast instead of exhausting memory.
//
// Text is collected with a strings.Builder per text node: appending with `+=`
// would be quadratic in the length of a long text run.
//
// Failures are `Error` values with kind InvalidInput and a domain failure in
// the domain "xml" with one of the codes below. A `malformed` failure carries
// the line and column as attributes.
//
// The tokeniser itself (xmlStreamSafe, the only user of encoding/xml) is in
// xml_stream.go, which is server only. This file has no build tag: Std.Codec
// imports Std.Xml, so the Sky.Spa client compiles it, and it must import
// nothing the client does not already carry.

import "strings"

const (
	xmlCodeTooLarge          = "too_large"
	xmlCodeTooDeep           = "too_deep"
	xmlCodeTooManyNodes      = "too_many_nodes"
	xmlCodeTooManyAttributes = "too_many_attributes"
	xmlCodeDoctype           = "doctype"
	xmlCodeCharset           = "charset"
	xmlCodeMalformed         = "malformed"
	// xmlCodeServerOnly: the call ran in a Sky.Spa wasm client, which does
	// not carry the parser (xml_stream_js.go). Kind Unavailable.
	xmlCodeServerOnly = "server_only"
)

// xmlLimits bounds one parse. Each must be positive.
type xmlLimits struct {
	maxBytes      int
	maxDepth      int
	maxNodes      int
	maxAttributes int
}

// xmlDefaultLimits is `Std.Xml.defaultLimits`: 16 MiB, depth 256, 1,000,000
// nodes, 256 attributes on one element.
var xmlDefaultLimits = xmlLimits{
	maxBytes:      16 << 20,
	maxDepth:      256,
	maxNodes:      1_000_000,
	maxAttributes: 256,
}

// xmlAttr is one attribute: its local name, namespace URI and value.
type xmlAttr struct {
	name  string
	space string
	value string
}

// xmlNode is one node of a parsed document: an element, or a text node
// (isText, text in text).
type xmlNode struct {
	isText   bool
	name     string // local name ("" for a text node)
	space    string // namespace URI ("" when none)
	attrs    []xmlAttr
	children []*xmlNode
	text     string
	sb       *strings.Builder // text being collected while parsing
}

// xmlFailure is a parse failure before it becomes a Sky Error.
type xmlFailure struct {
	code  string
	msg   string
	attrs [][2]string
}

func (f *xmlFailure) Error() string { return f.msg }

func xmlFail(code, msg string, attrs ...[2]string) *xmlFailure {
	return &xmlFailure{code: code, msg: msg, attrs: attrs}
}

// skyError is the Sky `Error` for the failure: InvalidInput, domain "xml".
func (f *xmlFailure) skyError() SkyADT {
	return xmlDomainError(f.code, f.msg, f.attrs)
}

// xmlDomainError builds an InvalidInput Error in the domain "xml"
// (Unavailable for xmlCodeServerOnly).
func xmlDomainError(code, msg string, attrs [][2]string) SkyADT {
	df := DomainFailure{Domain: "xml", Code: code, RetryAfterMs: -1, Attrs: attrs}
	kind := errKindInvalidInput
	if code == xmlCodeServerOnly {
		kind = errKindUnavailable
	}
	return ErrDomain(kind, msg, df).(SkyADT)
}

// xmlIsSpace reports whether s is only XML white space.
func xmlIsSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

// xmlEvents receives the tokens of a document from xmlStreamSafe. Each
// callback may stop the stream by returning a failure. CharData is only valid
// until the callback returns (encoding/xml reuses the buffer).
type xmlEvents struct {
	start func(name, space string, attrs []xmlAttr) *xmlFailure
	end   func() *xmlFailure
	text  func(t []byte) *xmlFailure
}

// xmlParseSafe parses src into its root element under lim. htmlEntities adds
// the HTML named entity table (&nbsp; and the rest), still with no DTD. It is
// a tree built on the events of xmlStreamSafe.
func xmlParseSafe(src string, htmlEntities bool, lim xmlLimits) (*xmlNode, *xmlFailure) {
	var root *xmlNode
	var stack []*xmlNode
	ev := xmlEvents{
		start: func(name, space string, attrs []xmlAttr) *xmlFailure {
			n := &xmlNode{name: name, space: space, attrs: attrs}
			if len(stack) == 0 {
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
			return nil
		},
		end: func() *xmlFailure {
			xmlFinishText(stack[len(stack)-1])
			stack = stack[:len(stack)-1]
			return nil
		},
		text: func(t []byte) *xmlFailure {
			p := stack[len(stack)-1]
			if k := len(p.children); k > 0 && p.children[k-1].isText {
				p.children[k-1].sb.Write(t)
				return nil
			}
			tn := &xmlNode{isText: true, sb: &strings.Builder{}}
			tn.sb.Write(t)
			p.children = append(p.children, tn)
			return nil
		},
	}
	if f := xmlStreamSafe(src, htmlEntities, lim, ev); f != nil {
		return nil, f
	}
	return root, nil
}

// xmlFinishText moves the collected text of n's text children into place.
func xmlFinishText(n *xmlNode) {
	for _, c := range n.children {
		if c.isText && c.sb != nil {
			c.text = c.sb.String()
			c.sb = nil
		}
	}
}

// xmlTextOf is all of n's descendant text, in document order.
func xmlTextOf(n *xmlNode) string {
	if n == nil {
		return ""
	}
	if n.isText {
		return n.text
	}
	var b strings.Builder
	xmlWriteText(&b, n)
	return b.String()
}

func xmlWriteText(b *strings.Builder, n *xmlNode) {
	for _, c := range n.children {
		if c.isText {
			b.WriteString(c.text)
		} else {
			xmlWriteText(b, c)
		}
	}
}
