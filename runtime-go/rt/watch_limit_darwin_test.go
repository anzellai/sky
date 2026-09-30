//go:build darwin

package rt

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWatchKqueue_DescriptorBudget is the D-7 regression for macOS. kqueue
// needs one descriptor per watched file and directory, and addTree ignored
// every error, so a recursive watch of a big tree (a node_modules not in
// `ignore`) could take every descriptor of the process: the app's listener
// and database pool then failed with EMFILE. The watches are now bounded
// process-wide: past the bound the watch fails at start with an Err naming
// the limit, and a tree that grows past it later delivers Overflow instead
// of an unwatched file.
func TestWatchKqueue_DescriptorBudget(t *testing.T) {
	defer watchFDLimitForTest.Store(0)
	dir := watchDir(t)
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := watchFDsInUse.Load()
	watchFDLimitForTest.Store(before + 10)
	res := procTask(t, Watch_watch([]any{dir}, watchOpts(true, 10)))
	if res.Tag == 0 {
		procTask(t, Watch_close(res.OkValue))
		t.Fatal("a watch past the descriptor budget started")
	}
	if !strings.Contains(errorMessageOf(res.ErrValue), "watch limit") {
		t.Fatalf("the Err does not name the limit: %s", errorMessageOf(res.ErrValue))
	}
	if n := watchFDsInUse.Load(); n != before {
		t.Fatalf("a failed watch kept %d descriptors", n-before)
	}

	// Within the budget at start, then the tree grows past it: Overflow.
	small := watchDir(t)
	watchFDLimitForTest.Store(before + 4)
	id := startWatchT(t, []string{small}, watchOpts(true, 10))
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(small, "g"+strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, ok := nextBatch(t, id, time.Second)
		if !ok {
			continue
		}
		for _, c := range b {
			if c == "Overflow" {
				procTask(t, Watch_close(id))
				if n := watchFDsInUse.Load(); n != before {
					t.Fatalf("closing the watcher left %d descriptors counted", n-before)
				}
				return
			}
		}
	}
	t.Fatal("a tree that grew past the budget delivered no Overflow")
}
