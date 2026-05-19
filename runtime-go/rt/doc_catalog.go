// doc_catalog.go — Go-side fast loader + searcher for sky-doc.
//
// The Sky.Tui doc browser would otherwise need to decode the
// 80k-entry symbols.json catalogue (skyshop with Stripe SDK) via
// Sky.Core.Json.Decode, which does 5–6 FFI calls per entry and
// can't finish in under several minutes.  Instead the catalogue
// is parsed once in pure Go (encoding/json — ~250 ms for 80k
// entries), cached per path, and exposed to Sky as two opaque
// kernel functions:
//
//   * `Doc_loadCatalog : String -> Result Error Int`
//     Reads + parses the JSON at `path`.  Returns total entry
//     count or an Error.  Caches the parsed index under `path`
//     so subsequent calls (e.g. after a navigation reload) skip
//     the parse.
//
//   * `Doc_searchCatalog : String -> String -> Int -> (List String, Int)`
//     Runs a case-insensitive substring search against the cached
//     index for `path`.  Returns at most `maxShown` pre-formatted
//     display lines plus the TOTAL match count.  Pure compute —
//     no I/O — fast enough to run on every keystroke.
//
// The display line layout is identical to what the previous Sky-
// side `entryDecoder` built: an 8-col padded bucket, then
// "Mod.name", optionally followed by ": sig" (trimmed to 80
// cols).  Keeping it pre-formatted means the Sky.Tui render loop
// only walks one Std.Ui element per row.

package rt

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

type docEntry struct {
	mod    string
	name   string
	sig    string
	bucket string
	// pre-lowercased for case-insensitive substring search
	modL  string
	nameL string
	sigL  string
	// pre-built display line ("stdlib  Module.name : sig …")
	line string
}

type docCatalog struct {
	entries []docEntry
}

// raw shape of one entry in the JSON catalogue (matches
// Sky/Doc/Render.hs's toSearchCatalog output).
type docRawEntry struct {
	Module string `json:"module"`
	Name   string `json:"name"`
	Sig    string `json:"sig"`
	Bucket string `json:"bucket"`
}

type docCatalogRoot struct {
	Entries []docRawEntry `json:"entries"`
}

// per-process cache of loaded catalogues, keyed by file path.
// Sky.Tui drives one path per session so the map stays at size 1
// in practice; the keyed shape is for testability + future
// multi-source merges.
var (
	docCatalogCache   sync.Map // map[string]*docCatalog
	docCatalogLoading sync.Map // map[string]*sync.Mutex — prevents concurrent parse of the same file
)

func docTrimSig(s string) string {
	// Match the Sky-side behaviour: keep ≤80 chars verbatim,
	// otherwise slice to 78 + add the ellipsis.
	if len(s) <= 80 {
		return s
	}
	// Slice by bytes — sigs are pure ASCII type signatures.
	return s[:78] + "…"
}

func docPad8(s string) string {
	if len(s) >= 8 {
		return s
	}
	return s + strings.Repeat(" ", 8-len(s))
}

func docBuildLine(e docRawEntry) string {
	var b strings.Builder
	b.Grow(len(e.Module) + len(e.Name) + len(e.Sig) + 16)
	b.WriteString(docPad8(e.Bucket))
	b.WriteString(e.Module)
	b.WriteByte('.')
	b.WriteString(e.Name)
	if e.Sig != "" {
		b.WriteString("  : ")
		b.WriteString(docTrimSig(e.Sig))
	}
	return b.String()
}

func docLoadCatalog(path string) (*docCatalog, error) {
	if cached, ok := docCatalogCache.Load(path); ok {
		return cached.(*docCatalog), nil
	}
	// Serialise concurrent loads of the same path so the first
	// keystroke after launch doesn't spawn N parses.
	muAny, _ := docCatalogLoading.LoadOrStore(path, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if cached, ok := docCatalogCache.Load(path); ok {
		return cached.(*docCatalog), nil
	}

	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var root docCatalogRoot
	if err := json.Unmarshal(bytes, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	entries := make([]docEntry, len(root.Entries))
	for i, r := range root.Entries {
		entries[i] = docEntry{
			mod:    r.Module,
			name:   r.Name,
			sig:    r.Sig,
			bucket: r.Bucket,
			modL:   strings.ToLower(r.Module),
			nameL:  strings.ToLower(r.Name),
			sigL:   strings.ToLower(r.Sig),
			line:   docBuildLine(r),
		}
	}
	cat := &docCatalog{entries: entries}
	docCatalogCache.Store(path, cat)
	return cat, nil
}

func (c *docCatalog) search(query string, maxShown int) ([]string, int) {
	lq := strings.TrimSpace(strings.ToLower(query))
	out := make([]string, 0, maxShown)
	total := 0
	if lq == "" {
		// Empty query — take the prefix and count the rest.
		for i, e := range c.entries {
			_ = e
			total++
			if i < maxShown {
				out = append(out, c.entries[i].line)
			}
		}
		return out, total
	}
	for _, e := range c.entries {
		if strings.Contains(e.nameL, lq) ||
			strings.Contains(e.modL, lq) ||
			(e.sigL != "" && strings.Contains(e.sigL, lq)) {
			total++
			if len(out) < maxShown {
				out = append(out, e.line)
			}
		}
	}
	return out, total
}

// Doc_loadCatalog — kernel binding.
//
// Sky signature: String -> Result Error Int
//
// The Int payload of Ok is the total entry count (Sky-side uses
// it for the "N / M entries" status line).
func Doc_loadCatalog(pathA any) any {
	path := AsString(pathA)
	cat, err := docLoadCatalog(path)
	if err != nil {
		return Err[any, any](ErrUnexpected(err.Error()))
	}
	return Ok[any, any](len(cat.entries))
}

// Doc_searchCatalog — kernel binding.
//
// Sky signature: String -> String -> Int -> (List String, Int)
//
// Returns (top-N display lines, TOTAL match count).  Synchronous;
// the underlying parsed catalogue is held in `docCatalogCache` so
// per-keystroke calls don't re-parse.  If the catalogue hasn't
// been loaded yet, returns ([], 0).
func Doc_searchCatalog(pathA, queryA, maxShownA any) any {
	path := AsString(pathA)
	query := AsString(queryA)
	maxShown := int(AsInt(maxShownA))
	any_cat, ok := docCatalogCache.Load(path)
	if !ok {
		return SkyTuple2{V0: docToSkyStringList(nil), V1: 0}
	}
	cat := any_cat.(*docCatalog)
	lines, total := cat.search(query, maxShown)
	return SkyTuple2{V0: docToSkyStringList(lines), V1: total}
}

// docToSkyStringList converts a Go []string to a Sky list ([]any of any).
func docToSkyStringList(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
