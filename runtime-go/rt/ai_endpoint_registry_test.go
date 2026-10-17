//go:build !js

package rt

// A slot that serves a registry adapter (`Endpoint.fromRegistry` with
// `registry:<name>@<alias>`): it resolves the alias when it starts, sends the
// version's served name to a remote server, loads the version's GGUF file into
// a local engine after checking its base model, and follows the alias as it
// moves. The resolver here is a Go function in the shape the Sky side passes
// (`String -> Bool -> Task Error Adapter`); the Sky resolver itself
// (Std.Ai.Registry.resolver) is covered by tests/Std/Ai/RegistryTest.sky and
// rust/crates/sky/tests/ai_registry_flow.rs.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeResolver answers with the adapter `current` points at, and counts its
// calls.
type fakeResolver struct {
	mu      sync.Mutex
	current map[string]any
	fail    any
	calls   int32
	refs    []string
	locals  []bool
}

func (f *fakeResolver) fn() any {
	return func(refA, localA any) any {
		return func() any {
			atomic.AddInt32(&f.calls, 1)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.refs = append(f.refs, refA.(string))
			f.locals = append(f.locals, localA.(bool))
			if f.fail != nil {
				return Err[any, any](f.fail)
			}
			cp := map[string]any{}
			for k, v := range f.current {
				cp[k] = v
			}
			return Ok[any, any](cp)
		}
	}
}

func (f *fakeResolver) set(m map[string]any) {
	f.mu.Lock()
	f.current = m
	f.mu.Unlock()
}

func adapterRecord(label, served, digest, base, path string) map[string]any {
	return map[string]any{
		"ref": "support-lora@prod", "label": label, "servedName": served,
		"digest": digest, "baseDigest": base, "format": "gguf", "path": path,
	}
}

func registryBuilder(slot string, r *fakeResolver) aiEndpointBuilder {
	b := endpointBuilder(slot)
	b.adapter = "registry:support-lora@prod"
	b.hasResolver, b.resolverName, b.resolve, b.adapterRefreshS = true, "registry", r.fn(), -1
	return b
}

func TestAiRegistryAdapterNeedsAResolver(t *testing.T) {
	aiTestEnv(t, nil)
	b := endpointBuilder("chat")
	b.url, b.adapter = "http://127.0.0.1:1/v1", "registry:support-lora@prod"
	if _, err := aiSlotFor(b); failCode(err) != aiNotConfigured || !strings.Contains(err.Error(), "Endpoint.fromRegistry") {
		t.Fatalf("a registry adapter without a resolver: %v", err)
	}
	r := &fakeResolver{}
	b = registryBuilder("chat", r)
	b.url, b.adapter = "http://127.0.0.1:1/v1", "registry:support-lora"
	if _, err := aiSlotFor(b); failCode(err) != aiInvalidSetting {
		t.Fatalf("a reference without an alias: %v", err)
	}
}

func TestAiRegistryAdapterRemoteSendsTheServedNameAndFollowsTheAlias(t *testing.T) {
	aiTestEnv(t, nil)
	var models []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var m struct{ Model string }
		json.Unmarshal(body, &m)
		mu.Lock()
		models = append(models, m.Model)
		mu.Unlock()
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	r := &fakeResolver{}
	r.set(adapterRecord("support-lora@v1", "support-lora-v1", sha([]byte("v1")), sha([]byte("base")), ""))
	b := registryBuilder("chat", r)
	b.url = srv.URL + "/v1"
	b.adapterRefreshS = 30
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.post("/chat/completions", chatBody("hi")); err != nil {
		t.Fatal(err)
	}
	// The alias moves; within the refresh interval the slot keeps v1.
	r.set(adapterRecord("support-lora@v2", "support-lora-v2", sha([]byte("v2")), sha([]byte("base")), ""))
	if _, err := s.post("/chat/completions", chatBody("hi")); err != nil {
		t.Fatal(err)
	}
	// After the interval the next call re-resolves and serves v2.
	s.adMu.Lock()
	s.adAt = time.Now().Add(-time.Minute)
	s.adMu.Unlock()
	if _, err := s.post("/chat/completions", chatBody("hi")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := strings.Join(models, ",")
	mu.Unlock()
	if got != "support-lora-v1,support-lora-v1,support-lora-v2" {
		t.Fatalf("models sent %q", got)
	}
	if r.refs[0] != "support-lora@prod" || r.locals[0] {
		t.Fatalf("the resolver got %v %v", r.refs, r.locals)
	}
	// describe names the resolved version, never more than a digest prefix.
	d := s.description().(map[string]any)
	found := false
	for _, st := range d["settings"].([]any) {
		m := st.(map[string]any)
		if m["key"] == "ADAPTER" {
			found = strings.Contains(m["value"].(string), "-> support-lora@v2 (sha256:")
		}
	}
	if !found {
		t.Fatalf("describe does not name the resolved version: %v", d["settings"])
	}

	// A refresh that fails keeps serving v2 and says so in describe.
	r.mu.Lock()
	r.fail = ErrUnavailable("the database is down")
	r.mu.Unlock()
	s.adMu.Lock()
	s.adAt = time.Now().Add(-time.Minute)
	s.adMu.Unlock()
	if _, err := s.post("/chat/completions", chatBody("hi")); err != nil {
		t.Fatalf("a failed refresh failed the call: %v", err)
	}
	mu.Lock()
	last := models[len(models)-1]
	mu.Unlock()
	if last != "support-lora-v2" {
		t.Fatalf("after a failed refresh the slot sent %q", last)
	}
	d = s.description().(map[string]any)
	for _, st := range d["settings"].([]any) {
		m := st.(map[string]any)
		if m["key"] == "ADAPTER" && !strings.Contains(m["value"].(string), "refresh failed") {
			t.Fatalf("describe hides the failed refresh: %v", m["value"])
		}
	}
}

func TestAiRegistryAdapterFirstResolutionFailureFailsTheCall(t *testing.T) {
	aiTestEnv(t, nil)
	r := &fakeResolver{fail: ErrUnavailable("Std.Ai NotRegistered: support-lora@prod is not in the registry")}
	b := registryBuilder("chat", r)
	b.url = "http://127.0.0.1:1/v1"
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.post("/chat/completions", chatBody("hi"))
	se, ok := err.(*aiSkyError)
	if !ok || !strings.Contains(skyErrorMessage(se.v), "NotRegistered") {
		t.Fatalf("the resolver's own error must reach the caller unchanged: %v", err)
	}
	if v := aiErrorValue(err); skyErrorMessage(v) != skyErrorMessage(se.v) {
		t.Fatalf("aiErrorValue rewrote the resolver's error: %v", v)
	}
}

func writeLora(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	data := ggufBytes([]ggufKV{
		{"general.architecture", "qwen2"},
		{"general.type", "adapter"},
		{"adapter.type", "lora"},
		{"general.name", name},
	})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, sha(data)
}

func TestAiRegistryAdapterLocalLoadsTheLoraAndSwitchesEngines(t *testing.T) {
	dir, model := aiTestEnv(t, nil)
	base := model[strings.LastIndex(model, "=")+1:]
	p1, d1 := writeLora(t, t.TempDir(), "v1.gguf")
	p2, d2 := writeLora(t, t.TempDir(), "v2.gguf")

	r := &fakeResolver{}
	r.set(adapterRecord("support-lora@v1", "support-lora-v1", d1, base, p1))
	b := registryBuilder("chat", r)
	b.hasLocal, b.local = true, localBuilder(model)
	b.adapterRefreshS = 30
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.post("/chat/completions", chatBody("hello")); err != nil {
		t.Fatal(err)
	}
	if !r.locals[0] {
		t.Fatal("a local slot must ask the resolver for the adapter's file")
	}
	first := s.engine
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	if !strings.Contains(string(launch), "--lora "+p1) {
		t.Fatalf("the engine was not started with the adapter: %s", launch)
	}

	// The alias moves to v2: the next due call starts an engine with v2 and
	// retires the v1 engine.
	r.set(adapterRecord("support-lora@v2", "support-lora-v2", d2, base, p2))
	s.adMu.Lock()
	s.adAt = time.Now().Add(-time.Minute)
	s.adMu.Unlock()
	if _, err := s.post("/chat/completions", chatBody("hello")); err != nil {
		t.Fatal(err)
	}
	if s.engine == first {
		t.Fatal("the slot kept the v1 engine after the alias moved")
	}
	launch, _ = os.ReadFile(filepath.Join(dir, "launches.log"))
	if !strings.Contains(string(launch), "--lora "+p2) {
		t.Fatalf("no engine was started with v2: %s", launch)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		first.mu.Lock()
		st := first.state
		first.mu.Unlock()
		if st == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the v1 engine was not retired (state %s)", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAiRegistryAdapterOnAnotherBaseIsAdapterMismatch(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	p1, d1 := writeLora(t, t.TempDir(), "v1.gguf")
	r := &fakeResolver{}
	r.set(adapterRecord("support-lora@v1", "support-lora-v1", d1, sha([]byte("another base")), p1))
	b := registryBuilder("chat", r)
	b.hasLocal, b.local = true, localBuilder(model)
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.post("/chat/completions", chatBody("hello"))
	if failCode(err) != aiAdapterMismatch {
		t.Fatalf("an adapter for another base: %v", err)
	}
	df, _ := DomainOf(aiErrorValue(err))
	if a, _ := df.Attr("adapterBase"); a != sha([]byte("another base")) {
		t.Fatalf("attributes %v", df.Attrs)
	}
	if s.engine != nil {
		t.Fatal("an engine was created for a mismatched adapter")
	}
}

func TestAiRegistryAdapterFileIsHashedBeforeEachLoad(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	base := model[strings.LastIndex(model, "=")+1:]
	p1, d1 := writeLora(t, t.TempDir(), "v1.gguf")
	r := &fakeResolver{}
	r.set(adapterRecord("support-lora@v1", "support-lora-v1", d1, base, p1))
	b := registryBuilder("chat", r)
	b.hasLocal, b.local = true, localBuilder(model)
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p1, []byte("swapped after the registry checked it"), 0o644)
	if _, err := s.post("/chat/completions", chatBody("hello")); failCode(err) != aiChecksumMismatch {
		t.Fatalf("a swapped adapter file was loaded: %v", err)
	}
}

// refResolver answers per reference: a known `<name>@<alias>` gets its
// adapter, any other the typed NotRegistered Std.Ai.Registry gives.
func refResolver(byRef map[string]map[string]any, refs *[]string) any {
	var mu sync.Mutex
	return func(refA, localA any) any {
		return func() any {
			ref := refA.(string)
			mu.Lock()
			*refs = append(*refs, ref)
			mu.Unlock()
			if m, ok := byRef[ref]; ok {
				return Ok[any, any](m)
			}
			return Err[any, any](aiErrorValue(aiFailf(aiNotRegistered, "%s is not in the registry", ref).with("ref", ref)))
		}
	}
}

// Local.withAdapters takes `registry:<name>@<alias>`: the slot resolves it
// through its registry (local, so with the version's file), loads that file
// with `--lora` after the same SHA-256 check as a pinned file, routes a call
// by the adapter's own name, and refuses a slot without a registry. An
// unknown name or alias is the registry's typed NotRegistered.
func TestAiNamedRegistryAdapterResolvesThroughTheRegistry(t *testing.T) {
	engineDir, model := aiTestEnv(t, nil)
	base := model[strings.LastIndex(model, "=")+1:]
	p1, d1 := writeLora(t, t.TempDir(), "support-v3.gguf")
	p2, d2 := writeLora(t, t.TempDir(), "legal.gguf")

	var refs []string
	byRef := map[string]map[string]any{
		"support-lora@prod": {"ref": "support-lora@prod", "label": "support-lora@v3", "servedName": "support-lora-v3",
			"digest": d1, "baseDigest": base, "format": "gguf", "path": p1},
	}
	b := endpointBuilder("chat")
	b.hasLocal, b.local = true, localBuilder(model)
	b.local.adapters = "support=registry:support-lora@prod,legal:0.5=file:" + p2 + "#sha256=" + d2

	// Without a registry the slot does not start.
	if _, err := aiSlotFor(b); failCode(err) != aiNotConfigured {
		t.Fatalf("a registry named adapter without a registry: %v", err)
	}

	b.hasResolver, b.resolverName, b.resolve, b.adapterRefreshS = true, "registry", refResolver(byRef, &refs), -1
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.post("/chat/completions", `{"model":"support","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := chatContent(t, out); got != "fake[support]: hi lora=0:1,1:0" {
		t.Fatalf("content %q", got)
	}
	if len(refs) != 1 || refs[0] != "support-lora@prod" {
		t.Fatalf("the resolver was asked for %v", refs)
	}
	launches, _ := os.ReadFile(filepath.Join(engineDir, "launches.log"))
	if !strings.Contains(string(launches), p1) || !strings.Contains(string(launches), p2) {
		t.Fatalf("the engine did not load both adapters: %s", launches)
	}
	if d, _ := s.description().(map[string]any); d == nil {
		t.Fatal("no description")
	}

	// An alias the registry does not know: the typed NotRegistered.
	b2 := b
	b2.slot = "chat2"
	b2.local.adapters = "support=registry:support-lora@staging"
	s2, err := aiSlotFor(b2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s2.post("/chat/completions", `{"model":"support","messages":[{"role":"user","content":"hi"}]}`)
	df, ok := DomainOf(aiErrorValue(err))
	if !ok || df.Code != string(aiNotRegistered) {
		t.Fatalf("an unknown alias: %v", err)
	}
	if r, _ := df.Attr("ref"); r != "support-lora@staging" {
		t.Fatalf("attributes %v", df.Attrs)
	}
	if s2.engine != nil {
		t.Fatal("an engine was created for an unresolved adapter")
	}

	// A version trained on another base: AdapterMismatch, no engine.
	byRef["other@prod"] = map[string]any{"ref": "other@prod", "label": "other@v1", "servedName": "other-v1",
		"digest": d1, "baseDigest": sha([]byte("another base")), "format": "gguf", "path": p1}
	b3 := b
	b3.slot = "chat3"
	b3.local.adapters = "other=registry:other@prod"
	s3, err := aiSlotFor(b3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s3.post("/chat/completions", `{"model":"other","messages":[{"role":"user","content":"hi"}]}`); failCode(err) != aiAdapterMismatch {
		t.Fatalf("an adapter for another base: %v", err)
	}

	// A registry file that does not hash to the version's digest is refused
	// by the load's SHA-256 check.
	byRef["tampered@prod"] = map[string]any{"ref": "tampered@prod", "label": "tampered@v1", "servedName": "tampered-v1",
		"digest": d2, "baseDigest": base, "format": "gguf", "path": p1}
	b4 := b
	b4.slot = "chat4"
	b4.local.adapters = "tampered=registry:tampered@prod"
	s4, err := aiSlotFor(b4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s4.post("/chat/completions", `{"model":"tampered","messages":[{"role":"user","content":"hi"}]}`); err == nil {
		t.Fatal("a registry file with another digest was loaded")
	}
}
