package rt

import (
	"bytes"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
)

func dRun(text string) docxRun { return docxRun{text: text} }

func dPara(runs ...docxRun) docxBlock {
	if runs == nil {
		runs = []docxRun{}
	}
	return docxBlock{kind: docxKindParagraph, runs: runs}
}

func dTable(rows ...[][]docxBlock) docxBlock { return docxBlock{kind: docxKindTable, rows: rows} }

func docxRoundTrip(t *testing.T, blocks []docxBlock) ([]docxBlock, string) {
	t.Helper()
	in := make([]any, len(blocks))
	for i, b := range blocks {
		in[i] = docxBlockADT(b)
	}
	w := Office_docxWrite(in).(SkyResult[any, any])
	if w.Tag != 0 {
		t.Fatalf("write: %v", officeMessage(w.ErrValue))
	}
	data := w.OkValue.(string)
	docxValidate(t, data)
	out, err := docxReadAll(data, zipDefaultLimits, 1_000_000, 256)
	if err != nil {
		t.Fatalf("read: %v", officeMessage(err))
	}
	return out, data
}

func docxNorm(b []docxBlock) []docxBlock {
	out := make([]docxBlock, len(b))
	for i, x := range b {
		if x.runs == nil {
			x.runs = []docxRun{}
		}
		if x.kind == docxKindTable {
			rows := make([][][]docxBlock, len(x.rows))
			for r, row := range x.rows {
				cells := make([][]docxBlock, len(row))
				for c, cell := range row {
					cells[c] = docxNorm(cell)
				}
				rows[r] = cells
			}
			x.rows = rows
		} else {
			x.rows = nil
		}
		out[i] = x
	}
	return out
}

// docxValidate is the second reader for a written document: encoding/xml
// struct decoding, independent of the streaming reader, against what Word
// needs to open a file without repairing it — every part typed and every
// relationship target present, the body closing with sectPr, every table
// cell ending in a paragraph, every hyperlink id an external relationship,
// every numId defined in numbering.xml and every style id in styles.xml.
func docxValidate(t *testing.T, data string) {
	t.Helper()
	parts := ooParts(t, data)
	ooCheckPackage(t, parts)
	var rels ooRels
	ooUnmarshal(t, parts, "word/_rels/document.xml.rels", &rels)
	external := map[string]bool{}
	for _, r := range rels.Rels {
		external[r.ID] = r.TargetMode == "External"
	}
	var styles struct {
		Styles []struct {
			ID string `xml:"styleId,attr"`
		} `xml:"style"`
	}
	ooUnmarshal(t, parts, "word/styles.xml", &styles)
	styleIDs := map[string]bool{}
	for _, s := range styles.Styles {
		styleIDs[s.ID] = true
	}
	var numbering struct {
		Nums []struct {
			ID string `xml:"numId,attr"`
		} `xml:"num"`
	}
	ooUnmarshal(t, parts, "word/numbering.xml", &numbering)
	numIDs := map[string]bool{}
	for _, n := range numbering.Nums {
		numIDs[n.ID] = true
	}
	d := xml.NewDecoder(bytes.NewReader(parts["word/document.xml"]))
	var stack []string
	var lastChild []string // the last child element of each open element
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if len(lastChild) > 0 {
				lastChild[len(lastChild)-1] = e.Name.Local
			}
			stack = append(stack, e.Name.Local)
			lastChild = append(lastChild, "")
			attr := func(local string) string {
				for _, a := range e.Attr {
					if a.Name.Local == local {
						return a.Value
					}
				}
				return ""
			}
			switch e.Name.Local {
			case "hyperlink":
				if id := attr("id"); id != "" && !external[id] {
					t.Errorf("second reader: hyperlink %s is not an external relationship", id)
				}
			case "numId":
				if !numIDs[attr("val")] {
					t.Errorf("second reader: numId %s is not in numbering.xml", attr("val"))
				}
			case "pStyle", "rStyle", "tblStyle":
				if !styleIDs[attr("val")] {
					t.Errorf("second reader: style %s is not in styles.xml", attr("val"))
				}
			}
		case xml.EndElement:
			last := lastChild[len(lastChild)-1]
			switch e.Name.Local {
			case "tc":
				if last != "p" {
					t.Errorf("second reader: a table cell ends with <%s>, not a paragraph", last)
				}
			case "body":
				if last != "sectPr" {
					t.Errorf("second reader: the body ends with <%s>, not sectPr", last)
				}
			case "tbl":
				if !strings.Contains(string(parts["word/document.xml"]), "<w:tblGrid>") {
					t.Error("second reader: a table has no tblGrid")
				}
			}
			stack = stack[:len(stack)-1]
			lastChild = lastChild[:len(lastChild)-1]
		}
	}
}

func TestDocxRoundTripEveryBlockKind(t *testing.T) {
	link := func(text, to string) docxRun { return docxRun{text: text, link: to} }
	in := []docxBlock{
		{kind: docxKindHeading, level: 0, runs: []docxRun{dRun("Invoice 2026-041")}},
		{kind: docxKindHeading, level: 1, runs: []docxRun{dRun("Summary")}},
		{kind: docxKindHeading, level: 9, runs: []docxRun{dRun("Deep")}},
		dPara(dRun("Plain, "), docxRun{text: "bold", bold: true}, dRun(" and "), docxRun{text: "italic", italic: true},
			docxRun{text: " both", bold: true, italic: true}),
		dPara(dRun("tab\there\nline two & <angle> \"quotes\" ünïcødé 😀")),
		dPara(),
		dPara(dRun("Pay at "), link("example.com", "https://example.com/pay?a=1&b=2"), dRun(", ask "),
			link("us", "mailto:billing@example.com"), dRun(", see "), link("terms", "#terms")),
		{kind: docxKindBulleted, level: 0, runs: []docxRun{dRun("one")}},
		{kind: docxKindBulleted, level: 1, runs: []docxRun{dRun("one.a")}},
		{kind: docxKindNumbered, level: 0, runs: []docxRun{dRun("first")}},
		{kind: docxKindNumbered, level: 8, runs: []docxRun{dRun("ninth level")}},
		dPara(dRun("between")),
		{kind: docxKindNumbered, level: 0, runs: []docxRun{dRun("restarts")}},
		dTable(
			[][]docxBlock{{dPara(dRun("Item"))}, {dPara(docxRun{text: "Amount", bold: true})}},
			[][]docxBlock{{dPara(dRun("Design")), {kind: docxKindBulleted, runs: []docxRun{dRun("in a cell")}}}, {dPara(dRun("1,200.00"))}},
			[][]docxBlock{{}, {dTable([][]docxBlock{{dPara(dRun("nested"))}})}},
			[][]docxBlock{{dPara()}, {dPara(dRun("x")), dPara()}},
		),
		dPara(dRun("after the table")),
		dTable([][]docxBlock{{dPara(dRun("last block is a table"))}}),
	}
	got, data := docxRoundTrip(t, in)
	if !reflect.DeepEqual(docxNorm(got), docxNorm(in)) {
		t.Errorf("round trip:\n got %#v\nwant %#v", docxNorm(got), docxNorm(in))
	}
	// Each run of numbered items restarts at 1: two numbered lists, two nums.
	num := string(ooParts(t, data)["word/numbering.xml"])
	if strings.Count(num, "<w:startOverride") != 2 {
		t.Errorf("numbered runs: %d restarts", strings.Count(num, "<w:startOverride"))
	}
	// The same blocks give the same bytes.
	_, again := docxRoundTrip(t, in)
	if again != data {
		t.Error("two writes differ")
	}
}

func TestDocxWriteRefusals(t *testing.T) {
	write := func(b docxBlock) string {
		_, code := officeCodeOf(t, Office_docxWrite([]any{docxBlockADT(b)}))
		return code
	}
	for _, to := range []string{"javascript:alert(1)", "file:///etc/passwd", "relative/page.html", "ftp://example.com", "data:text/html,x", "#", "http:", "vbscript:x"} {
		if c := write(dPara(docxRun{text: "x", link: to})); c != officeCodeUnsafeLink {
			t.Errorf("link %q: code %q", to, c)
		}
	}
	if c := write(dTable([][]docxBlock{{dPara(docxRun{text: "x", link: "javascript:x"})}})); c != officeCodeUnsafeLink {
		t.Errorf("a link in a table: %q", c)
	}
	for _, to := range []string{"HTTPS://Example.com", "mailto:a@b.c", "#anchor"} {
		if c := write(dPara(docxRun{text: "x", link: to})); c != "" {
			t.Errorf("link %q refused: %q", to, c)
		}
	}
	if c := write(docxBlock{kind: docxKindHeading, level: 10, runs: []docxRun{dRun("x")}}); c != officeCodeHeadingLevel {
		t.Errorf("heading 10: %q", c)
	}
	if c := write(docxBlock{kind: docxKindBulleted, level: 9, runs: []docxRun{dRun("x")}}); c != officeCodeListLevel {
		t.Errorf("list level 9: %q", c)
	}
}

// ── files other programs write ─────────────────────────────────────────────

const docxCT = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`

const docxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`

func docxFixture(t *testing.T, document, rels, styles, numbering string) string {
	t.Helper()
	parts := [][2]string{{"[Content_Types].xml", docxCT}, {"_rels/.rels", docxRootRels}, {"word/document.xml", document}}
	if rels != "" {
		parts = append(parts, [2]string{"word/_rels/document.xml.rels", rels})
	}
	if styles != "" {
		parts = append(parts, [2]string{"word/styles.xml", styles})
	}
	if numbering != "" {
		parts = append(parts, [2]string{"word/numbering.xml", numbering})
	}
	return officeZip(t, parts...)
}

const wordNs = `xmlns:wpc="http://schemas.microsoft.com/office/word/2010/wordprocessingCanvas" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape" mc:Ignorable="w14 wp14"`

// A document in the shape Word writes: localized style ids (German), runs
// split by rsid and proofErr, a tracked insertion and deletion, a content
// control, a simple field, a text box in alternate content, a bookmark, a
// list through a list style, an outline level, and a nested table whose
// cell ends with the empty paragraph Word requires.
const wordDocument = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document ` + wordNs + `><w:body>
<w:p w:rsidR="00A1"><w:pPr><w:pStyle w:val="Titel"/></w:pPr><w:r><w:t>Quarterly</w:t></w:r><w:r w:rsidR="00B2"><w:t xml:space="preserve"> report</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="berschrift1"/><w:rPr><w:b/></w:rPr></w:pPr><w:bookmarkStart w:id="0" w:name="intro"/><w:r><w:t>Intro</w:t></w:r><w:bookmarkEnd w:id="0"/></w:p>
<w:p><w:r><w:t xml:space="preserve">Teh </w:t></w:r><w:proofErr w:type="spellStart"/><w:r><w:t>speling</w:t></w:r><w:proofErr w:type="spellEnd"/><w:r><w:rPr><w:b/><w:bCs/></w:rPr><w:t xml:space="preserve"> bold</w:t></w:r><w:r><w:rPr><w:b w:val="0"/></w:rPr><w:t xml:space="preserve"> not</w:t></w:r><w:del w:id="1" w:author="A"><w:r><w:delText>gone</w:delText></w:r></w:del><w:ins w:id="2" w:author="A"><w:r><w:t xml:space="preserve"> added</w:t></w:r></w:ins></w:p>
<w:sdt><w:sdtPr><w:alias w:val="Client"/></w:sdtPr><w:sdtContent><w:p><w:r><w:t>Client: </w:t></w:r><w:sdt><w:sdtContent><w:r><w:rPr><w:i/></w:rPr><w:t>ACME</w:t></w:r></w:sdtContent></w:sdt></w:p></w:sdtContent></w:sdt>
<w:p><w:r><w:t xml:space="preserve">Visit </w:t></w:r><w:hyperlink r:id="rId8" w:history="1"><w:r><w:rPr><w:rStyle w:val="Hyperlink"/></w:rPr><w:t>our site</w:t></w:r></w:hyperlink><w:r><w:t xml:space="preserve"> or page </w:t></w:r><w:fldSimple w:instr=" PAGE "><w:r><w:t>3</w:t></w:r></w:fldSimple><w:r><mc:AlternateContent><mc:Choice Requires="wps"><w:drawing><wp:inline><wps:txbx><w:txbxContent><w:p><w:r><w:t>TEXTBOX</w:t></w:r></w:p></w:txbxContent></wps:txbx></wp:inline></w:drawing></mc:Choice><mc:Fallback><w:pict><w:p><w:r><w:t>FALLBACK</w:t></w:r></w:p></w:pict></mc:Fallback></mc:AlternateContent></w:r><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> DATE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>today</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Aufzhlungszeichen"/></w:pPr><w:r><w:t>bullet via style</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Listenabsatz"/><w:numPr><w:ilvl w:val="1"/><w:numId w:val="5"/></w:numPr></w:pPr><w:r><w:t>numbered level 1</w:t></w:r></w:p>
<w:p><w:pPr><w:outlineLvl w:val="2"/></w:pPr><w:r><w:t>outline heading</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="berschrift1"/><w:outlineLvl w:val="9"/></w:pPr><w:r><w:t>body via outline 9</w:t></w:r></w:p>
<w:tbl><w:tblPr><w:tblStyle w:val="Tabellenraster"/></w:tblPr><w:tblGrid><w:gridCol w:w="4508"/><w:gridCol w:w="4508"/></w:tblGrid>
<w:tr><w:tc><w:tcPr><w:tcW w:w="4508" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:tcPr><w:gridSpan w:val="1"/></w:tcPr><w:tbl><w:tr><w:tc><w:p><w:r><w:t>inner</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:p/></w:tc></w:tr>
</w:tbl>
<w:p/>
<w:sectPr><w:headerReference w:type="default" r:id="rId9"/><w:pgSz w:w="11906" w:h="16838"/></w:sectPr>
</w:body></w:document>`

const wordRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId8" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/" TargetMode="External"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/><Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/></Relationships>`

const wordStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:type="paragraph" w:default="1" w:styleId="Standard"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="berschrift1"><w:name w:val="heading 1"/><w:basedOn w:val="Standard"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Titel"><w:name w:val="Title"/><w:basedOn w:val="Standard"/></w:style><w:style w:type="paragraph" w:styleId="Listenabsatz"><w:name w:val="List Paragraph"/></w:style><w:style w:type="paragraph" w:styleId="Aufzhlungszeichen"><w:name w:val="List Bullet"/><w:basedOn w:val="Standard"/><w:pPr><w:numPr><w:numId w:val="3"/></w:numPr></w:pPr></w:style></w:styles>`

const wordNumbering = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/><w:lvlText w:val=""/></w:lvl></w:abstractNum><w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl><w:lvl w:ilvl="1"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum><w:num w:numId="3"><w:abstractNumId w:val="0"/></w:num><w:num w:numId="5"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="1"><w:lvl w:ilvl="1"><w:numFmt w:val="lowerLetter"/></w:lvl></w:lvlOverride></w:num></w:numbering>`

func TestDocxReadWordShapedFile(t *testing.T) {
	data := docxFixture(t, wordDocument, wordRels, wordStyles, wordNumbering)
	got, err := docxReadAll(data, zipDefaultLimits, 1000, 256)
	if err != nil {
		t.Fatalf("read: %v", officeMessage(err))
	}
	want := []docxBlock{
		{kind: docxKindHeading, level: 0, runs: []docxRun{dRun("Quarterly report")}},
		{kind: docxKindHeading, level: 1, runs: []docxRun{dRun("Intro")}},
		dPara(dRun("Teh speling"), docxRun{text: " bold", bold: true}, dRun(" not added")),
		dPara(dRun("Client: "), docxRun{text: "ACME", italic: true}),
		dPara(dRun("Visit "), docxRun{text: "our site", link: "https://example.com/"}, dRun(" or page 3today")),
		{kind: docxKindBulleted, level: 0, runs: []docxRun{dRun("bullet via style")}},
		{kind: docxKindNumbered, level: 1, runs: []docxRun{dRun("numbered level 1")}},
		{kind: docxKindHeading, level: 3, runs: []docxRun{dRun("outline heading")}},
		dPara(dRun("body via outline 9")),
		dTable([][]docxBlock{{dPara(dRun("a"))}, {dTable([][]docxBlock{{dPara(dRun("inner"))}})}}),
	}
	if !reflect.DeepEqual(docxNorm(got), docxNorm(want)) {
		t.Errorf("Word file:\n got %#v\nwant %#v", docxNorm(got), docxNorm(want))
	}
}

// The shapes LibreOffice and Google Docs write: English style ids with
// "Heading 1" names, list numbering on the paragraph, a Title paragraph and
// no numbering part at all (a list item then reads as Bulleted).
func TestDocxReadLibreOfficeAndGoogleShapedFiles(t *testing.T) {
	libre := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:p><w:pPr><w:pStyle w:val="Heading2"/><w:rPr></w:rPr></w:pPr><w:r><w:rPr></w:rPr><w:t>Zwei</w:t></w:r></w:p><w:p><w:pPr><w:pStyle w:val="Normal"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr><w:r><w:t>Punkt</w:t></w:r></w:p><w:sectPr><w:type w:val="nextPage"/></w:sectPr></w:body></w:document>`
	libreStyles := `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="Heading 2"/><w:basedOn w:val="Heading"/></w:style><w:style w:type="paragraph" w:styleId="Heading"><w:name w:val="Heading"/></w:style></w:styles>`
	libreNum := `<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum><w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num></w:numbering>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/></Relationships>`
	got, err := docxReadAll(docxFixture(t, libre, rels, libreStyles, libreNum), zipDefaultLimits, 100, 64)
	if err != nil {
		t.Fatal(officeMessage(err))
	}
	want := []docxBlock{{kind: docxKindHeading, level: 2, runs: []docxRun{dRun("Zwei")}}, {kind: docxKindNumbered, runs: []docxRun{dRun("Punkt")}}}
	if !reflect.DeepEqual(docxNorm(got), docxNorm(want)) {
		t.Errorf("LibreOffice:\n got %#v\nwant %#v", got, want)
	}

	google := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:rPr><w:rtl w:val="0"/></w:rPr><w:t xml:space="preserve">Notes</w:t></w:r></w:p><w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`
	got, err = docxReadAll(docxFixture(t, google, "", "", ""), zipDefaultLimits, 100, 64)
	if err != nil {
		t.Fatal(officeMessage(err))
	}
	want = []docxBlock{{kind: docxKindHeading, level: 0, runs: []docxRun{dRun("Notes")}}, {kind: docxKindBulleted, runs: []docxRun{dRun("item")}}}
	if !reflect.DeepEqual(docxNorm(got), docxNorm(want)) {
		t.Errorf("Google Docs:\n got %#v\nwant %#v", got, want)
	}
}

func TestDocxReadLimitsAndHostileParts(t *testing.T) {
	doc := func(body string) string {
		return docxFixture(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+body+`</w:body></w:document>`, "", "", "")
	}
	cases := []struct {
		name      string
		data      string
		maxBlocks int
		maxDepth  int
		dom, code string
	}{
		{"too many blocks", doc(strings.Repeat(`<w:p><w:r><w:t>x</w:t></w:r></w:p>`, 11)), 10, 256, "office", officeCodeTooManyBlocks},
		{"blocks in cells count", doc(`<w:tbl>` + strings.Repeat(`<w:tr><w:tc><w:p/></w:tc></w:tr>`, 6) + `</w:tbl>`), 5, 256, "office", officeCodeTooManyBlocks},
		{"deep nesting", doc(strings.Repeat(`<w:tbl><w:tr><w:tc>`, 30) + strings.Repeat(`</w:tc></w:tr></w:tbl>`, 30)), 1000, 64, "xml", xmlCodeTooDeep},
		{"a DOCTYPE", docxFixture(t, `<!DOCTYPE w [<!ENTITY x SYSTEM "file:///etc/passwd">]><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`, "", "", ""), 1000, 256, "xml", xmlCodeDoctype},
		{"a DOCTYPE in styles", docxFixture(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`,
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`,
			`<!DOCTYPE s><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`, ""), 1000, 256, "xml", xmlCodeDoctype},
		{"no document part", officeZip(t, [2]string{"a.txt", "x"}), 1000, 256, "office", officeCodeNotOffice},
		{"a zip bomb", doc(`<w:p><w:r><w:t>` + strings.Repeat("a", 64<<20) + `</w:t></w:r></w:p>`), 1000, 256, "zip", zipCodeRatioExceeded},
	}
	z := zipDefaultLimits
	for _, c := range cases {
		r := Office_docxRead(z.maxEntries, int(z.maxEntryBytes), int(z.maxTotalBytes), int(z.maxRatio), c.maxBlocks, c.maxDepth, c.data)
		dom, code := officeCodeOf(t, r)
		if dom != c.dom || code != c.code {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, dom, code, c.dom, c.code)
		}
	}
	r := Office_docxRead(z.maxEntries, int(z.maxEntryBytes), int(z.maxTotalBytes), int(z.maxRatio), 0, 10, doc("")).(SkyResult[any, any])
	if r.Tag == 0 {
		t.Error("a zero limit was accepted")
	}
}
