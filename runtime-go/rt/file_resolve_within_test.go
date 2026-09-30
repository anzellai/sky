package rt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFileResolveWithin_DotDotAfterMissingComponent is the A-3 regression.
// Once a component did not exist, resolvePath appended the rest of the path
// lexically: a `..` then popped the missing component and the NEXT component
// (an existing symlink) was never read. `<root>/nothere/../link/secret`
// therefore passed the containment check and opened the file behind a link
// that points outside the root. (A relative path was safe only because
// filepath.Join cleaned `nothere/..` away first; an absolute one is not
// cleaned.) A `..` after a missing component cannot be checked against the
// filesystem, so it is refused.
func TestFileResolveWithin_DotDotAfterMissingComponent(t *testing.T) {
	base := realDir(t)
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, "../outside", filepath.Join(root, "link"))
	for _, p := range []string{
		filepath.Join(root, "nothere", "..", "link", "secret"),
		root + "/nothere/../link/secret",
		root + "/nothere/deeper/../../link/secret",
		root + "/nothere/../link",
	} {
		v, e, good := runFileTask(t, File_resolveWithin(root, p))
		if good || errKindOf(e) != "PermissionDenied" {
			t.Errorf("resolveWithin(%q) = %v %v, want PermissionDenied", p, v, e)
		}
	}
	// A missing tail with no `..` after it is still fine (a file about to be
	// created).
	want := filepath.Join(root, "new", "dir", "f.txt")
	if v, e, good := runFileTask(t, File_resolveWithin(root, root+"/new/dir/f.txt")); !good || v != want {
		t.Errorf("a missing tail = %v %v, want %s", v, e, want)
	}
}
