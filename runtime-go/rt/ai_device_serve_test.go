//go:build !js

package rt

// The server half of Std.Ai.Device (ai_device_serve.go): the offer, the
// blob allow-list, the chunk list, the fallback call's rules, and the wire
// failure that carries a typed Std.Ai failure to the client.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// deviceEndpoint is an Endpoint config as the Sky side passes it.
func deviceEndpoint(slot, url, placement, model, adapter string) map[string]any {
	return map[string]any{
		"slot": slot, "mode": "", "url": url, "hasKey": false, "model": "", "adapter": "",
		"timeoutMs": -1, "maxInFlight": -1, "queue": -1, "queueWaitMs": -1, "retries": -1,
		"hasLocal": false, "hasResolver": false, "resolverName": "", "adapterRefreshS": -1,
		"hasDevice": true,
		"device": map[string]any{
			"placement": placement, "model": model, "adapter": adapter, "context": -1,
			"maxDownloadBytes": -1, "backends": []any{}, "metered": false,
		},
	}
}

// deviceServer registers one device slot and serves its routes.
func deviceServer(t *testing.T, cfg map[string]any) *httptest.Server {
	t.Helper()
	aiDeviceReset()
	t.Cleanup(aiDeviceReset)
	AiDeviceRegister(cfg["slot"].(string), func() any { return cfg })
	mux := http.NewServeMux()
	registerAiDeviceRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func writeBytes(t *testing.T, name string, data []byte) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return path, hex.EncodeToString(s[:])
}

func TestDeviceOfferNamesTheVerifiedFiles(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	adapterPath, adapterSum := writeBytes(t, "lora.gguf", []byte("GGUF adapter bytes"))
	cfg := deviceEndpoint("chat", "", "auto", modelRef, "file:"+adapterPath+"#sha256="+adapterSum)
	srv := deviceServer(t, cfg)

	resp, body := get(t, srv.URL+"/_sky/ai/device/chat", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("offer: %d %s", resp.StatusCode, body)
	}
	var o aiDeviceOffer
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		t.Fatal(err)
	}
	modelSum := modelRef[strings.Index(modelRef, "sha256=")+7:]
	if o.Placement != "auto" || o.Model.Sha256 != modelSum || o.Model.URL != "/_sky/ai/blob/"+modelSum || o.Model.Bytes == 0 {
		t.Fatalf("offer model: %+v", o)
	}
	if o.Adapter == nil || o.Adapter.Sha256 != adapterSum || o.Adapter.Bytes != int64(len("GGUF adapter bytes")) {
		t.Fatalf("offer adapter: %+v", o.Adapter)
	}
	if o.Context != aiDeviceDefaultContext || o.MaxDownloadBytes != aiDeviceDefaultMaxDownload || !o.Server || o.Here {
		t.Fatalf("offer defaults: %+v", o)
	}
	if strings.Join(o.Backends, ",") != "native,webgpu,wasm-cpu" {
		t.Fatalf("backends: %v", o.Backends)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if r2, _ := get(t, srv.URL+"/_sky/ai/device/chat", map[string]string{"If-None-Match": etag}); r2.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", r2.StatusCode)
	}
	if r3, b3 := get(t, srv.URL+"/_sky/ai/device/other", nil); r3.StatusCode != 404 || !strings.Contains(b3, "NotConfigured") {
		t.Fatalf("unknown slot: %d %s", r3.StatusCode, b3)
	}
}

func TestDeviceOfferPlacementFollowsTheOperator(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	srv := deviceServer(t, deviceEndpoint("chat", "", "auto", modelRef, ""))
	t.Setenv("SKY_AI_CHAT_PLACEMENT", "server")
	_, body := get(t, srv.URL+"/_sky/ai/device/chat", nil)
	var o aiDeviceOffer
	_ = json.Unmarshal([]byte(body), &o)
	if o.Placement != "server" || o.Model.Sha256 != "" {
		t.Fatalf("operator server placement: %s", body)
	}
	aiDeviceReset()
	srv2 := deviceServer(t, deviceEndpoint("chat", "", "auto", modelRef, ""))
	t.Setenv("SKY_AI_CHAT_PLACEMENT", "device")
	_, body = get(t, srv2.URL+"/_sky/ai/device/chat", nil)
	_ = json.Unmarshal([]byte(body), &o)
	if o.Placement != "device" || o.Server {
		t.Fatalf("operator device placement: %s", body)
	}
}

func TestDeviceOfferWithoutAModelIsNotConfigured(t *testing.T) {
	aiTestEnv(t, nil)
	srv := deviceServer(t, deviceEndpoint("chat", "", "auto", "", ""))
	resp, body := get(t, srv.URL+"/_sky/ai/device/chat", nil)
	f, ok := aiDeviceFailureFrom([]byte(body))
	if resp.StatusCode != 503 || !ok || f.code != aiNotConfigured {
		t.Fatalf("no model: %d %s", resp.StatusCode, body)
	}
	if v, _ := f.attrValue("setting"); v != "SKY_AI_CHAT_DEVICE_MODEL" {
		t.Fatalf("setting: %q", v)
	}
}

func TestDeviceBlobServesOnlyOfferedDigests(t *testing.T) {
	aiTestEnv(t, nil)
	data := make([]byte, 3*1024+17)
	for i := range data {
		data[i] = byte(i * 7)
	}
	path, sum := writeBytes(t, "model.gguf", data)
	_, otherSum := writeBytes(t, "other.gguf", []byte("not offered"))
	srv := deviceServer(t, deviceEndpoint("chat", "", "auto", "file:"+path+"#sha256="+sum, ""))
	// The allow-list is the offers made so far: none yet.
	if r, _ := get(t, srv.URL+"/_sky/ai/blob/"+sum, nil); r.StatusCode != 404 {
		t.Fatalf("blob before any offer: %d", r.StatusCode)
	}
	get(t, srv.URL+"/_sky/ai/device/chat", nil)
	r, b := get(t, srv.URL+"/_sky/ai/blob/"+sum, map[string]string{"Range": "bytes=10-19"})
	if r.StatusCode != 206 || b != string(data[10:20]) {
		t.Fatalf("range: %d %q", r.StatusCode, b)
	}
	if r, _ := get(t, srv.URL+"/_sky/ai/blob/"+otherSum, nil); r.StatusCode != 404 {
		t.Fatalf("a digest no offer names: %d", r.StatusCode)
	}
	for _, bad := range []string{"..%2f..%2fetc%2fpasswd", strings.Repeat("g", 64), sum[:63]} {
		if r, _ := get(t, srv.URL+"/_sky/ai/blob/"+bad, nil); r.StatusCode != 404 {
			t.Fatalf("%q: %d", bad, r.StatusCode)
		}
	}
	r, b = get(t, srv.URL+"/_sky/ai/blob/"+sum+".chunks.json", nil)
	var c aiDeviceChunks
	if r.StatusCode != 200 || json.Unmarshal([]byte(b), &c) != nil {
		t.Fatalf("chunks: %d %s", r.StatusCode, b)
	}
	if c.Sha256 != sum || c.Size != int64(len(data)) || c.ChunkBytes != aiDeviceChunkBytes || len(c.Chunks) != 1 || c.Chunks[0] != sum {
		t.Fatalf("chunks: %+v", c)
	}
}

func TestDeviceChunkListSplitsAt64MiB(t *testing.T) {
	aiTestEnv(t, nil)
	data := make([]byte, aiDeviceChunkBytes+5)
	path, sum := writeBytes(t, "big.gguf", data)
	b, err := aiDeviceChunkList(path, sum)
	if err != nil {
		t.Fatal(err)
	}
	var c aiDeviceChunks
	_ = json.Unmarshal(b, &c)
	first := sha256.Sum256(data[:aiDeviceChunkBytes])
	second := sha256.Sum256(data[aiDeviceChunkBytes:])
	if len(c.Chunks) != 2 || c.Chunks[0] != hex.EncodeToString(first[:]) || c.Chunks[1] != hex.EncodeToString(second[:]) {
		t.Fatalf("chunks: %+v", c.Chunks)
	}
	if _, err := aiDeviceChunkList(path, strings.Repeat("0", 64)); failCode(err) != aiChecksumMismatch {
		t.Fatalf("a wrong digest: %v", err)
	}
}

// openAIStub answers chat completions with a fixed text.
func openAIStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from the server"}}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, ctype, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func callBody(text string) string {
	b, _ := json.Marshal(aiDeviceCallBody{Path: "/chat/completions", Body: chatBody(text)})
	return string(b)
}

func TestDeviceCallFallbackRules(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	upstream := openAIStub(t)
	srv := deviceServer(t, deviceEndpoint("chat", upstream.URL+"/v1", "auto", modelRef, ""))
	url := srv.URL + "/_sky/ai/device/chat/call"
	sid := map[string]string{"Cookie": "sky_sid=one"}

	if st, b := post(t, url, "application/json", callBody("hi"), sid); st != 200 || chatContent(t, b) != "from the server" {
		t.Fatalf("call: %d %s", st, b)
	}
	if st, _ := post(t, url, "text/plain", callBody("hi"), sid); st != 415 {
		t.Fatalf("not JSON: %d", st)
	}
	if st, _ := post(t, url, "application/json", callBody("hi"), map[string]string{"Cookie": "sky_sid=one", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); st != 403 {
		t.Fatalf("cross origin: %d", st)
	}
	if st, _ := post(t, url, "application/json", `{"path":"/embeddings","body":"{}"}`, sid); st != 400 {
		t.Fatalf("not a device call: %d", st)
	}
	big := callBody(strings.Repeat("x", aiDeviceCallMaxBody))
	if st, _ := post(t, url, "application/json", big, map[string]string{"Cookie": "sky_sid=big"}); st != 413 {
		t.Fatalf("too large: %d", st)
	}
	// 30 a minute per session: the first call and the refused path counted.
	for i := 0; i < aiDeviceCallsPerMinute-2; i++ {
		if st, b := post(t, url, "application/json", callBody("hi"), sid); st != 200 {
			t.Fatalf("call %d: %d %s", i, st, b)
		}
	}
	st, b := post(t, url, "application/json", callBody("hi"), sid)
	f, ok := aiDeviceFailureFrom([]byte(b))
	if st != 429 || !ok || f.code != aiOverloaded {
		t.Fatalf("over the limit: %d %s", st, b)
	}
	// Another session still has its own budget.
	if st, _ := post(t, url, "application/json", callBody("hi"), map[string]string{"Cookie": "sky_sid=two"}); st != 200 {
		t.Fatalf("other session: %d", st)
	}
}

func TestDeviceOnlyHasNoServerFallback(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	upstream := openAIStub(t)
	srv := deviceServer(t, deviceEndpoint("chat", upstream.URL+"/v1", "device", modelRef, ""))
	if st, _ := post(t, srv.URL+"/_sky/ai/device/chat/call", "application/json", callBody("hi"), nil); st != 404 {
		t.Fatalf("DeviceOnly call route: %d", st)
	}
}

func TestDeviceRoutesAbsentWithoutASlot(t *testing.T) {
	aiDeviceReset()
	mux := http.NewServeMux()
	registerAiDeviceRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if r, _ := get(t, srv.URL+"/_sky/ai/device/chat", nil); r.StatusCode != 404 {
		t.Fatalf("no slot: %d", r.StatusCode)
	}
	if r, _ := get(t, srv.URL+AiDeviceWorkerPath, nil); r.StatusCode != 404 {
		t.Fatalf("worker without a slot: %d", r.StatusCode)
	}
}

func TestDeviceServerKernelsAreTheServerSlot(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	upstream := openAIStub(t)
	deviceServer(t, deviceEndpoint("chat", upstream.URL+"/v1", "auto", modelRef, ""))
	r := AnyTaskRun(AiDevice_offer("chat"))
	tag, ok, _ := anyResultView(r)
	var o aiDeviceOffer
	if tag != 0 || json.Unmarshal([]byte(AsString(ok)), &o) != nil || !o.Here {
		t.Fatalf("server offer: %v", r)
	}
	r = AnyTaskRun(AiDevice_server("chat", "/chat/completions", chatBody("hi")))
	tag, ok, _ = anyResultView(r)
	if tag != 0 || chatContent(t, AsString(ok)) != "from the server" {
		t.Fatalf("server call: %v", r)
	}
	r = AnyTaskRun(AiDevice_offer("missing"))
	if tag, _, _ = anyResultView(r); tag == 0 {
		t.Fatal("an unregistered slot answered")
	}
}

func TestDeviceWireFailureRoundTrip(t *testing.T) {
	in := aiDeviceUnsupportedf("webgpu", "WebGPU registered no GPU device")
	f, ok := aiDeviceFailureFrom(aiDeviceFailureJSON(in))
	if !ok || f.code != aiDeviceUnsupported {
		t.Fatalf("round trip: %+v", f)
	}
	if v, _ := f.attrValue("backend"); v != "webgpu" {
		t.Fatalf("backend: %q", v)
	}
	err := aiDeviceShellFailure("ai:" + string(aiDeviceFailureJSON(aiFailf(aiStorageDenied, "full").withInt("needBytes", 10).withInt("availableBytes", 2))))
	if failCode(err) != aiStorageDenied {
		t.Fatalf("shell failure: %v", err)
	}
	if failCode(aiDeviceShellFailure("unavailable: no GPU")) != aiEngineFailed {
		t.Fatal("a plain rejection is EngineFailed")
	}
	kind, df := aiDomainFailure(in)
	if kind != errKindUnavailable || df.Retryable {
		t.Fatalf("DeviceUnsupported kind %d retryable %v", kind, df.Retryable)
	}
}

// A blob outlives the server's write deadline: a device on a slow link reads
// a chunk for longer than SKY_HTTP_WRITE_TIMEOUT, and the response is not cut
// (the Android emulator's 64 MiB chunks were, at 30 s, as "unexpected end of
// stream").
func TestDeviceBlobOutlivesTheServerWriteDeadline(t *testing.T) {
	aiTestEnv(t, nil)
	data := make([]byte, 4<<20)
	for i := range data {
		data[i] = byte(i)
	}
	path, sum := writeBytes(t, "slow.gguf", data)
	aiDeviceReset()
	t.Cleanup(aiDeviceReset)
	cfg := deviceEndpoint("chat", "", "auto", "file:"+path+"#sha256="+sum, "")
	AiDeviceRegister("chat", func() any { return cfg })
	mux := http.NewServeMux()
	registerAiDeviceRoutes(mux)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()
	get(t, srv.URL+"/_sky/ai/device/chat", nil)
	resp, err := http.Get(srv.URL + "/_sky/ai/blob/" + sum)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := 0
	buf := make([]byte, 256<<10)
	for {
		n, err := resp.Body.Read(buf)
		got += n
		time.Sleep(60 * time.Millisecond) // a slow reader: about 1 s in all
		if err != nil {
			break
		}
	}
	if got != len(data) {
		t.Fatalf("read %d of %d bytes: the server cut the response at its write deadline", got, len(data))
	}
}
