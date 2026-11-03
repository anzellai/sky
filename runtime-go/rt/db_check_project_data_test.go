//go:build !js

package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

// `sky db check` said "up to date" (exit 0) on a SQLite database whose TEXT
// column held Codec.auto Money JSON read by Codec.moneyIn GBP, while the
// app's Store.migrate refused the same database: the check compared only
// the column type (v0.29.2 third Judge). Db_checkProjectData runs the
// refusal Store.migrate runs (codecStoreDataRefusal) and gives back its
// text and SQL, so the two cannot disagree.
func assertCheckProjectDataMatchesMigrate(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	moneyExec(t, d,
		"CREATE TABLE "+table+" (id TEXT PRIMARY KEY NOT NULL, cost TEXT NOT NULL)",
		"INSERT INTO "+table+` VALUES ('a', '{"amount":"10.00","currency":"GBP"}')`,
	)
	cs := []any{
		T2[any, any]{V0: "id", V1: "text"},
		T2[any, any]{V0: "cost", V1: "decimal|money=GBP"},
	}
	tables := []any{
		map[string]any{"Name": table, "Pk": "id", "Cols": cs},
		// A table the database does not have yet: migrate creates it.
		map[string]any{"Name": table + "_new", "Pk": "id", "Cols": cs},
	}
	res := AnyTaskRun(Db_checkProjectData(d, tables)).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Db_checkProjectData: %s", errorMessage(res.ErrValue))
	}
	var got []struct {
		Table, Column, Message, Check, SQL string
	}
	if err := json.Unmarshal([]byte(res.OkValue.(string)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Table != table || got[0].Column != "cost" {
		t.Fatalf("findings = %+v, want one for %s.cost", got, table)
	}
	mig := legacyMigrate(t, d, table, cs)
	if mig.Tag == 0 {
		t.Fatalf("Store.migrate accepted the column the check refuses")
	}
	if want := errorMessage(mig.ErrValue); got[0].Message != want {
		t.Fatalf("the check's text differs from Store.migrate's:\n check:   %s\n migrate: %s", got[0].Message, want)
	}
	if got[0].SQL == "" || !strings.Contains(got[0].Message, got[0].SQL) || got[0].Check == "" {
		t.Fatalf("the finding does not carry the conversion the message names: %+v", got[0])
	}
	// The conversion it gives makes both agree the column is right.
	moneyExec(t, d, got[0].SQL)
	res = AnyTaskRun(Db_checkProjectData(d, tables)).(SkyResult[any, any])
	if res.Tag != 0 || res.OkValue.(string) != "[]" {
		t.Fatalf("after the conversion the check still reports: %v", res)
	}
	if mig = legacyMigrate(t, d, table, cs); mig.Tag != 0 {
		t.Fatalf("after the conversion Store.migrate refuses: %s", errorMessage(mig.ErrValue))
	}
}

func TestCheckProjectDataMatchesMigrateSqlite(t *testing.T) {
	assertCheckProjectDataMatchesMigrate(t, openStoreQueryDb(t), "check_data")
}
