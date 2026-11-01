//go:build !js

package rt

// Database time values as text.
//
// The drivers hand a time column to Sky as a Go time.Time: pgx for a
// PostgreSQL timestamptz, timestamp or date, and modernc for a SQLite TEXT
// value in a column declared DATE, DATETIME or TIMESTAMP. Every Sky reader of
// that column sees text (a `Dict String String` row, a Store or Codec
// `String` field, Std.Db.Decode.string). Before v0.29.1 that text was
// time.Time.String(), which renders a timestamptz in the zone of the app
// PROCESS ("2026-11-01 02:13:48.987333 +0000 GMT"). The same stored instant
// then read differently on two hosts, and PostgreSQL refused the text as a
// parameter.
//
// The rows are normalised once, where they are scanned (dbScanRowDicts), so
// every reader sees the same stable text. The format is PostgreSQL's own text
// form, as `::text` gives it on a UTC server:
//
//	timestamptz  YYYY-MM-DD HH:MM:SS[.ffffff]+00   (always in UTC)
//	timestamp    YYYY-MM-DD HH:MM:SS[.ffffff]
//	date         YYYY-MM-DD
//	time         HH:MM:SS[.ffffff]
//
// A BC year reads with a " BC" suffix and a year past 9999 keeps all its
// digits, as PostgreSQL writes them. Trailing zeros of the fraction are
// dropped. SQLite reads in the same shapes: a DATE value with no time of day
// reads as YYYY-MM-DD, any other value as YYYY-MM-DD HH:MM:SS[.fffffffff] in
// the clock it was written with, followed by its offset as +HH:MM (SQLite's
// own form) when the written text carried a non-zero offset.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// dbTimeText renders a driver time value as stable text, independent of the
// process time zone. dbType is the column's database type name as
// database/sql reports it ("TIMESTAMPTZ", "TIMESTAMP", "DATE", "DATETIME",
// ...); an unknown or empty type reads the value as an instant, in UTC.
func dbTimeText(t time.Time, dbType string) string {
	switch strings.ToUpper(dbType) {
	case "DATE":
		if _, off := t.Zone(); off == 0 && t.Hour() == 0 && t.Minute() == 0 &&
			t.Second() == 0 && t.Nanosecond() == 0 {
			return dbDateText(t, "")
		}
		return dbLocalDateTimeText(t)
	case "TIMESTAMP", "DATETIME":
		return dbLocalDateTimeText(t)
	default: // TIMESTAMPTZ, or a type the drivers do not name
		u := t.UTC()
		return dbDateText(u, " "+dbClockText(u)+"+00")
	}
}

// dbLocalDateTimeText renders a value with no zone of its own (a PostgreSQL
// timestamp, a SQLite DATETIME) in the clock it carries. pgx gives such a
// value in UTC. modernc gives it in UTC unless the written text had an
// offset, which is kept as +HH:MM.
func dbLocalDateTimeText(t time.Time) string {
	suffix := ""
	if _, off := t.Zone(); off != 0 {
		sign := '+'
		if off < 0 {
			sign = '-'
			off = -off
		}
		suffix = fmt.Sprintf("%c%02d:%02d", sign, off/3600, off%3600/60)
	}
	return dbDateText(t, " "+dbClockText(t)+suffix)
}

// dbDateText renders the date part of t, then rest, then " BC" for a year
// before 1 AD (Go's year 0 is 1 BC), as PostgreSQL writes it.
func dbDateText(t time.Time, rest string) string {
	y, bc := t.Year(), ""
	if y <= 0 {
		y, bc = 1-y, " BC"
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, int(t.Month()), t.Day()) + rest + bc
}

// dbClockText renders HH:MM:SS with the fraction, trailing zeros dropped.
func dbClockText(t time.Time) string {
	s := fmt.Sprintf("%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
	if ns := t.Nanosecond(); ns != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%09d", ns), "0")
	}
	return s
}

// pgTimeOfDayText drops the trailing zeros of a PostgreSQL time-of-day
// value. pgx reads a `time` column as a string, and in the binary protocol
// it always writes six fraction digits ("02:13:48.000000") where PostgreSQL's
// own text is "02:13:48". Any other text is returned unchanged.
func pgTimeOfDayText(s string) string {
	if len(s) < 9 || s[2] != ':' || s[5] != ':' || s[8] != '.' {
		return s
	}
	for i := 9; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return s
		}
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// dbColumnTypeNames returns each result column's database type name, or nil
// when the driver cannot report them (the time values then read as
// instants).
func dbColumnTypeNames(rows *sql.Rows) []string {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil
	}
	out := make([]string, len(cts))
	for i, ct := range cts {
		out[i] = strings.ToUpper(ct.DatabaseTypeName())
	}
	return out
}

// normaliseSqlColumn is normaliseSqlValue with the column's type known: a
// time value becomes its stable text (dbTimeText), and a PostgreSQL time of
// day loses its binary-protocol trailing zeros.
func normaliseSqlColumn(v any, dbType, driver string) any {
	switch x := v.(type) {
	case time.Time:
		return dbTimeText(x, dbType)
	case string:
		if driver == "pgx" && dbType == "TIME" {
			return pgTimeOfDayText(x)
		}
	}
	return normaliseSqlValue(v)
}

// dbScanRowDicts reads every row of rows as column name → normalised value.
// It is the one place a query result becomes a Sky row, so every reader
// (Db.query, the typed decoders, Store, Codec, Table) sees the same text for
// a time value. The caller checks rows.Err afterwards.
func dbScanRowDicts(rows *sql.Rows, driver, op string) ([]map[string]any, any) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, Err[any, any](ErrIo(op + " columns: " + err.Error()))
	}
	types := dbColumnTypeNames(rows)
	var out []map[string]any
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, Err[any, any](ErrIo(op + " scan: " + err.Error()))
		}
		rowDict := make(map[string]any, len(cols))
		for i, c := range cols {
			dbType := ""
			if i < len(types) {
				dbType = types[i]
			}
			rowDict[c] = normaliseSqlColumn(raw[i], dbType, driver)
		}
		out = append(out, rowDict)
	}
	return out, nil
}
