package rt

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Every Sky.Core.File kernel maps an os error to one Sky error kind: a
// missing path is NotFound, a refused one PermissionDenied, an existing one
// Conflict, as File.stat does. v0.29.0 wrapped remove, readDir, mkdirAll,
// readFile, readFileBytes, writeFile, writeBytes and append with Ffi.
func TestFileKernelsMapOsErrorKinds(t *testing.T) {
	dir := realDir(t)
	missing := filepath.Join(dir, "nope", "x")
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	notFound := map[string]any{
		"remove":        File_remove(missing),
		"readDir":       File_readDir(missing),
		"readFile":      File_readFile(missing),
		"readFileLimit": File_readFileLimit(missing, 10),
		"readFileBytes": File_readFileBytes(missing),
		"writeFile":     File_writeFile(missing, "x"),
		"writeBytes":    File_writeBytes(missing, "x"),
		"append":        File_append(missing, "x"),
		"stat":          File_stat(missing),
		"copy":          File_copy(missing, filepath.Join(dir, "c")),
		"rename":        File_rename(missing, filepath.Join(dir, "r")),
	}
	for name, task := range notFound {
		_, e, ok := runFileTask(t, task)
		if ok {
			t.Errorf("File.%s of a missing path succeeded", name)
			continue
		}
		if k := errKindOf(e); k != "NotFound" {
			t.Errorf("File.%s of a missing path: kind %q, want NotFound (%v)", name, k, e)
		}
	}
	// mkdirAll under a regular file: the parent exists and is not a directory.
	_, e, ok := runFileTask(t, File_mkdirAll(filepath.Join(file, "sub")))
	if ok {
		t.Fatal("File.mkdirAll under a file succeeded")
	}
	if k := errKindOf(e); k == "FFI" {
		t.Errorf("File.mkdirAll under a file: kind FFI, want a file kind (%v)", e)
	}
	// readDir of a regular file is not Ffi either.
	if _, e, ok := runFileTask(t, File_readDir(file)); ok || errKindOf(e) == "FFI" {
		t.Errorf("File.readDir of a file: ok=%v kind %q, want an Io-family kind", ok, errKindOf(e))
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	denied := map[string]any{
		"readDir":   File_readDir(locked),
		"writeFile": File_writeFile(filepath.Join(locked, "a"), "x"),
		"mkdirAll":  File_mkdirAll(filepath.Join(locked, "a")),
		"remove":    File_remove(filepath.Join(locked, "a")),
	}
	for name, task := range denied {
		_, e, ok := runFileTask(t, task)
		if ok {
			t.Errorf("File.%s in a locked directory succeeded", name)
			continue
		}
		if k := errKindOf(e); k != "PermissionDenied" {
			t.Errorf("File.%s in a locked directory: kind %q, want PermissionDenied (%v)", name, k, e)
		}
	}
}

// An os error that says the path already exists maps to Conflict.
func TestFileErrorExistIsConflict(t *testing.T) {
	if k := errKindOf(fileError("File.x", os.ErrExist)); k != "Conflict" {
		t.Errorf("fs.ErrExist: kind %q, want Conflict", k)
	}
}
