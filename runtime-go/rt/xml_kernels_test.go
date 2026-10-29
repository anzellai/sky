package rt

import (
	"reflect"
	"strings"
	"testing"
)

// xmlCode parses src (Strict, default limits) and returns the domain code of
// the failure, or "" on success.
func xmlCode(t *testing.T, src string) string {
	t.Helper()
	_, f := xmlParseSafe(src, false, xmlDefaultLimits)
	if f == nil {
		return ""
	}
	// The failure must reach Sky as a domain Error with the same code.
	df, ok := DomainOf(f.skyError())
	if !ok || df.Domain != "xml" || df.Code != f.code {
		t.Fatalf("failure %q is not a domain error in \"xml\": %+v %v", f.code, df, ok)
	}
	return f.code
}

func TestXmlRefusesBillionLaughs(t *testing.T) {
	src := `<?xml version="1.0"?>
<!DOCTYPE lolz [
 <!ENTITY lol "lol">
 <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
 <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
]>
<lolz>&lol3;</lolz>`
	if c := xmlCode(t, src); c != xmlCodeDoctype {
		t.Fatalf("billion laughs: got %q, want doctype", c)
	}
}

func TestXmlRefusesExternalEntity(t *testing.T) {
	src := `<?xml version="1.0"?>
<!DOCTYPE foo [ <!ENTITY xxe SYSTEM "file:///etc/passwd"> ]>
<foo>&xxe;</foo>`
	if c := xmlCode(t, src); c != xmlCodeDoctype {
		t.Fatalf("XXE: got %q, want doctype", c)
	}
	// An entity that was never defined (no DTD) is malformed, never resolved.
	if c := xmlCode(t, `<foo>&xxe;</foo>`); c != xmlCodeMalformed {
		t.Fatalf("undefined entity: got %q, want malformed", c)
	}
}

func TestXmlRefusesPlainDoctype(t *testing.T) {
	if c := xmlCode(t, `<!DOCTYPE html><html/>`); c != xmlCodeDoctype {
		t.Fatalf("DOCTYPE: got %q, want doctype", c)
	}
}

func TestXmlRefusesDepth10000(t *testing.T) {
	src := strings.Repeat("<a>", 10_000) + strings.Repeat("</a>", 10_000)
	if c := xmlCode(t, src); c != xmlCodeTooDeep {
		t.Fatalf("depth 10,000: got %q, want too_deep", c)
	}
	ok := strings.Repeat("<a>", 256) + strings.Repeat("</a>", 256)
	if c := xmlCode(t, ok); c != "" {
		t.Fatalf("depth 256 is within the default limit: got %q", c)
	}
}

func TestXmlNodeLimitOfOneMillion(t *testing.T) {
	// 1,000,000 nodes in all (the root and 999,999 children) parse.
	ok := "<r>" + strings.Repeat("<a/>", 999_999) + "</r>"
	if c := xmlCode(t, ok); c != "" {
		t.Fatalf("1,000,000 nodes: got %q, want success", c)
	}
	over := "<r>" + strings.Repeat("<a/>", 1_000_000) + "</r>"
	if c := xmlCode(t, over); c != xmlCodeTooManyNodes {
		t.Fatalf("1,000,001 nodes: got %q, want too_many_nodes", c)
	}
}

func TestXmlSizeAndAttributeLimits(t *testing.T) {
	lim := xmlLimits{maxBytes: 40, maxDepth: 8, maxNodes: 100, maxAttributes: 2}
	if _, f := xmlParseSafe("<a>"+strings.Repeat("x", 60)+"</a>", false, lim); f == nil || f.code != xmlCodeTooLarge {
		t.Fatalf("size: got %+v, want too_large", f)
	}
	if _, f := xmlParseSafe(`<a x="1" y="2" z="3"/>`, false, lim); f == nil || f.code != xmlCodeTooManyAttributes {
		t.Fatalf("attributes: got %+v, want too_many_attributes", f)
	}
	// Namespace declarations do not count as data attributes, but they do
	// count against the limit (the parser sees them).
	if _, f := xmlParseSafe(`<a x="1" y="2"/>`, false, lim); f != nil {
		t.Fatalf("two attributes are within the limit: %+v", f)
	}
}

func TestXmlMalformedGivesLineAndColumn(t *testing.T) {
	_, f := xmlParseSafe("<a>\n  <b>\n</a>", false, xmlDefaultLimits)
	if f == nil || f.code != xmlCodeMalformed {
		t.Fatalf("got %+v, want malformed", f)
	}
	if !strings.Contains(f.msg, "line 3") {
		t.Fatalf("message has no line: %q", f.msg)
	}
	df, _ := DomainOf(f.skyError())
	if l, _ := df.Attr("line"); l != "3" {
		t.Fatalf("line attribute: %q", l)
	}
	if c, ok := df.Attr("column"); !ok || c == "" || c == "0" {
		t.Fatalf("column attribute: %q", c)
	}
	for _, bad := range []string{"", "  ", "<a/><b/>", "<a>", "text<a/>", "<a></b>"} {
		if c := xmlCode(t, bad); c != xmlCodeMalformed {
			t.Errorf("%q: got %q, want malformed", bad, c)
		}
	}
}

func TestXmlNamespacedAtomByLocalName(t *testing.T) {
	src := `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
  <title>Example</title>
  <entry><title>One</title><media:thumbnail url="a.png"/></entry>
  <entry><title>Two</title></entry>
</feed>`
	res := Xml_parseT(src)
	if res.Tag != 0 {
		t.Fatalf("parse failed: %v", res.ErrValue)
	}
	root := res.OkValue
	if Xml_nameT(root) != "feed" || Xml_namespaceT(root) != "http://www.w3.org/2005/Atom" {
		t.Fatalf("root: %q %q", Xml_nameT(root), Xml_namespaceT(root))
	}
	if len(Xml_attributesT(root)) != 0 {
		t.Fatalf("namespace declarations are not attributes: %v", Xml_attributesT(root))
	}
	entries := Xml_elementsNamedT("entry", root)
	if len(entries) != 2 {
		t.Fatalf("entries: %d", len(entries))
	}
	thumb := Xml_elementsNamedT("thumbnail", entries[0])
	if len(thumb) != 1 || Xml_namespaceT(thumb[0]) != "http://search.yahoo.com/mrss/" {
		t.Fatalf("thumbnail: %v", thumb)
	}
	if v := Xml_attributeT("url", thumb[0]); v.Tag != 0 || v.JustValue != "a.png" {
		t.Fatalf("url: %+v", v)
	}
	if Xml_textT(Xml_elementsNamedT("title", entries[1])[0]) != "Two" {
		t.Fatal("second title")
	}
}

func TestXmlLatin1AndWindows1252Feeds(t *testing.T) {
	src := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><t>caf\xe9</t>"
	res := Xml_parseT(src)
	if res.Tag != 0 || Xml_textT(res.OkValue) != "café" {
		t.Fatalf("latin-1: %+v", res)
	}
	src = "<?xml version=\"1.0\" encoding=\"windows-1252\"?><t>\x93quoted\x94 \x80</t>"
	res = Xml_parseT(src)
	if res.Tag != 0 || Xml_textT(res.OkValue) != "“quoted” €" {
		t.Fatalf("windows-1252: %+v", res)
	}
	if c := xmlCode(t, "<?xml version=\"1.0\" encoding=\"EBCDIC-US\"?><t/>"); c != xmlCodeCharset {
		t.Fatalf("EBCDIC: got %q, want charset", c)
	}
	// A UTF-8 byte order mark is stripped.
	if res := Xml_parseT("\xEF\xBB\xBF<t>x</t>"); res.Tag != 0 || Xml_textT(res.OkValue) != "x" {
		t.Fatalf("BOM: %+v", res)
	}
}

func TestXmlNbspOnlyInHtmlEntitiesMode(t *testing.T) {
	src := `<p>a&nbsp;b &amp; &#233;</p>`
	if c := xmlCode(t, src); c != xmlCodeMalformed {
		t.Fatalf("strict mode: got %q, want malformed", c)
	}
	n, f := xmlParseSafe(src, true, xmlDefaultLimits)
	if f != nil || xmlTextOf(n) != "a b & é" {
		t.Fatalf("HtmlEntities: %+v %q", f, xmlTextOf(n))
	}
	r := Xml_parseWith(true, 1<<20, 16, 100, 8, src).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("parseWith HtmlEntities: %+v", r)
	}
	if r := Xml_parseWith(false, 0, 16, 100, 8, src).(SkyResult[any, any]); r.Tag == 0 {
		t.Fatal("a zero limit must be refused")
	}
}

func TestXmlTextCollectsCdataAndDescendants(t *testing.T) {
	n, f := xmlParseSafe(`<a>x<b>y<![CDATA[<z>]]></b>w<!-- c --></a>`, false, xmlDefaultLimits)
	if f != nil {
		t.Fatal(f)
	}
	if got := xmlTextOf(n); got != "xy<z>w" {
		t.Fatalf("text: %q", got)
	}
	if len(n.children) != 3 {
		t.Fatalf("children (text, element, text): %d", len(n.children))
	}
	// A long text run is collected in linear time.
	long := "<a>" + strings.Repeat("&amp;x", 200_000) + "</a>"
	n, f = xmlParseSafe(long, false, xmlDefaultLimits)
	if f != nil || len(xmlTextOf(n)) != 400_000 {
		t.Fatalf("long text: %v %d", f, len(xmlTextOf(n)))
	}
}

func TestXmlTwinsAgreeWithErasedKernels(t *testing.T) {
	res := Xml_parseT(`<r xmlns="urn:x" a="1" b="2">t<c/>u<c>v</c></r>`)
	n := res.OkValue
	erased := Xml_parse(`<r xmlns="urn:x" a="1" b="2">t<c/>u<c>v</c></r>`).(SkyResult[any, any])
	if erased.Tag != 0 {
		t.Fatal(erased)
	}
	if Xml_name(n) != Xml_nameT(n) || Xml_namespace(n) != Xml_namespaceT(n) || Xml_text(n) != Xml_textT(n) {
		t.Fatal("scalar twins differ")
	}
	attrs := Xml_attributes(n).([]any)
	tattrs := Xml_attributesT(n)
	if len(attrs) != len(tattrs) || len(attrs) != 2 {
		t.Fatalf("attributes: %v %v", attrs, tattrs)
	}
	for i := range attrs {
		a := attrs[i].(SkyTuple2)
		if a.V0 != tattrs[i].V0 || a.V1 != tattrs[i].V1 {
			t.Fatalf("attribute %d", i)
		}
	}
	if len(Xml_children(n).([]any)) != len(Xml_childrenT(n)) || len(Xml_childrenT(n)) != 4 {
		t.Fatal("children")
	}
	if len(Xml_elements(n).([]any)) != len(Xml_elementsT(n)) || len(Xml_elementsT(n)) != 2 {
		t.Fatal("elements")
	}
	if len(Xml_elementsNamed("c", n).([]any)) != len(Xml_elementsNamedT("c", n)) {
		t.Fatal("elementsNamed")
	}
	if !reflect.DeepEqual(Xml_attribute("b", n), MaybeAsAny(Xml_attributeT("b", n))) {
		t.Fatal("attribute")
	}
	if !reflect.DeepEqual(Xml_attribute("zz", n), MaybeAsAny(Xml_attributeT("zz", n))) {
		t.Fatal("missing attribute")
	}
	if Xml_encode(2, Xml_textNode("x")) != Xml_encodeT(2, Xml_textNode("x")) ||
		Xml_document(0, Xml_textNode("x")) != Xml_documentT(0, Xml_textNode("x")) {
		t.Fatal("encode twins")
	}
}

func TestXmlEncodeEscapesAndIndents(t *testing.T) {
	doc := Xml_element("note", []any{Xml_attr("id", `a"<&>`+"\n")}, []any{
		Xml_element("to", []any{}, []any{Xml_textNode("Tove & <Jani>")}),
		Xml_element("empty", []any{}, []any{}),
		Xml_element("body", []any{}, []any{Xml_cdata("x ]]> y")}),
	})
	got := Xml_encodeT(0, doc)
	want := `<note id="a&quot;&lt;&amp;&gt;&#xA;"><to>Tove &amp; &lt;Jani&gt;</to><empty/><body><![CDATA[x ]]]]><![CDATA[> y]]></body></note>`
	if got != want {
		t.Fatalf("encode 0:\n got %s\nwant %s", got, want)
	}
	got = Xml_encodeT(2, doc)
	want = "<note id=\"a&quot;&lt;&amp;&gt;&#xA;\">\n  <to>Tove &amp; &lt;Jani&gt;</to>\n  <empty/>\n  <body><![CDATA[x ]]]]><![CDATA[> y]]></body>\n</note>"
	if got != want {
		t.Fatalf("encode 2:\n got %s\nwant %s", got, want)
	}
	if d := Xml_documentT(0, Xml_element("a", []any{}, []any{})); d != "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<a/>" {
		t.Fatalf("document: %q", d)
	}
}

func TestXmlEncodeRoundTripsThroughParse(t *testing.T) {
	text := "a & b < c > d \"e\" 'f' \t\r\n é 😀 ]]> end"
	doc := Xml_element("r", []any{Xml_xmlns("urn:r"), Xml_xmlnsPrefix("p", "urn:p"), Xml_attr("k", text)}, []any{
		Xml_element("t", []any{}, []any{Xml_textNode(text)}),
		Xml_element("c", []any{}, []any{Xml_cdata(text)}),
		Xml_element("p:q", []any{}, []any{}),
	})
	for _, indent := range []int{0, 4} {
		res := Xml_parseT(Xml_documentT(indent, doc))
		if res.Tag != 0 {
			t.Fatalf("indent %d: %v", indent, res.ErrValue)
		}
		root := res.OkValue
		if Xml_namespaceT(root) != "urn:r" {
			t.Fatalf("namespace %q", Xml_namespaceT(root))
		}
		if v := Xml_attributeT("k", root); v.JustValue != text {
			t.Fatalf("attribute round trip: %q", v.JustValue)
		}
		if got := Xml_textT(Xml_elementsNamedT("t", root)[0]); got != text {
			t.Fatalf("text round trip: %q", got)
		}
		if got := Xml_textT(Xml_elementsNamedT("c", root)[0]); got != text {
			t.Fatalf("cdata round trip: %q", got)
		}
		q := Xml_elementsNamedT("q", root)
		if len(q) != 1 || Xml_namespaceT(q[0]) != "urn:p" {
			t.Fatalf("prefixed element: %v", q)
		}
	}
}

func TestXmlEncodeReplacesWhatXmlCannotCarry(t *testing.T) {
	doc := Xml_element("1 bad name", []any{Xml_attr("", "\x00")}, []any{Xml_textNode("a\x01b\xffc")})
	got := Xml_encodeT(0, doc)
	want := "<__bad_name _=\"�\">a�b�c</__bad_name>"
	if got != want {
		t.Fatalf("total encode:\n got %q\nwant %q", got, want)
	}
	if res := Xml_parseT(got); res.Tag != 0 {
		t.Fatalf("the total output parses: %v", res.ErrValue)
	}
	r := Xml_encodeStrict(0, doc).(SkyResult[any, any])
	if r.Tag == 0 {
		t.Fatal("encodeStrict must refuse a bad name")
	}
	if df, _ := DomainOf(r.ErrValue); df.Code != xmlCodeInvalidName {
		t.Fatalf("code %q", df.Code)
	}
	r = Xml_encodeStrict(0, Xml_element("ok", []any{}, []any{Xml_textNode("\x08")})).(SkyResult[any, any])
	if df, _ := DomainOf(r.ErrValue); r.Tag == 0 || df.Code != xmlCodeInvalidChar {
		t.Fatalf("encodeStrict bad char: %+v", r)
	}
	r = Xml_encodeStrict(2, Xml_element("ok", []any{}, []any{Xml_textNode("fine")})).(SkyResult[any, any])
	if r.Tag != 0 || r.OkValue != "<ok>fine</ok>" {
		t.Fatalf("encodeStrict ok: %+v", r)
	}
}

func TestXmlSitemapAndSamlAssertion(t *testing.T) {
	// Write a sitemap.xml.
	url := func(loc, mod string) any {
		return Xml_element("url", []any{}, []any{
			Xml_element("loc", []any{}, []any{Xml_textNode(loc)}),
			Xml_element("lastmod", []any{}, []any{Xml_textNode(mod)}),
		})
	}
	sitemap := Xml_documentT(2, Xml_element("urlset",
		[]any{Xml_xmlns("http://www.sitemaps.org/schemas/sitemap/0.9")},
		[]any{url("https://example.test/?a=1&b=2", "2026-10-29"), url("https://example.test/about", "2026-10-01")}))
	want := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>https://example.test/?a=1&amp;b=2</loc>
    <lastmod>2026-10-29</lastmod>
  </url>
  <url>
    <loc>https://example.test/about</loc>
    <lastmod>2026-10-01</lastmod>
  </url>
</urlset>`
	if sitemap != want {
		t.Fatalf("sitemap:\n%s", sitemap)
	}
	// Read a SAML-style namespaced assertion by local name.
	saml := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="r1">
  <saml:Assertion ID="a1"><saml:Subject><saml:NameID>ada@example.test</saml:NameID></saml:Subject>
  <saml:AttributeStatement><saml:Attribute Name="role"><saml:AttributeValue>admin</saml:AttributeValue></saml:Attribute></saml:AttributeStatement>
  </saml:Assertion></samlp:Response>`
	res := Xml_parseT(saml)
	if res.Tag != 0 {
		t.Fatal(res.ErrValue)
	}
	a := Xml_elementsNamedT("Assertion", res.OkValue)[0]
	if Xml_namespaceT(a) != "urn:oasis:names:tc:SAML:2.0:assertion" {
		t.Fatal("assertion namespace")
	}
	nameID := Xml_elementsNamedT("NameID", Xml_elementsNamedT("Subject", a)[0])[0]
	if Xml_textT(nameID) != "ada@example.test" {
		t.Fatal("NameID")
	}
}
