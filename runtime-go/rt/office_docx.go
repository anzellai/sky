package rt

// office_docx.go — Std.Office.Docx.read / readWith / write.
//
// Reading. The document body is streamed (xmlStreamSafe), never built as a
// tree. styles.xml, numbering.xml and the relationships are small and read
// as trees. A paragraph is:
//
//   - a Heading when its style is a heading (style name "heading N" or id
//     "HeadingN", 1 to 9), the Title style (level 0), or has an outline level
//     (outlineLvl 0 to 8, on the paragraph or its style chain);
//   - else a ListItem when it has numbering (numPr on the paragraph or its
//     style): the level is ilvl, the kind comes from numbering.xml (numFmt
//     "bullet" is Bulleted, anything else Numbered);
//   - else a Paragraph.
//
// A run's text is its <w:t> (with <w:tab/> as a tab and <w:br/> as a line
// break); bold and italic come from its own <w:rPr>. Adjacent runs with the
// same bold, italic and link are joined, and empty runs dropped, because
// Word splits a run anywhere (spelling marks, revision ids). A hyperlink's
// target comes from document.xml.rels (an internal anchor reads as "#name").
// Tracked deletions (w:del, w:moveFrom) are skipped and insertions kept;
// content controls (w:sdt), smart tags and custom XML are flattened; drawings,
// text boxes, embedded objects and alternate content are never read. One
// trailing empty paragraph of the body and of each table cell is dropped
// (Word requires one after a table; the writer adds it back).
//
// Writing produces [Content_Types].xml, styles.xml (Normal, Title, Heading 1
// to 9, List Paragraph, Hyperlink, Table Grid), numbering.xml (a bullet and a
// decimal list; each run of numbered items restarts at 1) and the
// relationships. A link must be http, https or mailto, or an internal
// "#anchor": anything else is refused with the code unsafe_link.
//
// Representation. A `Block` holds `Run` records, which a kernel cannot build
// as the typed records Sky compares them with, so both directions use the
// private ADTs RawBlock / RawRun of Docx.sky and Sky maps them.

import (
	"strconv"
	"strings"
)

const (
	docxNsMain       = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	docxNsMainStrict = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	docxCtDocument   = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	docxCtStyles     = "application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"
	docxCtNumbering  = "application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"
	docxRelStyles    = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles"
	docxRelNumbering = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering"
	docxRelHyperlink = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink"
	docxRelOfficeDoc = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
)

// The RawBlock kinds (Docx.sky).
const (
	docxKindParagraph = 0
	docxKindHeading   = 1
	docxKindBulleted  = 2
	docxKindNumbered  = 3
	docxKindTable     = 4
)

// docxRun is one run of text.
type docxRun struct {
	text         string
	bold, italic bool
	link         string // "" = none
}

// docxBlock is one block as both directions hold it.
type docxBlock struct {
	kind  int
	level int
	runs  []docxRun
	rows  [][][]docxBlock
}

func docxRunADT(r docxRun) SkyADT {
	return SkyADT{Tag: 0, SkyName: "RawRun", Fields: []any{r.text, r.bold, r.italic, r.link}}
}

func docxBlockADT(b docxBlock) SkyADT {
	runs := make([]any, len(b.runs))
	for i, r := range b.runs {
		runs[i] = docxRunADT(r)
	}
	rows := make([]any, len(b.rows))
	for i, row := range b.rows {
		cells := make([]any, len(row))
		for j, cell := range row {
			blocks := make([]any, len(cell))
			for k, blk := range cell {
				blocks[k] = docxBlockADT(blk)
			}
			cells[j] = blocks
		}
		rows[i] = cells
	}
	return SkyADT{Tag: 0, SkyName: "RawBlock", Fields: []any{b.kind, b.level, runs, rows}}
}

// docxADTFields reads the fields of a Sky-built RawBlock / RawRun.
func docxADTFields(v any) []any {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok {
		return adt.Fields
	}
	return nil
}

func docxBlockOf(v any) docxBlock {
	f := docxADTFields(v)
	if len(f) != 4 {
		return docxBlock{}
	}
	b := docxBlock{kind: AsInt(f[0]), level: AsInt(f[1])}
	for _, rv := range asList(f[2]) {
		rf := docxADTFields(rv)
		if len(rf) != 4 {
			continue
		}
		b.runs = append(b.runs, docxRun{text: AsString(rf[0]), bold: AsBool(rf[1]), italic: AsBool(rf[2]), link: AsString(rf[3])})
	}
	for _, rowv := range asList(f[3]) {
		var row [][]docxBlock
		for _, cellv := range asList(rowv) {
			cell := []docxBlock{}
			for _, bv := range asList(cellv) {
				cell = append(cell, docxBlockOf(bv))
			}
			row = append(row, cell)
		}
		b.rows = append(b.rows, row)
	}
	return b
}

// ---------------------------------------------------------------- reading

// docxStyle is one paragraph style of styles.xml.
type docxStyle struct {
	name    string // lower case
	basedOn string
	outline int // -1 = none
	numID   string
	ilvl    int
}

func docxStyles(p *officePackage, part string) (map[string]docxStyle, any) {
	out := map[string]docxStyle{}
	if part == "" {
		return out, nil
	}
	root, err := p.tree(part)
	if err != nil || root == nil {
		return out, err
	}
	for _, s := range officeChildren(root, "style") {
		st := docxStyle{outline: -1}
		st.name = strings.ToLower(xmlAttrLocal(officeChild(s, "name"), "val"))
		st.basedOn = xmlAttrLocal(officeChild(s, "basedOn"), "val")
		if pPr := officeChild(s, "pPr"); pPr != nil {
			if o := officeChild(pPr, "outlineLvl"); o != nil {
				if n, err := strconv.Atoi(xmlAttrLocal(o, "val")); err == nil {
					st.outline = n
				}
			}
			if np := officeChild(pPr, "numPr"); np != nil {
				st.numID = xmlAttrLocal(officeChild(np, "numId"), "val")
				st.ilvl, _ = strconv.Atoi(xmlAttrLocal(officeChild(np, "ilvl"), "val"))
			}
		}
		out[xmlAttrLocal(s, "styleId")] = st
	}
	return out, nil
}

// docxNumbering maps numId -> ilvl -> bullet, from numbering.xml.
func docxNumbering(p *officePackage, part string) (map[string]map[int]bool, any) {
	out := map[string]map[int]bool{}
	if part == "" {
		return out, nil
	}
	root, err := p.tree(part)
	if err != nil || root == nil {
		return out, err
	}
	levels := func(n *xmlNode, into map[int]bool) {
		for _, l := range officeChildren(n, "lvl") {
			i, _ := strconv.Atoi(xmlAttrLocal(l, "ilvl"))
			into[i] = xmlAttrLocal(officeChild(l, "numFmt"), "val") == "bullet"
		}
	}
	abstract := map[string]map[int]bool{}
	for _, a := range officeChildren(root, "abstractNum") {
		m := map[int]bool{}
		levels(a, m)
		abstract[xmlAttrLocal(a, "abstractNumId")] = m
	}
	for _, n := range officeChildren(root, "num") {
		m := map[int]bool{}
		for k, v := range abstract[xmlAttrLocal(officeChild(n, "abstractNumId"), "val")] {
			m[k] = v
		}
		for _, ov := range officeChildren(n, "lvlOverride") {
			levels(ov, m)
		}
		out[xmlAttrLocal(n, "numId")] = m
	}
	return out, nil
}

// docxFrame is an open container while the body streams.
type docxFrame struct {
	kind   int // 0 blocks (body or cell), 1 table, 2 row
	blocks []docxBlock
	rows   [][][]docxBlock
	cells  [][]docxBlock
}

// docxPara is the paragraph being read.
type docxPara struct {
	style   string
	outline int // -1 = none
	numID   string
	hasNum  bool
	ilvl    int
	runs    []docxRun
}

type docxReader struct {
	rels      map[string]officeRel
	styles    map[string]docxStyle
	numbering map[string]map[int]bool
	maxBlocks int
	blocks    int
	frames    []*docxFrame
	names     []string // open element local names (w namespace only, else "")
	skip      int
	para      *docxPara
	inPPr     bool
	run       *docxRun
	inRPr     bool
	inT       bool
	text      strings.Builder
	links     []string
	fail      any
}

// docxSkipped reports the elements whose content is never read. A switch, not
// a package map: a map is built at init and linked into every program.
func docxSkipped(name string) bool {
	switch name {
	case "del", "moveFrom", "txbxContent", "drawing", "pict", "object",
		"sectPr", "instrText", "delText", "delInstrText", "footnotePr", "endnotePr":
		return true
	}
	return false
}

func (d *docxReader) start(name, space string, attrs []xmlAttr) *xmlFailure {
	if d.skip > 0 {
		d.skip++
		return nil
	}
	if name == "AlternateContent" || name == "Fallback" {
		d.skip = 1
		return nil
	}
	if space != docxNsMain && space != docxNsMainStrict {
		d.names = append(d.names, "")
		return nil
	}
	if docxSkipped(name) || strings.HasSuffix(name, "Change") {
		d.skip = 1
		return nil
	}
	d.names = append(d.names, name)
	switch name {
	case "tbl":
		d.para = nil
		d.frames = append(d.frames, &docxFrame{kind: 1})
	case "tr":
		d.frames = append(d.frames, &docxFrame{kind: 2})
	case "tc":
		d.frames = append(d.frames, &docxFrame{kind: 0})
	case "p":
		d.para = &docxPara{outline: -1}
	case "pPr":
		d.inPPr = true
	case "pStyle":
		if d.para != nil && d.inPPr {
			d.para.style = officeAttr(attrs, "val")
		}
	case "outlineLvl":
		if d.para != nil && d.inPPr {
			if n, err := strconv.Atoi(officeAttr(attrs, "val")); err == nil {
				d.para.outline = n
			}
		}
	case "numId":
		if d.para != nil && d.inPPr {
			d.para.numID = officeAttr(attrs, "val")
			d.para.hasNum = true
		}
	case "ilvl":
		if d.para != nil && d.inPPr {
			d.para.ilvl, _ = strconv.Atoi(officeAttr(attrs, "val"))
		}
	case "rPr":
		if d.inPPr {
			d.skip = 1 // the paragraph mark's properties
			d.names = d.names[:len(d.names)-1]
			return nil
		}
		d.inRPr = d.run != nil
	case "hyperlink":
		link := ""
		if id := officeRelID(attrs); id != "" {
			if r, ok := d.rels[id]; ok && r.external {
				link = r.target
			}
		} else if a := officeAttr(attrs, "anchor"); a != "" {
			link = "#" + a
		}
		d.links = append(d.links, link)
	case "r":
		if d.para != nil && !d.inPPr {
			link := ""
			if len(d.links) > 0 {
				link = d.links[len(d.links)-1]
			}
			d.run = &docxRun{link: link}
			d.text.Reset()
		}
	case "b":
		if d.inRPr && d.run != nil {
			d.run.bold = officeOn(attrs)
		}
	case "i":
		if d.inRPr && d.run != nil {
			d.run.italic = officeOn(attrs)
		}
	case "t":
		d.inT = d.run != nil && !d.inRPr
	case "tab":
		if d.run != nil && !d.inRPr && !d.inPPr {
			d.text.WriteByte('\t')
		}
	case "br", "cr":
		if d.run != nil && !d.inRPr && !d.inPPr {
			d.text.WriteByte('\n')
		}
	case "noBreakHyphen":
		if d.run != nil && !d.inRPr {
			d.text.WriteByte('-')
		}
	}
	return nil
}

func (d *docxReader) textEv(t []byte) *xmlFailure {
	if d.skip == 0 && d.inT {
		d.text.Write(t)
	}
	return nil
}

func (d *docxReader) end() *xmlFailure {
	if d.skip > 0 {
		d.skip--
		return nil
	}
	name := d.names[len(d.names)-1]
	d.names = d.names[:len(d.names)-1]
	switch name {
	case "pPr":
		d.inPPr = false
	case "rPr":
		d.inRPr = false
	case "t":
		d.inT = false
	case "hyperlink":
		if len(d.links) > 0 {
			d.links = d.links[:len(d.links)-1]
		}
	case "r":
		if d.run != nil && d.para != nil {
			d.run.text = d.text.String()
			d.para.runs = docxAppendRun(d.para.runs, *d.run)
		}
		d.run = nil
	case "p":
		if d.para != nil {
			b := d.paragraphBlock(d.para)
			d.para = nil
			return d.add(b)
		}
	case "tc":
		f := d.pop()
		if t := d.top(); t != nil && t.kind == 2 {
			t.cells = append(t.cells, docxTrimTrailingEmpty(f.blocks))
		}
	case "tr":
		f := d.pop()
		if t := d.top(); t != nil && t.kind == 1 {
			t.rows = append(t.rows, f.cells)
		}
	case "tbl":
		f := d.pop()
		return d.add(docxBlock{kind: docxKindTable, rows: f.rows})
	}
	return nil
}

func (d *docxReader) top() *docxFrame {
	if len(d.frames) == 0 {
		return nil
	}
	return d.frames[len(d.frames)-1]
}

func (d *docxReader) pop() *docxFrame {
	f := d.frames[len(d.frames)-1]
	if len(d.frames) > 1 {
		d.frames = d.frames[:len(d.frames)-1]
	}
	return f
}

// add puts a finished block into the innermost container.
func (d *docxReader) add(b docxBlock) *xmlFailure {
	d.blocks++
	if d.blocks > d.maxBlocks {
		d.fail = officeError(officeCodeTooManyBlocks,
			"Docx: the document has more than "+strconv.Itoa(d.maxBlocks)+" blocks",
			[2]string{"limit", strconv.Itoa(d.maxBlocks)})
		return xmlFail("stop", "stop")
	}
	if t := d.top(); t != nil && t.kind == 0 {
		t.blocks = append(t.blocks, b)
	}
	return nil
}

// docxAppendRun joins r to the last run when they look the same, and drops
// an empty run.
func docxAppendRun(runs []docxRun, r docxRun) []docxRun {
	if r.text == "" {
		return runs
	}
	if k := len(runs); k > 0 {
		last := &runs[k-1]
		if last.bold == r.bold && last.italic == r.italic && last.link == r.link {
			last.text += r.text
			return runs
		}
	}
	return append(runs, r)
}

// docxTrimTrailingEmpty drops one trailing empty paragraph.
func docxTrimTrailingEmpty(blocks []docxBlock) []docxBlock {
	if k := len(blocks); k > 0 && blocks[k-1].kind == docxKindParagraph && len(blocks[k-1].runs) == 0 {
		return blocks[:k-1]
	}
	if blocks == nil {
		return []docxBlock{}
	}
	return blocks
}

// headingLevel is the heading level of a style chain (-1 when none).
func (d *docxReader) headingLevel(styleID string) int {
	for i := 0; i < 16 && styleID != ""; i++ {
		st, ok := d.styles[styleID]
		name := st.name
		if !ok {
			name = strings.ToLower(styleID)
		}
		if name == "title" {
			return 0
		}
		for _, prefix := range []string{"heading ", "heading"} {
			if strings.HasPrefix(name, prefix) {
				if n, err := strconv.Atoi(strings.TrimPrefix(name, prefix)); err == nil && n >= 1 && n <= 9 {
					return n
				}
			}
		}
		if ok && st.outline >= 0 && st.outline <= 8 {
			return st.outline + 1
		}
		if !ok {
			return -1
		}
		styleID = st.basedOn
	}
	return -1
}

// styleNumbering is the numbering a style chain gives.
func (d *docxReader) styleNumbering(styleID string) (string, int, bool) {
	for i := 0; i < 16 && styleID != ""; i++ {
		st, ok := d.styles[styleID]
		if !ok {
			return "", 0, false
		}
		if st.numID != "" {
			return st.numID, st.ilvl, true
		}
		styleID = st.basedOn
	}
	return "", 0, false
}

func (d *docxReader) paragraphBlock(p *docxPara) docxBlock {
	runs := p.runs
	if runs == nil {
		runs = []docxRun{}
	}
	level := d.headingLevel(p.style)
	if p.outline >= 0 && p.outline <= 8 {
		level = p.outline + 1
	} else if p.outline == 9 {
		level = -1
	}
	if level >= 0 {
		return docxBlock{kind: docxKindHeading, level: level, runs: runs}
	}
	numID, ilvl, has := p.numID, p.ilvl, p.hasNum
	if !has {
		numID, ilvl, has = d.styleNumbering(p.style)
	}
	if has && numID != "" && numID != "0" {
		kind := docxKindBulleted
		if lv, ok := d.numbering[numID]; ok {
			if bullet, ok := lv[ilvl]; ok && !bullet {
				kind = docxKindNumbered
			}
		}
		return docxBlock{kind: kind, level: max(0, min(ilvl, 8)), runs: runs}
	}
	return docxBlock{kind: docxKindParagraph, runs: runs}
}

// docxReadAll is Std.Office.Docx.readWith.
func docxReadAll(data string, zl zipLimits, maxBlocks, maxDepth int) ([]docxBlock, any) {
	p, err := officeOpen(data, zl, maxDepth)
	if err != nil {
		return nil, err
	}
	part, err := p.mainPart("word/document.xml")
	if err != nil {
		return nil, err
	}
	if !p.zip.has(part) {
		return nil, officeError(officeCodeNotOffice, "Docx: the file is not a Word document (no document part "+strconv.Quote(part)+")",
			[2]string{"part", part})
	}
	rels, list, err := p.rels(part)
	if err != nil {
		return nil, err
	}
	stylesPart, numberingPart := "", ""
	for _, r := range list {
		switch {
		case strings.HasSuffix(r.typ, "/styles") && !r.external:
			stylesPart = r.target
		case strings.HasSuffix(r.typ, "/numbering") && !r.external:
			numberingPart = r.target
		}
	}
	styles, err := docxStyles(p, stylesPart)
	if err != nil {
		return nil, err
	}
	numbering, err := docxNumbering(p, numberingPart)
	if err != nil {
		return nil, err
	}
	d := &docxReader{rels: rels, styles: styles, numbering: numbering, maxBlocks: maxBlocks, frames: []*docxFrame{{kind: 0}}}
	_, err = p.stream(part, xmlEvents{start: d.start, end: d.end, text: d.textEv})
	if d.fail != nil {
		return nil, d.fail
	}
	if err != nil {
		return nil, err
	}
	return docxTrimTrailingEmpty(d.frames[0].blocks), nil
}

// ---------------------------------------------------------------- writing

// docxSafeLink reports whether a link may be written: http, https, mailto,
// or an internal "#anchor".
func docxSafeLink(link string) bool {
	if strings.HasPrefix(link, "#") {
		return len(link) > 1
	}
	scheme, rest, ok := strings.Cut(link, ":")
	if !ok || rest == "" {
		return false
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
		return strings.HasPrefix(rest, "//") && len(rest) > 2
	case "mailto":
		return true
	}
	return false
}

type docxWriter struct {
	o      officeXml
	links  []officeRel
	linkIx map[string]string
	nums   []int // the numIds of numbered runs, after the bullet num 1
	inList bool  // the previous block was a numbered item
	curNum int
	fail   any
}

func (w *docxWriter) failWith(code, msg string, attrs ...[2]string) {
	if w.fail == nil {
		w.fail = officeError(code, msg, attrs...)
	}
}

// container writes blocks plus the trailing empty paragraph Word needs (after
// a table, for an empty container, and to keep a final empty paragraph).
func (w *docxWriter) container(blocks []docxBlock) {
	w.inList = false
	for _, b := range blocks {
		w.block(b)
	}
	if k := len(blocks); k == 0 || blocks[k-1].kind == docxKindTable || (blocks[k-1].kind == docxKindParagraph && len(blocks[k-1].runs) == 0) {
		w.o.raw(`<w:p/>`)
	}
	w.inList = false
}

func (w *docxWriter) block(b docxBlock) {
	if w.fail != nil {
		return
	}
	if b.kind != docxKindNumbered {
		w.inList = false
	}
	switch b.kind {
	case docxKindTable:
		w.table(b.rows)
		return
	case docxKindHeading:
		if b.level < 0 || b.level > 9 {
			w.failWith(officeCodeHeadingLevel, "Docx.write: heading level "+strconv.Itoa(b.level)+" is not 0 (Title) to 9",
				[2]string{"level", strconv.Itoa(b.level)})
			return
		}
		style := "Title"
		if b.level > 0 {
			style = "Heading" + strconv.Itoa(b.level)
		}
		w.o.raw(`<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`)
	case docxKindBulleted, docxKindNumbered:
		if b.level < 0 || b.level > 8 {
			w.failWith(officeCodeListLevel, "Docx.write: list level "+strconv.Itoa(b.level)+" is not 0 to 8",
				[2]string{"level", strconv.Itoa(b.level)})
			return
		}
		numID := 1
		if b.kind == docxKindNumbered {
			if !w.inList {
				w.curNum = 2 + len(w.nums)
				w.nums = append(w.nums, w.curNum)
				w.inList = true
			}
			numID = w.curNum
		}
		w.o.raw(`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="` + strconv.Itoa(b.level) +
			`"/><w:numId w:val="` + strconv.Itoa(numID) + `"/></w:numPr></w:pPr>`)
	default:
		if len(b.runs) == 0 {
			w.o.raw(`<w:p/>`)
			return
		}
		w.o.raw(`<w:p>`)
	}
	for _, r := range b.runs {
		w.run(r)
	}
	w.o.raw(`</w:p>`)
}

func (w *docxWriter) run(r docxRun) {
	if r.text == "" {
		return
	}
	if r.link != "" {
		if !docxSafeLink(r.link) {
			w.failWith(officeCodeUnsafeLink, "Docx.write: the link "+strconv.Quote(r.link)+" is not http, https, mailto or an internal #anchor",
				[2]string{"link", r.link})
			return
		}
		if strings.HasPrefix(r.link, "#") {
			w.o.raw(`<w:hyperlink`)
			w.o.attr("w:anchor", r.link[1:])
			w.o.raw(`>`)
		} else {
			id, ok := w.linkIx[r.link]
			if !ok {
				id = "rId" + strconv.Itoa(3+len(w.links))
				w.links = append(w.links, officeRel{id: id, typ: docxRelHyperlink, target: r.link, external: true})
				w.linkIx[r.link] = id
			}
			w.o.raw(`<w:hyperlink r:id="` + id + `" w:history="1">`)
		}
	}
	w.o.raw(`<w:r>`)
	if r.link != "" || r.bold || r.italic {
		w.o.raw(`<w:rPr>`)
		if r.link != "" {
			w.o.raw(`<w:rStyle w:val="Hyperlink"/>`)
		}
		if r.bold {
			w.o.raw(`<w:b/>`)
		}
		if r.italic {
			w.o.raw(`<w:i/>`)
		}
		w.o.raw(`</w:rPr>`)
	}
	// Text between tabs and line breaks goes in <w:t>.
	start := 0
	flush := func(end int) {
		if end > start {
			w.o.raw(`<w:t xml:space="preserve">`)
			w.o.text(r.text[start:end])
			w.o.raw(`</w:t>`)
		}
	}
	for i := 0; i < len(r.text); i++ {
		switch r.text[i] {
		case '\t':
			flush(i)
			w.o.raw(`<w:tab/>`)
			start = i + 1
		case '\n':
			flush(i)
			w.o.raw(`<w:br/>`)
			start = i + 1
		}
	}
	flush(len(r.text))
	w.o.raw(`</w:r>`)
	if r.link != "" {
		w.o.raw(`</w:hyperlink>`)
	}
}

func (w *docxWriter) table(rows [][][]docxBlock) {
	cols := 1
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	width := 9026 / cols // the text width of an A4 page with 1-inch margins, in twentieths of a point
	w.o.raw(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="0" w:type="auto"/><w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="1" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for i := 0; i < cols; i++ {
		w.o.raw(`<w:gridCol w:w="` + strconv.Itoa(width) + `"/>`)
	}
	w.o.raw(`</w:tblGrid>`)
	for _, row := range rows {
		w.o.raw(`<w:tr>`)
		for _, cell := range row {
			w.o.raw(`<w:tc><w:tcPr><w:tcW w:w="` + strconv.Itoa(width) + `" w:type="dxa"/></w:tcPr>`)
			w.container(cell)
			w.o.raw(`</w:tc>`)
		}
		if len(row) == 0 {
			// A row needs a cell.
			w.o.raw(`<w:tc><w:tcPr><w:tcW w:w="` + strconv.Itoa(width) + `" w:type="dxa"/></w:tcPr><w:p/></w:tc>`)
		}
		w.o.raw(`</w:tr>`)
	}
	w.inList = false
	w.o.raw(`</w:tbl>`)
}

func docxStylesXml() string {
	var o officeXml
	o.raw(officeXmlDecl)
	o.raw(`<w:styles xmlns:w="` + docxNsMain + `">`)
	o.raw(`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Calibri" w:cs="Calibri"/><w:sz w:val="22"/><w:szCs w:val="22"/><w:lang w:val="en-GB"/></w:rPr></w:rPrDefault>`)
	o.raw(`<w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="259" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>`)
	o.raw(`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>`)
	o.raw(`<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="80"/></w:pPr><w:rPr><w:sz w:val="56"/><w:szCs w:val="56"/></w:rPr></w:style>`)
	sizes := []int{0, 32, 28, 26, 24, 22, 22, 22, 22, 22}
	for lvl := 1; lvl <= 9; lvl++ {
		n := strconv.Itoa(lvl)
		o.raw(`<w:style w:type="paragraph" w:styleId="Heading` + n + `"><w:name w:val="heading ` + n + `"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:uiPriority w:val="9"/><w:qFormat/>`)
		o.raw(`<w:pPr><w:keepNext/><w:spacing w:before="240" w:after="80"/><w:outlineLvl w:val="` + strconv.Itoa(lvl-1) + `"/></w:pPr>`)
		o.raw(`<w:rPr><w:b/><w:sz w:val="` + strconv.Itoa(sizes[lvl]) + `"/><w:szCs w:val="` + strconv.Itoa(sizes[lvl]) + `"/></w:rPr></w:style>`)
	}
	o.raw(`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:ind w:left="720"/><w:contextualSpacing/></w:pPr></w:style>`)
	o.raw(`<w:style w:type="character" w:default="1" w:styleId="DefaultParagraphFont"><w:name w:val="Default Paragraph Font"/><w:uiPriority w:val="1"/><w:semiHidden/></w:style>`)
	o.raw(`<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:basedOn w:val="DefaultParagraphFont"/><w:rPr><w:color w:val="0563C1"/><w:u w:val="single"/></w:rPr></w:style>`)
	o.raw(`<w:style w:type="table" w:default="1" w:styleId="TableNormal"><w:name w:val="Normal Table"/><w:semiHidden/><w:tblPr><w:tblInd w:w="0" w:type="dxa"/><w:tblCellMar><w:top w:w="0" w:type="dxa"/><w:left w:w="108" w:type="dxa"/><w:bottom w:w="0" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>`)
	o.raw(`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:basedOn w:val="TableNormal"/><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:tblPr><w:tblBorders>`)
	for _, side := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		o.raw(`<w:` + side + ` w:val="single" w:sz="4" w:space="0" w:color="auto"/>`)
	}
	o.raw(`</w:tblBorders></w:tblPr></w:style></w:styles>`)
	return o.String()
}

func docxNumberingXml(numbered []int) string {
	var o officeXml
	o.raw(officeXmlDecl)
	o.raw(`<w:numbering xmlns:w="` + docxNsMain + `">`)
	bullets := []string{"•", "◦", "▪"}
	formats := []string{"decimal", "lowerLetter", "lowerRoman"}
	for id := 0; id < 2; id++ {
		o.raw(`<w:abstractNum w:abstractNumId="` + strconv.Itoa(id) + `"><w:multiLevelType w:val="hybridMultilevel"/>`)
		for lvl := 0; lvl < 9; lvl++ {
			o.raw(`<w:lvl w:ilvl="` + strconv.Itoa(lvl) + `"><w:start w:val="1"/>`)
			if id == 0 {
				o.raw(`<w:numFmt w:val="bullet"/><w:lvlText`)
				o.attr("w:val", bullets[lvl%3])
			} else {
				o.raw(`<w:numFmt w:val="` + formats[lvl%3] + `"/><w:lvlText`)
				o.attr("w:val", "%"+strconv.Itoa(lvl+1)+".")
			}
			o.raw(`/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="` + strconv.Itoa(720*(lvl+1)) + `" w:hanging="360"/></w:pPr></w:lvl>`)
		}
		o.raw(`</w:abstractNum>`)
	}
	o.raw(`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	for _, n := range numbered {
		o.raw(`<w:num w:numId="` + strconv.Itoa(n) + `"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`)
	}
	o.raw(`</w:numbering>`)
	return o.String()
}

// docxWriteAll is Std.Office.Docx.write.
func docxWriteAll(blocks []docxBlock) (string, any) {
	w := &docxWriter{linkIx: map[string]string{}}
	w.o.raw(officeXmlDecl)
	w.o.raw(`<w:document xmlns:w="` + docxNsMain + `" xmlns:r="` + officeNsDocRelationships + `"><w:body>`)
	w.container(blocks)
	if w.fail != nil {
		return "", w.fail
	}
	w.o.raw(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`)
	rels := append([]officeRel{
		{id: "rId1", typ: docxRelStyles, target: "styles.xml"},
		{id: "rId2", typ: docxRelNumbering, target: "numbering.xml"},
	}, w.links...)
	entries := []zipEntry{
		{path: "[Content_Types].xml", bytes: []byte(officeContentTypes([][2]string{
			{"word/document.xml", docxCtDocument}, {"word/styles.xml", docxCtStyles}, {"word/numbering.xml", docxCtNumbering},
		}))},
		{path: "_rels/.rels", bytes: []byte(officeRelsXml([]officeRel{{id: "rId1", typ: docxRelOfficeDoc, target: "word/document.xml"}}))},
		{path: "word/document.xml", bytes: []byte(w.o.String())},
		{path: "word/_rels/document.xml.rels", bytes: []byte(officeRelsXml(rels))},
		{path: "word/styles.xml", bytes: []byte(docxStylesXml())},
		{path: "word/numbering.xml", bytes: []byte(docxNumberingXml(w.nums))},
	}
	out, fail := zipWriteAll(entries, true)
	if fail != nil {
		return "", fail.skyError()
	}
	return string(out), nil
}

// Office_docxRead is the primitive behind `Docx.read` / `Docx.readWith`: the
// Limits record as six Ints (the zip limits first).
func Office_docxRead(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio, maxBlocks, maxDepth, data any) any {
	zl, bad := zipLimitsOf(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio)
	if bad != nil {
		return bad
	}
	mb, md := AsInt(maxBlocks), AsInt(maxDepth)
	if mb <= 0 || md <= 0 {
		return Err[any, any](ErrInvalidInput("Docx: every limit must be positive, got maxBlocks " + strconv.Itoa(mb) + ", maxDepth " + strconv.Itoa(md)))
	}
	blocks, err := docxReadAll(asBytesString(data), zl, mb, md)
	if err != nil {
		return Err[any, any](err)
	}
	out := make([]any, len(blocks))
	for i, b := range blocks {
		out[i] = docxBlockADT(b)
	}
	return Ok[any, any](out)
}

// Office_docxWrite is the primitive behind `Docx.write`.
func Office_docxWrite(blocks any) any {
	var in []docxBlock
	for _, b := range asList(blocks) {
		in = append(in, docxBlockOf(b))
	}
	out, err := docxWriteAll(in)
	if err != nil {
		return Err[any, any](err)
	}
	return Ok[any, any](out)
}
