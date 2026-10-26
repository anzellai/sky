//go:build !js

package rt

// ai_train.go — the runtime half of Std.Ai.Train: a training run's directory,
// the detached trainer process, and the protocol file it writes.
//
// Sky decides; Go does the I/O. The durable workflow in
// sky-stdlib/Std/Ai/Train.sky calls these kernels as journalled steps:
//
//	Ai_trainHost        : ()     -> Task Error Host
//	Ai_trainResolve     : String -> Task Error Setting      the backend
//	Ai_trainShapes      : String -> Task Error Shape        a serving GGUF's shape
//	Ai_trainBaseEnsure  : String -> Task Error String       a pinned weights snapshot
//	Ai_trainBaseReference, Ai_trainBasePin                  ai_train_base.go
//	Ai_trainStage       : String -> String -> String -> String -> Task Error Staged
//	Ai_trainLaunch      : String -> String -> Task Error Launched
//	Ai_trainWatch       : String -> Int -> Int -> Task Error Watched
//	Ai_trainCheckpoints : String -> Task Error (List Checkpoint)
//	Ai_trainCancel      : String -> Task Error Bool
//	Ai_trainRead        : String -> Task Error RunState
//	Ai_trainRuns        : String -> Task Error (List RunEntry)
//	Ai_trainFinish      : String -> String -> Task Error ()
//
// A run directory `<workDir>/<runId>/` is the source of truth for a run:
//
//	job.json              the job as Train.jobJson encodes it
//	data/train.jsonl      the frozen version's Train rows
//	data/valid.jsonl      its Eval rows
//	driver.json           the attempt the driver is running
//	trainer.json          pid, launch token, attempt, resumeFrom, start time
//	events.jsonl          the driver's protocol lines (its stdout)
//	stderr.log            the driver's stderr
//	checkpoints/<step>/   adapter.safetensors, optimizer.safetensors,
//	                      state.json, manifest.json
//	adapter/              the PEFT adapter
//	adapter.gguf          the GGUF adapter (withGgufExport)
//	cancel                present once a cancel was asked for
//	result.json           the finished run's record, written by Train.sky
//
// The trainer is not a Process.spawn child: such a child dies with the
// program, and a training run must outlive the program that launched it (a
// laptop's `sky ai train` is Ctrl-C'd, a worker restarts). It is started in a
// session of its own with its stdout redirected to events.jsonl, and the Sky
// side reads that file from an offset. Its identity is the launch token on its
// command line, so a recycled pid is never attached to and never signalled.
//
// Under test mode no process starts: Ai_trainLaunch writes the protocol a Fake
// run would write, a tiny valid PEFT adapter and real checkpoints, so a
// program's tests run the whole workflow offline in milliseconds.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sky-app/rt/periodic"
)

const (
	aiTrainJobFile     = "job.json"
	aiTrainDriverFile  = "driver.json"
	aiTrainTrainerFile = "trainer.json"
	aiTrainEventsFile  = "events.jsonl"
	aiTrainStderrFile  = "stderr.log"
	aiTrainCancelFile  = "cancel"
	aiTrainResultFile  = "result.json"
	aiTrainStageFile   = "stage.json"
	// aiTrainGuardFile is written by sky-trainer's memory guard before it
	// kills the trainer over its cap (scripts/ai/trainer/guard.c).
	aiTrainGuardFile = "guard.json"
)

// aiTrainMaxWatch bounds one watch. A durable step must end well inside the
// 30-minute lease, so a long run is watched in windows of at most 5 minutes.
const aiTrainMaxWatch = 5 * time.Minute

// aiTrainCancelGrace is how long a cancelled trainer has to write its
// checkpoint and its `cancelled` line before it is killed. A variable for the
// tests.
var aiTrainCancelGrace = 60 * time.Second

// aiTrainPollEvery is the watch loop's poll interval.
var aiTrainPollEvery = 200 * time.Millisecond

// aiTrainLauncher builds the trainer command for one attempt (driver.json as
// cfg): the native sky-trainer when the attempt names one (the Mlx backend,
// ai_trainer_bundle.go), else the Python driver in its toolchain. A variable
// so the Go tests run testdata/faketrainer instead.
var aiTrainLauncher = func(cfg map[string]any, runDir, token string) (*exec.Cmd, error) {
	if bin := aiJStr(cfg, "trainer"); bin != "" {
		args, err := aiTrainNativeArgs(cfg, runDir, token)
		if err != nil {
			return nil, err
		}
		return exec.Command(bin, args...), nil
	}
	return exec.Command(aiJStr(cfg, "python"), "-I", "-B", filepath.Join(aiJStr(cfg, "driverDir"), "sky_train.py"),
		"--run", runDir, "--launch", token), nil
}

// ─── run directories seen by this process (for the console) ─────────────

var aiTrainSeen struct {
	sync.Mutex
	dirs map[string]bool
}

func aiTrainNoteRun(runDir string) {
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return
	}
	aiTrainSeen.Lock()
	if aiTrainSeen.dirs == nil {
		aiTrainSeen.dirs = map[string]bool{}
	}
	aiTrainSeen.dirs[abs] = true
	aiTrainSeen.Unlock()
}

// ─── small helpers ──────────────────────────────────────────────────────

func aiWriteFileAtomic(path string, data []byte) error {
	rnd, err := aiRandomHex(4)
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + rnd
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func aiTrainFail(format string, args ...any) *aiFailure {
	return aiFailf(aiTrainingFailed, format, args...)
}

func aiTrainJSON(s string) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func aiJNum(m map[string]any, key string, def int) int {
	switch x := m[key].(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
		if f, err := x.Float64(); err == nil {
			return int(f)
		}
	case float64:
		return int(x)
	case int:
		return x
	}
	return def
}

func aiJStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func aiJMap(m map[string]any, key string) map[string]any {
	x, _ := m[key].(map[string]any)
	if x == nil {
		return map[string]any{}
	}
	return x
}

// ─── host and backend ───────────────────────────────────────────────────

// aiHasCUDA reports an NVIDIA driver on this host (Linux).
func aiHasCUDA() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return fileExists("/proc/driver/nvidia/version") || fileExists("/dev/nvidiactl")
}

// aiTrainAccelerator is the accelerator a training run uses:
// `<PREFIX>_AI_ACCEL=cpu` forces the CPU, else the host's. A value that is
// not one of the accelerator words is InvalidSetting, never ignored.
func aiTrainAccelerator() (string, error) {
	v, err := aiEnvChecked("AI_ACCEL", aiExpectAccel)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(v, "cpu") {
		return "cpu", nil
	}
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "metal", nil
	case aiHasCUDA():
		return "cuda", nil
	}
	return "cpu", nil
}

// Ai_trainHost : () -> Task Error Host
func Ai_trainHost(_ any) any {
	return func() any {
		accel, err := aiTrainAccelerator()
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](map[string]any{
			"os":             runtime.GOOS,
			"arch":           runtime.GOARCH,
			"totalBytes":     int(detectRAMBytes()),
			"availableBytes": int(aiHostAvailableBytes()),
			"accelerator":    accel,
		})
	}
}

// aiTrainResolveBackend applies the backend rule: `<PREFIX>_AI_TRAIN_BACKEND`
// beats Train.withBackend in code, which beats detection (Mlx on Apple
// silicon, Unsloth on Linux with an NVIDIA GPU). A backend this host cannot
// run is NoAccelerator, never a silent fallback to another one.
func aiTrainResolveBackend(code string) (backend, source string, err error) {
	name := skyEnvName("AI_TRAIN_BACKEND")
	valid := func(s string) bool {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "mlx", "unsloth", "vertex", "fake":
			return true
		}
		return false
	}
	if raw, ok := lookupEnvRaw(name); ok && strings.TrimSpace(raw) != "" && !valid(raw) {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "sagemaker" {
			return "", "", aiFailf(aiInvalidSetting, "%s=%q: the SageMaker backend is not supported yet; use vertex (Google Cloud), mlx, unsloth or fake", name, raw).with("setting", name).with("reason", "the SageMaker backend is not supported yet")
		}
		return "", "", aiFailf(aiInvalidSetting, "%s=%q: use mlx, unsloth, vertex or fake", name, raw).with("setting", name).with("reason", "use mlx, unsloth, vertex or fake")
	}
	for i, v := range configLayers("AI_TRAIN_BACKEND", code) {
		if !valid(v) {
			continue
		}
		backend = strings.ToLower(strings.TrimSpace(v))
		source = "code"
		if i == 0 && v != code {
			source = "env"
		}
		if raw, ok := lookupEnvRaw(name); ok && raw == v && v != code {
			source = "env"
		}
		break
	}
	if backend == "" {
		switch {
		case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
			backend = "mlx"
		case aiHasCUDA():
			backend = "unsloth"
		default:
			return "", "", aiFailf(aiNoAccelerator,
				"no training backend for %s/%s: Mlx needs Apple silicon and Unsloth needs Linux with an NVIDIA GPU; set %s=fake to run the scripted Fake backend",
				runtime.GOOS, runtime.GOARCH, name).with("wanted", "mlx or unsloth")
		}
		source = "detected"
	}
	switch backend {
	case "mlx":
		if !(runtime.GOOS == "darwin" && runtime.GOARCH == "arm64") {
			return "", "", aiFailf(aiNoAccelerator, "the Mlx backend runs on Apple silicon, and this host is %s/%s; use unsloth on a Linux GPU host, or fake", runtime.GOOS, runtime.GOARCH).with("wanted", "apple-silicon")
		}
	case "unsloth":
		if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || !aiHasCUDA() {
			return "", "", aiFailf(aiNoAccelerator, "the Unsloth backend runs on Linux x86_64 with an NVIDIA GPU (CUDA 13), and this host is %s/%s without one; use mlx on a Mac, or fake", runtime.GOOS, runtime.GOARCH).with("wanted", "cuda")
		}
	}
	return backend, source, nil
}

// Ai_trainResolve : String -> Task Error Setting — the backend to train with.
func Ai_trainResolve(codeArg any) any {
	code := strings.ToLower(strings.TrimSpace(AsString(codeArg)))
	return func() any {
		b, src, err := aiTrainResolveBackend(code)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](map[string]any{"key": "TRAIN_BACKEND", "name": skyEnvName("AI_TRAIN_BACKEND"), "value": b, "source": src})
	}
}

// ─── the serving model's shape ──────────────────────────────────────────

// aiTrainShape is what training needs to know about the base: the projection
// sizes (the Fake adapter's shapes, the memory estimate) and the
// architecture (the GGUF adapter's).
type aiTrainShape struct {
	arch                                   string
	layers, hidden, intermediate, qDim, kv int
	vocab                                  int
	params                                 int64
}

// aiDevShape is Qwen2.5 0.5B Instruct, the development base. Test mode uses it
// without reading the model file, which is not on a test machine.
var aiDevShape = aiTrainShape{arch: "qwen2", layers: 24, hidden: 896, intermediate: 4864, qDim: 896, kv: 128, vocab: 151936, params: 494032768}

func aiTrainShapeOf(path string) (aiTrainShape, error) {
	kv, tensors, err := readGGUFHeader(path)
	if err != nil {
		return aiTrainShape{}, err
	}
	m, err := ggufMetaFromKV(kv)
	if err != nil {
		return aiTrainShape{}, err
	}
	num := func(k string) int {
		switch x := kv[m.arch+"."+k].(type) {
		case uint64:
			return int(x)
		case int64:
			return int(x)
		}
		return 0
	}
	s := aiTrainShape{arch: m.arch, layers: m.blockCount, hidden: m.embedding, intermediate: num("feed_forward_length")}
	head := m.keyLength
	if head == 0 && m.headCount > 0 {
		head = m.embedding / m.headCount
	}
	s.qDim = m.headCount * head
	s.kv = m.headCountKV * head
	if s.qDim == 0 {
		s.qDim = s.hidden
	}
	if s.kv == 0 {
		s.kv = s.hidden
	}
	for _, t := range tensors {
		n := int64(1)
		for _, d := range t.dims {
			n *= int64(d)
		}
		s.params += n
		if t.name == "token_embd.weight" && len(t.dims) == 2 {
			s.vocab = int(t.dims[1])
		}
	}
	return s, nil
}

func aiTrainShapeValue(s aiTrainShape) map[string]any {
	return map[string]any{
		"architecture": s.arch,
		"layers":       s.layers,
		"hidden":       s.hidden,
		"intermediate": s.intermediate,
		"qDim":         s.qDim,
		"kvDim":        s.kv,
		"vocab":        s.vocab,
		"parameters":   int(s.params),
	}
}

// aiTrainServingShape reads the shape of a serving model reference, fetching
// the model when allowed.
func aiTrainServingShape(ctx context.Context, ref string) (aiTrainShape, string, error) {
	m, err := aiParseModelRef(ref)
	if err != nil {
		return aiTrainShape{}, "", err
	}
	if testModeActive() {
		return aiDevShape, "", nil
	}
	path, err := aiEnsureModel(ctx, m)
	if err != nil {
		return aiTrainShape{}, "", err
	}
	s, err := aiTrainShapeOf(path)
	if err != nil {
		return aiTrainShape{}, "", aiFailf(aiInvalidSetting, "the serving model %s cannot be read: %v", ref, err).with("setting", "serving")
	}
	return s, path, nil
}

// Ai_trainShapes : String -> Task Error Shape
func Ai_trainShapes(refArg any) any {
	ref := AsString(refArg)
	return func() any {
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		s, _, err := aiTrainServingShape(ctx, ref)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](aiTrainShapeValue(s))
	}
}

// ─── staging ────────────────────────────────────────────────────────────

// Ai_trainStage : String -> String -> String -> String -> Task Error Staged
//
// runDir, the frozen version's data file, its digest, and the job's JSON. The
// file is hashed first (a version whose bytes changed is ChecksumMismatch),
// then its Train rows go to data/train.jsonl and its Eval rows to
// data/valid.jsonl, each line the row's own canonical JSON (the chat or
// instruction format MLX-LM and TRL read). Staging the same version again
// writes nothing.
func Ai_trainStage(runArg, blobArg, digestArg, jobArg any) any {
	runDir, blob, digest, job := AsString(runArg), AsString(blobArg), AsString(digestArg), AsString(jobArg)
	return func() any {
		staged, err := aiTrainStage(runDir, blob, digest, job)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](staged)
	}
}

func aiTrainStage(runDir, blob, digest, job string) (map[string]any, error) {
	if err := os.MkdirAll(filepath.Join(runDir, "data"), 0o755); err != nil {
		return nil, aiTrainFail("cannot create %s: %v", runDir, err)
	}
	aiTrainNoteRun(runDir)
	if b, err := os.ReadFile(filepath.Join(runDir, aiTrainStageFile)); err == nil {
		var st struct {
			Digest      string `json:"digest"`
			Train, Eval int
		}
		if json.Unmarshal(b, &st) == nil && st.Digest == digest {
			return map[string]any{"train": st.Train, "eval": st.Eval}, nil
		}
	}
	got, err := aiFileSha256(blob)
	if err != nil {
		return nil, aiFailf(aiDatasetInvalid, "cannot read the dataset file %s: %v", blob, err).with("dataset", blob).with("reasons", err.Error())
	}
	if got != digest {
		return nil, aiFailf(aiChecksumMismatch, "the dataset file %s is sha256 %s, and its version says %s; it changed after it was frozen", blob, got, digest).
			with("expected", digest).with("actual", got)
	}
	in, err := os.Open(blob)
	if err != nil {
		return nil, aiFailf(aiDatasetInvalid, "cannot read %s: %v", blob, err).with("dataset", blob).with("reasons", err.Error())
	}
	defer in.Close()
	var train, valid bytes.Buffer
	nTrain, nEval := 0, 0
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		var rec struct {
			Row   json.RawMessage `json:"row"`
			Split string          `json:"split"`
		}
		if err := json.Unmarshal(text, &rec); err != nil || len(rec.Row) == 0 {
			return nil, aiFailf(aiDatasetInvalid, "%s line %d is not a frozen dataset row", blob, line).with("dataset", blob).with("reasons", fmt.Sprintf("line %d is not a frozen dataset row", line))
		}
		switch rec.Split {
		case "train":
			train.Write(rec.Row)
			train.WriteByte('\n')
			nTrain++
		case "eval":
			valid.Write(rec.Row)
			valid.WriteByte('\n')
			nEval++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, aiFailf(aiDatasetInvalid, "cannot read %s: %v", blob, err).with("dataset", blob).with("reasons", err.Error())
	}
	for name, data := range map[string][]byte{
		filepath.Join("data", "train.jsonl"): train.Bytes(),
		filepath.Join("data", "valid.jsonl"): valid.Bytes(),
		aiTrainJobFile:                       []byte(job + "\n"),
	} {
		if err := aiWriteFileAtomic(filepath.Join(runDir, name), data); err != nil {
			return nil, aiTrainFail("cannot write %s: %v", name, err)
		}
	}
	st, _ := json.Marshal(map[string]any{"digest": digest, "train": nTrain, "eval": nEval})
	if err := aiWriteFileAtomic(filepath.Join(runDir, aiTrainStageFile), st); err != nil {
		return nil, aiTrainFail("cannot write %s: %v", aiTrainStageFile, err)
	}
	return map[string]any{"train": nTrain, "eval": nEval}, nil
}

// ─── the trainer ────────────────────────────────────────────────────────

type aiTrainer struct {
	Pid         int    `json:"pid"`
	Launch      string `json:"launch"`
	Attempt     int    `json:"attempt"`
	ResumeFrom  int    `json:"resumeFrom"`
	StartedAtMs int64  `json:"startedAtMs"`
	Toolchain   string `json:"toolchain"`
	Simulated   bool   `json:"simulated"`
}

func aiReadTrainer(runDir string) (aiTrainer, bool) {
	b, err := os.ReadFile(filepath.Join(runDir, aiTrainTrainerFile))
	if err != nil {
		return aiTrainer{}, false
	}
	var t aiTrainer
	if json.Unmarshal(b, &t) != nil {
		return aiTrainer{}, false
	}
	return t, true
}

// aiTrainerAlive reports whether the trainer a trainer.json names still runs.
// The pid alone is not enough: after the trainer exits, the kernel may give
// its pid to another process. The launch token on the command line is unique
// to the launch, so only the real trainer matches. Without `ps` (a minimal
// container) it answers false, and a cancel then relies on the cancel file.
func aiTrainerAlive(t aiTrainer) bool {
	if t.Pid <= 0 || t.Simulated || !processAlive(t.Pid) {
		return false
	}
	cmd, ok := processCommand(t.Pid)
	return ok && t.Launch != "" && strings.Contains(cmd, t.Launch)
}

// Ai_trainLaunch : String -> String -> Task Error Launched
//
// Start attempt `attempt` of a run (driver.json as Train.sky built it:
// the job's settings, the backend, the toolchain's python and driver
// directories, the base and its shapes, attempt, resumeFrom, resumeDir).
// Idempotent: when trainer.json already records this attempt or a later one,
// it attaches to that trainer instead of starting a second.
func Ai_trainLaunch(runArg, driverArg any) any {
	runDir, driver := AsString(runArg), AsString(driverArg)
	return func() any {
		v, err := aiTrainLaunch(runDir, driver)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](v)
	}
}

func aiTrainerValue(t aiTrainer, attached bool) map[string]any {
	return map[string]any{
		"pid":         t.Pid,
		"attempt":     t.Attempt,
		"resumeFrom":  t.ResumeFrom,
		"startedAtMs": int(t.StartedAtMs),
		"attached":    attached,
	}
}

func aiTrainLaunch(runDir, driver string) (map[string]any, error) {
	cfg, err := aiTrainJSON(driver)
	if err != nil {
		return nil, aiTrainFail("the driver configuration is not JSON: %v", err)
	}
	aiTrainNoteRun(runDir)
	attempt := aiJNum(cfg, "attempt", 0)
	unlock, err := aiLockFile(filepath.Join(runDir, "launch.lock"), 2*time.Minute)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if t, ok := aiReadTrainer(runDir); ok && t.Attempt >= attempt {
		return aiTrainerValue(t, true), nil
	}
	// `<PREFIX>_AI_ACCEL=cpu` runs MLX on the CPU (a CI runner has no Metal).
	accel, err := aiTrainAccelerator()
	if err != nil {
		return nil, err
	}
	if accel == "cpu" {
		cfg["device"] = "cpu"
	}
	if b, err := json.Marshal(cfg); err == nil {
		driver = string(b)
	}
	if err := aiWriteFileAtomic(filepath.Join(runDir, aiTrainDriverFile), []byte(driver)); err != nil {
		return nil, aiTrainFail("cannot write %s: %v", aiTrainDriverFile, err)
	}
	launch, err := aiRandomHex(8)
	if err != nil {
		return nil, err
	}
	t := aiTrainer{
		Launch:      "sky-train-" + launch,
		Attempt:     attempt,
		ResumeFrom:  aiJNum(cfg, "resumeFrom", 0),
		StartedAtMs: time.Now().UnixMilli(),
		Toolchain:   aiJStr(cfg, "toolchain"),
	}
	if testModeActive() {
		t.Simulated = true
		if err := aiTrainSimulate(runDir, cfg); err != nil {
			return nil, err
		}
	} else {
		backend := aiJStr(cfg, "backend")
		if bin := aiJStr(cfg, "trainer"); bin != "" {
			if !fileExists(bin) {
				return nil, aiFailf(aiNotProvisioned, "the %s trainer (%s) is not installed; run `sky ai provision --train %s`",
					backend, bin, backend).with("what", aiTrainerExe).with("command", "sky ai provision --train "+backend)
			}
		} else if python := aiJStr(cfg, "python"); python == "" || !fileExists(python) {
			return nil, aiFailf(aiNotProvisioned, "the %s toolchain's python (%s) is not installed; run `sky ai provision --train %s`",
				backend, python, backend).with("what", backend+" toolchain").with("command", "sky ai provision --train "+backend)
		}
		// A guard.json from an earlier attempt is not this attempt's.
		if err := os.Remove(filepath.Join(runDir, aiTrainGuardFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, aiTrainFail("cannot clear %s: %v", aiTrainGuardFile, err)
		}
		pid, err := aiTrainSpawn(runDir, t.Launch, cfg)
		if err != nil {
			return nil, err
		}
		t.Pid = pid
	}
	b, _ := json.Marshal(t)
	if err := aiWriteFileAtomic(filepath.Join(runDir, aiTrainTrainerFile), b); err != nil {
		return nil, aiTrainFail("cannot write %s: %v", aiTrainTrainerFile, err)
	}
	return aiTrainerValue(t, false), nil
}

// aiTrainEnv is the trainer's environment: cleared, then an allow-list (the
// engine's, plus the offline switches). A token, a key or a pip / uv setting
// of the user's never reaches the trainer.
func aiTrainEnv(cfg map[string]any) []string {
	env := aiEngineEnv()
	env = append(env,
		"HF_HUB_OFFLINE=1",
		"TRANSFORMERS_OFFLINE=1",
		"HF_DATASETS_OFFLINE=1",
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONUNBUFFERED=1",
		"TOKENIZERS_PARALLELISM=false",
	)
	return env
}

func aiTrainSpawn(runDir, token string, cfg map[string]any) (int, error) {
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return 0, aiTrainFail("cannot resolve %s: %v", runDir, err)
	}
	events, err := os.OpenFile(filepath.Join(abs, aiTrainEventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, aiTrainFail("cannot open %s: %v", aiTrainEventsFile, err)
	}
	defer events.Close()
	stderr, err := os.OpenFile(filepath.Join(abs, aiTrainStderrFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, aiTrainFail("cannot open %s: %v", aiTrainStderrFile, err)
	}
	defer stderr.Close()
	cmd, err := aiTrainLauncher(cfg, abs, token)
	if err != nil {
		return 0, err
	}
	cmd.Dir = abs
	cmd.Stdin = nil
	cmd.Stdout = events
	cmd.Stderr = stderr
	cmd.Env = aiTrainEnv(cfg)
	cmd.SysProcAttr = aiTrainSysAttr()
	if err := cmd.Start(); err != nil {
		return 0, aiFailf(aiToolchainFailed, "cannot start the trainer %s: %v", cmd.Path, err).with("tool", "trainer").with("logTail", err.Error())
	}
	pid := cmd.Process.Pid
	// Reap the trainer if it ends while this program runs; if this program
	// ends first, the trainer is re-parented and keeps training.
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

// ─── watching ───────────────────────────────────────────────────────────

// aiTrainReadLines reads the complete lines of the events file from offset,
// at most limit bytes of them. A line still being written (no newline yet) is
// left for the next read.
func aiTrainReadLines(runDir string, offset int64, limit int) ([]string, int64, error) {
	f, err := os.Open(filepath.Join(runDir, aiTrainEventsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, offset, nil
	}
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return nil, offset, err
	}
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return nil, offset, nil
	}
	var lines []string
	for _, l := range bytes.Split(buf[:end], []byte{'\n'}) {
		if s := strings.TrimSpace(string(l)); s != "" {
			lines = append(lines, s)
		}
	}
	return lines, offset + int64(end) + 1, nil
}

// aiTrainTerminal reports whether a protocol line ends an attempt.
func aiTrainTerminal(line string) bool {
	var ev struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil {
		return false
	}
	switch ev.Type {
	case "done", "error", "cancelled":
		return true
	}
	return false
}

// Ai_trainWatch : String -> Int -> Int -> Task Error Watched
//
// Wait up to maxWaitMs (at most 5 minutes) for the attempt to end: a terminal
// protocol line, or the trainer gone. Returns the complete lines after offset,
// the next offset, and whether the trainer is still alive. Each poll runs
// inside periodic.Guard.
func Ai_trainWatch(runArg, offsetArg, waitArg any) any {
	runDir := AsString(runArg)
	offset := int64(AsInt(offsetArg))
	wait := time.Duration(AsInt(waitArg)) * time.Millisecond
	return func() any {
		v, err := aiTrainWatch(runDir, offset, wait)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](v)
	}
}

const aiTrainWatchLimit = 4 << 20

func aiTrainWatch(runDir string, offset int64, wait time.Duration) (map[string]any, error) {
	if wait <= 0 || wait > aiTrainMaxWatch {
		wait = aiTrainMaxWatch
	}
	aiTrainNoteRun(runDir)
	deadline := time.Now().Add(wait)
	var lines []string
	alive := false
	var readErr error
	for {
		done := false
		periodic.Guard("ai.train-watch", periodicReport, func() error {
			t, _ := aiReadTrainer(runDir)
			alive = aiTrainerAlive(t)
			got, next, err := aiTrainReadLines(runDir, offset, aiTrainWatchLimit)
			if err != nil {
				readErr = err
				done = true
				return err
			}
			lines = append(lines, got...)
			offset = next
			for _, l := range got {
				if aiTrainTerminal(l) {
					done = true
				}
			}
			if !alive || len(lines) > 20000 {
				done = true
			}
			return nil
		})
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(aiTrainPollEvery)
	}
	if readErr != nil {
		return nil, aiTrainFail("cannot read %s: %v", aiTrainEventsFile, readErr)
	}
	if !alive {
		// The trainer may have written its last lines as it exited.
		if got, next, err := aiTrainReadLines(runDir, offset, aiTrainWatchLimit); err == nil {
			lines = append(lines, got...)
			offset = next
		}
		lines, offset = aiTrainGuardEnd(runDir, lines, offset)
	}
	out := make([]any, len(lines))
	for i, l := range lines {
		out[i] = l
	}
	return map[string]any{
		"offset":          int(offset),
		"lines":           out,
		"alive":           alive,
		"cancelRequested": fileExists(filepath.Join(runDir, aiTrainCancelFile)),
		"stderrTail":      aiTrainStderrTail(runDir),
	}, nil
}

// aiTrainGuardEnd: a trainer gone without a terminal line, whose guard wrote
// guard.json, was killed over its memory cap. The guard writes guard.json
// before the kill and its error line after, so the line may still be on its
// way: wait for it a little, else end the attempt with the line guard.json
// stands for. Either way the workflow sees ModelTooLarge, never a death it
// would relaunch into the same cap.
func aiTrainGuardEnd(runDir string, lines []string, offset int64) ([]string, int64) {
	for _, l := range lines {
		if aiTrainTerminal(l) {
			return lines, offset
		}
	}
	b, err := os.ReadFile(filepath.Join(runDir, aiTrainGuardFile))
	if err != nil {
		return lines, offset
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, next, err := aiTrainReadLines(runDir, offset, aiTrainWatchLimit)
		if err == nil {
			lines = append(lines, got...)
			offset = next
			for _, l := range got {
				if aiTrainTerminal(l) {
					return lines, offset
				}
			}
		}
		time.Sleep(aiTrainPollEvery)
	}
	var g struct {
		CapBytes  int64  `json:"capBytes"`
		PeakBytes int64  `json:"peakBytes"`
		Code      string `json:"code"`
	}
	if json.Unmarshal(b, &g) != nil || g.Code == "" {
		g.Code = "ModelTooLarge"
	}
	line, _ := json.Marshal(map[string]any{
		"type": "error", "v": 1, "code": g.Code, "capBytes": g.CapBytes, "peakBytes": g.PeakBytes,
		"message": fmt.Sprintf("the trainer used %d bytes, over its memory cap of %d bytes; give the job a smaller sequence length, batch or rank, a quantised base, or a larger cap", g.PeakBytes, g.CapBytes),
	})
	return append(lines, string(line)), offset
}

func aiTrainStderrTail(runDir string) string {
	f, err := os.Open(filepath.Join(runDir, aiTrainStderrFile))
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	const n = 1200
	if st.Size() > n {
		f.Seek(st.Size()-n, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	return aiTail(string(b), n)
}

// ─── checkpoints ────────────────────────────────────────────────────────

type aiCheckpoint struct {
	step int
	path string
}

// aiTrainCheckpoints lists the checkpoints whose files match their manifest,
// newest first. A checkpoint that does not match (a torn write, a disk
// error) is skipped, so a resume falls back to the one before it.
func aiTrainCheckpoints(runDir string) []aiCheckpoint {
	root := filepath.Join(runDir, "checkpoints")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []aiCheckpoint
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) != 7 {
			continue
		}
		step, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		if aiCheckpointVerified(filepath.Join(root, name)) {
			out = append(out, aiCheckpoint{step: step, path: filepath.Join("checkpoints", name)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].step > out[j].step })
	return out
}

func aiCheckpointVerified(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return false
	}
	var m struct {
		Step  int               `json:"step"`
		Files map[string]string `json:"files"`
	}
	if json.Unmarshal(b, &m) != nil || len(m.Files) == 0 {
		return false
	}
	for name, want := range m.Files {
		if strings.Contains(name, "/") || strings.Contains(name, "..") {
			return false
		}
		got, err := aiFileSha256(filepath.Join(dir, name))
		if err != nil || got != want {
			return false
		}
	}
	return true
}

// Ai_trainCheckpoints : String -> Task Error (List Checkpoint)
func Ai_trainCheckpoints(runArg any) any {
	runDir := AsString(runArg)
	return func() any {
		var out []any
		for _, c := range aiTrainCheckpoints(runDir) {
			out = append(out, map[string]any{"step": c.step, "path": c.path})
		}
		if out == nil {
			out = []any{}
		}
		return Ok[any, any](out)
	}
}

// ─── cancel ─────────────────────────────────────────────────────────────

// Ai_trainCancel : String -> Task Error Bool
//
// Write the cancel file, then SIGTERM the trainer's process group (only when
// the process is still the trainer). The driver checkpoints at its next step
// and writes `cancelled`; a trainer still running after the grace period is
// killed. True when a live trainer was signalled.
func Ai_trainCancel(runArg any) any {
	runDir := AsString(runArg)
	return func() any {
		signalled, err := aiTrainCancel(runDir)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](signalled)
	}
}

func aiTrainCancel(runDir string) (bool, error) {
	if !fileExists(filepath.Join(runDir, aiTrainJobFile)) {
		return false, aiFailf(aiNotRegistered, "there is no training run at %s", runDir).with("ref", runDir)
	}
	if err := os.WriteFile(filepath.Join(runDir, aiTrainCancelFile), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return false, aiTrainFail("cannot write the cancel file: %v", err)
	}
	t, ok := aiReadTrainer(runDir)
	if !ok || !aiTrainerAlive(t) {
		return false, nil
	}
	_ = aiTrainSignal(t.Pid, syscall.SIGTERM)
	deadline := time.Now().Add(aiTrainCancelGrace)
	gone := false
	for time.Now().Before(deadline) {
		periodic.Guard("ai.train-cancel", periodicReport, func() error {
			gone = !aiTrainerAlive(t)
			return nil
		})
		if gone {
			return true, nil
		}
		time.Sleep(aiTrainPollEvery)
	}
	_ = aiTrainSignal(t.Pid, syscall.SIGKILL)
	return true, nil
}

// ─── reading a run ──────────────────────────────────────────────────────

const aiTrainReadLimit = 32 << 20

// Ai_trainRead : String -> Task Error RunState — everything progress needs:
// every protocol line so far, whether the trainer runs, and the finished
// run's record.
func Ai_trainRead(runArg any) any {
	runDir := AsString(runArg)
	return func() any {
		return Ok[any, any](aiTrainReadState(runDir))
	}
}

func aiTrainReadState(runDir string) map[string]any {
	exists := fileExists(filepath.Join(runDir, aiTrainJobFile))
	if exists {
		aiTrainNoteRun(runDir)
	}
	t, _ := aiReadTrainer(runDir)
	var offset int64
	if st, err := os.Stat(filepath.Join(runDir, aiTrainEventsFile)); err == nil && st.Size() > aiTrainReadLimit {
		offset = st.Size() - aiTrainReadLimit
	}
	lines, _, _ := aiTrainReadLines(runDir, offset, aiTrainReadLimit+1)
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line may be cut
	}
	out := make([]any, len(lines))
	for i, l := range lines {
		out[i] = l
	}
	result, _ := os.ReadFile(filepath.Join(runDir, aiTrainResultFile))
	_, launched := aiReadTrainer(runDir)
	alive := aiTrainerAlive(t)
	attempt := t.Attempt
	cloud := aiTrainReadCloud(runDir)
	if cloud.Job != "" {
		// A Vertex run: the job's state, as the last watch saw it, stands in
		// for the local trainer process.
		launched, alive, attempt = true, aiTrainCloudLive(cloud.State), cloud.Attempt
	}
	var size int64
	if st, err := os.Stat(filepath.Join(runDir, aiTrainEventsFile)); err == nil {
		size = st.Size()
	}
	return map[string]any{
		"exists":          exists,
		"launched":        launched,
		"converted":       fileExists(filepath.Join(runDir, "adapter.gguf")),
		"size":            int(size),
		"attemptOffset":   int(aiTrainAttemptOffset(runDir)),
		"generation":      aiTrainGeneration(runDir),
		"lines":           out,
		"alive":           alive,
		"attempt":         attempt,
		"pid":             t.Pid,
		"cancelRequested": fileExists(filepath.Join(runDir, aiTrainCancelFile)),
		"result":          string(bytes.TrimSpace(result)),
		"stderrTail":      aiTrainStderrTail(runDir),
	}
}

// aiTrainAttemptOffset is where the latest attempt's lines start in the
// events file: the offset of its last `hello` line (0 when there is none).
func aiTrainAttemptOffset(runDir string) int64 {
	b, err := os.ReadFile(filepath.Join(runDir, aiTrainEventsFile))
	if err != nil {
		return 0
	}
	var at, off int64
	for _, line := range bytes.SplitAfter(b, []byte{'\n'}) {
		if !bytes.HasSuffix(line, []byte{'\n'}) {
			break
		}
		if bytes.Contains(line, []byte(`"type":"hello"`)) {
			at = off
		}
		off += int64(len(line))
	}
	return at
}

const aiTrainGenerationFile = "generation"

// aiTrainGeneration counts the times a run was resumed (`Train.resume`). Each
// generation is its own durable run, so a failed or cancelled run can start
// again from its newest checkpoint.
func aiTrainGeneration(runDir string) int {
	b, err := os.ReadFile(filepath.Join(runDir, aiTrainGenerationFile))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// Ai_trainResume : String -> Task Error Int — start a finished, failed or
// cancelled run again: the next generation, with its result and its cancel
// file removed. A run whose trainer still runs is not resumed (InvalidSetting).
func Ai_trainResume(runArg any) any {
	runDir := AsString(runArg)
	return func() any {
		n, err := aiTrainResume(runDir)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](n)
	}
}

func aiTrainResume(runDir string) (int, error) {
	if !fileExists(filepath.Join(runDir, aiTrainJobFile)) {
		return 0, aiFailf(aiNotRegistered, "there is no training run at %s", runDir).with("ref", runDir)
	}
	if t, ok := aiReadTrainer(runDir); ok && aiTrainerAlive(t) {
		return 0, aiFailf(aiInvalidSetting, "the run at %s is still training (pid %d); cancel it first, or wait", runDir, t.Pid).with("setting", "run")
	}
	if c := aiTrainReadCloud(runDir); c.Job != "" && aiTrainCloudLive(c.State) {
		return 0, aiFailf(aiInvalidSetting, "the run at %s is still training on Vertex AI (%s); cancel it first, or wait", runDir, c.State).with("setting", "run")
	}
	n := aiTrainGeneration(runDir) + 1
	if err := aiWriteFileAtomic(filepath.Join(runDir, aiTrainGenerationFile), []byte(strconv.Itoa(n)+"\n")); err != nil {
		return 0, aiTrainFail("cannot write %s: %v", aiTrainGenerationFile, err)
	}
	os.Remove(filepath.Join(runDir, aiTrainResultFile))
	os.Remove(filepath.Join(runDir, aiTrainCancelFile))
	return n, nil
}

// Ai_trainRuns : String -> Task Error (List RunEntry) — the runs in a work
// directory, newest first.
func Ai_trainRuns(workArg any) any {
	work := AsString(workArg)
	return func() any {
		out := []any{}
		for _, r := range aiTrainRunsIn(work) {
			out = append(out, map[string]any{"runId": r.id, "dir": r.dir, "modifiedMs": int(r.modified)})
		}
		return Ok[any, any](out)
	}
}

type aiTrainRunEntry struct {
	id, dir  string
	modified int64
}

func aiTrainRunsIn(work string) []aiTrainRunEntry {
	entries, err := os.ReadDir(work)
	if err != nil {
		return nil
	}
	var out []aiTrainRunEntry
	for _, e := range entries {
		dir := filepath.Join(work, e.Name())
		if !e.IsDir() || !fileExists(filepath.Join(dir, aiTrainJobFile)) {
			continue
		}
		mod := int64(0)
		for _, f := range []string{aiTrainEventsFile, aiTrainResultFile, aiTrainJobFile} {
			if st, err := os.Stat(filepath.Join(dir, f)); err == nil && st.ModTime().UnixMilli() > mod {
				mod = st.ModTime().UnixMilli()
			}
		}
		out = append(out, aiTrainRunEntry{id: e.Name(), dir: dir, modified: mod})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].modified != out[j].modified {
			return out[i].modified > out[j].modified
		}
		return out[i].id < out[j].id
	})
	return out
}

// Ai_trainFinish : String -> String -> Task Error () — write the finished
// run's record (result.json).
func Ai_trainFinish(runArg, resultArg any) any {
	runDir, result := AsString(runArg), AsString(resultArg)
	return func() any {
		if err := aiWriteFileAtomic(filepath.Join(runDir, aiTrainResultFile), []byte(result+"\n")); err != nil {
			return aiTaskErr(aiTrainFail("cannot write %s: %v", aiTrainResultFile, err))
		}
		return Ok[any, any](struct{}{})
	}
}

// ─── test mode ──────────────────────────────────────────────────────────

// aiTrainSimulate writes what a Fake attempt writes, synchronously: the
// protocol lines, verified checkpoints, and (when the attempt finishes) a
// rank-`rank` PEFT adapter with zero B matrices. It honours fakeCrashAt on the
// first attempt (no terminal line, as a killed trainer leaves it) and the
// cancel file.
func aiTrainSimulate(runDir string, cfg map[string]any) error {
	ev, err := os.OpenFile(filepath.Join(runDir, aiTrainEventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return aiTrainFail("cannot open %s: %v", aiTrainEventsFile, err)
	}
	defer ev.Close()
	emit := func(typ string, fields map[string]any) {
		fields["v"] = 1
		fields["type"] = typ
		b, _ := json.Marshal(fields)
		ev.Write(append(b, '\n'))
	}
	nTrain, nValid := aiCountLines(filepath.Join(runDir, "data", "train.jsonl")), aiCountLines(filepath.Join(runDir, "data", "valid.jsonl"))
	attempt := aiJNum(cfg, "attempt", 0)
	start := aiJNum(cfg, "resumeFrom", 0)
	if nTrain == 0 || nValid == 0 {
		emit("error", map[string]any{"code": "DatasetInvalid", "message": "the train or eval split is empty"})
		return nil
	}
	batch := aiJMap(cfg, "batch")
	eff := aiJNum(batch, "perDevice", 4) * aiJNum(batch, "accumulate", 1)
	if eff < 1 {
		eff = 1
	}
	total := aiJNum(cfg, "maxSteps", 0)
	if total <= 0 {
		epochs := 1.0
		if f, err := strconv.ParseFloat(fmt.Sprint(cfg["epochs"]), 64); err == nil {
			epochs = f
		}
		total = int(math.Ceil(epochs * float64(nTrain) / float64(eff)))
	}
	if total < 1 {
		total = 1
	}
	seed := aiJNum(cfg, "seed", 0)
	loss := func(k int) float64 {
		frac := float64(k) / float64(total)
		return 0.45 + 1.6*math.Exp(-3*frac) + 0.02*math.Sin(float64(k)*0.7+float64(seed))
	}
	emit("hello", map[string]any{"backend": "fake", "toolchain": "test-mode", "versions": map[string]any{"simulated": "1"},
		"attempt": attempt, "resumeFrom": start, "totalSteps": total, "pid": 0})
	every, evalEvery, crash := aiJNum(cfg, "checkpointEvery", 0), aiJNum(cfg, "evalEvery", 0), aiJNum(cfg, "fakeCrashAt", 0)
	cancelled := func() bool { return fileExists(filepath.Join(runDir, aiTrainCancelFile)) }
	lastCk := start
	epochLen := (nTrain + eff - 1) / eff
	for step := start; step < total; step++ {
		if cancelled() {
			if step > lastCk {
				if err := aiTrainSimCheckpoint(runDir, step); err != nil {
					return err
				}
				emit("checkpoint", map[string]any{"step": step, "path": filepath.Join("checkpoints", fmt.Sprintf("%07d", step))})
			}
			emit("cancelled", map[string]any{"step": step})
			return nil
		}
		done := step + 1
		emit("progress", map[string]any{"step": done, "total": total, "loss": loss(done), "learningRate": cfg["learningRate"],
			"tokensPerSecond": 1000.0, "epoch": step / max(1, epochLen)})
		if evalEvery > 0 && done%evalEvery == 0 && done < total {
			emit("metric", map[string]any{"step": done, "name": "val_loss", "value": loss(done) + 0.08})
		}
		if every > 0 && done%every == 0 && done < total {
			if err := aiTrainSimCheckpoint(runDir, done); err != nil {
				return err
			}
			emit("checkpoint", map[string]any{"step": done, "path": filepath.Join("checkpoints", fmt.Sprintf("%07d", done))})
			lastCk = done
		}
		if crash > 0 && attempt == 0 && done == crash {
			return nil // the simulated crash: no terminal line
		}
	}
	emit("metric", map[string]any{"step": total, "name": "val_loss", "value": loss(total) + 0.08})
	sum, err := aiTrainSimAdapter(runDir, cfg)
	if err != nil {
		return err
	}
	emit("artefact", map[string]any{"kind": "peft", "path": "adapter", "sha256": sum})
	emit("done", map[string]any{"step": total, "trainLoss": loss(total), "valLoss": loss(total) + 0.08})
	return nil
}

func aiCountLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(l)) > 0 {
			n++
		}
	}
	return n
}

func aiTrainSimCheckpoint(runDir string, step int) error {
	dir := filepath.Join(runDir, "checkpoints", fmt.Sprintf("%07d", step))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return aiTrainFail("cannot write a checkpoint: %v", err)
	}
	for name, data := range aiTrainSimCheckpointFiles(step) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return aiTrainFail("cannot write a checkpoint: %v", err)
		}
	}
	return nil
}

// aiTrainSimCheckpointFiles is a Fake checkpoint's files, manifest.json
// included (the digests of the other three).
func aiTrainSimCheckpointFiles(step int) map[string][]byte {
	files := map[string][]byte{}
	files["adapter.safetensors"] = aiSafetensorsBytes(map[string]aiTensor{"fake.step": {shape: []int{1}, data: []float32{float32(step)}}}, nil)
	files["optimizer.safetensors"] = aiSafetensorsBytes(map[string]aiTensor{"fake.m": {shape: []int{1}, data: []float32{0.5}}}, nil)
	files["state.json"] = []byte(fmt.Sprintf(`{"step":%d}`, step))
	sums := map[string]string{}
	for name, data := range files {
		sums[name] = aiSha256Hex(data)
	}
	m, _ := json.Marshal(map[string]any{"step": step, "files": sums})
	files["manifest.json"] = m
	return files
}

func aiSha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// aiTrainSimAdapter writes the Fake backend's adapter: rank `rank` A matrices
// with small values, zero B matrices, for the targets and layers of the job
// over the shapes driver.json carries.
func aiTrainSimAdapter(runDir string, cfg map[string]any) (string, error) {
	out := filepath.Join(runDir, "adapter")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", aiTrainFail("cannot write the adapter: %v", err)
	}
	weights, conf := aiTrainSimAdapterFiles(cfg)
	if err := os.WriteFile(filepath.Join(out, "adapter_model.safetensors"), weights, 0o644); err != nil {
		return "", aiTrainFail("cannot write the adapter: %v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "adapter_config.json"), conf, 0o644); err != nil {
		return "", aiTrainFail("cannot write the adapter: %v", err)
	}
	return aiSha256Hex(weights), nil
}

// aiTrainSimAdapterFiles is the Fake adapter's two files: the PEFT weights
// and adapter_config.json.
func aiTrainSimAdapterFiles(cfg map[string]any) ([]byte, []byte) {
	shapes := aiJMap(cfg, "shapes")
	s := aiTrainShape{layers: aiJNum(shapes, "layers", 2), hidden: aiJNum(shapes, "hidden", 8), intermediate: aiJNum(shapes, "intermediate", 16),
		qDim: aiJNum(shapes, "qDim", 8), kv: aiJNum(shapes, "kvDim", 8)}
	rank := aiJNum(cfg, "rank", 8)
	want := aiJNum(cfg, "layers", 16)
	if want < 0 || want > s.layers {
		want = s.layers
	}
	var targets []string
	if ts, ok := cfg["targets"].([]any); ok {
		for _, t := range ts {
			if str, ok := t.(string); ok {
				targets = append(targets, str)
			}
		}
	}
	tensors := map[string]aiTensor{}
	for layer := s.layers - want; layer < s.layers; layer++ {
		for _, t := range targets {
			in, outDim, ok := aiTargetDims(t, s)
			if !ok {
				continue
			}
			base := fmt.Sprintf("base_model.model.model.layers.%d.%s", layer, t)
			a := make([]float32, rank*in)
			for i := range a {
				a[i] = float32((i%7)-3) / 1000
			}
			tensors[base+".lora_A.weight"] = aiTensor{shape: []int{rank, in}, data: a}
			tensors[base+".lora_B.weight"] = aiTensor{shape: []int{outDim, rank}, data: make([]float32, outDim*rank)}
		}
	}
	conf, _ := json.Marshal(map[string]any{"peft_type": "LORA", "r": rank, "lora_alpha": aiJNum(cfg, "alpha", 2*rank),
		"target_modules": targets, "base_model_name_or_path": aiJStr(aiJMap(cfg, "base"), "id"), "task_type": "CAUSAL_LM", "bias": "none"})
	return aiSafetensorsBytes(tensors, map[string]string{"format": "pt"}), conf
}

// aiTargetDims is a projection's (in, out) from the base's shape.
func aiTargetDims(target string, s aiTrainShape) (int, int, bool) {
	name := target
	if i := strings.LastIndexByte(target, '.'); i >= 0 {
		name = target[i+1:]
	}
	switch name {
	case "q_proj":
		return s.hidden, s.qDim, true
	case "k_proj", "v_proj":
		return s.hidden, s.kv, true
	case "o_proj":
		return s.qDim, s.hidden, true
	case "gate_proj", "up_proj":
		return s.hidden, s.intermediate, true
	case "down_proj":
		return s.intermediate, s.hidden, true
	}
	return 0, 0, false
}

// ─── cloud runs ─────────────────────────────────────────────────────────

// aiTrainCloudFile is a Vertex run's state as its last watch saw it, written
// by Train.sky: the job, its state, the attempt, the staging location and the
// spend so far. Its presence makes the run a cloud run for progress, the
// console, cancel and resume.
const aiTrainCloudFile = "cloud.json"

type aiTrainCloud struct {
	Backend    string `json:"backend"`
	Job        string `json:"job"`
	State      string `json:"state"`
	Attempt    int    `json:"attempt"`
	Staging    string `json:"staging"`
	RunKey     string `json:"runKey"`
	Project    string `json:"project"`
	Region     string `json:"region"`
	Machine    string `json:"machine"`
	Spent      string `json:"spent"`
	Budget     string `json:"budget"`
	UpdatedMs  int64  `json:"updatedMs"`
	SpentMinor int    `json:"spentMinor"`
}

func aiTrainReadCloud(runDir string) aiTrainCloud {
	var c aiTrainCloud
	if b, err := os.ReadFile(filepath.Join(runDir, aiTrainCloudFile)); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

// aiTrainCloudLive: a Vertex job in one of these states may still train (and
// cost money).
func aiTrainCloudLive(state string) bool {
	switch state {
	case "JOB_STATE_QUEUED", "JOB_STATE_PENDING", "JOB_STATE_RUNNING", "JOB_STATE_CANCELLING", "JOB_STATE_UPDATING", "JOB_STATE_PAUSED":
		return true
	}
	return false
}

// Ai_trainCloudResolve : Code -> Task Error (List Setting) — the Vertex
// settings, each through the shared resolver (operator env > the job's
// `withX` value > default), in a fixed order: PROJECT, REGION, MACHINE,
// STAGING, BUDGET, IMAGE, SERVICE_ACCOUNT, LABELS. A setting nobody set is "" with
// source "default"; Train.sky names the builder and the variable it needs.
func Ai_trainCloudResolve(codeArg any) any {
	code := map[string]string{
		"PROJECT":         aiStrField(codeArg, "Project", "project"),
		"REGION":          aiStrField(codeArg, "Region", "region"),
		"MACHINE":         aiStrField(codeArg, "Machine", "machine"),
		"STAGING":         aiStrField(codeArg, "Staging", "staging"),
		"BUDGET":          aiStrField(codeArg, "Budget", "budget"),
		"IMAGE":           aiStrField(codeArg, "Image", "image"),
		"SERVICE_ACCOUNT": aiStrField(codeArg, "ServiceAccount", "serviceAccount"),
		"LABELS":          aiStrField(codeArg, "Labels", "labels"),
	}
	return func() any {
		out := []any{}
		for _, key := range []string{"PROJECT", "REGION", "MACHINE", "STAGING", "BUDGET", "IMAGE", "SERVICE_ACCOUNT", "LABELS"} {
			v, src := aiTrainCloudLayer(key, code[key])
			out = append(out, map[string]any{"key": key, "name": skyEnvName(aiTrainSuffix(key)), "value": strings.TrimSpace(v), "source": src})
		}
		return Ok[any, any](out)
	}
}

// aiTrainCloudLayer resolves one SKY_AI_TRAIN_* setting. Each suffix is a
// literal, so the configuration census sees every variable.
func aiTrainCloudLayer(key, code string) (string, string) {
	switch key {
	case "PROJECT":
		return aiLayer("AI_TRAIN_PROJECT", code, nil)
	case "REGION":
		return aiLayer("AI_TRAIN_REGION", code, nil)
	case "MACHINE":
		return aiLayer("AI_TRAIN_MACHINE", code, nil)
	case "STAGING":
		return aiLayer("AI_TRAIN_STAGING", code, nil)
	case "BUDGET":
		return aiLayer("AI_TRAIN_BUDGET", code, nil)
	case "IMAGE":
		return aiLayer("AI_TRAIN_IMAGE", code, nil)
	case "SERVICE_ACCOUNT":
		return aiLayer("AI_TRAIN_SERVICE_ACCOUNT", code, nil)
	case "LABELS":
		return aiLayer("AI_TRAIN_LABELS", code, nil)
	}
	return "", "default"
}

// aiTrainSuffix is the env suffix of a cloud training setting, for the name
// `describe` shows (the read itself is aiLayer's).
func aiTrainSuffix(key string) string { return "AI_TRAIN_" + key }
