package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sky-app/bluedb"
)

func TestCLIVerifyCleanExitsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := bluedb.Open(path, bluedb.Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%02d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// argv order: <path> verify (matches the CLI's positional parsing).
	code, out, errb := runCLI(t, "", path, "verify")
	if code != 0 {
		t.Fatalf("verify clean: exit=%d want 0 (stderr=%s)", code, errb)
	}
	if !strings.Contains(out, "clean") {
		t.Fatalf("verify clean: output missing \"clean\": %q", out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("verify clean: output missing \"OK\": %q", out)
	}
}

func TestCLIVerifyCorruptExitsNonZeroWithOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := bluedb.Open(path, bluedb.Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%04d", i)), []byte(fmt.Sprintf("val-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), orig...)
	corrupt[len(corrupt)/2] ^= 0xFF // middle byte → valid records before + after
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errb := runCLI(t, "", path, "verify")
	if code == 0 {
		t.Fatalf("verify corrupt: exit=0, want non-zero (out=%s)", out)
	}
	if !strings.Contains(out, "corruption") {
		t.Fatalf("verify corrupt: output missing \"corruption\": %q", out)
	}
	if !strings.Contains(out, "first bad offset") {
		t.Fatalf("verify corrupt: output missing \"first bad offset\": %q", out)
	}
	if !strings.Contains(errb, "NOT OK") {
		t.Fatalf("verify corrupt: stderr missing \"NOT OK\": %q", errb)
	}
}

func TestCLIVerifyJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := bluedb.Open(path, bluedb.Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Put([]byte("a"), []byte("1"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, errb := runCLI(t, "", path, "verify", "--json")
	if code != 0 {
		t.Fatalf("verify --json: exit=%d want 0 (stderr=%s)", code, errb)
	}
	if !strings.Contains(out, `"WalStatus": "clean"`) {
		t.Fatalf("verify --json: missing WalStatus clean: %q", out)
	}
	if !strings.Contains(out, `"OK": true`) {
		t.Fatalf("verify --json: missing OK true: %q", out)
	}
}
