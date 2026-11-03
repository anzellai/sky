//go:build integration
// +build integration

package rt

import (
	"net/url"
	"strings"
	"testing"
)

// v0.29.2 fourth Judge, gap 1, PostgreSQL arm: read-only mode sets
// default_transaction_read_only=on for the session. A DSN may carry a setting
// of its own (`options=-c default_transaction_read_only=off`, or the key
// itself); the read-only guard must win, so a write still fails.
func TestReadOnlyPostgresDsnCannotTurnTheGuardOff(t *testing.T) {
	base := requirePostgresDSN(t)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	t.Setenv("SKY_READ_ONLY_DB", "1")
	for i, extra := range []string{
		"options=" + url.QueryEscape("-c default_transaction_read_only=off"),
		"default_transaction_read_only=off",
		"options=" + url.QueryEscape("-c transaction_read_only=off"),
	} {
		dsn := base + sep + extra
		res := Db_connect(dsn).(func() any)()
		r := res.(SkyResult[any, any])
		if r.Tag != 0 {
			// A refusal is an acceptable guard too, when it says why.
			msg := errorMessage(r.ErrValue)
			if !strings.Contains(msg, "read-only") {
				t.Errorf("case %d: refused without naming read-only mode: %s", i, msg)
			}
			continue
		}
		db := r.OkValue.(*SkyDb)
		_, err := db.conn.Exec("CREATE TABLE sky_ro_guard_probe (x INT)")
		if err == nil {
			_, _ = db.conn.Exec("DROP TABLE sky_ro_guard_probe")
			t.Errorf("case %d (%s): a write ran on a read-only connection", i, extra)
		}
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dsn)
		dbRegistryMu.Unlock()
	}
}

// Gap 4, PostgreSQL arm: 20,000 rows of 5 columns is 100,000 bound
// parameters, above PostgreSQL's 65,535.
func TestStoreInsertManyChunksAboveThePostgresParameterLimit(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	insertManyChunkCase(t, d, "many_pg_4", 10000, 4)
	insertManyChunkCase(t, d, "many_pg_5", 20000, 5)
}
