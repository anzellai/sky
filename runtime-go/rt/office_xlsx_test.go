package rt

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// ── helpers ────────────────────────────────────────────────────────────────

// officeZip builds an archive from part name -> content, in the order given.
func officeZip(t *testing.T, parts ...[2]string) string {
	t.Helper()
	return zipRaw(t, func(w *zip.Writer) {
		for _, p := range parts {
			zipAdd(t, w, &zip.FileHeader{Name: p[0], Method: zip.Deflate}, []byte(p[1]))
		}
	})
}

func officeCodeOf(t *testing.T, v any) (string, string) {
	t.Helper()
	r := v.(SkyResult[any, any])
	if r.Tag == 0 {
		return "", ""
	}
	df, ok := DomainOf(r.ErrValue)
	if !ok {
		t.Fatalf("not a domain error: %+v", r.ErrValue)
	}
	return df.Domain, df.Code
}

func xlsxReadWith(data string, lim xlsxLimits) any {
	z := zipDefaultLimits
	return Office_xlsxRead(z.maxEntries, int(z.maxEntryBytes), int(z.maxTotalBytes), int(z.maxRatio),
		lim.maxSheets, lim.maxRows, lim.maxCells, lim.maxColumns, data)
}

var xlsxDefaultReadLimits = xlsxLimits{maxSheets: 100, maxRows: 200_000, maxCells: 2_000_000, maxColumns: 16_384}

func xlsxReadOK(t *testing.T, data string) map[string]any {
	t.Helper()
	r := xlsxReadWith(data, xlsxDefaultReadLimits).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("read failed: %v", officeMessage(r.ErrValue))
	}
	return r.OkValue.(map[string]any)
}

// wcell is a WCell record as Sky passes it.
func wcell(kind int) map[string]any {
	return map[string]any{
		"kind": kind, "text": "", "number": 0.0, "flag": false, "parts": []any{},
		"bold": false, "italic": false, "size": 0, "fill": -1, "format": "", "wrap": false,
	}
}

func wText(s string) map[string]any    { c := wcell(xlsxKindText); c["text"] = s; return c }
func wNum(f float64) map[string]any    { c := wcell(xlsxKindNumber); c["number"] = f; return c }
func wBool(b bool) map[string]any      { c := wcell(xlsxKindBool); c["flag"] = b; return c }
func wFormula(s string) map[string]any { c := wcell(xlsxKindFormula); c["text"] = s; return c }
func wFailure(s string) map[string]any { c := wcell(xlsxKindFailure); c["text"] = s; return c }
func wDate(p ...int) map[string]any {
	c := wcell(xlsxKindDate)
	parts := make([]any, len(p))
	for i, x := range p {
		parts[i] = x
	}
	c["parts"] = parts
	return c
}

func rng(r1, c1, r2, c2 int) map[string]any {
	return map[string]any{"firstRow": r1, "firstColumn": c1, "lastRow": r2, "lastColumn": c2}
}

func wsheet(name string, rows ...[]any) map[string]any {
	rs := make([]any, len(rows))
	for i, r := range rows {
		rs[i] = r
	}
	return map[string]any{
		"name": name, "visibility": 0, "rows": rs, "columnWidths": []any{},
		"frozenRows": 0, "frozenColumns": 0, "merged": []any{}, "validations": []any{},
	}
}

func xlsxWriteOK(t *testing.T, sheets ...map[string]any) string {
	t.Helper()
	in := make([]any, len(sheets))
	for i, s := range sheets {
		in[i] = s
	}
	r := Office_xlsxWrite(in).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("write failed: %v", officeMessage(r.ErrValue))
	}
	return r.OkValue.(string)
}

func xlsxWriteCode(t *testing.T, sheets ...map[string]any) string {
	t.Helper()
	in := make([]any, len(sheets))
	for i, s := range sheets {
		in[i] = s
	}
	_, code := officeCodeOf(t, Office_xlsxWrite(in))
	return code
}

func raw(tag int, f ...any) SkyADT { return xlsxRaw(tag, f...) }

func sheetRows(wb map[string]any, i int) []any {
	return wb["sheets"].([]any)[i].(map[string]any)["rows"].([]any)
}

// ── the second reader ──────────────────────────────────────────────────────
//
// xlsxValidate checks a written workbook with encoding/xml struct decoding,
// independent of the streaming reader under test, against the rules Excel
// enforces when it opens a file (it "repairs" or refuses a file that breaks
// one): every part has a content type, every relationship target exists,
// the worksheet children come in schema order, the style and shared string
// counts match, every style index is defined, the active tab is visible and
// exactly one sheet is selected.

type ooTypes struct {
	Defaults []struct {
		Extension, ContentType string `xml:",attr"`
	} `xml:"Default"`
	Overrides []struct {
		PartName, ContentType string `xml:",attr"`
	} `xml:"Override"`
}

type ooRels struct {
	Rels []struct {
		ID         string `xml:"Id,attr"`
		Type       string `xml:"Type,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	} `xml:"Relationship"`
}

type ooWorkbook struct {
	Views []struct {
		ActiveTab int `xml:"activeTab,attr"`
	} `xml:"bookViews>workbookView"`
	Sheets []struct {
		Name  string `xml:"name,attr"`
		ID    int    `xml:"sheetId,attr"`
		State string `xml:"state,attr"`
		RID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
	Calc struct {
		Full string `xml:"fullCalcOnLoad,attr"`
	} `xml:"calcPr"`
}

type ooStyles struct {
	NumFmts struct {
		Count int `xml:"count,attr"`
		Fmts  []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmt"`
	} `xml:"numFmts"`
	Fonts struct {
		Count int        `xml:"count,attr"`
		Fonts []struct{} `xml:"font"`
	} `xml:"fonts"`
	Fills struct {
		Count int        `xml:"count,attr"`
		Fills []struct{} `xml:"fill"`
	} `xml:"fills"`
	Xfs struct {
		Count int `xml:"count,attr"`
		Xfs   []struct {
			NumFmt int `xml:"numFmtId,attr"`
			Font   int `xml:"fontId,attr"`
			Fill   int `xml:"fillId,attr"`
		} `xml:"xf"`
	} `xml:"cellXfs"`
}

type ooSst struct {
	Count  int `xml:"count,attr"`
	Unique int `xml:"uniqueCount,attr"`
	Si     []struct {
		T string `xml:"t"`
	} `xml:"si"`
}

type ooAnyChildren struct {
	Children []struct {
		XMLName xml.Name
		Attrs   []xml.Attr `xml:",any,attr"`
	} `xml:",any"`
}

func ooParts(t *testing.T, data string) map[string][]byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader([]byte(data)), int64(len(data)))
	if err != nil {
		t.Fatalf("second reader: not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func ooUnmarshal(t *testing.T, parts map[string][]byte, name string, v any) {
	t.Helper()
	b, ok := parts[name]
	if !ok {
		t.Fatalf("second reader: part %s missing", name)
	}
	if err := xml.Unmarshal(b, v); err != nil {
		t.Fatalf("second reader: part %s: %v", name, err)
	}
}

// ooCheckPackage checks content types and relationships for every part.
func ooCheckPackage(t *testing.T, parts map[string][]byte) {
	t.Helper()
	var ct ooTypes
	ooUnmarshal(t, parts, "[Content_Types].xml", &ct)
	defaults := map[string]bool{}
	for _, d := range ct.Defaults {
		defaults[d.Extension] = true
	}
	overrides := map[string]bool{}
	for _, o := range ct.Overrides {
		overrides[strings.TrimPrefix(o.PartName, "/")] = true
		if _, ok := parts[strings.TrimPrefix(o.PartName, "/")]; !ok {
			t.Errorf("second reader: content type for missing part %s", o.PartName)
		}
	}
	for name := range parts {
		if name == "[Content_Types].xml" {
			continue
		}
		if !overrides[name] && !defaults[strings.TrimPrefix(path.Ext(name), ".")] {
			t.Errorf("second reader: part %s has no content type", name)
		}
		if strings.HasSuffix(name, ".rels") {
			var rels ooRels
			ooUnmarshal(t, parts, name, &rels)
			src := strings.Replace(strings.TrimSuffix(name, ".rels"), "_rels/", "", 1)
			ids := map[string]bool{}
			for _, r := range rels.Rels {
				if ids[r.ID] {
					t.Errorf("second reader: %s repeats the id %s", name, r.ID)
				}
				ids[r.ID] = true
				if r.TargetMode == "External" {
					continue
				}
				target := officeResolve(src, r.Target)
				if src == "" || name == "_rels/.rels" {
					target = path.Clean(r.Target)
				}
				if _, ok := parts[target]; !ok {
					t.Errorf("second reader: %s points at missing part %s", name, target)
				}
			}
		}
	}
}

// worksheetOrder is the schema order of CT_Worksheet's children.
var worksheetOrder = []string{"sheetPr", "dimension", "sheetViews", "sheetFormatPr", "cols", "sheetData", "sheetCalcPr",
	"sheetProtection", "protectedRanges", "scenarios", "autoFilter", "sortState", "dataConsolidate", "customSheetViews",
	"mergeCells", "phoneticPr", "conditionalFormatting", "dataValidations", "hyperlinks", "printOptions", "pageMargins"}

func xlsxValidate(t *testing.T, data string) {
	t.Helper()
	parts := ooParts(t, data)
	ooCheckPackage(t, parts)
	var wb ooWorkbook
	ooUnmarshal(t, parts, "xl/workbook.xml", &wb)
	if wb.Calc.Full != "1" {
		t.Errorf("second reader: calcPr fullCalcOnLoad is %q", wb.Calc.Full)
	}
	if len(wb.Views) != 1 || wb.Views[0].ActiveTab >= len(wb.Sheets) || wb.Sheets[wb.Views[0].ActiveTab].State != "" {
		t.Errorf("second reader: the active tab is not a visible sheet")
	}
	var st ooStyles
	ooUnmarshal(t, parts, "xl/styles.xml", &st)
	if st.Fonts.Count != len(st.Fonts.Fonts) || st.Fills.Count != len(st.Fills.Fills) || st.Xfs.Count != len(st.Xfs.Xfs) ||
		st.NumFmts.Count != len(st.NumFmts.Fmts) || len(st.Fills.Fills) < 2 {
		t.Errorf("second reader: styles.xml counts do not match: %+v", st)
	}
	fmts := map[int]bool{}
	for _, f := range st.NumFmts.Fmts {
		fmts[f.ID] = true
	}
	for _, x := range st.Xfs.Xfs {
		if x.Font >= len(st.Fonts.Fonts) || x.Fill >= len(st.Fills.Fills) || (x.NumFmt >= 164 && !fmts[x.NumFmt]) {
			t.Errorf("second reader: an xf refers to a missing font, fill or format: %+v", x)
		}
	}
	var sst ooSst
	ooUnmarshal(t, parts, "xl/sharedStrings.xml", &sst)
	if sst.Unique != len(sst.Si) || sst.Count < sst.Unique {
		t.Errorf("second reader: sharedStrings counts %d/%d for %d strings", sst.Count, sst.Unique, len(sst.Si))
	}
	var rels ooRels
	ooUnmarshal(t, parts, "xl/_rels/workbook.xml.rels", &rels)
	targets := map[string]string{}
	for _, r := range rels.Rels {
		targets[r.ID] = officeResolve("xl/workbook.xml", r.Target)
	}
	selected := 0
	names := map[string]bool{}
	for _, s := range wb.Sheets {
		if names[strings.ToLower(s.Name)] {
			t.Errorf("second reader: repeated sheet name %q", s.Name)
		}
		names[strings.ToLower(s.Name)] = true
		part := targets[s.RID]
		var ws ooAnyChildren
		ooUnmarshal(t, parts, part, &ws)
		last := -1
		for _, c := range ws.Children {
			at := -1
			for i, n := range worksheetOrder {
				if n == c.XMLName.Local {
					at = i
				}
			}
			if at < 0 || at <= last {
				t.Errorf("second reader: %s: <%s> is out of schema order", part, c.XMLName.Local)
			}
			last = at
		}
		if strings.Contains(string(parts[part]), `tabSelected="1"`) {
			selected++
		}
		// Every cell's style is defined and every shared string index exists.
		d := xml.NewDecoder(bytes.NewReader(parts[part]))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			se, ok := tok.(xml.StartElement)
			if !ok || se.Name.Local != "c" {
				continue
			}
			var c struct {
				R string `xml:"r,attr"`
				S int    `xml:"s,attr"`
				T string `xml:"t,attr"`
				V string `xml:"v"`
			}
			if err := d.DecodeElement(&c, &se); err != nil {
				t.Fatal(err)
			}
			if c.S >= len(st.Xfs.Xfs) {
				t.Errorf("second reader: %s %s has style %d of %d", part, c.R, c.S, len(st.Xfs.Xfs))
			}
			if c.T == "s" {
				if i, err := strconv.Atoi(c.V); err != nil || i >= len(sst.Si) {
					t.Errorf("second reader: %s %s names shared string %q", part, c.R, c.V)
				}
			}
		}
	}
	if selected != 1 {
		t.Errorf("second reader: %d sheets are selected, want 1", selected)
	}
}

// ── round trips ────────────────────────────────────────────────────────────

func TestXlsxRoundTripEveryCellKind(t *testing.T) {
	bold := wText("Bold header")
	bold["bold"] = true
	money := wNum(1250.5)
	money["format"] = "#,##0.00"
	filled := wText("filled")
	filled["fill"] = 0xFFEE00
	filled["italic"] = true
	filled["size"] = 14
	filled["wrap"] = true
	pct := wNum(0.125)
	pct["format"] = "0.0%"
	stamp := wDate(2026, 10, 29, 13, 45, 7, 123)
	stamp["format"] = "yyyy-mm-dd hh:mm:ss"
	data := xlsxWriteOK(t, wsheet("Data",
		[]any{bold, wText(""), wText("  spaced  "), wText("ünïcødé ✓ 漢字 😀")},
		[]any{},
		[]any{wNum(0), wNum(-1.5), wNum(1e300), wNum(123456789012), money, pct},
		[]any{wBool(true), wBool(false), wcell(xlsxKindEmpty), wFailure("#DIV/0!"), wFailure("#N/A")},
		[]any{wDate(2026, 10, 29, 0, 0, 0, 0), stamp, wDate(1900, 1, 1, 0, 0, 0, 0), wDate(1900, 2, 28, 0, 0, 0, 0),
			wDate(1900, 2, 29, 0, 0, 0, 0), wDate(1900, 3, 1, 0, 0, 0, 0), wDate(1899, 12, 30, 9, 30, 0, 0), wDate(9999, 12, 31, 23, 59, 59, 999)},
		[]any{wFormula("SUM(C3:D3)"), wFormula("=A1&\"x\""), filled},
		[]any{wText("tab\tnew\nline\r"), wText("ctl\x01\x1fend"), wText("lit _x0041_ kept"), wText("bad \xff byte")},
		[]any{wcell(xlsxKindEmpty), wcell(xlsxKindEmpty), wText("after gap")},
	))
	xlsxValidate(t, data)
	wb := xlsxReadOK(t, data)
	got := sheetRows(wb, 0)
	want := []any{
		[]any{raw(1, "Bold header"), raw(1, ""), raw(1, "  spaced  "), raw(1, "ünïcødé ✓ 漢字 😀")},
		[]any{},
		[]any{raw(2, 0.0), raw(2, -1.5), raw(2, 1e300), raw(2, 123456789012.0), raw(2, 1250.5), raw(2, 0.125)},
		[]any{raw(3, true), raw(3, false), raw(0), raw(6, "#DIV/0!"), raw(6, "#N/A")},
		[]any{raw(4, 2026, 10, 29, 0, 0, 0, 0), raw(4, 2026, 10, 29, 13, 45, 7, 123), raw(4, 1900, 1, 1, 0, 0, 0, 0), raw(4, 1900, 2, 28, 0, 0, 0, 0),
			raw(4, 1900, 2, 29, 0, 0, 0, 0), raw(4, 1900, 3, 1, 0, 0, 0, 0), raw(4, 1899, 12, 30, 9, 30, 0, 0), raw(4, 9999, 12, 31, 23, 59, 59, 999)},
		[]any{raw(5, "SUM(C3:D3)", raw(0)), raw(5, "A1&\"x\"", raw(0)), raw(1, "filled")},
		[]any{raw(1, "tab\tnew\nline\r"), raw(1, "ctl\x01\x1fend"), raw(1, "lit _x0041_ kept"), raw(1, "bad \uFFFD byte")},
		[]any{raw(0), raw(0), raw(1, "after gap")},
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("row %d:\n got %#v\nwant %#v", i, got[i], want[i])
		}
	}
}

func TestXlsxRoundTripSheetFeatures(t *testing.T) {
	data := wsheet("Data", []any{wText("Status"), wText("Owner")}, []any{wText("open"), wText("ana")})
	data["columnWidths"] = []any{map[string]any{"column": 0, "width": 12.5}, map[string]any{"column": 3, "width": 40.0}}
	data["frozenRows"] = 1
	data["merged"] = []any{rng(3, 0, 4, 2), rng(6, 1, 6, 3)}
	data["validations"] = []any{
		map[string]any{"kind": 0, "range": rng(1, 0, 99, 0), "items": []any{"open", "closed", "on hold"}, "source": ""},
		map[string]any{"kind": 1, "range": rng(1, 1, 99, 1), "items": []any{}, "source": "Lists!$A$1:$A$3"},
	}
	cols := wsheet("Cols", []any{wNum(1)})
	cols["frozenColumns"] = 2
	both := wsheet("Both", []any{wNum(1)})
	both["frozenRows"], both["frozenColumns"] = 3, 1
	hidden := wsheet("Lists", []any{wText("ana")}, []any{wText("bo")}, []any{wText("cy")})
	hidden["visibility"] = 1
	very := wsheet("Secret", []any{wText("x")})
	very["visibility"] = 2
	out := xlsxWriteOK(t, hidden, data, cols, both, very)
	xlsxValidate(t, out)
	wb := xlsxReadOK(t, out)
	sheets := wb["sheets"].([]any)
	type pick struct {
		name        string
		vis, fr, fc int
	}
	wantPick := []pick{{"Lists", 1, 0, 0}, {"Data", 0, 1, 0}, {"Cols", 0, 0, 2}, {"Both", 0, 3, 1}, {"Secret", 2, 0, 0}}
	for i, w := range wantPick {
		s := sheets[i].(map[string]any)
		if s["name"] != w.name || s["visibility"] != w.vis || s["frozenRows"] != w.fr || s["frozenColumns"] != w.fc {
			t.Errorf("sheet %d: got %v %v %v %v, want %+v", i, s["name"], s["visibility"], s["frozenRows"], s["frozenColumns"], w)
		}
	}
	d := sheets[1].(map[string]any)
	if !reflect.DeepEqual(d["merged"], []any{rng(3, 0, 4, 2), rng(6, 1, 6, 3)}) {
		t.Errorf("merged: %#v", d["merged"])
	}
	if !reflect.DeepEqual(d["columnWidths"], []any{map[string]any{"column": 0, "width": 12.5}, map[string]any{"column": 3, "width": 40.0}}) {
		t.Errorf("widths: %#v", d["columnWidths"])
	}
	wantV := []any{
		map[string]any{"kind": 0, "range": rng(1, 0, 99, 0), "items": []any{"open", "closed", "on hold"}, "source": ""},
		map[string]any{"kind": 1, "range": rng(1, 1, 99, 1), "items": []any{}, "source": "Lists!$A$1:$A$3"},
	}
	if !reflect.DeepEqual(d["validations"], wantV) {
		t.Errorf("validations:\n got %#v\nwant %#v", d["validations"], wantV)
	}
	// The first visible sheet is the active one, though a hidden sheet is first.
	if !strings.Contains(string(ooParts(t, out)["xl/workbook.xml"]), `activeTab="1"`) {
		t.Errorf("activeTab is not the first visible sheet")
	}
}

func TestXlsxWriteDeterministicAndStylesDeduplicated(t *testing.T) {
	var rows [][]any
	for i := 0; i < 500; i++ {
		c := wText("row " + strconv.Itoa(i%7))
		c["bold"] = true
		rows = append(rows, []any{c, wNum(float64(i))})
	}
	a := xlsxWriteOK(t, wsheet("S", rows...))
	b := xlsxWriteOK(t, wsheet("S", rows...))
	if a != b {
		t.Fatal("two writes of the same sheets differ")
	}
	var st ooStyles
	ooUnmarshal(t, ooParts(t, a), "xl/styles.xml", &st)
	if len(st.Xfs.Xfs) != 2 || len(st.Fonts.Fonts) != 2 {
		t.Errorf("styles not deduplicated: %d xfs, %d fonts", len(st.Xfs.Xfs), len(st.Fonts.Fonts))
	}
	var sst ooSst
	ooUnmarshal(t, ooParts(t, a), "xl/sharedStrings.xml", &sst)
	if len(sst.Si) != 7 {
		t.Errorf("shared strings not deduplicated: %d", len(sst.Si))
	}
}

// ── write refusals ────────────────────────────────────────────────────────

func TestXlsxWriteRefusals(t *testing.T) {
	long := strings.Repeat("x", 32)
	hiddenOnly := wsheet("A", []any{wNum(1)})
	hiddenOnly["visibility"] = 1
	cases := []struct {
		name   string
		sheets []map[string]any
		code   string
	}{
		{"empty name", []map[string]any{wsheet("")}, officeCodeSheetName},
		{"32 characters", []map[string]any{wsheet(long)}, officeCodeSheetName},
		{"a slash", []map[string]any{wsheet("a/b")}, officeCodeSheetName},
		{"a bracket", []map[string]any{wsheet("[a]")}, officeCodeSheetName},
		{"an apostrophe first", []map[string]any{wsheet("'a")}, officeCodeSheetName},
		{"History", []map[string]any{wsheet("history")}, officeCodeSheetName},
		{"a duplicate ignoring case", []map[string]any{wsheet("Data"), wsheet("DATA")}, officeCodeSheetName},
		{"no sheet", nil, officeCodeNoVisibleSheet},
		{"only hidden", []map[string]any{hiddenOnly}, officeCodeNoVisibleSheet},
		{"empty formula", []map[string]any{wsheet("A", []any{wFormula("=")})}, officeCodeFormula},
		{"unknown error value", []map[string]any{wsheet("A", []any{wFailure("#OOPS")})}, officeCodeFailureValue},
		{"bad date", []map[string]any{wsheet("A", []any{wDate(2026, 2, 30, 0, 0, 0, 0)})}, officeCodeDateRange},
		{"date before 1900", []map[string]any{wsheet("A", []any{wDate(1899, 12, 31, 0, 0, 0, 0)})}, officeCodeDateRange},
		{"text too long", []map[string]any{wsheet("A", []any{wText(strings.Repeat("é", 32_768))})}, officeCodeTextTooLong},
	}
	for _, c := range cases {
		if got := xlsxWriteCode(t, c.sheets...); got != c.code {
			t.Errorf("%s: code %q, want %q", c.name, got, c.code)
		}
	}
	if got := xlsxWriteCode(t, wsheet(strings.Repeat("é", 31))); got != "" {
		t.Errorf("31 characters (62 bytes) refused: %q", got)
	}

	withSheet := func(f func(s map[string]any)) string {
		s := wsheet("A", []any{wNum(1)})
		f(s)
		return xlsxWriteCode(t, s)
	}
	checks := []struct {
		name string
		f    func(s map[string]any)
		code string
	}{
		{"reversed merge", func(s map[string]any) { s["merged"] = []any{rng(2, 0, 1, 0)} }, officeCodeRange},
		{"one-cell merge", func(s map[string]any) { s["merged"] = []any{rng(1, 1, 1, 1)} }, officeCodeRange},
		{"overlapping merges", func(s map[string]any) { s["merged"] = []any{rng(0, 0, 2, 2), rng(2, 2, 3, 3)} }, officeCodeRange},
		{"merge off the sheet", func(s map[string]any) { s["merged"] = []any{rng(0, 0, 0, 16_384)} }, officeCodeRange},
		{"negative frozen", func(s map[string]any) { s["frozenRows"] = -1 }, officeCodeRange},
		{"duplicate width", func(s map[string]any) {
			s["columnWidths"] = []any{map[string]any{"column": 1, "width": 9.0}, map[string]any{"column": 1, "width": 10.0}}
		}, officeCodeColumnWidth},
		{"zero width", func(s map[string]any) { s["columnWidths"] = []any{map[string]any{"column": 1, "width": 0.0}} }, officeCodeColumnWidth},
		{"inline list over 255", func(s map[string]any) {
			var items []any
			for i := 0; i < 60; i++ {
				items = append(items, "item"+strconv.Itoa(i))
			}
			s["validations"] = []any{map[string]any{"kind": 0, "range": rng(0, 0, 9, 0), "items": items, "source": ""}}
		}, officeCodeValidationTooLong},
		{"item with a comma", func(s map[string]any) {
			s["validations"] = []any{map[string]any{"kind": 0, "range": rng(0, 0, 9, 0), "items": []any{"a,b"}, "source": ""}}
		}, officeCodeValidationItem},
		{"empty list", func(s map[string]any) {
			s["validations"] = []any{map[string]any{"kind": 0, "range": rng(0, 0, 9, 0), "items": []any{}, "source": ""}}
		}, officeCodeValidationItem},
		{"too many columns", func(s map[string]any) {
			row := make([]any, 16_385)
			for i := range row {
				row[i] = wcell(xlsxKindEmpty)
			}
			s["rows"] = []any{row}
		}, officeCodeTooManyColumns},
	}
	for _, c := range checks {
		if got := withSheet(c.f); got != c.code {
			t.Errorf("%s: code %q, want %q", c.name, got, c.code)
		}
	}
	// The validation message names ListFromRange as the fix.
	s := wsheet("A")
	var items []any
	for i := 0; i < 60; i++ {
		items = append(items, "item"+strconv.Itoa(i))
	}
	s["validations"] = []any{map[string]any{"kind": 0, "range": rng(0, 0, 9, 0), "items": items, "source": ""}}
	r := Office_xlsxWrite([]any{s}).(SkyResult[any, any])
	if !strings.Contains(officeMessage(r.ErrValue), "ListFromRange") {
		t.Errorf("validation_too_long does not name ListFromRange: %q", officeMessage(r.ErrValue))
	}
}

func TestXlsxWriteTooManyRows(t *testing.T) {
	rows := make([]any, xlsxMaxRows+1)
	for i := range rows {
		rows[i] = []any{}
	}
	s := wsheet("A")
	s["rows"] = rows
	if got := xlsxWriteCode(t, s); got != officeCodeTooManyRows {
		t.Errorf("code %q", got)
	}
}

// ── reading files other programs write ─────────────────────────────────────
//
// These fixtures are built part by part in the shape each program writes
// (namespaces, mc:Ignorable, shared and array formulas, rich text, phonetic
// runs, inline strings, rows and cells without an index, the date styles
// each one uses), since no spreadsheet program runs in this test.

const xlsxCT = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`

func xlsxFixture(t *testing.T, workbook, rels, styles, shared string, sheets ...string) string {
	t.Helper()
	parts := [][2]string{{"[Content_Types].xml", xlsxCT}, {"_rels/.rels", xlsxRootRels}, {"xl/workbook.xml", workbook}, {"xl/_rels/workbook.xml.rels", rels}}
	if styles != "" {
		parts = append(parts, [2]string{"xl/styles.xml", styles})
	}
	if shared != "" {
		parts = append(parts, [2]string{"xl/sharedStrings.xml", shared})
	}
	for i, s := range sheets {
		parts = append(parts, [2]string{"xl/worksheets/sheet" + strconv.Itoa(i+1) + ".xml", s})
	}
	return officeZip(t, parts...)
}

const excelWorkbook = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x15 xr xr6 xr10 xr2" xmlns:x15="http://schemas.microsoft.com/office/spreadsheetml/2010/11/main"><fileVersion appName="xl" lastEdited="7" lowestEdited="7" rupBuild="27425"/><workbookPr defaultThemeVersion="202300"/><mc:AlternateContent xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><mc:Choice Requires="x15"><x15ac:absPath url="C:\Users\" xmlns:x15ac="http://schemas.microsoft.com/office/spreadsheetml/2010/11/ac"/></mc:Choice></mc:AlternateContent><bookViews><workbookView xWindow="-120" yWindow="-120" windowWidth="29040" windowHeight="15720" activeTab="1"/></bookViews><sheets><sheet name="Ledger" sheetId="1" r:id="rId1"/><sheet name="Notes" sheetId="2" state="hidden" r:id="rId2"/></sheets><definedNames><definedName name="_xlnm.Print_Area" localSheetId="0">Ledger!$A$1:$D$9</definedName></definedNames><calcPr calcId="191029"/></workbook>`

const excelRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/sheet1.xml"/><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/externalLink" Target="externalLinks/externalLink1.xml"/></Relationships>`

const excelStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x14ac x16r2 xr" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac"><numFmts count="3"><numFmt numFmtId="164" formatCode="&quot;Date: &quot;0.00"/><numFmt numFmtId="165" formatCode="dd/mm/yyyy;@"/><numFmt numFmtId="166" formatCode="[Red]#,##0.00;[Blue]\-#,##0.00"/></numFmts><fonts count="1" x14ac:knownFonts="1"><font><sz val="11"/><color theme="1"/><name val="Aptos Narrow"/><family val="2"/><scheme val="minor"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="6"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="14" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="166" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="20" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`

const excelShared = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="5" uniqueCount="4"><si><t>Account</t></si><si><r><rPr><b/><sz val="11"/></rPr><t xml:space="preserve">Rich </t></r><r><rPr><i/></rPr><t>text</t></r></si><si><t>東京</t><rPh sb="0" eb="2"><t>トウキョウ</t></rPh><phoneticPr fontId="1"/></si><si><t>a_x000D_b</t></si></sst>`

const excelSheet1 = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x14ac xr xr2 xr3" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac" xr:uid="{00000000-0001-0000-0000-000000000000}" xmlns:xr="http://schemas.microsoft.com/office/spreadsheetml/2014/revision"><dimension ref="A1:D6"/><sheetViews><sheetView tabSelected="1" workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/><selection pane="bottomLeft" activeCell="B3" sqref="B3"/></sheetView></sheetViews><sheetFormatPr defaultRowHeight="15" x14ac:dyDescent="0.25"/><cols><col min="1" max="2" width="14.7109375" customWidth="1"/><col min="3" max="3" width="9" style="1"/></cols><sheetData><row r="1" spans="1:4" x14ac:dyDescent="0.25"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>3</v></c></row><row r="2" spans="1:4"><c r="A2"><v>10</v></c><c r="B2"><f>A2*2</f><v>20</v></c><c r="C2" s="1"><v>45594</v></c><c r="D2" s="2"><v>3.5</v></c></row><row r="3"><c r="A3"><v>11</v></c><c r="B3"><f t="shared" ref="B3:B5" si="0">A3*2+$A$2+SUM(A$3:A3)</f><v>34</v></c><c r="C3" s="3"><v>45657.5</v></c><c r="D3" s="4"><v>-2</v></c></row><row r="4"><c r="A4"><v>12</v></c><c r="B4"><f t="shared" si="0"/><v>59</v></c><c r="C4" s="5"><v>0.75</v></c><c r="D4" t="e"><f>1/0</f><v>#DIV/0!</v></c></row><row r="6"><c r="B6" t="str"><f>"Total "&amp;TEXT(SUM(B2:B4),"0")</f><v>Total 113</v></c><c r="C6" t="b"><v>1</v></c><c r="D6" t="inlineStr"><is><t>inline</t></is></c></row></sheetData><mergeCells count="1"><mergeCell ref="C1:D1"/></mergeCells><dataValidations count="2"><dataValidation type="list" allowBlank="1" showInputMessage="1" showErrorMessage="1" sqref="A8:A9 C8"><formula1>"yes,no,""maybe"""</formula1></dataValidation><dataValidation type="whole" sqref="B8"><formula1>1</formula1><formula2>9</formula2></dataValidation></dataValidations><pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/></worksheet>`

const excelSheet2 = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c><v>1</v></c><c><v>2</v></c></row><row><c t="s"><v>0</v></c></row></sheetData></worksheet>`

func TestXlsxReadExcelShapedFile(t *testing.T) {
	data := xlsxFixture(t, excelWorkbook, excelRels, excelStyles, excelShared, excelSheet1, excelSheet2)
	wb := xlsxReadOK(t, data)
	if wb["date1904"] != false {
		t.Errorf("date1904")
	}
	sheets := wb["sheets"].([]any)
	s0 := sheets[0].(map[string]any)
	want := []any{
		[]any{raw(1, "Account"), raw(1, "Rich text"), raw(1, "東京"), raw(1, "a\rb")},
		[]any{raw(2, 10.0), raw(5, "A2*2", raw(2, 20.0)), raw(4, 2024, 10, 29, 0, 0, 0, 0), raw(2, 3.5)},
		[]any{raw(2, 11.0), raw(5, "A3*2+$A$2+SUM(A$3:A3)", raw(2, 34.0)), raw(4, 2024, 12, 31, 12, 0, 0, 0), raw(2, -2.0)},
		[]any{raw(2, 12.0), raw(5, "A4*2+$A$2+SUM(A$3:A4)", raw(2, 59.0)), raw(4, 1899, 12, 30, 18, 0, 0, 0), raw(5, "1/0", raw(6, "#DIV/0!"))},
		[]any{},
		[]any{raw(0), raw(5, `"Total "&TEXT(SUM(B2:B4),"0")`, raw(1, "Total 113")), raw(3, true), raw(1, "inline")},
	}
	got := s0["rows"].([]any)
	for i := range want {
		if i >= len(got) || !reflect.DeepEqual(got[i], want[i]) {
			var g any
			if i < len(got) {
				g = got[i]
			}
			t.Errorf("row %d:\n got %#v\nwant %#v", i, g, want[i])
		}
	}
	if s0["frozenRows"] != 1 || s0["frozenColumns"] != 0 {
		t.Errorf("frozen: %v %v", s0["frozenRows"], s0["frozenColumns"])
	}
	if !reflect.DeepEqual(s0["columnWidths"], []any{map[string]any{"column": 0, "width": 14.7109375}, map[string]any{"column": 1, "width": 14.7109375}}) {
		t.Errorf("widths: %#v", s0["columnWidths"])
	}
	if !reflect.DeepEqual(s0["merged"], []any{rng(0, 2, 0, 3)}) {
		t.Errorf("merged: %#v", s0["merged"])
	}
	items := []any{"yes", "no", `"maybe"`}
	wantV := []any{
		map[string]any{"kind": 0, "range": rng(7, 0, 8, 0), "items": items, "source": ""},
		map[string]any{"kind": 0, "range": rng(2-2+7, 2, 7, 2), "items": items, "source": ""},
	}
	if !reflect.DeepEqual(s0["validations"], wantV) {
		t.Errorf("validations:\n got %#v\nwant %#v", s0["validations"], wantV)
	}
	s1 := sheets[1].(map[string]any)
	if s1["visibility"] != 1 || !reflect.DeepEqual(s1["rows"], []any{[]any{raw(2, 1.0), raw(2, 2.0)}, []any{raw(1, "Account")}}) {
		t.Errorf("sheet 2: %v %#v", s1["visibility"], s1["rows"])
	}
	// sheetNames reads only the workbook.
	names := Office_xlsxSheetNames(data).(SkyResult[any, any])
	if names.Tag != 0 || !reflect.DeepEqual(names.OkValue, []any{
		map[string]any{"name": "Ledger", "visibility": 0}, map[string]any{"name": "Notes", "visibility": 1},
	}) {
		t.Errorf("sheetNames: %#v", names.OkValue)
	}
}

const libreWorkbook = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><fileVersion appName="Calc"/><workbookPr backupFile="false" showObjects="all" date1904="true"/><workbookProtection/><bookViews><workbookView showHorizontalScroll="true" activeTab="0"/></bookViews><sheets><sheet name="Tabelle1" sheetId="1" state="visible" r:id="rId2"/></sheets><calcPr iterateCount="100" refMode="A1" iterate="false" iterateDelta="0.001"/></workbook>`

const libreRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`

const libreStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="2"><numFmt numFmtId="164" formatCode="General"/><numFmt numFmtId="165" formatCode="YYYY\-MM\-DD\ HH:MM"/></numFmts><cellXfs count="3"><xf numFmtId="164" fontId="0" fillId="0" borderId="0" applyFont="false" applyBorder="false" applyAlignment="false" applyProtection="false"/><xf numFmtId="165" fontId="0" fillId="0" borderId="0" applyFont="false" applyBorder="false" applyAlignment="false" applyProtection="false"/><xf numFmtId="164" fontId="4" fillId="0" borderId="0"/></cellXfs></styleSheet>`

const libreShared = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2"><si><t xml:space="preserve">Name </t></si><si><t xml:space="preserve">Zeit</t></si></sst>`

const libreSheet = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheetPr filterMode="false"><pageSetUpPr fitToPage="false"/></sheetPr><dimension ref="A1:B2"/><sheetViews><sheetView showFormulas="false" tabSelected="true" workbookViewId="0"><pane xSplit="1" ySplit="1" topLeftCell="B2" activePane="bottomRight" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultColWidth="11.53515625" defaultRowHeight="12.8"/><cols><col collapsed="false" customWidth="true" hidden="false" outlineLevel="0" max="1" min="1" style="0" width="20.5"/><col collapsed="false" customWidth="false" hidden="false" outlineLevel="0" max="1024" min="2" style="0" width="11.52"/></cols><sheetData><row r="1" customFormat="false" ht="12.8" hidden="false" customHeight="false" outlineLevel="0" collapsed="false"><c r="A1" s="0" t="s"><v>0</v></c><c r="B1" s="0" t="s"><v>1</v></c></row><row r="2" customFormat="false" ht="12.8" hidden="false" customHeight="false" outlineLevel="0" collapsed="false"><c r="A2" s="2" t="n"><v>42</v></c><c r="B2" s="1" t="n"><v>0.5</v></c></row></sheetData><printOptions headings="false" gridLines="false"/></worksheet>`

func TestXlsxReadLibreOfficeShapedFile1904(t *testing.T) {
	data := xlsxFixture(t, libreWorkbook, libreRels, libreStyles, libreShared, libreSheet)
	wb := xlsxReadOK(t, data)
	if wb["date1904"] != true {
		t.Fatal("date1904 not read")
	}
	s := wb["sheets"].([]any)[0].(map[string]any)
	want := []any{
		[]any{raw(1, "Name "), raw(1, "Zeit")},
		[]any{raw(2, 42.0), raw(4, 1904, 1, 1, 12, 0, 0, 0)},
	}
	if !reflect.DeepEqual(s["rows"], want) {
		t.Errorf("rows:\n got %#v\nwant %#v", s["rows"], want)
	}
	if s["frozenRows"] != 1 || s["frozenColumns"] != 1 || s["visibility"] != 0 {
		t.Errorf("frozen / visibility: %v %v %v", s["frozenRows"], s["frozenColumns"], s["visibility"])
	}
	if !reflect.DeepEqual(s["columnWidths"], []any{map[string]any{"column": 0, "width": 20.5}}) {
		t.Errorf("widths: %#v", s["columnWidths"])
	}
}

const googleWorkbook = `<?xml version="1.0" ?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><workbookPr/><sheets><sheet state="visible" name="Sheet1" sheetId="1" r:id="rId4"/><sheet state="hidden" name="Lookups" sheetId="2" r:id="rId5"/></sheets><definedNames/><calcPr/></workbook>`

const googleRels = `<?xml version="1.0" ?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`

const googleStyles = `<?xml version="1.0" ?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><numFmts count="1"><numFmt numFmtId="164" formatCode="m/d/yyyy"/></numFmts><cellXfs count="3"><xf borderId="0" fillId="0" fontId="0" numFmtId="0" xfId="0" applyAlignment="1" applyFont="1"/><xf borderId="0" fillId="0" fontId="0" numFmtId="164" xfId="0" applyAlignment="1" applyFont="1" applyNumberFormat="1"><alignment readingOrder="0"/></xf><xf borderId="0" fillId="0" fontId="1" numFmtId="0" xfId="0" applyAlignment="1" applyFont="1"><alignment readingOrder="0"/></xf></cellXfs></styleSheet>`

const googleShared = `<?xml version="1.0" ?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="3" uniqueCount="3"><si><t>Date</t></si><si><t>Amount</t></si><si><t>open</t></si></sst>`

const googleSheet1 = `<?xml version="1.0" ?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><sheetPr><outlinePr summaryBelow="0" summaryRight="0"/></sheetPr><sheetViews><sheetView workbookViewId="0"/></sheetViews><sheetFormatPr customHeight="1" defaultColWidth="12.63" defaultRowHeight="15.75"/><sheetData><row r="1"><c r="A1" s="2" t="s"><v>0</v></c><c r="B1" s="2" t="s"><v>1</v></c></row><row r="2"><c r="A2" s="1"><v>46324.0</v></c><c r="B2" s="2"><v>99.95</v></c><c r="C2" s="2" t="s"><v>2</v></c></row><row r="3"><c r="B3" s="2"><f>SUM(B2)</f><v>99.95</v></c></row></sheetData><dataValidations><dataValidation type="list" allowBlank="1" showErrorMessage="1" sqref="C2:C1000"><formula1>Lookups!$A$1:$A$3</formula1></dataValidation></dataValidations><drawing r:id="rId1"/></worksheet>`

const googleSheet2 = `<?xml version="1.0" ?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>2</v></c></row></sheetData></worksheet>`

func TestXlsxReadGoogleSheetsShapedFile(t *testing.T) {
	data := xlsxFixture(t, googleWorkbook, googleRels, googleStyles, googleShared, googleSheet1, googleSheet2)
	wb := xlsxReadOK(t, data)
	s := wb["sheets"].([]any)[0].(map[string]any)
	want := []any{
		[]any{raw(1, "Date"), raw(1, "Amount")},
		[]any{raw(4, 2026, 10, 29, 0, 0, 0, 0), raw(2, 99.95), raw(1, "open")},
		[]any{raw(0), raw(5, "SUM(B2)", raw(2, 99.95))},
	}
	if !reflect.DeepEqual(s["rows"], want) {
		t.Errorf("rows:\n got %#v\nwant %#v", s["rows"], want)
	}
	if !reflect.DeepEqual(s["validations"], []any{map[string]any{"kind": 1, "range": rng(1, 2, 999, 2), "items": []any{}, "source": "Lookups!$A$1:$A$3"}}) {
		t.Errorf("validations: %#v", s["validations"])
	}
	if wb["sheets"].([]any)[1].(map[string]any)["visibility"] != 1 {
		t.Error("hidden sheet")
	}
}

// ── pieces ────────────────────────────────────────────────────────────────

func TestXlsxDateFormatDetection(t *testing.T) {
	for code, want := range map[string]bool{
		"yyyy-mm-dd": true, "dd/mm/yyyy;@": true, "[h]:mm:ss": true, "hh:mm AM/PM": true, `YYYY\-MM\-DD\ HH:MM`: true,
		"General": false, "0.00": false, "#,##0.00": false, `"Date: "0.00`: false, "[Red]#,##0.00;[Blue]\\-#,##0.00": false,
		"0.00E+00": false, "@": false, `\d0`: false, "_-* #,##0.00_-": false,
	} {
		if got := xlsxDateFormat(code); got != want {
			t.Errorf("%q: got %v, want %v", code, got, want)
		}
	}
	for id, want := range map[int]bool{0: false, 13: false, 14: true, 22: true, 23: false, 45: true, 47: true, 48: false} {
		if got := xlsxBuiltinDate(id); got != want {
			t.Errorf("id %d: got %v", id, got)
		}
	}
}

func TestXlsxShiftFormula(t *testing.T) {
	cases := []struct {
		f      string
		dr, dc int
		want   string
	}{
		{"A1*2", 1, 0, "A2*2"},
		{"$A$1+A$1+$A1+A1", 2, 3, "$A$1+D$1+$A3+D3"},
		{`"A1"&B2`, 1, 1, `"A1"&C3`},
		{"SUM(A1:A3)", 1, 0, "SUM(A2:A4)"},
		{"'My Sheet'!B2+Data!C3", 1, 0, "'My Sheet'!B3+Data!C4"},
		{"LOG10(A1)", 1, 0, "LOG10(A2)"},
		{"A1-1", -1, 0, "#REF!-1"},
		{"XFD1", 0, 1, "#REF!"},
		{"1.5E+3*A1", 1, 0, "1.5E+3*A2"},
	}
	for _, c := range cases {
		if got := xlsxShiftFormula(c.f, c.dr, c.dc); got != c.want {
			t.Errorf("%q by (%d,%d): got %q, want %q", c.f, c.dr, c.dc, got, c.want)
		}
	}
}

func TestXlsxSerialParts(t *testing.T) {
	cases := map[float64][7]int{
		1:       {1900, 1, 1, 0, 0, 0, 0},
		59:      {1900, 2, 28, 0, 0, 0, 0},
		60:      {1900, 2, 29, 0, 0, 0, 0},
		61:      {1900, 3, 1, 0, 0, 0, 0},
		0.5:     {1899, 12, 30, 12, 0, 0, 0},
		46324.0: {2026, 10, 29, 0, 0, 0, 0},
	}
	for serial, want := range cases {
		if got := xlsxSerialParts(serial, false); got != want {
			t.Errorf("%v: got %v, want %v", serial, got, want)
		}
		if s, ok := xlsxDateSerial(want[:]); !ok || s != serial {
			t.Errorf("%v back: got %v %v", want, s, ok)
		}
	}
	if got := xlsxSerialParts(0, true); got != [7]int{1904, 1, 1, 0, 0, 0, 0} {
		t.Errorf("1904 serial 0: %v", got)
	}
}

// ── limits, bombs, hostile parts ───────────────────────────────────────────

func xlsxOneSheet(t *testing.T, sheet string) string {
	t.Helper()
	wbx := `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
	return xlsxFixture(t, wbx, rels, "", "", sheet)
}

func TestXlsxReadLimits(t *testing.T) {
	small := xlsxLimits{maxSheets: 1, maxRows: 10, maxCells: 20, maxColumns: 5}
	sheet := func(body string) string {
		return `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + body + `</sheetData></worksheet>`
	}
	cases := []struct {
		name string
		data string
		lim  xlsxLimits
		dom  string
		code string
	}{
		{"a row past maxRows", xlsxOneSheet(t, sheet(`<row r="11"><c r="A11"><v>1</v></c></row>`)), small, "office", officeCodeTooManyRows},
		{"a column past maxColumns", xlsxOneSheet(t, sheet(`<row r="1"><c r="F1"><v>1</v></c></row>`)), small, "office", officeCodeTooManyColumns},
		{"a column past XFD", xlsxOneSheet(t, sheet(`<row r="1"><c r="XFE1"><v>1</v></c></row>`)), xlsxDefaultReadLimits, "office", officeCodeTooManyColumns},
		{"gaps count as cells", xlsxOneSheet(t, sheet(`<row r="1"><c r="E1"><v>1</v></c></row><row r="2"><c r="E2"/></row><row r="3"><c r="E3"/></row><row r="4"><c r="E4"/></row><row r="5"><c r="E5"/></row>`)),
			xlsxLimits{maxSheets: 1, maxRows: 10, maxCells: 20, maxColumns: 5}, "office", officeCodeTooManyCells},
		{"a sparse far cell", xlsxOneSheet(t, sheet(`<row r="1"><c r="XFD1"><v>1</v></c></row>`)), xlsxLimits{maxSheets: 1, maxRows: 10, maxCells: 1000, maxColumns: 16384}, "office", officeCodeTooManyCells},
		{"merges count", xlsxOneSheet(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/><mergeCells>`+strings.Repeat(`<mergeCell ref="A1:B2"/>`, 21)+`</mergeCells></worksheet>`), small, "office", officeCodeTooManyCells},
		{"a DOCTYPE in a sheet", xlsxOneSheet(t, `<!DOCTYPE x [<!ENTITY a "aaaa">]><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`), xlsxDefaultReadLimits, "xml", xmlCodeDoctype},
		{"deep XML in a sheet", xlsxOneSheet(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`+strings.Repeat("<x>", 300)+strings.Repeat("</x>", 300)+`</worksheet>`), xlsxDefaultReadLimits, "xml", xmlCodeTooDeep},
		{"a bad shared string index", xlsxOneSheet(t, sheet(`<row r="1"><c r="A1" t="s"><v>9</v></c></row>`)), xlsxDefaultReadLimits, "office", officeCodeCorrupt},
		{"not a number", xlsxOneSheet(t, sheet(`<row r="1"><c r="A1"><v>abc</v></c></row>`)), xlsxDefaultReadLimits, "office", officeCodeCorrupt},
		{"not a zip", "plain text", xlsxDefaultReadLimits, "zip", zipCodeNotZip},
		{"a zip with no workbook", officeZip(t, [2]string{"a.txt", "x"}), xlsxDefaultReadLimits, "office", officeCodeNotOffice},
	}
	for _, c := range cases {
		dom, code := officeCodeOf(t, xlsxReadWith(c.data, c.lim))
		if dom != c.dom || code != c.code {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, dom, code, c.dom, c.code)
		}
	}
	// Too many sheets.
	two := xlsxWriteOK(t, wsheet("A"), wsheet("B"))
	if _, code := officeCodeOf(t, xlsxReadWith(two, xlsxLimits{maxSheets: 1, maxRows: 10, maxCells: 10, maxColumns: 10})); code != officeCodeTooManySheets {
		t.Errorf("too many sheets: %q", code)
	}
	// Shared strings bounded by maxCells.
	many := xlsxWriteOK(t, wsheet("A", []any{wText("a"), wText("b"), wText("c")}))
	if _, code := officeCodeOf(t, xlsxReadWith(many, xlsxLimits{maxSheets: 1, maxRows: 10, maxCells: 2, maxColumns: 10})); code != officeCodeTooManyCells {
		t.Errorf("shared strings: %q", code)
	}
	// A limit that is not positive.
	r := xlsxReadWith(two, xlsxLimits{maxSheets: 0, maxRows: 1, maxCells: 1, maxColumns: 1}).(SkyResult[any, any])
	if r.Tag == 0 {
		t.Error("a zero limit was accepted")
	}
}

func TestXlsxReadZipBomb(t *testing.T) {
	// A worksheet that inflates 64 MiB from a few hundred kilobytes: the
	// ratio limit stops it after the first MiB, before it is held.
	bomb := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + strings.Repeat(" ", 64<<20) + `</sheetData></worksheet>`
	data := xlsxOneSheet(t, bomb)
	if len(data) > 1<<20 {
		t.Fatalf("the bomb is %d bytes", len(data))
	}
	dom, code := officeCodeOf(t, xlsxReadWith(data, xlsxDefaultReadLimits))
	if dom != "zip" || code != zipCodeRatioExceeded {
		t.Errorf("got %s/%s", dom, code)
	}
}

// officeMessage is the message of a Sky Error, for a test failure.
func officeMessage(v any) string {
	if adt, ok := v.(skyErrorAdt); ok && len(adt.Fields) == 2 {
		if info, ok := adt.Fields[1].(skyErrorInfo); ok {
			return info.Message
		}
	}
	return "<not an error>"
}
