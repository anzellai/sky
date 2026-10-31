package rt

// office_xlsx_read.go — Std.Office.Xlsx.read / readWith / sheetNames.
//
// The workbook, its relationships and styles.xml are small and read as trees.
// The shared strings and every worksheet are streamed token by token
// (xmlStreamSafe), so a large sheet costs O(cells) and builds no tree.
//
// What a cell reads as:
//
//   - t="s" (a shared string), t="str", t="inlineStr": Text. Rich-text runs
//     are joined; phonetic runs (rPh) are skipped; `_xHHHH_` escapes decode.
//   - t="b": Bool. t="e": Failure (the error text, `#DIV/0!`). t="d": Date.
//   - a number: Date when its style's number format is a date (built-in ids
//     14 to 22 and 45 to 47, or a custom code with d, m, y, h or s outside
//     quotes and brackets), else Number.
//   - a formula: Formula with its text and the cached value (Empty when the
//     file has none). A shared formula's followers get the master's text with
//     the relative references moved.
//
// Rows keep their index: a missing row is an empty list, a missing cell
// before the last one in its row is Empty. Serial 60 of the 1900 system is
// Excel's phantom 1900-02-29 and reads as that date; a date1904 workbook
// counts from 1904-01-01.
//
// The Limits bound what is read: sheets, the row and column of any cell, and
// the cells materialised (gaps included), so a sparse cell far away cannot
// make the reader allocate millions of Empty cells.
//
// Representation. Sky's `Cell` holds a `DateTime` record, which a kernel
// cannot build as the typed record Sky compares it with. The kernel returns a
// private ADT (`RawCell`, the tags below) and Std.Office.Xlsx maps it to
// `Cell` in Sky.

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The RawCell constructors (sky-stdlib/Std/Office/Xlsx.sky), by tag.
const (
	xlsxRawEmpty   = 0
	xlsxRawText    = 1
	xlsxRawNumber  = 2
	xlsxRawBool    = 3
	xlsxRawDate    = 4
	xlsxRawFormula = 5
	xlsxRawFailure = 6
)

var xlsxRawNames = [...]string{"RawEmpty", "RawText", "RawNumber", "RawBool", "RawDate", "RawFormula", "RawFailure"}

func xlsxRaw(tag int, fields ...any) SkyADT {
	if fields == nil {
		fields = []any{}
	}
	return SkyADT{Tag: tag, SkyName: xlsxRawNames[tag], Fields: fields}
}

// xlsxRawEmptyCell is an empty cell. A function, not a package variable: a
// variable built at init would be linked into every program, the Sky.Spa
// wasm client included, which never reads a workbook.
func xlsxRawEmptyCell() SkyADT { return xlsxRaw(xlsxRawEmpty) }

// xlsxLimits is Std.Office.Xlsx.Limits without its zip part.
type xlsxLimits struct {
	maxSheets  int
	maxRows    int
	maxCells   int
	maxColumns int
}

// xlsxMaxRows / xlsxMaxColumns are the sizes of an Excel sheet.
const (
	xlsxMaxRows    = 1_048_576
	xlsxMaxColumns = 16_384
)

// xlsxDepth bounds the element depth of every part of a workbook.
const xlsxDepth = 256

// xlsxSheetRef is one <sheet> of workbook.xml.
type xlsxSheetRef struct {
	name       string
	visibility int // 0 Visible, 1 Hidden, 2 VeryHidden
	part       string
}

// xlsxWorkbookInfo is what workbook.xml and its relationships say.
type xlsxWorkbookInfo struct {
	part          string
	sheets        []xlsxSheetRef
	date1904      bool
	sharedStrings string // part name, "" when none
	styles        string
}

func xlsxVisibility(state string) int {
	switch state {
	case "hidden":
		return 1
	case "veryHidden":
		return 2
	}
	return 0
}

// xlsxOpenWorkbook reads workbook.xml and its relationships.
func xlsxOpenWorkbook(p *officePackage) (*xlsxWorkbookInfo, any) {
	part, err := p.mainPart("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	root, err := p.tree(part)
	if err != nil {
		return nil, err
	}
	if root == nil || root.name != "workbook" {
		return nil, officeError(officeCodeNotOffice, "Xlsx: the file is not a workbook (no workbook part "+strconv.Quote(part)+")",
			[2]string{"part", part})
	}
	byID, list, err := p.rels(part)
	if err != nil {
		return nil, err
	}
	info := &xlsxWorkbookInfo{part: part}
	if pr := officeChild(root, "workbookPr"); pr != nil {
		v := strings.ToLower(xmlAttrLocal(pr, "date1904"))
		info.date1904 = v == "1" || v == "true"
	}
	for _, r := range list {
		switch {
		case strings.HasSuffix(r.typ, "/sharedStrings") && !r.external:
			info.sharedStrings = r.target
		case strings.HasSuffix(r.typ, "/styles") && !r.external:
			info.styles = r.target
		}
	}
	for _, s := range officeChildren(officeChild(root, "sheets"), "sheet") {
		ref := xlsxSheetRef{name: xmlAttrLocal(s, "name"), visibility: xlsxVisibility(xmlAttrLocal(s, "state"))}
		for _, a := range s.attrs {
			if a.name == "id" && strings.Contains(a.space, "relationships") {
				if r, ok := byID[a.value]; ok && !r.external {
					ref.part = r.target
				}
			}
		}
		info.sheets = append(info.sheets, ref)
	}
	return info, nil
}

// xlsxDateFormat reports whether a number format code shows a date or a
// time: a d, m, y, h or s outside quotes, brackets and escapes.
func xlsxDateFormat(code string) bool {
	inQuote := false
	for i := 0; i < len(code); i++ {
		c := code[i]
		if inQuote {
			if c == '"' {
				inQuote = false
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '[':
			if j := strings.IndexByte(code[i:], ']'); j >= 0 {
				i += j
			}
		case '\\', '_', '*':
			i++ // the next character is literal (or a fill / space width)
		case 'd', 'D', 'm', 'M', 'y', 'Y', 'h', 'H', 's', 'S':
			return true
		}
	}
	return false
}

// xlsxBuiltinDate is the built-in number formats that are dates.
func xlsxBuiltinDate(id int) bool {
	return (id >= 14 && id <= 22) || (id >= 45 && id <= 47)
}

// xlsxDateStyles reads styles.xml: for each cellXfs index, whether its number
// format is a date.
func xlsxDateStyles(p *officePackage, part string) ([]bool, any) {
	if part == "" {
		return nil, nil
	}
	root, err := p.tree(part)
	if err != nil || root == nil {
		return nil, err
	}
	custom := map[int]string{}
	for _, f := range officeChildren(officeChild(root, "numFmts"), "numFmt") {
		id, e := strconv.Atoi(xmlAttrLocal(f, "numFmtId"))
		if e == nil {
			custom[id] = xmlAttrLocal(f, "formatCode")
		}
	}
	var out []bool
	for _, xf := range officeChildren(officeChild(root, "cellXfs"), "xf") {
		id, _ := strconv.Atoi(xmlAttrLocal(xf, "numFmtId"))
		date := false
		if code, ok := custom[id]; ok {
			date = xlsxDateFormat(code)
		} else {
			date = xlsxBuiltinDate(id)
		}
		out = append(out, date)
	}
	return out, nil
}

// xlsxUnescape decodes the `_xHHHH_` escapes Excel writes for characters XML
// cannot carry (and `_x005F_` for a literal underscore before one).
func xlsxUnescape(s string) string {
	if !strings.Contains(s, "_x") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+7 <= len(s) && s[i] == '_' && s[i+1] == 'x' && s[i+6] == '_' {
			if v, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
				b.WriteRune(rune(v))
				i += 7
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// xlsxSharedStrings streams sharedStrings.xml. Each <si> is one string: its
// <t> text, or its rich runs' <t> joined, phonetic runs (rPh) skipped.
func xlsxSharedStrings(p *officePackage, part string, maxStrings int) ([]string, any) {
	if part == "" {
		return nil, nil
	}
	var out []string
	var sb strings.Builder
	var names []string // the open elements, so end knows which one closes
	inSi, inT, rph := false, false, 0
	var tooMany any
	ev := xmlEvents{
		start: func(name, _ string, _ []xmlAttr) *xmlFailure {
			names = append(names, name)
			switch name {
			case "si":
				inSi = true
				sb.Reset()
			case "rPh":
				rph++
			case "t":
				inT = inSi && rph == 0
			}
			return nil
		},
		end: func() *xmlFailure {
			name := names[len(names)-1]
			names = names[:len(names)-1]
			switch name {
			case "si":
				if len(out) >= maxStrings {
					tooMany = officeError(officeCodeTooManyCells,
						"Xlsx: the shared strings hold more than "+strconv.Itoa(maxStrings)+" strings (maxCells)",
						[2]string{"limit", strconv.Itoa(maxStrings)})
					return xmlFail("stop", "stop")
				}
				out = append(out, xlsxUnescape(sb.String()))
				inSi = false
			case "rPh":
				rph--
			case "t":
				inT = false
			}
			return nil
		},
		text: func(t []byte) *xmlFailure {
			if inT {
				sb.Write(t)
			}
			return nil
		},
	}
	_, err := p.stream(part, ev)
	if tooMany != nil {
		return nil, tooMany
	}
	return out, err
}

// xlsxColumn parses column letters ("A" = 0, "XFD" = 16383); ok is false for
// anything else.
func xlsxColumn(s string) (int, bool) {
	if s == "" || len(s) > 3 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c < 'A' || c > 'Z' {
			return 0, false
		}
		n = n*26 + int(c-'A'+1)
	}
	if n > xlsxMaxColumns {
		return 0, false
	}
	return n - 1, true
}

// xlsxColumnName is the letters of a 0-based column.
func xlsxColumnName(col int) string {
	var buf [4]byte
	i := len(buf)
	for n := col + 1; n > 0; n = (n - 1) / 26 {
		i--
		buf[i] = byte('A' + (n-1)%26)
	}
	return string(buf[i:])
}

// xlsxCellName is "A1" for row 0, column 0.
func xlsxCellName(row, col int) string {
	return xlsxColumnName(col) + strconv.Itoa(row+1)
}

// xlsxParseRef parses a cell reference ("B3", "$B$3") to 0-based row and
// column.
func xlsxParseRef(ref string) (row, col int, ok bool) {
	ref = strings.ReplaceAll(ref, "$", "")
	i := 0
	for i < len(ref) && ((ref[i] >= 'A' && ref[i] <= 'Z') || (ref[i] >= 'a' && ref[i] <= 'z')) {
		i++
	}
	col, ok = xlsxColumn(ref[:i])
	if !ok || i == len(ref) {
		return 0, 0, false
	}
	r, err := strconv.Atoi(ref[i:])
	if err != nil || r < 1 || r > xlsxMaxRows {
		return 0, 0, false
	}
	return r - 1, col, true
}

// xlsxRangeOf parses "A1:C3" (or one cell "A1") to a Range record.
func xlsxRangeOf(ref string) (map[string]any, bool) {
	a, b, found := strings.Cut(ref, ":")
	r1, c1, ok := xlsxParseRef(a)
	if !ok {
		return nil, false
	}
	r2, c2 := r1, c1
	if found {
		if r2, c2, ok = xlsxParseRef(b); !ok {
			return nil, false
		}
	}
	return map[string]any{
		"firstRow": min(r1, r2), "firstColumn": min(c1, c2),
		"lastRow": max(r1, r2), "lastColumn": max(c1, c2),
	}, true
}

// xlsxShiftFormula moves the relative references of a shared formula's
// master text by (dr, dc), as Excel does for each follower cell. Strings are
// left alone, absolute ($) parts do not move, and a reference moved off the
// sheet becomes #REF!.
func xlsxShiftFormula(f string, dr, dc int) string {
	if dr == 0 && dc == 0 {
		return f
	}
	var b strings.Builder
	isWord := func(c byte) bool {
		return c == '_' || c == '.' || c == '$' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	for i := 0; i < len(f); {
		c := f[i]
		switch {
		case c == '"': // a string literal, "" inside
			j := i + 1
			for j < len(f) {
				if f[j] == '"' {
					if j+1 < len(f) && f[j+1] == '"' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(f))
			b.WriteString(f[i:j])
			i = j
		case c == '\'': // a quoted sheet name
			j := i + 1
			for j < len(f) {
				if f[j] == '\'' {
					if j+1 < len(f) && f[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(f))
			b.WriteString(f[i:j])
			i = j
		case isWord(c):
			j := i
			for j < len(f) && isWord(f[j]) {
				j++
			}
			tok := f[i:j]
			if (i > 0 && isWord(f[i-1])) || (j < len(f) && (f[j] == '(' || f[j] == '!')) {
				b.WriteString(tok)
			} else {
				b.WriteString(xlsxShiftToken(tok, dr, dc))
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// xlsxShiftToken moves one token when it is a cell reference ("B$3").
func xlsxShiftToken(tok string, dr, dc int) string {
	i := 0
	colAbs := false
	if i < len(tok) && tok[i] == '$' {
		colAbs = true
		i++
	}
	j := i
	for j < len(tok) && ((tok[j] >= 'A' && tok[j] <= 'Z') || (tok[j] >= 'a' && tok[j] <= 'z')) {
		j++
	}
	col, ok := xlsxColumn(tok[i:j])
	if !ok || j == len(tok) {
		return tok
	}
	k := j
	rowAbs := false
	if tok[k] == '$' {
		rowAbs = true
		k++
	}
	if k == len(tok) {
		return tok
	}
	for m := k; m < len(tok); m++ {
		if tok[m] < '0' || tok[m] > '9' {
			return tok
		}
	}
	row, err := strconv.Atoi(tok[k:])
	if err != nil || row < 1 {
		return tok
	}
	if !colAbs {
		col += dc
	}
	if !rowAbs {
		row += dr
	}
	if col < 0 || col >= xlsxMaxColumns || row < 1 || row > xlsxMaxRows {
		return "#REF!"
	}
	out := ""
	if colAbs {
		out += "$"
	}
	out += xlsxColumnName(col)
	if rowAbs {
		out += "$"
	}
	return out + strconv.Itoa(row)
}

// xlsxSerialParts converts a date serial to its parts (year … millisecond),
// to the nearest millisecond.
func xlsxSerialParts(serial float64, date1904 bool) [7]int {
	total := int64(math.Round(serial * 86_400_000))
	days := total / 86_400_000
	ms := total % 86_400_000
	if ms < 0 {
		ms += 86_400_000
		days--
	}
	var base time.Time
	switch {
	case date1904:
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	case days == 60:
		// Excel's phantom 1900-02-29 (Lotus 1-2-3 treated 1900 as a leap year).
		return [7]int{1900, 2, 29, int(ms / 3_600_000), int(ms / 60_000 % 60), int(ms / 1000 % 60), int(ms % 1000)}
	case days >= 1 && days < 60:
		base = time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	default:
		base = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	}
	t := base.AddDate(0, 0, int(days))
	return [7]int{t.Year(), int(t.Month()), t.Day(), int(ms / 3_600_000), int(ms / 60_000 % 60), int(ms / 1000 % 60), int(ms % 1000)}
}

func xlsxRawDateOf(p [7]int) SkyADT {
	return xlsxRaw(xlsxRawDate, p[0], p[1], p[2], p[3], p[4], p[5], p[6])
}

// xlsxISODate parses the value of a t="d" cell.
func xlsxISODate(s string) (SkyADT, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02", "15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			y := t.Year()
			if layout == "15:04:05.999999999" {
				y = 1899
				t = time.Date(1899, 12, 30, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
			}
			return xlsxRawDateOf([7]int{y, int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond() / 1_000_000}), true
		}
	}
	return SkyADT{}, false
}

// xlsxSheet is one worksheet as the reader builds it.
type xlsxSheet struct {
	rows        [][]any
	merged      []any
	widths      []any
	frozenRows  int
	frozenCols  int
	validations []any
}

// xlsxSheetReader streams one worksheet.
type xlsxSheetReader struct {
	lim       xlsxLimits
	cells     *int // cells materialised across the workbook
	shared    []string
	dates     []bool
	date1904  bool
	out       xlsxSheet
	names     []string
	row       int // current row (0-based), -1 before the first
	col       int // last column in the row
	inData    bool
	cell      struct{ t, s, ref string }
	val       strings.Builder
	inV       bool
	inline    strings.Builder
	inIs      bool
	inT       bool
	rph       int
	formula   strings.Builder
	inF       bool
	hasF      bool
	fShared   bool
	fSi       string
	masters   map[string][3]any // si -> {text, row, col}
	validType string
	validRef  string
	inFormula bool
	formula1  strings.Builder
	fail      any
}

func (r *xlsxSheetReader) stop(err any) *xmlFailure {
	r.fail = err
	return xmlFail("stop", "stop")
}

// count adds n to what the workbook holds (cells, and the merges, widths and
// validations kept beside them) and stops the read past maxCells.
func (r *xlsxSheetReader) count(n int) *xmlFailure {
	*r.cells += n
	if *r.cells > r.lim.maxCells {
		return r.stop(officeError(officeCodeTooManyCells,
			"Xlsx: the workbook has more than "+strconv.Itoa(r.lim.maxCells)+" cells (gaps before a cell, merges, column widths and validations count)",
			[2]string{"limit", strconv.Itoa(r.lim.maxCells)}))
	}
	return nil
}

func (r *xlsxSheetReader) start(name, _ string, attrs []xmlAttr) *xmlFailure {
	r.names = append(r.names, name)
	switch name {
	case "sheetData":
		r.inData = true
	case "row":
		if !r.inData {
			return nil
		}
		next := r.row + 1
		if v := officeAttr(attrs, "r"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return r.stop(officeError(officeCodeCorrupt, "Xlsx: a row has the index "+strconv.Quote(v)))
			}
			next = n - 1
		}
		if next+1 > r.lim.maxRows {
			return r.stop(officeError(officeCodeTooManyRows,
				"Xlsx: a sheet has a row "+strconv.Itoa(next+1)+", more than the limit of "+strconv.Itoa(r.lim.maxRows)+" rows",
				[2]string{"limit", strconv.Itoa(r.lim.maxRows)}))
		}
		if next < len(r.out.rows) {
			// A row repeated or out of order: keep adding to it.
			next = max(next, len(r.out.rows)-1)
		}
		for len(r.out.rows) <= next {
			r.out.rows = append(r.out.rows, []any{})
		}
		r.row = next
		r.col = len(r.out.rows[next]) - 1
	case "c":
		if !r.inData || r.row < 0 {
			return nil
		}
		r.cell.t = officeAttr(attrs, "t")
		r.cell.s = officeAttr(attrs, "s")
		r.cell.ref = officeAttr(attrs, "r")
		r.val.Reset()
		r.inline.Reset()
		r.formula.Reset()
		r.hasF, r.fShared, r.fSi = false, false, ""
	case "v":
		r.inV = true
	case "is":
		r.inIs = true
	case "rPh":
		r.rph++
	case "t":
		r.inT = r.inIs && r.rph == 0
	case "f":
		r.inF = true
		r.hasF = true
		r.fShared = officeAttr(attrs, "t") == "shared"
		r.fSi = officeAttr(attrs, "si")
	case "mergeCell":
		if rg, ok := xlsxRangeOf(officeAttr(attrs, "ref")); ok {
			if f := r.count(1); f != nil {
				return f
			}
			r.out.merged = append(r.out.merged, rg)
		}
	case "pane":
		st := officeAttr(attrs, "state")
		if st == "frozen" || st == "frozenSplit" {
			x, _ := strconv.ParseFloat(officeAttr(attrs, "xSplit"), 64)
			y, _ := strconv.ParseFloat(officeAttr(attrs, "ySplit"), 64)
			r.out.frozenCols, r.out.frozenRows = int(x), int(y)
		}
	case "col":
		lo, e1 := strconv.Atoi(officeAttr(attrs, "min"))
		hi, e2 := strconv.Atoi(officeAttr(attrs, "max"))
		w, e3 := strconv.ParseFloat(officeAttr(attrs, "width"), 64)
		if e1 != nil || e2 != nil || e3 != nil || lo < 1 || hi < lo {
			return nil
		}
		if !officeAttrHas(attrs, "customWidth") || !officeOn([]xmlAttr{{name: "val", value: officeAttr(attrs, "customWidth")}}) {
			return nil
		}
		hi = min(hi, min(r.lim.maxColumns, xlsxMaxColumns))
		if f := r.count(max(hi-lo+1, 0)); f != nil {
			return f
		}
		for c := lo; c <= hi; c++ {
			r.out.widths = append(r.out.widths, map[string]any{"column": c - 1, "width": w})
		}
	case "dataValidation":
		r.validType = officeAttr(attrs, "type")
		r.validRef = officeAttr(attrs, "sqref")
		r.formula1.Reset()
	case "formula1":
		r.inFormula = true
	}
	return nil
}

func (r *xlsxSheetReader) text(t []byte) *xmlFailure {
	switch {
	case r.inV:
		r.val.Write(t)
	case r.inF:
		r.formula.Write(t)
	case r.inT:
		r.inline.Write(t)
	case r.inFormula:
		r.formula1.Write(t)
	}
	return nil
}

func (r *xlsxSheetReader) end() *xmlFailure {
	name := r.names[len(r.names)-1]
	r.names = r.names[:len(r.names)-1]
	switch name {
	case "sheetData":
		r.inData = false
	case "v":
		r.inV = false
	case "is":
		r.inIs = false
	case "rPh":
		r.rph--
	case "t":
		r.inT = false
	case "f":
		r.inF = false
	case "formula1":
		r.inFormula = false
	case "dataValidation":
		r.endValidation()
		if r.fail != nil {
			return xmlFail("stop", "stop")
		}
	case "c":
		if r.inData && r.row >= 0 {
			return r.endCell()
		}
	}
	return nil
}

// endValidation keeps a list validation: an inline list (a quoted formula)
// is ListOf, anything else ListFromRange. Each range of sqref is one.
func (r *xlsxSheetReader) endValidation() {
	if r.validType != "list" {
		return
	}
	f := strings.TrimSpace(r.formula1.String())
	kind, items, source := 1, []any{}, strings.TrimPrefix(f, "=")
	if len(f) >= 2 && f[0] == '"' && f[len(f)-1] == '"' {
		kind, source = 0, ""
		inner := strings.ReplaceAll(f[1:len(f)-1], `""`, `"`)
		for _, it := range strings.Split(inner, ",") {
			items = append(items, it)
		}
	}
	for _, ref := range strings.Fields(r.validRef) {
		if rg, ok := xlsxRangeOf(ref); ok {
			if r.count(1) != nil {
				return
			}
			r.out.validations = append(r.out.validations, map[string]any{
				"kind": kind, "range": rg, "items": items, "source": source,
			})
		}
	}
}

func (r *xlsxSheetReader) endCell() *xmlFailure {
	col := r.col + 1
	if r.cell.ref != "" {
		_, c, ok := xlsxParseRef(r.cell.ref)
		if !ok {
			// A column past the last one (or a broken reference).
			letters := strings.TrimRight(strings.ReplaceAll(r.cell.ref, "$", ""), "0123456789")
			if len(letters) > 0 && len(letters) <= 3 {
				return r.stop(officeError(officeCodeTooManyColumns,
					"Xlsx: the cell "+strconv.Quote(r.cell.ref)+" is past the last column",
					[2]string{"cell", r.cell.ref}))
			}
			return r.stop(officeError(officeCodeCorrupt, "Xlsx: the cell reference "+strconv.Quote(r.cell.ref)+" is not valid",
				[2]string{"cell", r.cell.ref}))
		}
		col = c
	}
	if col+1 > r.lim.maxColumns {
		return r.stop(officeError(officeCodeTooManyColumns,
			"Xlsx: a cell is in column "+strconv.Itoa(col+1)+", more than the limit of "+strconv.Itoa(r.lim.maxColumns)+" columns",
			[2]string{"limit", strconv.Itoa(r.lim.maxColumns)}))
	}
	row := r.out.rows[r.row]
	if col < len(row) {
		col = len(row) // a cell repeated or out of order goes after the last
		if col+1 > r.lim.maxColumns {
			return r.stop(officeError(officeCodeTooManyColumns,
				"Xlsx: a row has more than "+strconv.Itoa(r.lim.maxColumns)+" columns",
				[2]string{"limit", strconv.Itoa(r.lim.maxColumns)}))
		}
	}
	if f := r.count(col + 1 - len(row)); f != nil {
		return f
	}
	for len(row) < col {
		row = append(row, xlsxRawEmptyCell())
	}
	v, err := r.value()
	if err != nil {
		return r.stop(err)
	}
	if r.hasF {
		text := r.formula.String()
		if r.fShared && r.fSi != "" {
			if text != "" {
				if r.masters == nil {
					r.masters = map[string][3]any{}
				}
				r.masters[r.fSi] = [3]any{text, r.row, col}
			} else if m, ok := r.masters[r.fSi]; ok {
				text = xlsxShiftFormula(m[0].(string), r.row-m[1].(int), col-m[2].(int))
			}
		}
		v = xlsxRaw(xlsxRawFormula, text, v)
	}
	row = append(row, v)
	r.out.rows[r.row] = row
	r.col = col
	return nil
}

// value is the cell's value (a formula's cached value).
func (r *xlsxSheetReader) value() (SkyADT, any) {
	v := r.val.String()
	switch r.cell.t {
	case "s":
		if strings.TrimSpace(v) == "" {
			return xlsxRawEmptyCell(), nil
		}
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || i < 0 || i >= len(r.shared) {
			return SkyADT{}, officeError(officeCodeCorrupt, "Xlsx: the cell "+r.cell.ref+" names shared string "+strconv.Quote(v)+", which the workbook does not have",
				[2]string{"cell", r.cell.ref})
		}
		return xlsxRaw(xlsxRawText, r.shared[i]), nil
	case "inlineStr":
		return xlsxRaw(xlsxRawText, xlsxUnescape(r.inline.String())), nil
	case "str":
		return xlsxRaw(xlsxRawText, xlsxUnescape(v)), nil
	case "b":
		if v == "" {
			return xlsxRawEmptyCell(), nil
		}
		return xlsxRaw(xlsxRawBool, v == "1" || strings.EqualFold(v, "true")), nil
	case "e":
		return xlsxRaw(xlsxRawFailure, v), nil
	case "d":
		if v == "" {
			return xlsxRawEmptyCell(), nil
		}
		if d, ok := xlsxISODate(strings.TrimSpace(v)); ok {
			return d, nil
		}
		return xlsxRaw(xlsxRawText, v), nil
	}
	if strings.TrimSpace(v) == "" {
		return xlsxRawEmptyCell(), nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return SkyADT{}, officeError(officeCodeCorrupt, "Xlsx: the cell "+r.cell.ref+" holds "+strconv.Quote(v)+", which is not a number",
			[2]string{"cell", r.cell.ref})
	}
	if s, err := strconv.Atoi(r.cell.s); err == nil && s >= 0 && s < len(r.dates) && r.dates[s] {
		return xlsxRawDateOf(xlsxSerialParts(f, r.date1904)), nil
	}
	return xlsxRaw(xlsxRawNumber, f), nil
}

// xlsxReadAll is Std.Office.Xlsx.readWith.
func xlsxReadAll(data string, zl zipLimits, lim xlsxLimits) (map[string]any, any) {
	p, err := officeOpen(data, zl, xlsxDepth)
	if err != nil {
		return nil, err
	}
	info, err := xlsxOpenWorkbook(p)
	if err != nil {
		return nil, err
	}
	if len(info.sheets) > lim.maxSheets {
		return nil, officeError(officeCodeTooManySheets,
			"Xlsx: the workbook has "+strconv.Itoa(len(info.sheets))+" sheets, more than the limit of "+strconv.Itoa(lim.maxSheets),
			[2]string{"sheets", strconv.Itoa(len(info.sheets))}, [2]string{"limit", strconv.Itoa(lim.maxSheets)})
	}
	shared, err := xlsxSharedStrings(p, info.sharedStrings, lim.maxCells)
	if err != nil {
		return nil, err
	}
	dates, err := xlsxDateStyles(p, info.styles)
	if err != nil {
		return nil, err
	}
	cells := 0
	sheets := make([]any, 0, len(info.sheets))
	for _, ref := range info.sheets {
		r := &xlsxSheetReader{lim: lim, cells: &cells, shared: shared, dates: dates, date1904: info.date1904, row: -1}
		if ref.part != "" {
			_, err := p.stream(ref.part, xmlEvents{start: r.start, end: r.end, text: r.text})
			if r.fail != nil {
				return nil, r.fail
			}
			if err != nil {
				return nil, err
			}
		}
		sort.SliceStable(r.out.widths, func(i, j int) bool {
			return r.out.widths[i].(map[string]any)["column"].(int) < r.out.widths[j].(map[string]any)["column"].(int)
		})
		rows := make([]any, len(r.out.rows))
		for i, row := range r.out.rows {
			rows[i] = row
		}
		sheets = append(sheets, map[string]any{
			"name":          ref.name,
			"visibility":    ref.visibility,
			"rows":          rows,
			"merged":        officeList(r.out.merged),
			"columnWidths":  officeList(r.out.widths),
			"frozenRows":    r.out.frozenRows,
			"frozenColumns": r.out.frozenCols,
			"validations":   officeList(r.out.validations),
		})
	}
	return map[string]any{"sheets": sheets, "date1904": info.date1904}, nil
}

// officeList is l, or an empty list for nil.
func officeList(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// xlsxSheetNamesOf is Std.Office.Xlsx.sheetNames: workbook.xml only.
func xlsxSheetNamesOf(data string) ([]any, any) {
	p, err := officeOpen(data, zipDefaultLimits, xlsxDepth)
	if err != nil {
		return nil, err
	}
	info, err := xlsxOpenWorkbook(p)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(info.sheets))
	for i, s := range info.sheets {
		out[i] = map[string]any{"name": s.name, "visibility": s.visibility}
	}
	return out, nil
}

// Office_xlsxRead is the primitive behind `Xlsx.read` / `Xlsx.readWith`: the
// Limits record as eight Ints (the zip limits first).
func Office_xlsxRead(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio, maxSheets, maxRows, maxCells, maxColumns, data any) any {
	zl, bad := zipLimitsOf(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio)
	if bad != nil {
		return bad
	}
	lim := xlsxLimits{maxSheets: AsInt(maxSheets), maxRows: AsInt(maxRows), maxCells: AsInt(maxCells), maxColumns: AsInt(maxColumns)}
	if lim.maxSheets <= 0 || lim.maxRows <= 0 || lim.maxCells <= 0 || lim.maxColumns <= 0 {
		return Err[any, any](ErrInvalidInput("Xlsx: every limit must be positive, got maxSheets " + strconv.Itoa(lim.maxSheets) +
			", maxRows " + strconv.Itoa(lim.maxRows) + ", maxCells " + strconv.Itoa(lim.maxCells) + ", maxColumns " + strconv.Itoa(lim.maxColumns)))
	}
	wb, err := xlsxReadAll(asBytesString(data), zl, lim)
	if err != nil {
		return Err[any, any](err)
	}
	return Ok[any, any](wb)
}

// Office_xlsxSheetNames is the primitive behind `Xlsx.sheetNames`.
func Office_xlsxSheetNames(data any) any {
	names, err := xlsxSheetNamesOf(asBytesString(data))
	if err != nil {
		return Err[any, any](err)
	}
	return Ok[any, any](names)
}
