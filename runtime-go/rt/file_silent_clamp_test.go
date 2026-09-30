package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFile_NoSilentClamps is the C-18 regression (the runtime half; the
// `permissions` digit check is in SyncFileConformanceTest.sky).
//   - readFileLimit with a limit below 1 read the whole file (it meant "the
//     100 MB default"), so a computed limit that went to 0 dropped the cap.
//   - resolveWithin with an empty root confined the path to the working
//     directory.
//
// Both are refused with InvalidInput.
func TestFile_NoSilentClamps(t *testing.T) {
	dir := realDir(t)
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, lim := range []int{0, -1} {
		v, e, good := runFileTask(t, File_readFileLimit(f, lim))
		if good || errKindOf(e) != "InvalidInput" {
			t.Errorf("readFileLimit %d = %v %v, want InvalidInput", lim, v, e)
		}
	}
	if v, _, good := runFileTask(t, File_readFileLimit(f, 100)); !good || len(v.(string)) != 100 {
		t.Errorf("readFileLimit at the file size = %v", v)
	}
	if v, e, good := runFileTask(t, File_resolveWithin("", "x")); good || errKindOf(e) != "InvalidInput" {
		t.Errorf("resolveWithin with an empty root = %v %v, want InvalidInput", v, e)
	}
	// Each refusal names the change and the migration note.
	for _, c := range []struct {
		task   any
		anchor string
	}{
		{File_readFileLimit(f, 0), "docs/migration/v0.27.md#readfilelimit-positive-limit"},
		{File_resolveWithin("", "x"), "docs/migration/v0.27.md#resolvewithin-empty-root"},
		{File_chmod(-1, f), "docs/migration/v0.27.md#file-permissions-no-clamp"},
	} {
		_, e, good := runFileTask(t, c.task)
		if good {
			t.Errorf("want an Err naming %s", c.anchor)
			continue
		}
		if msg, _ := renderSkyError(e); !strings.Contains(msg, c.anchor) || !strings.Contains(msg, "v0.27.0") {
			t.Errorf("the Err does not name the change and %s: %s", c.anchor, msg)
		}
	}
}
