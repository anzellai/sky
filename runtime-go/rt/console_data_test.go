package rt

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"sky-app/bluedb"
)

// registerTestStore opens a bluedb store and puts it in the app-store registry,
// as BlueDB_open would. Returns the path + a cleanup.
func registerTestStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := bluedb.Open(path, bluedb.Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Put([]byte("user:1"), []byte(`{"name":"Ada"}`))
	_ = db.Put([]byte("user:2"), []byte(`{"name":"Lin"}`))
	id := bluedbNextID.Add(1)
	bluedbRegMu.Lock()
	bluedbRegistry[id] = &bluedbEntry{db: db, path: path}
	bluedbByPath[path] = id
	bluedbRegMu.Unlock()
	t.Cleanup(func() {
		bluedbRegMu.Lock()
		delete(bluedbRegistry, id)
		delete(bluedbByPath, path)
		bluedbRegMu.Unlock()
		_ = db.Close()
	})
	return path
}

// F1 — the loopback IP must NOT bypass auth on the data endpoint.
func TestDataEndpointNoLoopbackBypass(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_CONSOLE_DATA", "readonly")
	t.Setenv("SKY_ADMIN_TOKEN", "s3cret-admin-token-of-sufficient-length")

	req := httptest.NewRequest("GET", "/_sky/console/api/data", nil)
	req.RemoteAddr = "127.0.0.1:54321" // loopback peer (e.g. behind a proxy)
	w := httptest.NewRecorder()
	HandleConsoleData(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("loopback with no token must be 401, got %d", w.Code)
	}
}

func TestDataEndpointReadWithToken(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_CONSOLE_DATA", "readonly")
	t.Setenv("SKY_ADMIN_TOKEN", "tok-123456789012345678901234567890")
	path := registerTestStore(t)

	// list stores
	req := httptest.NewRequest("GET", "/_sky/console/api/data", nil)
	req.Header.Set("Authorization", "Bearer tok-123456789012345678901234567890")
	w := httptest.NewRecorder()
	HandleConsoleData(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), path) {
		t.Fatalf("list stores: code=%d body=%s", w.Code, w.Body.String())
	}

	// scan
	req = httptest.NewRequest("GET", "/_sky/console/api/data?store="+path+"&prefix=user:", nil)
	req.Header.Set("Authorization", "Bearer tok-123456789012345678901234567890")
	w = httptest.NewRecorder()
	HandleConsoleData(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "user:1") || !strings.Contains(w.Body.String(), "Ada") {
		t.Fatalf("scan: code=%d body=%s", w.Code, w.Body.String())
	}
}

// F3 — writes disabled unless readwrite, even with a valid token.
func TestDataMutateRequiresReadwrite(t *testing.T) {
	t.Setenv("SKY_CONSOLE_DATA", "readonly") // NOT readwrite
	t.Setenv("SKY_ADMIN_TOKEN", "tok-123456789012345678901234567890")
	path := registerTestStore(t)
	req := httptest.NewRequest("POST", "/_sky/console/api/data/mutate",
		strings.NewReader(`{"store":"`+path+`","op":"put","key":"x","value":"1"}`))
	req.Header.Set("Authorization", "Bearer tok-123456789012345678901234567890")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sky-Console", "1")
	w := httptest.NewRecorder()
	HandleConsoleDataMutate(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("mutate in readonly must 404, got %d", w.Code)
	}
}

// F2 — mutate needs the X-Sky-Console header (blocks cross-site form POST).
func TestDataMutateRequiresCustomHeader(t *testing.T) {
	t.Setenv("SKY_CONSOLE_DATA", "readwrite")
	t.Setenv("SKY_ADMIN_TOKEN", "tok-123456789012345678901234567890")
	path := registerTestStore(t)
	req := httptest.NewRequest("POST", "/_sky/console/api/data/mutate",
		strings.NewReader(`{"store":"`+path+`","op":"put","key":"x","value":"1"}`))
	req.Header.Set("Authorization", "Bearer tok-123456789012345678901234567890")
	req.Header.Set("Content-Type", "application/json")
	// no X-Sky-Console header
	w := httptest.NewRecorder()
	HandleConsoleDataMutate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mutate without X-Sky-Console must 400, got %d", w.Code)
	}
}

func TestDataMutatePutDeleteRoundTrip(t *testing.T) {
	t.Setenv("SKY_CONSOLE_DATA", "readwrite")
	t.Setenv("SKY_ADMIN_TOKEN", "tok-123456789012345678901234567890")
	path := registerTestStore(t)
	auth := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer tok-123456789012345678901234567890")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Sky-Console", "1")
	}
	// put
	req := httptest.NewRequest("POST", "/_sky/console/api/data/mutate",
		strings.NewReader(`{"store":"`+path+`","op":"put","key":"cfg:1","value":"hello"}`))
	auth(req)
	w := httptest.NewRecorder()
	HandleConsoleDataMutate(w, req)
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	// verify via read
	entry := findBluedbStore(path)
	if v, ok := entry.db.Get([]byte("cfg:1")); !ok || string(v) != "hello" {
		t.Fatalf("put not applied: %q %v", v, ok)
	}
	// delete
	req = httptest.NewRequest("POST", "/_sky/console/api/data/mutate",
		strings.NewReader(`{"store":"`+path+`","op":"delete","key":"cfg:1"}`))
	auth(req)
	w = httptest.NewRecorder()
	HandleConsoleDataMutate(w, req)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, ok := entry.db.Get([]byte("cfg:1")); ok {
		t.Fatal("delete not applied")
	}
}
