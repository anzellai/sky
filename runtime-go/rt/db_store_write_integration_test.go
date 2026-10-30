//go:build integration
// +build integration

package rt

// Std.Db.Store writes, PostgreSQL arm (v0.29.0, batch D). Every test name
// carries "Postgres" so the CI integration jobs (`-run Postgres`) run it. The
// concurrency tests open a pool with several connections, so the goroutines
// race on separate PostgreSQL sessions.

import (
	"fmt"
	"testing"
)

func TestStoreWritePostgresCounterUpsertRules(t *testing.T) {
	assertCounterUpsertRules(t, openStoreQueryPostgres(t, ""), "daily_counts_w_pg")
}

func TestStoreWritePostgresReturningAndSsrGuard(t *testing.T) {
	assertWriteReturning(t, openStoreQueryPostgres(t, ""), "jobs_ret_pg")
}

// insertOrIgnore "claim once": 8 sessions insert one key at the same time,
// exactly one writes the row.
func TestStoreWritePostgresInsertOrIgnoreClaimsOnce(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	d.conn.SetMaxOpenConns(8)
	const table = "claims_once_pg"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	if r := autoMigrate(t, d, table, cspec("id", "text", "owner", "text"), nil); r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	// Store.insertOrIgnore renders this text for the store (pk "id").
	sql := "INSERT INTO " + table + " (id, owner) VALUES (?, ?) ON CONFLICT DO NOTHING"
	for round := 0; round < 5; round++ {
		key := fmt.Sprintf("k%d", round)
		won := claimOnce(t, d, 8, sql, func(i int) []any { return []any{key, fmt.Sprintf("o%d", i)} })
		if won != 1 {
			t.Fatalf("round %d: %d claims won, want exactly 1", round, won)
		}
	}
	var n int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 5 {
		t.Fatalf("rows: %d (%v), want 5", n, err)
	}
}

// The counter upsert from 16 sessions at once: every increment lands.
func TestStoreWritePostgresCounterUpsertIsAtomic(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	d.conn.SetMaxOpenConns(16)
	const table = "daily_counts_race_pg"
	cs := storeWriteCounterTable(t, d, table)
	sql := storeWriteCounterSql(table)
	affected := claimOnce(t, d, 16, sql, func(i int) []any {
		return storeWriteCounterValues(cs, 1, i, i, "s")
	})
	if affected != 16 {
		t.Fatalf("rows affected %d, want 16", affected)
	}
	var count, first, last int
	if err := d.conn.QueryRow("SELECT count, first_at, last_at FROM "+table).Scan(&count, &first, &last); err != nil {
		t.Fatal(err)
	}
	if count != 16 || first != 0 || last != 15 {
		t.Fatalf("count=%d first=%d last=%d, want 16 0 15", count, first, last)
	}
}

// updateFieldsReturning "claim": 8 sessions claim one job, exactly one gets
// the row back.
func TestStoreWritePostgresReturningClaimsOnce(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	d.conn.SetMaxOpenConns(8)
	const table = "jobs_claim_pg"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	cs := cspec("id", "text", "owner", "text?")
	if r := autoMigrate(t, d, table, cs, nil); r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id) VALUES ('j1')"); err != nil {
		t.Fatal(err)
	}
	claim := "UPDATE " + table + " SET owner = ? WHERE (id = ?) AND (owner IS NULL) RETURNING id, owner"
	groups := []any{T3[any, any, any]{V0: "", V1: "", V2: cs}}
	results := make(chan int, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			<-start
			r := AnyTaskRun(Db_writeReturningObjects(d, claim, []any{fmt.Sprintf("o%d", i), "j1"}, groups)).(SkyResult[any, any])
			if r.Tag != 0 {
				results <- -100
				return
			}
			results <- len(AsList(r.OkValue))
		}(i)
	}
	close(start)
	total := 0
	for i := 0; i < 8; i++ {
		total += <-results
	}
	if total != 1 {
		t.Fatalf("%d claims got the row, want exactly 1", total)
	}
}
