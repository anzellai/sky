package rt

// Precompressed static files. `sky build` writes `<file>.br` (brotli) and
// `<file>.gz` (gzip) beside the wasm client, its loader and its boot script
// in a Sky.Spa `dist/`. A static mount sends one of them, unchanged, when the
// request allows its encoding, instead of gzipping the raw file on every
// request: the brotli file is about a quarter the size of an on-the-fly gzip
// and costs no CPU to send.
//
// The sidecar is used only when all of these hold, else the request takes the
// path it always took (the raw file, gzipped on the fly by gzipStatic):
//   - a GET or HEAD with no Range header (a Range is served from the raw file,
//     whose byte offsets the client asked for);
//   - the raw file exists and is a regular file, and its Content-Type is known
//     from its extension and compressible (a type that would be sniffed is
//     never sniffed from compressed bytes);
//   - the sidecar is a regular, non-empty file no older than the raw file (a
//     sidecar older than its source is stale and never sent).
//
// The response carries the raw file's Content-Type and Last-Modified (so
// If-Modified-Since works as before), `Content-Encoding`, `Vary:
// Accept-Encoding` and the sidecar's exact Content-Length.

import (
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
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
	var files http.Handler = http.StripPrefix(prefix, http.FileServer(http.Dir(dir)))
	roots := []staticRoot{{prefix: prefix, dir: dir}}
	if fallbackRoot != "" {
		files = staticFallThrough(files, http.StripPrefix("/", http.FileServer(http.Dir(fallbackRoot))))
		roots = append(roots, staticRoot{prefix: "/", dir: fallbackRoot})
	}
	return gzipStatic(precompressedStatic(files, roots))
}

// precompressedEncodings are the sidecars tried, best first.
var precompressedEncodings = []struct{ coding, ext string }{
	{"br", ".br"},
	{"gzip", ".gz"},
}

// precompressedStatic sends a fresh `.br` / `.gz` sidecar of the requested file
// when the request allows it (see the file header), else hands the request to
// next unchanged.
func precompressedStatic(next http.Handler, roots []staticRoot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.Header.Get("Range") != "" {
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
			srcInfo, err := src.Stat()
			_ = src.Close()
			if err != nil || !srcInfo.Mode().IsRegular() {
				break
			}
			ctype := mime.TypeByExtension(path.Ext(name))
			if ctype == "" || !compressibleStaticType(ctype) {
				break
			}
			for _, enc := range precompressedEncodings {
				if !acceptsCoding(accept, enc.coding) {
					continue
				}
				if servePrecompressed(w, r, fsys, name, enc.coding, enc.ext, ctype, srcInfo) {
					return
				}
			}
			break
		}
		next.ServeHTTP(w, r)
	})
}

// servePrecompressed sends name+ext as the body of name, when it is a fresh
// sidecar. It reports whether it answered the request.
func servePrecompressed(w http.ResponseWriter, r *http.Request, fsys http.Dir, name, coding, ext, ctype string, src os.FileInfo) bool {
	f, err := fsys.Open(name + ext)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.ModTime().Before(src.ModTime()) {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Encoding", coding)
	addHeaderValue(h, "Vary", "Accept-Encoding")
	// http.ServeContent leaves Content-Length unset when Content-Encoding is,
	// so the sidecar's length is set on the 200 itself (a 304 or 412 carries
	// none).
	lw := &sidecarLengthWriter{ResponseWriter: w, length: info.Size()}
	http.ServeContent(lw, r, name, src.ModTime(), f)
	return true
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

// acceptsCoding reports whether the Accept-Encoding header allows coding. An
// entry naming the coding decides; otherwise `*` does. A q of zero refuses.
func acceptsCoding(accept, coding string) bool {
	wildcard := false
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		c := strings.ToLower(strings.TrimSpace(fields[0]))
		if c != coding && c != "*" {
			continue
		}
		allowed := true
		for _, p := range fields[1:] {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "q=0" || p == "q=0.0" || p == "q=0.00" || p == "q=0.000" {
				allowed = false
			}
		}
		if c == coding {
			return allowed
		}
		wildcard = allowed
	}
	return wildcard
}
