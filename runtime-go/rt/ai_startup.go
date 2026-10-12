//go:build !js

package rt

// ai_startup.go — the startup report's AI lines: one per Std.Ai slot the app
// declares (docs/design/std-ai-lifecycle.md §5.5).
//
// The build lists the slots the source names (`Endpoint.endpoint "chat"`,
// project/src/ai_slots.rs) and the generated `ai_slots_gen.go` hands them to
// AiDeclareSlots. At startup no slot has resolved yet (a slot resolves on its
// first call, from env and code), so a line says what the operator's
// environment already decides: the mode, the target (scheme, host and port
// only) and which settings come from the environment. A slot the environment
// does not touch says that its code decides at the first call. A slot that
// has already resolved (a program that warmed it) is shown as resolved.
//
// No line contains "listening" (TestNoAddedStartupLineLooksLikeAListeningLine)
// and no line holds a secret or a URL path.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	aiDeclaredMu    sync.Mutex
	aiDeclaredSlots []string
)

// AiDeclareSlots records the slots the build found in the source.
func AiDeclareSlots(slots []string) {
	aiDeclaredMu.Lock()
	defer aiDeclaredMu.Unlock()
	aiDeclaredSlots = append([]string(nil), slots...)
	sort.Strings(aiDeclaredSlots)
}

func aiDeclared() []string {
	aiDeclaredMu.Lock()
	defer aiDeclaredMu.Unlock()
	return append([]string(nil), aiDeclaredSlots...)
}

// aiStartupLines is the AI block of the startup report.
func aiStartupLines() []string {
	resolved := map[string]*aiSlotConfig{}
	aiSlotsMu.Lock()
	for _, s := range aiSlots {
		resolved[s.cfg.slot] = s.cfg
	}
	aiSlotsMu.Unlock()
	return aiStartupLinesFor(aiDeclared(), resolved, func(name string) (string, bool) {
		v, ok := lookupEnvRaw(name)
		if !ok || v == "" || isSeededDefault(name) || isConfigApplied(name) {
			return "", false
		}
		return v, true
	})
}

// aiStartupLinesFor is aiStartupLines with its inputs given: the declared
// slots, the slots already resolved, and the operator's environment.
func aiStartupLinesFor(slots []string, resolved map[string]*aiSlotConfig, env func(string) (string, bool)) []string {
	var out []string
	for _, slot := range slots {
		label := "ai " + slot
		if c, ok := resolved[slot]; ok {
			var fromEnv []string
			for _, st := range c.settings {
				if st.source == "env" {
					fromEnv = append(fromEnv, strings.ToLower(st.key))
				}
			}
			out = append(out, fmt.Sprintf("  %-11s  %-6s  %s%s", label, c.mode, c.target(), aiEnvNote(fromEnv)))
			continue
		}
		get := func(key string) (string, bool) { return env(skyEnvName(aiSlotSuffix(slot, key))) }
		var fromEnv []string
		for _, key := range []string{"MODE", "URL", "KEY", "MODEL", "ADAPTER"} {
			if _, ok := get(key); ok {
				fromEnv = append(fromEnv, strings.ToLower(key))
			}
		}
		mode, _ := get("MODE")
		mode = strings.ToLower(strings.TrimSpace(mode))
		url, hasURL := get("URL")
		switch {
		case mode == "local" && hasURL:
			out = append(out, fmt.Sprintf("  %-11s  %s and %s are both set; the slot fails at its first call",
				label, skyEnvName(aiSlotSuffix(slot, "MODE"))+"=local", skyEnvName(aiSlotSuffix(slot, "URL"))))
		case mode == "remote" || (mode == "" && hasURL):
			target := "(no URL: set " + skyEnvName(aiSlotSuffix(slot, "URL")) + ")"
			if hasURL {
				target = aiRedactURL(strings.TrimRight(strings.TrimSpace(url), "/"))
			}
			out = append(out, fmt.Sprintf("  %-11s  %-6s  %s%s", label, "remote", target, aiEnvNote(fromEnv)))
		case mode == "local":
			model, _ := get("MODEL")
			target := "the code's model"
			if model != "" {
				target = aiModelLabel(model)
			}
			out = append(out, fmt.Sprintf("  %-11s  %-6s  %s%s", label, "local", target, aiEnvNote(fromEnv)))
		case len(fromEnv) > 0:
			out = append(out, fmt.Sprintf("  %-11s  set in code, resolved at the first call%s", label, aiEnvNote(fromEnv)))
		default:
			out = append(out, fmt.Sprintf("  %-11s  set in code, resolved at the first call", label))
		}
	}
	return out
}

// aiEnvNote names the settings the environment sets, never their values.
func aiEnvNote(keys []string) string {
	if len(keys) == 0 {
		return "  (all: code)"
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": env"
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

// aiModelLabel shortens a model reference to its file name and digest prefix.
func aiModelLabel(ref string) string {
	m, err := aiParseModelRef(ref)
	if err != nil {
		return "(an invalid model reference)"
	}
	name := m.file
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	return name + " sha256:" + m.sha256[:12]
}
