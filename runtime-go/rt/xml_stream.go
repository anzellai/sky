//go:build !js

package rt

// xml_stream.go — the encoding/xml tokeniser behind Std.Xml (xml_safe.go has
// the tree, the limits and the failures).
//
// Server only. This file is the one place the runtime imports encoding/xml
// and golang.org/x/text/encoding/charmap. Go runs the package init of every
// package a binary imports, whatever the linker drops, so an import in a file
// the Sky.Spa client compiles puts the package (and archive/zip's flate
// writer, through its init) into every wasm client. The js build gets
// xml_stream_js.go instead, and runtime-go/rt/wasm_client_deps_test.go fails
// when a server-only package reaches the js import graph again.

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

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

// xmlStreamSafe is the one safe XML tokeniser: it reads src under lim and
// hands each element start, element end and text run to ev, without building
// a tree. Every refusal of the reader lives here (size, directives, charset,
// depth, nodes, attributes, one root, no text outside it), so xmlParseSafe
// (the tree behind Std.Xml) and a streaming reader (the worksheets and shared
// strings of Std.Office.Xlsx) refuse the same documents. The node count is
// the same as the tree's: an element is one node, and a run of text in one
// parent is one node however many tokens (or comments) split it.
func xmlStreamSafe(src string, htmlEntities bool, lim xmlLimits, ev xmlEvents) *xmlFailure {
	if len(src) > lim.maxBytes {
		return xmlFail(xmlCodeTooLarge,
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

	var open []string // the names of the open elements
	seenRoot := false
	lastText := false // the previous data token in this parent was text
	nodes := 0
	malformed := func(msg string) *xmlFailure {
		line, col := d.InputPos()
		return xmlFail(xmlCodeMalformed,
			"Xml.parse: malformed XML at line "+strconv.Itoa(line)+", column "+strconv.Itoa(col)+": "+msg,
			[2]string{"line", strconv.Itoa(line)}, [2]string{"column", strconv.Itoa(col)})
	}
	tooMany := func() *xmlFailure {
		return xmlFail(xmlCodeTooManyNodes,
			"Xml.parse: the document has more than "+strconv.Itoa(lim.maxNodes)+" nodes",
			[2]string{"limit", strconv.Itoa(lim.maxNodes)})
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if badCharset != "" {
				return xmlFail(xmlCodeCharset,
					"Xml.parse: the encoding "+strconv.Quote(badCharset)+" is not supported (UTF-8, US-ASCII, ISO-8859-1 and Windows-1252 are)",
					[2]string{"encoding", badCharset})
			}
			msg := err.Error()
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				msg = se.Msg
			}
			return malformed(msg)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth := len(open)
			if seenRoot && depth == 0 {
				return malformed("more than one root element")
			}
			if depth+1 > lim.maxDepth {
				return xmlFail(xmlCodeTooDeep,
					"Xml.parse: elements are nested more than "+strconv.Itoa(lim.maxDepth)+" deep",
					[2]string{"limit", strconv.Itoa(lim.maxDepth)})
			}
			nodes++
			if nodes > lim.maxNodes {
				return tooMany()
			}
			if len(t.Attr) > lim.maxAttributes {
				return xmlFail(xmlCodeTooManyAttributes,
					"Xml.parse: the element <"+t.Name.Local+"> has "+strconv.Itoa(len(t.Attr))+" attributes, more than the limit of "+strconv.Itoa(lim.maxAttributes),
					[2]string{"element", t.Name.Local}, [2]string{"limit", strconv.Itoa(lim.maxAttributes)})
			}
			var attrs []xmlAttr
			for _, a := range t.Attr {
				// Namespace declarations are not attributes of the data.
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				attrs = append(attrs, xmlAttr{name: a.Name.Local, space: a.Name.Space, value: a.Value})
			}
			seenRoot = true
			open = append(open, t.Name.Local)
			lastText = false
			if f := ev.start(t.Name.Local, t.Name.Space, attrs); f != nil {
				return f
			}
		case xml.EndElement:
			if len(open) == 0 {
				return malformed("unexpected end element </" + t.Name.Local + ">")
			}
			open = open[:len(open)-1]
			lastText = false
			if f := ev.end(); f != nil {
				return f
			}
		case xml.CharData:
			if len(open) == 0 {
				if !xmlIsSpace(string(t)) {
					return malformed("text outside the root element")
				}
				continue
			}
			if !lastText {
				nodes++
				if nodes > lim.maxNodes {
					return tooMany()
				}
			}
			lastText = true
			if f := ev.text(t); f != nil {
				return f
			}
		case xml.Directive:
			return xmlFail(xmlCodeDoctype,
				"Xml.parse: a DOCTYPE or other directive is not allowed (no DTD, no entity definitions)")
		case xml.Comment, xml.ProcInst:
			// Comments and processing instructions carry no data.
		}
	}
	if len(open) != 0 {
		return malformed("unexpected end of input inside <" + open[len(open)-1] + ">")
	}
	if !seenRoot {
		return malformed("no root element")
	}
	return nil
}
