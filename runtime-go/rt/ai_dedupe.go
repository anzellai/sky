//go:build !js

package rt

// ai_dedupe.go — the seen-key set behind `Std.Ai.Dataset.dedupeBy` and the
// duplicate-row check of `inspect` / `validate`, with a memory bound.
//
//	Ai_dedupeOpen  : String -> Int -> Task Error String
//	Ai_dedupeAdd   : String -> List String -> Task Error (List Bool)
//	Ai_dedupeFirst : String -> Int -> List String -> Task Error (List Int)
//	Ai_dedupeClose : String -> Task Error Int
//
// A dataset can be far larger than memory, and dedupe must remember every
// key it has seen. The set keeps the SHA-256 of each key (32 bytes, never the
// key itself, which can be personal data), with the row number where the key
// was first seen (`inspect` reports a repeat as "row N repeats row M"). It
// holds at most `limit` digests in memory; when it is full it writes them,
// sorted, to a run file under its work directory and starts again with an
// empty map. A lookup checks the map, then each run by binary search on the
// file (40-byte records: the digest, then the row as a big-endian uint64,
// read with ReadAt). When there are more than aiDedupeMaxRuns runs they are merged into
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
	"encoding/binary"
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

// aiDedupeRecord is the size of one run-file record: the digest, then the
// row where the key was first seen.
const aiDedupeRecord = sha256.Size + 8

// aiDedupeSet is one dedupe step's seen keys.
type aiDedupeSet struct {
	mu    sync.Mutex
	dir   string
	limit int
	mem   map[aiDedupeKey]uint64 // digest -> row first seen at
	runs  []string               // sorted run files of aiDedupeRecord records
	count int                    // distinct keys seen
	seq   int                    // next run file number
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
	return &aiDedupeSet{dir: dir, limit: limit, mem: make(map[aiDedupeKey]uint64)}, nil
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
	first, err := s.first(0, keys)
	if err != nil {
		return nil, err
	}
	out := make([]bool, len(keys))
	for i, f := range first {
		out[i] = f == 0
	}
	return out, nil
}

// first records each key as seen at row start+i (rows count from 1) and
// returns, per key, the row where it was first seen, or 0 when it is new. A
// key repeated within keys is new at its first occurrence and a repeat of
// that row after. With start 0 every key is recorded at row 0, and only the
// new/seen answer (0 or not) is meaningful: `add` uses it that way.
func (s *aiDedupeSet) first(start int, keys []string) ([]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uint64, len(keys))
	for i, k := range keys {
		d := sha256.Sum256([]byte(k))
		row := uint64(0)
		if start > 0 {
			row = uint64(start + i)
		}
		if prev, ok := s.mem[d]; ok {
			out[i] = aiDedupeSeenRow(prev)
			continue
		}
		prev, seen, err := s.inRuns(d)
		if err != nil {
			return nil, err
		}
		if seen {
			out[i] = aiDedupeSeenRow(prev)
			continue
		}
		s.mem[d] = row
		s.count++
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

// aiDedupeSeenRow is the answer for a key seen before: its row, or 1 for a
// set that records no rows (a seen key must never answer 0, "new").
func aiDedupeSeenRow(row uint64) uint64 {
	if row == 0 {
		return 1
	}
	return row
}

// inRuns looks a digest up in the run files: its row and whether it is there.
func (s *aiDedupeSet) inRuns(d aiDedupeKey) (uint64, bool, error) {
	for _, path := range s.runs {
		row, ok, err := aiDedupeSearch(path, d)
		if err != nil || ok {
			return row, ok, err
		}
	}
	return 0, false, nil
}

// aiDedupeSearch binary-searches one sorted run file.
func aiDedupeSearch(path string, d aiDedupeKey) (uint64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	n := int(info.Size() / aiDedupeRecord)
	var buf [aiDedupeRecord]byte
	var readErr error
	i := sort.Search(n, func(i int) bool {
		if readErr != nil {
			return true
		}
		if _, err := f.ReadAt(buf[:], int64(i)*aiDedupeRecord); err != nil {
			readErr = err
			return true
		}
		return bytes.Compare(buf[:sha256.Size], d[:]) >= 0
	})
	if readErr != nil {
		return 0, false, readErr
	}
	if i >= n {
		return 0, false, nil
	}
	if _, err := f.ReadAt(buf[:], int64(i)*aiDedupeRecord); err != nil {
		return 0, false, err
	}
	if !bytes.Equal(buf[:sha256.Size], d[:]) {
		return 0, false, nil
	}
	return binary.BigEndian.Uint64(buf[sha256.Size:]), true, nil
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
	var rec [aiDedupeRecord]byte
	for _, k := range keys {
		copy(rec[:], k[:])
		binary.BigEndian.PutUint64(rec[sha256.Size:], s.mem[k])
		if _, err := w.Write(rec[:]); err != nil {
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
	s.mem = make(map[aiDedupeKey]uint64)
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
		cur [aiDedupeRecord]byte
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
			if h.ok && (min == nil || bytes.Compare(h.cur[:sha256.Size], min.cur[:sha256.Size]) < 0) {
				min = h
			}
		}
		if min == nil {
			break
		}
		var d aiDedupeKey
		copy(d[:], min.cur[:sha256.Size])
		if !wrote || d != last {
			if _, err := w.Write(min.cur[:]); err != nil {
				out.Close()
				return err
			}
			last, wrote = d, true
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

// Ai_dedupeFirst : String -> Int -> List String -> Task Error (List Int) —
// record keys as rows start, start+1, …; each result is the row where the
// key was first seen, or 0 for a key not seen before. `inspect` uses it to
// report "row N repeats row M" without holding every row's key in memory.
func Ai_dedupeFirst(handleArg, startArg, keysArg any) any {
	handle := AsString(handleArg)
	start := AsInt(startArg)
	items := AsList(keysArg)
	return func() any {
		s, err := aiDedupeLookup(handle)
		if err != nil {
			return aiTaskErr(err)
		}
		if start < 1 {
			start = 1
		}
		keys := make([]string, len(items))
		for i, k := range items {
			keys[i] = AsString(k)
		}
		first, err := s.first(start, keys)
		if err != nil {
			return aiTaskErr(aiFailf(aiDatasetInvalid, "inspect: the spill files under %s failed: %v", s.dir, aiPathErr(err)).
				with("dataset", "inspect").with("reasons", "spill files failed"))
		}
		out := make([]any, len(first))
		for i, f := range first {
			out[i] = int(f)
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
