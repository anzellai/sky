//go:build !js

package rt

import (
	"strings"
	"testing"
)

func trainCode(over map[string]string) map[string]any {
	c := map[string]any{
		"sequenceLength": "1024", "batch": "4x1", "rank": "8", "quantBits": "16",
		"memoryCap": "",
	}
	for k, v := range over {
		c[k] = v
	}
	return c
}

func settingsByKey(t *testing.T, code map[string]any) map[string][2]string {
	t.Helper()
	got, err := aiTrainSettingsResolve(code)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]string{}
	for _, s := range got {
		out[s["key"].(string)] = [2]string{s["value"].(string), s["source"].(string)}
		if s["name"] != skyEnvName("AI_TRAIN_"+s["key"].(string)) {
			t.Errorf("%v names %v", s["key"], s["name"])
		}
	}
	return out
}

func TestTrainSettingsResolveTheOperatorsVariablesOverTheCode(t *testing.T) {
	for _, k := range aiTrainSettingKeys {
		t.Setenv("SKY_AI_TRAIN_"+k, "")
	}
	got := settingsByKey(t, trainCode(nil))
	want := map[string][2]string{
		"SEQUENCE_LENGTH": {"1024", "code"}, "BATCH": {"4x1", "code"}, "RANK": {"8", "code"},
		"QUANT_BITS": {"16", "code"}, "MEMORY_CAP": {"", "default"},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
	t.Setenv("SKY_AI_TRAIN_SEQUENCE_LENGTH", "256")
	t.Setenv("SKY_AI_TRAIN_BATCH", "2")
	t.Setenv("SKY_AI_TRAIN_RANK", " 16 ")
	t.Setenv("SKY_AI_TRAIN_QUANT_BITS", "4")
	t.Setenv("SKY_AI_TRAIN_MEMORY_CAP", "2.5GB")
	got = settingsByKey(t, trainCode(nil))
	want = map[string][2]string{
		"SEQUENCE_LENGTH": {"256", "env"}, "BATCH": {"2x1", "env"}, "RANK": {"16", "env"},
		"QUANT_BITS": {"4", "env"}, "MEMORY_CAP": {"2500000000", "env"},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
	for v, bytes := range map[string]string{"2684354560": "2684354560", "2500MB": "2500000000", "2.5GiB": "2684354560", "512 MiB": "536870912"} {
		t.Setenv("SKY_AI_TRAIN_MEMORY_CAP", v)
		if got := settingsByKey(t, trainCode(nil))["MEMORY_CAP"][0]; got != bytes {
			t.Errorf("MEMORY_CAP %q = %s, want %s", v, got, bytes)
		}
	}
}

func TestTrainSettingsResolveRefusesABadValueWithoutShowingIt(t *testing.T) {
	cases := map[string]string{
		"SEQUENCE_LENGTH": "-12", "BATCH": "2x0", "RANK": "-3", "QUANT_BITS": "5",
		"MEMORY_CAP": "10 parsecs",
	}
	for k, bad := range cases {
		t.Run(k, func(t *testing.T) {
			for _, other := range aiTrainSettingKeys {
				t.Setenv("SKY_AI_TRAIN_"+other, "")
			}
			t.Setenv("SKY_AI_TRAIN_"+k, bad)
			_, err := aiTrainSettingsResolve(trainCode(nil))
			f := failureOf(t, err, aiInvalidSetting)
			if setting, _ := f.attrValue("setting"); setting != "SKY_AI_TRAIN_"+k {
				t.Fatalf("setting %q", setting)
			}
			reason, _ := f.attrValue("reason")
			if strings.Contains(f.msg, bad) || strings.Contains(reason, bad) {
				t.Fatalf("the value is shown: %s / %s", f.msg, reason)
			}
		})
	}
	// A bad value in code names the builder.
	_, err := aiTrainSettingsResolve(trainCode(map[string]string{"quantBits": "5"}))
	f := failureOf(t, err, aiInvalidSetting)
	if setting, _ := f.attrValue("setting"); setting != "Train.withQuantBits" {
		t.Fatalf("setting %q", setting)
	}
}

func TestTrainNativeArgsCarryTheMemorySettings(t *testing.T) {
	dense := t.TempDir()
	writeQwen2Config(t, dense, nil)
	args, err := aiTrainNativeArgs(nativeDriver(dense, "/x/sky-trainer", map[string]any{
		"quantised": true, "quantBits": 8, "memoryCap": 2500000000,
	}), "/r", "tok")
	if err != nil {
		t.Fatal(err)
	}
	m := argMap(t, args)
	for k, v := range map[string]string{"--quant-bits": "8", "--memory-cap": "2500000000", "--cache-limit": "268435456"} {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	args, _ = aiTrainNativeArgs(nativeDriver(dense, "/x/sky-trainer", map[string]any{"quantised": false, "quantBits": 16}), "/r", "tok")
	m = argMap(t, args)
	if _, ok := m["--quant-bits"]; ok {
		t.Fatalf("%v", m)
	}
	if _, ok := m["--memory-cap"]; ok {
		t.Fatal("no cap in the driver, no --memory-cap")
	}
}

// aiTrainSettingKeys are the settings an operator may override, in the
// resolver's order.
var aiTrainSettingKeys = []string{"SEQUENCE_LENGTH", "BATCH", "RANK", "QUANT_BITS", "MEMORY_CAP"}

// Gradient checkpointing is not a setting: the native trainer's chunked loss
// recomputes each adapted layer in its own backward pass, and Unsloth manages
// its own checkpointing. SKY_AI_TRAIN_GRAD_CHECKPOINT is not read, and the
// trainer is never passed --grad-checkpoint.
func TestTrainSettingsHaveNoGradientCheckpointing(t *testing.T) {
	t.Setenv("SKY_AI_TRAIN_GRAD_CHECKPOINT", "sometimes")
	got, err := aiTrainSettingsResolve(trainCode(nil))
	if err != nil {
		t.Fatalf("SKY_AI_TRAIN_GRAD_CHECKPOINT was read: %v", err)
	}
	var keys []string
	for _, s := range got {
		keys = append(keys, s["key"].(string))
	}
	if strings.Join(keys, ",") != strings.Join(aiTrainSettingKeys, ",") {
		t.Fatalf("settings %v, want %v", keys, aiTrainSettingKeys)
	}
	dense := t.TempDir()
	writeQwen2Config(t, dense, nil)
	args, err := aiTrainNativeArgs(nativeDriver(dense, "/x/sky-trainer", map[string]any{"gradientCheckpointing": true}), "/r", "tok")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if a == "--grad-checkpoint" {
			t.Fatalf("the trainer is passed --grad-checkpoint: %v", args)
		}
	}
}
