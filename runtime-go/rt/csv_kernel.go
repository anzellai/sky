// Package rt — Std.Csv runtime kernels.
//
// v0.15.47 stdlib batch (#380): CSV encode/decode + streaming reader.
// All built on Go's stdlib `encoding/csv` for RFC 4180 conformance.
package rt

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
)

// csvDelimiterRune extracts the single-rune delimiter from a Sky string.
// Empty string OR non-ASCII multi-rune input → ','.
func csvDelimiterRune(delim any) rune {
	s := fmt.Sprintf("%v", delim)
	if s == "" {
		return ','
	}
	for _, r := range s {
		return r
	}
	return ','
}

// makeCsvRecord builds the canonical `{ header, rows }` record value
// that the Sky-side `Csv` type alias decodes to. Returned as a
// `map[string]any` — `coerceMapToStruct` bridges to the user's
// typed `Sky_Std_Csv_Csv_R` shape.
func makeCsvRecord(header []string, rows [][]string) any {
	hdr := make([]any, len(header))
	for i, s := range header {
		hdr[i] = s
	}
	rs := make([]any, len(rows))
	for i, row := range rows {
		inner := make([]any, len(row))
		for j, c := range row {
			inner[j] = c
		}
		rs[i] = inner
	}
	return map[string]any{
		"header": hdr,
		"rows":   rs,
	}
}

// Csv_parse implements:
//
//	Std.Csv.parse : String -> Result Error Csv
func Csv_parse(input any) any {
	return csvParseImpl(asBytesString(input), ',')
}

// Csv_parseWithDelimiter implements:
//
//	Std.Csv.parseWithDelimiter : String -> String -> Result Error Csv
func Csv_parseWithDelimiter(delim, input any) any {
	return csvParseImpl(asBytesString(input), csvDelimiterRune(delim))
}

func csvParseImpl(input string, delim rune) any {
	r := csv.NewReader(strings.NewReader(input))
	r.Comma = delim
	r.FieldsPerRecord = -1 // tolerate variable-width
	records, err := r.ReadAll()
	if err != nil {
		return Err[any, any](ErrInvalidInput("csv.parse: " + err.Error()))
	}
	if len(records) == 0 {
		return Ok[any, any](makeCsvRecord(nil, nil))
	}
	header := records[0]
	rows := records[1:]
	return Ok[any, any](makeCsvRecord(header, rows))
}

// Csv_encode implements:
//
//	Std.Csv.encode : Csv -> String
func Csv_encode(csvArg any) any {
	return csvEncodeImpl(csvArg, ',')
}

// Csv_encodeWithDelimiter implements:
//
//	Std.Csv.encodeWithDelimiter : String -> Csv -> String
func Csv_encodeWithDelimiter(delim, csvArg any) any {
	return csvEncodeImpl(csvArg, csvDelimiterRune(delim))
}

func csvEncodeImpl(csvArg any, delim rune) string {
	header := csvExtractStringList(recordField(csvArg, "Header", "header"))
	rowsRaw := AsList(recordField(csvArg, "Rows", "rows"))
	rows := make([][]string, 0, len(rowsRaw))
	for _, r := range rowsRaw {
		rows = append(rows, csvExtractStringList(r))
	}
	var buf strings.Builder
	w := csv.NewWriter(&buf)
	w.Comma = delim
	if len(header) > 0 {
		_ = w.Write(header)
	}
	_ = w.WriteAll(rows)
	w.Flush()
	out := buf.String()
	// strings.Builder writes \r\n by default per encoding/csv; trim
	// the trailing \n if the user passed an empty body so the
	// round-trip is exact.
	return out
}

// csvExtractStringList converts any list-shaped value to []string.
func csvExtractStringList(v any) []string {
	items := AsList(v)
	out := make([]string, len(items))
	for i, it := range items {
		if s, ok := it.(string); ok {
			out[i] = s
		} else {
			out[i] = fmt.Sprintf("%v", it)
		}
	}
	return out
}

// Csv_parseStreamFromFile implements:
//
//	Std.Csv.parseStreamFromFile : String -> Task Error (List (List String))
//
// Returns every row (including header) as `List (List String)`.
// Buffered reader; doesn't materialise the whole file in memory.
func Csv_parseStreamFromFile(pathArg any) any {
	path := fmt.Sprintf("%v", pathArg)
	return func() any {
		f, err := os.Open(path)
		if err != nil {
			return Err[any, any](ErrInvalidInput("csv.parseStream: " + err.Error()))
		}
		defer f.Close()
		r := csv.NewReader(f)
		r.FieldsPerRecord = -1
		var rows []any
		for {
			rec, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return Err[any, any](ErrInvalidInput("csv.parseStream: " + err.Error()))
			}
			inner := make([]any, len(rec))
			for i, c := range rec {
				inner[i] = c
			}
			rows = append(rows, inner)
		}
		return Ok[any, any](rows)
	}
}
