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

// aiSlotEnvForms are the forms of every `<PREFIX>_AI_<SLOT>_<KEY>` variable
// that has one. KEY and the free-text keys (MODEL on a remote slot is a
// server's model name) are not listed.
var aiSlotEnvForms = []struct {
	key string
	ex  aiExpect
}{
	{"MODE", aiExpectMode},
	{"URL", aiExpectURL},
	{"ADAPTER", aiExpectAdapterRef},
	{"ADAPTER_REFRESH_S", aiExpectNonNegative},
	{"AUTH", aiExpectAuth},
	{"TIMEOUT_MS", aiExpectPositive},
	{"MAX_INFLIGHT", aiExpectPositive},
	{"QUEUE", aiExpectNonNegative},
	{"QUEUE_WAIT_MS", aiExpectPositive},
	{"RETRIES", aiExpectNonNegative},
	{"CONTEXT", aiExpectPositive},
	{"GPU", aiExpectGpu},
	{"PARALLEL", aiExpectPositive},
	{"IDLE_UNLOAD_S", aiExpectNonNegative},
	{"PORT", aiExpectPort},
	{"DRAFT", aiExpectModelRef},
	{"DRAFT_MAX", aiExpectPositive},
	{"DRAFT_MIN", aiExpectNonNegative},
	{"ADAPTERS", aiExpectAdapterList},
	{"PLACEMENT", aiExpectPlacement},
	{"DEVICE_MODEL", aiExpectModelRef},
	{"DEVICE_ADAPTER", aiExpectDeviceAdRef},
	{"DEVICE_THREADS", aiExpectThreads},
	{"DEVICE_GPU_LAYERS", aiExpectGpuLayers},
}

// aiGlobalEnvForms are the global AI variables that have a form.
var aiGlobalEnvForms = []struct {
	suffix string
	ex     aiExpect
}{
	{"AI_FETCH", aiExpectFetch},
	{"AI_ACCEL", aiExpectAccel},
	{"AI_TRAIN_BACKEND", aiExpectTrainBack},
	{"AI_GCP_ENDPOINT", aiExpectURL},
	{"AI_REGISTRY", aiExpectRegistry},
}

// aiStartupSettingsError checks, before a server listens, every AI variable
// the environment sets for the slots the source declares and every global
// AI variable: a value that does not parse is an InvalidSetting failure that
// names the variable and the form expected (never a value that could be
// secret), and the app refuses to start. A program that serves nothing
// meets the same check at its first AI call instead. A local slot's MODEL is
// checked as a model reference at that call, because a remote slot's MODEL
// is free text.
func aiStartupSettingsError() error {
	for _, g := range aiGlobalEnvForms {
		if _, err := aiEnvChecked(g.suffix, g.ex); err != nil {
			return err
		}
	}
	for _, slot := range aiDeclared() {
		if aiValidSlot(slot) != nil {
			continue
		}
		for _, f := range aiSlotEnvForms {
			if _, err := aiEnvChecked(aiSlotSuffix(slot, f.key), f.ex); err != nil {
				return err
			}
		}
	}
	return nil
}

// aiStartupRefusal is the line a server prints when it refuses to start on
// an AI setting.
func aiStartupRefusal(err error) string {
	return "[sky.ai] FATAL: the app refuses to start: " + aiErrorText(err)
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
