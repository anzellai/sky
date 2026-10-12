//go:build !js

package rt

// Tests for the byte-moving kernels of Std.Ai.Store (ai_store.go): a blob is
// stored and fetched under its own digest, and a wrong byte is never visible
// as a stored blob. The GCS and S3 request shapes and signing are Sky code
// (sky-stdlib/Std/Ai/Store.sky) and are tested in tests/Std/Ai/StoreTest.sky
// and rust/crates/sky/tests/ai_registry_flow.rs; here the remote side is an
// httptest server that answers like a bucket.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func runAiTask(t *testing.T, thunk any) (any, any) {
	t.Helper()
	tag, ok, errV := anyResultView(thunk.(func() any)())
	if tag == 0 {
		return ok, nil
	}
	return nil, errV
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func aiErrCode(t *testing.T, e any) string {
	t.Helper()
	df, ok := DomainOf(e)
	if !ok {
		t.Fatalf("not a domain failure: %#v", e)
	}
	return df.Code
}

func TestAiStoreLocalPutAndGetByDigest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "adapter.bin")
	data := []byte("not really an adapter, but bytes are bytes\x00\x01\x02")
	os.WriteFile(src, data, 0o644)

	v, err := runAiTask(t, Ai_storeLocalPut(root, src))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	m := v.(map[string]any)
	if m["sha256"] != sha(data) || m["size"] != len(data) {
		t.Fatalf("blob %v", m)
	}
	blob := filepath.Join(root, "blobs", "sha256", sha(data))
	if b, _ := os.ReadFile(blob); string(b) != string(data) {
		t.Fatal("the blob is not under blobs/sha256/<hex>")
	}
	// A second put of the same bytes is a no-op.
	if _, err := runAiTask(t, Ai_storeLocalPut(root, src)); err != nil {
		t.Fatal(err)
	}
	p, err := runAiTask(t, Ai_storeLocalGet(root, sha(data)))
	if err != nil || p != blob {
		t.Fatalf("get %v %v", p, err)
	}
	// A blob damaged on disk is ChecksumMismatch, never returned.
	os.WriteFile(blob, []byte("tampered"), 0o644)
	if _, err := runAiTask(t, Ai_storeLocalGet(root, sha(data))); err == nil || aiErrCode(t, err) != "ChecksumMismatch" {
		t.Fatalf("a damaged blob was served: %v", err)
	}
	// A missing blob is NotProvisioned.
	if _, err := runAiTask(t, Ai_storeLocalGet(root, sha([]byte("other")))); err == nil || aiErrCode(t, err) != "NotProvisioned" {
		t.Fatalf("a missing blob: %v", err)
	}
	// Leftovers: no partial file is left in the blob directory.
	entries, _ := os.ReadDir(filepath.Dir(blob))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".partial") {
			t.Fatalf("a partial file was left behind: %s", e.Name())
		}
	}
}

// bucket is an httptest server that stores PUT bodies and serves them on GET,
// with a conditional create (If-None-Match: *) that answers 412.
type bucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	headers map[string]http.Header
	corrupt bool
}

func newBucket(t *testing.T) (*bucket, *httptest.Server) {
	b := &bucket{objects: map[string][]byte{}, headers: map[string]http.Header{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.headers[r.Method+" "+r.URL.Path] = r.Header.Clone()
		switch r.Method {
		case "PUT":
			if _, ok := b.objects[r.URL.Path]; ok && r.Header.Get("If-None-Match") == "*" {
				w.WriteHeader(412)
				return
			}
			data, _ := io.ReadAll(r.Body)
			if int64(len(data)) != r.ContentLength {
				w.WriteHeader(400)
				return
			}
			b.objects[r.URL.Path] = data
			w.WriteHeader(200)
		case "GET":
			data, ok := b.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			if b.corrupt {
				data = append([]byte("x"), data...)
			}
			w.Write(data)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(srv.Close)
	return b, srv
}

func storeReq(method, url string, headers ...[2]string) any {
	hs := []any{}
	for _, h := range headers {
		hs = append(hs, T2[any, any]{V0: h[0], V1: h[1]})
	}
	return map[string]any{"method": method, "url": url, "headers": hs, "timeoutMs": 5000}
}

func TestAiStoreHttpPutAndGetVerifyTheDigest(t *testing.T) {
	t.Setenv("SKY_AI_HOME", t.TempDir())
	b, srv := newBucket(t)
	src := filepath.Join(t.TempDir(), "a.gguf")
	data := []byte(strings.Repeat("adapter-bytes-", 4096))
	os.WriteFile(src, data, 0o644)
	url := srv.URL + "/bucket/registry/blobs/sha256/" + sha(data)

	st, err := runAiTask(t, Ai_storeHttpPut(storeReq("PUT", url, [2]string{"If-None-Match", "*"}, [2]string{"X-Amz-Content-Sha256", sha(data)}), src))
	if err != nil || st != 200 {
		t.Fatalf("put %v %v", st, err)
	}
	if got := b.headers["PUT /bucket/registry/blobs/sha256/"+sha(data)].Get("X-Amz-Content-Sha256"); got != sha(data) {
		t.Fatalf("the signed headers were not sent: %q", got)
	}
	// A second PUT finds the object: 412 is success (the name is the digest).
	if st, err := runAiTask(t, Ai_storeHttpPut(storeReq("PUT", url, [2]string{"If-None-Match", "*"}), src)); err != nil || st != 412 {
		t.Fatalf("second put %v %v", st, err)
	}
	p, err := runAiTask(t, Ai_storeHttpGet(storeReq("GET", url), sha(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p.(string)); string(got) != string(data) {
		t.Fatal("the fetched blob differs")
	}
	if c, _ := runAiTask(t, Ai_storeCached(sha(data))); c != p {
		t.Fatalf("cached %v, want %v", c, p)
	}

	// A body that does not hash to the digest is refused and not cached.
	os.Remove(p.(string))
	b.corrupt = true
	if _, err := runAiTask(t, Ai_storeHttpGet(storeReq("GET", url), sha(data))); err == nil || aiErrCode(t, err) != "ChecksumMismatch" {
		t.Fatalf("a corrupt download: %v", err)
	}
	if _, serr := os.Stat(p.(string)); serr == nil {
		t.Fatal("a corrupt download was cached")
	}
	if c, _ := runAiTask(t, Ai_storeCached(sha(data))); c != "" {
		t.Fatalf("cached after a corrupt download: %v", c)
	}
	// A missing object is NotProvisioned; a refusal is RemoteRejected.
	b.corrupt = false
	if _, err := runAiTask(t, Ai_storeHttpGet(storeReq("GET", srv.URL+"/nope"), sha([]byte("x")))); err == nil || aiErrCode(t, err) != "NotProvisioned" {
		t.Fatalf("missing object: %v", err)
	}
	if _, err := runAiTask(t, Ai_storeHttpPut(storeReq("DELETE", url), src)); err == nil || aiErrCode(t, err) != "RemoteRejected" {
		t.Fatalf("a refused request: %v", err)
	}
}

func TestAiAdapterInfoAcceptsAdaptersOnly(t *testing.T) {
	dir := t.TempDir()
	lora := filepath.Join(dir, "lora.gguf")
	os.WriteFile(lora, ggufBytes([]ggufKV{
		{"general.architecture", "qwen2"},
		{"general.type", "adapter"},
		{"adapter.type", "lora"},
		{"adapter.lora.alpha", float32(16)},
	}), 0o644)
	v, err := runAiTask(t, Ai_adapterInfo(lora))
	if err != nil {
		t.Fatal(err)
	}
	if m := v.(map[string]any); m["format"] != "gguf" || m["kind"] != "lora" || m["architecture"] != "qwen2" {
		t.Fatalf("info %v", m)
	}

	full := filepath.Join(dir, "model.gguf")
	os.WriteFile(full, ggufBytes(qwenTinyMeta()), 0o644)
	if _, err := runAiTask(t, Ai_adapterInfo(full)); err == nil || aiErrCode(t, err) != "InvalidSetting" {
		t.Fatalf("a full model was accepted as an adapter: %v", err)
	}

	st := filepath.Join(dir, "adapter_model.safetensors")
	hdr := `{"base_model.model.layers.0.self_attn.q_proj.lora_A.weight":{"dtype":"F32","shape":[8,896],"data_offsets":[0,0]}}`
	b := make([]byte, 8)
	for i, n := 0, uint64(len(hdr)); i < 8; i++ {
		b[i] = byte(n >> (8 * i))
	}
	os.WriteFile(st, append(b, hdr...), 0o644)
	if v, err := runAiTask(t, Ai_adapterInfo(st)); err != nil || v.(map[string]any)["format"] != "safetensors" {
		t.Fatalf("safetensors: %v %v", v, err)
	}

	pickle := filepath.Join(dir, "adapter.bin")
	os.WriteFile(pickle, []byte("\x80\x02}q\x00(X\x05\x00\x00\x00"), 0o644)
	if _, err := runAiTask(t, Ai_adapterInfo(pickle)); err == nil {
		t.Fatal("a pickle checkpoint was accepted")
	}
}

func TestAiRegistryLocationPrecedence(t *testing.T) {
	t.Setenv("SKY_AI_REGISTRY", "")
	v, _ := runAiTask(t, Ai_registryLocation(""))
	if m := v.(map[string]any); m["value"] != "file://.sky/ai/registry" || m["source"] != "default" {
		t.Fatalf("default %v", m)
	}
	v, _ = runAiTask(t, Ai_registryLocation("gs://code-bucket/reg"))
	if m := v.(map[string]any); m["value"] != "gs://code-bucket/reg" || m["source"] != "code" {
		t.Fatalf("code %v", m)
	}
	t.Setenv("SKY_AI_REGISTRY", "s3://env-bucket/reg")
	v, _ = runAiTask(t, Ai_registryLocation("gs://code-bucket/reg"))
	if m := v.(map[string]any); m["value"] != "s3://env-bucket/reg" || m["source"] != "env" || m["name"] != "SKY_AI_REGISTRY" {
		t.Fatalf("env %v", m)
	}
}
