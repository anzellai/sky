package rt

// persist_writeskew_test.go — the discriminating write-skew e2e for BlueDB
// Phase-3 A1/A2/A3. It runs the classic "on-call doctors" write-skew invariant
// (two writers each read the count of on-call doctors and, if ≥ 2, take
// themselves off call) concurrently on all three backends:
//
//   • embedded (bluedb EmbeddedBackend) — real SSI → invariant HELD.
//   • SQLite   — BEGIN IMMEDIATE + SetMaxOpenConns(1) → invariant HELD. (SQLite
//     cannot DISCRIMINATE isolation here because MaxOpenConns(1) already
//     serializes the two transactions onto one connection — the A2 finding; the
//     serializable path still issues BEGIN IMMEDIATE as the STATED mechanism.)
//   • Postgres — the DISCRIMINATOR: under READ COMMITTED (bare Begin) the
//     invariant is VIOLATED (0 on-call); under our serializable path
//     (BeginTx SERIALIZABLE + 40001 retry) it is HELD (≥ 1 on-call). Gated on a
//     reachable pg via SKY_TEST_PG_URL / DATABASE_URL.
//
// The test is DISCRIMINATING by construction: TestWriteSkewPostgres asserts the
// READ COMMITTED baseline FAILS (invariant violated) and the serializable path
// PASSES — so a regression that silently dropped SERIALIZABLE back to the driver
// default would make the pg test fail.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sky-app/bluedb"
)

// ── shared helpers ────────────────────────────────────────────────────────────

func forceDbConnect(t *testing.T, path string) *SkyDb {
	t.Helper()
	thunk, ok := Db_connect(path).(func() any)
	if !ok {
		t.Fatalf("Db_connect did not return a thunk for %q", path)
	}
	res := thunk()
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("Db_connect(%q) failed: %#v", path, res)
	}
	d, ok := sr.OkValue.(*SkyDb)
	if !ok {
		t.Fatalf("Db_connect(%q) returned non-*SkyDb", path)
	}
	return d
}

func mustExec(t *testing.T, d *SkyDb, q string) {
	t.Helper()
	if _, err := d.conn.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// resetDoctors builds a fresh 2-doctor table, both on call. `on_call INT` (0/1)
// is dialect-agnostic (works verbatim on SQLite AND Postgres).
func resetDoctors(t *testing.T, d *SkyDb) {
	t.Helper()
	mustExec(t, d, "DROP TABLE IF EXISTS doctors")
	mustExec(t, d, "CREATE TABLE doctors (name TEXT PRIMARY KEY, on_call INT)")
	mustExec(t, d, "INSERT INTO doctors (name, on_call) VALUES ('alice', 1)")
	mustExec(t, d, "INSERT INTO doctors (name, on_call) VALUES ('bob', 1)")
}

func onCallCount(t *testing.T, d *SkyDb) int {
	t.Helper()
	var n int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM doctors WHERE on_call = 1").Scan(&n); err != nil {
		t.Fatalf("count on-call: %v", err)
	}
	return n
}

// doctorBody is one transaction body: read the on-call count, pause to widen the
// concurrency window, and (only if someone else is on call) go off call.
func doctorBody(name string) func(*SkyDb) any {
	return func(txDb *SkyDb) any {
		ex := txDb.executor()
		var cnt int
		if err := ex.QueryRow("SELECT COUNT(*) FROM doctors WHERE on_call = 1").Scan(&cnt); err != nil {
			return Err[any, any](ErrFfi(err.Error()))
		}
		time.Sleep(60 * time.Millisecond) // both read stale before either writes
		if cnt >= 2 {
			if _, err := ex.Exec("UPDATE doctors SET on_call = 0 WHERE name = '" + name + "'"); err != nil {
				return Err[any, any](ErrFfi(err.Error()))
			}
		}
		return Ok[any, any](nil)
	}
}

// runTwoWriters runs alice + bob concurrently under the given isolation.
func runTwoWriters(d *SkyDb, serializable bool) {
	var wg sync.WaitGroup
	for _, name := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if serializable {
				dbWithSerializableTransactionCore(d, doctorBody(n))
			} else {
				dbSerializableTxAttempt(d, false, doctorBody(n)) // READ COMMITTED baseline, no retry
			}
		}(name)
	}
	wg.Wait()
}

// ── SQLite ────────────────────────────────────────────────────────────────────

func TestWriteSkewSQLiteSerializableHolds(t *testing.T) {
	d := forceDbConnect(t, filepath.Join(t.TempDir(), "ws_ser.db"))
	resetDoctors(t, d)
	runTwoWriters(d, true)
	if got := onCallCount(t, d); got < 1 {
		t.Fatalf("write-skew: invariant VIOLATED on SQLite serializable — %d on-call (want ≥ 1)", got)
	}
}

// SQLite's serialization comes from SetMaxOpenConns(1) (the A2 finding), NOT from
// the isolation mode — so even the READ COMMITTED baseline holds here. Documented
// explicitly so the mechanism is not mistaken for isolation; the discriminating
// isolation proof lives in TestWriteSkewPostgres.
func TestWriteSkewSQLiteReadCommittedAlsoHolds_MaxConns1(t *testing.T) {
	d := forceDbConnect(t, filepath.Join(t.TempDir(), "ws_rc.db"))
	resetDoctors(t, d)
	runTwoWriters(d, false)
	if got := onCallCount(t, d); got < 1 {
		t.Fatalf("unexpected: SQLite MaxOpenConns(1) should serialize both writers — got %d on-call", got)
	}
}

// ── Postgres (the discriminator) ──────────────────────────────────────────────

func pgTestURL() string {
	if u := os.Getenv("SKY_TEST_PG_URL"); u != "" {
		return u
	}
	return os.Getenv("DATABASE_URL")
}

func TestWriteSkewPostgres(t *testing.T) {
	url := pgTestURL()
	if url == "" {
		t.Skip("no SKY_TEST_PG_URL / DATABASE_URL — Postgres write-skew discrimination skipped")
	}
	d := forceDbConnect(t, url)

	// (1) READ COMMITTED baseline — write-skew VIOLATES the invariant (proves the
	//     test discriminates isolation levels).
	resetDoctors(t, d)
	runTwoWriters(d, false)
	rc := onCallCount(t, d)

	// (2) Serializable path — the invariant HOLDS (one writer retries → sees the
	//     other's commit → guard fails → stays on call).
	resetDoctors(t, d)
	runTwoWriters(d, true)
	ser := onCallCount(t, d)

	if rc != 0 {
		t.Fatalf("expected READ COMMITTED to write-skew to 0 on-call (discriminator), got %d", rc)
	}
	if ser < 1 {
		t.Fatalf("expected SERIALIZABLE to HOLD the invariant (≥ 1 on-call), got %d", ser)
	}
	t.Logf("pg discrimination proven: READ COMMITTED → %d on-call (violated), SERIALIZABLE → %d on-call (held)", rc, ser)
}

// ── Embedded (bluedb SSI) ─────────────────────────────────────────────────────

func doctorsEmbeddedSchema() bluedb.CollSchema {
	return bluedb.CollSchema{
		Name: "doctors",
		ID:   bluedb.CollID(1),
		Key:  "name",
		Cols: []bluedb.ColSpec{
			{Name: "name", Type: bluedb.ColText},
			{Name: "on_call", Type: bluedb.ColInt},
		},
		Indexes: []bluedb.IndexSpec{
			{ID: bluedb.IndexID(1), Name: "on_call", Col: "on_call", Type: bluedb.ColInt},
		},
		Generated: map[string]bool{},
	}
}

func onCallEmbeddedPlan() bluedb.QueryPlan {
	return bluedb.QueryPlan{
		Where: bluedb.CondNode{Op: bluedb.CondEq, Col: "on_call", Type: bluedb.ColInt, Val: bluedb.IntVal(1)},
		Limit: -1,
	}
}

func TestWriteSkewEmbeddedHolds(t *testing.T) {
	eng, err := bluedb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer eng.Close()
	b := bluedb.NewEmbeddedBackend(eng)
	coll := doctorsEmbeddedSchema()

	for _, n := range []string{"alice", "bob"} {
		if _, err := b.Insert(coll, []byte(`{"name":"`+n+`","on_call":1}`), nil); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}

	var wg sync.WaitGroup
	for _, n := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_ = b.Transaction(func(tx bluedb.TxHandle) error {
				rows, err := tx.Query(coll, onCallEmbeddedPlan())
				if err != nil {
					return err
				}
				time.Sleep(40 * time.Millisecond)
				if len(rows) >= 2 {
					return tx.Put(coll, name, []byte(`{"name":"`+name+`","on_call":0}`), nil)
				}
				return nil
			})
		}(n)
	}
	wg.Wait()

	rows, err := b.Query(coll, onCallEmbeddedPlan())
	if err != nil {
		t.Fatalf("final count: %v", err)
	}
	if len(rows) < 1 {
		t.Fatalf("write-skew: invariant VIOLATED on embedded SSI — %d on-call (want ≥ 1)", len(rows))
	}
}
