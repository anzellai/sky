package rt

// office_package.go — what Std.Office.Xlsx and Std.Office.Docx share: the
// "office" error domain, the Open Packaging Conventions parts (relationships,
// content types, part names), the XML limits each part is read under, and
// the escaping the writers use.
//
// An Office file is a ZIP archive of XML parts. Both readers open it once
// with the limited zip reader (zip_safe.go, zipOpenArchive) and read each part
// with the one safe XML tokeniser (xml_safe.go): small parts (relationships,
// the workbook, styles, numbering) as a tree with xmlParseSafe, and the parts
// that grow with the data (worksheets, shared strings, the document body) as
// a stream with xmlStreamSafe, so no tree of a large part is ever built.
// Neither reads macros, external links or embedded objects: only the parts
// named here are opened.
//
// The Sky.Spa split classifies every `Office_` kernel as a server effect
// (spa_partition.rs EFFECT_KERNELS).

import (
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The error codes of the domain "office".
const (
	officeCodeSheetName          = "sheet_name"
	officeCodeNoVisibleSheet     = "no_visible_sheet"
	officeCodeTooManyRows        = "too_many_rows"
	officeCodeTooManyColumns     = "too_many_columns"
	officeCodeTooManyCells       = "too_many_cells"
	officeCodeTooManySheets      = "too_many_sheets"
	officeCodeTooManyBlocks      = "too_many_blocks"
	officeCodeValidationTooLong  = "validation_too_long"
	officeCodeValidationItem     = "validation_item"
	officeCodeRange              = "range"
	officeCodeColumnWidth        = "column_width"
	officeCodeDateRange          = "date_range"
	officeCodeTextTooLong        = "text_too_long"
	officeCodeNumber             = "number"
	officeCodeFormula            = "formula"
	officeCodeFailureValue       = "failure_value"
	officeCodeUnsafeLink         = "unsafe_link"
	officeCodeHeadingLevel       = "heading_level"
	officeCodeListLevel          = "list_level"
	officeCodeNotOffice          = "not_office"
	officeCodeCorrupt            = "corrupt"
	officeRelOfficeDocumentType  = "/officeDocument"
	officeNsRelationships        = "http://schemas.openxmlformats.org/package/2006/relationships"
	officeNsDocRelationships     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	officeNsDocRelationshipsBase = "officeDocument/2006/relationships"
)

// officeError is the Sky `Error` for a failure in the domain "office": kind
// InvalidInput, the code and attributes given.
func officeError(code, msg string, attrs ...[2]string) any {
	df := DomainFailure{Domain: "office", Code: code, RetryAfterMs: -1, Attrs: attrs}
	return ErrDomain(errKindInvalidInput, msg, df)
}

// officePartLimits is how one XML part is read: as large as the zip limits
// let the part inflate, at most depth elements deep, and (for a part read as
// a tree) at most nodes nodes. A streamed part passes a large node bound: its
// size is bounded by the counts the reader keeps itself (cells, blocks).
func officePartLimits(zl zipLimits, depth, nodes int) xmlLimits {
	maxBytes := int(zl.maxEntryBytes)
	if maxBytes <= 0 || int64(maxBytes) != zl.maxEntryBytes {
		maxBytes = int(^uint(0) >> 1)
	}
	return xmlLimits{maxBytes: maxBytes, maxDepth: depth, maxNodes: nodes, maxAttributes: 256}
}

// officeTreeNodes bounds a part read as a tree (relationships, the workbook,
// styles, numbering): one million nodes, as Std.Xml's default.
const officeTreeNodes = 1_000_000

// officeStreamNodes is the node bound of a streamed part: effectively none,
// since the reader bounds what it keeps (cells, strings, blocks) itself.
const officeStreamNodes = int(^uint(0) >> 1)

// officePackage is an opened Office file.
type officePackage struct {
	zip   *zipArchive
	zlim  zipLimits
	depth int
}

func officeOpen(data string, zl zipLimits, depth int) (*officePackage, any) {
	a, fail := zipOpenArchive(data, zl)
	if fail != nil {
		return nil, fail.skyError()
	}
	return &officePackage{zip: a, zlim: zl, depth: depth}, nil
}

// part reads the named part as bytes; found is false when it is absent.
func (p *officePackage) part(name string) (string, bool, any) {
	b, found, fail := p.zip.read(name)
	if fail != nil {
		return "", false, fail.skyError()
	}
	return string(b), found, nil
}

// tree reads the named part as a tree; nil (with no error) when absent.
func (p *officePackage) tree(name string) (*xmlNode, any) {
	src, found, err := p.part(name)
	if err != nil || !found {
		return nil, err
	}
	root, f := xmlParseSafe(src, false, officePartLimits(p.zlim, p.depth, officeTreeNodes))
	if f != nil {
		return nil, officePartFailure(name, f)
	}
	return root, nil
}

// stream reads the named part through ev; found is false when absent.
func (p *officePackage) stream(name string, ev xmlEvents) (bool, any) {
	src, found, err := p.part(name)
	if err != nil || !found {
		return found, err
	}
	if f := xmlStreamSafe(src, false, officePartLimits(p.zlim, p.depth, officeStreamNodes), ev); f != nil {
		return true, officePartFailure(name, f)
	}
	return true, nil
}

// officePartFailure keeps an XML failure in the domain "xml" (its code says
// what was refused) and names the part it was found in.
func officePartFailure(part string, f *xmlFailure) any {
	attrs := append([][2]string{{"part", part}}, f.attrs...)
	return xmlDomainError(f.code, "Office: the part "+part+": "+f.msg, attrs)
}

// officeRel is one relationship of a part.
type officeRel struct {
	id       string
	typ      string // the type URI
	target   string // resolved to a part name, or the URI when external
	external bool
}

// officeRelsName is the relationships part of the part name ("" = the
// package): word/document.xml -> word/_rels/document.xml.rels.
func officeRelsName(part string) string {
	if part == "" {
		return "_rels/.rels"
	}
	dir, file := path.Split(part)
	return dir + "_rels/" + file + ".rels"
}

// officeResolve turns a relationship target into a part name: an absolute
// target is from the package root, a relative one from the source's folder.
func officeResolve(source, target string) string {
	if strings.HasPrefix(target, "/") {
		return path.Clean(strings.TrimPrefix(target, "/"))
	}
	return path.Clean(path.Join(path.Dir(source), target))
}

// rels reads the relationships of part (absent relationships are none).
func (p *officePackage) rels(part string) (map[string]officeRel, []officeRel, any) {
	root, err := p.tree(officeRelsName(part))
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]officeRel{}
	var list []officeRel
	if root == nil {
		return byID, list, nil
	}
	for _, c := range root.children {
		if c.isText || c.name != "Relationship" {
			continue
		}
		r := officeRel{id: xmlAttrLocal(c, "Id"), typ: xmlAttrLocal(c, "Type")}
		target := xmlAttrLocal(c, "Target")
		r.external = strings.EqualFold(xmlAttrLocal(c, "TargetMode"), "External")
		if r.external {
			r.target = target
		} else {
			r.target = officeResolve(part, target)
		}
		byID[r.id] = r
		list = append(list, r)
	}
	return byID, list, nil
}

// mainPart is the target of the package's officeDocument relationship, or
// fallback when the package has none (a file some writers make).
func (p *officePackage) mainPart(fallback string) (string, any) {
	_, list, err := p.rels("")
	if err != nil {
		return "", err
	}
	for _, r := range list {
		if strings.HasSuffix(r.typ, officeRelOfficeDocumentType) && !r.external {
			return r.target, nil
		}
	}
	return fallback, nil
}

// xmlAttrLocal is the value of the attribute with the local name (any
// namespace), or "" (also for a nil node).
func xmlAttrLocal(n *xmlNode, local string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.attrs {
		if a.name == local {
			return a.value
		}
	}
	return ""
}

// officeAttr is the same over a streamed attribute list.
func officeAttr(attrs []xmlAttr, local string) string {
	for _, a := range attrs {
		if a.name == local {
			return a.value
		}
	}
	return ""
}

// officeAttrHas reports whether the attribute is present.
func officeAttrHas(attrs []xmlAttr, local string) bool {
	for _, a := range attrs {
		if a.name == local {
			return true
		}
	}
	return false
}

// officeRelID is the r:id of an element (a relationships-namespace "id").
func officeRelID(attrs []xmlAttr) string {
	for _, a := range attrs {
		if a.name == "id" && strings.Contains(a.space, "relationships") {
			return a.value
		}
	}
	return ""
}

// officeOn reads an OOXML boolean property (`<w:b/>`, `<w:b w:val="0"/>`):
// present means on unless val is false, 0 or off.
func officeOn(attrs []xmlAttr) bool {
	switch strings.ToLower(officeAttr(attrs, "val")) {
	case "0", "false", "off", "none":
		return false
	}
	return true
}

// officeChildren lists the element children of n with the local name.
func officeChildren(n *xmlNode, local string) []*xmlNode {
	var out []*xmlNode
	if n == nil {
		return out
	}
	for _, c := range n.children {
		if !c.isText && c.name == local {
			out = append(out, c)
		}
	}
	return out
}

// officeChild is the first element child of n with the local name, or nil.
func officeChild(n *xmlNode, local string) *xmlNode {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if !c.isText && c.name == local {
			return c
		}
	}
	return nil
}

// officeXml writes an Office part with the shared XML writer's escaping.
type officeXml struct{ w xmlWriter }

func (o *officeXml) raw(s string) { o.w.b.WriteString(s) }

// text writes character data, each character XML cannot carry replaced by
// U+FFFD (Std.Xml.Encode's total encode).
func (o *officeXml) text(s string) { o.w.escape(s, false) }

// attr writes ` name="value"`.
func (o *officeXml) attr(name, value string) {
	o.w.b.WriteByte(' ')
	o.w.b.WriteString(name)
	o.w.b.WriteString(`="`)
	o.w.escape(value, true)
	o.w.b.WriteByte('"')
}

func (o *officeXml) int(name string, v int) { o.attr(name, strconv.Itoa(v)) }

func (o *officeXml) String() string { return o.w.b.String() }

const officeXmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// officeContentTypes writes [Content_Types].xml: the rels and xml defaults
// and one override per part.
func officeContentTypes(overrides [][2]string) string {
	var o officeXml
	o.raw(officeXmlDecl)
	o.raw(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	o.raw(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	o.raw(`<Default Extension="xml" ContentType="application/xml"/>`)
	for _, ov := range overrides {
		o.raw(`<Override`)
		o.attr("PartName", "/"+ov[0])
		o.attr("ContentType", ov[1])
		o.raw(`/>`)
	}
	o.raw(`</Types>`)
	return o.String()
}

// officeRelsXml writes a relationships part. Each rel is {Id, Type, Target,
// external}.
func officeRelsXml(rels []officeRel) string {
	var o officeXml
	o.raw(officeXmlDecl)
	o.raw(`<Relationships xmlns="` + officeNsRelationships + `">`)
	for _, r := range rels {
		o.raw(`<Relationship`)
		o.attr("Id", r.id)
		o.attr("Type", r.typ)
		o.attr("Target", r.target)
		if r.external {
			o.attr("TargetMode", "External")
		}
		o.raw(`/>`)
	}
	o.raw(`</Relationships>`)
	return o.String()
}

// officeRuneCount is the number of characters of s (invalid UTF-8 bytes count
// one each).
func officeRuneCount(s string) int { return utf8.RuneCountInString(s) }
