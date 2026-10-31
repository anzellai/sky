package rt

// office_xlsx_write.go — Std.Office.Xlsx.write.
//
// Sky passes the sheets as plain records (Xlsx.sky `WSheet` / `WCell`), so
// the kernel reads no Sky ADT. The writer:
//
//   - deduplicates styles into cellXfs (fonts, fills and number formats each
//     deduplicated too), so a million bold cells cost one style;
//   - writes text through the shared strings table, deduplicated, with
//     characters XML cannot carry as Excel's `_xHHHH_` escapes;
//   - writes a date as its serial number (1900 system, Excel's phantom
//     1900-02-29 kept) with a date format, `yyyy-mm-dd` unless one is given;
//   - writes a formula as `<f>` with no cached value and sets
//     `calcPr fullCalcOnLoad="1"`, which asks a reader to compute it when the
//     file opens (LibreOffice does: scripts/office-second-reader-e2e.sh);
//   - is deterministic: the same sheets give the same bytes (the zip writer
//     stamps a fixed time).
//
// It refuses (domain "office") what Excel would refuse or "repair": a bad
// sheet name, no visible sheet, too many rows or columns, a bad range or an
// overlapping merge, an inline validation list over 255 characters, text over
// 32,767 characters, a date before 1900.

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The WCell kinds (Xlsx.sky `cellKind`).
const (
	xlsxKindEmpty   = 0
	xlsxKindText    = 1
	xlsxKindNumber  = 2
	xlsxKindBool    = 3
	xlsxKindDate    = 4
	xlsxKindFormula = 5
	xlsxKindFailure = 6
)

const (
	xlsxNsMain        = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	xlsxMaxText       = 32_767
	xlsxMaxSheetName  = 31
	xlsxMaxListLength = 255
	xlsxDefaultDate   = "yyyy-mm-dd"
	xlsxCtWorkbook    = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	xlsxCtWorksheet   = "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"
	xlsxCtStyles      = "application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"
	xlsxCtShared      = "application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"
	xlsxRelWorksheet  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"
	xlsxRelStyles     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles"
	xlsxRelShared     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings"
	xlsxRelOfficeDoc  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
)

// xlsxErrorValue reports whether s is an error value a cell can hold. A
// switch, not a package map: a map is built at init and linked into every
// program.
func xlsxErrorValue(s string) bool {
	switch s {
	case "#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A",
		"#GETTING_DATA", "#SPILL!", "#CALC!", "#FIELD!", "#BLOCKED!", "#CONNECT!",
		"#UNKNOWN!", "#BUSY!":
		return true
	}
	return false
}

// xlsxCell is one WCell read from Sky.
type xlsxCell struct {
	kind   int
	text   string
	number float64
	flag   bool
	parts  []int
	style  xlsxStyle
}

// xlsxStyle is what a cell's cellXfs entry holds.
type xlsxStyle struct {
	bold, italic, wrap bool
	size               int // 0 = the default 11
	fill               int // -1 = none, else 0xRRGGBB
	format             string
}

type xlsxFont struct {
	bold, italic bool
	size         int
}

// xlsxStyles deduplicates styles into the parts of styles.xml.
type xlsxStyles struct {
	fonts   []xlsxFont
	fontIx  map[xlsxFont]int
	fills   []int
	fillIx  map[int]int
	formats []string
	fmtIx   map[string]int
	xfs     [][4]int // numFmtId, fontId, fillId, wrap
	xfIx    map[[4]int]int
}

func newXlsxStyles() *xlsxStyles {
	s := &xlsxStyles{
		fonts:  []xlsxFont{{size: 11}},
		fontIx: map[xlsxFont]int{{size: 11}: 0},
		fillIx: map[int]int{},
		fmtIx:  map[string]int{},
		xfs:    [][4]int{{0, 0, 0, 0}},
		xfIx:   map[[4]int]int{{0, 0, 0, 0}: 0},
	}
	return s
}

// index is the cellXfs index of st.
func (s *xlsxStyles) index(st xlsxStyle) int {
	size := st.size
	if size <= 0 {
		size = 11
	}
	f := xlsxFont{bold: st.bold, italic: st.italic, size: size}
	fontID, ok := s.fontIx[f]
	if !ok {
		fontID = len(s.fonts)
		s.fonts = append(s.fonts, f)
		s.fontIx[f] = fontID
	}
	fillID := 0
	if st.fill >= 0 {
		if id, ok := s.fillIx[st.fill]; ok {
			fillID = id
		} else {
			fillID = 2 + len(s.fills) // 0 and 1 are the two fills Excel requires
			s.fills = append(s.fills, st.fill)
			s.fillIx[st.fill] = fillID
		}
	}
	fmtID := 0
	if st.format != "" && st.format != "General" {
		if id, ok := s.fmtIx[st.format]; ok {
			fmtID = id
		} else {
			fmtID = 164 + len(s.formats)
			s.formats = append(s.formats, st.format)
			s.fmtIx[st.format] = fmtID
		}
	}
	wrap := 0
	if st.wrap {
		wrap = 1
	}
	key := [4]int{fmtID, fontID, fillID, wrap}
	if id, ok := s.xfIx[key]; ok {
		return id
	}
	id := len(s.xfs)
	s.xfs = append(s.xfs, key)
	s.xfIx[key] = id
	return id
}

func (s *xlsxStyles) xml() string {
	var o officeXml
	o.raw(officeXmlDecl)
	o.raw(`<styleSheet xmlns="` + xlsxNsMain + `">`)
	if len(s.formats) > 0 {
		o.raw(`<numFmts`)
		o.int("count", len(s.formats))
		o.raw(`>`)
		for i, f := range s.formats {
			o.raw(`<numFmt`)
			o.int("numFmtId", 164+i)
			o.attr("formatCode", f)
			o.raw(`/>`)
		}
		o.raw(`</numFmts>`)
	}
	o.raw(`<fonts`)
	o.int("count", len(s.fonts))
	o.raw(`>`)
	for _, f := range s.fonts {
		o.raw(`<font>`)
		if f.bold {
			o.raw(`<b/>`)
		}
		if f.italic {
			o.raw(`<i/>`)
		}
		o.raw(`<sz`)
		o.int("val", f.size)
		o.raw(`/><name val="Calibri"/><family val="2"/></font>`)
	}
	o.raw(`</fonts><fills`)
	o.int("count", 2+len(s.fills))
	o.raw(`><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>`)
	for _, rgb := range s.fills {
		hex := strings.ToUpper(strconv.FormatInt(int64(rgb), 16))
		hex = strings.Repeat("0", 6-len(hex)) + hex
		o.raw(`<fill><patternFill patternType="solid"><fgColor rgb="FF` + hex + `"/><bgColor indexed="64"/></patternFill></fill>`)
	}
	o.raw(`</fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>`)
	o.raw(`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs`)
	o.int("count", len(s.xfs))
	o.raw(`>`)
	for _, x := range s.xfs {
		o.raw(`<xf`)
		o.int("numFmtId", x[0])
		o.int("fontId", x[1])
		o.int("fillId", x[2])
		o.raw(` borderId="0" xfId="0"`)
		if x[0] != 0 {
			o.raw(` applyNumberFormat="1"`)
		}
		if x[1] != 0 {
			o.raw(` applyFont="1"`)
		}
		if x[2] != 0 {
			o.raw(` applyFill="1"`)
		}
		if x[3] != 0 {
			o.raw(` applyAlignment="1"><alignment wrapText="1"/></xf>`)
		} else {
			o.raw(`/>`)
		}
	}
	o.raw(`</cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`)
	return o.String()
}

// xlsxEscapeText writes Excel's `_xHHHH_` escape for each character XML
// cannot carry, and escapes the underscore of a literal `_xHHHH_` so it reads
// back as written.
func xlsxEscapeText(s string) string {
	need := strings.Contains(s, "_x")
	if !need {
		for i := 0; i < len(s); {
			r, size := utf8.DecodeRuneInString(s[i:])
			if (r == utf8.RuneError && size == 1) || !xmlCharOK(r) {
				need = true
				break
			}
			i += size
		}
	}
	if !need {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+7 <= len(s) && s[i] == '_' && s[i+1] == 'x' && s[i+6] == '_' {
			if _, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
				b.WriteString("_x005F_")
				i++
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || !xmlCharOK(r) {
			if r == utf8.RuneError && size == 1 {
				r = 0xFFFD
				b.WriteRune(r)
			} else if r <= 0xFFFF {
				h := strings.ToUpper(strconv.FormatInt(int64(r), 16))
				b.WriteString("_x" + strings.Repeat("0", 4-len(h)) + h + "_")
			} else {
				b.WriteRune(0xFFFD)
			}
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// xlsxDateSerial converts date parts to a 1900-system serial number. ok is
// false for an invalid date or one before 1900-01-01 (other than the serial
// 0 day 1899-12-30 a time-only value uses).
func xlsxDateSerial(p []int) (float64, bool) {
	if len(p) != 7 {
		return 0, false
	}
	y, mo, d, h, mi, s, ms := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
	if mo < 1 || mo > 12 || d < 1 || d > 31 || h < 0 || h > 23 || mi < 0 || mi > 59 || s < 0 || s > 59 || ms < 0 || ms > 999 || y > 9999 {
		return 0, false
	}
	frac := float64(((h*60+mi)*60+s)*1000+ms) / 86_400_000
	if y == 1900 && mo == 2 && d == 29 {
		return 60 + frac, true
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return 0, false
	}
	// Unix seconds, not a Duration: a Duration overflows after 292 years.
	days := int((t.Unix() - time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).Unix()) / 86_400)
	switch {
	case days == 0:
		return frac, true
	case days < 2: // 1899-12-31 and earlier have no serial
		return 0, false
	case days < 61: // 1900-01-01 … 1900-02-28 count from 1899-12-31
		return float64(days-1) + frac, true
	}
	return float64(days) + frac, true
}

// xlsxSheetIn is one WSheet read from Sky.
type xlsxSheetIn struct {
	name        string
	visibility  int
	rows        [][]xlsxCell
	widths      [][2]float64
	frozenRows  int
	frozenCols  int
	merged      [][4]int
	validations []xlsxValidationIn
}

type xlsxValidationIn struct {
	kind   int // 0 ListOf, 1 ListFromRange
	rg     [4]int
	items  []string
	source string
}

func xlsxRangeIn(v any) [4]int {
	return [4]int{
		AsInt(recordField(v, "FirstRow", "firstRow")), AsInt(recordField(v, "FirstColumn", "firstColumn")),
		AsInt(recordField(v, "LastRow", "lastRow")), AsInt(recordField(v, "LastColumn", "lastColumn")),
	}
}

func xlsxSheetsIn(sheets any) []xlsxSheetIn {
	var out []xlsxSheetIn
	for _, sv := range asList(sheets) {
		s := xlsxSheetIn{
			name:       AsString(recordField(sv, "Name", "name")),
			visibility: AsInt(recordField(sv, "Visibility", "visibility")),
			frozenRows: AsInt(recordField(sv, "FrozenRows", "frozenRows")),
			frozenCols: AsInt(recordField(sv, "FrozenColumns", "frozenColumns")),
		}
		for _, rv := range asList(recordField(sv, "Rows", "rows")) {
			var row []xlsxCell
			for _, cv := range asList(rv) {
				c := xlsxCell{
					kind:   AsInt(recordField(cv, "Kind", "kind")),
					text:   AsString(recordField(cv, "Text", "text")),
					number: AsFloat(recordField(cv, "Number", "number")),
					flag:   AsBool(recordField(cv, "Flag", "flag")),
					style: xlsxStyle{
						bold:   AsBool(recordField(cv, "Bold", "bold")),
						italic: AsBool(recordField(cv, "Italic", "italic")),
						wrap:   AsBool(recordField(cv, "Wrap", "wrap")),
						size:   AsInt(recordField(cv, "Size", "size")),
						fill:   AsInt(recordField(cv, "Fill", "fill")),
						format: AsString(recordField(cv, "Format", "format")),
					},
				}
				for _, p := range asList(recordField(cv, "Parts", "parts")) {
					c.parts = append(c.parts, AsInt(p))
				}
				row = append(row, c)
			}
			s.rows = append(s.rows, row)
		}
		for _, w := range asList(recordField(sv, "ColumnWidths", "columnWidths")) {
			s.widths = append(s.widths, [2]float64{float64(AsInt(recordField(w, "Column", "column"))), AsFloat(recordField(w, "Width", "width"))})
		}
		for _, m := range asList(recordField(sv, "Merged", "merged")) {
			s.merged = append(s.merged, xlsxRangeIn(m))
		}
		for _, vv := range asList(recordField(sv, "Validations", "validations")) {
			v := xlsxValidationIn{
				kind:   AsInt(recordField(vv, "Kind", "kind")),
				rg:     xlsxRangeIn(recordField(vv, "Range", "range")),
				source: AsString(recordField(vv, "Source", "source")),
			}
			for _, it := range asList(recordField(vv, "Items", "items")) {
				v.items = append(v.items, AsString(it))
			}
			s.validations = append(s.validations, v)
		}
		out = append(out, s)
	}
	return out
}

// xlsxCheckName applies Excel's sheet name rules.
func xlsxCheckName(name string, seen map[string]bool) any {
	bad := func(why string) any {
		return officeError(officeCodeSheetName, "Xlsx.write: the sheet name "+strconv.Quote(name)+" "+why,
			[2]string{"name", name})
	}
	n := officeRuneCount(name)
	switch {
	case n == 0:
		return bad("is empty")
	case n > xlsxMaxSheetName:
		return bad("is longer than 31 characters")
	case strings.ContainsAny(name, `[]:*?/\`):
		return bad(`contains one of [ ] : * ? / \`)
	case strings.HasPrefix(name, "'") || strings.HasSuffix(name, "'"):
		return bad("starts or ends with an apostrophe")
	case strings.EqualFold(name, "History"):
		return bad("is reserved by Excel")
	}
	for _, r := range name {
		if !xmlCharOK(r) || r < 0x20 {
			return bad("contains a control character")
		}
	}
	key := strings.ToLower(name)
	if seen[key] {
		return bad("repeats another sheet name (names are compared without case)")
	}
	seen[key] = true
	return nil
}

// xlsxCheckRange refuses a range that is reversed, negative or off the sheet.
func xlsxCheckRange(sheet string, what string, r [4]int) any {
	if r[0] < 0 || r[1] < 0 || r[2] < r[0] || r[3] < r[1] || r[2] >= xlsxMaxRows || r[3] >= xlsxMaxColumns {
		return officeError(officeCodeRange,
			"Xlsx.write: the "+what+" range (rows "+strconv.Itoa(r[0])+" to "+strconv.Itoa(r[2])+", columns "+strconv.Itoa(r[1])+" to "+strconv.Itoa(r[3])+
				") on sheet "+strconv.Quote(sheet)+" is not valid (0-based and inclusive, first <= last, within 1,048,576 rows and 16,384 columns)",
			[2]string{"sheet", sheet})
	}
	return nil
}

func xlsxRangeRef(r [4]int) string {
	a := xlsxCellName(r[0], r[1])
	if r[0] == r[2] && r[1] == r[3] {
		return a
	}
	return a + ":" + xlsxCellName(r[2], r[3])
}

// xlsxFormatNumber writes a float so it reads back as the same float.
func xlsxFormatNumber(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// xlsxWriteAll is Std.Office.Xlsx.write.
func xlsxWriteAll(sheets []xlsxSheetIn) (string, any) {
	firstVisible := -1
	seen := map[string]bool{}
	for i, s := range sheets {
		if err := xlsxCheckName(s.name, seen); err != nil {
			return "", err
		}
		if s.visibility == 0 && firstVisible < 0 {
			firstVisible = i
		}
	}
	if firstVisible < 0 {
		return "", officeError(officeCodeNoVisibleSheet, "Xlsx.write: a workbook needs at least one visible sheet")
	}
	styles := newXlsxStyles()
	var strs []string
	strIx := map[string]int{}
	entries := []zipEntry{}
	var overrides [][2]string
	overrides = append(overrides, [2]string{"xl/workbook.xml", xlsxCtWorkbook})
	var wbRels []officeRel
	for i, s := range sheets {
		body, err := xlsxSheetXml(s, i == firstVisible, styles, &strs, strIx)
		if err != nil {
			return "", err
		}
		part := "worksheets/sheet" + strconv.Itoa(i+1) + ".xml"
		entries = append(entries, zipEntry{path: "xl/" + part, bytes: []byte(body)})
		overrides = append(overrides, [2]string{"xl/" + part, xlsxCtWorksheet})
		wbRels = append(wbRels, officeRel{id: "rId" + strconv.Itoa(i+1), typ: xlsxRelWorksheet, target: part})
	}
	n := len(sheets)
	wbRels = append(wbRels,
		officeRel{id: "rId" + strconv.Itoa(n+1), typ: xlsxRelStyles, target: "styles.xml"},
		officeRel{id: "rId" + strconv.Itoa(n+2), typ: xlsxRelShared, target: "sharedStrings.xml"})
	overrides = append(overrides, [2]string{"xl/styles.xml", xlsxCtStyles}, [2]string{"xl/sharedStrings.xml", xlsxCtShared})

	var wb officeXml
	wb.raw(officeXmlDecl)
	wb.raw(`<workbook xmlns="` + xlsxNsMain + `" xmlns:r="` + officeNsDocRelationships + `">`)
	wb.raw(`<bookViews><workbookView`)
	wb.int("activeTab", firstVisible)
	wb.raw(`/></bookViews><sheets>`)
	for i, s := range sheets {
		wb.raw(`<sheet`)
		wb.attr("name", s.name)
		wb.int("sheetId", i+1)
		switch s.visibility {
		case 1:
			wb.attr("state", "hidden")
		case 2:
			wb.attr("state", "veryHidden")
		}
		wb.attr("r:id", "rId"+strconv.Itoa(i+1))
		wb.raw(`/>`)
	}
	wb.raw(`</sheets><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`)

	var sst officeXml
	sst.raw(officeXmlDecl)
	sst.raw(`<sst xmlns="` + xlsxNsMain + `"`)
	sst.int("count", len(strs))
	sst.int("uniqueCount", len(strs))
	sst.raw(`>`)
	for _, s := range strs {
		sst.raw(`<si><t xml:space="preserve">`)
		sst.text(xlsxEscapeText(s))
		sst.raw(`</t></si>`)
	}
	sst.raw(`</sst>`)

	all := []zipEntry{
		{path: "[Content_Types].xml", bytes: []byte(officeContentTypes(overrides))},
		{path: "_rels/.rels", bytes: []byte(officeRelsXml([]officeRel{{id: "rId1", typ: xlsxRelOfficeDoc, target: "xl/workbook.xml"}}))},
		{path: "xl/workbook.xml", bytes: []byte(wb.String())},
		{path: "xl/_rels/workbook.xml.rels", bytes: []byte(officeRelsXml(wbRels))},
		{path: "xl/styles.xml", bytes: []byte(styles.xml())},
		{path: "xl/sharedStrings.xml", bytes: []byte(sst.String())},
	}
	all = append(all, entries...)
	out, fail := zipWriteAll(all, true)
	if fail != nil {
		return "", fail.skyError()
	}
	return string(out), nil
}

// xlsxSheetXml writes one worksheet part.
func xlsxSheetXml(s xlsxSheetIn, selected bool, styles *xlsxStyles, strs *[]string, strIx map[string]int) (string, any) {
	q := strconv.Quote(s.name)
	if len(s.rows) > xlsxMaxRows {
		return "", officeError(officeCodeTooManyRows,
			"Xlsx.write: the sheet "+q+" has "+strconv.Itoa(len(s.rows))+" rows, more than Excel's 1,048,576",
			[2]string{"sheet", s.name})
	}
	if s.frozenRows < 0 || s.frozenCols < 0 || s.frozenRows >= xlsxMaxRows || s.frozenCols >= xlsxMaxColumns {
		return "", officeError(officeCodeRange, "Xlsx.write: the frozen panes of sheet "+q+" are out of range",
			[2]string{"sheet", s.name})
	}
	var o officeXml
	o.raw(officeXmlDecl)
	o.raw(`<worksheet xmlns="` + xlsxNsMain + `" xmlns:r="` + officeNsDocRelationships + `">`)
	o.raw(`<sheetViews><sheetView`)
	if selected {
		o.raw(` tabSelected="1"`)
	}
	o.raw(` workbookViewId="0"`)
	if s.frozenRows > 0 || s.frozenCols > 0 {
		o.raw(`><pane`)
		if s.frozenCols > 0 {
			o.int("xSplit", s.frozenCols)
		}
		if s.frozenRows > 0 {
			o.int("ySplit", s.frozenRows)
		}
		o.attr("topLeftCell", xlsxCellName(s.frozenRows, s.frozenCols))
		pane := "bottomRight"
		if s.frozenCols == 0 {
			pane = "bottomLeft"
		} else if s.frozenRows == 0 {
			pane = "topRight"
		}
		o.attr("activePane", pane)
		o.raw(` state="frozen"/></sheetView>`)
	} else {
		o.raw(`/>`)
	}
	o.raw(`</sheetViews>`)

	if len(s.widths) > 0 {
		widths := append([][2]float64(nil), s.widths...)
		sort.SliceStable(widths, func(i, j int) bool { return widths[i][0] < widths[j][0] })
		o.raw(`<cols>`)
		for i, w := range widths {
			col := int(w[0])
			if col < 0 || col >= xlsxMaxColumns || !(w[1] > 0 && w[1] <= 255) || (i > 0 && int(widths[i-1][0]) == col) {
				return "", officeError(officeCodeColumnWidth,
					"Xlsx.write: the column width ("+strconv.Itoa(col)+", "+xlsxFormatNumber(w[1])+") on sheet "+q+
						" is not valid (a column 0 to 16,383 given once, a width above 0 and at most 255)",
					[2]string{"sheet", s.name})
			}
			o.raw(`<col`)
			o.int("min", col+1)
			o.int("max", col+1)
			o.attr("width", xlsxFormatNumber(w[1]))
			o.raw(` customWidth="1"/>`)
		}
		o.raw(`</cols>`)
	}

	o.raw(`<sheetData>`)
	for ri, row := range s.rows {
		if len(row) > xlsxMaxColumns {
			return "", officeError(officeCodeTooManyColumns,
				"Xlsx.write: row "+strconv.Itoa(ri)+" of sheet "+q+" has "+strconv.Itoa(len(row))+" cells, more than Excel's 16,384 columns",
				[2]string{"sheet", s.name}, [2]string{"row", strconv.Itoa(ri)})
		}
		o.raw(`<row`)
		o.int("r", ri+1)
		if len(row) == 0 {
			o.raw(`/>`)
			continue
		}
		o.raw(`>`)
		for ci, c := range row {
			if err := xlsxCellXml(&o, s.name, ri, ci, c, styles, strs, strIx); err != nil {
				return "", err
			}
		}
		o.raw(`</row>`)
	}
	o.raw(`</sheetData>`)

	if len(s.merged) > 0 {
		merged := append([][4]int(nil), s.merged...)
		for _, m := range merged {
			if err := xlsxCheckRange(s.name, "merged", m); err != nil {
				return "", err
			}
			if m[0] == m[2] && m[1] == m[3] {
				return "", officeError(officeCodeRange, "Xlsx.write: the merged range "+xlsxRangeRef(m)+" on sheet "+q+" is one cell",
					[2]string{"sheet", s.name}, [2]string{"range", xlsxRangeRef(m)})
			}
		}
		sort.SliceStable(merged, func(i, j int) bool { return merged[i][0] < merged[j][0] })
		for i, a := range merged {
			for _, b := range merged[i+1:] {
				if b[0] > a[2] {
					break
				}
				if b[1] <= a[3] && a[1] <= b[3] {
					return "", officeError(officeCodeRange,
						"Xlsx.write: the merged ranges "+xlsxRangeRef(a)+" and "+xlsxRangeRef(b)+" on sheet "+q+" overlap",
						[2]string{"sheet", s.name})
				}
			}
		}
		o.raw(`<mergeCells`)
		o.int("count", len(s.merged))
		o.raw(`>`)
		for _, m := range s.merged {
			o.raw(`<mergeCell`)
			o.attr("ref", xlsxRangeRef(m))
			o.raw(`/>`)
		}
		o.raw(`</mergeCells>`)
	}

	if len(s.validations) > 0 {
		o.raw(`<dataValidations`)
		o.int("count", len(s.validations))
		o.raw(`>`)
		for _, v := range s.validations {
			if err := xlsxCheckRange(s.name, "validation", v.rg); err != nil {
				return "", err
			}
			var formula string
			if v.kind == 0 {
				if len(v.items) == 0 {
					return "", officeError(officeCodeValidationItem, "Xlsx.write: the validation list on "+xlsxRangeRef(v.rg)+" of sheet "+q+" is empty",
						[2]string{"sheet", s.name})
				}
				for _, it := range v.items {
					if strings.ContainsAny(it, `,"`) || it == "" {
						return "", officeError(officeCodeValidationItem,
							"Xlsx.write: the validation item "+strconv.Quote(it)+" on sheet "+q+
								" is empty or contains a comma or a double quote, which an inline list cannot hold; put the items in cells and use ListFromRange",
							[2]string{"sheet", s.name}, [2]string{"item", it})
					}
				}
				joined := strings.Join(v.items, ",")
				if officeRuneCount(joined) > xlsxMaxListLength {
					return "", officeError(officeCodeValidationTooLong,
						"Xlsx.write: the validation list on "+xlsxRangeRef(v.rg)+" of sheet "+q+" is "+strconv.Itoa(officeRuneCount(joined))+
							" characters, more than Excel's 255 for an inline list; put the items in cells and use ListFromRange",
						[2]string{"sheet", s.name}, [2]string{"length", strconv.Itoa(officeRuneCount(joined))})
				}
				formula = `"` + joined + `"`
			} else {
				formula = strings.TrimPrefix(v.source, "=")
				if formula == "" || officeRuneCount(formula) > xlsxMaxListLength {
					return "", officeError(officeCodeValidationTooLong,
						"Xlsx.write: the validation source "+strconv.Quote(formula)+" on sheet "+q+" is empty or longer than 255 characters",
						[2]string{"sheet", s.name})
				}
			}
			o.raw(`<dataValidation type="list" allowBlank="1" showInputMessage="1" showErrorMessage="1"`)
			o.attr("sqref", xlsxRangeRef(v.rg))
			o.raw(`><formula1>`)
			o.text(formula)
			o.raw(`</formula1></dataValidation>`)
		}
		o.raw(`</dataValidations>`)
	}
	o.raw(`</worksheet>`)
	return o.String(), nil
}

// xlsxCellXml writes one <c>.
func xlsxCellXml(o *officeXml, sheet string, ri, ci int, c xlsxCell, styles *xlsxStyles, strs *[]string, strIx map[string]int) any {
	ref := xlsxCellName(ri, ci)
	st := c.style
	if c.kind == xlsxKindDate && st.format == "" {
		st.format = xlsxDefaultDate
	}
	o.raw(`<c`)
	o.attr("r", ref)
	if s := styles.index(st); s != 0 {
		o.int("s", s)
	}
	badText := func(what string) any {
		return officeError(officeCodeTextTooLong,
			"Xlsx.write: the "+what+" in cell "+ref+" of sheet "+strconv.Quote(sheet)+" is longer than Excel's 32,767 characters",
			[2]string{"sheet", sheet}, [2]string{"cell", ref})
	}
	switch c.kind {
	case xlsxKindText:
		if officeRuneCount(c.text) > xlsxMaxText {
			return badText("text")
		}
		ix, ok := strIx[c.text]
		if !ok {
			ix = len(*strs)
			*strs = append(*strs, c.text)
			strIx[c.text] = ix
		}
		o.raw(` t="s"><v>` + strconv.Itoa(ix) + `</v></c>`)
	case xlsxKindNumber:
		if math.IsNaN(c.number) || math.IsInf(c.number, 0) {
			return officeError(officeCodeNumber, "Xlsx.write: the number in cell "+ref+" of sheet "+strconv.Quote(sheet)+" is not finite",
				[2]string{"sheet", sheet}, [2]string{"cell", ref})
		}
		o.raw(`><v>` + xlsxFormatNumber(c.number) + `</v></c>`)
	case xlsxKindBool:
		v := "0"
		if c.flag {
			v = "1"
		}
		o.raw(` t="b"><v>` + v + `</v></c>`)
	case xlsxKindDate:
		serial, ok := xlsxDateSerial(c.parts)
		if !ok {
			return officeError(officeCodeDateRange,
				"Xlsx.write: the date in cell "+ref+" of sheet "+strconv.Quote(sheet)+" is not a valid date from 1900-01-01 to 9999-12-31 (or a time on 1899-12-30)",
				[2]string{"sheet", sheet}, [2]string{"cell", ref})
		}
		o.raw(`><v>` + xlsxFormatNumber(serial) + `</v></c>`)
	case xlsxKindFormula:
		f := strings.TrimPrefix(c.text, "=")
		if f == "" {
			return officeError(officeCodeFormula, "Xlsx.write: the formula in cell "+ref+" of sheet "+strconv.Quote(sheet)+" is empty",
				[2]string{"sheet", sheet}, [2]string{"cell", ref})
		}
		if officeRuneCount(f) > 8192 {
			return officeError(officeCodeTextTooLong, "Xlsx.write: the formula in cell "+ref+" of sheet "+strconv.Quote(sheet)+" is longer than Excel's 8,192 characters",
				[2]string{"sheet", sheet}, [2]string{"cell", ref})
		}
		o.raw(`><f>`)
		o.text(f)
		o.raw(`</f></c>`)
	case xlsxKindFailure:
		if !xlsxErrorValue(c.text) {
			return officeError(officeCodeFailureValue, "Xlsx.write: "+strconv.Quote(c.text)+" in cell "+ref+" of sheet "+strconv.Quote(sheet)+" is not an Excel error value (#N/A, #DIV/0!, #VALUE!, …)",
				[2]string{"sheet", sheet}, [2]string{"cell", ref})
		}
		o.raw(` t="e"><v>`)
		o.text(c.text)
		o.raw(`</v></c>`)
	default:
		o.raw(`/>`)
	}
	return nil
}

// Office_xlsxWrite is the primitive behind `Xlsx.write`.
func Office_xlsxWrite(sheets any) any {
	out, err := xlsxWriteAll(xlsxSheetsIn(sheets))
	if err != nil {
		return Err[any, any](err)
	}
	return Ok[any, any](out)
}
