package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("# comment\nexport SKY_BLUEDB_URL=https://app.example\nSKY_ADMIN_TOKEN=\"tok-123\"\n"), 0o600)
	kv := loadEnvFile(p)
	if kv["SKY_BLUEDB_URL"] != "https://app.example" {
		t.Fatalf("url: %q", kv["SKY_BLUEDB_URL"])
	}
	if kv["SKY_ADMIN_TOKEN"] != "tok-123" {
		t.Fatalf("token (quotes should strip): %q", kv["SKY_ADMIN_TOKEN"])
	}
}

// Regression: mutations must POST to /data/mutate, reads GET /data. (The bug was
// mutate POSTing to /data, which the read handler answered 200 → silent no-op.)
func TestRemoteRoutesMutateToMutatePath(t *testing.T) {
	var gotPaths []string
	var gotMethods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		gotMethods = append(gotMethods, r.Method)
		if r.Method == http.MethodPost {
			if r.Header.Get("X-Sky-Console") != "1" {
				t.Errorf("mutate missing X-Sky-Console header")
			}
			if r.Header.Get("Authorization") != "Bearer tok-1" {
				t.Errorf("mutate missing bearer: %q", r.Header.Get("Authorization"))
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "op": "put", "key": "k"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"rows": []map[string]string{{"key": "k", "value": "v"}}})
	}))
	defer srv.Close()

	f := flags{url: srv.URL, token: "tok-1", limit: 100}
	var out, errb bytes.Buffer

	// put
	code := runRemote([]string{"/some/store.blue", "put", "k", "v"}, f, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("put code=%d err=%s", code, errb.String())
	}
	// scan
	runRemote([]string{"/some/store.blue", "scan", "k"}, f, strings.NewReader(""), &out, &errb)

	joinedPost := ""
	joinedGet := ""
	for i, p := range gotPaths {
		if gotMethods[i] == http.MethodPost {
			joinedPost = p
		} else {
			joinedGet = p
		}
	}
	if !strings.HasSuffix(joinedPost, "/_sky/console/api/data/mutate") {
		t.Fatalf("put must POST to /data/mutate, got %q", joinedPost)
	}
	if !strings.HasSuffix(joinedGet, "/_sky/console/api/data") {
		t.Fatalf("scan must GET /data, got %q", joinedGet)
	}
}
