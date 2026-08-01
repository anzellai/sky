package rt

import (
	"testing"
)

// B2 regression — Db.connect must NOT freeze the memoised `db` handle to Err on
// a transient boot-time connectivity failure.
//
// `db = Task.run (Db.connect ())` is a compiler LazyCaf: it caches the FIRST
// Result forever. Pre-fix, Db_connect eagerly Ping()'d and returned Err on
// failure, so a boot race (systemd `After=postgresql` = unit started, not
// accepting) froze the handle to Err → every query returned it → the app served
// broken pages until a MANUAL restart. `database/sql` is a self-healing lazy
// pool; the eager Ping was defeating it. Post-fix, an unreachable DB at boot
// returns Ok(pool) with a warning — the next query self-heals once the DB is up.
func TestDbConnectSelfHealsOnUnreachable(t *testing.T) {
	// Isolate the global readiness-probe list (Db_connect registers a "db"
	// probe) so we don't leak a dangling failing probe into other tests.
	saved := readinessProbes.Load()
	defer readinessProbes.Store(saved)

	// Well-formed but unreachable: sql.Open succeeds (lazy), the boot Ping
	// fails fast (port 1 → connection refused). connect_timeout bounds it.
	dsn := "postgres://u:p@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1"
	defer func() {
		dbRegistryMu.Lock()
		delete(dbRegistry, dsn)
		dbRegistryMu.Unlock()
	}()

	res := Db_connect(dsn)
	fn, ok := res.(func() any)
	if !ok {
		t.Fatalf("Db_connect shape: %T", res)
	}
	got, ok := fn().(SkyResult[any, any])
	if !ok {
		t.Fatalf("forced Db_connect: %T", fn())
	}
	if got.Tag != 0 {
		t.Fatalf("unreachable DB should return Ok(healable pool), got Err: %v", got.ErrValue)
	}
	if _, ok := got.OkValue.(*SkyDb); !ok {
		t.Fatalf("Ok payload should be *SkyDb (a live pool), got %T", got.OkValue)
	}
}
