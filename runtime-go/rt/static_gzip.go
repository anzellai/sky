package rt

// HTTP gzip for static assets served by `Server.static` (and the Sky.Live /
// Sky.Webview static mounts). `http.FileServer` sets the Content-Type (incl.
// `application/wasm`) and writes the body without compression; this wrapper
// gzips a COMPRESSIBLE 200 response on the wire when the client advertises
// `Accept-Encoding: gzip`. It matters most for the Sky.Spa client `.wasm` — a
// standard-Go wasm bundle is multi-MB raw but ~25% of that gzipped, and the
// browser downloads the raw bytes unless the server compresses.
//
// Deliberately conservative: only plain GET (no Range), only a 200 with a
// compressible Content-Type and no existing Content-Encoding. Range/206, 304,
// and already-encoded or incompressible media (images/video/fonts) pass through
// untouched, so caching, resumable downloads, and MIME handling are unchanged.

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// compressibleStaticType reports whether a static asset of this content type
// benefits from gzip on the wire. Text + wasm + JSON/XML/SVG compress well;
// already-compressed media does not, so it is skipped to avoid spending CPU for
// no size win (and to avoid inflating already-compressed bytes).
func compressibleStaticType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/wasm",
		"application/javascript", "text/javascript",
		"application/json", "application/manifest+json",
		"application/xml", "text/xml",
		"image/svg+xml":
		return true
	}
	return strings.HasPrefix(ct, "text/")
}

var staticGzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// gzipStaticWriter defers the compress/passthrough decision until the headers
// are known: `http.ServeContent` sets Content-Type (by extension, then content
// sniff) before the first Write, so the type is available by then.
type gzipStaticWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	status      int
	decided     bool
	compressing bool
}

func (w *gzipStaticWriter) WriteHeader(code int) {
	w.status = code
	w.decide()
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipStaticWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	// Advertise that the representation varies by Accept-Encoding regardless of
	// whether we compress THIS response — a shared cache must key on it.
	addHeaderValue(h, "Vary", "Accept-Encoding")
	if w.status != http.StatusOK ||
		h.Get("Content-Encoding") != "" ||
		h.Get("Content-Range") != "" ||
		!compressibleStaticType(h.Get("Content-Type")) {
		return
	}
	w.compressing = true
	// The gzipped length differs from the file size FileServer computed; drop it
	// so the response terminates on EOF (net/http chunks it) rather than
	// mismatching a stale Content-Length.
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	w.gz = staticGzipPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
}

func (w *gzipStaticWriter) Write(b []byte) (int, error) {
	if !w.decided {
		// A handler that writes a 200 body without an explicit WriteHeader.
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.decide()
	}
	if w.compressing {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// close flushes + returns the gzip.Writer to the pool. Safe to call when no
// compression happened.
func (w *gzipStaticWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		w.gz.Reset(io.Discard)
		staticGzipPool.Put(w.gz)
		w.gz = nil
	}
}

// gzipStatic wraps a static file handler so compressible 200 responses are
// gzipped on the wire for clients that accept it. See the file header for the
// (deliberately narrow) conditions.
func gzipStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.Header.Get("Range") != "" ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			h.ServeHTTP(w, r)
			return
		}
		gw := &gzipStaticWriter{ResponseWriter: w}
		defer gw.close()
		h.ServeHTTP(gw, r)
	})
}

// acceptsGzip reports whether the Accept-Encoding header allows gzip. It honours
// an explicit `gzip;q=0` disable, which a bare `strings.Contains` would miss.
func acceptsGzip(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		coding := strings.ToLower(strings.TrimSpace(fields[0]))
		if coding != "gzip" && coding != "*" {
			continue
		}
		for _, p := range fields[1:] {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "q=0" || p == "q=0.0" || p == "q=0.00" || p == "q=0.000" {
				return false
			}
		}
		return true
	}
	return false
}

// addHeaderValue appends val to a comma/multi-valued header once, skipping a
// case-insensitive duplicate that is already present.
func addHeaderValue(h http.Header, key, val string) {
	for _, v := range h.Values(key) {
		if strings.EqualFold(strings.TrimSpace(v), val) {
			return
		}
	}
	h.Add(key, val)
}
