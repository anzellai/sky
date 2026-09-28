//go:build !js

package rt

// watch.go — Std.Watch: file-system change notification.
//
//	Sky side                         Runtime side
//	────────                         ────────────
//	Watch.watch paths opts         → Watch_watch: start the OS backend
//	                                 (inotify on Linux, watch_inotify.go;
//	                                 kqueue on macOS, watch_kqueue.go) and
//	                                 the coalescer goroutine.
//	Watch.next w                   → Watch_next (Task consumer)
//	Watch.changes w toMsg          → Watch_changes (Sub consumer, sub_source.go)
//	Watch.close w                  → Watch_close
//
// Both backends use the Go standard library only (syscall): inotify and
// kqueue are what the usual third-party watcher wraps, and fsnotify is not a
// dependency of the runtime, so adding it would buy no capability.
//
// # Coalescing
//
// Editors and build tools change files in bursts (write a temp file, rename
// it over the original, touch the directory). The backend's raw events go to
// a coalescer that waits until the tree has been quiet for the debounce
// window (default 50 ms, and at most 10 windows from the first change of a
// burst, so a never-quiet tree still reports), then emits ONE batch in which
// each path appears once:
//
//	Created then Modified  → Created      Created then Removed  → (nothing)
//	Removed then Created   → Modified     Modified then Removed → Removed
//	a Removed and a Created of the same file (inode / rename cookie)
//	                        → Renamed old new
//
// # Overflow
//
// When the OS event queue overflowed (inotify IN_Q_OVERFLOW), or the
// runtime's own queue did because nobody consumed batches, events were lost.
// The next batch is then exactly `[ Overflow ]`: the app should rescan what
// it watches. Pending changes are discarded, since the rescan supersedes
// them.
//
// # One consumer mode
//
// Like Sky.Core.Process: the first `next` or `changes` fixes the mode for
// the watcher's life; the other is refused (`next` returns Err InvalidInput,
// a `changes` Sub is ignored and logged).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Change tags, in the Sky `Change` declaration order.
const (
	watchCreated = iota
	watchModified
	watchRemoved
	watchRenamed
	watchOverflow
)

// Queue bounds. Variables so tests can shrink them.
var (
	// watchRawQueueCap bounds raw events waiting for the coalescer.
	watchRawQueueCap = 8192
	// watchBatchQueueCap bounds batches waiting for a consumer.
	watchBatchQueueCap = 256
)

type rawEvent struct {
	kind     int    // watchCreated / watchModified / watchRemoved / watchOverflow
	path     string // full path as reported to Sky
	pairKey  string // rename pairing: "ino:<n>" or "cookie:<n>", or ""
	overflow bool
}

type change struct {
	kind int
	path string
	to   string // Renamed only
}

type watchOptions struct {
	recursive bool
	debounce  time.Duration
	ignore    []string
}

type watchRoot struct {
	path  string // cleaned, as the caller gave it
	isDir bool
}

// watchBackend is one OS notification source.
type watchBackend interface {
	close()
}

type watcher struct {
	id    int64
	roots []watchRoot
	opts  watchOptions

	raw     chan rawEvent
	stop    chan struct{}
	stopped chan struct{} // coalescer exited

	backend watchBackend

	mu        sync.Mutex
	batchCap  int // bound on queued batches (watchBatchQueueCap at start)
	batches   [][]change
	overflow  bool // the next batch is [Overflow]
	changed   chan struct{}
	closed    bool
	subActive bool
	sess      *liveSession

	owner     atomic.Int32
	closeOnce sync.Once
}

var (
	watchRegistry sync.Map // map[int64]*watcher
	watchIDs      atomic.Int64
)

func lookupWatcher(idArg any) (*watcher, any) {
	id := int64(AsInt(idArg))
	if v, ok := watchRegistry.Load(id); ok {
		return v.(*watcher), nil
	}
	return nil, ErrInvalidInput(fmt.Sprintf("Watch: no watcher %d (it was closed, or its session ended)", id))
}

// send hands one raw event to the coalescer without ever blocking the
// backend: a full queue is an overflow (the app rescans).
func (w *watcher) send(ev rawEvent) {
	if ev.overflow {
		w.markOverflow()
		return
	}
	select {
	case w.raw <- ev:
	default:
		w.markOverflow()
	}
}

// ignored reports whether rel (slash-separated, relative to its root) is
// excluded. A pattern without "/" matches any single path component
// (".git", "*.swp", "node_modules"); a pattern with "/" matches the relative
// path or any leading directory of it ("build/*.o", "vendor/cache").
func (w *watcher) ignored(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	for _, pat := range w.opts.ignore {
		if pat == "" {
			continue
		}
		if !strings.Contains(pat, "/") {
			for _, p := range parts {
				if ok, _ := filepath.Match(pat, p); ok {
					return true
				}
			}
			continue
		}
		for i := 1; i <= len(parts); i++ {
			if ok, _ := filepath.Match(pat, strings.Join(parts[:i], "/")); ok {
				return true
			}
		}
	}
	return false
}

// relTo returns path relative to the root that contains it.
func (w *watcher) relTo(path string) (string, bool) {
	for _, r := range w.roots {
		if !r.isDir {
			if path == r.path {
				return filepath.Base(path), true
			}
			continue
		}
		if path == r.path {
			return ".", true
		}
		if strings.HasPrefix(path, r.path+string(filepath.Separator)) {
			return path[len(r.path)+1:], true
		}
	}
	return "", false
}

// wants reports whether an event on path should reach the app: it is under a
// root, not ignored, and (non-recursive) directly inside a directory root.
func (w *watcher) wants(path string) bool {
	rel, ok := w.relTo(path)
	if !ok || rel == "." {
		return false
	}
	if w.ignored(rel) {
		return false
	}
	if !w.opts.recursive {
		for _, r := range w.roots {
			if r.isDir && filepath.Dir(path) == r.path {
				return true
			}
			if !r.isDir && path == r.path {
				return true
			}
		}
		return false
	}
	return true
}

// ── coalescer ────────────────────────────────────────────────────────────

type pendingEntry struct {
	path    string
	kind    int
	pairKey string // of the event that set kind (Created / Removed only)
	to      string // Renamed
	gone    bool   // cancelled out (Created then Removed)
}

type pendingSet struct {
	order []*pendingEntry
	byKey map[string]*pendingEntry
}

func newPendingSet() *pendingSet { return &pendingSet{byKey: map[string]*pendingEntry{}} }

func (p *pendingSet) empty() bool { return len(p.order) == 0 }

func (p *pendingSet) add(ev rawEvent) {
	e, ok := p.byKey[ev.path]
	if !ok || e.gone {
		e = &pendingEntry{path: ev.path, kind: ev.kind, pairKey: ev.pairKey}
		p.byKey[ev.path] = e
		p.order = append(p.order, e)
		return
	}
	switch {
	case e.kind == watchCreated && (ev.kind == watchModified || ev.kind == watchCreated):
		// still Created (a backend may report a new entry twice)
	case e.kind == watchCreated && ev.kind == watchRemoved:
		e.gone = true
		delete(p.byKey, ev.path)
	case e.kind == watchRemoved && ev.kind == watchCreated:
		e.kind, e.pairKey = watchModified, ""
	case ev.kind == watchRemoved:
		e.kind, e.pairKey = watchRemoved, ev.pairKey
	case ev.kind == watchCreated:
		e.kind, e.pairKey = watchModified, ""
	default:
		// Modified on top of Modified stays Modified.
	}
}

// batch resolves the pending set into the emitted batch, pairing a Removed
// and a Created that share a pair key into one Renamed.
func (p *pendingSet) batch() []change {
	createdByKey := map[string]*pendingEntry{}
	for _, e := range p.order {
		if !e.gone && e.kind == watchCreated && e.pairKey != "" {
			createdByKey[e.pairKey] = e
		}
	}
	var out []change
	for _, e := range p.order {
		if e.gone {
			continue
		}
		if e.kind == watchRemoved && e.pairKey != "" {
			if to, ok := createdByKey[e.pairKey]; ok && !to.gone {
				out = append(out, change{kind: watchRenamed, path: e.path, to: to.path})
				to.gone = true
				continue
			}
		}
		out = append(out, change{kind: e.kind, path: e.path})
	}
	return out
}

// coalesce runs until stop. It never blocks the backend (send is
// non-blocking) and never blocks on a consumer (batches are queued, bounded).
func (w *watcher) coalesce() {
	defer close(w.stopped)
	pending := newPendingSet()
	var quiet, cap *time.Timer
	var quietC, capC <-chan time.Time
	flushTimersOff := func() {
		if quiet != nil {
			quiet.Stop()
		}
		if cap != nil {
			cap.Stop()
		}
		quiet, cap, quietC, capC = nil, nil, nil, nil
	}
	defer flushTimersOff()
	maxLatency := 10 * w.opts.debounce
	flush := func() {
		flushTimersOff()
		if pending.empty() {
			return
		}
		b := pending.batch()
		pending = newPendingSet()
		if len(b) > 0 {
			w.pushBatch(b)
		}
	}
	for {
		select {
		case <-w.stop:
			return
		case ev := <-w.raw:
			pending.add(ev)
			if quiet == nil {
				quiet = time.NewTimer(w.opts.debounce)
				cap = time.NewTimer(maxLatency)
				quietC, capC = quiet.C, cap.C
			} else {
				if !quiet.Stop() {
					select {
					case <-quiet.C:
					default:
					}
				}
				quiet.Reset(w.opts.debounce)
			}
		case <-quietC:
			flush()
		case <-capC:
			flush()
		}
	}
}

func (w *watcher) signalLocked() {
	close(w.changed)
	w.changed = make(chan struct{})
}

func (w *watcher) pushBatch(b []change) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overflow {
		// An Overflow is pending: the rescan it asks for covers b too.
		w.signalLocked()
		return
	}
	if len(w.batches) >= w.batchCap {
		// Nobody consumed: the app must rescan.
		w.batches = nil
		w.overflow = true
		w.signalLocked()
		return
	}
	w.batches = append(w.batches, b)
	w.signalLocked()
}

func (w *watcher) markOverflow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = nil
	w.overflow = true
	w.signalLocked()
}

// take removes the next batch. ok=false: nothing queued. closed=true: the
// watcher was closed and nothing is queued.
func (w *watcher) take() (b []change, ok bool, closed bool, changed <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overflow {
		w.overflow = false
		return []change{{kind: watchOverflow}}, true, false, w.changed
	}
	if len(w.batches) > 0 {
		b = w.batches[0]
		w.batches = w.batches[1:]
		return b, true, false, w.changed
	}
	return nil, false, w.closed, w.changed
}

// ── lifecycle ────────────────────────────────────────────────────────────

func parseWatchOptions(opts any) watchOptions {
	o := watchOptions{
		recursive: AsBool(recordField(opts, "Recursive", "recursive")),
		debounce:  time.Duration(AsInt(recordField(opts, "DebounceMs", "debounceMs"))) * time.Millisecond,
	}
	if o.debounce <= 0 {
		o.debounce = 50 * time.Millisecond
	}
	for _, p := range AsList(recordField(opts, "Ignore", "ignore")) {
		o.ignore = append(o.ignore, asBytesString(p))
	}
	return o
}

func startWatcher(paths []string, opts watchOptions) (*watcher, any) {
	if len(paths) == 0 {
		return nil, ErrInvalidInput("Watch.watch: no paths given")
	}
	w := &watcher{
		opts:     opts,
		raw:      make(chan rawEvent, watchRawQueueCap),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
		changed:  make(chan struct{}),
		batchCap: watchBatchQueueCap,
	}
	for _, p := range paths {
		if p == "" {
			return nil, ErrInvalidInput("Watch.watch: an empty path")
		}
		clean := filepath.Clean(p)
		st, err := os.Stat(clean)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, makeError(5, "NotFound", "Watch.watch: "+clean+": no such file or directory")
			}
			return nil, ErrIo("Watch.watch: " + err.Error())
		}
		w.roots = append(w.roots, watchRoot{path: clean, isDir: st.IsDir()})
	}
	be, err := newWatchBackend(w)
	if err != nil {
		if errors.Is(err, errWatchUnsupported) {
			return nil, ErrUnavailable("Watch.watch: " + err.Error())
		}
		return nil, ErrIo("Watch.watch: " + err.Error())
	}
	w.backend = be
	w.id = watchIDs.Add(1)
	go w.coalesce()
	watchRegistry.Store(w.id, w)
	if sess := currentLiveSession(); sess != nil {
		w.sess = sess
		sess.addOwned(fmt.Sprintf("watch:%d", w.id), func() { w.shutdown() })
	}
	return w, nil
}

var errWatchUnsupported = errors.New("file watching is supported on Linux and macOS only")

// shutdown stops the backend and the coalescer, wakes every waiter and
// forgets the watcher. Idempotent.
func (w *watcher) shutdown() {
	w.closeOnce.Do(func() {
		w.backend.close()
		close(w.stop)
		<-w.stopped
		w.mu.Lock()
		w.closed = true
		w.signalLocked()
		w.mu.Unlock()
		watchRegistry.Delete(w.id)
		if w.sess != nil {
			w.sess.removeOwned(fmt.Sprintf("watch:%d", w.id))
		}
	})
}

func changeValue(c change) any {
	switch c.kind {
	case watchCreated:
		return SkyADT{Tag: watchCreated, SkyName: "Created", Fields: []any{c.path}}
	case watchModified:
		return SkyADT{Tag: watchModified, SkyName: "Modified", Fields: []any{c.path}}
	case watchRemoved:
		return SkyADT{Tag: watchRemoved, SkyName: "Removed", Fields: []any{c.path}}
	case watchRenamed:
		return SkyADT{Tag: watchRenamed, SkyName: "Renamed", Fields: []any{c.path, c.to}}
	}
	return SkyADT{Tag: watchOverflow, SkyName: "Overflow"}
}

func changesValue(b []change) []any {
	out := make([]any, len(b))
	for i, c := range b {
		out[i] = changeValue(c)
	}
	return out
}

// ── Kernels ──────────────────────────────────────────────────────────────

// Watch_watch : List String -> Options -> Task Error Int
func Watch_watch(pathsArg, optsArg any) any {
	var paths []string
	for _, p := range AsList(pathsArg) {
		paths = append(paths, asBytesString(p))
	}
	opts := parseWatchOptions(optsArg)
	return func() any {
		w, err := startWatcher(paths, opts)
		if err != nil {
			return Err[any, any](err)
		}
		return Ok[any, any](int(w.id))
	}
}

// Watch_next : Int -> Task Error (List Change)
//
// Waits for the next batch. Returns [] once the watcher is closed.
func Watch_next(idArg any) any {
	return func() any {
		w, e := lookupWatcher(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		if !(w.owner.CompareAndSwap(procOwnerNone, procOwnerTask) || w.owner.Load() == procOwnerTask) {
			return Err[any, any](ErrInvalidInput(fmt.Sprintf(
				"Watch.next: watcher %d is read by a changes Sub; a watcher has one consumer mode", w.id)))
		}
		for {
			b, ok, closed, changed := w.take()
			if ok {
				return Ok[any, any](changesValue(b))
			}
			if closed {
				return Ok[any, any]([]any{})
			}
			<-changed
		}
	}
}

// Watch_close : Int -> Task Error ()   (idempotent)
func Watch_close(idArg any) any {
	return func() any {
		if v, ok := watchRegistry.Load(int64(AsInt(idArg))); ok {
			v.(*watcher).shutdown()
		}
		return Ok[any, any](struct{}{})
	}
}

// Watch_changes : Int -> (List Change -> msg) -> Sub msg
func Watch_changes(idArg, toMsg any) SkySub {
	id := int64(AsInt(idArg))
	key := fmt.Sprintf("watch:%d", id)
	if v, ok := watchRegistry.Load(id); ok {
		return subT{kind: "subscribeSource", toMsg: toMsg, sourceKey: key, source: v.(*watcher)}
	}
	return subT{kind: "subscribeSource", toMsg: toMsg, sourceKey: key, source: deadSource{}}
}

// ── subSource ────────────────────────────────────────────────────────────

func (w *watcher) claimSub() error {
	if !(w.owner.CompareAndSwap(procOwnerNone, procOwnerSub) || w.owner.Load() == procOwnerSub) {
		return fmt.Errorf("watcher %d is read by a Task (Watch.next); a watcher has one consumer mode", w.id)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.subActive {
		return fmt.Errorf("watcher %d already has a changes Sub", w.id)
	}
	w.subActive = true
	return nil
}

func (w *watcher) releaseSub() {
	w.mu.Lock()
	w.subActive = false
	w.mu.Unlock()
}

// pump delivers each batch as one List Change until the watcher closes.
func (w *watcher) pump(stop <-chan struct{}, emit func(ev any) bool) {
	for {
		b, ok, closed, changed := w.take()
		if ok {
			if !emit(changesValue(b)) {
				// Not delivered (the Sub is stopping): put it back so the
				// next consumer still sees it.
				w.mu.Lock()
				if len(b) == 1 && b[0].kind == watchOverflow {
					w.overflow = true
				} else {
					w.batches = append([][]change{b}, w.batches...)
				}
				w.mu.Unlock()
				return
			}
			continue
		}
		if closed {
			return
		}
		select {
		case <-stop:
			return
		case <-changed:
		}
	}
}

// injectOverflow simulates an OS queue overflow (tests).
func (w *watcher) injectOverflow() { w.send(rawEvent{overflow: true}) }
