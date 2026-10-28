//go:build !js

package rt

import (
	"strings"
	"testing"
)

func TestAiStartupLinesNameEachSlotWithoutSecrets(t *testing.T) {
	env := map[string]string{
		"SKY_AI_CHAT_URL":     "https://user:pass@gpu.internal:8443/v1?token=abc",
		"SKY_AI_CHAT_KEY":     "sk-very-secret",
		"SKY_AI_CHAT_MODEL":   "base-8b",
		"SKY_AI_CHAT_ADAPTER": "registry:support-lora@prod",
		"SKY_AI_EMBED_MODE":   "local",
		"SKY_AI_EMBED_MODEL":  "hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_0.gguf#sha256=7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed",
		"SKY_AI_BAD_MODE":     "local",
		"SKY_AI_BAD_URL":      "https://x.example/v1",
	}
	lookup := func(name string) (string, bool) { v, ok := env[name]; return v, ok }
	lines := aiStartupLinesFor([]string{"bad", "chat", "embed", "judge"}, nil, lookup)
	if len(lines) != 4 {
		t.Fatalf("one line per slot: %q", lines)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		"ai chat      remote  https://gpu.internal:8443  (url: env, key: env, model: env, adapter: env)",
		"ai embed     local   qwen2.5-0.5b-instruct-q4_0.gguf sha256:7671c0c304e6  (mode: env, model: env)",
		"ai judge     set in code, resolved at the first call",
		"SKY_AI_BAD_MODE=local and SKY_AI_BAD_URL are both set",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	for _, leak := range []string{"sk-very-secret", "pass@", "token=abc", "/v1", "listening"} {
		if strings.Contains(all, leak) {
			t.Errorf("the AI lines show %q:\n%s", leak, all)
		}
	}
}

func TestAiStartupLinesShowAResolvedSlot(t *testing.T) {
	c := &aiSlotConfig{slot: "chat", mode: "remote", baseURL: "https://gpu.internal:8443/v1",
		settings: []aiSetting{{key: "URL", source: "env"}, {key: "TIMEOUT_MS", source: "code"}}}
	lines := aiStartupLinesFor([]string{"chat"}, map[string]*aiSlotConfig{"chat": c},
		func(string) (string, bool) { return "", false })
	if len(lines) != 1 || !strings.Contains(lines[0], "remote  https://gpu.internal:8443  (url: env)") {
		t.Fatalf("%q", lines)
	}
}

func TestAiDeclareSlotsFeedsTheReport(t *testing.T) {
	AiDeclareSlots([]string{"judge", "chat"})
	t.Cleanup(func() { AiDeclareSlots(nil) })
	if got := strings.Join(aiDeclared(), ","); got != "chat,judge" {
		t.Fatalf("declared %q", got)
	}
	t.Setenv("SKY_AI_CHAT_URL", "")
	t.Setenv("SKY_AI_JUDGE_URL", "")
	if n := len(aiStartupLines()); n != 2 {
		t.Fatalf("the report has %d AI lines for two slots", n)
	}
}
