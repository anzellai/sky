package rt

// Std.Db.Store writes (v0.29.0, batch D): the two kernels the Sky side uses
// for insertOrIgnore / upsertWith / insertReturning (Db_objectSqlValues) and
// for every RETURNING verb (Db_writeReturningObjects), plus the SQL the Store
// renders for the conflict rules, run on SQLite. The PostgreSQL arm and the
// concurrency tests are in db_store_write_integration_test.go.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func storeWriteRecord(keys []string, vals []any) JsonValue {
	return JsonValue{raw: jsonOrderedObject{keys: keys, vals: vals}}
}

func TestStoreWriteObjectSqlValuesFollowTheColspec(t *testing.T) {
	cs := cspec("id", "text", "n", "int", "price", "real", "ok", "bool", "note", "text?", "tags", "blob")
	rec := storeWriteRecord(
		[]string{"tags", "note", "ok", "price", "n", "id"},
		[]any{[]any{"a", "b"}, nil, true, 1.5, 7, "x'1"})
	got := AsList(storeWriteOk(t, Db_objectSqlValues(cs, rec)))
	want := []string{"SqlString x'1", "SqlInt 7", "SqlFloat 1.5", "SqlBool true", "SqlNull", "SqlString [\"a\",\"b\"]"}
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d", len(got), len(want))
	}
	for i, v := range got {
		adt := v.(SkyADT)
		s := adt.SkyName
		if adt.SkyName != "SqlNull" {
			s += " " + fmt.Sprintf("%v", adt.Fields[0])
		}
		if s != want[i] {
			t.Fatalf("value %d: got %q, want %q", i, s, want[i])
		}
	}
}

// storeWriteOk unwraps an Ok, failing the test on an Err.
func storeWriteOk(t *testing.T, v any) any {
	t.Helper()
	r, ok := v.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("want Ok, got %+v", v)
	}
	return r.OkValue
}

// storeWriteExec runs a statement through Db_exec (the Store's runExec) and
// returns rows affected.
func storeWriteExec(t *testing.T, d *SkyDb, sql string, params []any) int {
	t.Helper()
	r, ok := AnyTaskRun(Db_exec(d, sql, params)).(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("%s: %+v", sql, r)
	}
	return AsInt(r.OkValue)
}

// The counter upsert the Store renders for
//
//	upsertWith { conflictOn = [ day, customer_id, event ],
//	             onConflict = [ addExcluded count, keepMin first_at, keepMax last_at, setExcluded source ] }
func storeWriteCounterSql(table string) string {
	keep := func(c, op string) string {
		old, new := table+"."+c, "excluded."+c
		return c + " = CASE WHEN " + new + " IS NULL THEN " + old + " WHEN " + old + " IS NULL THEN " +
			new + " WHEN " + new + " " + op + " " + old + " THEN " + new + " ELSE " + old + " END"
	}
	return "INSERT INTO " + table + " (day, customer_id, event, count, first_at, last_at, source) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (day, customer_id, event) DO UPDATE SET count = " +
		table + ".count + excluded.count, " + keep("first_at", "<") + ", " + keep("last_at", ">") +
		", source = excluded.source"
}

func storeWriteCounterTable(t *testing.T, d *SkyDb, table string) []any {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	cs := cspec("day", "text", "customer_id", "text", "event", "text", "count", "int",
		"first_at", "int?", "last_at", "int?", "source", "text")
	ix := []any{storeIdx([]string{"day", "customer_id", "event"}, true, "")}
	if r := autoMigrate(t, d, table, cs, ix); r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	return cs
}

func storeWriteCounterValues(cs []any, n int, first, last any, source string) []any {
	rec := storeWriteRecord(
		[]string{"day", "customer_id", "event", "count", "first_at", "last_at", "source"},
		[]any{"2026-10-30", "c1", "open", n, first, last, source})
	r := Db_objectSqlValues(cs, rec).(SkyResult[any, any])
	return AsList(r.OkValue)
}

func assertCounterUpsertRules(t *testing.T, d *SkyDb, table string) {
	cs := storeWriteCounterTable(t, d, table)
	sql := storeWriteCounterSql(table)
	steps := []struct {
		n           int
		first, last any
		source      string
	}{{2, nil, 9, "web"}, {3, 5, 3, "app"}, {1, 2, nil, "cli"}}
	for _, s := range steps {
		if n := storeWriteExec(t, d, sql, storeWriteCounterValues(cs, s.n, s.first, s.last, s.source)); n != 1 {
			t.Fatalf("rows affected %d, want 1", n)
		}
	}
	var count, first, last int
	var source string
	if err := d.conn.QueryRow("SELECT count, first_at, last_at, source FROM "+table).
		Scan(&count, &first, &last, &source); err != nil {
		t.Fatal(err)
	}
	if count != 6 || first != 2 || last != 9 || source != "cli" {
		t.Fatalf("row: count=%d first=%d last=%d source=%q, want 6 2 9 cli", count, first, last, source)
	}
}

func TestStoreWriteSqliteCounterUpsertRules(t *testing.T) {
	assertCounterUpsertRules(t, openStoreQueryDb(t), "daily_counts_w")
}

// Db_writeReturningObjects reads the rows a write returns, and it is a write:
// inside an SSR settle it is suppressed and nothing changes.
func assertWriteReturning(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	cs := cspec("id", "text", "owner", "text?")
	if r := autoMigrate(t, d, table, cs, nil); r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	storeWriteExec(t, d, "INSERT INTO "+table+" (id) VALUES (?)", []any{"j1"})
	claim := "UPDATE " + table + " SET owner = ? WHERE (id = ?) AND (owner IS NULL) RETURNING id, owner"
	groups := []any{T3[any, any, any]{V0: "", V1: "", V2: cs}}
	run := func(owner string) SkyResult[any, any] {
		return AnyTaskRun(Db_writeReturningObjects(d, claim, []any{owner, "j1"}, groups)).(SkyResult[any, any])
	}
	enterSsrSettle()
	r := run("ssr")
	exitSsrSettle()
	if r.Tag == 0 || !strings.Contains(fmt.Sprintf("%v", r.ErrValue), "server-side render") {
		t.Fatalf("a RETURNING write ran inside an SSR settle: %+v", r)
	}
	r = run("me")
	if r.Tag != 0 {
		t.Fatalf("claim: %+v", r)
	}
	rows := AsList(r.OkValue)
	if len(rows) != 1 || AsList(rows[0])[0] != `{"id":"j1","owner":"me"}` {
		t.Fatalf("claim rows: %v", rows)
	}
	if r = run("other"); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second claim: %+v", r)
	}
}

func TestStoreWriteSqliteReturningAndSsrGuard(t *testing.T) {
	assertWriteReturning(t, openStoreQueryDb(t), "jobs_ret")
}

// claimOnce runs one statement from n goroutines at once and returns the
// sum of rows affected.
func claimOnce(t *testing.T, d *SkyDb, n int, sql string, params func(i int) []any) int {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, ok := AnyTaskRun(Db_exec(d, sql, params(i))).(SkyResult[any, any])
			if !ok || r.Tag != 0 {
				errs[i] = fmt.Errorf("%+v", r)
				return
			}
			results[i] = AsInt(r.OkValue)
		}(i)
	}
	close(start)
	wg.Wait()
	total := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		total += results[i]
	}
	return total
}
