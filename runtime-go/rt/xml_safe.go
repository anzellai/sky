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

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

const (
	xmlCodeTooLarge          = "too_large"
	xmlCodeTooDeep           = "too_deep"
	xmlCodeTooManyNodes      = "too_many_nodes"
	xmlCodeTooManyAttributes = "too_many_attributes"
	xmlCodeDoctype           = "doctype"
	xmlCodeCharset           = "charset"
	xmlCodeMalformed         = "malformed"
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

// xmlDomainError builds an InvalidInput Error in the domain "xml".
func xmlDomainError(code, msg string, attrs [][2]string) SkyADT {
	df := DomainFailure{Domain: "xml", Code: code, RetryAfterMs: -1, Attrs: attrs}
	return ErrDomain(errKindInvalidInput, msg, df).(SkyADT)
}

// xmlCharsetReader accepts the four encodings Std.Xml reads. A UTF-8 document
// never reaches it (encoding/xml reads UTF-8 itself).
func xmlCharsetReader(bad *string) func(string, io.Reader) (io.Reader, error) {
	return func(label string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "utf-8", "utf8", "us-ascii", "ascii":
			return input, nil
		case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1":
			return charmap.ISO8859_1.NewDecoder().Reader(input), nil
		case "windows-1252", "cp1252", "x-cp1252":
			return charmap.Windows1252.NewDecoder().Reader(input), nil
		}
		*bad = label
		return nil, errors.New("unsupported encoding " + strconv.Quote(label))
	}
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

// xmlParseSafe parses src into its root element under lim. htmlEntities adds
// the HTML named entity table (&nbsp; and the rest), still with no DTD.
func xmlParseSafe(src string, htmlEntities bool, lim xmlLimits) (*xmlNode, *xmlFailure) {
	if len(src) > lim.maxBytes {
		return nil, xmlFail(xmlCodeTooLarge,
			"Xml.parse: the document is "+strconv.Itoa(len(src))+" bytes, more than the limit of "+strconv.Itoa(lim.maxBytes),
			[2]string{"bytes", strconv.Itoa(len(src))}, [2]string{"limit", strconv.Itoa(lim.maxBytes)})
	}
	src = strings.TrimPrefix(src, "\xEF\xBB\xBF")
	d := xml.NewDecoder(strings.NewReader(src))
	d.Strict = true
	if htmlEntities {
		d.Entity = xml.HTMLEntity
	}
	badCharset := ""
	d.CharsetReader = xmlCharsetReader(&badCharset)

	var root *xmlNode
	var stack []*xmlNode
	nodes := 0
	malformed := func(msg string) *xmlFailure {
		line, col := d.InputPos()
		return xmlFail(xmlCodeMalformed,
			"Xml.parse: malformed XML at line "+strconv.Itoa(line)+", column "+strconv.Itoa(col)+": "+msg,
			[2]string{"line", strconv.Itoa(line)}, [2]string{"column", strconv.Itoa(col)})
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if badCharset != "" {
				return nil, xmlFail(xmlCodeCharset,
					"Xml.parse: the encoding "+strconv.Quote(badCharset)+" is not supported (UTF-8, US-ASCII, ISO-8859-1 and Windows-1252 are)",
					[2]string{"encoding", badCharset})
			}
			msg := err.Error()
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				msg = se.Msg
			}
			return nil, malformed(msg)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if root != nil && len(stack) == 0 {
				return nil, malformed("more than one root element")
			}
			if len(stack)+1 > lim.maxDepth {
				return nil, xmlFail(xmlCodeTooDeep,
					"Xml.parse: elements are nested more than "+strconv.Itoa(lim.maxDepth)+" deep",
					[2]string{"limit", strconv.Itoa(lim.maxDepth)})
			}
			nodes++
			if nodes > lim.maxNodes {
				return nil, xmlFail(xmlCodeTooManyNodes,
					"Xml.parse: the document has more than "+strconv.Itoa(lim.maxNodes)+" nodes",
					[2]string{"limit", strconv.Itoa(lim.maxNodes)})
			}
			if len(t.Attr) > lim.maxAttributes {
				return nil, xmlFail(xmlCodeTooManyAttributes,
					"Xml.parse: the element <"+t.Name.Local+"> has "+strconv.Itoa(len(t.Attr))+" attributes, more than the limit of "+strconv.Itoa(lim.maxAttributes),
					[2]string{"element", t.Name.Local}, [2]string{"limit", strconv.Itoa(lim.maxAttributes)})
			}
			n := &xmlNode{name: t.Name.Local, space: t.Name.Space}
			for _, a := range t.Attr {
				// Namespace declarations are not attributes of the data.
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				n.attrs = append(n.attrs, xmlAttr{name: a.Name.Local, space: a.Name.Space, value: a.Value})
			}
			if len(stack) == 0 {
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, malformed("unexpected end element </" + t.Name.Local + ">")
			}
			xmlFinishText(stack[len(stack)-1])
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if !xmlIsSpace(string(t)) {
					return nil, malformed("text outside the root element")
				}
				continue
			}
			p := stack[len(stack)-1]
			if k := len(p.children); k > 0 && p.children[k-1].isText {
				p.children[k-1].sb.Write(t)
				continue
			}
			nodes++
			if nodes > lim.maxNodes {
				return nil, xmlFail(xmlCodeTooManyNodes,
					"Xml.parse: the document has more than "+strconv.Itoa(lim.maxNodes)+" nodes",
					[2]string{"limit", strconv.Itoa(lim.maxNodes)})
			}
			tn := &xmlNode{isText: true, sb: &strings.Builder{}}
			tn.sb.Write(t)
			p.children = append(p.children, tn)
		case xml.Directive:
			return nil, xmlFail(xmlCodeDoctype,
				"Xml.parse: a DOCTYPE or other directive is not allowed (no DTD, no entity definitions)")
		case xml.Comment, xml.ProcInst:
			// Comments and processing instructions carry no data.
		}
	}
	if len(stack) != 0 {
		return nil, malformed("unexpected end of input inside <" + stack[len(stack)-1].name + ">")
	}
	if root == nil {
		return nil, malformed("no root element")
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
