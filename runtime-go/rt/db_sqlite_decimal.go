//go:build !js

package rt

// Exact decimal ordering and sum on SQLite.
//
// A Std.Db.Store decimal column (Codec.decimal, Codec.moneyIn) is TEXT on
// SQLite holding the canonical decimal (`12.5`), because NUMERIC affinity
// would turn `0.1` into a REAL. Text does not order as a number (`10` <
// `9.5`), and SUM over text goes through REAL. So the Store renders, on
// SQLite only:
//
//   - `col COLLATE sky_decimal` in a comparison, an ORDER BY and MIN / MAX,
//     which compares the two texts as decimals;
//   - `sky_decimal_sum(col)` for sumAs, which adds exactly and returns the
//     canonical text.
//
// Both read the JSON spelling (`"12.5"`) a v0.23-v0.29.1 Codec.auto blob
// column holds, so legacy rows compare and add the same way. Text that is
// not a decimal sorts after every decimal, in text order, so the order is
// total and stable; the sum refuses it with an error.
//
// The registration is process-wide and applies to every SQLite connection
// opened after init (database/sql opens them lazily, after this runs).

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	sqlite "modernc.org/sqlite"
)

// sqliteDecimalOf reads one SQLite TEXT value as a decimal: the canonical
// text, or its JSON spelling.
func sqliteDecimalOf(s string) (decimal.Decimal, bool) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	d, err := decimalParseStrict(strings.TrimSpace(s))
	return d, err == nil
}

// sqliteDecimalCollate is the sky_decimal collation: decimals by value, then
// any other text after them, by text.
func sqliteDecimalCollate(left, right string) int {
	l, lok := sqliteDecimalOf(left)
	r, rok := sqliteDecimalOf(right)
	switch {
	case lok && rok:
		return l.Cmp(r)
	case lok:
		return -1
	case rok:
		return 1
	}
	return strings.Compare(left, right)
}

// sqliteDecimalArg reads one aggregate argument. NULL is skipped (ok=false,
// err=nil); text that is not a decimal is an error.
func sqliteDecimalArg(v driver.Value) (decimal.Decimal, bool, error) {
	switch x := v.(type) {
	case nil:
		return decimal.Zero, false, nil
	case int64:
		return decimal.NewFromInt(x), true, nil
	case float64:
		// A REAL a raw statement wrote: its shortest round-trip text.
		d, err := decimal.NewFromString(strconv.FormatFloat(x, 'f', -1, 64))
		return d, err == nil, err
	case string:
		if d, ok := sqliteDecimalOf(x); ok {
			return d, true, nil
		}
		return decimal.Zero, false, fmt.Errorf("sky_decimal_sum: %q is not a decimal", strictShortText(x))
	case []byte:
		if d, ok := sqliteDecimalOf(string(x)); ok {
			return d, true, nil
		}
		return decimal.Zero, false, fmt.Errorf("sky_decimal_sum: %q is not a decimal", strictShortText(string(x)))
	}
	return decimal.Zero, false, fmt.Errorf("sky_decimal_sum: a %T is not a decimal", v)
}

// sqliteDecimalSum is one evaluation of sky_decimal_sum: exact, NULL for no
// non-NULL row (as SUM), else the canonical text.
type sqliteDecimalSum struct {
	sum  decimal.Decimal
	rows int
}

func (a *sqliteDecimalSum) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) != 1 {
		return fmt.Errorf("sky_decimal_sum takes one argument")
	}
	d, ok, err := sqliteDecimalArg(args[0])
	if err != nil {
		return err
	}
	if ok {
		a.sum = a.sum.Add(d)
		a.rows++
	}
	return nil
}

func (a *sqliteDecimalSum) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) != 1 {
		return fmt.Errorf("sky_decimal_sum takes one argument")
	}
	d, ok, err := sqliteDecimalArg(args[0])
	if err != nil {
		return err
	}
	if ok {
		a.sum = a.sum.Sub(d)
		a.rows--
	}
	return nil
}

func (a *sqliteDecimalSum) WindowValue(_ *sqlite.FunctionContext) (driver.Value, error) {
	if a.rows == 0 {
		return nil, nil
	}
	return decimalCanonical(a.sum), nil
}

func (a *sqliteDecimalSum) Final(_ *sqlite.FunctionContext) {}

func init() {
	sqlite.MustRegisterCollationUtf8("sky_decimal", sqliteDecimalCollate)
	sqlite.MustRegisterFunction("sky_decimal_sum", &sqlite.FunctionImpl{
		NArgs:         1,
		Deterministic: true,
		MakeAggregate: func(_ sqlite.FunctionContext) (sqlite.AggregateFunction, error) {
			return &sqliteDecimalSum{sum: decimal.Zero}, nil
		},
	})
}
