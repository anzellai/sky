package rt

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSqlRedactionTokenAware(t *testing.T) {
	redact := []string{"password", "passwd", "pwd", "user_pw", "passphrase", "pin",
		"signing_key", "api_key", "session_token", "secret", "credential", "ssn", "cvv"}
	for _, c := range redact {
		if !isSensitiveCol(c) {
			t.Errorf("column %q must be redacted", c)
		}
	}
	visible := []string{"email", "name", "monkey_id", "keyboard", "id", "created_at",
		"description", "keynote_url"}
	for _, c := range visible {
		if isSensitiveCol(c) {
			t.Errorf("column %q must NOT be redacted (over-redaction)", c)
		}
	}
}

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

	// listSqlSources shows it now — under an OPAQUE HANDLE, never the raw DSN.
	srcs := listSqlSources()
	found := false
	for _, s := range srcs {
		if strings.Contains(s.Name, path) || strings.Contains(s.Label, "s3cret") {
			t.Fatalf("DSN/path must not appear in discovery: name=%q label=%q", s.Name, s.Label)
		}
		if s.Name == sqlSourceHandle(path) {
			found = true
			if len(s.Tables) != 1 || s.Tables[0] != "users" {
				t.Fatalf("tables: %v", s.Tables)
			}
		}
	}
	if !found {
		t.Fatal("sql source not listed after a table was registered")
	}
	// findSqlSource resolves the handle back to the SkyDb.
	if findSqlSource(sqlSourceHandle(path)) == nil {
		t.Fatal("findSqlSource must resolve the opaque handle")
	}
	if findSqlSource("src-deadbeef0000") != nil {
		t.Fatal("findSqlSource must reject an unknown handle")
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

// e2e through HandleConsoleData: discovery lists the SQL source by handle;
// ?sql lists tables; ?sql&table browses redacted rows. Auth applies as to KV.
func TestConsoleDataSqlBrowseEndToEnd(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_CONSOLE_DATA", "readonly")
	t.Setenv("SKY_ADMIN_TOKEN", "tok-123456789012345678901234567890")

	path := filepath.Join(t.TempDir(), "e2e.db")
	driver, dsn := detectDriver(path)
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		t.Skipf("sqlite unavailable: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE members (id TEXT, email TEXT, password TEXT)`); err != nil {
		t.Skipf("create: %v", err)
	}
	conn.Exec(`INSERT INTO members VALUES ('m1','ada@x','topsecret')`)
	d := &SkyDb{conn: conn, name: path, driver: driver}
	dbRegistryMu.Lock()
	dbRegistry[path] = d
	dbRegistryMu.Unlock()
	defer func() {
		dbRegistryMu.Lock()
		delete(dbRegistry, path)
		dbRegistryMu.Unlock()
	}()
	registerBrowsableTable(path, "members")

	bearer := "Bearer tok-123456789012345678901234567890"
	handle := sqlSourceHandle(path)

	// discovery: lists the handle, NEVER the raw path/DSN.
	req := httptest.NewRequest("GET", "/_sky/console/api/data", nil)
	req.Header.Set("Authorization", bearer)
	w := httptest.NewRecorder()
	HandleConsoleData(w, req)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, handle) {
		t.Fatalf("discovery: code=%d body=%s", w.Code, body)
	}
	if strings.Contains(body, path) {
		t.Fatalf("discovery must NOT leak the DSN/path: %s", body)
	}

	// list tables for the source.
	req = httptest.NewRequest("GET", "/_sky/console/api/data?sql="+handle, nil)
	req.Header.Set("Authorization", bearer)
	w = httptest.NewRecorder()
	HandleConsoleData(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "members") {
		t.Fatalf("list tables: code=%d body=%s", w.Code, w.Body.String())
	}

	// browse rows: email visible, password redacted.
	req = httptest.NewRequest("GET", "/_sky/console/api/data?sql="+handle+"&table=members", nil)
	req.Header.Set("Authorization", bearer)
	w = httptest.NewRecorder()
	HandleConsoleData(w, req)
	rb := w.Body.String()
	if w.Code != 200 || !strings.Contains(rb, "ada@x") {
		t.Fatalf("browse rows: code=%d body=%s", w.Code, rb)
	}
	if strings.Contains(rb, "topsecret") {
		t.Fatalf("password leaked in rows: %s", rb)
	}
	if !strings.Contains(rb, `"***"`) {
		t.Fatalf("expected redacted marker: %s", rb)
	}

	// no auth → 401 (SQL path gated like KV).
	req = httptest.NewRequest("GET", "/_sky/console/api/data?sql="+handle+"&table=members", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w = httptest.NewRecorder()
	HandleConsoleData(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no-token SQL browse must be 401, got %d", w.Code)
	}
}
