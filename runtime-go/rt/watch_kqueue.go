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
}

type kqueueBackend struct {
	w      *watcher
	kq     int
	wakeR  int
	wakeW  int
	mu     sync.Mutex
	byFD   map[int]*kqNode
	byPath map[string]*kqNode
	done   chan struct{}
	closed bool
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
	n.entries = listDir(dir)
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

// dropPath stops watching path and everything below it.
func (b *kqueueBackend) dropPath(path string) {
	prefix := path + string(filepath.Separator)
	b.mu.Lock()
	var fds []int
	for p, n := range b.byPath {
		if p == path || strings.HasPrefix(p, prefix) {
			fds = append(fds, n.fd)
			delete(b.byPath, p)
			delete(b.byFD, n.fd)
		}
	}
	b.mu.Unlock()
	for _, fd := range fds {
		syscall.Close(fd) // closing the descriptor removes its kevent
	}
}

func (b *kqueueBackend) closeFDs() {
	b.mu.Lock()
	for fd := range b.byFD {
		syscall.Close(fd)
	}
	b.byFD = map[int]*kqNode{}
	b.byPath = map[string]*kqNode{}
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
	}
}

func (b *kqueueBackend) handle(fd int, fflags uint32) {
	b.mu.Lock()
	node, ok := b.byFD[fd]
	b.mu.Unlock()
	if !ok {
		return
	}
	if node.isDir {
		if fflags&syscall.NOTE_WRITE != 0 {
			b.rescan(node)
		}
		return
	}
	if fflags&(syscall.NOTE_DELETE|syscall.NOTE_RENAME) != 0 {
		// The directory rescan reports the removal / rename; this file's
		// descriptor is stale now.
		b.dropPath(node.path)
		return
	}
	if fflags&(syscall.NOTE_WRITE|syscall.NOTE_EXTEND|syscall.NOTE_ATTRIB) != 0 && b.w.wants(node.path) {
		b.w.send(rawEvent{kind: watchModified, path: node.path})
	}
}

// rescan diffs a directory against its snapshot.
func (b *kqueueBackend) rescan(node *kqNode) {
	now := listDir(node.path)
	b.mu.Lock()
	before := node.entries
	node.entries = now
	b.mu.Unlock()
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
		if old, had := before[name]; had && old.ino == cur.ino {
			continue
		}
		if b.w.wants(p) {
			b.w.send(rawEvent{kind: watchCreated, path: p, pairKey: fmt.Sprintf("ino:%d", cur.ino)})
		}
		if cur.isDir {
			if b.w.opts.recursive {
				if rel, ok := b.w.relTo(p); ok && !b.w.ignored(rel) {
					_ = b.addTree(p, true)
				}
			}
			continue
		}
		if b.w.wants(p) {
			_ = b.addFile(p)
		}
	}
}
