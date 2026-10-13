//go:build !js

package rt

// Tests of the GPU tier (ai_serve.go) against testdata/fakevllm, a Go stand-in
// for vLLM's OpenAI server with runtime LoRA loading. The resolver is a Go
// function in the shape the Sky side passes (String -> Task Error
// TierAdapter); Std.Ai.Serve's own resolver over the registry's pointer
// objects is covered by tests/Std/Ai/ServeTest.sky and the cargo flow
// rust/crates/sky/tests/ai_serve_flow.rs.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	fakeVLLMOnce sync.Once
	fakeVLLMBin  string
	fakeVLLMErr  error
)

// startFakeVLLM builds (once) and starts testdata/fakevllm, returning its URL.
func startFakeVLLM(t *testing.T, args ...string) string {
	t.Helper()
	fakeVLLMOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sky-fakevllm-")
		if err != nil {
			fakeVLLMErr = err
			return
		}
		fakeVLLMBin = filepath.Join(dir, "fakevllm")
		if out, err := exec.Command("go", "build", "-o", fakeVLLMBin, "./testdata/fakevllm").CombinedOutput(); err != nil {
			fakeVLLMErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if fakeVLLMErr != nil {
		t.Fatalf("build fakevllm: %v", fakeVLLMErr)
	}
	portFile := filepath.Join(t.TempDir(), "port")
	cmd := exec.Command(fakeVLLMBin, append([]string{"-port-file", portFile, "-base", "qwen-base"}, args...)...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(portFile); err == nil && len(b) > 0 {
			return "http://" + string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fakevllm did not start")
	return ""
}

func vllmStats(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url + "/_fake/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

const tierBaseID = "Qwen/Qwen2.5-0.5B-Instruct@7ae557604adf67be50417f59c2c2f167def9a775"

// fakeRegistry is the Sky resolver's stand-in: aliases point at versions, and
// each version's PEFT files exist on disk.
type fakeRegistry struct {
	mu      sync.Mutex
	dir     string
	aliases map[string]int // "support@prod" -> version
	bases   map[int]string // version -> base id (default tierBaseID)
	calls   int32
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	return &fakeRegistry{dir: t.TempDir(), aliases: map[string]int{}, bases: map[int]string{}}
}

func (f *fakeRegistry) point(ref string, v int) {
	f.mu.Lock()
	f.aliases[ref] = v
	f.mu.Unlock()
}

func (f *fakeRegistry) fn() any {
	return func(refA any) any {
		return func() any {
			atomic.AddInt32(&f.calls, 1)
			ref := refA.(string)
			name, which, _ := strings.Cut(ref, "@")
			f.mu.Lock()
			defer f.mu.Unlock()
			v := 0
			if strings.HasPrefix(which, "v") {
				fmt.Sscanf(which, "v%d", &v)
			} else {
				v = f.aliases[ref]
			}
			if v == 0 {
				return Err[any, any](aiErrorValue(aiFailf(aiNotRegistered, "%s is not in the registry", ref).with("ref", ref)))
			}
			dir := filepath.Join(f.dir, fmt.Sprintf("%s-v%d", name, v))
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "w.safetensors"), []byte(fmt.Sprintf("weights %d", v)), 0o644)
			os.WriteFile(filepath.Join(dir, "c.json"), []byte(`{"r":8}`), 0o644)
			base := f.bases[v]
			if base == "" {
				base = tierBaseID
			}
			return Ok[any, any](map[string]any{"ref": ref, "label": fmt.Sprintf("%s@v%d", name, v),
				"servedName": fmt.Sprintf("%s-v%d", name, v), "baseId": base, "digest": fmt.Sprintf("%064d", v),
				"peft": filepath.Join(dir, "w.safetensors"), "config": filepath.Join(dir, "c.json")})
		}
	}
}

// startTier runs a tier on a loopback port against the engine at url.
func startTier(t *testing.T, engine string, reg *fakeRegistry, mutate func(*aiTierConfig)) (*aiTier, string) {
	t.Helper()
	cfg := aiTierConfig{engine: "vllm", bind: "127.0.0.1:0", token: "tier-token", engineURL: engine,
		baseID: tierBaseID, baseName: "qwen-base", adapters: []string{"support@prod"}, maxAdapters: 4,
		poll: time.Hour, modelsDir: t.TempDir(), resolve: reg.fn()}
	if mutate != nil {
		mutate(&cfg)
	}
	tier := newAiTier(cfg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go tier.serveOn(ln)
	t.Cleanup(tier.close)
	return tier, "http://" + ln.Addr().String()
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, _ := http.Get(url + "/health")
	b, _ := io.ReadAll(resp.Body)
	t.Fatalf("the tier never became ready: %s", b)
}

func tierChat(t *testing.T, url, token, model string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions",
		strings.NewReader(`{"model":`+jsonQuote(model)+`,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAiTierRoutesTheBaseAndAdaptersBehindItsToken(t *testing.T) {
	engine := startFakeVLLM(t)
	reg := newFakeRegistry(t)
	reg.point("support@prod", 1)
	_, url := startTier(t, engine, reg, nil)
	waitReady(t, url)

	if st, body := tierChat(t, url, "tier-token", ""); st != 200 || !strings.Contains(body, "echo:qwen-base") {
		t.Fatalf("no model is the base: %d %s", st, body)
	}
	if st, body := tierChat(t, url, "tier-token", "support-v1"); st != 200 || !strings.Contains(body, "echo:support-v1") {
		t.Fatalf("the alias's adapter: %d %s", st, body)
	}
	if st, body := tierChat(t, url, "wrong", "support-v1"); st != 401 || strings.Contains(body, "tier-token") {
		t.Fatalf("a wrong token is 401 and never echoed: %d %s", st, body)
	}
	if st, _ := tierChat(t, url, "tier-token", "gpt-4"); st != 404 {
		t.Fatalf("an unknown model is 404, got %d", st)
	}
	resp, _ := http.Get(url + "/v1/models")
	if resp.StatusCode != 401 {
		t.Fatalf("/v1/models needs the token too, got %d", resp.StatusCode)
	}
}

func TestAiTierLoadsAVersionOnDemandAndEvictsTheLeastRecentlyUsed(t *testing.T) {
	engine := startFakeVLLM(t)
	reg := newFakeRegistry(t)
	reg.point("support@prod", 1)
	_, url := startTier(t, engine, reg, func(c *aiTierConfig) { c.maxAdapters = 2 })
	waitReady(t, url)
	if st, body := tierChat(t, url, "tier-token", "support-v3"); st != 200 || !strings.Contains(body, "echo:support-v3") {
		t.Fatalf("a version that is not resident is loaded on demand: %d %s", st, body)
	}
	if st, _ := tierChat(t, url, "tier-token", "support-v4"); st != 200 {
		t.Fatalf("a second on-demand version evicts the least recently used unpinned one, got %d", st)
	}
	time.Sleep(100 * time.Millisecond)
	loaded := fmt.Sprint(vllmStats(t, engine)["loaded"])
	if !strings.Contains(loaded, "support-v1") || !strings.Contains(loaded, "support-v4") || strings.Contains(loaded, "support-v3") {
		t.Fatalf("the alias's v1 stays pinned and v3 is evicted: %s", loaded)
	}
}

// The hot-load: while 20 requests run on @prod, the alias moves to v2. The
// tier loads v2, keeps v1 loaded until its last request finishes, then
// unloads it. Zero requests fail, and none sees its adapter vanish.
// The harness gate ai-serve-hot-load runs this test and requires its
// `ASSERTIONS:` line. Its falsifying mutation lets drain unload a retired
// version while a request still uses it; the "v1 stays loaded" and "no
// request failed" assertions must go red.
func TestAiTierHotLoadsAPromotionWithoutDroppingARequest(t *testing.T) {
	n := 0
	engine := startFakeVLLM(t, "-delay-ms", "150")
	reg := newFakeRegistry(t)
	reg.point("support@prod", 1)
	tier, url := startTier(t, engine, reg, nil)
	waitReady(t, url)

	var wg sync.WaitGroup
	var failures int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i*10) * time.Millisecond)
			model := "support-v1"
			if i >= 10 {
				model = "support-v2"
			}
			if st, body := tierChat(t, url, "tier-token", model); st != 200 {
				atomic.AddInt32(&failures, 1)
				t.Logf("request %d (%s): %d %s", i, model, st, body)
			}
		}(i)
	}
	time.Sleep(30 * time.Millisecond)
	reg.point("support@prod", 2)
	if err := tier.refreshAliases(); err != nil {
		t.Fatal(err)
	}
	n++
	stats := vllmStats(t, engine)
	if !strings.Contains(fmt.Sprint(stats["loaded"]), "support-v1") {
		t.Fatalf("v1 is unloaded while its requests run: %v", stats["loaded"])
	}
	n++
	wg.Wait()
	tier.drain()
	time.Sleep(100 * time.Millisecond)
	stats = vllmStats(t, engine)
	if failures != 0 || stats["broken"].(float64) != 0 {
		t.Fatalf("%d requests failed, %v saw their adapter vanish", failures, stats["broken"])
	}
	n++
	if loaded := fmt.Sprint(stats["loaded"]); strings.Contains(loaded, "support-v1") || !strings.Contains(loaded, "support-v2") {
		t.Fatalf("after the drain only v2 is loaded: %s", loaded)
	}
	n++
	if st, _ := tierChat(t, url, "tier-token", "support-v2"); st != 200 {
		t.Fatalf("v2 serves")
	}
	n++
	fmt.Printf("ASSERTIONS: %d\n", n)
}

func TestAiTierHealthWaitsForTheEngineAndRefusesAnotherBase(t *testing.T) {
	engine := startFakeVLLM(t, "-health-after-ms", "400")
	reg := newFakeRegistry(t)
	reg.point("support@prod", 1)
	reg.bases[1] = "meta-llama/Llama-3.2-1B@abc"
	tier, url := startTier(t, engine, reg, nil)
	resp, _ := http.Get(url + "/health")
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("not ready while the engine starts, got %d", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !tier.isEngineUp() {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	resp, _ = http.Get(url + "/health")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(b), "AdapterMismatch") {
		t.Fatalf("an alias trained on another base keeps the tier unready, naming why: %d %s", resp.StatusCode, b)
	}
	reg.bases[1] = ""
	tier.refreshAliases()
	waitReady(t, url)
}

func TestAiTierStreamsAndReportsSignals(t *testing.T) {
	engine := startFakeVLLM(t)
	reg := newFakeRegistry(t)
	reg.point("support@prod", 1)
	tier, url := startTier(t, engine, reg, nil)
	waitReady(t, url)
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions",
		strings.NewReader(`{"model":"support-v1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer tier-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") || !strings.Contains(string(b), "data: [DONE]") {
		t.Fatalf("SSE passes through: %s %s", resp.Header.Get("Content-Type"), b)
	}
	tier.scrapeEngine()
	mresp, _ := http.Get(url + "/metrics")
	mb, _ := io.ReadAll(mresp.Body)
	mresp.Body.Close()
	for _, want := range []string{"sky_ai_engine_kv_cache 0.25", "sky_ai_tier_resident_adapters 1", `sky_ai_tier_requests_total{model="support-v1",outcome="ok"} 1`} {
		if !strings.Contains(string(mb), want) {
			t.Fatalf("/metrics has no %q:\n%s", want, mb)
		}
	}
	if st := aiTierStatuses(); len(st) == 0 || st[len(st)-1].Signals["kvCache"] != 0.25 {
		t.Fatalf("the console sees the tier: %+v", st)
	}
}

func TestAiTierRefusesToServeOpenWithoutAToken(t *testing.T) {
	t.Setenv("SKY_AI_SERVE_TOKEN", "")
	t.Setenv("SKY_AI_SERVE_BIND", "")
	raw := map[string]any{"engine": "vllm", "bind": "0.0.0.0:0", "baseId": tierBaseID, "baseName": "b",
		"adapters": []any{}, "maxAdapters": -1, "pollS": -1, "modelsDir": t.TempDir(), "engineUrl": "", "hasToken": false}
	res := AnyTaskRun(Ai_serveRun(raw))
	tag, _, errV := anyResultView(res)
	if tag == 0 {
		t.Fatal("a tier on all interfaces without a token must not start")
	}
	if df, ok := DomainOf(errV); !ok || df.Code != string(aiNotConfigured) {
		t.Fatalf("NotConfigured, got %+v", df)
	}
}

// The Cloud Run region is its own setting: the target's region in code, and
// SKY_AI_SERVE_REGION over it. The training region (SKY_AI_TRAIN_REGION) does
// not move it, so a tier can serve in another region than the one it trained
// in (the live l5 stage serves in europe-west4 and trains in us-central1).
func TestAiServeTargetRegionIsItsOwnSetting(t *testing.T) {
	region := func() (string, string) {
		r := force(t, Ai_serveTarget(map[string]any{"project": "p", "region": "us-central1", "image": "i@sha256:01"}))
		list, ok := r.([]any)
		if !ok {
			t.Fatalf("serveTarget: %#v", r)
		}
		for _, s := range list {
			m := s.(map[string]any)
			if m["key"] == "REGION" {
				return m["value"].(string), m["source"].(string)
			}
		}
		t.Fatal("no REGION setting")
		return "", ""
	}
	t.Setenv("SKY_AI_SERVE_REGION", "")
	t.Setenv("SKY_AI_TRAIN_REGION", "asia-east1")
	if v, src := region(); v != "us-central1" || src != "code" {
		t.Fatalf("code region with a training region set: %s (%s)", v, src)
	}
	t.Setenv("SKY_AI_SERVE_REGION", "europe-west4")
	if v, src := region(); v != "europe-west4" || src != "env" {
		t.Fatalf("env over code: %s (%s)", v, src)
	}
}
