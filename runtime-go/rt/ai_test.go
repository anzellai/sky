//go:build !js

package rt

// Tests for Std.Ai.Endpoint and Std.Ai.Local (ai_*.go). They run against a
// FAKE engine (testdata/fakellama), so they are fast and need no model: the
// real llama-server and model are exercised by the live end-to-end test
// (rust/crates/sky/tests/ai_local_live.rs).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── fixtures ───────────────────────────────────────────────────────────

var (
	fakeLlamaOnce sync.Once
	fakeLlamaBin  string
	fakeLlamaErr  error
)

// fakeEngineDir builds the fake engine once and copies it into a fresh
// directory as `llama-server`, with knobs written to its fake.json.
func fakeEngineDir(t *testing.T, knobs map[string]any) string {
	t.Helper()
	fakeLlamaOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sky-fakellama-")
		if err != nil {
			fakeLlamaErr = err
			return
		}
		fakeLlamaBin = filepath.Join(dir, aiExe("fakellama"))
		cmd := exec.Command("go", "build", "-o", fakeLlamaBin, "./testdata/fakellama")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fakeLlamaErr = fmt.Errorf("build fakellama: %v\n%s", err, out)
		}
	})
	if fakeLlamaErr != nil {
		t.Fatal(fakeLlamaErr)
	}
	dir := t.TempDir()
	b, err := os.ReadFile(fakeLlamaBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, aiExe("llama-server")), b, 0o755); err != nil {
		t.Fatal(err)
	}
	if knobs != nil {
		kb, _ := json.Marshal(knobs)
		os.WriteFile(filepath.Join(dir, "fake.json"), kb, 0o644)
	}
	return dir
}

// ggufKV is one metadata entry for writeGGUF.
type ggufKV struct {
	key string
	val any // string, uint32, uint64, or []string (an array, skipped by the reader)
}

func ggufBytes(kvs []ggufKV) []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(0))
	w(uint64(len(kvs)))
	for _, kv := range kvs {
		str(kv.key)
		switch v := kv.val.(type) {
		case string:
			w(uint32(8))
			str(v)
		case uint32:
			w(uint32(4))
			w(v)
		case uint64:
			w(uint32(10))
			w(v)
		case float32:
			w(uint32(6))
			w(v)
		case []string:
			w(uint32(9))
			w(uint32(8))
			w(uint64(len(v)))
			for _, s := range v {
				str(s)
			}
		case []int32:
			w(uint32(9))
			w(uint32(5))
			w(uint64(len(v)))
			for _, x := range v {
				w(x)
			}
		}
	}
	return b.Bytes()
}

// qwenTinyMeta is the shape of Qwen2.5 0.5B: 24 layers, 896 wide, 14 heads,
// 2 KV heads, 32768 trained context.
func qwenTinyMeta() []ggufKV {
	return []ggufKV{
		{"general.architecture", "qwen2"},
		{"general.file_type", uint32(2)},
		{"qwen2.block_count", uint32(24)},
		{"qwen2.context_length", uint32(32768)},
		{"qwen2.embedding_length", uint32(896)},
		{"qwen2.attention.head_count", uint32(14)},
		{"qwen2.attention.head_count_kv", uint32(2)},
		{"qwen2.rope.freq_base", float32(1e6)},
		{"tokenizer.ggml.tokens", []string{"<a>", "<b>", "hello", "world"}},
		{"tokenizer.ggml.token_type", []int32{1, 1, 1, 1}},
	}
}

// writeModel writes a small GGUF model file and returns its path and digest.
func writeModel(t *testing.T, dir string) (string, string) {
	t.Helper()
	p := filepath.Join(dir, "tiny.gguf")
	data := ggufBytes(qwenTinyMeta())
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return p, hex.EncodeToString(sum[:])
}

// aiTestEnv isolates a test: its own AI home, the fake engine, fast timing,
// development mode, and no slot or engine left behind.
func aiTestEnv(t *testing.T, knobs map[string]any) (engineDir, modelRef string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SKY_AI_HOME", home)
	engineDir = fakeEngineDir(t, knobs)
	t.Setenv("SKY_AI_LLAMA_BIN", engineDir)
	t.Setenv("ENV", "")
	t.Setenv("SKY_ENV", "")
	path, sum := writeModel(t, t.TempDir())
	modelRef = "file:" + path + "#sha256=" + sum

	saved := aiK()
	aiSetKnobs(func(k *aiKnobs) {
		k.healthTimeout, k.healthPoll, k.idleTick, k.restartBackoff = 20*time.Second, 20*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
		k.availableBytes = func() int64 { return 8 << 30 }
	})
	t.Cleanup(func() {
		aiResetSlots()
		aiResetEngines()
		// aiResetEngines stops the engines this process still knows. A test
		// that forgets one on purpose (the orphan reap test) and then fails
		// before its reap, or any engine left out of the table, is recorded
		// in this test's AI home: end every one of those too. The home is
		// the test's TempDir, so no later start would ever reap them.
		for _, e := range aiRegistryRead().Engines {
			if aiIsEngineProcess(e) {
				aiKillEngineGroup(e.Pid)
			}
		}
		aiSetKnobs(func(k *aiKnobs) { *k = saved })
	})
	aiResetSlots()
	return engineDir, modelRef
}

// localBuilder is a Local.Spec as the Sky side passes it, unset fields -1.
func localBuilder(model string) aiLocalBuilder {
	return aiLocalBuilder{model: model, context: -1, parallel: -1, idleUnload: -1, port: -1}
}

func endpointBuilder(slot string) aiEndpointBuilder {
	return aiEndpointBuilder{slot: slot, timeoutMs: -1, maxInFlight: -1, queue: -1, queueWaitMs: -1, retries: -1}
}

// failCode extracts the failure code from an error, or "".
func failCode(err error) aiFailCode {
	if f, ok := err.(*aiFailure); ok {
		return f.code
	}
	return ""
}

func chatBody(text string) string {
	return `{"model":"","messages":[{"role":"user","content":` + jsonQuote(text) + `}]}`
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func chatContent(t *testing.T, body string) string {
	t.Helper()
	var r struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || len(r.Choices) == 0 {
		t.Fatalf("not a chat answer: %v %q", err, body)
	}
	return r.Choices[0].Message.Content
}

// ─── resolution ─────────────────────────────────────────────────────────

func TestAiResolutionRule(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	local := endpointBuilder("chat")
	local.hasLocal, local.local = true, localBuilder(model)

	t.Run("a URL means remote", func(t *testing.T) {
		t.Setenv("SKY_AI_CHAT_URL", "https://gpu.example.test/v1")
		c, err := resolveAiSlot(local)
		if err != nil || c.mode != "remote" || c.baseURL != "https://gpu.example.test/v1" {
			t.Fatalf("got %+v, %v; want remote (a URL wins over a withLocal spec)", c, err)
		}
	})
	t.Run("withLocal means local", func(t *testing.T) {
		c, err := resolveAiSlot(local)
		if err != nil || c.mode != "local" || c.local.model.sha256 == "" {
			t.Fatalf("got %+v, %v; want local", c, err)
		}
	})
	t.Run("MODE=local with a URL is an error naming both sources", func(t *testing.T) {
		t.Setenv("SKY_AI_CHAT_MODE", "local")
		b := local
		b.url = "https://gpu.example.test/v1"
		_, err := resolveAiSlot(b)
		if failCode(err) != aiInvalidSetting || !strings.Contains(err.Error(), "SKY_AI_CHAT_MODE") ||
			!strings.Contains(err.Error(), "Endpoint.withUrl") {
			t.Fatalf("got %v; want InvalidSetting naming SKY_AI_CHAT_MODE and Endpoint.withUrl", err)
		}
	})
	t.Run("MODE=remote obeyed: no URL is NotConfigured, never local", func(t *testing.T) {
		t.Setenv("SKY_AI_CHAT_MODE", "remote")
		_, err := resolveAiSlot(local)
		if failCode(err) != aiNotConfigured || !strings.Contains(err.Error(), "SKY_AI_CHAT_URL") {
			t.Fatalf("got %v; want NotConfigured naming SKY_AI_CHAT_URL", err)
		}
	})
	t.Run("MODE=local with a MODEL from env needs no withLocal", func(t *testing.T) {
		t.Setenv("SKY_AI_CHAT_MODE", "local")
		t.Setenv("SKY_AI_CHAT_MODEL", model)
		c, err := resolveAiSlot(endpointBuilder("chat"))
		if err != nil || c.mode != "local" || c.local.model.String() != model {
			t.Fatalf("got %+v, %v", c, err)
		}
	})
	t.Run("nothing is NotConfigured naming the variables", func(t *testing.T) {
		_, err := resolveAiSlot(endpointBuilder("chat"))
		if failCode(err) != aiNotConfigured || !strings.Contains(err.Error(), "SKY_AI_CHAT_URL") ||
			!strings.Contains(err.Error(), "Endpoint.withLocal") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an unknown MODE is refused", func(t *testing.T) {
		t.Setenv("SKY_AI_CHAT_MODE", "cloud")
		_, err := resolveAiSlot(local)
		if failCode(err) != aiInvalidSetting {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an unpinned model is refused", func(t *testing.T) {
		b := local
		b.local.model = "hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/x.gguf"
		_, err := resolveAiSlot(b)
		if failCode(err) != aiInvalidSetting || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestAiSlotNames(t *testing.T) {
	for _, ok := range []string{"chat", "embed", "judge_2"} {
		if err := aiValidSlot(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Chat", "2chat", "chat-x", "home", "fetch", "serve"} {
		if err := aiValidSlot(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The precedence gate: operator env > withX in code > stdlib fallback, a
// seeded value is never an operator value, and an unparseable value falls
// through instead of collapsing to the fallback.
func TestAiPrecedenceEnvBuilderFallback(t *testing.T) {
	aiTestEnv(t, nil)
	b := endpointBuilder("chat")
	b.url = "https://code.example.test/v1"

	c, err := resolveAiSlot(b)
	if err != nil || c.timeout != 120*time.Second {
		t.Fatalf("fallback: got %v, %v; want 120s", c.timeout, err)
	}
	b.timeoutMs = 7000
	c, _ = resolveAiSlot(b)
	if c.timeout != 7*time.Second {
		t.Fatalf("builder: got %v; want 7s", c.timeout)
	}
	t.Setenv("SKY_AI_CHAT_TIMEOUT_MS", "5000")
	c, _ = resolveAiSlot(b)
	if c.timeout != 5*time.Second {
		t.Fatalf("env: got %v; want 5s (operator env beats the builder)", c.timeout)
	}
	t.Setenv("SKY_AI_CHAT_TIMEOUT_MS", "soon")
	c, _ = resolveAiSlot(b)
	if c.timeout != 7*time.Second {
		t.Fatalf("unparseable env: got %v; want the builder's 7s", c.timeout)
	}
	t.Setenv("SKY_AI_CHAT_TIMEOUT_MS", "")
	SetEnvDefault("SKY_AI_CHAT_TIMEOUT_MS", "9000")
	defer clearSeededDefault("SKY_AI_CHAT_TIMEOUT_MS")
	c, _ = resolveAiSlot(b)
	if c.timeout != 7*time.Second {
		t.Fatalf("seeded: got %v; want the builder's 7s (a seeded value is not an operator value)", c.timeout)
	}

	t.Setenv("SKY_AI_CHAT_URL", "https://env.example.test/v1")
	c, _ = resolveAiSlot(b)
	if c.baseURL != "https://env.example.test/v1" {
		t.Fatalf("URL: got %q", c.baseURL)
	}
	var urlSrc string
	for _, s := range c.settings {
		if s.key == "URL" {
			urlSrc = s.source
		}
	}
	if urlSrc != "env" {
		t.Fatalf("URL source %q, want env", urlSrc)
	}
}

func TestAiPrefixIsHonoured(t *testing.T) {
	aiTestEnv(t, nil)
	SetEnvPrefix("FENCE")
	defer SetEnvPrefix("SKY")
	t.Setenv("FENCE_AI_CHAT_URL", "https://fence.example.test/v1")
	t.Setenv("SKY_AI_CHAT_URL", "https://wrong.example.test/v1")
	c, err := resolveAiSlot(endpointBuilder("chat"))
	if err != nil || c.baseURL != "https://fence.example.test/v1" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestAiKeyIsASecretAndDescribeHidesIt(t *testing.T) {
	aiTestEnv(t, nil)
	t.Setenv("SKY_AI_CHAT_URL", "https://user:pw@gpu.example.test:8443/v1/tenant?token=abc")
	t.Setenv("SKY_AI_CHAT_KEY", "sk-very-secret-value")
	s, err := aiSlotFor(endpointBuilder("chat"))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := json.Marshal(s.description())
	for _, leak := range []string{"sk-very-secret-value", "token=abc", "tenant", "pw@"} {
		if strings.Contains(string(d), leak) {
			t.Fatalf("describe leaks %q: %s", leak, d)
		}
	}
	if !strings.Contains(string(d), "https://gpu.example.test:8443") {
		t.Fatalf("describe lost the host: %s", d)
	}
}

// ─── fetch and verify ───────────────────────────────────────────────────

func TestAiChecksumMismatchInstallsNothing(t *testing.T) {
	aiTestEnv(t, nil)
	payload := ggufBytes(qwenTinyMeta())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer srv.Close()
	saved := aiHuggingFaceURL
	aiHuggingFaceURL = func(repo, file string) string { return srv.URL + "/" + repo + "/" + file }
	defer func() { aiHuggingFaceURL = saved }()

	wrong := strings.Repeat("0", 64)
	_, err := aiEnsureModel(context.Background(), aiModelRef{kind: "hf", repo: "o/r", file: "m.gguf", sha256: wrong})
	if failCode(err) != aiChecksumMismatch || !strings.Contains(err.Error(), wrong) {
		t.Fatalf("got %v; want ChecksumMismatch naming the expected digest", err)
	}
	if fileExists(aiModelStorePath(wrong)) {
		t.Fatal("a mismatched model was installed")
	}
	if ents, _ := os.ReadDir(filepath.Join(aiHome(), "downloads")); len(ents) > 0 {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".partial") {
				t.Fatalf("a mismatched partial download was kept: %s", e.Name())
			}
		}
	}
	sum := sha256.Sum256(payload)
	right := hex.EncodeToString(sum[:])
	p, err := aiEnsureModel(context.Background(), aiModelRef{kind: "hf", repo: "o/r", file: "m.gguf", sha256: right})
	if err != nil || p != aiModelStorePath(right) {
		t.Fatalf("verified download: %q, %v", p, err)
	}
	// A file: model is hashed where it lies.
	path, _ := writeModel(t, t.TempDir())
	_, err = aiEnsureModel(context.Background(), aiModelRef{kind: "file", file: path, sha256: wrong})
	if failCode(err) != aiChecksumMismatch {
		t.Fatalf("file model: got %v", err)
	}
}

func TestAiEngineArchiveMismatchInstallsNothing(t *testing.T) {
	aiTestEnv(t, nil)
	t.Setenv("SKY_AI_LLAMA_BIN", "")
	// Pin an archive for THIS host, whatever it is, so the test runs on every
	// platform the runtime builds for.
	savedAssets := aiLlamaAssets
	aiLlamaAssets = []aiLlamaAsset{{goos: runtime.GOOS, goarch: runtime.GOARCH, variant: "cpu",
		file: "llama-test.tar.gz", sha256: strings.Repeat("1", 64)}}
	defer func() { aiLlamaAssets = savedAssets }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not the archive")) }))
	defer srv.Close()
	saved := aiLlamaReleaseURL
	aiLlamaReleaseURL = func(file string) string { return srv.URL + "/" + file }
	defer func() { aiLlamaReleaseURL = saved }()
	_, err := aiDiscoverEngine(context.Background())
	if failCode(err) != aiChecksumMismatch {
		t.Fatalf("got %v; want ChecksumMismatch", err)
	}
	a, _ := aiHostAsset()
	if fileExists(aiEngineDir(a)) {
		t.Fatal("a mismatched engine was installed")
	}
}

func TestAiFetchOffIsNotProvisioned(t *testing.T) {
	aiTestEnv(t, nil)
	t.Setenv("SKY_AI_FETCH", "off")
	_, err := aiEnsureModel(context.Background(), aiModelRef{kind: "hf", repo: "o/r", file: "m.gguf", sha256: strings.Repeat("a", 64)})
	if failCode(err) != aiNotProvisioned || !strings.Contains(err.Error(), "sky ai pull") {
		t.Fatalf("got %v", err)
	}
	t.Setenv("SKY_AI_FETCH", "")
	t.Setenv("ENV", "production")
	if ok, _ := aiFetchAllowed(); ok {
		t.Fatal("fetching is on in production by default")
	}
	t.Setenv("SKY_AI_LLAMA_BIN", t.TempDir())
	if _, err := aiDiscoverEngine(context.Background()); failCode(err) != aiNotProvisioned {
		t.Fatalf("an AI_LLAMA_BIN with no llama-server: got %v", err)
	}
}

// ─── GGUF and fit ───────────────────────────────────────────────────────

func TestAiGGUFMetadataAndFit(t *testing.T) {
	m, err := parseGGUFMeta(bytes.NewReader(ggufBytes(qwenTinyMeta())))
	if err != nil {
		t.Fatal(err)
	}
	if m.arch != "qwen2" || m.blockCount != 24 || m.headCountKV != 2 || m.contextLength != 32768 || m.embedding != 896 {
		t.Fatalf("meta %+v", m)
	}
	// 24 layers x 2 KV heads x (64+64) x 2 bytes = 12288 bytes per token.
	if got := aiKVBytesPerToken(m); got != 12288 {
		t.Fatalf("KV bytes per token %d, want 12288", got)
	}
	weights := int64(400 << 20)

	f, err := aiPlanFit(m, weights, 0, "auto", "metal", 4<<30)
	if err != nil || f.contextTokens != aiDefaultContext || f.gpuLayers != 25 || f.expectedTps == 0 {
		t.Fatalf("roomy host: %+v, %v", f, err)
	}
	// The default context is reduced to fit, never below the minimum.
	tight := weights + aiComputeOverhead + weights/20 + 12288*2048 + 1
	f, err = aiPlanFit(m, weights, 0, "auto", "cpu", tight)
	if err != nil || f.contextTokens != 2048 || f.gpuLayers != 0 {
		t.Fatalf("tight host: %+v, %v", f, err)
	}
	// An explicit context is honoured or refused, never cut.
	_, err = aiPlanFit(m, weights, 8192, "auto", "cpu", tight)
	if failCode(err) != aiModelTooLarge {
		t.Fatalf("explicit context: got %v; want ModelTooLarge", err)
	}
	_, err = aiPlanFit(m, weights, 0, "auto", "cpu", weights)
	if failCode(err) != aiModelTooLarge || !strings.Contains(err.Error(), "available") {
		t.Fatalf("too small: got %v", err)
	}
	if _, err = aiPlanFit(m, weights, 0, "all", "cpu", 4<<30); failCode(err) != aiNoAccelerator {
		t.Fatalf("gpu=all on a CPU build: got %v; want NoAccelerator", err)
	}
	if _, err = aiPlanFit(m, weights, 65536, "auto", "cpu", 64<<30); failCode(err) != aiInvalidSetting {
		t.Fatalf("context over the trained context: got %v", err)
	}
	if aiErrorValue(aiFailf(aiModelTooLarge, "x")).(skyErrorAdt).Fields[0] != errorKindAdt(7, "InvalidInput") {
		t.Fatal("ModelTooLarge is not InvalidInput")
	}
}

func TestAiGGUFRejectsHostileHeaders(t *testing.T) {
	cases := map[string][]byte{
		"empty":     nil,
		"not gguf":  []byte("GGML\x03\x00\x00\x00"),
		"truncated": ggufBytes(qwenTinyMeta())[:40],
	}
	var huge bytes.Buffer
	huge.WriteString("GGUF")
	binary.Write(&huge, binary.LittleEndian, uint32(3))
	binary.Write(&huge, binary.LittleEndian, uint64(0))
	binary.Write(&huge, binary.LittleEndian, uint64(1))
	binary.Write(&huge, binary.LittleEndian, uint64(1<<40)) // a 1 TB key
	cases["huge string"] = huge.Bytes()
	for name, b := range cases {
		if _, err := parseGGUFMeta(bytes.NewReader(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ─── queue ──────────────────────────────────────────────────────────────

func TestAiQueueOverloadedAndTimedOut(t *testing.T) {
	q := newAiQueue("chat", 1, 1, 200*time.Millisecond)
	release, err := q.acquire()
	if err != nil {
		t.Fatal(err)
	}
	waiterErr := make(chan error, 1)
	go func() {
		r, err := q.acquire()
		if r != nil {
			r()
		}
		waiterErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := q.acquire(); failCode(err) != aiOverloaded {
		t.Fatalf("full queue: got %v; want Overloaded at once", err)
	}
	if err := <-waiterErr; failCode(err) != aiTimedOut {
		t.Fatalf("waiter: got %v; want TimedOut", err)
	}
	release()
	r, err := q.acquire()
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	r()
	if k := aiErrorValue(aiFailf(aiOverloaded, "x")).(skyErrorAdt).Fields[0]; k != errorKindAdt(9, "Unavailable") {
		t.Fatalf("Overloaded kind %v", k)
	}
	if k := aiErrorValue(aiFailf(aiTimedOut, "x")).(skyErrorAdt).Fields[0]; k != errorKindAdt(4, "Timeout") {
		t.Fatalf("TimedOut kind %v", k)
	}
}

// ─── remote ─────────────────────────────────────────────────────────────

func TestAiRemoteRetriesOnlyOverloadAndNeverFallsBackToLocal(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	var hits atomic.Int64
	var mode atomic.Value
	mode.Store("429-then-ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer k-1" {
			w.WriteHeader(401)
			return
		}
		switch mode.Load().(string) {
		case "429-then-ok":
			if n == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(429)
				return
			}
		case "always-503":
			w.WriteHeader(503)
			return
		case "slow":
			time.Sleep(400 * time.Millisecond)
		case "bad-request":
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"bad"}`)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"model=%v"}}],"usage":{}}`, req["model"])
	}))
	defer srv.Close()

	b := endpointBuilder("chat")
	b.url, b.key, b.hasKey, b.model = srv.URL+"/v1", "k-1", true, "base-8b"
	b.hasLocal, b.local = true, localBuilder(model) // must NOT be used: the URL wins
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.post("/chat/completions", chatBody("hi"))
	if err != nil || chatContent(t, out) != "model=base-8b" || hits.Load() != 2 {
		t.Fatalf("429 then ok: %q, %v, hits %d", out, err, hits.Load())
	}

	mode.Store("always-503")
	hits.Store(0)
	_, err = s.post("/chat/completions", chatBody("hi"))
	if failCode(err) != aiOverloaded || hits.Load() != 3 {
		t.Fatalf("always 503: %v after %d hits; want Overloaded after 1 + 2 retries", err, hits.Load())
	}
	aiEnginesMu.Lock()
	n := len(aiEngines)
	aiEnginesMu.Unlock()
	if n != 0 {
		t.Fatal("a remote failure started a local engine")
	}

	mode.Store("bad-request")
	hits.Store(0)
	_, err = s.post("/chat/completions", chatBody("hi"))
	if failCode(err) != aiRemoteRejected || hits.Load() != 1 {
		t.Fatalf("400: %v after %d hits; want no retry", err, hits.Load())
	}

	mode.Store("slow")
	hits.Store(0)
	b2 := b
	b2.timeoutMs = 100
	s2, _ := aiSlotFor(b2)
	_, err = s2.post("/chat/completions", chatBody("hi"))
	if failCode(err) != aiTimedOut {
		t.Fatalf("slow: got %v; want TimedOut", err)
	}
	time.Sleep(450 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("a timed-out call was retried (%d hits)", hits.Load())
	}

	b3 := endpointBuilder("chat")
	b3.url = "http://127.0.0.1:1/v1"
	b3.retries = 0
	s3, _ := aiSlotFor(b3)
	if _, err = s3.post("/chat/completions", chatBody("hi")); failCode(err) != aiUnreachable {
		t.Fatalf("down: got %v; want Unreachable", err)
	}
}

func TestAiPatchModelKeepsAnExplicitModel(t *testing.T) {
	if got := aiPatchModel(`{"model":"","x":1}`, "m"); !strings.Contains(got, `"model":"m"`) {
		t.Fatal(got)
	}
	if got := aiPatchModel(`{"model":"adapter-1"}`, "m"); !strings.Contains(got, `"model":"adapter-1"`) {
		t.Fatal(got)
	}
}

// ─── the local engine (fake) ────────────────────────────────────────────

func localSlot(t *testing.T, model string, mutate func(*aiEndpointBuilder)) *aiSlot {
	t.Helper()
	b := endpointBuilder("chat")
	b.hasLocal, b.local = true, localBuilder(model)
	if mutate != nil {
		mutate(&b)
	}
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAiLocalEngineServesChatAndEmbeddings(t *testing.T) {
	dir, model := aiTestEnv(t, nil)
	t.Setenv("SKY_AI_TEST_OPERATOR_SECRET", "must-not-reach-the-engine")
	s := localSlot(t, model, func(b *aiEndpointBuilder) { b.local.embeddings = true })
	out, err := s.post("/chat/completions", chatBody("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got := chatContent(t, out); got != "fake[local]: hello" {
		t.Fatalf("chat %q", got)
	}
	out, err = s.post("/embeddings", `{"model":"","input":["ab","cd"]}`)
	if err != nil || !strings.Contains(out, `"embedding"`) {
		t.Fatalf("embeddings %q, %v", out, err)
	}
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	e := s.engine
	if strings.Contains(string(launch), e.apiKey) {
		t.Fatal("the engine API key is on the command line")
	}
	for _, want := range []string{"--host 127.0.0.1", "--api-key-file", "--embeddings", "-c 8192", "-np 2", "--no-webui"} {
		if !strings.Contains(string(launch), want) {
			t.Fatalf("launch %q lacks %q", launch, want)
		}
	}
	st, err := os.Stat(e.keyFile)
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("key file %v %v", st, err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	if strings.Contains(string(env), "SKY_AI_TEST_OPERATOR_SECRET") || strings.Contains(string(env), "SKY_AI_HOME") {
		t.Fatalf("the engine inherited operator variables: %s", env)
	}
	if !strings.Contains(string(env), "PATH") {
		t.Fatalf("the engine lost PATH: %s", env)
	}
	// The engine is recorded in engines.json with both liveness legs.
	list := aiRegistryList()
	if len(list) != 1 || list[0].(map[string]any)["state"] != "running" {
		t.Fatalf("registry %v", list)
	}
}

func TestAiLocalEngineRefusesWithoutTheKey(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	s := localSlot(t, model, nil)
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", s.engine.port), "application/json", strings.NewReader(chatBody("x")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("a call without the per-run key got %d", resp.StatusCode)
	}
}

func TestAiEngineCrashIsTypedAndRestarts(t *testing.T) {
	dir, model := aiTestEnv(t, map[string]any{"crashOnRequest": 1, "firstLaunchOnly": true})
	s := localSlot(t, model, nil)
	_, err := s.post("/chat/completions", chatBody("boom"))
	if failCode(err) != aiEngineCrashed || !strings.Contains(err.Error(), "GGML_ASSERT") || !strings.Contains(err.Error(), "134") {
		t.Fatalf("got %v; want EngineCrashed with the exit code and the log tail", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := s.post("/chat/completions", chatBody("again"))
		if err == nil {
			if chatContent(t, out) != "fake[local]: again" {
				t.Fatal(out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine did not come back: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	if n := strings.Count(string(launch), "\n"); n != 2 {
		t.Fatalf("%d launches, want 2", n)
	}
	if st := s.engine.status().(map[string]any); st["restarts"].(int) < 1 || st["lastCrash"] == "" {
		t.Fatalf("status %v", st)
	}
}

func TestAiEngineStaysDownAfterRepeatedCrashes(t *testing.T) {
	_, model := aiTestEnv(t, map[string]any{"crashOnRequest": 1})
	aiSetKnobs(func(k *aiKnobs) { k.maxCrashes = 2 })
	s := localSlot(t, model, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := s.post("/chat/completions", chatBody("x"))
		if err != nil && strings.Contains(err.Error(), "stays down") {
			if failCode(err) != aiEngineCrashed {
				t.Fatalf("got %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine never gave up: last %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestAiEngineCrashOutsideDevelopmentExitsTheApp(t *testing.T) {
	_, model := aiTestEnv(t, map[string]any{"crashOnRequest": 1})
	exited := make(chan string, 1)
	aiSetKnobs(func(k *aiKnobs) { k.fatalExit = func(msg string) { exited <- msg } })
	s := localSlot(t, model, nil)
	if _, err := s.ensureEngine(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV", "production")
	_, err := s.post("/chat/completions", chatBody("x"))
	if failCode(err) != aiEngineCrashed {
		t.Fatalf("got %v", err)
	}
	select {
	case msg := <-exited:
		if !strings.Contains(msg, "exits") {
			t.Fatal(msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a production engine crash did not exit the app")
	}
}

func TestAiEngineThatDiesWhileLoadingIsEngineCrashed(t *testing.T) {
	_, model := aiTestEnv(t, map[string]any{"exitOnStart": 3})
	s := localSlot(t, model, nil)
	_, err := s.post("/chat/completions", chatBody("x"))
	if failCode(err) != aiEngineCrashed || !strings.Contains(err.Error(), "refusing to load") {
		t.Fatalf("got %v", err)
	}
}

func TestAiEngineWaitsForHealth(t *testing.T) {
	_, model := aiTestEnv(t, map[string]any{"healthDelayMs": 400})
	s := localSlot(t, model, nil)
	start := time.Now()
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("a call went out before /health was ready")
	}
}

func TestAiIdleUnloadStopsAndRestarts(t *testing.T) {
	dir, model := aiTestEnv(t, nil)
	s := localSlot(t, model, func(b *aiEndpointBuilder) { b.local.idleUnload = 1 })
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	pid := s.engine.pid
	deadline := time.Now().Add(5 * time.Second)
	for s.engine.status().(map[string]any)["state"] != "stopped" {
		if time.Now().After(deadline) {
			t.Fatal("the idle engine was not unloaded")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !aiWaitGone(pid, 5*time.Second) {
		t.Fatal("the unloaded engine process still runs")
	}
	if len(aiRegistryList()) != 0 {
		t.Fatal("an unloaded engine is still in engines.json")
	}
	if _, err := s.post("/chat/completions", chatBody("y")); err != nil {
		t.Fatal(err)
	}
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	if n := strings.Count(string(launch), "\n"); n != 2 {
		t.Fatalf("%d launches, want 2 (one before and one after the unload)", n)
	}
}

func TestAiLocalSlotQueueBoundsTheEngine(t *testing.T) {
	_, model := aiTestEnv(t, map[string]any{"slowMs": 300})
	s := localSlot(t, model, func(b *aiEndpointBuilder) { b.maxInFlight, b.queue, b.queueWaitMs = 1, 0, 50 })
	if err := func() error { _, err := s.ensureEngine(context.Background(), nil); return err }(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.post("/chat/completions", chatBody("x"))
			errs <- err
		}()
		time.Sleep(30 * time.Millisecond)
	}
	wg.Wait()
	close(errs)
	var ok, over int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case failCode(err) == aiOverloaded:
			over++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	if ok != 1 || over != 1 {
		t.Fatalf("ok %d overloaded %d; want 1 and 1", ok, over)
	}
}

func TestAiEnginesAreSharedAcrossProcessesThroughTheRegistry(t *testing.T) {
	dir, model := aiTestEnv(t, nil)
	s := localSlot(t, model, nil)
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	// Pretend another live process holds it: re-home the entry to our parent.
	aiRegistryUpdate(func(f *aiRegistryFile) {
		for i := range f.Engines {
			f.Engines[i].HolderPid = os.Getppid()
		}
	})
	aiEnginesMu.Lock()
	aiEngines = map[string]*aiEngine{}
	aiEnginesMu.Unlock()
	aiResetSlots()
	s2 := localSlot(t, model, nil)
	out, err := s2.post("/chat/completions", chatBody("shared"))
	if err != nil || chatContent(t, out) != "fake[local]: shared" {
		t.Fatalf("%q %v", out, err)
	}
	if !s2.engine.attached {
		t.Fatal("the second program started its own engine instead of using the running one")
	}
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	if n := strings.Count(string(launch), "\n"); n != 1 {
		t.Fatalf("%d launches, want 1", n)
	}
	// sky ai stop: the engine whose holder is "another process" is ended by
	// its process group (our parent is not signalled: it is the test runner,
	// so give the entry back first and stop the engine as its holder).
	aiRegistryUpdate(func(f *aiRegistryFile) {
		for i := range f.Engines {
			f.Engines[i].HolderPid = os.Getpid()
		}
	})
	pid := s.engine.pid
	aiEnginesMu.Lock()
	aiEngines[s.engine.key] = s.engine
	aiEnginesMu.Unlock()
	if n := aiStopSlot("chat"); n != 1 {
		t.Fatalf("stopped %d", n)
	}
	if !aiWaitGone(pid, 5*time.Second) {
		t.Fatal("sky ai stop left the engine running")
	}
}

func TestAiStopSlotEndsAnOrphanedEngine(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	s := localSlot(t, model, nil)
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	pid := s.engine.pid
	// The holder is gone (a pid that cannot be alive): the engine is orphaned.
	aiRegistryUpdate(func(f *aiRegistryFile) {})
	b, _ := os.ReadFile(aiRegistryPath())
	var f aiRegistryFile
	json.Unmarshal(b, &f)
	f.Engines[0].HolderPid = 999999
	nb, _ := json.Marshal(f)
	os.WriteFile(aiRegistryPath(), nb, 0o600)
	if st := aiRegistryList()[0].(map[string]any)["state"]; st != "orphaned" {
		t.Fatalf("state %v", st)
	}
	if n := aiStopSlot(""); n != 1 {
		t.Fatalf("stopped %d", n)
	}
	if !aiWaitGone(pid, 5*time.Second) {
		t.Fatal("the orphaned engine still runs")
	}
}

// The exit path of a program (killAllChildProcesses, from generated main's
// defer, ExitProcess and the shutdown drain) stops its engines as engines
// BEFORE it kills the children. Before, it killed the engine like any other
// child: the supervisor saw an unexpected exit, counted a crash (in
// production: exited the app; in development: queued a restart), and only
// its goroutine removed the registry entry, in a race with the process exit.
// When the process won, `engines.json` kept an entry for an engine that was
// gone (ai_cli_flow.rs endpoint test, about 1 run in 35 under load).
func TestAiExitPathStopsEnginesBeforeKillingChildren(t *testing.T) {
	dir, model := aiTestEnv(t, nil)
	s := localSlot(t, model, nil)
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	pid := s.engine.pid
	killAllChildProcesses()
	// The process would end here: what is on disk now is what it leaves.
	if list := aiRegistryList(); len(list) != 0 {
		t.Fatalf("the exit path left registry entries: %v", list)
	}
	if !aiWaitGone(pid, 5*time.Second) {
		t.Fatal("the exit path left the engine running")
	}
	// The stop was intentional: no crash is recorded and nothing restarts.
	time.Sleep(300 * time.Millisecond)
	st := s.engine.status().(map[string]any)
	if st["state"] != "stopped" || st["lastCrash"] != "" {
		t.Fatalf("the exit path was taken for a crash: %v", st)
	}
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	if n := strings.Count(string(launch), "\n"); n != 1 {
		t.Fatalf("%d launches, want 1", n)
	}
}

// A holder killed without running its exit path (SIGKILL, SIGPIPE) leaves its
// engine running in its own process group. The next program that starts an
// engine ends it, and only it: an entry whose pid now names another process
// (no key file on its command line) is never killed.
func TestAiOrphanedEnginesAreReapedByTheNextStart(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	s := localSlot(t, model, nil)
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		t.Fatal(err)
	}
	orphan := s.engine.pid
	// Ours to end whatever happens below: this test forgets the engine on
	// purpose, so neither aiResetEngines nor a t.Fatal before the reap would.
	t.Cleanup(func() {
		if processAlive(orphan) {
			aiKillEngineGroup(orphan)
		}
	})
	// A bystander process recorded under a recycled pid must survive.
	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	defer bystander.Process.Kill()
	aiRegistryUpdate(func(f *aiRegistryFile) {
		f.Engines[0].HolderPid = 999999
	})
	b, _ := os.ReadFile(aiRegistryPath())
	var f aiRegistryFile
	json.Unmarshal(b, &f)
	f.Engines[0].HolderPid = 999999
	recycled := f.Engines[0]
	recycled.ID, recycled.Pid, recycled.KeyFile = "recycled", bystander.Process.Pid, "/nonexistent/key"
	f.Engines = append(f.Engines, recycled)
	nb, _ := json.Marshal(f)
	os.WriteFile(aiRegistryPath(), nb, 0o600)
	// Forget the engine in this process, as a dead holder would have: no
	// supervisor is left to restart it.
	s.engine.mu.Lock()
	s.engine.retired = true
	s.engine.mu.Unlock()
	aiEnginesMu.Lock()
	aiEngines = map[string]*aiEngine{}
	aiEnginesMu.Unlock()
	aiResetSlots()

	if n := aiReapOrphans(); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if !aiWaitGone(orphan, 5*time.Second) {
		t.Fatal("the orphaned engine still runs")
	}
	if !processAlive(bystander.Process.Pid) {
		t.Fatal("a process under a recycled pid was killed")
	}
}

// ─── test mode ──────────────────────────────────────────────────────────

func TestAiTestModeAnswersFromFixturesAndStartsNoEngine(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	mocks := t.TempDir()
	os.WriteFile(filepath.Join(mocks, "chat.json"), []byte(`{"match":{"urlContains":"/chat/completions"},"status":200,"body":"{\"choices\":[{\"message\":{\"content\":\"MOCKED\"}}],\"usage\":{}}"}`), 0o644)
	t.Setenv("SKY_TEST_MOCKS_DIR", mocks)
	initTestMode()
	savedOn := testModeOn
	testModeOn = true
	httpMocksOnce = sync.Once{}
	defer func() { testModeOn = savedOn; httpMocksOnce = sync.Once{} }()

	s := localSlot(t, model, nil)
	out, err := s.post("/chat/completions", chatBody("x"))
	if err != nil || chatContent(t, out) != "MOCKED" {
		t.Fatalf("%q %v", out, err)
	}
	if s.engine != nil {
		t.Fatal("test mode started an engine")
	}
}

// ─── kernels ────────────────────────────────────────────────────────────

func TestAiEndpointKernelRoundTrip(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	cfg := map[string]any{
		"slot": "chat", "mode": "", "url": "", "hasKey": false, "key": Secret{},
		"model": "", "adapter": "", "timeoutMs": -1, "maxInFlight": -1, "queue": -1,
		"queueWaitMs": -1, "retries": -1, "hasLocal": true,
		"local": map[string]any{"model": model, "context": -1, "gpu": "", "parallel": -1,
			"idleUnload": -1, "embeddings": false, "port": -1},
	}
	res := Ai_endpointPost(cfg, "/chat/completions", chatBody("kernel")).(func() any)().(SkyResult[any, any])
	if res.Tag != 0 || chatContent(t, res.OkValue.(string)) != "fake[local]: kernel" {
		t.Fatalf("%+v", res)
	}
	d := Ai_endpointDescribe(cfg).(func() any)().(SkyResult[any, any])
	if d.Tag != 0 || d.OkValue.(map[string]any)["mode"] != "local" {
		t.Fatalf("%+v", d)
	}
	bad := map[string]any{"slot": "Bad Slot"}
	if r := Ai_endpointPost(bad, "/chat/completions", "{}").(func() any)().(SkyResult[any, any]); r.Tag == 0 {
		t.Fatal("an invalid slot was accepted")
	}
}

func TestAiPullStoresByDigestAndProvisionHonoursAnOperatorBuild(t *testing.T) {
	aiTestEnv(t, nil)
	payload := ggufBytes(qwenTinyMeta())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer srv.Close()
	saved := aiHuggingFaceURL
	aiHuggingFaceURL = func(repo, file string) string { return srv.URL + "/" + repo + "/" + file }
	defer func() { aiHuggingFaceURL = saved }()

	res := Ai_localPull("org/repo", "m.gguf").(func() any)().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("%+v", res)
	}
	got := res.OkValue.(map[string]any)
	sum := sha256.Sum256(payload)
	if got["sha256"] != hex.EncodeToString(sum[:]) || got["path"] != aiModelStorePath(got["sha256"].(string)) || got["bytes"] != len(payload) {
		t.Fatalf("pull %v", got)
	}
	// The pulled model now satisfies a pinned reference with fetching off.
	t.Setenv("SKY_AI_FETCH", "off")
	ref := "hf:org/repo/m.gguf#sha256=" + got["sha256"].(string)
	if r := Ai_localFetch(ref).(func() any)().(SkyResult[any, any]); r.Tag != 0 {
		t.Fatalf("fetch after pull: %+v", r)
	}

	p := Ai_localProvision(struct{}{}).(func() any)().(SkyResult[any, any])
	if p.Tag != 0 || p.OkValue.(map[string]any)["variant"] != "custom" {
		t.Fatalf("provision with an operator build: %+v", p)
	}
}

// An interrupted download resumes with a Range request, and the digest is
// still taken from the whole file on disk.
func TestAiDownloadResumesAPartialFile(t *testing.T) {
	aiTestEnv(t, nil)
	payload := bytes.Repeat([]byte("sky-ai-resume-"), 5000)
	var ranged atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranged.Store(true)
		}
		http.ServeContent(w, r, "m.gguf", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	dest := filepath.Join(aiHome(), "models", "resumed.gguf")
	dl := filepath.Join(aiHome(), "downloads")
	os.MkdirAll(dl, 0o755)
	os.WriteFile(filepath.Join(dl, "resumed.gguf.partial"), payload[:len(payload)/3], 0o644)
	sum := sha256.Sum256(payload)
	got, err := aiDownload(context.Background(), srv.URL+"/m.gguf", dest, hex.EncodeToString(sum[:]))
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("%q %v", got, err)
	}
	if !ranged.Load() {
		t.Fatal("the download started again from zero instead of resuming")
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, payload) {
		t.Fatal("the resumed file differs")
	}
}

// TestAiForeignListenerOnTheEnginePortIsEngineFailed is the regression for a
// start that accepted another process as its engine. /health needs no key,
// so with a fixed port (`Local.withPort`) any server already listening there
// answered the readiness check, the engine that could not bind exited unseen,
// and every call went to the foreign server. The readiness check must prove
// the listener is the engine this program started: it must accept the
// launch's fresh key and refuse any other. A listener that does not is an
// EngineFailed start.
func TestAiForeignListenerOnTheEnginePortIsEngineFailed(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	// A keyless server that answers every path with 200, as a llama-server
	// started without --api-key does.
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions" {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"foreign"}}]}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer foreign.Close()
	port := foreign.Listener.Addr().(*net.TCPAddr).Port
	s := localSlot(t, model, func(b *aiEndpointBuilder) { b.local.port = port })
	out, err := s.post("/chat/completions", chatBody("x"))
	if failCode(err) != aiEngineFailed {
		t.Fatalf("a foreign listener on the engine port was accepted as the engine: out=%q err=%v", out, err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(port)) {
		t.Fatalf("the failure does not name the port: %v", err)
	}
}
