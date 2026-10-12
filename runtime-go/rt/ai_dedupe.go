//go:build !js

package rt

// ai_dedupe.go — the seen-key set behind `Std.Ai.Dataset.dedupeBy`, with a
// memory bound.
//
//	Ai_dedupeOpen  : String -> Int -> Task Error String
//	Ai_dedupeAdd   : String -> List String -> Task Error (List Bool)
//	Ai_dedupeClose : String -> Task Error Int
//
// A dataset can be far larger than memory, and dedupe must remember every
// key it has seen. The set keeps the SHA-256 of each key (32 bytes, never the
// key itself, which can be personal data). It holds at most `limit` digests
// in memory; when it is full it writes them, sorted, to a run file under its
// work directory and starts again with an empty map. A lookup checks the map,
// then each run by binary search on the file (32-byte records, read with
// ReadAt). When there are more than aiDedupeMaxRuns runs they are merged into
// one by a streaming merge, so a lookup reads a bounded number of files.
//
// Memory is bounded by `limit` digests plus one buffered reader per run while
// merging. The work directory is removed on close; a directory left by a
// process that died is removed by the next open after a day.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// aiDedupeMaxRuns is the most run files a set keeps before it merges them.
const aiDedupeMaxRuns = 8

// aiDedupeKey is the SHA-256 of a key.
type aiDedupeKey = [sha256.Size]byte

// aiDedupeSet is one dedupe step's seen keys.
type aiDedupeSet struct {
	mu    sync.Mutex
	dir   string
	limit int
	mem   map[aiDedupeKey]struct{}
	runs  []string // sorted run files, each a sequence of 32-byte digests
	count int      // distinct keys seen
	seq   int      // next run file number
	// peak is the largest the in-memory map has been (tests read it).
	peak int
}

var (
	aiDedupeMu   sync.Mutex
	aiDedupeSets = map[string]*aiDedupeSet{}
)

// aiDedupeWorkRoot is where sets spill: `.sky/ai/work/dedupe` under the
// working directory (the project root under `sky run` and `sky ai`).
func aiDedupeWorkRoot() string {
	return filepath.Join(".sky", "ai", "work", "dedupe")
}

// newAiDedupeSet makes a set that holds at most limit digests in memory and
// spills under root.
func newAiDedupeSet(root string, limit int) (*aiDedupeSet, error) {
	if limit < 1 {
		limit = 1
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	aiDedupeSweep(root, 24*time.Hour)
	dir, err := os.MkdirTemp(root, "set-")
	if err != nil {
		return nil, err
	}
	return &aiDedupeSet{dir: dir, limit: limit, mem: make(map[aiDedupeKey]struct{})}, nil
}

// aiDedupeSweep removes set directories older than age (left by a process
// that ended before it closed its set).
func aiDedupeSweep(root string, age time.Duration) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "set-") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// add records each key and reports whether it was new. A key repeated
// within keys counts as seen from its second occurrence.
func (s *aiDedupeSet) add(keys []string) ([]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bool, len(keys))
	for i, k := range keys {
		d := sha256.Sum256([]byte(k))
		if _, ok := s.mem[d]; ok {
			continue
		}
		seen, err := s.inRuns(d)
		if err != nil {
			return nil, err
		}
		if seen {
			continue
		}
		s.mem[d] = struct{}{}
		s.count++
		out[i] = true
		if len(s.mem) > s.peak {
			s.peak = len(s.mem)
		}
		if len(s.mem) >= s.limit {
			if err := s.spill(); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// inRuns is whether a digest is in any run file.
func (s *aiDedupeSet) inRuns(d aiDedupeKey) (bool, error) {
	for _, path := range s.runs {
		ok, err := aiDedupeSearch(path, d)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// aiDedupeSearch binary-searches one sorted run file.
func aiDedupeSearch(path string, d aiDedupeKey) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	n := int(info.Size() / sha256.Size)
	var buf aiDedupeKey
	var readErr error
	i := sort.Search(n, func(i int) bool {
		if readErr != nil {
			return true
		}
		if _, err := f.ReadAt(buf[:], int64(i)*sha256.Size); err != nil {
			readErr = err
			return true
		}
		return bytes.Compare(buf[:], d[:]) >= 0
	})
	if readErr != nil {
		return false, readErr
	}
	if i >= n {
		return false, nil
	}
	if _, err := f.ReadAt(buf[:], int64(i)*sha256.Size); err != nil {
		return false, err
	}
	return buf == d, nil
}

// spill writes the in-memory digests to a new sorted run and empties the
// map, merging the runs when there are too many.
func (s *aiDedupeSet) spill() error {
	keys := make([]aiDedupeKey, 0, len(s.mem))
	for k := range s.mem {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	path := filepath.Join(s.dir, fmt.Sprintf("run-%06d", s.seq))
	s.seq++
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 64<<10)
	for _, k := range keys {
		if _, err := w.Write(k[:]); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.runs = append(s.runs, path)
	s.mem = make(map[aiDedupeKey]struct{})
	if len(s.runs) > aiDedupeMaxRuns {
		return s.merge()
	}
	return nil
}

// merge combines every run into one sorted run, streaming: it holds one
// buffered reader per run, never the runs' contents.
func (s *aiDedupeSet) merge() error {
	type head struct {
		r   *bufio.Reader
		f   *os.File
		cur aiDedupeKey
		ok  bool
	}
	heads := make([]*head, 0, len(s.runs))
	defer func() {
		for _, h := range heads {
			h.f.Close()
		}
	}()
	next := func(h *head) error {
		_, err := io.ReadFull(h.r, h.cur[:])
		if err == io.EOF {
			h.ok = false
			return nil
		}
		if err != nil {
			return err
		}
		h.ok = true
		return nil
	}
	for _, p := range s.runs {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h := &head{r: bufio.NewReaderSize(f, 32<<10), f: f}
		heads = append(heads, h)
		if err := next(h); err != nil {
			return err
		}
	}
	path := filepath.Join(s.dir, fmt.Sprintf("run-%06d", s.seq))
	s.seq++
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 64<<10)
	var last aiDedupeKey
	wrote := false
	for {
		var min *head
		for _, h := range heads {
			if h.ok && (min == nil || bytes.Compare(h.cur[:], min.cur[:]) < 0) {
				min = h
			}
		}
		if min == nil {
			break
		}
		if !wrote || min.cur != last {
			if _, err := w.Write(min.cur[:]); err != nil {
				out.Close()
				return err
			}
			last, wrote = min.cur, true
		}
		if err := next(min); err != nil {
			out.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	for _, p := range s.runs {
		_ = os.Remove(p)
	}
	s.runs = []string{path}
	return nil
}

// close removes the set's files and returns how many distinct keys it saw.
func (s *aiDedupeSet) close() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.RemoveAll(s.dir)
	s.mem = nil
	s.runs = nil
	return s.count
}

func aiDedupeHandle() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "dedupe-" + hex.EncodeToString(b[:])
}

func aiDedupeLookup(handle string) (*aiDedupeSet, error) {
	aiDedupeMu.Lock()
	defer aiDedupeMu.Unlock()
	s, ok := aiDedupeSets[handle]
	if !ok {
		return nil, aiFailf(aiInvalidSetting, "Std.Ai.Dataset: dedupe set %q is closed", handle).with("setting", "dedupe")
	}
	return s, nil
}

// Ai_dedupeOpen : String -> Int -> Task Error String — a new set that spills
// under the given directory ("" for the default work directory) once it
// holds `limit` keys in memory. The result is the set's handle.
func Ai_dedupeOpen(dirArg, limitArg any) any {
	dir, limit := AsString(dirArg), AsInt(limitArg)
	return func() any {
		if dir == "" {
			dir = aiDedupeWorkRoot()
		}
		s, err := newAiDedupeSet(dir, limit)
		if err != nil {
			return aiTaskErr(aiFailf(aiInvalidSetting, "Std.Ai.Dataset: cannot create the dedupe work directory under %s: %v", dir, aiPathErr(err)).with("setting", "dedupe"))
		}
		h := aiDedupeHandle()
		aiDedupeMu.Lock()
		aiDedupeSets[h] = s
		aiDedupeMu.Unlock()
		return Ok[any, any](h)
	}
}

// Ai_dedupeAdd : String -> List String -> Task Error (List Bool) — record
// keys; each result is True for a key not seen before.
func Ai_dedupeAdd(handleArg, keysArg any) any {
	handle := AsString(handleArg)
	items := AsList(keysArg)
	return func() any {
		s, err := aiDedupeLookup(handle)
		if err != nil {
			return aiTaskErr(err)
		}
		keys := make([]string, len(items))
		for i, k := range items {
			keys[i] = AsString(k)
		}
		fresh, err := s.add(keys)
		if err != nil {
			return aiTaskErr(aiFailf(aiDatasetInvalid, "dedupe: the spill files under %s failed: %v", s.dir, aiPathErr(err)).
				with("dataset", "dedupe").with("reasons", "spill files failed"))
		}
		out := make([]any, len(fresh))
		for i, b := range fresh {
			out[i] = b
		}
		return Ok[any, any](out)
	}
}

// Ai_dedupeClose : String -> Task Error Int — remove the set's files and
// return the number of distinct keys it saw. Closing twice is harmless.
func Ai_dedupeClose(handleArg any) any {
	handle := AsString(handleArg)
	return func() any {
		aiDedupeMu.Lock()
		s, ok := aiDedupeSets[handle]
		delete(aiDedupeSets, handle)
		aiDedupeMu.Unlock()
		if !ok {
			return Ok[any, any](0)
		}
		return Ok[any, any](s.close())
	}
}
