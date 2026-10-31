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

// pgArgExecutor is a dbExecutor on pgx that binds its arguments with pgArg.
type pgArgExecutor struct{ inner dbExecutor }

func (e pgArgExecutor) Exec(query string, args ...any) (sql.Result, error) {
	return e.inner.Exec(query, pgArgs(args)...)
}

func (e pgArgExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	return e.inner.Query(query, pgArgs(args)...)
}

func (e pgArgExecutor) QueryRow(query string, args ...any) *sql.Row {
	return e.inner.QueryRow(query, pgArgs(args)...)
}
