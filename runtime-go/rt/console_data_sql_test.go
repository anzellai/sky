package rt

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSqlBrowseRedactAndAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	driver, dsn := detectDriver(path)
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE users (id TEXT, email TEXT, password TEXT)`); err != nil {
		t.Skipf("create: %v", err)
	}
	conn.Exec(`INSERT INTO users VALUES ('u1','ada@x','s3cret1')`)
	conn.Exec(`INSERT INTO users VALUES ('u2','lin@x','s3cret2')`)

	d := &SkyDb{conn: conn, name: path, driver: driver}
	dbRegistryMu.Lock()
	dbRegistry[path] = d
	dbRegistryMu.Unlock()
	defer func() {
		dbRegistryMu.Lock()
		delete(dbRegistry, path)
		dbRegistryMu.Unlock()
	}()

	// Default-deny: before registration, the table is NOT browsable.
	if _, err := browseSqlTable(d, "users", 10, 0); err == nil {
		t.Fatal("unregistered table must be denied (default-deny)")
	}
	registerBrowsableTable(path, "users")

	// listSqlSources shows it now.
	srcs := listSqlSources()
	found := false
	for _, s := range srcs {
		if s.Name == path {
			found = true
			if len(s.Tables) != 1 || s.Tables[0] != "users" {
				t.Fatalf("tables: %v", s.Tables)
			}
		}
	}
	if !found {
		t.Fatal("sql source not listed after a table was registered")
	}

	res, err := browseSqlTable(d, "users", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows: %d want 2", len(res.Rows))
	}
	// password column present but REDACTED in every row + listed as redacted.
	pi := -1
	for i, c := range res.Columns {
		if c == "password" {
			pi = i
		}
	}
	if pi < 0 {
		t.Fatal("password column missing")
	}
	for _, row := range res.Rows {
		if row[pi] != "***" {
			t.Fatalf("password not redacted: %q", row[pi])
		}
	}
	inRedacted := false
	for _, c := range res.Redacted {
		if c == "password" {
			inRedacted = true
		}
	}
	if !inRedacted {
		t.Fatal("password not reported as redacted")
	}
	// email is NOT sensitive → shown.
	ei := -1
	for i, c := range res.Columns {
		if c == "email" {
			ei = i
		}
	}
	if res.Rows[0][ei] != "ada@x" && res.Rows[1][ei] != "ada@x" {
		t.Fatalf("email should be visible, got %v", res.Rows)
	}

	// a non-registered table is denied even if it exists.
	conn.Exec(`CREATE TABLE secrets (k TEXT)`)
	if _, err := browseSqlTable(d, "secrets", 10, 0); err == nil {
		t.Fatal("non-allowlisted table must be denied")
	}
}
