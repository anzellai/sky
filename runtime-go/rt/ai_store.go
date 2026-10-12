//go:build !js

package rt

// ai_store.go — the byte-moving half of Std.Ai.Store, the registry's artefact
// store.
//
// Std.Ai.Store keeps adapters and other registry artefacts content-addressed
// under `blobs/sha256/<hex>` in a local directory, a GCS bucket or an S3
// bucket. The Sky side owns everything that is policy: the layout, the URLs,
// the request signing (AWS SigV4 with Crypto.hmacSha256, Google OAuth through
// the metadata server or a service-account JWT) and the credentials. This file
// owns only what Sky cannot do well with a `String`: streaming a large binary
// file from disk into an HTTP body or a directory, and streaming an HTTP body
// to disk, with its SHA-256 checked before anything counts as stored.
//
//	Ai_fileDigest     : String -> Task Error { sha256 : String, size : Int }
//	Ai_adapterInfo    : String -> Task Error { format : String, kind : String, architecture : String }
//	Ai_storeLocalPut  : String -> String -> Task Error { sha256 : String, size : Int }
//	Ai_storeLocalGet  : String -> String -> Task Error String
//	Ai_storeHttpPut   : Request -> String -> Task Error Int
//	Ai_storeHttpGet   : Request -> String -> Task Error String
//	Ai_storeCached    : String -> Task Error String
//	Ai_registryLocation : String -> Task Error Setting
//
// Every request goes through the test-mode transport, so under `sky test` in
// test mode a GCS or S3 call is answered by a fixture under tests/mocks/ and
// never reaches a network.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// aiStoreClient carries artefact transfers. There is no overall timeout (an
// adapter can be hundreds of megabytes); a request's own deadline bounds it,
// and the dial and header timeouts bound a dead server.
var aiStoreClient = &http.Client{Transport: &testHttpTransport{base: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
	ResponseHeaderTimeout: 120 * time.Second,
	IdleConnTimeout:       60 * time.Second,
}}}

// aiStoreDefaultTimeout bounds one transfer when the request names none.
const aiStoreDefaultTimeout = 30 * time.Minute

// aiBlobRel is where a blob lives under a store's root or prefix.
func aiBlobRel(hexDigest string) string { return "blobs/sha256/" + hexDigest }

// aiArtefactCache is where a blob fetched from a remote store is kept on this
// machine, shared by every project of the user, like the model store.
func aiArtefactCache(hexDigest string) string {
	return filepath.Join(aiHome(), "artefacts", "sha256", hexDigest)
}

// aiDigestValue is the Sky `Blob` record.
func aiDigestValue(sum string, size int64) any {
	return map[string]any{"sha256": sum, "size": int(size)}
}

// aiStoreFail names a store failure. A missing source is NotFound-shaped
// (NotProvisioned would name a command, which a store failure has none of),
// so it is an InvalidSetting on the path; an I/O failure is EngineFailed's
// sibling for stores, reported as Unreachable for a remote target.
func aiStoreLocalErr(format string, args ...any) *aiFailure {
	return aiFailf(aiInvalidSetting, format, args...).with("setting", "store")
}

// aiHashFile hashes a file and returns its digest and size.
func aiHashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Ai_fileDigest : String -> Task Error { sha256 : String, size : Int }
func Ai_fileDigest(pathArg any) any {
	path := AsString(pathArg)
	return func() any {
		sum, size, err := aiHashFile(path)
		if err != nil {
			return aiTaskErr(aiStoreLocalErr("cannot read %s: %v", path, err))
		}
		return Ok[any, any](aiDigestValue(sum, size))
	}
}

// aiCopyVerified copies src to dest through a temporary file in dest's
// directory and renames it into place only when the bytes hash to want. A
// half-written or wrong blob is never visible at dest.
func aiCopyVerified(src io.Reader, dest, want string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, aiStoreLocalErr("cannot create %s: %v", filepath.Dir(dest), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".partial-*")
	if err != nil {
		return 0, aiStoreLocalErr("cannot write in %s: %v", filepath.Dir(dest), err)
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(tmp, h), src)
	if err := tmp.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		os.Remove(tmp.Name())
		return 0, cerr
	}
	got := hex.EncodeToString(h.Sum(nil))
	if want != "" && got != want {
		os.Remove(tmp.Name())
		return 0, aiFailf(aiChecksumMismatch, "expected sha256 %s, the bytes are sha256 %s; nothing was stored", want, got).
			with("expected", want).with("actual", got)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return 0, aiStoreLocalErr("cannot install %s: %v", dest, err)
	}
	return n, nil
}

// aiVerifiedBlob reports whether path exists and hashes to want.
func aiVerifiedBlob(path, want string) bool {
	sum, _, err := aiHashFile(path)
	return err == nil && sum == want
}

// Ai_storeLocalPut : String -> String -> Task Error Blob — copy the file at
// src into the local store rooted at root, under its own digest. A blob that
// is already there (and still hashes right) is not copied again.
func Ai_storeLocalPut(rootArg, srcArg any) any {
	root, src := AsString(rootArg), AsString(srcArg)
	return func() any {
		sum, size, err := aiHashFile(src)
		if err != nil {
			return aiTaskErr(aiStoreLocalErr("cannot read %s: %v", src, err))
		}
		dest := filepath.Join(root, filepath.FromSlash(aiBlobRel(sum)))
		if aiVerifiedBlob(dest, sum) {
			return Ok[any, any](aiDigestValue(sum, size))
		}
		f, err := os.Open(src)
		if err != nil {
			return aiTaskErr(aiStoreLocalErr("cannot read %s: %v", src, err))
		}
		defer f.Close()
		if _, err := aiCopyVerified(f, dest, sum); err != nil {
			if fe, ok := err.(*aiFailure); ok {
				return aiTaskErr(fe)
			}
			return aiTaskErr(aiStoreLocalErr("cannot store %s: %v", src, err))
		}
		return Ok[any, any](aiDigestValue(sum, size))
	}
}

// Ai_storeLocalGet : String -> String -> Task Error String — the path of a
// blob in the local store, after checking that it still hashes to its name.
func Ai_storeLocalGet(rootArg, digestArg any) any {
	root, sum := AsString(rootArg), AsString(digestArg)
	return func() any {
		if !aiValidSha256(sum) {
			return aiTaskErr(aiFailf(aiInvalidSetting, "%q is not a sha256 digest (64 lower-case hex digits)", sum).with("setting", "digest"))
		}
		path := filepath.Join(root, filepath.FromSlash(aiBlobRel(sum)))
		got, _, err := aiHashFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return aiTaskErr(aiFailf(aiNotProvisioned, "artefact sha256:%s is not in the store %s", sum, root).
					with("what", "artefact sha256:"+sum).with("command", "Store.put"))
			}
			return aiTaskErr(aiStoreLocalErr("cannot read %s: %v", path, err))
		}
		if got != sum {
			return aiTaskErr(aiFailf(aiChecksumMismatch, "the blob %s hashes to sha256 %s: the store is damaged", path, got).
				with("expected", sum).with("actual", got))
		}
		return Ok[any, any](path)
	}
}

// aiStoreRequest is a signed request the Sky side built.
type aiStoreRequest struct {
	method    string
	url       string
	headers   [][2]string
	timeoutMs int
}

func parseAiStoreRequest(v any) aiStoreRequest {
	r := aiStoreRequest{
		method:    aiStrField(v, "Method", "method"),
		url:       aiStrField(v, "Url", "url"),
		timeoutMs: aiIntField(v, "TimeoutMs", "timeoutMs"),
	}
	for _, h := range asList(recordField(v, "Headers", "headers")) {
		r.headers = append(r.headers, [2]string{AsString(tupleFirst(h)), AsString(tupleSecond(h))})
	}
	return r
}

func (r aiStoreRequest) build(ctx context.Context, body io.Reader, size int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, r.method, r.url, body)
	if err != nil {
		return nil, aiFailf(aiInvalidSetting, "bad store URL %s: %v", aiRedactURL(r.url), err).with("setting", "store")
	}
	for _, h := range r.headers {
		if strings.EqualFold(h[0], "Host") {
			req.Host = h[1]
			continue
		}
		req.Header.Set(h[0], h[1])
	}
	if body != nil {
		req.ContentLength = size
	}
	return req, nil
}

func (r aiStoreRequest) context() (context.Context, context.CancelFunc) {
	d := aiStoreDefaultTimeout
	if r.timeoutMs > 0 {
		d = time.Duration(r.timeoutMs) * time.Millisecond
	}
	return context.WithTimeout(context.Background(), d)
}

// aiStoreHTTPErr classifies a store answer that is not a success.
func aiStoreHTTPErr(r aiStoreRequest, status int, body string) error {
	target := aiRedactURL(r.url)
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		f := aiFailf(aiOverloaded, "%s %s answered HTTP %d: %s", r.method, target, status, aiTail(body, 300))
		f.httpStatus = status
		return f
	case status == http.StatusNotFound:
		return aiFailf(aiNotProvisioned, "%s %s: the object does not exist (HTTP 404)", r.method, target).
			with("what", "the object at "+target).with("command", "Store.put")
	default:
		f := aiFailf(aiRemoteRejected, "%s %s answered HTTP %d: %s", r.method, target, status, aiTail(body, 500))
		f.httpStatus = status
		return f
	}
}

func aiStoreTransportErr(r aiStoreRequest, ctx context.Context, err error) error {
	target := aiRedactURL(r.url)
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return aiFailf(aiTimedOut, "%s %s: no answer before the deadline", r.method, target)
	}
	return aiFailf(aiUnreachable, "%s %s: %v", r.method, target, aiStripURL(err)).with("target", target)
}

// Ai_storeHttpPut : Request -> String -> Task Error Int — stream the file at
// src as the body of a signed request (a GCS upload or an S3 PUT). 2xx is
// stored; 412 (a conditional create found the object already there) is also
// a success, because the object's name is its digest. The answer's status is
// returned.
func Ai_storeHttpPut(reqArg, srcArg any) any {
	r, src := parseAiStoreRequest(reqArg), AsString(srcArg)
	return func() any {
		f, err := os.Open(src)
		if err != nil {
			return aiTaskErr(aiStoreLocalErr("cannot read %s: %v", src, err))
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return aiTaskErr(aiStoreLocalErr("cannot read %s: %v", src, err))
		}
		ctx, cancel := r.context()
		defer cancel()
		req, err := r.build(ctx, bufio.NewReaderSize(f, 1<<20), st.Size())
		if err != nil {
			return aiTaskErr(err)
		}
		resp, err := aiStoreClient.Do(req)
		if err != nil {
			return aiTaskErr(aiStoreTransportErr(r, ctx, err))
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusPreconditionFailed {
			return Ok[any, any](resp.StatusCode)
		}
		return aiTaskErr(aiStoreHTTPErr(r, resp.StatusCode, string(body)))
	}
}

// Ai_storeHttpGet : Request -> String -> Task Error String — download a blob
// into this machine's artefact cache, checking its digest, and return the
// cached path. A cached blob that still hashes right is used without a
// request.
func Ai_storeHttpGet(reqArg, digestArg any) any {
	r, sum := parseAiStoreRequest(reqArg), AsString(digestArg)
	return func() any {
		if !aiValidSha256(sum) {
			return aiTaskErr(aiFailf(aiInvalidSetting, "%q is not a sha256 digest (64 lower-case hex digits)", sum).with("setting", "digest"))
		}
		dest := aiArtefactCache(sum)
		if aiVerifiedBlob(dest, sum) {
			return Ok[any, any](dest)
		}
		ctx, cancel := r.context()
		defer cancel()
		req, err := r.build(ctx, nil, 0)
		if err != nil {
			return aiTaskErr(err)
		}
		resp, err := aiStoreClient.Do(req)
		if err != nil {
			return aiTaskErr(aiStoreTransportErr(r, ctx, err))
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return aiTaskErr(aiStoreHTTPErr(r, resp.StatusCode, string(body)))
		}
		if _, err := aiCopyVerified(resp.Body, dest, sum); err != nil {
			if fe, ok := err.(*aiFailure); ok {
				return aiTaskErr(fe)
			}
			return aiTaskErr(aiStoreTransportErr(r, ctx, err))
		}
		return Ok[any, any](dest)
	}
}

// Ai_storeCached : String -> Task Error String — the cached path of a blob
// that is already on this machine and still hashes right, else "".
func Ai_storeCached(digestArg any) any {
	sum := AsString(digestArg)
	return func() any {
		if aiValidSha256(sum) && aiVerifiedBlob(aiArtefactCache(sum), sum) {
			return Ok[any, any](aiArtefactCache(sum))
		}
		return Ok[any, any]("")
	}
}

// Ai_registryLocation : String -> Task Error Setting — where the registry's
// artefacts live. `<PREFIX>_AI_REGISTRY` (file://…, gs://bucket/prefix or
// s3://bucket/prefix) beats the code's value, which beats the stdlib default
// `file://.sky/ai/registry`. The resolver is the shared one (configLayers).
func Ai_registryLocation(codeArg any) any {
	code := AsString(codeArg)
	return func() any {
		v, src := aiLayer("AI_REGISTRY", code, nil)
		if v == "" {
			v, src = "file://.sky/ai/registry", "default"
		}
		return Ok[any, any](map[string]any{
			"key":    "REGISTRY",
			"name":   skyEnvName("AI_REGISTRY"),
			"value":  v,
			"source": src,
		})
	}
}

// ─── adapter files ──────────────────────────────────────────────────────

// aiAdapterInfo checks that a file is an adapter Sky can register: a GGUF
// file whose metadata says it is a LoRA adapter, or a safetensors file whose
// header parses. A full model, a pickle checkpoint or a damaged file is
// refused before it is stored, so the registry holds adapters only.
func aiAdapterInfo(path string) (format, kind, arch string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", aiStoreLocalErr("cannot read %s: %v", path, err)
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return "", "", "", aiFailf(aiInvalidSetting, "%s is too short to be an adapter", path).with("setting", "adapter")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", "", aiStoreLocalErr("cannot read %s: %v", path, err)
	}
	if string(magic[:]) == "GGUF" {
		kv, err := parseGGUFKV(bufio.NewReaderSize(f, 1<<16))
		if err != nil {
			return "", "", "", aiFailf(aiInvalidSetting, "%s: %v", path, err).with("setting", "adapter")
		}
		gt, _ := kv["general.type"].(string)
		at, _ := kv["adapter.type"].(string)
		arch, _ = kv["general.architecture"].(string)
		if gt != "adapter" || at != "lora" {
			return "", "", "", aiFailf(aiInvalidSetting,
				"%s is a GGUF file but not a LoRA adapter (general.type %q, adapter.type %q); the registry holds adapters only",
				path, gt, at).with("setting", "adapter")
		}
		return "gguf", "lora", arch, nil
	}
	// safetensors: an 8-byte little-endian header length, then a JSON object.
	var n uint64
	if err := binary.Read(f, binary.LittleEndian, &n); err != nil || n == 0 || n > 100<<20 {
		return "", "", "", aiFailf(aiInvalidSetting, "%s is neither a GGUF LoRA adapter nor a safetensors file", path).with("setting", "adapter")
	}
	hdr := make([]byte, n)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return "", "", "", aiFailf(aiInvalidSetting, "%s: the safetensors header is truncated", path).with("setting", "adapter")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(hdr, &m); err != nil {
		return "", "", "", aiFailf(aiInvalidSetting, "%s: the safetensors header is not a JSON object: %v", path, err).with("setting", "adapter")
	}
	return "safetensors", "lora", "", nil
}

// Ai_adapterInfo : String -> Task Error { format, kind, architecture }
func Ai_adapterInfo(pathArg any) any {
	path := AsString(pathArg)
	return func() any {
		format, kind, arch, err := aiAdapterInfo(path)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](map[string]any{"format": format, "kind": kind, "architecture": arch})
	}
}
