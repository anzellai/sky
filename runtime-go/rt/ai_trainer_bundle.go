//go:build !js

package rt

// ai_trainer_bundle.go — sky-trainer, the native trainer Std.Ai.Train runs
// for the Mlx backend, and the command line it is launched with.
//
// The trainer is a separate executable, not part of the Sky binary: an app
// stays CGO_ENABLED=0 and cross-builds, carries no Metal library, and a Metal
// out-of-memory kill ends the trainer, never the app. It comes from one of:
//
//   - <PREFIX>_AI_TRAINER_BIN: the operator's sky-trainer (the executable or
//     the directory that holds it), for example one built with
//     scripts/ai/trainer/build.sh. Set but not a sky-trainer is an error,
//     never a silent fallback to the pinned bundle.
//   - the pinned bundle for this host (ai_pins.go): downloaded, checked by
//     its SHA-256 (ChecksumMismatch installs nothing), unpacked into a staging
//     directory, run once with --version, and renamed into
//     <AI home>/trainers/<backend>/<version>-<variant>/ with ok.json last.
//
// Go does all the parsing: it reads the base's config.json (only
// Qwen2ForCausalLM is trained), and passes the shapes, the LoRA settings and
// the run's place as flags. The data is the tokenize step's train.tok,
// valid.tok and order.bin in the run directory.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const aiTrainerExe = "sky-trainer"

// aiTrainerCacheLimit bounds MLX's buffer cache (256 MiB): freed buffers MLX
// keeps for reuse count in the process's footprint, and so against the cap.
const aiTrainerCacheLimit = 256 << 20

// aiTrainerBinary is a usable sky-trainer.
type aiTrainerBinary struct {
	backend string
	path    string
	id      string
	version string
	variant string // "metal", "cpu" or "custom"
	source  string // "env" or "pinned"
}

// value is the Toolchain record Train.provision returns for the trainer.
func (b aiTrainerBinary) value() map[string]any {
	return map[string]any{
		"name": b.backend, "id": b.id, "python": "", "driverDir": "", "dir": filepath.Dir(b.path),
		"versions": []any{map[string]any{"name": aiTrainerExe, "version": b.version}},
		"packages": 0, "installed": true, "trainer": b.path,
	}
}

func aiTrainerSupported(backend string) error {
	if backend != "mlx" {
		return aiFailf(aiInvalidSetting, "the native trainer runs the mlx backend; %s trains in its own toolchain", backend).
			with("setting", "backend").with("reason", "the native trainer runs the mlx backend")
	}
	return nil
}

func aiHostTrainerAsset() (aiTrainerAsset, bool) {
	for _, a := range aiTrainerAssets {
		if a.goos == runtime.GOOS && a.goarch == runtime.GOARCH {
			return a, true
		}
	}
	return aiTrainerAsset{}, false
}

func aiTrainerDir(backend string, a aiTrainerAsset) string {
	return filepath.Join(aiHome(), "trainers", backend, aiTrainerVersion+"-"+a.variant)
}

// aiTrainerOk is the ok.json of an installed bundle.
type aiTrainerOk struct {
	Version string `json:"version"`
	Variant string `json:"variant"`
	File    string `json:"file"`
	Sha256  string `json:"sha256"`
}

// aiInstalledTrainer reads a complete bundle: ok.json names this pin and the
// trainer is there.
func aiInstalledTrainer(backend string, a aiTrainerAsset) (aiTrainerBinary, bool) {
	dir := aiTrainerDir(backend, a)
	b, err := os.ReadFile(filepath.Join(dir, "ok.json"))
	if err != nil {
		return aiTrainerBinary{}, false
	}
	var ok aiTrainerOk
	if json.Unmarshal(b, &ok) != nil || ok.Sha256 != a.sha256 || ok.Version != aiTrainerVersion {
		return aiTrainerBinary{}, false
	}
	bin := filepath.Join(dir, aiExe(aiTrainerExe))
	if !fileExists(bin) {
		return aiTrainerBinary{}, false
	}
	return aiTrainerBinary{backend: backend, path: bin, id: aiTrainerVersion + "-" + a.variant + "-" + a.sha256[:12],
		version: aiTrainerVersion, variant: a.variant, source: "pinned"}, true
}

// aiOperatorTrainer resolves <PREFIX>_AI_TRAINER_BIN: "" when it is unset.
func aiOperatorTrainer() (string, error) {
	v := strings.TrimSpace(skyGetenv("AI_TRAINER_BIN"))
	if v == "" {
		return "", nil
	}
	bin := v
	if st, err := os.Stat(v); err == nil && st.IsDir() {
		bin = filepath.Join(v, aiExe(aiTrainerExe))
	}
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		name := skyEnvName("AI_TRAINER_BIN")
		return "", aiFailf(aiNotProvisioned,
			"%s does not name a sky-trainer executable (%s); point it at sky-trainer or the directory that holds it, or unset it to use the pinned bundle",
			name, bin).with("what", aiTrainerExe).with("command", "set "+name+" to a sky-trainer built with scripts/ai/trainer/build.sh")
	}
	return bin, nil
}

// aiCheckTrainer runs `sky-trainer --version` and returns the version. want
// is the version a pinned bundle must print ("" for the operator's own).
func aiCheckTrainer(bin, want string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = aiEngineEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", aiToolFail(aiTrainerExe, "%s --version failed: %v: %s", bin, err, aiTail(string(out), 600))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 || fields[0] != aiTrainerExe {
		return "", aiToolFail(aiTrainerExe, "%s --version printed %q; it is not sky-trainer", bin, aiTail(string(out), 200))
	}
	if want != "" && fields[1] != want {
		return "", aiToolFail(aiTrainerExe, "%s is sky-trainer %s, and this Sky pins %s", bin, fields[1], want)
	}
	return fields[1], nil
}

// aiFindTrainer is the trainer that is there now, without fetching or
// running anything: the operator's, else an installed pinned bundle.
func aiFindTrainer(backend string) (aiTrainerBinary, bool, error) {
	if err := aiTrainerSupported(backend); err != nil {
		return aiTrainerBinary{}, false, err
	}
	bin, err := aiOperatorTrainer()
	if err != nil {
		return aiTrainerBinary{}, false, err
	}
	if bin != "" {
		digest, _ := aiFileSha256(bin)
		return aiTrainerBinary{backend: backend, path: bin, id: "custom-" + aiShort(digest), variant: "custom", source: "env"}, true, nil
	}
	if a, ok := aiHostTrainerAsset(); ok {
		if b, ok := aiInstalledTrainer(backend, a); ok {
			return b, true, nil
		}
	}
	return aiTrainerBinary{}, false, nil
}

func aiShort(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// aiEnsureTrainer returns the trainer for backend, installing the pinned
// bundle when fetching is allowed.
func aiEnsureTrainer(ctx context.Context, backend string) (aiTrainerBinary, error) {
	b, found, err := aiFindTrainer(backend)
	if err != nil {
		return aiTrainerBinary{}, err
	}
	if found && b.source == "env" {
		v, err := aiCheckTrainer(b.path, "")
		if err != nil {
			return aiTrainerBinary{}, err
		}
		b.version = v
		return b, nil
	}
	if found {
		return b, nil
	}
	a, ok := aiHostTrainerAsset()
	if !ok {
		name := skyEnvName("AI_TRAINER_BIN")
		return aiTrainerBinary{}, aiFailf(aiNotProvisioned,
			"there is no pinned sky-trainer bundle for %s/%s; build one with scripts/ai/trainer/build.sh and set %s to it",
			runtime.GOOS, runtime.GOARCH, name).
			with("what", aiTrainerExe).with("command", "set "+name+" to a sky-trainer built with scripts/ai/trainer/build.sh")
	}
	allowed, err := aiFetchAllowed()
	if err != nil {
		return aiTrainerBinary{}, err
	}
	if !allowed {
		return aiTrainerBinary{}, aiFailf(aiNotProvisioned,
			"sky-trainer %s (%s) is not installed and fetching is off (%s); turn fetching on and run `sky ai provision --train %s` on this host, or set %s to a sky-trainer built with scripts/ai/trainer/build.sh",
			aiTrainerVersion, a.variant, skyEnvName("AI_FETCH"), backend, skyEnvName("AI_TRAINER_BIN")).
			with("what", aiTrainerExe+" "+aiTrainerVersion).with("command", "sky ai provision --train "+backend)
	}
	return aiInstallTrainer(ctx, backend, a)
}

// aiInstallTrainer downloads, verifies and installs the pinned bundle. Two
// programs provisioning at once take turns on a lock file; the second finds
// the first's bundle.
func aiInstallTrainer(ctx context.Context, backend string, a aiTrainerAsset) (aiTrainerBinary, error) {
	final := aiTrainerDir(backend, a)
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "cannot create %s: %v", parent, err)
	}
	unlock, err := aiLockFile(filepath.Join(parent, "install.lock"), 30*time.Minute)
	if err != nil {
		return aiTrainerBinary{}, err
	}
	defer unlock()
	if b, ok := aiInstalledTrainer(backend, a); ok {
		return b, nil
	}
	archive := filepath.Join(aiHome(), "downloads", a.file)
	if _, err := aiDownload(ctx, aiTrainerReleaseURL(a.file), archive, a.sha256); err != nil {
		return aiTrainerBinary{}, err
	}
	defer os.Remove(archive)
	tmp, err := os.MkdirTemp(parent, "staging-")
	if err != nil {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "cannot create a staging directory: %v", err)
	}
	defer os.RemoveAll(tmp)
	f, err := os.Open(archive)
	if err != nil {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "cannot open %s: %v", archive, err)
	}
	err = extractTarGz(f, tmp)
	f.Close()
	if err != nil {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "cannot extract %s: %v", a.file, err)
	}
	root := tmp
	if entries, err := os.ReadDir(tmp); err == nil && len(entries) == 1 && entries[0].IsDir() {
		root = filepath.Join(tmp, entries[0].Name())
	}
	bin := filepath.Join(root, aiExe(aiTrainerExe))
	if !fileExists(bin) {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "%s has no %s", a.file, aiTrainerExe)
	}
	if _, err := aiCheckTrainer(bin, aiTrainerVersion); err != nil {
		return aiTrainerBinary{}, err
	}
	ok, _ := json.MarshalIndent(aiTrainerOk{Version: aiTrainerVersion, Variant: a.variant, File: a.file, Sha256: a.sha256}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "ok.json"), ok, 0o644); err != nil {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "cannot write ok.json: %v", err)
	}
	os.RemoveAll(final)
	if err := os.Rename(root, final); err != nil {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "cannot install the trainer at %s: %v", final, err)
	}
	b, found := aiInstalledTrainer(backend, a)
	if !found {
		return aiTrainerBinary{}, aiToolFail(aiTrainerExe, "the trainer at %s does not read back", final)
	}
	return b, nil
}

// ─── the command line ───────────────────────────────────────────────────

// aiQwen2Config is what the trainer needs from a base's config.json.
type aiQwen2Config struct {
	Architectures []string `json:"architectures"`
	Hidden        int      `json:"hidden_size"`
	Intermediate  int      `json:"intermediate_size"`
	Heads         int      `json:"num_attention_heads"`
	Layers        int      `json:"num_hidden_layers"`
	KVHeads       int      `json:"num_key_value_heads"`
	RmsEps        float64  `json:"rms_norm_eps"`
	RopeTheta     float64  `json:"rope_theta"`
	Tied          bool     `json:"tie_word_embeddings"`
	Vocab         int      `json:"vocab_size"`
	Quantization  *struct {
		Bits      int `json:"bits"`
		GroupSize int `json:"group_size"`
	} `json:"quantization"`
}

func aiBaseInvalid(weights, format string, args ...any) *aiFailure {
	reason := fmt.Sprintf(format, args...)
	return aiFailf(aiInvalidSetting, "the base at %s: %s", weights, reason).with("setting", "base").with("reason", reason)
}

// aiReadQwen2Config reads and checks a base's config.json.
func aiReadQwen2Config(weights string) (aiQwen2Config, error) {
	var c aiQwen2Config
	b, err := os.ReadFile(filepath.Join(weights, "config.json"))
	if err != nil {
		return c, aiBaseInvalid(weights, "it has no readable config.json")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, aiBaseInvalid(weights, "config.json is not JSON: %v", err)
	}
	qwen := false
	for _, a := range c.Architectures {
		qwen = qwen || a == "Qwen2ForCausalLM"
	}
	if !qwen {
		return c, aiBaseInvalid(weights, "its architecture is %s; the native trainer trains Qwen2ForCausalLM", strings.Join(c.Architectures, ", "))
	}
	if c.Hidden <= 0 || c.Intermediate <= 0 || c.Heads <= 0 || c.Layers <= 0 || c.KVHeads <= 0 || c.Vocab <= 0 ||
		c.RmsEps <= 0 || c.RopeTheta <= 0 {
		return c, aiBaseInvalid(weights, "config.json lacks one of hidden_size, intermediate_size, num_attention_heads, num_hidden_layers, num_key_value_heads, vocab_size, rms_norm_eps, rope_theta")
	}
	if q := c.Quantization; q != nil && ((q.Bits != 4 && q.Bits != 8) || q.GroupSize <= 0) {
		return c, aiBaseInvalid(weights, "its quantization is %d bits in groups of %d; the trainer reads 4 or 8 bits", q.Bits, q.GroupSize)
	}
	return c, nil
}

func aiFloatArg(x float64) string { return strconv.FormatFloat(x, 'g', -1, 64) }

func aiJFloat(m map[string]any, key string, def float64) float64 {
	if x, ok := m[key].(float64); ok && !math.IsNaN(x) && !math.IsInf(x, 0) {
		return x
	}
	return def
}

// aiTrainNativeArgs is sky-trainer's command line for one attempt, from the
// attempt's driver.json and the base's config.json.
func aiTrainNativeArgs(cfg map[string]any, runDir, token string) ([]string, error) {
	base := aiJMap(cfg, "base")
	weights := aiJStr(base, "weights")
	c, err := aiReadQwen2Config(weights)
	if err != nil {
		return nil, err
	}
	var targets []string
	if ts, ok := cfg["targets"].([]any); ok {
		for _, t := range ts {
			s, _ := t.(string)
			if i := strings.LastIndexByte(s, '.'); i >= 0 {
				s = s[i+1:]
			}
			if s != "" {
				targets = append(targets, s)
			}
		}
	}
	if len(targets) == 0 {
		return nil, aiFailf(aiInvalidSetting, "the job names no target projections").with("setting", "targets").with("reason", "no targets")
	}
	seed := aiJNum(cfg, "seed", 0)
	if seed < 0 {
		return nil, aiFailf(aiInvalidSetting, "the seed is %d; use a seed of 0 or more", seed).with("setting", "seed").with("reason", "the seed must be 0 or more")
	}
	layers := aiJNum(cfg, "layers", 16)
	if layers < 0 || layers > c.Layers {
		layers = c.Layers
	}
	tied := "0"
	if c.Tied {
		tied = "1"
	}
	batch := aiJMap(cfg, "batch")
	args := []string{
		"--run", runDir, "--launch", token, "--model", weights, "--base-id", aiJStr(base, "id"),
		"--layers", strconv.Itoa(c.Layers), "--hidden", strconv.Itoa(c.Hidden), "--heads", strconv.Itoa(c.Heads),
		"--kv-heads", strconv.Itoa(c.KVHeads), "--intermediate", strconv.Itoa(c.Intermediate),
		"--vocab", strconv.Itoa(c.Vocab), "--tied", tied,
		"--rope-theta", aiFloatArg(c.RopeTheta), "--rms-eps", aiFloatArg(c.RmsEps),
	}
	bits := aiJNum(cfg, "quantBits", 0)
	if quantised, _ := cfg["quantised"].(bool); bits == 0 && quantised {
		bits = 4
	}
	if q := c.Quantization; q != nil {
		args = append(args, "--base-bits", strconv.Itoa(q.Bits), "--base-group-size", strconv.Itoa(q.GroupSize))
	} else if bits == 4 || bits == 8 {
		args = append(args, "--quant-bits", strconv.Itoa(bits))
	}
	args = append(args,
		"--rank", strconv.Itoa(aiJNum(cfg, "rank", 8)), "--alpha", strconv.Itoa(aiJNum(cfg, "alpha", 16)),
		"--lora-layers", strconv.Itoa(layers), "--targets", strings.Join(targets, ","),
		"--lr", aiFloatArg(aiJFloat(cfg, "learningRate", 1e-4)),
		"--batch", strconv.Itoa(max(1, aiJNum(batch, "perDevice", 1))),
		"--accumulate", strconv.Itoa(max(1, aiJNum(batch, "accumulate", 1))),
		"--checkpoint-every", strconv.Itoa(max(0, aiJNum(cfg, "checkpointEvery", 0))),
		"--eval-every", strconv.Itoa(max(0, aiJNum(cfg, "evalEvery", 0))),
		"--seed", strconv.Itoa(seed),
		"--attempt", strconv.Itoa(aiJNum(cfg, "attempt", 0)),
	)
	if from := aiJNum(cfg, "resumeFrom", 0); from > 0 {
		args = append(args, "--resume-step", strconv.Itoa(from), "--resume-dir", aiJStr(cfg, "resumeDir"))
	}
	args = append(args, "--cache-limit", strconv.Itoa(aiTrainerCacheLimit))
	if capBytes := aiJNum(cfg, "memoryCap", 0); capBytes > 0 {
		args = append(args, "--memory-cap", strconv.Itoa(capBytes))
	}
	if aiJStr(cfg, "device") == "cpu" {
		args = append(args, "--cpu")
	}
	return args, nil
}

// ─── kernels ────────────────────────────────────────────────────────────

// Ai_trainerEnsure : String -> Task Error Toolchain — the native trainer of a
// backend, installed if fetching is allowed: sky-trainer for Mlx, the
// runtime's built-in trainer for Fake (nothing to install).
func Ai_trainerEnsure(nameArg any) any {
	name := strings.ToLower(strings.TrimSpace(AsString(nameArg)))
	return func() any {
		if name == "fake" {
			if testModeActive() {
				return Ok[any, any](aiTestToolchain(name))
			}
			return Ok[any, any](aiFakeToolchain())
		}
		if err := aiTrainerSupported(name); err != nil {
			return aiTaskErr(err)
		}
		if testModeActive() {
			v := aiTestToolchain(name)
			v["trainer"] = "test-mode"
			return Ok[any, any](v)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		b, err := aiEnsureTrainer(ctx, name)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](b.value())
	}
}

// aiTrainerStatus is the native trainer's Toolchain record for
// Ai_toolchainStatus, installed or not.
func aiTrainerStatus(backend string) (map[string]any, bool) {
	b, found, err := aiFindTrainer(backend)
	if err != nil {
		return nil, false
	}
	if found {
		v := b.value()
		if b.version == "" {
			v["versions"] = []any{}
		}
		return v, true
	}
	dir, id := "", ""
	if a, ok := aiHostTrainerAsset(); ok {
		dir, id = aiTrainerDir(backend, a), aiTrainerVersion+"-"+a.variant+"-"+a.sha256[:12]
	}
	return map[string]any{"name": backend, "id": id, "python": "", "driverDir": "", "dir": dir,
		"versions": []any{}, "packages": 0, "installed": false, "trainer": ""}, true
}
