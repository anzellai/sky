package rt

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDbConnectAppliesConcurrencyDefaults — regression for v0.17.10.
// Db_connect must apply SQLite concurrency defaults (WAL journal +
// busy_timeout + single-conn pool) automatically so user-level
// Db.exec calls don't hit SQLITE_BUSY under concurrent goroutines.
func TestDbConnectAppliesConcurrencyDefaults(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "concurrency.db")

	db := unwrapDbConnect(t, dbPath)
	defer func() {
		if db != nil && db.conn != nil {
			_ = db.conn.Close()
		}
		dbRegistryMu.Lock()
		delete(dbRegistry, dbPath)
		dbRegistryMu.Unlock()
	}()

	var mode, timeout string
	if err := db.conn.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode read failed: %v", err)
	}
	if strings.ToLower(mode) != "wal" {
		t.Errorf("journal_mode: want wal, got %q", mode)
	}
	if err := db.conn.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout read failed: %v", err)
	}
	if timeout != "5000" {
		t.Errorf("busy_timeout: want 5000, got %q", timeout)
	}

	stats := db.conn.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Errorf("SetMaxOpenConns: want 1, got %d", stats.MaxOpenConnections)
	}

	if _, err := db.conn.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, v INTEGER)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// Concurrent-writer stress — 20 goros × 10 INSERTs. Pre-fix, a
	// large fraction of these fired SQLITE_BUSY. Post-fix: zero.
	const nGoros = 20
	const nInserts = 10
	var wg sync.WaitGroup
	errCh := make(chan error, nGoros*nInserts)
	for g := 0; g < nGoros; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < nInserts; i++ {
				id := insertKey(g, i)
				if _, err := db.conn.Exec(`INSERT INTO t (id, v) VALUES (?, ?)`, id, g*1000+i); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)

	busyCount := 0
	var firstErr error
	for err := range errCh {
		if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
			busyCount++
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if busyCount > 0 {
		t.Fatalf("SQLITE_BUSY leaked: %d writes affected; first err: %v", busyCount, firstErr)
	}
	if firstErr != nil {
		t.Fatalf("unexpected non-BUSY error: %v", firstErr)
	}

	var total int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&total); err != nil {
		t.Fatalf("COUNT read failed: %v", err)
	}
	if total != nGoros*nInserts {
		t.Errorf("row count: want %d, got %d", nGoros*nInserts, total)
	}
}

// In-memory DBs may reject WAL. Fix must still return Ok (PRAGMA
// failure is Log_warn, not abort).
func TestDbConnectMemoryDbSkipsPragmaFailure(t *testing.T) {
	db := unwrapDbConnect(t, ":memory:")
	defer func() {
		if db != nil && db.conn != nil {
			db.conn.Close()
		}
		dbRegistryMu.Lock()
		delete(dbRegistry, ":memory:")
		dbRegistryMu.Unlock()
	}()

	if _, err := db.conn.Exec("SELECT 1"); err != nil {
		t.Fatalf("in-memory DB usable: %v", err)
	}
}

// Helper to force + unwrap Db_connect's Task-shaped return.
func unwrapDbConnect(t *testing.T, path string) *SkyDb {
	t.Helper()
	res := Db_connect(path)
	fn, ok := res.(func() any)
	if !ok {
		t.Fatalf("Db_connect returned unexpected shape: %T", res)
	}
	got := fn()
	sr, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_connect: expected SkyResult, got %T", got)
	}
	if sr.Tag != 0 {
		t.Fatalf("Db_connect Err: %v", sr.ErrValue)
	}
	db, ok := sr.OkValue.(*SkyDb)
	if !ok {
		t.Fatalf("Db_connect Ok payload not *SkyDb: %T", sr.OkValue)
	}
	return db
}

func insertKey(g, i int) string {
	return "g" + intStr(g) + "-i" + intStr(i)
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
