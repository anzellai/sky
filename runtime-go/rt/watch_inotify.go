//go:build linux && !js

package rt

// watch_inotify.go — Std.Watch on Linux, with inotify from the standard
// library (syscall.InotifyInit1 / InotifyAddWatch). The descriptor is
// non-blocking and wrapped in *os.File, so reads park in the Go poller and
// close() wakes them.
//
// Replacement. Files are watched through their directory, so a file saved
// by rename stays watched. A watch on a directory follows its inode: when a
// watched root directory (or the directory of a file root) is moved away
// (IN_MOVE_SELF) the watch would go on reporting the moved directory under
// the old path, and when it is deleted (IN_DELETE_SELF, then IN_IGNORED) the
// watch ends. Both re-arm the watch on the path; when the path is missing,
// the nearest existing ancestor is watched (a helper, never reported) until
// it exists again. Other directories are re-armed by their parent's
// IN_MOVED_TO / IN_CREATE.
//
// inotify reports a save by rename as IN_MOVED_TO of the name, with no event
// for the file it replaced. Each watched directory therefore keeps the names
// it holds (names), so a create or move onto an existing name is reported
// as a replacement (Modified, as the kqueue backend's inode diff reports it)
// and not as a new file.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const inotifyMask = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MODIFY |
	syscall.IN_ATTRIB | syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_FROM |
	syscall.IN_MOVED_TO | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF

type inotifyBackend struct {
	w    *watcher
	f    *os.File
	fd   int
	mu   sync.Mutex
	dirs map[int32]string // wd → directory path
	wds  map[string]int32 // directory path → wd
	// helpers: ancestors of a missing anchor, watched only to see it return.
	helpers map[string]bool
	// names: the entries of each watched directory (not of helpers).
	names map[string]map[string]bool
	done  chan struct{}
}

func newWatchBackend(w *watcher) (watchBackend, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	b := &inotifyBackend{
		w:       w,
		f:       os.NewFile(uintptr(fd), "inotify"),
		fd:      fd,
		dirs:    map[int32]string{},
		wds:     map[string]int32{},
		helpers: map[string]bool{},
		names:   map[string]map[string]bool{},
		done:    make(chan struct{}),
	}
	for _, r := range w.roots {
		dir := r.path
		if !r.isDir {
			// A file root is watched through its directory, so an editor's
			// "write a temp file, rename it over" is seen as a modification.
			dir = filepath.Dir(r.path)
			if err := b.addDir(dir); err != nil {
				b.f.Close()
				return nil, err
			}
			continue
		}
		if err := b.addTree(dir, false); err != nil {
			b.f.Close()
			return nil, err
		}
	}
	go b.loop()
	return b, nil
}

func (b *inotifyBackend) addDir(dir string) error {
	wd, err := syscall.InotifyAddWatch(b.fd, dir, inotifyMask)
	if err != nil {
		return fmt.Errorf("inotify_add_watch %s: %w", dir, err)
	}
	// Listed after the watch exists: a name created meanwhile is both
	// listed and reported, never missed.
	names := map[string]bool{}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			names[e.Name()] = true
		}
	}
	b.mu.Lock()
	b.dirs[int32(wd)] = dir
	b.wds[dir] = int32(wd)
	delete(b.helpers, dir)
	b.names[dir] = names
	b.mu.Unlock()
	return nil
}

// addTree watches dir and, when recursive, every directory below it that is
// not ignored. With report, every entry found is reported as Created: a
// directory that appeared may have been filled before its watch existed.
func (b *inotifyBackend) addTree(dir string, report bool) error {
	if err := b.addDir(dir); err != nil {
		return err
	}
	if !b.w.opts.recursive && !report {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // vanished meanwhile: its removal is reported by the parent
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if report && b.w.wants(p) {
			b.w.send(rawEvent{kind: watchCreated, path: p})
		}
		if e.IsDir() && b.w.opts.recursive {
			if rel, ok := b.w.relTo(p); ok && b.w.ignored(rel) {
				continue
			}
			_ = b.addTree(p, report)
		}
	}
	return nil
}

// dropTree removes the watches on dir and every directory below it.
func (b *inotifyBackend) dropTree(dir string) {
	prefix := dir + string(filepath.Separator)
	b.mu.Lock()
	var drop []int32
	for p, wd := range b.wds {
		if p == dir || strings.HasPrefix(p, prefix) {
			drop = append(drop, wd)
			delete(b.wds, p)
			delete(b.dirs, wd)
			delete(b.helpers, p)
			delete(b.names, p)
		}
	}
	b.mu.Unlock()
	for _, wd := range drop {
		_, _ = syscall.InotifyRmWatch(b.fd, uint32(wd))
	}
}

// dropWatch removes the watch wd (on dir) and every watch below dir, unless
// dir already has a newer watch.
func (b *inotifyBackend) dropWatch(wd int32, dir string) {
	b.mu.Lock()
	current := b.wds[dir] == wd
	delete(b.dirs, wd)
	b.mu.Unlock()
	_, _ = syscall.InotifyRmWatch(b.fd, uint32(wd))
	if current {
		b.dropTree(dir)
	}
}

// anchors: the directories a watch hangs from — each directory root, and
// the directory of each file root.
func (b *inotifyBackend) anchors() []watchRoot {
	var out []watchRoot
	seen := map[string]bool{}
	for _, r := range b.w.roots {
		a := watchRoot{path: r.path, isDir: true}
		if !r.isDir {
			a.path = filepath.Dir(r.path)
			a.isDir = false // the directory of a file root: never recursed
		}
		if !seen[a.path] {
			seen[a.path] = true
			out = append(out, a)
		}
	}
	return out
}

func inotifyIsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// reconcileAnchors watches every anchor that exists and has no watch (its
// entries are reported Created: nothing is known of what it held before),
// and, for one that does not exist, its nearest existing ancestor as a
// helper. Helpers no longer needed are dropped.
func (b *inotifyBackend) reconcileAnchors() {
	for round := 0; round < 4; round++ {
		needed := map[string]bool{}
		added := false
		for _, a := range b.anchors() {
			b.mu.Lock()
			_, watched := b.wds[a.path]
			isHelper := b.helpers[a.path]
			b.mu.Unlock()
			if watched && !isHelper {
				continue
			}
			if inotifyIsDir(a.path) {
				if isHelper {
					b.dropTree(a.path)
				}
				if a.isDir {
					if b.addTree(a.path, true) == nil {
						continue
					}
				} else if b.addDir(a.path) == nil {
					for _, r := range b.w.roots {
						if !r.isDir && filepath.Dir(r.path) == a.path {
							if _, err := os.Lstat(r.path); err == nil {
								b.w.send(rawEvent{kind: watchCreated, path: r.path})
							}
						}
					}
					continue
				}
			}
			for p := filepath.Dir(a.path); ; p = filepath.Dir(p) {
				if inotifyIsDir(p) {
					needed[p] = true
					b.mu.Lock()
					_, has := b.wds[p]
					b.mu.Unlock()
					if !has {
						if wd, err := syscall.InotifyAddWatch(b.fd, p, inotifyMask); err == nil {
							b.mu.Lock()
							b.dirs[int32(wd)] = p
							b.wds[p] = int32(wd)
							b.helpers[p] = true
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
		var drop []string
		b.mu.Lock()
		for p := range b.helpers {
			if !needed[p] {
				drop = append(drop, p)
			}
		}
		b.mu.Unlock()
		for _, p := range drop {
			b.mu.Lock()
			wd, ok := b.wds[p]
			if ok {
				delete(b.wds, p)
				delete(b.dirs, wd)
			}
			delete(b.helpers, p)
			delete(b.names, p)
			b.mu.Unlock()
			if ok {
				_, _ = syscall.InotifyRmWatch(b.fd, uint32(wd))
			}
		}
		// A path that appeared before a new helper's watch existed raised
		// no event on it: check once more.
		if !added {
			return
		}
	}
}

func (b *inotifyBackend) close() {
	b.f.Close()
	<-b.done
}

func (b *inotifyBackend) loop() {
	defer close(b.done)
	buf := make([]byte, 64<<10)
	for {
		n, err := b.f.Read(buf)
		if err != nil {
			return
		}
		off := 0
		for off+syscall.SizeofInotifyEvent <= n {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameLen := int(ev.Len)
			name := ""
			if nameLen > 0 {
				raw := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+nameLen]
				for i, c := range raw {
					if c == 0 {
						raw = raw[:i]
						break
					}
				}
				name = string(raw)
			}
			b.handle(ev.Wd, ev.Mask, ev.Cookie, name)
			off += syscall.SizeofInotifyEvent + nameLen
		}
	}
}

func (b *inotifyBackend) handle(wd int32, mask, cookie uint32, name string) {
	if mask&syscall.IN_Q_OVERFLOW != 0 {
		b.w.send(rawEvent{overflow: true})
		return
	}
	b.mu.Lock()
	dir, ok := b.dirs[wd]
	b.mu.Unlock()
	if !ok {
		return
	}
	b.mu.Lock()
	helper := b.helpers[dir]
	b.mu.Unlock()
	if mask&syscall.IN_IGNORED != 0 {
		b.mu.Lock()
		delete(b.dirs, wd)
		if b.wds[dir] == wd {
			delete(b.wds, dir)
			delete(b.helpers, dir)
			delete(b.names, dir)
		}
		b.mu.Unlock()
		b.reconcileAnchors()
		return
	}
	if mask&(syscall.IN_MOVE_SELF|syscall.IN_DELETE_SELF) != 0 {
		// This directory left its path (or was deleted). An anchor is
		// re-armed on the path; any other directory was re-armed by its
		// parent's IN_MOVED_TO / IN_CREATE, and its stale watch goes.
		b.dropWatch(wd, dir)
		b.reconcileAnchors()
		return
	}
	if helper {
		// Something appeared or went in an ancestor of a missing anchor.
		b.reconcileAnchors()
		return
	}
	if name == "" {
		return // an event on the watched directory itself
	}
	path := filepath.Join(dir, name)
	isDir := mask&syscall.IN_ISDIR != 0
	pair := ""
	if cookie != 0 {
		pair = fmt.Sprintf("cookie:%d", cookie)
	}
	switch {
	case mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0:
		b.mu.Lock()
		existed := false
		if set := b.names[dir]; set != nil {
			existed = set[name]
			set[name] = true
		}
		b.mu.Unlock()
		if existed && b.w.wants(path) {
			// Moved onto an existing name: the old file is replaced. Removed
			// then Created of one path coalesces to Modified.
			b.w.send(rawEvent{kind: watchRemoved, path: path})
		}
		if b.w.wants(path) {
			b.w.send(rawEvent{kind: watchCreated, path: path, pairKey: pair})
		}
		if isDir && b.w.opts.recursive {
			if rel, ok := b.w.relTo(path); ok && !b.w.ignored(rel) {
				_ = b.addTree(path, true)
			}
		}
	case mask&(syscall.IN_DELETE|syscall.IN_MOVED_FROM) != 0:
		b.mu.Lock()
		if set := b.names[dir]; set != nil {
			delete(set, name)
		}
		b.mu.Unlock()
		if b.w.wants(path) {
			b.w.send(rawEvent{kind: watchRemoved, path: path, pairKey: pair})
		}
		if isDir && mask&syscall.IN_MOVED_FROM != 0 {
			// The directory left this path: its watches (and those below
			// it) must not keep reporting the old path. A move within the
			// tree re-adds them under the new path (IN_MOVED_TO).
			b.dropTree(path)
		}
	case mask&(syscall.IN_MODIFY|syscall.IN_ATTRIB|syscall.IN_CLOSE_WRITE) != 0:
		if !isDir && b.w.wants(path) {
			b.w.send(rawEvent{kind: watchModified, path: path})
		}
	}
}
