//go:build linux

package rt

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWatchInotify_OutOfWatchesIsNotSilent is the D-7 regression for Linux.
// Past fs.inotify.max_user_watches, inotify_add_watch fails with ENOSPC, and
// addTree ignored the error for every sub-directory: that sub-tree went
// unwatched with no Err and no Overflow, a silent missed change. The kernel
// limit is simulated (setting it needs root).
func TestWatchInotify_OutOfWatchesIsNotSilent(t *testing.T) {
	defer inotifyAddWatchHook.Store(nil)
	full := func(fd int, p string, mask uint32) (int, error) {
		if strings.Contains(filepath.Base(p), "sub") {
			return -1, syscall.ENOSPC
		}
		return syscall.InotifyAddWatch(fd, p, mask)
	}

	dir := watchDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	inotifyAddWatchHook.Store(&full)
	res := procTask(t, Watch_watch([]any{dir}, watchOpts(true, 10)))
	inotifyAddWatchHook.Store(nil)
	if res.Tag == 0 {
		procTask(t, Watch_close(res.OkValue))
		t.Fatal("a recursive watch started with a sub-directory it could not watch")
	}

	// Started fine; a directory created later cannot be watched: Overflow.
	dir2 := watchDir(t)
	id := startWatchT(t, []string{dir2}, watchOpts(true, 10))
	inotifyAddWatchHook.Store(&full)
	if err := os.Mkdir(filepath.Join(dir2, "sub2"), 0o755); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, ok := nextBatch(t, id, time.Second)
		if !ok {
			continue
		}
		for _, c := range b {
			if c == "Overflow" {
				return
			}
		}
	}
	t.Fatal("a directory that could not be watched delivered no Overflow")
}
