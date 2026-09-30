//go:build linux || darwin

package rt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func watchOpts(recursive bool, debounceMs int, ignore ...string) map[string]any {
	ig := make([]any, len(ignore))
	for i, p := range ignore {
		ig[i] = p
	}
	return map[string]any{"recursive": recursive, "debounceMs": debounceMs, "ignore": ig}
}

// watchDir resolves symlinks (macOS TempDir is under /var → /private/var) so
// reported paths compare equal to the ones the test builds.
func watchDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func startWatchT(t *testing.T, paths []string, opts map[string]any) int {
	t.Helper()
	ps := make([]any, len(paths))
	for i, p := range paths {
		ps[i] = p
	}
	id := procOk(t, procTask(t, Watch_watch(ps, opts))).(int)
	t.Cleanup(func() { procTask(t, Watch_close(id)) })
	// Give the backend a moment to have every watch in place (kqueue
	// registration is synchronous; this only absorbs scheduler jitter).
	time.Sleep(20 * time.Millisecond)
	return id
}

func changeString(v any) string {
	adt := v.(SkyADT)
	switch adt.SkyName {
	case "Renamed":
		return fmt.Sprintf("Renamed %s %s", filepath.Base(adt.Fields[0].(string)), filepath.Base(adt.Fields[1].(string)))
	case "Overflow":
		return "Overflow"
	}
	return adt.SkyName + " " + filepath.Base(adt.Fields[0].(string))
}

func batchStrings(v any) []string {
	var out []string
	for _, c := range v.([]any) {
		out = append(out, changeString(c))
	}
	return out
}

// nextBatch runs Watch.next with a deadline. ok=false on timeout.
func nextBatch(t *testing.T, id int, within time.Duration) ([]string, bool) {
	t.Helper()
	ch := make(chan SkyResult[any, any], 1)
	go func() { ch <- Watch_next(id).(func() any)().(SkyResult[any, any]) }()
	select {
	case res := <-ch:
		return batchStrings(procOk(t, res)), true
	case <-time.After(within):
		return nil, false
	}
}

// expectChanges reads batches until every wanted change was seen.
func expectChanges(t *testing.T, id int, want ...string) []string {
	t.Helper()
	var got []string
	deadline := time.Now().Add(5 * time.Second)
	for {
		missing := false
		for _, w := range want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				missing = true
			}
		}
		if !missing {
			return got
		}
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("want %v, got %v", want, got)
		}
		b, ok := nextBatch(t, id, left)
		if !ok {
			t.Fatalf("want %v, got %v (timed out)", want, got)
		}
		got = append(got, b...)
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatchCreateModifyDeleteRename(t *testing.T) {
	dir := watchDir(t)
	id := startWatchT(t, []string{dir}, watchOpts(false, 20))
	a := filepath.Join(dir, "a.txt")
	writeFile(t, a, "1")
	expectChanges(t, id, "Created a.txt")
	time.Sleep(60 * time.Millisecond)
	f, _ := os.OpenFile(a, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("2")
	f.Close()
	expectChanges(t, id, "Modified a.txt")
	time.Sleep(60 * time.Millisecond)
	if err := os.Rename(a, filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	expectChanges(t, id, "Renamed a.txt b.txt")
	time.Sleep(60 * time.Millisecond)
	os.Remove(filepath.Join(dir, "b.txt"))
	expectChanges(t, id, "Removed b.txt")
}

// A burst becomes one batch with each path once. The debounce is wide (300
// ms) so a loaded runner does not split the burst (G-3: at 80 ms under -race
// a stalled loop straddled two windows); if it still does, the later batch
// may only repeat the path as Modified, never report the cancelled file.
func TestWatchCoalescesBursts(t *testing.T) {
	dir := watchDir(t)
	id := startWatchT(t, []string{dir}, watchOpts(false, 300))
	a := filepath.Join(dir, "burst.txt")
	for i := 0; i < 10; i++ {
		writeFile(t, a, strings.Repeat("x", i+1))
	}
	// Created then Removed inside the window cancels out.
	tmp := filepath.Join(dir, "gone.txt")
	writeFile(t, tmp, "x")
	os.Remove(tmp)
	b, ok := nextBatch(t, id, 5*time.Second)
	if !ok {
		t.Fatal("no batch")
	}
	if len(b) != 1 || b[0] != "Created burst.txt" {
		t.Fatalf("a burst must coalesce into [Created burst.txt], got %v", b)
	}
	for {
		more, ok := nextBatch(t, id, time.Second)
		if !ok {
			break
		}
		for _, c := range more {
			if c != "Modified burst.txt" {
				t.Fatalf("a later batch of the same burst reported %q (all: %v)", c, more)
			}
		}
	}
}

func TestWatchRecursiveAndIgnore(t *testing.T) {
	dir := watchDir(t)
	id := startWatchT(t, []string{dir}, watchOpts(true, 20, "*.tmp", "skip"))
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	expectChanges(t, id, "Created sub")
	time.Sleep(60 * time.Millisecond)
	writeFile(t, filepath.Join(sub, "deep.txt"), "x")
	writeFile(t, filepath.Join(sub, "junk.tmp"), "x")
	os.Mkdir(filepath.Join(dir, "skip"), 0o755)
	writeFile(t, filepath.Join(dir, "skip", "hidden.txt"), "x")
	got := expectChanges(t, id, "Created deep.txt")
	time.Sleep(200 * time.Millisecond)
	if more, ok := nextBatch(t, id, 200*time.Millisecond); ok {
		got = append(got, more...)
	}
	for _, g := range got {
		if strings.Contains(g, "junk.tmp") || strings.Contains(g, "skip") || strings.Contains(g, "hidden") {
			t.Fatalf("an ignored path was reported: %v", got)
		}
	}
	// Non-recursive: a file in a subdirectory is not reported.
	dir2 := watchDir(t)
	os.Mkdir(filepath.Join(dir2, "sub"), 0o755)
	id2 := startWatchT(t, []string{dir2}, watchOpts(false, 20))
	writeFile(t, filepath.Join(dir2, "sub", "deep.txt"), "x")
	writeFile(t, filepath.Join(dir2, "top.txt"), "x")
	got2 := expectChanges(t, id2, "Created top.txt")
	for _, g := range got2 {
		if strings.Contains(g, "deep.txt") {
			t.Fatalf("non-recursive watch reported a nested file: %v", got2)
		}
	}
}

func TestWatchFileRoot(t *testing.T) {
	dir := watchDir(t)
	f := filepath.Join(dir, "only.txt")
	writeFile(t, f, "1")
	id := startWatchT(t, []string{f}, watchOpts(false, 20))
	writeFile(t, filepath.Join(dir, "other.txt"), "x")
	writeFile(t, f, "22")
	got := expectChanges(t, id, "Modified only.txt")
	for _, g := range got {
		if strings.Contains(g, "other") {
			t.Fatalf("a file root reported a sibling: %v", got)
		}
	}
}

func TestWatchOverflow(t *testing.T) {
	dir := watchDir(t)
	id := startWatchT(t, []string{dir}, watchOpts(false, 20))
	w, _ := lookupWatcher(id)
	// An OS queue overflow (simulated): the next batch is exactly [Overflow].
	writeFile(t, filepath.Join(dir, "x"), "1")
	time.Sleep(10 * time.Millisecond)
	w.injectOverflow()
	b, ok := nextBatch(t, id, 5*time.Second)
	if !ok || len(b) != 1 || b[0] != "Overflow" {
		t.Fatalf("after an overflow the next batch must be [Overflow], got %v", b)
	}
	// The runtime's own queue: batches nobody consumes overflow too.
	w.mu.Lock()
	w.batchCap = 2
	w.mu.Unlock()
	for i := 0; i < 5; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%d", i)), "x")
		time.Sleep(80 * time.Millisecond)
	}
	b, ok = nextBatch(t, id, 5*time.Second)
	if !ok || len(b) != 1 || b[0] != "Overflow" {
		t.Fatalf("an unconsumed queue past its bound must yield [Overflow], got %v", b)
	}
}

func TestWatchErrors(t *testing.T) {
	res := procTask(t, Watch_watch([]any{"/definitely/not/here"}, watchOpts(false, 0)))
	if res.Tag == 0 || procErrTag(res.ErrValue) != 5 {
		t.Fatal("watching a missing path must be Err NotFound")
	}
	res = procTask(t, Watch_watch([]any{}, watchOpts(false, 0)))
	if res.Tag == 0 || errorKindName(res.ErrValue) != "InvalidInput" {
		t.Fatal("watching nothing must be Err InvalidInput")
	}
}

// close wakes a waiting next (it returns []) and stops a Sub; nothing is
// delivered afterwards and no goroutine is left.
func TestWatchCloseStopsDelivery(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	dir := watchDir(t)
	ps := []any{dir}
	id := procOk(t, procTask(t, Watch_watch(ps, watchOpts(true, 20)))).(int)
	done := make(chan SkyResult[any, any], 1)
	go func() { done <- Watch_next(id).(func() any)().(SkyResult[any, any]) }()
	time.Sleep(50 * time.Millisecond)
	procOk(t, procTask(t, Watch_close(id)))
	select {
	case res := <-done:
		if l := procOk(t, res).([]any); len(l) != 0 {
			t.Fatalf("next on a closed watcher = %v, want []", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not wake a waiting next")
	}

	// Sub mode.
	id2 := procOk(t, procTask(t, Watch_watch(ps, watchOpts(false, 20)))).(int)
	msgCh := make(chan any, 16)
	m := newSubManager(msgCh)
	m.update(func(any) any { return Watch_changes(id2, func(b any) any { return b }) }, nil)
	writeFile(t, filepath.Join(dir, "sub-a"), "1")
	select {
	case b := <-msgCh:
		if s := batchStrings(b); len(s) != 1 || s[0] != "Created sub-a" {
			t.Fatalf("Sub batch %v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("changes Sub delivered nothing")
	}
	// A Task read is refused while the Sub reads it.
	if res := procTask(t, Watch_next(id2)); res.Tag == 0 {
		t.Fatal("next on a Sub-read watcher must be Err")
	}
	procOk(t, procTask(t, Watch_close(id2)))
	writeFile(t, filepath.Join(dir, "sub-b"), "1")
	select {
	case b := <-msgCh:
		t.Fatalf("delivery after close: %v", batchStrings(b))
	case <-time.After(300 * time.Millisecond):
	}
	m.stopAll()

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+1 {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutines: before %d, after %d\n%s", before, runtime.NumGoroutine(), buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatchOwnedBySessionClosesWithIt(t *testing.T) {
	dir := watchDir(t)
	sess := &liveSession{done: make(chan struct{})}
	var id int
	runWithLiveSession(sess, func() {
		id = procOk(t, procTask(t, Watch_watch([]any{dir}, watchOpts(false, 20)))).(int)
	})
	sess.markDone()
	if _, e := lookupWatcher(id); e == nil {
		t.Fatal("a session-owned watcher survived the session")
	}
}

func TestPendingSetMergeRules(t *testing.T) {
	p := newPendingSet()
	p.add(rawEvent{kind: watchCreated, path: "/a"})
	p.add(rawEvent{kind: watchModified, path: "/a"})
	p.add(rawEvent{kind: watchCreated, path: "/b"})
	p.add(rawEvent{kind: watchRemoved, path: "/b"})
	p.add(rawEvent{kind: watchRemoved, path: "/c"})
	p.add(rawEvent{kind: watchCreated, path: "/c"})
	p.add(rawEvent{kind: watchModified, path: "/d"})
	p.add(rawEvent{kind: watchRemoved, path: "/d"})
	p.add(rawEvent{kind: watchRemoved, path: "/old", pairKey: "ino:7"})
	p.add(rawEvent{kind: watchCreated, path: "/new", pairKey: "ino:7"})
	var got []string
	for _, c := range p.batch() {
		s := fmt.Sprintf("%d%s", c.kind, c.path)
		if c.to != "" {
			s += ">" + c.to
		}
		got = append(got, s)
	}
	sort.Strings(got)
	want := []string{"0/a", "1/c", "2/d", "3/old>/new"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("merge = %v, want %v", got, want)
	}
}

// ── Replacement by rename and re-creation (editor saves) ────────────────
//
// An editor saves by writing a temporary file and renaming it over the
// original. The watch must keep reporting the NEW file afterwards: on
// macOS the kqueue backend holds a descriptor per file, and a descriptor
// follows the old inode, so every replacement has to re-arm the watch.

// watchFeed reads a watcher with ONE goroutine, so a read that times out
// in the test never leaves a second Watch.next behind to take a batch.
type watchFeed struct {
	ch chan []string
}

func newWatchFeed(t *testing.T, id int) *watchFeed {
	t.Helper()
	f := &watchFeed{ch: make(chan []string, 256)}
	go func() {
		defer close(f.ch)
		for {
			res := Watch_next(id).(func() any)().(SkyResult[any, any])
			if res.Tag != 0 {
				return
			}
			l := res.OkValue.([]any)
			if len(l) == 0 {
				return // closed
			}
			f.ch <- batchStrings(l)
		}
	}()
	return f
}

// drain reads batches until the watcher has been quiet for quiet.
func (f *watchFeed) drain(quiet time.Duration) []string {
	var got []string
	for {
		select {
		case b, ok := <-f.ch:
			if !ok {
				return got
			}
			got = append(got, b...)
		case <-time.After(quiet):
			return got
		}
	}
}

// expect reads until one of want was reported; the whole read is returned.
func (f *watchFeed) expect(t *testing.T, want ...string) []string {
	t.Helper()
	var got []string
	deadline := time.After(5 * time.Second)
	for {
		for _, g := range got {
			for _, w := range want {
				if g == w {
					return got
				}
			}
		}
		select {
		case b, ok := <-f.ch:
			if !ok {
				t.Fatalf("want one of %v, got %v (watcher closed)", want, got)
			}
			got = append(got, b...)
		case <-deadline:
			t.Fatalf("want one of %v, got %v (timed out)", want, got)
		}
	}
}

func appendText(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// saveByRename replaces p the way an editor saves it.
func saveByRename(t *testing.T, p, s string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".tmp")
	writeFile(t, tmp, s)
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
}

const watchQuiet = 150 * time.Millisecond

func TestWatchFileReplacedByRenameStaysWatched(t *testing.T) {
	for _, mode := range []string{"dir", "file"} {
		t.Run(mode, func(t *testing.T) {
			dir := watchDir(t)
			a := filepath.Join(dir, "a.txt")
			writeFile(t, a, "one\n")
			root := dir
			if mode == "file" {
				root = a
			}
			f := newWatchFeed(t, startWatchT(t, []string{root}, watchOpts(false, 20)))
			appendText(t, a, "two\n")
			f.expect(t, "Modified a.txt")
			f.drain(watchQuiet)
			// Twelve saves in a row: every one of them, and every append
			// after one, must be reported.
			for i := 0; i < 12; i++ {
				saveByRename(t, a, fmt.Sprintf("saved %d\n", i))
				f.expect(t, "Modified a.txt")
				f.drain(watchQuiet)
				appendText(t, a, "more\n")
				f.expect(t, "Modified a.txt")
				f.drain(watchQuiet)
			}
		})
	}
}

func TestWatchFileDeletedAndRecreatedStaysWatched(t *testing.T) {
	dir := watchDir(t)
	a := filepath.Join(dir, "a.txt")
	writeFile(t, a, "one\n")
	f := newWatchFeed(t, startWatchT(t, []string{dir}, watchOpts(false, 20)))
	for i := 0; i < 5; i++ {
		if err := os.Remove(a); err != nil {
			t.Fatal(err)
		}
		f.expect(t, "Removed a.txt")
		f.drain(watchQuiet)
		writeFile(t, a, "back\n")
		f.expect(t, "Created a.txt")
		f.drain(watchQuiet)
		appendText(t, a, "more\n")
		f.expect(t, "Modified a.txt")
		f.drain(watchQuiet)
	}
}

// A subdirectory of a recursive watch is replaced whole: a new directory is
// filled and renamed into place, the old one moved away and removed.
func TestWatchSubdirectoryReplacedByRenameStaysWatched(t *testing.T) {
	dir := watchDir(t)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	writeFile(t, filepath.Join(sub, "f.txt"), "one\n")
	f := newWatchFeed(t, startWatchT(t, []string{dir}, watchOpts(true, 20)))
	for i := 0; i < 5; i++ {
		fresh := filepath.Join(dir, "sub.new")
		os.Mkdir(fresh, 0o755)
		writeFile(t, filepath.Join(fresh, "f.txt"), fmt.Sprintf("gen %d\n", i))
		old := filepath.Join(dir, "sub.old")
		if err := os.Rename(sub, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fresh, sub); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(old); err != nil {
			t.Fatal(err)
		}
		f.expect(t, "Created f.txt", "Modified f.txt", "Created sub", "Modified sub")
		f.drain(watchQuiet)
		appendText(t, filepath.Join(sub, "f.txt"), "more\n")
		f.expect(t, "Modified f.txt")
		f.drain(watchQuiet)
	}
}

// The watched directory ITSELF is replaced: by a rename (move the old one
// away, rename a new one into place), and by removal and re-creation.
func TestWatchRootDirectoryReplacedStaysWatched(t *testing.T) {
	base := watchDir(t)
	root := filepath.Join(base, "root")
	os.Mkdir(root, 0o755)
	a := filepath.Join(root, "a.txt")
	writeFile(t, a, "one\n")
	f := newWatchFeed(t, startWatchT(t, []string{root}, watchOpts(false, 20)))
	appendText(t, a, "two\n")
	f.expect(t, "Modified a.txt")
	f.drain(watchQuiet)
	for i := 0; i < 5; i++ {
		fresh := filepath.Join(base, "root.new")
		os.Mkdir(fresh, 0o755)
		writeFile(t, filepath.Join(fresh, "a.txt"), fmt.Sprintf("gen %d\n", i))
		old := filepath.Join(base, "root.old")
		if err := os.Rename(root, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fresh, root); err != nil {
			t.Fatal(err)
		}
		f.expect(t, "Created a.txt", "Modified a.txt")
		f.drain(watchQuiet)
		// The old directory is no longer the watched one.
		appendText(t, filepath.Join(old, "a.txt"), "stale\n")
		if err := os.RemoveAll(old); err != nil {
			t.Fatal(err)
		}
		appendText(t, a, "more\n")
		got := f.expect(t, "Modified a.txt")
		got = append(got, f.drain(watchQuiet)...)
		for _, g := range got {
			if g != "Modified a.txt" {
				t.Fatalf("after the replacement only the new a.txt may be reported, got %v", got)
			}
		}
	}
	// Removed, then created again (a gap in between).
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "Removed a.txt")
	f.drain(watchQuiet)
	os.Mkdir(root, 0o755)
	writeFile(t, filepath.Join(root, "b.txt"), "b\n")
	f.expect(t, "Created b.txt")
	f.drain(watchQuiet)
	appendText(t, filepath.Join(root, "b.txt"), "more\n")
	f.expect(t, "Modified b.txt")
}
