package rt

import (
	"fmt"
	"testing"
)

type fieldStrictRec struct {
	Name string
	Age  int
}

func fieldPanic(record any, field string) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprintf("%v", r)
		}
	}()
	Field(record, field)
	return ""
}

// TestField_StrictOnANonRecordOrAMissingField is the FIELD regression.
// Emitted code reads a record field with rt.Field, and it returned nil for a
// value that is not a record (an Int, a String, nil) or has no such field.
// The nil then flowed on as a silent wrong value: a `x.name` read from an
// erased value printed "<nil>" (C/t6). The emitted read is strict: a
// classified CoerceFailure. Runtime-internal optional reads use fieldOrNil.
func TestField_StrictOnANonRecordOrAMissingField(t *testing.T) {
	rec := fieldStrictRec{Name: "Ada", Age: 40}
	if got := Field(rec, "Name"); got != "Ada" {
		t.Fatalf("Field(struct, Name) = %v", got)
	}
	if got := Field(&rec, "Age"); got != 40 {
		t.Fatalf("Field(*struct, Age) = %v", got)
	}
	m := map[string]any{"path": "/x", "opt": nil}
	if got := Field(m, "Path"); got != "/x" {
		t.Fatalf("the case-insensitive map read is gone: %v", got)
	}
	if got := Field(m, "opt"); got != nil {
		t.Fatalf("a present nil map value = %v", got)
	}
	for _, c := range []struct {
		rec   any
		field string
	}{
		{42, "name"},
		{"text", "name"},
		{nil, "name"},
		{[]any{1}, "name"},
		{rec, "Missing"},
		{m, "missing"},
	} {
		msg := fieldPanic(c.rec, c.field)
		if msg == "" {
			t.Errorf("Field(%T, %q) returned instead of failing", c.rec, c.field)
			continue
		}
		if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
			t.Errorf("Field(%T, %q) panicked %q, classified %s, want CoerceFailure", c.rec, c.field, msg, kind)
		}
	}
	// The runtime's own optional reads stay lenient.
	if fieldOrNil(42, "x") != nil || fieldOrNil(m, "missing") != nil || fieldOrNil(nil, "x") != nil {
		t.Fatal("fieldOrNil is not lenient")
	}
	if fieldOrNil(m, "Path") != "/x" || fieldOrNil(rec, "Name") != "Ada" {
		t.Fatal("fieldOrNil lost a present field")
	}
}
