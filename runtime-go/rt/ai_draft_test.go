//go:build !js

package rt

// Offline tests for a local engine's draft model and named adapters
// (ai_draft.go): the settings and their variables, the launch arguments, the
// vocabulary check, the per-call adapter choice, and the answer's `timings`
// kept as the engine wrote them. The real llama-server runs them in
// rust/crates/sky/tests/ai_local_live.rs.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tinyFiles writes the synthetic Qwen2 model, its draft, two adapters and a
// model with another vocabulary, and returns their references by name.
func tinyFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	model, modelSum, adapter, adapterSum, err := AiTinyQwen(dir)
	if err != nil {
		t.Fatal(err)
	}
	extras, err := AiTinyQwenExtras(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(p, sum string) string { return "file:" + p + "#sha256=" + sum }
	out := map[string]string{"model": ref(model, modelSum), "adapter": ref(adapter, adapterSum)}
	for k, f := range extras {
		out[k] = ref(f[0], f[1])
	}
	return out
}

func TestAiDraftAndAdaptersResolveFromCodeAndEnv(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	files := tinyFiles(t)
	b := endpointBuilder("chat")
	b.hasLocal = true
	b.local = localBuilder(model)
	b.local.draft = model
	b.local.draftMax, b.local.draftMin = 6, 1
	b.local.adapters = "support=" + files["adapter"] + ",legal:0.5=" + files["adapterB"]
	c, err := resolveAiSlot(b)
	if err != nil {
		t.Fatal(err)
	}
	s := c.local
	if s.draft.String() != model || s.draftMax != 6 || s.draftMin != 1 {
		t.Fatalf("draft from code: %+v", s)
	}
	if len(s.adapters) != 2 || s.adapters[0].name != "support" || s.adapters[0].scale != 1 ||
		s.adapters[1].name != "legal" || s.adapters[1].scale != 0.5 {
		t.Fatalf("adapters from code: %+v", s.adapters)
	}

	// The operator's variables win over the code, through the same layers.
	t.Setenv("SKY_AI_CHAT_DRAFT", files["draft"])
	t.Setenv("SKY_AI_CHAT_DRAFT_MAX", "9")
	t.Setenv("SKY_AI_CHAT_ADAPTERS", "only:2="+files["adapterB"])
	c, err = resolveAiSlot(b)
	if err != nil {
		t.Fatal(err)
	}
	s = c.local
	if s.draft.String() != files["draft"] || s.draftMax != 9 || s.draftMin != 1 {
		t.Fatalf("draft from env: %+v", s)
	}
	if len(s.adapters) != 1 || s.adapters[0].name != "only" || s.adapters[0].scale != 2 {
		t.Fatalf("adapters from env: %+v", s.adapters)
	}
	for _, st := range c.settings {
		if st.key == "DRAFT" && st.source != "env" {
			t.Fatalf("DRAFT source %q", st.source)
		}
	}

	// The adapter list's own rules.
	for _, bad := range []string{
		"local=" + files["adapter"],
		"a=" + files["adapter"] + ",a=" + files["adapterB"],
		"a:0=" + files["adapter"],
		"a:x=" + files["adapter"],
		"a=file:/x.gguf",
		"a b=" + files["adapter"],
	} {
		if _, err := aiParseNamedAdapters(bad, "Local.withAdapters"); failCode(err) != aiInvalidSetting {
			t.Errorf("%q: got %v; want InvalidSetting", bad, err)
		}
	}

	// A single applied adapter and named adapters cannot share an engine.
	both := b
	both.local.adapter = files["adapter"]
	t.Setenv("SKY_AI_CHAT_ADAPTERS", "")
	if _, err := resolveAiSlot(both); failCode(err) != aiInvalidSetting {
		t.Fatalf("withAdapter and withAdapters: got %v", err)
	}
}

func TestAiDraftAndAdapterLaunchArgs(t *testing.T) {
	spec := aiLocalSpec{draftMax: 8, draftMin: 2}
	got := strings.Join(aiDraftAndAdapterArgs(spec, "/m/draft.gguf", []string{"/a/one.gguf", "/a/two.gguf"}), " ")
	want := "-md /m/draft.gguf --spec-type draft-simple --spec-draft-n-max 8 --spec-draft-n-min 2 " +
		"--lora /a/one.gguf,/a/two.gguf --lora-init-without-apply"
	if got != want {
		t.Fatalf("args:\n got %s\nwant %s", got, want)
	}
	if a := aiDraftAndAdapterArgs(aiLocalSpec{}, "", nil); len(a) != 0 {
		t.Fatalf("nothing set: %v", a)
	}
	m, _ := aiParseModelRef("file:/m.gguf#sha256=" + strings.Repeat("a", 64))
	d, _ := aiParseModelRef("file:/d.gguf#sha256=" + strings.Repeat("b", 64))
	base := aiLocalSpec{model: m, gpu: "auto", parallel: 2}
	withDraft := base
	withDraft.draft = d
	withAd := base
	withAd.adapters = []aiNamedAdapter{{name: "a", ref: d, scale: 1}}
	if aiEngineKey(base) == aiEngineKey(withDraft) || aiEngineKey(base) == aiEngineKey(withAd) {
		t.Fatal("a draft or named adapters must make another engine")
	}
}

func TestAiDraftVocabularyMustMatchTheBase(t *testing.T) {
	aiTestEnv(t, nil)
	files := tinyFiles(t)
	parse := func(s string) aiModelRef {
		m, err := aiParseModelRef(s)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	base, draft, other := parse(files["model"]), parse(files["draft"]), parse(files["otherVocab"])
	if err := aiCheckDraftVocab("chat", base, base.file, draft, draft.file); err != nil {
		t.Fatalf("same vocabulary: %v", err)
	}
	err := aiCheckDraftVocab("chat", base, base.file, other, other.file)
	mustInvalid(t, err, "SKY_AI_CHAT_DRAFT (or Local.withDraft in code)", "")
	if !strings.Contains(err.Error(), "vocabular") {
		t.Fatalf("the message does not say why: %v", err)
	}

	// The engine start runs the same check, through the model store's
	// SHA-256 checks: a draft whose file does not hash to its pin is
	// ChecksumMismatch, and nothing is loaded.
	spec := aiLocalSpec{model: base, draft: other}
	if _, _, _, err := aiEnsureDraftAndAdapters(context.Background(), "", spec, base.file); failCode(err) != aiInvalidSetting {
		t.Fatalf("start with another vocabulary: %v", err)
	}
	wrong := draft
	wrong.sha256 = strings.Repeat("0", 64)
	spec.draft = wrong
	if _, _, _, err := aiEnsureDraftAndAdapters(context.Background(), "", spec, base.file); failCode(err) != aiChecksumMismatch {
		t.Fatalf("draft pin: got %v; want ChecksumMismatch", err)
	}
	ad := parse(files["adapter"])
	ad.sha256 = strings.Repeat("1", 64)
	spec = aiLocalSpec{model: base, adapters: []aiNamedAdapter{{name: "a", ref: ad, scale: 1}}}
	if _, _, _, err := aiEnsureDraftAndAdapters(context.Background(), "", spec, base.file); failCode(err) != aiChecksumMismatch {
		t.Fatalf("adapter pin: got %v; want ChecksumMismatch", err)
	}
	// A Hugging Face draft with fetching off is NotProvisioned, like a base.
	t.Setenv("SKY_AI_FETCH", "off")
	hf := parse("hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/q.gguf#sha256=" + strings.Repeat("c", 64))
	spec = aiLocalSpec{model: base, draft: hf}
	if _, _, _, err := aiEnsureDraftAndAdapters(context.Background(), "", spec, base.file); failCode(err) != aiNotProvisioned {
		t.Fatalf("draft not provisioned: got %v", err)
	}
}

func TestAiDraftRefusedInTheEngineLog(t *testing.T) {
	log := "0.00.048 I loading draft model\n0.00.050 E srv    load_model: failed to initialize speculative decoding context: draft model vocab type must match target model to use speculation\n0.00.053 I listening"
	if line := aiDraftRefusedInLog(log); !strings.Contains(line, "speculative decoding") {
		t.Fatalf("got %q", line)
	}
	if line := aiDraftRefusedInLog("0.00.050 I srv listening on http://127.0.0.1:1"); line != "" {
		t.Fatalf("a clean log: %q", line)
	}
}

func TestAiAdapterRouting(t *testing.T) {
	m, _ := aiParseModelRef("file:/m.gguf#sha256=" + strings.Repeat("a", 64))
	ref, _ := aiParseModelRef("file:/a.gguf#sha256=" + strings.Repeat("b", 64))
	spec := aiLocalSpec{model: m, adapters: []aiNamedAdapter{
		{name: "support", ref: ref, scale: 1}, {name: "legal", ref: ref, scale: 0.5},
	}}
	lora := func(body string) string {
		var v struct {
			Lora []aiLoraEntry `json:"lora"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, l := range v.Lora {
			b, _ := json.Marshal(l)
			parts = append(parts, string(b))
		}
		return strings.Join(parts, ",")
	}
	for _, c := range []struct{ model, want string }{
		{"legal", `{"id":0,"scale":0},{"id":1,"scale":0.5}`},
		{"support", `{"id":0,"scale":1},{"id":1,"scale":0}`},
		{"local", `{"id":0,"scale":0},{"id":1,"scale":0}`},
		{"", `{"id":0,"scale":0},{"id":1,"scale":0}`},
	} {
		out, err := aiRouteAdapter(spec, `{"model":`+jsonQuote(c.model)+`,"messages":[]}`)
		if err != nil {
			t.Fatalf("%q: %v", c.model, err)
		}
		if got := lora(out); got != c.want {
			t.Fatalf("%q: lora %s; want %s", c.model, got, c.want)
		}
	}
	// A name that is not loaded never answers from the base model.
	_, err := aiRouteAdapter(spec, `{"model":"billing","messages":[]}`)
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiNotRegistered || !strings.Contains(f.msg, "support, legal") {
		t.Fatalf("unknown adapter: %v; want NotRegistered listing the loaded names", err)
	}
	if v, _ := f.attrValue("ref"); v != "billing" {
		t.Fatalf("ref %q", v)
	}
	// The adapter is chosen by name, never by a hand-written lora list.
	if _, err := aiRouteAdapter(spec, `{"model":"legal","lora":[{"id":0,"scale":1}]}`); failCode(err) != aiInvalidSetting {
		t.Fatalf("own lora list: %v", err)
	}
	// An engine without named adapters sends the body as it is.
	body := `{"model":"anything","messages":[]}`
	if out, err := aiRouteAdapter(aiLocalSpec{model: m}, body); err != nil || out != body {
		t.Fatalf("no adapters: %q %v", out, err)
	}
}

// TestAiEngineWithDraftAndAdaptersOnTheFakeEngine runs the whole path on the
// fake engine: the launch carries the draft and adapter flags, a call names
// an adapter and the engine gets the lora list, an unknown name fails before
// the engine sees it, the answer keeps `timings` (draft fields included), and
// a draft the engine refuses in its log fails the start.
func TestAiEngineWithDraftAndAdaptersOnTheFakeEngine(t *testing.T) {
	engineDir, model := aiTestEnv(t, map[string]any{"timings": true})
	dir := t.TempDir()
	ad := filepath.Join(dir, "a.gguf")
	os.WriteFile(ad, []byte("not a real adapter"), 0o644)
	adSum, _ := aiFileSha256(ad)
	b := endpointBuilder("chat")
	b.hasLocal = true
	b.local = localBuilder(model)
	b.local.draft = model
	b.local.draftMax = 4
	b.local.adapters = "support=file:" + ad + "#sha256=" + adSum
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.post("/chat/completions", `{"model":"support","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := chatContent(t, out); got != "fake[support]: hi lora=0:1" {
		t.Fatalf("content %q", got)
	}
	var ans struct {
		Timings map[string]float64 `json:"timings"`
	}
	_ = json.Unmarshal([]byte(out), &ans)
	if ans.Timings["predicted_per_second"] == 0 || ans.Timings["draft_n"] != 8 || ans.Timings["draft_n_accepted"] != 6 {
		t.Fatalf("timings were not kept: %s", out)
	}
	launches, _ := os.ReadFile(filepath.Join(engineDir, "launches.log"))
	for _, flag := range []string{"-md ", "--spec-type draft-simple", "--spec-draft-n-max 4", "--lora " + ad, "--lora-init-without-apply"} {
		if !strings.Contains(string(launches), flag) {
			t.Fatalf("launch has no %q: %s", flag, launches)
		}
	}
	if _, err := s.post("/chat/completions", `{"model":"billing","messages":[{"role":"user","content":"hi"}]}`); failCode(err) != aiNotRegistered {
		t.Fatalf("unknown adapter: %v", err)
	}
	out, err = s.post("/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if err != nil || chatContent(t, out) != "fake[local]: hi lora=0:0" {
		t.Fatalf("base model: %v %s", err, out)
	}

	// A stream routes its adapter the same way: never every adapter at its
	// launch scale, never the base model for an unknown name.
	id, err := s.openStream("/chat/completions", `{"model":"support","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, body := streamAll(t, id); bodyContent(body) != "fake[support]: hi lora=0:1" {
		t.Fatalf("stream content %q", bodyContent(body))
	}
	if _, err := s.openStream("/chat/completions", `{"model":"billing","messages":[{"role":"user","content":"hi"}]}`); failCode(err) != aiNotRegistered {
		t.Fatalf("unknown adapter on a stream: %v", err)
	}

	// A measured call reports the engine's own rate and the draft acceptance.
	f := newAiCallFacts(s.cfg.slot, s.cfg.mode)
	if _, err := s.callWith("/chat/completions", `{"model":"support","messages":[{"role":"user","content":"hi"}]}`, f); err != nil {
		t.Fatal(err)
	}
	var facts factsJSON
	_ = json.Unmarshal([]byte(f.json()), &facts)
	if facts.Tier == nil || *facts.Tier != "local" || facts.Adapter == nil || *facts.Adapter != "support" ||
		facts.DraftAcceptance == nil || *facts.DraftAcceptance != 0.75 || facts.TokensPerSecond == nil {
		t.Fatalf("facts of a draft engine call: %s", f.json())
	}

	// The engine loaded the draft and could not use it: the start fails.
	aiResetSlots()
	aiResetEngines()
	refusing := fakeEngineDir(t, map[string]any{"refuseDraft": true})
	t.Setenv("SKY_AI_LLAMA_BIN", refusing)
	b2 := b
	b2.local.draftMax = 5 // another engine
	s2, err := aiSlotFor(b2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s2.post("/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	mustInvalid(t, err, "SKY_AI_CHAT_DRAFT (or Local.withDraft in code)", "")
}
