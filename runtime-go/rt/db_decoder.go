package rt

import (
	"fmt"
	"strconv"
)

// DB row decoders (v0.15.45 — Std.Db.Decode) — mirror of
// Sky.Core.Json.Decode's combinator shape but for SQL row maps.
//
// A `Decoder a` is an opaque value carrying a function from a row
// (map[string]any — the canonical shape Db_query returns) to a
// SkyResult[any, any] holding the decoded value or an ErrDecode.
//
// Combinators (string/int/float/bool/nullable/succeed/fail/map/
// map2..5/andMap/required/optional) compose decoders without ever
// touching the row directly — user code declares the shape, the
// runtime walks the row at run-time once Db.queryDecode / .getByIdDecode
// fires.
//
// Layered above existing Db kernels (Db_query, Db_getById) — does not
// replace them. The existing kernel-only Db.queryDecode (which accepted
// `b` as an opaque second-pass payload) gains a typed-decoder path
// via DbDec_run; old callers still work because Db_queryDecode falls
// through to a generic shape on a non-DbDecoder second arg.

// DbDecoder wraps a row-decoder function.
type DbDecoder struct {
	run func(row map[string]any) any // row → SkyResult[any, any]
}

// dbRowAsMap normalises a DB-row argument to map[string]any. Db_query
// emits map[string]any per row; the typed-codegen pipeline may also
// hand us map[string]string for `Dict String String`. Accept both.
func dbRowAsMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return dbAnyToStringMap(v)
}

// dbColString reads a row column as a String. Accepts nil → "".
func dbColString(row map[string]any, name string) (string, bool) {
	v, ok := row[name]
	if !ok {
		return "", false
	}
	if v == nil {
		return "", true
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	// Numeric/bool round-trip via fmt — the SQL driver may have
	// already widened the cell.
	return fmt.Sprintf("%v", v), true
}

// ── Primitive decoders ─────────────────────────────────────────────

// DbDec_string : String -> Decoder String — read a String column.
func DbDec_string(colName any) any {
	col := AsString(colName)
	return DbDecoder{run: func(row map[string]any) any {
		if s, ok := dbColString(row, col); ok {
			return Ok[any, any](s)
		}
		return Err[any, any](ErrDecode("missing column: " + col))
	}}
}

// DbDec_int : String -> Decoder Int — read a column and parse as Int.
// Accepts int, int64, float64, and decimal-stringified Ints.
func DbDec_int(colName any) any {
	col := AsString(colName)
	return DbDecoder{run: func(row map[string]any) any {
		v, ok := row[col]
		if !ok {
			return Err[any, any](ErrDecode("missing column: " + col))
		}
		switch x := v.(type) {
		case int:
			return Ok[any, any](x)
		case int64:
			return Ok[any, any](int(x))
		case float64:
			return Ok[any, any](int(x))
		case string:
			if n, err := strconv.Atoi(x); err == nil {
				return Ok[any, any](n)
			}
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return Ok[any, any](int(f))
			}
			return Err[any, any](ErrDecode("column " + col + ": expected Int, got " + x))
		case nil:
			return Err[any, any](ErrDecode("column " + col + ": expected Int, got NULL"))
		}
		return Err[any, any](ErrDecode(fmt.Sprintf("column %s: expected Int, got %T", col, v)))
	}}
}

// DbDec_float : String -> Decoder Float — read a column and parse as
// Float. Accepts float64, int, int64, and string forms.
func DbDec_float(colName any) any {
	col := AsString(colName)
	return DbDecoder{run: func(row map[string]any) any {
		v, ok := row[col]
		if !ok {
			return Err[any, any](ErrDecode("missing column: " + col))
		}
		switch x := v.(type) {
		case float64:
			return Ok[any, any](x)
		case int:
			return Ok[any, any](float64(x))
		case int64:
			return Ok[any, any](float64(x))
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return Ok[any, any](f)
			}
			return Err[any, any](ErrDecode("column " + col + ": expected Float, got " + x))
		case nil:
			return Err[any, any](ErrDecode("column " + col + ": expected Float, got NULL"))
		}
		return Err[any, any](ErrDecode(fmt.Sprintf("column %s: expected Float, got %T", col, v)))
	}}
}

// DbDec_bool : String -> Decoder Bool — read a column and parse as
// Bool. Accepts bool, int (0/1), and case-insensitive "true"/"false"/
// "t"/"f"/"1"/"0" string forms.
func DbDec_bool(colName any) any {
	col := AsString(colName)
	return DbDecoder{run: func(row map[string]any) any {
		v, ok := row[col]
		if !ok {
			return Err[any, any](ErrDecode("missing column: " + col))
		}
		switch x := v.(type) {
		case bool:
			return Ok[any, any](x)
		case int:
			return Ok[any, any](x != 0)
		case int64:
			return Ok[any, any](x != 0)
		case string:
			switch x {
			case "true", "TRUE", "True", "t", "T", "1":
				return Ok[any, any](true)
			case "false", "FALSE", "False", "f", "F", "0":
				return Ok[any, any](false)
			}
			return Err[any, any](ErrDecode("column " + col + ": expected Bool, got " + x))
		case nil:
			return Err[any, any](ErrDecode("column " + col + ": expected Bool, got NULL"))
		}
		return Err[any, any](ErrDecode(fmt.Sprintf("column %s: expected Bool, got %T", col, v)))
	}}
}

// DbDec_nullable : String -> Decoder a -> Decoder (Maybe a) —
// returns Just for non-null cells, Nothing for NULL / absent
// columns. The inner decoder gets the value when present.
func DbDec_nullable(colName any, inner any) any {
	col := AsString(colName)
	d, ok := inner.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("nullable: inner is not a Decoder"))
	}
	return DbDecoder{run: func(row map[string]any) any {
		v, present := row[col]
		if !present || v == nil {
			return Ok[any, any](Nothing[any]())
		}
		// Delegate to the inner decoder by re-running it on the same row.
		inner := d.run(row)
		r, ok := inner.(SkyResult[any, any])
		if !ok {
			return Err[any, any](ErrDecode(fmt.Sprintf("nullable: inner returned non-Result %T", inner)))
		}
		if r.Tag != 0 {
			// Inner errored — but we observed a non-null value, so the
			// error is structural (e.g. wrong type for the column). Surface.
			return inner
		}
		return Ok[any, any](Just[any](r.OkValue))
	}}
}

// ── Combinators ────────────────────────────────────────────────────

// DbDec_succeed : a -> Decoder a — always-succeed decoder.
func DbDec_succeed(v any) any {
	captured := v
	return DbDecoder{run: func(_ map[string]any) any {
		return Ok[any, any](captured)
	}}
}

// DbDec_fail : String -> Decoder a — always-fail decoder.
func DbDec_fail(msg any) any {
	m := AsString(msg)
	return DbDecoder{run: func(_ map[string]any) any {
		return Err[any, any](ErrDecode(m))
	}}
}

// DbDec_map : (a -> b) -> Decoder a -> Decoder b — transform the
// decoded value.
func DbDec_map(fn any, dec any) any {
	d, ok := dec.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("map: arg is not a Decoder"))
	}
	return DbDecoder{run: func(row map[string]any) any {
		inner := d.run(row)
		r, ok := inner.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return inner
		}
		mapped := SkyCall(fn, r.OkValue)
		return Ok[any, any](mapped)
	}}
}

// DbDec_andThen : (a -> Decoder b) -> Decoder a -> Decoder b —
// chain decoders.
func DbDec_andThen(fn any, dec any) any {
	d, ok := dec.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("andThen: arg is not a Decoder"))
	}
	return DbDecoder{run: func(row map[string]any) any {
		inner := d.run(row)
		r, ok := inner.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return inner
		}
		next := SkyCall(fn, r.OkValue)
		nd, ok := next.(DbDecoder)
		if !ok {
			return Err[any, any](ErrInvalidInput("andThen: callback returned non-Decoder"))
		}
		return nd.run(row)
	}}
}

// DbDec_andMap : Decoder a -> Decoder (a -> b) -> Decoder b —
// applicative-style decoder combine. Cornerstone of the Pipeline
// shape (required / optional). Reverses Sky's `|>` order:
//
//   succeed Ctor
//      |> andMap (DbDec.string "name")
//      |> andMap (DbDec.int "age")
//
// becomes
//
//   andMap (DbDec.int "age")  (andMap (DbDec.string "name") (succeed Ctor))
//
// — the outer call processes the inner-most fields LAST.
func DbDec_andMap(decA any, decFn any) any {
	da, ok := decA.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("andMap: first arg is not a Decoder"))
	}
	df, ok := decFn.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("andMap: second arg is not a Decoder"))
	}
	return DbDecoder{run: func(row map[string]any) any {
		fnResult := df.run(row)
		fr, ok := fnResult.(SkyResult[any, any])
		if !ok || fr.Tag != 0 {
			return fnResult
		}
		argResult := da.run(row)
		ar, ok := argResult.(SkyResult[any, any])
		if !ok || ar.Tag != 0 {
			return argResult
		}
		applied := SkyCall(fr.OkValue, ar.OkValue)
		return Ok[any, any](applied)
	}}
}

// DbDec_map2 : (a -> b -> c) -> Decoder a -> Decoder b -> Decoder c
func DbDec_map2(fn, d1, d2 any) any {
	da, ok1 := d1.(DbDecoder)
	db, ok2 := d2.(DbDecoder)
	if !ok1 || !ok2 {
		return Err[any, any](ErrInvalidInput("map2: arg is not a Decoder"))
	}
	return DbDecoder{run: func(row map[string]any) any {
		r1 := da.run(row)
		s1, ok := r1.(SkyResult[any, any])
		if !ok || s1.Tag != 0 {
			return r1
		}
		r2 := db.run(row)
		s2, ok := r2.(SkyResult[any, any])
		if !ok || s2.Tag != 0 {
			return r2
		}
		step1 := SkyCall(fn, s1.OkValue)
		final := SkyCall(step1, s2.OkValue)
		return Ok[any, any](final)
	}}
}

// DbDec_map3 : (a -> b -> c -> d) -> Decoder a -> Decoder b -> Decoder c -> Decoder d
func DbDec_map3(fn, d1, d2, d3 any) any {
	da, ok1 := d1.(DbDecoder)
	db, ok2 := d2.(DbDecoder)
	dc, ok3 := d3.(DbDecoder)
	if !ok1 || !ok2 || !ok3 {
		return Err[any, any](ErrInvalidInput("map3: arg is not a Decoder"))
	}
	return DbDecoder{run: func(row map[string]any) any {
		r1 := da.run(row)
		s1, ok := r1.(SkyResult[any, any])
		if !ok || s1.Tag != 0 {
			return r1
		}
		r2 := db.run(row)
		s2, ok := r2.(SkyResult[any, any])
		if !ok || s2.Tag != 0 {
			return r2
		}
		r3 := dc.run(row)
		s3, ok := r3.(SkyResult[any, any])
		if !ok || s3.Tag != 0 {
			return r3
		}
		step1 := SkyCall(fn, s1.OkValue)
		step2 := SkyCall(step1, s2.OkValue)
		final := SkyCall(step2, s3.OkValue)
		return Ok[any, any](final)
	}}
}

// DbDec_map4 : (a -> b -> c -> d -> e) -> Decoder a -> Decoder b -> Decoder c -> Decoder d -> Decoder e
func DbDec_map4(fn, d1, d2, d3, d4 any) any {
	das := []any{d1, d2, d3, d4}
	dbs := make([]DbDecoder, 4)
	for i, d := range das {
		dec, ok := d.(DbDecoder)
		if !ok {
			return Err[any, any](ErrInvalidInput("map4: arg is not a Decoder"))
		}
		dbs[i] = dec
	}
	return DbDecoder{run: func(row map[string]any) any {
		vals := make([]any, 4)
		for i, dec := range dbs {
			r := dec.run(row)
			s, ok := r.(SkyResult[any, any])
			if !ok || s.Tag != 0 {
				return r
			}
			vals[i] = s.OkValue
		}
		acc := fn
		for _, v := range vals {
			acc = SkyCall(acc, v)
		}
		return Ok[any, any](acc)
	}}
}

// DbDec_map5
func DbDec_map5(fn, d1, d2, d3, d4, d5 any) any {
	das := []any{d1, d2, d3, d4, d5}
	dbs := make([]DbDecoder, 5)
	for i, d := range das {
		dec, ok := d.(DbDecoder)
		if !ok {
			return Err[any, any](ErrInvalidInput("map5: arg is not a Decoder"))
		}
		dbs[i] = dec
	}
	return DbDecoder{run: func(row map[string]any) any {
		vals := make([]any, 5)
		for i, dec := range dbs {
			r := dec.run(row)
			s, ok := r.(SkyResult[any, any])
			if !ok || s.Tag != 0 {
				return r
			}
			vals[i] = s.OkValue
		}
		acc := fn
		for _, v := range vals {
			acc = SkyCall(acc, v)
		}
		return Ok[any, any](acc)
	}}
}

// ── Pipeline-style ─────────────────────────────────────────────────

// DbDec_required : String -> Decoder a -> Decoder (a -> b) -> Decoder b
// Mirrors Json.Decode.Pipeline.required for SQL rows: read a column
// using the supplied per-column decoder, apply the partial-applied
// constructor.
func DbDec_required(colName any, fieldDec any, ctorDec any) any {
	_ = colName // the field decoder already names its column
	return DbDec_andMap(fieldDec, ctorDec)
}

// DbDec_optional : String -> Decoder a -> a -> Decoder (a -> b) -> Decoder b
// Like `required` but supplies a fallback when the field's decoder
// fails (typically absent column / NULL).
func DbDec_optional(colName any, fieldDec any, fallback any, ctorDec any) any {
	_ = colName
	fd, ok := fieldDec.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("optional: field arg is not a Decoder"))
	}
	withFallback := DbDecoder{run: func(row map[string]any) any {
		inner := fd.run(row)
		r, ok := inner.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return Ok[any, any](fallback)
		}
		return inner
	}}
	return DbDec_andMap(withFallback, ctorDec)
}

// ── Run a decoder against a row ────────────────────────────────────

// DbDec_run : Decoder a -> row -> Result Error a — used internally
// by Db.queryDecode and Db.getByIdDecode. Exposed for testing.
func DbDec_run(decoder any, row any) any {
	d, ok := decoder.(DbDecoder)
	if !ok {
		return Err[any, any](ErrInvalidInput("run: arg is not a Decoder"))
	}
	m, ok := dbRowAsMap(row)
	if !ok {
		return Err[any, any](ErrInvalidInput("run: row is not a Dict-shaped map"))
	}
	return d.run(m)
}

// Db_queryDecodeRows : Db -> String -> List a -> Decoder b ->
// Task Error (List b) — like Db.queryDecode but typed against the
// new DbDecoder.  Renamed entry point so the legacy Db_queryDecode
// (which accepted a JSON-shaped opaque second pass) keeps working for
// callers that haven't migrated.
//
// v0.15.45: the existing Db_queryDecode is the user-facing kernel.
// We rewire it below to recognise both DbDecoder AND the prior
// shape (no decoder → return rows as-is).
func Db_queryDecodeRows(db any, query any, args any, decoder any) any {
	capDb, capQ, capArgs, capDec := db, query, args, decoder
	return func() any {
		resp := AnyTaskRun(Db_query(capDb, capQ, capArgs))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
		rows := AsList(r.OkValue)
		d, isDec := capDec.(DbDecoder)
		if !isDec {
			// Fall through to the legacy JSON path (kept for back-compat).
			return legacyJsonDecodeQuery(rows, capDec)
		}
		out := make([]any, 0, len(rows))
		for _, row := range rows {
			m, ok := dbRowAsMap(row)
			if !ok {
				return Err[any, any](ErrDecode("queryDecode: row is not a Dict"))
			}
			result := d.run(m)
			sr, ok := result.(SkyResult[any, any])
			if !ok {
				return Err[any, any](ErrDecode("queryDecode: decoder returned non-Result"))
			}
			if sr.Tag != 0 {
				return result
			}
			out = append(out, sr.OkValue)
		}
		return Ok[any, any](out)
	}
}

// legacyJsonDecodeQuery: the pre-v0.15.45 behaviour — try a JsonDecoder
// path, else surface rows raw. Kept so callers that passed a JsonDecoder
// (or no decoder at all) keep working.
func legacyJsonDecodeQuery(rows []any, decoder any) any {
	d, isDec := decoder.(JsonDecoder)
	if !isDec {
		return Ok[any, any](rows)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		result := d.run(row)
		sr, ok := result.(SkyResult[any, any])
		if !ok {
			return Err[any, any](ErrDecode("decode error"))
		}
		if sr.Tag != 0 {
			return result
		}
		out = append(out, sr.OkValue)
	}
	return Ok[any, any](out)
}

// Db_getByIdDecode : Db -> String -> Int -> Decoder a ->
// Task Error (Maybe a) — typed sibling of Db.getById. Threads the
// decoded shape through; returns Nothing when no row matches the id.
func Db_getByIdDecode(db any, table any, id any, decoder any) any {
	capDb, capTable, capId, capDec := db, table, id, decoder
	return func() any {
		resp := AnyTaskRun(Db_getById(capDb, capTable, capId))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
		// getById returns Maybe (Dict String String). Surface Nothing
		// directly; on Just, run the decoder.
		m, isMaybe := r.OkValue.(SkyMaybe[any])
		if !isMaybe || m.Tag != 0 {
			return Ok[any, any](Nothing[any]())
		}
		d, isDec := capDec.(DbDecoder)
		if !isDec {
			// No decoder — pass the raw Dict through.
			return Ok[any, any](Just[any](m.JustValue))
		}
		row, ok := dbRowAsMap(m.JustValue)
		if !ok {
			return Err[any, any](ErrDecode("getByIdDecode: row is not a Dict"))
		}
		result := d.run(row)
		sr, ok := result.(SkyResult[any, any])
		if !ok {
			return Err[any, any](ErrDecode("getByIdDecode: decoder returned non-Result"))
		}
		if sr.Tag != 0 {
			return result
		}
		return Ok[any, any](Just[any](sr.OkValue))
	}
}
