//go:build !js

package rt

// PostgreSQL argument binding for a column whose real type is TEXT.
//
// pgx encodes a parameter by the type the server infers for it, and it has no
// plan for a Go int64, float64 or bool into a TEXT parameter ("unable to
// encode 7 into text format for text (OID 25)"). SQLite takes the value and
// stores its text. A TEXT column can hold a value the Store binds as a
// number: v0.28 created the column of a nested `Codec.maybe` scalar
// (`Maybe (Maybe Int)`, kind `int??`) as TEXT, v0.29 gives that field the
// kind `int?` and binds an int64, and migrate never alters an existing
// column. So on pgx every int, float and bool argument is bound as a named
// type that also gives its text: into a TEXT parameter pgx writes the text
// (the decimal, the shortest float, `true` / `false`, the same text v0.28
// wrote), into any other parameter pgx finds the underlying Go type and
// encodes it exactly as before.

import (
	"database/sql"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

type pgArgInt int64

func (v pgArgInt) TextValue() (pgtype.Text, error) {
	return pgtype.Text{String: strconv.FormatInt(int64(v), 10), Valid: true}, nil
}

type pgArgFloat float64

func (v pgArgFloat) TextValue() (pgtype.Text, error) {
	return pgtype.Text{String: strconv.FormatFloat(float64(v), 'g', -1, 64), Valid: true}, nil
}

type pgArgBool bool

func (v pgArgBool) TextValue() (pgtype.Text, error) {
	return pgtype.Text{String: strconv.FormatBool(bool(v)), Valid: true}, nil
}

// pgArg is one argument as pgx binds it: an int, float or bool as its named
// type, anything else unchanged.
func pgArg(a any) any {
	switch v := a.(type) {
	case int64:
		return pgArgInt(v)
	case int:
		return pgArgInt(int64(v))
	case float64:
		return pgArgFloat(v)
	case bool:
		return pgArgBool(v)
	}
	return a
}

func pgArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = pgArg(a)
	}
	return out
}

// pgArgExecutor is a dbExecutor on pgx that binds its arguments with pgArg,
// a marked list parameter as an array (db_list_param.go), and casts a marked
// Int placeholder to BIGINT on an INTEGER or SMALLINT column
// (db_pgx_int_cast.go). It clears the column-type cache after a schema
// statement, and after a marked statement fails with an error a stale entry
// can cause.
type pgArgExecutor struct {
	inner dbExecutor
	pool  *sql.DB
}

func (e pgArgExecutor) prepare(query string, args []any) (string, []any, bool) {
	q, marked := pgIntCastQuery(e.inner, e.pool, query)
	return q, pgArgs(pgListArgs(query, args)), marked
}

func (e pgArgExecutor) after(query string, marked bool, err error) {
	if sqliteIsSchemaStatement(query) || (marked && pgIntStaleErr(err)) {
		pgIntCacheReset(e.pool)
	}
}

func (e pgArgExecutor) Exec(query string, args ...any) (sql.Result, error) {
	q, a, marked := e.prepare(query, args)
	res, err := e.inner.Exec(q, a...)
	e.after(query, marked, err)
	return res, err
}

func (e pgArgExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	q, a, marked := e.prepare(query, args)
	rows, err := e.inner.Query(q, a...)
	e.after(query, marked, err)
	return rows, err
}

func (e pgArgExecutor) QueryRow(query string, args ...any) *sql.Row {
	q, a, marked := e.prepare(query, args)
	row := e.inner.QueryRow(q, a...)
	if marked {
		e.after(query, marked, row.Err())
	}
	return row
}
