package rt

import "testing"

// v0.15.45 — Std.Db.Decode runtime test coverage.
//
// Exercises the typed DB row decoder pipeline against hand-rolled
// row maps (avoiding a real DB connection, which is covered by the
// example sweep).  Each test pins one combinator's contract.

func TestDbDecStringSucceedsOnPresentColumn(t *testing.T) {
	dec := DbDec_string("name")
	row := map[string]any{"name": "alice", "id": 42}
	got := DbDec_run(dec, row)
	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("expected Ok, got %#v", got)
	}
	if s, ok := r.OkValue.(string); !ok || s != "alice" {
		t.Errorf("expected Ok(\"alice\"), got %v", r.OkValue)
	}
}

func TestDbDecStringFailsOnMissingColumn(t *testing.T) {
	dec := DbDec_string("missing")
	row := map[string]any{"name": "alice"}
	got := DbDec_run(dec, row)
	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("expected Err, got %#v", got)
	}
}

func TestDbDecIntParsesNumericForms(t *testing.T) {
	dec := DbDec_int("age")

	// Native int
	r1 := DbDec_run(dec, map[string]any{"age": 42})
	if v, _ := r1.(SkyResult[any, any]); v.Tag != 0 || v.OkValue.(int) != 42 {
		t.Errorf("native int failed: %#v", r1)
	}

	// float64 round-trip
	r2 := DbDec_run(dec, map[string]any{"age": 42.0})
	if v, _ := r2.(SkyResult[any, any]); v.Tag != 0 || v.OkValue.(int) != 42 {
		t.Errorf("float64 → int failed: %#v", r2)
	}

	// stringified
	r3 := DbDec_run(dec, map[string]any{"age": "42"})
	if v, _ := r3.(SkyResult[any, any]); v.Tag != 0 || v.OkValue.(int) != 42 {
		t.Errorf("string → int failed: %#v", r3)
	}
}

func TestDbDecBoolParsesAllForms(t *testing.T) {
	dec := DbDec_bool("active")

	for _, val := range []any{true, 1, int64(1), "true", "TRUE", "t", "1"} {
		r := DbDec_run(dec, map[string]any{"active": val})
		s, ok := r.(SkyResult[any, any])
		if !ok || s.Tag != 0 || s.OkValue != true {
			t.Errorf("expected Ok(true) for %#v, got %#v", val, r)
		}
	}

	for _, val := range []any{false, 0, int64(0), "false", "FALSE", "f", "0"} {
		r := DbDec_run(dec, map[string]any{"active": val})
		s, ok := r.(SkyResult[any, any])
		if !ok || s.Tag != 0 || s.OkValue != false {
			t.Errorf("expected Ok(false) for %#v, got %#v", val, r)
		}
	}
}

func TestDbDecNullableReturnsNothingOnNull(t *testing.T) {
	inner := DbDec_string("middle_name")
	dec := DbDec_nullable(inner)
	row := map[string]any{"middle_name": nil}
	got := DbDec_run(dec, row)
	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("expected Ok, got %#v", got)
	}
	m, ok := r.OkValue.(SkyMaybe[any])
	if !ok {
		t.Fatalf("expected SkyMaybe[any], got %T", r.OkValue)
	}
	if m.Tag != 1 {
		t.Errorf("expected Nothing (tag 1), got tag %d", m.Tag)
	}
}

func TestDbDecNullableReturnsJustOnPresentValue(t *testing.T) {
	inner := DbDec_string("middle_name")
	dec := DbDec_nullable(inner)
	row := map[string]any{"middle_name": "Q"}
	got := DbDec_run(dec, row)
	r, _ := got.(SkyResult[any, any])
	m, _ := r.OkValue.(SkyMaybe[any])
	if m.Tag != 0 || m.JustValue.(string) != "Q" {
		t.Errorf("expected Just(\"Q\"), got %#v", m)
	}
}

func TestDbDecPipelineBuildsCorrectShape(t *testing.T) {
	// Build a 3-field record decoder via the pipeline shape:
	//
	//   succeed (\id name age -> { id, name, age })
	//      |> andMap (DbDec.int "id")
	//      |> andMap (DbDec.string "name")
	//      |> andMap (DbDec.int "age")
	//
	// SkyCall-curried — `Sky` lambdas always lower to `func(any) any`
	// chains.  Mimics the typed-codegen output.

	ctor := func(id any) any {
		return func(name any) any {
			return func(age any) any {
				return map[string]any{
					"id":   id,
					"name": name,
					"age":  age,
				}
			}
		}
	}

	dec := DbDec_andMap(
		DbDec_int("age"),
		DbDec_andMap(
			DbDec_string("name"),
			DbDec_andMap(
				DbDec_int("id"),
				DbDec_succeed(any(ctor)),
			),
		),
	)

	row := map[string]any{"id": 7, "name": "alice", "age": 30}
	got := DbDec_run(dec, row)
	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("expected Ok, got %#v", got)
	}
	out, ok := r.OkValue.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", r.OkValue)
	}
	if out["id"].(int) != 7 || out["name"].(string) != "alice" || out["age"].(int) != 30 {
		t.Errorf("pipeline produced wrong record: %#v", out)
	}
}

func TestDbDecOptionalFallsBackOnMissingColumn(t *testing.T) {
	ctor := func(name any) any {
		return func(age any) any {
			return map[string]any{"name": name, "age": age}
		}
	}
	// optional col fieldDec fallback ctorDec  →  ctorDec a→b
	dec := DbDec_optional(
		"age", DbDec_int("age"), 0,
		DbDec_andMap(
			DbDec_string("name"),
			DbDec_succeed(any(ctor)),
		),
	)

	// Missing column → uses fallback.
	r1 := DbDec_run(dec, map[string]any{"name": "alice"})
	if s, _ := r1.(SkyResult[any, any]); s.Tag != 0 {
		t.Fatalf("expected Ok with fallback, got %#v", r1)
	}
	out, _ := r1.(SkyResult[any, any]).OkValue.(map[string]any)
	if out["age"].(int) != 0 {
		t.Errorf("expected fallback age=0, got %v", out["age"])
	}

	// Column present → uses decoded value.
	r2 := DbDec_run(dec, map[string]any{"name": "alice", "age": 25})
	out2, _ := r2.(SkyResult[any, any]).OkValue.(map[string]any)
	if out2["age"].(int) != 25 {
		t.Errorf("expected decoded age=25, got %v", out2["age"])
	}
}

func TestDbDecMapTransformsDecodedValue(t *testing.T) {
	upper := func(s any) any {
		v := s.(string)
		out := ""
		for _, c := range v {
			if c >= 'a' && c <= 'z' {
				c = c - 32
			}
			out += string(c)
		}
		return out
	}
	dec := DbDec_map(any(upper), DbDec_string("name"))
	r := DbDec_run(dec, map[string]any{"name": "alice"})
	s, _ := r.(SkyResult[any, any])
	if s.Tag != 0 || s.OkValue.(string) != "ALICE" {
		t.Errorf("expected Ok(\"ALICE\"), got %#v", r)
	}
}
