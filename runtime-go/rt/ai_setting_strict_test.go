//go:build !js

package rt

// A bad AI setting fails loudly. A value an operator (or the code) gives an
// AI setting that does not parse is an InvalidSetting failure naming the
// variable (or the builder) and the form expected. It is never skipped in
// favour of another layer, and a value that could be secret never appears in
// the message. Settings read when a server starts refuse the start; settings
// read per call fail the call.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// mustInvalid asserts err is InvalidSetting naming setting, with a reason,
// and (when hidden is not "") without hidden anywhere in the message.
func mustInvalid(t *testing.T, err error, setting, hidden string) {
	t.Helper()
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiInvalidSetting {
		t.Fatalf("got %v; want InvalidSetting naming %s", err, setting)
	}
	if v, _ := f.attrValue("setting"); v != setting {
		t.Fatalf("setting %q; want %q (%v)", v, setting, err)
	}
	if v, _ := f.attrValue("reason"); v == "" {
		t.Fatalf("no reason: %v", err)
	}
	if !strings.Contains(f.msg, setting) {
		t.Fatalf("the message does not name %s: %v", setting, err)
	}
	if hidden != "" && strings.Contains(f.msg+f.Error(), hidden) {
		t.Fatalf("the message shows the value %q: %v", hidden, err)
	}
}

// TestAiSlotSettingIsRefusedNotSkipped covers every kind of slot setting: a
// whole number, a duration in ms, an enum, a URL, a model reference, the
// adapter list and the draft settings. Each bad operator value fails the
// slot's resolution (and so the call) naming the variable.
func TestAiSlotSettingIsRefusedNotSkipped(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	remote := endpointBuilder("chat")
	remote.url = "https://code.example.test/v1"
	remote.timeoutMs, remote.maxInFlight, remote.queue, remote.queueWaitMs, remote.retries = 7000, 3, 5, 900, 1
	local := endpointBuilder("chat")
	local.hasLocal = true
	local.local = localBuilder(model)
	local.local.context, local.local.parallel, local.local.idleUnload, local.local.port = 2048, 2, 30, 0
	local.local.draftMax, local.local.draftMin = -1, -1

	cases := []struct {
		name, key, value string
		b                aiEndpointBuilder
		secret           bool
	}{
		{"int: max in flight", "MAX_INFLIGHT", "lots", remote, false},
		{"int: a negative queue", "QUEUE", "-1", remote, false},
		{"int: retries", "RETRIES", "two", remote, false},
		{"ms: timeout", "TIMEOUT_MS", "soon", remote, false},
		{"ms: queue wait of 0", "QUEUE_WAIT_MS", "0", remote, false},
		{"enum: mode", "MODE", "cloud", remote, false},
		{"enum: auth", "AUTH", "basic", remote, false},
		{"enum: gpu", "GPU", "lots", local, false},
		{"url: no scheme, with credentials", "URL", "user:hunter2@gpu.example.test/v1", remote, true},
		{"local int: context", "CONTEXT", "big", local, false},
		{"local int: parallel of 0", "PARALLEL", "0", local, false},
		{"local seconds: idle unload", "IDLE_UNLOAD_S", "-5", local, false},
		{"local port: out of range", "PORT", "70000", local, false},
		{"model ref: unpinned local model", "MODEL", "hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/x.gguf", local, false},
		{"adapter ref: half a registry ref", "ADAPTER", "registry:support", remote, false},
		{"draft: unpinned", "DRAFT", "hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/x.gguf", local, false},
		{"draft: max", "DRAFT_MAX", "many", local, false},
		{"draft: min", "DRAFT_MIN", "-1", local, false},
		{"adapters: no reference", "ADAPTERS", "support", local, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name := "SKY_AI_CHAT_" + c.key
			t.Setenv(name, c.value)
			_, err := resolveAiSlot(c.b)
			hidden := ""
			if c.secret {
				hidden = "hunter2"
			}
			mustInvalid(t, err, name, hidden)
			if !c.secret && c.key != "MODEL" && !strings.Contains(err.Error(), c.value) {
				t.Fatalf("a value that is not secret is shown to help the operator: %v", err)
			}
		})
	}
}

// TestAiSettingFromCodeNamesTheBuilder: a code value that does not parse is
// refused too, naming the builder, on a slot and on Local.start.
func TestAiSettingFromCodeNamesTheBuilder(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	b := endpointBuilder("chat")
	b.url = "https://code.example.test/v1"
	b.timeoutMs = 0
	_, err := resolveAiSlot(b)
	mustInvalid(t, err, "Endpoint.withTimeout", "")

	lb := localBuilder(model)
	lb.context, lb.draftMax, lb.draftMin = 0, -1, -1
	_, err = resolveAiLocal("", lb, "", func(aiSetting) {})
	mustInvalid(t, err, "Local.withContext", "")

	lb = localBuilder(model)
	lb.draftMax, lb.draftMin = -1, -1
	lb.port = 70000
	_, err = resolveAiLocal("", lb, "", func(aiSetting) {})
	mustInvalid(t, err, "Local.withPort", "")
}

// TestAiGlobalSettingIsRefused: the global variables fail the call that
// reads them: the accelerator (engine fit and training host), fetching, and
// the Google Cloud emulator URL (whose value is never shown).
func TestAiGlobalSettingIsRefused(t *testing.T) {
	aiTestEnv(t, nil)
	t.Setenv("SKY_AI_ACCEL", "gpu")
	_, err := aiAccelerator("cpu")
	mustInvalid(t, err, "SKY_AI_ACCEL", "")
	_, err = aiTrainAccelerator()
	mustInvalid(t, err, "SKY_AI_ACCEL", "")
	t.Setenv("SKY_AI_ACCEL", "")

	t.Setenv("SKY_AI_FETCH", "maybe")
	_, err = aiFetchAllowed()
	mustInvalid(t, err, "SKY_AI_FETCH", "")
	t.Setenv("SKY_AI_FETCH", "")

	t.Setenv("SKY_AI_GCP_ENDPOINT", "localhost:9/token=hunter2")
	res := Ai_gcpEndpoint(nil).(func() any)()
	tag, _, errV := anyResultView(res)
	if tag == 0 {
		t.Fatalf("Ai_gcpEndpoint accepted a value that is not a URL: %v", res)
	}
	text := skyErrorMessage(errV)
	if !strings.Contains(text, "SKY_AI_GCP_ENDPOINT") || strings.Contains(text, "hunter2") {
		t.Fatalf("got %q; want SKY_AI_GCP_ENDPOINT named and the value hidden", text)
	}
	if aiGcpLoopbackEmulator("localhost:9") {
		t.Fatal("an invalid emulator URL opened the network in test mode")
	}

	t.Setenv("SKY_AI_TRAIN_BACKEND", "tpu")
	_, _, err = aiTrainResolveBackend("")
	mustInvalid(t, err, "SKY_AI_TRAIN_BACKEND", "")
}

// TestAiDevicePlacementIsRefusedNotSkipped: a PLACEMENT that is not auto,
// device or server fails the offer, never runs on the server by default.
func TestAiDevicePlacementIsRefusedNotSkipped(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	srv := deviceServer(t, deviceEndpoint("chat", "", "auto", modelRef, ""))
	t.Setenv("SKY_AI_CHAT_PLACEMENT", "edge")
	resp, body := get(t, srv.URL+"/_sky/ai/device/chat", nil)
	f, ok := aiDeviceFailureFrom([]byte(body))
	if resp.StatusCode == 200 || !ok || f.code != aiInvalidSetting {
		t.Fatalf("got %d %s; want InvalidSetting", resp.StatusCode, body)
	}
	if v, _ := f.attrValue("setting"); v != "SKY_AI_CHAT_PLACEMENT" {
		t.Fatalf("setting %q", v)
	}
	aiDeviceReset()
	t.Setenv("SKY_AI_CHAT_PLACEMENT", "")
	t.Setenv("SKY_AI_CHAT_DEVICE_MODEL", "file:/nowhere.gguf")
	srv2 := deviceServer(t, deviceEndpoint("chat", "", "device", modelRef, ""))
	_, body = get(t, srv2.URL+"/_sky/ai/device/chat", nil)
	if f, ok := aiDeviceFailureFrom([]byte(body)); !ok || f.code != aiInvalidSetting {
		t.Fatalf("device model: %s", body)
	} else if v, _ := f.attrValue("setting"); v != "SKY_AI_CHAT_DEVICE_MODEL" {
		t.Fatalf("device model setting %q", v)
	}
}

// TestAiStartupRefusesABadSetting: the check a server runs before it
// listens names the first variable that does not parse, for a declared slot
// and for a global variable, and passes when every value parses.
func TestAiStartupRefusesABadSetting(t *testing.T) {
	aiTestEnv(t, nil)
	AiDeclareSlots([]string{"chat"})
	t.Cleanup(func() { AiDeclareSlots(nil) })
	if err := aiStartupSettingsError(); err != nil {
		t.Fatalf("nothing set: %v", err)
	}
	t.Setenv("SKY_AI_CHAT_TIMEOUT_MS", "30000")
	t.Setenv("SKY_AI_CHAT_URL", "https://gpu.example.test/v1")
	if err := aiStartupSettingsError(); err != nil {
		t.Fatalf("valid values: %v", err)
	}
	t.Setenv("SKY_AI_CHAT_RETRIES", "a few")
	mustInvalid(t, aiStartupSettingsError(), "SKY_AI_CHAT_RETRIES", "")
	t.Setenv("SKY_AI_CHAT_RETRIES", "")
	t.Setenv("SKY_AI_CHAT_URL", "gpu.example.test/v1?key=hunter2")
	mustInvalid(t, aiStartupSettingsError(), "SKY_AI_CHAT_URL", "hunter2")
	t.Setenv("SKY_AI_CHAT_URL", "")
	t.Setenv("SKY_AI_FETCH", "sometimes")
	err := aiStartupSettingsError()
	mustInvalid(t, err, "SKY_AI_FETCH", "")
	if line := aiStartupRefusal(err); !strings.Contains(line, "refuses to start") || !strings.Contains(line, "SKY_AI_FETCH") {
		t.Fatalf("refusal line %q", line)
	}
	// An embedded Sky.Live app gets the typed failure as its Task's Err.
	v := liveStartError(err, true)
	b, _ := json.Marshal(skyErrorMessage(v))
	if !strings.Contains(string(b), "SKY_AI_FETCH") {
		t.Fatalf("embedded start error %s", b)
	}
	if tag, _, _ := anyResultView(Err[any, any](v)); tag == 0 {
		t.Fatal("not an Err")
	}
}

// The GPU tier's settings (ai_serve.go) are refused, not skipped: a bad
// SKY_AI_SERVE_MAX_ADAPTERS used to fall back to the default in silence.
func TestAiServeSettingIsRefusedNotSkipped(t *testing.T) {
	base := aiTierConfig{engine: "vllm", maxAdapters: -1, poll: -1 * time.Second}
	cases := []struct{ key, value string }{
		{"MAX_ADAPTERS", "many"},
		{"MAX_ADAPTERS", "0"},
		{"POLL_S", "soon"},
		{"BIND", "everywhere"},
		{"ENGINE", "tgi"},
		{"ENGINE_URL", "engine.internal:8000"},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			name := skyEnvName("AI_SERVE_" + c.key)
			t.Setenv(name, c.value)
			_, _, err := aiServeSettings(base, "")
			mustInvalid(t, err, name, "")
		})
	}
	t.Run("a good value is kept", func(t *testing.T) {
		t.Setenv(skyEnvName("AI_SERVE_MAX_ADAPTERS"), "6")
		_, c, err := aiServeSettings(base, "")
		if err != nil || c.maxAdapters != 6 {
			t.Fatalf("got %d, %v", c.maxAdapters, err)
		}
	})
}
