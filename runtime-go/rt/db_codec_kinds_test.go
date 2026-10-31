package rt

// Value kinds on the Std.Db.Store write and read paths (v0.29.0). A value of
// the wrong kind for its column is an error naming the column, never a
// converted value: rawToSqlArg used to write 0, false or the Go `%v` text,
// and rowValToJsonRaw used to read an unparsable stored value as JSON null,
// which a `Codec.maybe` decoder reads as Nothing.

import (
	"fmt"
	"strings"
	"testing"
)

func TestRawToSqlArgRefusesAValueOfTheWrongKind(t *testing.T) {
	refused := []struct {
		kind string
		raw  any
	}{
		{"text", 5},
		{"text", true},
		{"int", "abc"},
		{"int", 2.5},
		{"int", true},
		{"real", "1.5"},
		{"bool", 1},
		{"bool", "true"},
	}
	for _, c := range refused {
		if v, err := rawToSqlArg(c.raw, c.kind, "col"); err == nil {
			t.Errorf("%s <- %#v: got %#v, want an error", c.kind, c.raw, v)
		} else if !strings.Contains(err.Error(), `column "col" is `+c.kind) {
			t.Errorf("%s <- %#v: the error does not name the column and kind: %v", c.kind, c.raw, err)
		}
	}
	accepted := []struct {
		kind string
		raw  any
		want any
	}{
		{"text", "x", "x"},
		{"int", 7, int64(7)},
		{"int", 3.0, int64(3)},
		{"real", 2, 2.0},
		{"real", 1.5, 1.5},
		{"bool", false, false},
		{"blob", []any{"a"}, `["a"]`},
		{"blob", "s", `"s"`},
		{"int", nil, nil},
	}
	for _, c := range accepted {
		v, err := rawToSqlArg(c.raw, c.kind, "col")
		if err != nil || v != c.want {
			t.Errorf("%s <- %#v: got (%#v, %v), want %#v", c.kind, c.raw, v, err, c.want)
		}
	}
}

func TestObjectSqlValuesIsAnErrForAValueOfTheWrongKind(t *testing.T) {
	cs := cspec("id", "text", "n", "int")
	rec := storeWriteRecord([]string{"id", "n"}, []any{"a", "abc"})
	r, ok := Db_objectSqlValues(cs, rec).(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("want an Err, got %+v", r)
	}
	if msg := fmt.Sprintf("%v", r.ErrValue); !strings.Contains(msg, `column "n" is int`) {
		t.Fatalf("the Err does not name the column: %q", msg)
	}
}

func TestRowValToJsonRawReadsEveryLegacyShapeAndRefusesTheRest(t *testing.T) {
	accepted := []struct {
		raw  any
		kind string
		want any
	}{
		{"5", "int", int64(5)},           // a v0.28 nested Maybe Int written as text
		{int64(5), "int", int64(5)},      // an INTEGER cell
		{"1e+06", "int", int64(1000000)}, // the %v text of an integral float
		{"5.0", "int", int64(5)},
		{"2.5", "real", 2.5},
		{int64(3), "real", 3.0},
		{int64(1), "bool", true}, // SQLite 0/1
		{int64(0), "bool", false},
		{true, "bool", true}, // PostgreSQL
		{"f", "bool", false},
		{"[1,2]", "blob", []any{float64(1), float64(2)}},
		{"anything", "text", "anything"},
		{nil, "int", nil}, // SQL NULL stays null
	}
	for _, c := range accepted {
		v, err := rowValToJsonRaw(c.raw, true, c.kind, "col")
		if err != nil {
			t.Errorf("%#v as %s: unexpected error %v", c.raw, c.kind, err)
			continue
		}
		if s, ok := v.([]any); ok {
			w := c.want.([]any)
			if len(s) != len(w) || s[0] != w[0] || s[1] != w[1] {
				t.Errorf("%#v as %s: got %#v, want %#v", c.raw, c.kind, v, c.want)
			}
			continue
		}
		if v != c.want {
			t.Errorf("%#v as %s: got %#v, want %#v", c.raw, c.kind, v, c.want)
		}
	}
	refused := []struct {
		raw  any
		kind string
	}{
		{"abc", "int"},
		{"2.7", "int"},
		{"abc", "real"},
		{int64(2), "bool"},
		{"yes", "bool"},
		{"not json", "blob"},
	}
	for _, c := range refused {
		if v, err := rowValToJsonRaw(c.raw, true, c.kind, "col"); err == nil {
			t.Errorf("%#v as %s: got %#v, want an error", c.raw, c.kind, v)
		} else if !strings.HasPrefix(err.Error(), `Store: column "col" is `) {
			t.Errorf("%#v as %s: the error does not name the column: %v", c.raw, c.kind, err)
		}
	}
	if _, err := rowValToJsonRaw(nil, false, "text", "gone"); err == nil ||
		!strings.Contains(err.Error(), `column "gone" is not in the query result`) {
		t.Errorf("a column missing from the row: got %v, want an error naming it", err)
	}
}

func TestRenderAddColumnRefusesADefaultOfTheWrongKind(t *testing.T) {
	text := func(s string) *migDefault { return &migDefault{Text: &s} }
	i := int64(1)
	f := 1.5
	b := true
	refused := []migColumn{
		{Name: "n", Type: "int", Nullable: true, Default: text("abc")},
		{Name: "n", Type: "int", Default: &migDefault{Float: &f}},
		{Name: "ok", Type: "bool", Default: &migDefault{Int: &i}},
		{Name: "s", Type: "text", Default: &migDefault{Bool: &b}},
		{Name: "tags", Type: "blob", Default: text("not json")},
		{Name: "x", Type: "real", Default: &migDefault{Now: true}},
	}
	for _, c := range refused {
		if _, err := renderAddColumn("sqlite", "t", c, "2026-01-01 00:00:00", 0); err == nil {
			t.Errorf("%s %s with default %s: want an error", c.Name, c.Type, migDefKind(c.Default))
		}
	}
	accepted := []migColumn{
		{Name: "n", Type: "int", Default: &migDefault{Int: &i}},
		{Name: "x", Type: "real", Default: &migDefault{Int: &i}},
		{Name: "tags", Type: "blob", Default: text("[]")},
		{Name: "at", Type: "text", Default: &migDefault{Now: true}},
		{Name: "ms", Type: "int", Default: &migDefault{Now: true}},
		{Name: "s", Type: "text", Default: text("abc")},
	}
	for _, c := range accepted {
		if _, err := renderAddColumn("sqlite", "t", c, "2026-01-01 00:00:00", 0); err != nil {
			t.Errorf("%s %s with default %s: unexpected error %v", c.Name, c.Type, migDefKind(c.Default), err)
		}
	}
}
