//go:build !js

package rt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stand-in for sky-trainer: `--version` prints the pinned version, a run
// records its arguments and writes a hello and a done line.
const fakeSkyTrainerScript = `#!/bin/sh
case "$1" in
--version) echo "sky-trainer 0.1.1 (mlx-c)"; exit 0 ;;
esac
for a in "$@"; do printf '%s\n' "$a"; done > "$(dirname "$0")/argv.txt"
echo '{"attempt":0,"backend":"mlx","resumeFrom":0,"toolchain":"native","totalSteps":1,"type":"hello","v":1}'
echo '{"step":1,"trainLoss":1,"type":"done","v":1,"valLoss":1}'
`

type trainerServer struct {
	bundle   []byte
	requests int
	mu       sync.Mutex
}

// trainerWorld points the trainer pins at a local server that serves a
// bundle holding the fake sky-trainer, in a fresh AI home.
func trainerWorld(t *testing.T, corrupt bool) *trainerServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake trainer is a shell script")
	}
	t.Setenv("SKY_AI_HOME", t.TempDir())
	t.Setenv("SKY_AI_FETCH", "on")
	t.Setenv("SKY_AI_TRAINER_BIN", "")
	ts := &trainerServer{bundle: tarGz(t, map[string]string{
		"sky-trainer-test/sky-trainer":      fakeSkyTrainerScript,
		"sky-trainer-test/lib/libmlx.so":    "not a library",
		"sky-trainer-test/NOTICE":           "MLX (MIT), mlx-c (MIT)",
		"sky-trainer-test/sbom.spdx.json":   "{}",
		"sky-trainer-test/lib/libmlxc.so":   "not a library",
		"sky-trainer-test/LICENSES/mlx.txt": "MIT",
	})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		ts.requests++
		ts.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/trainer.tar.gz") {
			http.NotFound(w, r)
			return
		}
		if corrupt {
			w.Write(ts.bundle[:len(ts.bundle)-1])
			return
		}
		w.Write(ts.bundle)
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(ts.bundle)
	savedAssets, savedURL := aiTrainerAssets, aiTrainerReleaseURL
	aiTrainerAssets = []aiTrainerAsset{{goos: runtime.GOOS, goarch: runtime.GOARCH, variant: "test", file: "trainer.tar.gz", sha256: hex.EncodeToString(sum[:])}}
	aiTrainerReleaseURL = func(f string) string { return srv.URL + "/trainer/" + f }
	t.Cleanup(func() { aiTrainerAssets, aiTrainerReleaseURL = savedAssets, savedURL })
	return ts
}

func failureOf(t *testing.T, err error, code aiFailCode) *aiFailure {
	t.Helper()
	f, ok := err.(*aiFailure)
	if !ok || f.code != code {
		t.Fatalf("want %v, got %v", code, err)
	}
	return f
}

func TestTrainerInstallsFromThePinnedBundle(t *testing.T) {
	ts := trainerWorld(t, false)
	b, err := aiEnsureTrainer(context.Background(), "mlx")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(aiHome(), "trainers", "mlx", aiTrainerVersion+"-test", "sky-trainer")
	if b.path != want || b.source != "pinned" || b.version != aiTrainerVersion {
		t.Fatalf("%+v, want %s", b, want)
	}
	for _, f := range []string{"ok.json", "NOTICE", "sbom.spdx.json", "lib/libmlx.so"} {
		if !fileExists(filepath.Join(filepath.Dir(b.path), filepath.FromSlash(f))) {
			t.Fatalf("no %s in the bundle directory", f)
		}
	}
	// Installed: a second call downloads nothing.
	before := ts.requests
	again, err := aiEnsureTrainer(context.Background(), "mlx")
	if err != nil || again.path != b.path || ts.requests != before {
		t.Fatalf("again: %v, requests %d -> %d", err, before, ts.requests)
	}
	entries, _ := os.ReadDir(filepath.Dir(filepath.Dir(b.path)))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "staging-") {
			t.Fatalf("staging left behind: %s", e.Name())
		}
	}
	// The value Train.provision returns names the trainer.
	v := b.value()
	if v["trainer"] != b.path || v["name"] != "mlx" || v["installed"] != true || v["python"] != "" {
		t.Fatalf("%v", v)
	}
}

func TestTrainerChecksumMismatchInstallsNothing(t *testing.T) {
	trainerWorld(t, true)
	_, err := aiEnsureTrainer(context.Background(), "mlx")
	f := failureOf(t, err, aiChecksumMismatch)
	exp, _ := f.attrValue("expected")
	act, _ := f.attrValue("actual")
	if !aiValidSha256(exp) || !aiValidSha256(act) || exp == act {
		t.Fatalf("both digests: %q %q", exp, act)
	}
	if entries, _ := os.ReadDir(filepath.Join(aiHome(), "trainers", "mlx")); len(entries) > 0 {
		for _, e := range entries {
			if e.Name() != "install.lock" {
				t.Fatalf("something was installed: %s", e.Name())
			}
		}
	}
}

func TestTrainerNotProvisionedWhenFetchIsOff(t *testing.T) {
	ts := trainerWorld(t, false)
	t.Setenv("SKY_AI_FETCH", "off")
	_, err := aiEnsureTrainer(context.Background(), "mlx")
	f := failureOf(t, err, aiNotProvisioned)
	if cmd, _ := f.attrValue("command"); cmd != "sky ai provision --train mlx" {
		t.Fatalf("command %q", cmd)
	}
	if ts.requests != 0 {
		t.Fatalf("fetch is off and %d requests were made", ts.requests)
	}
}

func TestTrainerFromTheOperatorsBinary(t *testing.T) {
	ts := trainerWorld(t, false)
	dir := t.TempDir()
	bin := filepath.Join(dir, "sky-trainer")
	if err := os.WriteFile(bin, []byte(fakeSkyTrainerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// The executable itself, or the directory that holds it.
	for _, v := range []string{bin, dir} {
		t.Setenv("SKY_AI_TRAINER_BIN", v)
		b, err := aiEnsureTrainer(context.Background(), "mlx")
		if err != nil {
			t.Fatal(err)
		}
		if b.path != bin || b.source != "env" {
			t.Fatalf("%s: %+v", v, b)
		}
	}
	if ts.requests != 0 {
		t.Fatalf("the operator's trainer was given and %d requests were made", ts.requests)
	}
	// Set to something that is not a trainer: NotProvisioned naming the
	// variable, never a silent fallback to the pinned bundle.
	t.Setenv("SKY_AI_TRAINER_BIN", filepath.Join(dir, "missing"))
	_, err := aiEnsureTrainer(context.Background(), "mlx")
	f := failureOf(t, err, aiNotProvisioned)
	if !strings.Contains(f.msg, "SKY_AI_TRAINER_BIN") {
		t.Fatalf("%s", f.msg)
	}
	// A binary that is not sky-trainer is refused.
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte("#!/bin/sh\necho hello\n"), 0o755)
	t.Setenv("SKY_AI_TRAINER_BIN", other)
	_, err = aiEnsureTrainer(context.Background(), "mlx")
	failureOf(t, err, aiToolchainFailed)
}

func TestTrainerWithoutABundleForThisHostNamesTheOverride(t *testing.T) {
	trainerWorld(t, false)
	aiTrainerAssets = nil
	_, err := aiEnsureTrainer(context.Background(), "mlx")
	f := failureOf(t, err, aiNotProvisioned)
	if cmd, _ := f.attrValue("command"); !strings.Contains(cmd, "SKY_AI_TRAINER_BIN") {
		t.Fatalf("command %q", cmd)
	}
	_, err = aiEnsureTrainer(context.Background(), "unsloth")
	failureOf(t, err, aiInvalidSetting)
}

// ─── the native launch ──────────────────────────────────────────────────

func writeQwen2Config(t *testing.T, dir string, extra map[string]any) {
	t.Helper()
	cfg := map[string]any{
		"architectures": []any{"Qwen2ForCausalLM"}, "hidden_size": 896, "intermediate_size": 4864,
		"num_attention_heads": 14, "num_hidden_layers": 24, "num_key_value_heads": 2, "rms_norm_eps": 1e-6,
		"rope_theta": 1000000.0, "tie_word_embeddings": true, "vocab_size": 151936,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func nativeDriver(weights, trainer string, over map[string]any) map[string]any {
	cfg := map[string]any{
		"alpha": 16, "attempt": 0, "backend": "mlx", "base": map[string]any{"id": "qwen", "weights": weights},
		"batch":           map[string]any{"accumulate": 2, "perDevice": 1},
		"checkpointEvery": 8, "evalEvery": 12, "layers": 8, "learningRate": 0.0001, "quantised": true,
		"rank": 8, "resumeDir": "", "resumeFrom": 0, "seed": 7, "sequenceLength": 128,
		"targets": []any{"self_attn.q_proj", "self_attn.v_proj"}, "trainer": trainer, "v": 1,
	}
	for k, v := range over {
		cfg[k] = v
	}
	return cfg
}

func argMap(t *testing.T, args []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			t.Fatalf("argument %q is not a flag: %v", args[i], args)
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			m[args[i]] = args[i+1]
			i++
		} else {
			m[args[i]] = ""
		}
	}
	return m
}

// The launcher reads driver.json through aiTrainJSON, which decodes numbers as
// json.Number. A learning rate set in code (Train.withLearningRate) must reach
// sky-trainer's --lr: it used to fall back to 1e-4 for every job, because the
// float reader only accepted float64 (found by examples/76-ai-shell-command,
// whose job asks for 2e-4).
func TestTrainNativeArgsReadTheLearningRateFromDriverJSON(t *testing.T) {
	weights := t.TempDir()
	writeQwen2Config(t, weights, map[string]any{"quantization": map[string]any{"bits": 4, "group_size": 64}})
	raw, err := json.Marshal(nativeDriver(weights, "/x/sky-trainer", map[string]any{"learningRate": 0.0002}))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := aiTrainJSON(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	args, err := aiTrainNativeArgs(cfg, "/runs/r", "sky-train-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := argMap(t, args)["--lr"]; got != "0.0002" {
		t.Fatalf("--lr = %q, want 0.0002 (the job's learning rate, read from driver.json)", got)
	}
}

func TestTrainNativeArgsComeFromTheDriverAndTheBaseConfig(t *testing.T) {
	weights := t.TempDir()
	writeQwen2Config(t, weights, map[string]any{"quantization": map[string]any{"bits": 4, "group_size": 64}})
	args, err := aiTrainNativeArgs(nativeDriver(weights, "/x/sky-trainer", map[string]any{
		"attempt": 1, "resumeFrom": 8, "resumeDir": "checkpoints/0000008", "device": "cpu",
	}), "/runs/r", "sky-train-abc")
	if err != nil {
		t.Fatal(err)
	}
	m := argMap(t, args)
	want := map[string]string{
		"--run": "/runs/r", "--launch": "sky-train-abc", "--model": weights, "--base-id": "qwen",
		"--layers": "24", "--hidden": "896", "--heads": "14", "--kv-heads": "2", "--intermediate": "4864",
		"--vocab": "151936", "--tied": "1", "--rope-theta": "1e+06", "--rms-eps": "1e-06",
		"--base-bits": "4", "--base-group-size": "64",
		"--rank": "8", "--alpha": "16", "--lora-layers": "8", "--targets": "q_proj,v_proj",
		"--lr": "0.0001", "--batch": "1", "--accumulate": "2", "--checkpoint-every": "8", "--eval-every": "12",
		"--seed": "7", "--attempt": "1", "--resume-step": "8", "--resume-dir": "checkpoints/0000008", "--cpu": "",
	}
	for k, v := range want {
		if got, ok := m[k]; !ok || got != v {
			t.Errorf("%s = %q (present %v), want %q", k, got, ok, v)
		}
	}
	if _, ok := m["--quant-bits"]; ok {
		t.Error("an MLX-quantised base is not quantised again")
	}

	// A dense base marked quantised is quantised on load; -1 layers is all.
	dense := t.TempDir()
	writeQwen2Config(t, dense, map[string]any{"tie_word_embeddings": false})
	args, err = aiTrainNativeArgs(nativeDriver(dense, "/x/sky-trainer", map[string]any{"layers": -1}), "/r", "tok")
	if err != nil {
		t.Fatal(err)
	}
	m = argMap(t, args)
	if m["--quant-bits"] != "4" || m["--lora-layers"] != "24" || m["--tied"] != "0" {
		t.Fatalf("%v", m)
	}
	if _, ok := m["--base-bits"]; ok {
		t.Fatal("a dense base has no --base-bits")
	}
	if _, ok := m["--resume-step"]; ok {
		t.Fatal("a first attempt resumes from nothing")
	}
	if _, ok := m["--cpu"]; ok {
		t.Fatal("--cpu without device cpu")
	}

	// Anything but Qwen2 is refused before a process starts.
	llama := t.TempDir()
	writeQwen2Config(t, llama, map[string]any{"architectures": []any{"LlamaForCausalLM"}})
	_, err = aiTrainNativeArgs(nativeDriver(llama, "/x/sky-trainer", nil), "/r", "tok")
	f := failureOf(t, err, aiInvalidSetting)
	if !strings.Contains(f.msg, "LlamaForCausalLM") {
		t.Fatalf("%s", f.msg)
	}
	_, err = aiTrainNativeArgs(nativeDriver(t.TempDir(), "/x/sky-trainer", nil), "/r", "tok")
	failureOf(t, err, aiInvalidSetting)
}

func TestTrainLaunchStartsTheNativeTrainer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake trainer is a shell script")
	}
	t.Setenv("SKY_AI_ACCEL", "")
	bindir := t.TempDir()
	bin := filepath.Join(bindir, "sky-trainer")
	os.WriteFile(bin, []byte(fakeSkyTrainerScript), 0o755)
	weights := t.TempDir()
	writeQwen2Config(t, weights, nil)
	dir := trainRun(t)
	cfg, _ := json.Marshal(nativeDriver(weights, bin, nil))
	v, err := aiTrainLaunch(dir, string(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if v["pid"].(int) <= 0 {
		t.Fatalf("%v", v)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(filepath.Join(dir, aiTrainEventsFile)); strings.Contains(string(b), `"type":"done"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	events, _ := os.ReadFile(filepath.Join(dir, aiTrainEventsFile))
	if !strings.Contains(string(events), `"toolchain":"native"`) {
		t.Fatalf("events: %s", events)
	}
	argv, _ := os.ReadFile(filepath.Join(bindir, "argv.txt"))
	tr, _ := aiReadTrainer(dir)
	m := argMap(t, strings.Split(strings.TrimSpace(string(argv)), "\n"))
	abs, _ := filepath.Abs(dir)
	if m["--launch"] != tr.Launch || m["--run"] != abs || m["--model"] != weights {
		t.Fatalf("argv %v, trainer %+v", m, tr)
	}

	// A trainer path that does not exist is NotProvisioned, naming the command.
	dir2 := trainRun(t)
	cfg, _ = json.Marshal(nativeDriver(weights, filepath.Join(bindir, "gone"), nil))
	_, err = aiTrainLaunch(dir2, string(cfg))
	f := failureOf(t, err, aiNotProvisioned)
	if cmd, _ := f.attrValue("command"); cmd != "sky ai provision --train mlx" {
		t.Fatalf("command %q", cmd)
	}
}
