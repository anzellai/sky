//go:build !js

package rt

// Precompressed static files. `sky build` writes `<file>.br` (brotli) and
// `<file>.gz` (gzip) beside the wasm client, its loader and its boot script
// in a Sky.Spa `dist/`, and records them in `sky-precompress.json` there (see
// precompressManifest). A static mount sends one of them, unchanged, when the
// request allows its encoding, instead of gzipping the raw file on every
// request: the brotli file is about a quarter the size of an on-the-fly gzip
// and costs no CPU to send.
//
// The sidecar is used only when all of these hold, else the request takes the
// path it always took (the raw file, gzipped on the fly by gzipStatic):
//   - a GET or HEAD that accepts the sidecar's encoding;
//   - the raw file exists and is a regular file, and its Content-Type is known
//     from its extension and compressible (a type that would be sniffed is
//     never sniffed from compressed bytes);
//   - the mount's directory has a manifest written by `sky build` that lists
//     the file, the raw file's size and SHA-256 are the ones it records, and
//     so are the sidecar's. A sidecar the build did not write (one a user
//     committed in their own static directory) and a sidecar of an earlier
//     version of the file are never sent, whatever their mtimes say.
//
// A Range request that accepts the encoding is served from the sidecar (the
// representation a full GET returns, as nginx gzip_static does): the 206
// carries Content-Encoding and ranges over the sidecar's bytes. A Range
// request that accepts neither encoding gets the raw file.
//
// The response carries the raw file's Content-Type and Last-Modified (so
// If-Modified-Since works as before), an ETag of its own representation (so
// If-Range and If-None-Match tell the brotli, gzip and raw bytes apart),
// `Content-Encoding`, `Vary: Accept-Encoding` and the exact Content-Length.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// staticRoot is one directory a static mount maps request paths onto, after
// stripping prefix from the URL path.
type staticRoot struct {
	prefix string
	dir    string
}

// staticMount serves the files of dir under a URL prefix, which is stripped
// before the request path is mapped onto dir. A non-empty fallbackRoot serves a
// file that dir does not have from that directory at the same URL path (see
// staticFallThrough). Every static mount goes through here.
func staticMount(prefix, dir, fallbackRoot string) http.Handler {
	var files http.Handler = http.StripPrefix(prefix, http.FileServer(manifestHidingDir(dir)))
	roots := []staticRoot{{prefix: prefix, dir: dir}}
	if fallbackRoot != "" {
		files = staticFallThrough(files, http.StripPrefix("/", http.FileServer(manifestHidingDir(fallbackRoot))))
		roots = append(roots, staticRoot{prefix: "/", dir: fallbackRoot})
	}
	return gzipStatic(precompressedStatic(files, roots))
}

// manifestHidingDir is http.Dir with the build's precompress manifest hidden:
// the file is metadata for this server (precompressedStatic reads it), not an
// asset, so a request for it is answered as for a file that does not exist
// and a directory listing leaves it out. Only a manifest the server would use
// is hidden (loadPrecompressManifest parses it); a file of that name that is
// not one, such as a user's own file in their static directory, is served.
type manifestHidingDir string

func (d manifestHidingDir) Open(name string) (http.File, error) {
	dir := http.Dir(d)
	clean := path.Clean("/" + name)
	if strings.EqualFold(path.Base(clean), precompressManifestName) &&
		loadPrecompressManifest(filepath.Join(string(d), filepath.FromSlash(path.Dir(clean)))) != nil {
		return nil, os.ErrNotExist
	}
	f, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	return manifestHidingFile{File: f, dir: filepath.Join(string(d), filepath.FromSlash(clean))}, nil
}

// manifestHidingFile leaves a used precompress manifest out of a directory
// listing (http.FileServer lists through Readdir when the file has no
// ReadDir).
type manifestHidingFile struct {
	http.File
	dir string
}

func (f manifestHidingFile) Readdir(count int) ([]os.FileInfo, error) {
	infos, err := f.File.Readdir(count)
	if loadPrecompressManifest(f.dir) == nil {
		return infos, err
	}
	kept := infos[:0]
	for _, fi := range infos {
		if !strings.EqualFold(fi.Name(), precompressManifestName) {
			kept = append(kept, fi)
		}
	}
	return kept, err
}

// precompressedEncodings are the sidecars tried, best first. The order is
// fixed (brotli before gzip) and does not follow the request's q-values: a
// client that accepts both gets brotli even when it ranks gzip higher. Every
// browser that sends `br` prefers it, and brotli is the smaller file.
var precompressedEncodings = []struct{ coding, ext string }{
	{"br", ".br"},
	{"gzip", ".gz"},
}

// precompressedStatic sends a fresh `.br` / `.gz` sidecar of the requested file
// when the request allows it (see the file header), else hands the request to
// next unchanged.
func precompressedStatic(next http.Handler, roots []staticRoot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		accept := r.Header.Get("Accept-Encoding")
		if accept == "" {
			next.ServeHTTP(w, r)
			return
		}
		// http.FileServer redirects a request for ".../index.html"; leave that
		// (and every directory request) to it.
		if strings.HasSuffix(r.URL.Path, "/") || strings.HasSuffix(r.URL.Path, "/index.html") {
			next.ServeHTTP(w, r)
			return
		}
		for _, root := range roots {
			name, ok := strings.CutPrefix(r.URL.Path, root.prefix)
			if !ok {
				continue
			}
			if !strings.HasPrefix(name, "/") {
				name = "/" + name
			}
			name = path.Clean(name)
			fsys := http.Dir(root.dir)
			src, err := fsys.Open(name)
			if err != nil {
				if os.IsNotExist(err) {
					continue // the next root (the fallback mount) may have it
				}
				break
			}
			served := serveListedSidecar(w, r, fsys, root.dir, name, accept, src)
			_ = src.Close()
			if served {
				return
			}
			break
		}
		next.ServeHTTP(w, r)
	})
}

// serveListedSidecar answers the request from a sidecar of src (the raw file
// name in fsys, whose directory is dir) when the manifest lists both and they
// are unchanged. It reports whether it answered.
func serveListedSidecar(w http.ResponseWriter, r *http.Request, fsys http.Dir, dir, name, accept string, src http.File) bool {
	srcInfo, err := src.Stat()
	if err != nil || !srcInfo.Mode().IsRegular() {
		return false
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" || !compressibleStaticType(ctype) {
		return false
	}
	m := loadPrecompressManifest(dir)
	if m == nil {
		return false
	}
	entry, ok := m.Files[strings.TrimPrefix(name, "/")]
	if !ok || !entry.matches(dir+name, srcInfo, src) {
		return false
	}
	for _, enc := range precompressedEncodings {
		if !acceptsCoding(accept, enc.coding) {
			continue
		}
		want, ok := entry.Encodings[enc.coding]
		if !ok {
			continue
		}
		if servePrecompressed(w, r, fsys, dir, name, enc.coding, enc.ext, ctype, srcInfo, entry.Sha256, want) {
			return true
		}
	}
	return false
}

// servePrecompressed sends name+ext as the body of name, when it is the
// sidecar the manifest records. It reports whether it answered the request.
func servePrecompressed(w http.ResponseWriter, r *http.Request, fsys http.Dir, dir, name, coding, ext, ctype string, src os.FileInfo, srcSum string, want precompressFile) bool {
	f, err := fsys.Open(name + ext)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || !want.matches(dir+name+ext, info, f) {
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Encoding", coding)
	// A strong validator of this representation: the source's hash and the
	// coding. If-Range against it never mixes the bytes of two encodings.
	h.Set("ETag", `"`+srcSum[:min(len(srcSum), 32)]+"-"+coding+`"`)
	addHeaderValue(h, "Vary", "Accept-Encoding")
	// http.ServeContent leaves Content-Length unset on a 200 when
	// Content-Encoding is set, so the sidecar's length is set on the 200
	// itself (a 206 has its own from ServeContent; a 304 or 412 carries none).
	lw := &sidecarLengthWriter{ResponseWriter: w, length: info.Size()}
	http.ServeContent(lw, r, name, src.ModTime(), f)
	return true
}

// precompressManifestName is the file `sky build` writes in a bundle
// directory beside the sidecars it produced (rust/crates/sky/src/main.rs,
// write_precompress_manifest).
const precompressManifestName = "sky-precompress.json"

// precompressManifest lists, per file name relative to its directory, the
// source's size and SHA-256 and those of each sidecar the build wrote, keyed
// by coding ("br", "gzip").
type precompressManifest struct {
	Version int                         `json:"version"`
	Files   map[string]precompressEntry `json:"files"`
}

type precompressEntry struct {
	precompressFile
	Encodings map[string]precompressFile `json:"encodings"`
}

type precompressFile struct {
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

// matches reports whether the open file f (at p, described by info) is the
// one recorded: the size first, then the SHA-256, hashed once per (path,
// size, mtime) for the life of the process.
func (want precompressFile) matches(p string, info os.FileInfo, f io.Reader) bool {
	if info.Size() != want.Size || len(want.Sha256) != sha256.Size*2 {
		return false
	}
	return fileSha256(p, info, f) == strings.ToLower(want.Sha256)
}

type manifestCacheEntry struct {
	size  int64
	mtime time.Time
	m     *precompressManifest
}

var precompressManifests = struct {
	sync.Mutex
	byDir map[string]manifestCacheEntry
}{byDir: map[string]manifestCacheEntry{}}

// loadPrecompressManifest returns dir's manifest, nil when it has none (a
// user's static directory) or it cannot be read. It is parsed again only when
// its size or mtime changes.
func loadPrecompressManifest(dir string) *precompressManifest {
	p := filepath.Join(dir, precompressManifestName)
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	precompressManifests.Lock()
	c, ok := precompressManifests.byDir[dir]
	precompressManifests.Unlock()
	if ok && c.size == info.Size() && c.mtime.Equal(info.ModTime()) {
		return c.m
	}
	b, err := os.ReadFile(p)
	var m *precompressManifest
	if err == nil {
		var parsed precompressManifest
		if json.Unmarshal(b, &parsed) == nil && parsed.Version == 1 {
			m = &parsed
		}
	}
	precompressManifests.Lock()
	if len(precompressManifests.byDir) >= 256 {
		precompressManifests.byDir = map[string]manifestCacheEntry{}
	}
	precompressManifests.byDir[dir] = manifestCacheEntry{size: info.Size(), mtime: info.ModTime(), m: m}
	precompressManifests.Unlock()
	return m
}

type fileHashKey struct {
	path  string
	size  int64
	mtime int64
}

type fileHash struct {
	once sync.Once
	sum  string
}

var fileHashes = struct {
	sync.Mutex
	byKey map[fileHashKey]*fileHash
}{byKey: map[fileHashKey]*fileHash{}}

// fileSha256 is the hex SHA-256 of f (at p, described by info), computed once
// per (path, size, mtime). Concurrent first requests wait for one hash. A
// read error yields "", which matches nothing.
func fileSha256(p string, info os.FileInfo, f io.Reader) string {
	k := fileHashKey{path: p, size: info.Size(), mtime: info.ModTime().UnixNano()}
	fileHashes.Lock()
	e, ok := fileHashes.byKey[k]
	if !ok {
		if len(fileHashes.byKey) >= 4096 {
			fileHashes.byKey = map[fileHashKey]*fileHash{}
		}
		e = &fileHash{}
		fileHashes.byKey[k] = e
	}
	fileHashes.Unlock()
	e.once.Do(func() {
		h := sha256.New()
		if n, err := io.Copy(h, f); err == nil && n == info.Size() {
			e.sum = hex.EncodeToString(h.Sum(nil))
		}
	})
	return e.sum
}

// sidecarLengthWriter sets Content-Length on a 200 response.
type sidecarLengthWriter struct {
	http.ResponseWriter
	length      int64
	wroteHeader bool
}

func (w *sidecarLengthWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if code == http.StatusOK {
			w.Header().Set("Content-Length", strconv.FormatInt(w.length, 10))
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *sidecarLengthWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
