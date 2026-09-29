//go:build darwin && !js

package rt

// watch_kqueue.go — Std.Watch on macOS, with kqueue from the standard
// library (syscall.Kqueue / Kevent), the mechanism the usual third-party
// watcher also uses there.
//
// kqueue reports THAT a vnode changed, not what: a directory's NOTE_WRITE
// means "an entry was added, removed or renamed". So every watched directory
// keeps a snapshot of its entries (name → inode, is-dir); on NOTE_WRITE the
// directory is re-listed and diffed. A name that appeared is Created, a name
// that went is Removed, a name whose inode changed was replaced (Modified).
// The inode is the rename pair key, so a Removed and a Created of the same
// inode in one batch become Renamed. Each watched regular file has its own
// descriptor for content changes (NOTE_WRITE / NOTE_EXTEND / NOTE_ATTRIB →
// Modified).
//
// Replacement. A descriptor follows its inode, not its name: after an
// editor's save (write a temporary file, rename it over the original) the
// old descriptor watches a file that is gone. So a NOTE_DELETE / NOTE_RENAME
// on a file drops that file's node and rescans its directory, which watches
// whatever now has the name; a rescan also watches any wanted entry that has
// no watch yet. A watched root directory (or the directory of a file root)
// that is renamed or deleted is re-armed on its path, diffed against its last
// listing; when the path is missing, the nearest existing ancestor is watched
// (a helper node, never reported) until the path comes back.
//
// Descriptor numbers are reused at once by the kernel, and one Kevent call
// returns a batch of events: closing a descriptor while events for it are
// still in the batch let a stale NOTE_DELETE match the NEW file's
// descriptor, which then dropped the new watch. Closes are therefore
// deferred until the batch has been handled (retire / flushRetired).
//
// The event loop blocks in Kevent; close() wakes it through a pipe that is
// registered with the same kqueue.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const oEvtOnly = 0x8000 // O_EVTONLY: a descriptor for events only

type kqEntry struct {
	ino   uint64
	isDir bool
}

type kqNode struct {
	fd    int
	path  string
	isDir bool
	// entries: a directory's last listing (name → entry).
	entries map[string]kqEntry
	// helper: an ancestor of a missing root, watched only to learn when the
	// root's path exists again. Never reported.
	helper bool
}

type kqueueBackend struct {
	w      *watcher
	kq     int
	wakeR  int
	wakeW  int
	mu     sync.Mutex
	byFD   map[int]*kqNode
	byPath map[string]*kqNode
	// retired: descriptors no longer watched, closed after the current
	// batch of events (see the file comment).
	retired []int
	done    chan struct{}
	closed  bool
}

func newWatchBackend(w *watcher) (watchBackend, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	syscall.CloseOnExec(kq)
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		syscall.Close(kq)
		return nil, fmt.Errorf("pipe: %w", err)
	}
	syscall.CloseOnExec(p[0])
	syscall.CloseOnExec(p[1])
	b := &kqueueBackend{
		w: w, kq: kq, wakeR: p[0], wakeW: p[1],
		byFD: map[int]*kqNode{}, byPath: map[string]*kqNode{},
		done: make(chan struct{}),
	}
	ev := syscall.Kevent_t{}
	syscall.SetKevent(&ev, b.wakeR, syscall.EVFILT_READ, syscall.EV_ADD)
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{ev}, nil, nil); err != nil {
		b.closeFDs()
		return nil, fmt.Errorf("kevent: %w", err)
	}
	for _, r := range w.roots {
		if r.isDir {
			if err := b.addTree(r.path, false); err != nil {
				b.closeFDs()
				return nil, err
			}
			continue
		}
		// A file root: its directory (filtered to the file) plus the file.
		if err := b.addDirOnly(filepath.Dir(r.path)); err != nil {
			b.closeFDs()
			return nil, err
		}
		_ = b.addFile(r.path)
	}
	go b.loop()
	return b, nil
}

func (b *kqueueBackend) register(path string, isDir bool) (*kqNode, error) {
	b.mu.Lock()
	if n, ok := b.byPath[path]; ok {
		// A helper that is now wanted for itself becomes a real watch.
		n.helper = false
		b.mu.Unlock()
		return n, nil
	}
	b.mu.Unlock()
	fd, err := syscall.Open(path, oEvtOnly|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	ev := syscall.Kevent_t{}
	syscall.SetKevent(&ev, fd, syscall.EVFILT_VNODE, syscall.EV_ADD|syscall.EV_CLEAR)
	ev.Fflags = syscall.NOTE_WRITE | syscall.NOTE_EXTEND | syscall.NOTE_ATTRIB |
		syscall.NOTE_DELETE | syscall.NOTE_RENAME
	if _, err := syscall.Kevent(b.kq, []syscall.Kevent_t{ev}, nil, nil); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	n := &kqNode{fd: fd, path: path, isDir: isDir}
	b.mu.Lock()
	b.byFD[fd] = n
	b.byPath[path] = n
	b.mu.Unlock()
	return n, nil
}

// registered reports whether path has a watch.
func (b *kqueueBackend) registered(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.byPath[path]
	return ok
}

// listDir snapshots a directory's entries.
func listDir(dir string) map[string]kqEntry {
	out := map[string]kqEntry{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		var ino uint64
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			ino = st.Ino
		}
		out[e.Name()] = kqEntry{ino: ino, isDir: e.IsDir()}
	}
	return out
}

// addDirOnly watches one directory for entry changes (no recursion, no file
// descriptors for its files) — the parent of a file root.
func (b *kqueueBackend) addDirOnly(dir string) error {
	n, err := b.register(dir, true)
	if err != nil {
		return fmt.Errorf("watch %s: %w", dir, err)
	}
	b.mu.Lock()
	if n.entries == nil {
		n.entries = listDir(dir)
	}
	b.mu.Unlock()
	return nil
}

func (b *kqueueBackend) addFile(path string) error {
	_, err := b.register(path, false)
	return err
}

// addTree watches dir, a descriptor per file in it that the app wants, and
// (recursive) every subdirectory. With report, every entry found is reported
// Created (a directory that just appeared may already hold files).
func (b *kqueueBackend) addTree(dir string, report bool) error {
	if err := b.addDirOnly(dir); err != nil {
		return err
	}
	b.mu.Lock()
	entries := b.byPath[dir].entries
	b.mu.Unlock()
	for name, e := range entries {
		p := filepath.Join(dir, name)
		if report && b.w.wants(p) {
			b.w.send(rawEvent{kind: watchCreated, path: p, pairKey: fmt.Sprintf("ino:%d", e.ino)})
		}
		if e.isDir {
			if b.w.opts.recursive {
				if rel, ok := b.w.relTo(p); ok && !b.w.ignored(rel) {
					_ = b.addTree(p, report)
				}
			}
			continue
		}
		if b.w.wants(p) {
			_ = b.addFile(p)
		}
	}
	return nil
}

// retireLocked forgets a node; its descriptor is closed after the batch.
func (b *kqueueBackend) retireLocked(n *kqNode) {
	if b.byFD[n.fd] == n {
		delete(b.byFD, n.fd)
		b.retired = append(b.retired, n.fd)
	}
	if b.byPath[n.path] == n {
		delete(b.byPath, n.path)
	}
}

// dropPath stops watching path and everything below it.
func (b *kqueueBackend) dropPath(path string) {
	prefix := path + string(filepath.Separator)
	b.mu.Lock()
	defer b.mu.Unlock()
	for p, n := range b.byPath {
		if p == path || strings.HasPrefix(p, prefix) {
			b.retireLocked(n)
		}
	}
}

// dropNode stops watching one node (not what a newer node has at its path).
func (b *kqueueBackend) dropNode(n *kqNode) {
	b.mu.Lock()
	b.retireLocked(n)
	b.mu.Unlock()
}

// flushRetired closes the descriptors retired during the last batch.
// Closing a descriptor removes its kevent and any event still queued for it.
func (b *kqueueBackend) flushRetired() {
	b.mu.Lock()
	fds := b.retired
	b.retired = nil
	b.mu.Unlock()
	for _, fd := range fds {
		syscall.Close(fd)
	}
}

func (b *kqueueBackend) closeFDs() {
	b.mu.Lock()
	for fd := range b.byFD {
		syscall.Close(fd)
	}
	for _, fd := range b.retired {
		syscall.Close(fd)
	}
	b.byFD = map[int]*kqNode{}
	b.byPath = map[string]*kqNode{}
	b.retired = nil
	b.mu.Unlock()
	syscall.Close(b.kq)
	syscall.Close(b.wakeR)
	syscall.Close(b.wakeW)
}

func (b *kqueueBackend) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	_, _ = syscall.Write(b.wakeW, []byte{1})
	<-b.done
	b.closeFDs()
}

func (b *kqueueBackend) loop() {
	defer close(b.done)
	events := make([]syscall.Kevent_t, 64)
	for {
		n, err := syscall.Kevent(b.kq, nil, events, nil)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		for i := 0; i < n; i++ {
			fd := int(events[i].Ident)
			if fd == b.wakeR {
				return
			}
			b.handle(fd, events[i].Fflags)
		}
		b.flushRetired()
	}
}

func (b *kqueueBackend) handle(fd int, fflags uint32) {
	b.mu.Lock()
	node, ok := b.byFD[fd]
	b.mu.Unlock()
	if !ok {
		return
	}
	gone := fflags&(syscall.NOTE_DELETE|syscall.NOTE_RENAME) != 0
	switch {
	case node.helper:
		if gone {
			b.dropNode(node)
		} else if fflags&syscall.NOTE_WRITE != 0 {
			b.rescan(node)
		}
	case node.isDir:
		if gone {
			b.dirGone(node)
		} else if fflags&syscall.NOTE_WRITE != 0 {
			b.rescan(node)
		}
	default:
		if gone {
			// The name may already hold a new file (a save by rename): drop
			// this descriptor only, and let the directory diff report the
			// change and watch whatever has the name now.
			b.dropNode(node)
			if parent := b.nodeAt(filepath.Dir(node.path)); parent != nil && parent.isDir {
				b.rescan(parent)
			}
		} else if fflags&(syscall.NOTE_WRITE|syscall.NOTE_EXTEND|syscall.NOTE_ATTRIB) != 0 && b.w.wants(node.path) {
			b.w.send(rawEvent{kind: watchModified, path: node.path})
		}
		return
	}
	b.reconcileAnchors()
}

func (b *kqueueBackend) nodeAt(path string) *kqNode {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.byPath[path]
}

// anchors: the directories a watch hangs from — each directory root, and
// the directory of each file root. They are re-armed on their path when
// replaced; any other directory is re-armed by its parent's rescan.
func (b *kqueueBackend) anchors() []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range b.w.roots {
		p := r.path
		if !r.isDir {
			p = filepath.Dir(r.path)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func (b *kqueueBackend) isAnchor(path string) bool {
	for _, a := range b.anchors() {
		if a == path {
			return true
		}
	}
	return false
}

func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// dirGone: a watched directory was renamed or deleted.
func (b *kqueueBackend) dirGone(node *kqNode) {
	if !b.isAnchor(node.path) {
		if parent := b.nodeAt(filepath.Dir(node.path)); parent != nil && parent.isDir {
			// The parent's diff drops this directory and watches whatever
			// has its name now.
			b.rescan(parent)
			return
		}
		b.dropPath(node.path)
		return
	}
	b.mu.Lock()
	old := node.entries
	b.mu.Unlock()
	b.dropPath(node.path)
	if b.armAnchor(node.path, old) {
		return
	}
	// The path is gone for now: what was in it is gone with it.
	for name, e := range old {
		p := filepath.Join(node.path, name)
		if b.w.wants(p) {
			b.w.send(rawEvent{kind: watchRemoved, path: p, pairKey: fmt.Sprintf("ino:%d", e.ino)})
		}
	}
}

// armAnchor watches an anchor directory again and reports its entries
// against old (its last listing; nil when it was absent). False when the
// path is not a directory now.
func (b *kqueueBackend) armAnchor(path string, old map[string]kqEntry) bool {
	if !isDirPath(path) {
		return false
	}
	n, err := b.register(path, true)
	if err != nil {
		return false
	}
	if old == nil {
		old = map[string]kqEntry{}
	}
	b.mu.Lock()
	n.entries = old
	b.mu.Unlock()
	b.rescan(n)
	return true
}

// reconcileAnchors watches every anchor that exists and has no watch, and,
// for one that does not exist, its nearest existing ancestor (a helper) so
// its return is seen. Helpers no longer needed are dropped.
func (b *kqueueBackend) reconcileAnchors() {
	for round := 0; round < 4; round++ {
		needed := map[string]bool{}
		added := false
		for _, a := range b.anchors() {
			if b.registered(a) {
				if n := b.nodeAt(a); n == nil || !n.helper {
					continue
				}
			}
			if b.armAnchor(a, nil) {
				continue
			}
			for p := filepath.Dir(a); ; p = filepath.Dir(p) {
				if isDirPath(p) {
					needed[p] = true
					if !b.registered(p) {
						if n, err := b.register(p, true); err == nil {
							b.mu.Lock()
							n.helper = true
							n.entries = listDir(p)
							b.mu.Unlock()
							added = true
						}
					}
					break
				}
				if p == filepath.Dir(p) {
					break
				}
			}
		}
		b.mu.Lock()
		for p, n := range b.byPath {
			if n.helper && !needed[p] {
				b.retireLocked(n)
			}
		}
		b.mu.Unlock()
		// A path that appeared between the check and a new helper's first
		// listing raised no event on that helper: check once more.
		if !added {
			return
		}
	}
}

// rescan diffs a directory against its snapshot, and watches every wanted
// entry that has no watch (one dropped by its own delete / rename event).
func (b *kqueueBackend) rescan(node *kqNode) {
	now := listDir(node.path)
	b.mu.Lock()
	before := node.entries
	node.entries = now
	helper := node.helper
	b.mu.Unlock()
	if helper {
		return // reconcileAnchors acts on what appeared
	}
	for name, old := range before {
		p := filepath.Join(node.path, name)
		cur, still := now[name]
		if still && cur.ino == old.ino {
			continue
		}
		if b.w.wants(p) {
			b.w.send(rawEvent{kind: watchRemoved, path: p, pairKey: fmt.Sprintf("ino:%d", old.ino)})
		}
		b.dropPath(p)
	}
	for name, cur := range now {
		p := filepath.Join(node.path, name)
		old, had := before[name]
		fresh := !had || old.ino != cur.ino
		if fresh && b.w.wants(p) {
			b.w.send(rawEvent{kind: watchCreated, path: p, pairKey: fmt.Sprintf("ino:%d", cur.ino)})
		}
		if cur.isDir {
			// A root directory below this one is an anchor: reconcileAnchors
			// arms it.
			if rel, ok := b.w.relTo(p); ok && rel != "." && b.w.opts.recursive && !b.w.ignored(rel) {
				if fresh || !b.registered(p) {
					_ = b.addTree(p, fresh)
				}
			}
			continue
		}
		if b.w.wants(p) && (fresh || !b.registered(p)) {
			_ = b.addFile(p)
		}
	}
}
