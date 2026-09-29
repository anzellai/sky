package rt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runFileTask runs a Sky.Core.File kernel's thunk and splits its Result.
func runFileTask(t *testing.T, task any) (any, any, bool) {
	t.Helper()
	f, ok := task.(func() any)
	if !ok {
		t.Fatalf("a File kernel must return a Task thunk, got %T", task)
	}
	r, ok := f().(SkyResult[any, any])
	if !ok {
		t.Fatalf("a File Task must yield a Result")
	}
	if r.Tag == 0 {
		return r.OkValue, nil, true
	}
	return nil, r.ErrValue, false
}

func errKindOf(e any) string {
	s, _ := renderSkyError(e)
	k, _, _ := strings.Cut(s, ":")
	return k
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks need a privilege on Windows: %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
}

// realDir is t.TempDir() with its own symlinks resolved (macOS: /private/var).
func realDir(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFileStatLstatChmodReadLink(t *testing.T) {
	dir := realDir(t)
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Minute).UnixMilli()

	v, e, ok := runFileTask(t, File_stat(f))
	if !ok {
		t.Fatalf("stat: %v", e)
	}
	info := v.(map[string]any)
	if info["kind"] != fileKindFile || info["size"] != 5 {
		t.Errorf("stat file = %v", info)
	}
	if m := info["modified"].(int); m < int(before) {
		t.Errorf("modified %d is not Unix milliseconds of now", m)
	}
	if runtime.GOOS != "windows" && info["mode"] != 0o640 {
		t.Errorf("mode = %o, want 640", info["mode"])
	}

	if _, e, ok := runFileTask(t, File_chmod(0o600, f)); !ok {
		t.Fatalf("chmod: %v", e)
	}
	v, _, _ = runFileTask(t, File_stat(f))
	if runtime.GOOS != "windows" && v.(map[string]any)["mode"] != 0o600 {
		t.Errorf("mode after chmod = %o", v.(map[string]any)["mode"])
	}
	if _, e, ok := runFileTask(t, File_chmod(0o10000, f)); ok || errKindOf(e) != "InvalidInput" {
		t.Errorf("an out-of-range mode is InvalidInput, got %v", e)
	}

	v, _, _ = runFileTask(t, File_stat(dir))
	if v.(map[string]any)["kind"] != fileKindDirectory {
		t.Errorf("stat dir kind = %v", v)
	}

	link := filepath.Join(dir, "l")
	symlinkOrSkip(t, "a.txt", link)
	v, _, _ = runFileTask(t, File_stat(link))
	if v.(map[string]any)["kind"] != fileKindFile {
		t.Errorf("stat follows the link: %v", v)
	}
	v, _, _ = runFileTask(t, File_lstat(link))
	if v.(map[string]any)["kind"] != fileKindSymlink {
		t.Errorf("lstat describes the link: %v", v)
	}
	v, e, ok = runFileTask(t, File_readLink(link))
	if !ok || v != "a.txt" {
		t.Errorf("readLink = %v %v", v, e)
	}
	if _, e, ok := runFileTask(t, File_readLink(f)); ok || errKindOf(e) == "" {
		t.Errorf("readLink of a regular file is an error, got %v", e)
	}
	v, e, ok = runFileTask(t, File_realPath(link))
	if !ok || v != f {
		t.Errorf("realPath(link) = %v %v, want %s", v, e, f)
	}
	if _, e, ok := runFileTask(t, File_stat(filepath.Join(dir, "nope"))); ok || errKindOf(e) != "NotFound" {
		t.Errorf("stat of a missing path is NotFound, got %v", e)
	}
	if _, e, ok := runFileTask(t, File_realPath(filepath.Join(dir, "nope"))); ok || errKindOf(e) != "NotFound" {
		t.Errorf("realPath of a missing path is NotFound, got %v", e)
	}
}

// resolveWithin is the confinement helper: a path, after every symlink and
// `..` is followed, must stay inside the root.
func TestFileResolveWithin(t *testing.T) {
	base := realDir(t)
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, "escape"))                     // absolute, out
	symlinkOrSkip(t, "../outside", filepath.Join(root, "escape2"))               // relative, out
	symlinkOrSkip(t, "sub", filepath.Join(root, "inside"))                       // relative, in
	symlinkOrSkip(t, filepath.Join(root, "sub"), filepath.Join(outside, "back")) // out → in
	symlinkOrSkip(t, "loop", filepath.Join(root, "loop"))                        // cycle

	ok := []struct{ path, want string }{
		{"sub/f.txt", filepath.Join(root, "sub", "f.txt")},
		{".", root},
		{"sub/../sub/f.txt", filepath.Join(root, "sub", "f.txt")},
		{"inside/f.txt", filepath.Join(root, "sub", "f.txt")},
		{"sub/new/file.txt", filepath.Join(root, "sub", "new", "file.txt")}, // not yet created
		{filepath.Join(root, "sub"), filepath.Join(root, "sub")},            // absolute, inside
		{filepath.Join(outside, "back", "f.txt"), filepath.Join(root, "sub", "f.txt")},
		// `inside/..` is the parent of the link's TARGET (root/sub/..), so root.
		{"inside/..", root},
	}
	for _, c := range ok {
		v, e, good := runFileTask(t, File_resolveWithin(root, c.path))
		if !good || v != c.want {
			t.Errorf("resolveWithin(%q) = %v %v, want %s", c.path, v, e, c.want)
		}
	}

	denied := []string{
		"..",
		"../outside/secret",
		"sub/../../outside",
		"escape/secret",
		"escape2/secret",
		"escape",
		"missing/../../outside", // lexical `..` past a missing component
		outside,
		filepath.Join(root, "escape", "anything-new"),
	}
	for _, p := range denied {
		v, e, good := runFileTask(t, File_resolveWithin(root, p))
		if good || errKindOf(e) != "PermissionDenied" {
			t.Errorf("resolveWithin(%q) = %v %v, want PermissionDenied", p, v, e)
		}
	}

	if _, e, good := runFileTask(t, File_resolveWithin(root, "loop/x")); good {
		t.Errorf("a symlink cycle is an error, got ok")
	} else if errKindOf(e) == "PermissionDenied" {
		t.Errorf("a cycle is not an escape: %v", e)
	}
	if _, e, good := runFileTask(t, File_resolveWithin(filepath.Join(base, "no-root"), "x")); good || errKindOf(e) != "NotFound" {
		t.Errorf("a missing root is NotFound, got %v", e)
	}
	if _, e, good := runFileTask(t, File_resolveWithin(root, "")); good || errKindOf(e) != "InvalidInput" {
		t.Errorf("an empty path is InvalidInput, got %v", e)
	}
}

// File.copy / rename / tempFile / tempDir act when the Task runs, never when
// the expression is evaluated.
func TestFileEffectsRunOnlyWhenTheTaskRuns(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	copyTask := File_copy(src, dst)
	moved := filepath.Join(dir, "moved")
	renameTask := File_rename(src, moved)
	_ = File_tempDir("sky-lazy-")
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("File.copy acted before its Task ran")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("File.rename acted before its Task ran")
	}
	if _, e, ok := runFileTask(t, copyTask); !ok {
		t.Fatalf("copy: %v", e)
	}
	if _, e, ok := runFileTask(t, renameTask); !ok {
		t.Fatalf("rename: %v", e)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("rename did not run: %v", err)
	}
	v, _, ok := runFileTask(t, File_tempDir("sky-lazy-"))
	if !ok {
		t.Fatal("tempDir failed")
	}
	os.RemoveAll(v.(string))
}
