//go:build linux && !js

package rt

// watch_inotify.go — Std.Watch on Linux, with inotify from the standard
// library (syscall.InotifyInit1 / InotifyAddWatch). The descriptor is
// non-blocking and wrapped in *os.File, so reads park in the Go poller and
// close() wakes them.

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
	done chan struct{}
}

func newWatchBackend(w *watcher) (watchBackend, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	b := &inotifyBackend{
		w:    w,
		f:    os.NewFile(uintptr(fd), "inotify"),
		fd:   fd,
		dirs: map[int32]string{},
		wds:  map[string]int32{},
		done: make(chan struct{}),
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
	b.mu.Lock()
	b.dirs[int32(wd)] = dir
	b.wds[dir] = int32(wd)
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
		}
	}
	b.mu.Unlock()
	for _, wd := range drop {
		_, _ = syscall.InotifyRmWatch(b.fd, uint32(wd))
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
	if mask&syscall.IN_IGNORED != 0 {
		b.mu.Lock()
		delete(b.dirs, wd)
		if b.wds[dir] == wd {
			delete(b.wds, dir)
		}
		b.mu.Unlock()
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
		if b.w.wants(path) {
			b.w.send(rawEvent{kind: watchCreated, path: path, pairKey: pair})
		}
		if isDir && b.w.opts.recursive {
			if rel, ok := b.w.relTo(path); ok && !b.w.ignored(rel) {
				_ = b.addTree(path, true)
			}
		}
	case mask&(syscall.IN_DELETE|syscall.IN_MOVED_FROM) != 0:
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
