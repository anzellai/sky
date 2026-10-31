package rt

// xml_kernels.go — the Std.Xml kernels over the tree xml_safe.go builds.
//
// Representation. A Std.Xml `Node` is the stdlib ADT `Node__Internal` whose
// one field holds the `*xmlNode` (the way a Regex holds its compiled
// expression). Sky code never matches on it: the constructor is not exported,
// so the type is opaque and `Codec.auto` refuses it. A node is read only, so
// one parsed tree may be shared by every request.
//
// No `//go:build !js` tag: Std.Codec imports Std.Xml and Codec is compiled for
// the Sky.Spa wasm client too. The linker dropping unreached functions is not
// enough to keep encoding/xml out of the client: Go runs the init of every
// imported package, so the import alone links it. The import therefore lives
// only in xml_stream.go (server only); the client's xml_stream_js.go returns a
// classified Unavailable Err. The Sky.Spa split classifies every `Xml_` kernel
// as a server effect (spa_partition.rs), so a correct build never reaches it.
// wasm_client_deps_test.go fails when encoding/xml reaches the js build again.
//
// Each accessor has a typed twin (the `T` suffix) that the lowering uses when
// the call's Go type is known (lower.rs `kernel_typed_return_twin`, doc 14
// §5.3), so a read narrows nothing at the call site. xml_kernels_test.go
// checks each twin against its erased kernel.

import "strconv"

const xmlNodeADTName = "Node__Internal"

func xmlNodeBox(n *xmlNode) SkyADT {
	return SkyADT{Tag: 0, SkyName: xmlNodeADTName, Fields: []any{n}}
}

// xmlNodeOf returns the node a Node carries. Every Node is made by a parse or
// a child read, so another shape cannot reach here from well-typed code; it
// reads as an empty text node rather than a panic.
func xmlNodeOf(v any) *xmlNode {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok && adt.SkyName == xmlNodeADTName && len(adt.Fields) == 1 {
		if n, ok := adt.Fields[0].(*xmlNode); ok && n != nil {
			return n
		}
	}
	if n, ok := v.(*xmlNode); ok && n != nil {
		return n
	}
	return &xmlNode{isText: true}
}

func xmlParseResultT(src string, html bool, lim xmlLimits) SkyResult[SkyADT, SkyADT] {
	root, f := xmlParseSafe(src, html, lim)
	if f != nil {
		return Err[SkyADT, SkyADT](f.skyError())
	}
	return Ok[SkyADT, SkyADT](xmlNodeBox(root))
}

func xmlResultAny(r SkyResult[SkyADT, SkyADT]) any {
	if r.Tag == 0 {
		return Ok[any, any](r.OkValue)
	}
	return Err[any, any](r.ErrValue)
}

// Xml_parse is `Std.Xml.parse : String -> Result Error Node` (Strict mode,
// default limits).
func Xml_parse(src any) any {
	return xmlResultAny(xmlParseResultT(AsString(src), false, xmlDefaultLimits))
}

// Xml_parseT is Xml_parse over a proven String, typed as the emitted
// `Result Error Node` (both ADTs are `= rt.SkyADT`).
func Xml_parseT(src string) SkyResult[SkyADT, SkyADT] {
	return xmlParseResultT(src, false, xmlDefaultLimits)
}

// Xml_parseWith is the primitive behind `Std.Xml.parseWith`: the Mode as a
// Bool (True = HtmlEntities) and the Limits record as four Ints, so the
// kernel boundary carries no Sky ADT or record.
func Xml_parseWith(html, maxBytes, maxDepth, maxNodes, maxAttributes, src any) any {
	lim := xmlLimits{
		maxBytes:      AsInt(maxBytes),
		maxDepth:      AsInt(maxDepth),
		maxNodes:      AsInt(maxNodes),
		maxAttributes: AsInt(maxAttributes),
	}
	if lim.maxBytes <= 0 || lim.maxDepth <= 0 || lim.maxNodes <= 0 || lim.maxAttributes <= 0 {
		return Err[any, any](ErrInvalidInput("Xml.parseWith: every limit must be positive, got maxBytes " +
			strconv.Itoa(lim.maxBytes) + ", maxDepth " + strconv.Itoa(lim.maxDepth) +
			", maxNodes " + strconv.Itoa(lim.maxNodes) + ", maxAttributes " + strconv.Itoa(lim.maxAttributes)))
	}
	return xmlResultAny(xmlParseResultT(AsString(src), AsBool(html), lim))
}

// Xml_name is `Std.Xml.name`: the local name ("" for a text node).
func Xml_name(n any) any     { return xmlNodeOf(n).name }
func Xml_nameT(n any) string { return xmlNodeOf(n).name }

// Xml_namespace is `Std.Xml.namespace`: the namespace URI ("" when none).
func Xml_namespace(n any) any     { return xmlNodeOf(n).space }
func Xml_namespaceT(n any) string { return xmlNodeOf(n).space }

// Xml_text is `Std.Xml.text`: all descendant text, concatenated.
func Xml_text(n any) any     { return xmlTextOf(xmlNodeOf(n)) }
func Xml_textT(n any) string { return xmlTextOf(xmlNodeOf(n)) }

// Xml_attributes is `Std.Xml.attributes`: (local name, value) pairs in
// document order. Namespace declarations are left out.
func Xml_attributes(n any) any {
	attrs := xmlNodeOf(n).attrs
	out := make([]any, len(attrs))
	for i, a := range attrs {
		out[i] = SkyTuple2{V0: a.name, V1: a.value}
	}
	return out
}

func Xml_attributesT(n any) []T2[string, string] {
	attrs := xmlNodeOf(n).attrs
	out := make([]T2[string, string], len(attrs))
	for i, a := range attrs {
		out[i] = T2[string, string]{V0: a.name, V1: a.value}
	}
	return out
}

// Xml_children is `Std.Xml.children`: every child node (elements and text)
// in document order.
func Xml_children(n any) any {
	kids := xmlNodeOf(n).children
	out := make([]any, len(kids))
	for i, c := range kids {
		out[i] = xmlNodeBox(c)
	}
	return out
}

func Xml_childrenT(n any) []SkyADT {
	kids := xmlNodeOf(n).children
	out := make([]SkyADT, len(kids))
	for i, c := range kids {
		out[i] = xmlNodeBox(c)
	}
	return out
}

// Xml_elements is the internal `Std.Xml.elements`: the child ELEMENTS (text
// left out). Std.Xml.Decode and Codec.fromXml use it.
func Xml_elements(n any) any {
	out := []any{}
	for _, c := range xmlNodeOf(n).children {
		if !c.isText {
			out = append(out, xmlNodeBox(c))
		}
	}
	return out
}

func Xml_elementsT(n any) []SkyADT {
	out := []SkyADT{}
	for _, c := range xmlNodeOf(n).children {
		if !c.isText {
			out = append(out, xmlNodeBox(c))
		}
	}
	return out
}

// Xml_elementsNamed is the internal `Std.Xml.elementsNamed`: the child
// elements with one local name, in document order. O(children).
func Xml_elementsNamed(name, n any) any {
	want := AsString(name)
	out := []any{}
	for _, c := range xmlNodeOf(n).children {
		if !c.isText && c.name == want {
			out = append(out, xmlNodeBox(c))
		}
	}
	return out
}

func Xml_elementsNamedT(name string, n any) []SkyADT {
	out := []SkyADT{}
	for _, c := range xmlNodeOf(n).children {
		if !c.isText && c.name == name {
			out = append(out, xmlNodeBox(c))
		}
	}
	return out
}

// Xml_attribute is the internal `Std.Xml.attribute`: the value of the first
// attribute with that local name.
func Xml_attribute(name, n any) any {
	want := AsString(name)
	for _, a := range xmlNodeOf(n).attrs {
		if a.name == want {
			return Just[any](a.value)
		}
	}
	return Nothing[any]()
}

func Xml_attributeT(name string, n any) SkyMaybe[string] {
	for _, a := range xmlNodeOf(n).attrs {
		if a.name == name {
			return Just[string](a.value)
		}
	}
	return Nothing[string]()
}
