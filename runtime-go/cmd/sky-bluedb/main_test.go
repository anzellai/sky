package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"sky-app/bluedb"
)

func runCLI(t *testing.T, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(argv, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLICrudRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.blue")

	if code, _, e := runCLI(t, "", path, "put", "user:1", `{"name":"Ada"}`); code != 0 {
		t.Fatalf("put: code=%d err=%s", code, e)
	}
	runCLI(t, "", path, "put", "user:2", `{"name":"Lin"}`)
	runCLI(t, "", path, "put", "order:1", "widget")

	// keys with prefix, sorted + isolated
	code, out, _ := runCLI(t, "", path, "keys", "user:")
	if code != 0 || out != "user:1\nuser:2\n" {
		t.Fatalf("keys user: code=%d out=%q", code, out)
	}

	// get --json parses stored JSON (not double-escaped)
	_, out, _ = runCLI(t, "", path, "get", "user:1", "--json")
	if !strings.Contains(out, `"name": "Ada"`) {
		t.Fatalf("get --json: %q", out)
	}

	// delete with --yes
	if code, _, _ := runCLI(t, "", path, "delete", "order:1", "--yes"); code != 0 {
		t.Fatalf("delete code=%d", code)
	}
	_, out, _ = runCLI(t, "", path, "keys")
	if strings.Contains(out, "order:1") {
		t.Fatalf("order:1 should be gone: %q", out)
	}

	// missing key → exit 4
	if code, _, _ := runCLI(t, "", path, "get", "nope"); code != 4 {
		t.Fatalf("missing get want exit 4, got %d", code)
	}
}

func TestCLIDeleteAbortsWithoutYes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.blue")
	runCLI(t, "", path, "put", "k", "v")
	// stdin "n" → abort, key survives
	code, out, _ := runCLI(t, "n\n", path, "delete", "k")
	if code != 0 || !strings.Contains(out, "aborted") {
		t.Fatalf("delete no-confirm: code=%d out=%q", code, out)
	}
	if c, _, _ := runCLI(t, "", path, "get", "k"); c != 0 {
		t.Fatalf("key must survive an aborted delete, got exit %d", c)
	}
}

func TestCLIRejectsLiveStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.blue")
	// Simulate a running app: hold the store open (exclusive lock).
	db, err := bluedb.Open(path, bluedb.Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	code, _, errOut := runCLI(t, "", path, "stats")
	if code != 3 {
		t.Fatalf("live store want exit 3, got %d (err=%s)", code, errOut)
	}
	if !strings.Contains(errOut, "running app") {
		t.Fatalf("expected actionable lock message, got %q", errOut)
	}
}

func TestCLIBinaryValueNotCorrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.blue")
	// A non-UTF8 value must not be printed as garbage/invalid.
	db, _ := bluedb.Open(path, bluedb.Options{Sync: false})
	_ = db.Put([]byte("blob"), []byte{0xff, 0xfe, 0x00, 0x01})
	db.Close()

	_, out, _ := runCLI(t, "", path, "get", "blob")
	if !strings.Contains(out, "<binary 4 bytes>") {
		t.Fatalf("binary value should render as marker, got %q", out)
	}
	_, out, _ = runCLI(t, "", path, "get", "blob", "--raw")
	if strings.TrimSpace(out) != "fffe0001" {
		t.Fatalf("--raw should hex-dump, got %q", out)
	}
}
