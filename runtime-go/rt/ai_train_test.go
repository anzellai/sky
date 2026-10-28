//go:build !js

package rt

// Tests of ai_train.go. The trainer is the Fake trainer the runtime itself
// runs (ai_fake_trainer.go): the launcher starts this test binary again with
// --sky-ai-fake-trainer, exactly as a Sky program starts itself, so the
// process handling, the protocol file, checkpoints, cancel and resume are
// exercised for real and in seconds. The Unsloth driver (Python) has its own
// tests (testdata/train_driver_tests).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func fakeTrainer(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the trainer's process group handling is Unix")
	}
	saved := aiTrainLauncher
	// The trainer runs detached in a session of its own (aiTrainSysAttr), so
	// it outlives the test by design; a test that fails mid-run (or a
	// "hang" / "partial" trainer) would leave it running for up to an hour.
	// Every trainer this test launches is ended with the test.
	var launchedMu sync.Mutex
	var launched []*exec.Cmd
	aiTrainLauncher = func(cfg map[string]any, runDir, token string) (*exec.Cmd, error) {
		c, err := saved(cfg, runDir, token)
		if err != nil {
			return nil, err
		}
		launchedMu.Lock()
		launched = append(launched, c)
		launchedMu.Unlock()
		return c, nil
	}
	t.Cleanup(func() {
		launchedMu.Lock()
		defer launchedMu.Unlock()
		for _, c := range launched {
			if c.Process != nil && processAlive(c.Process.Pid) {
				_ = procSignalGroup(c.Process, syscall.SIGKILL)
			}
		}
	})
	savedGrace, savedPoll := aiTrainCancelGrace, aiTrainPollEvery
	aiTrainPollEvery = 20 * time.Millisecond
	t.Cleanup(func() {
		aiTrainLauncher = saved
		aiTrainCancelGrace, aiTrainPollEvery = savedGrace, savedPoll
	})
}

// trainRun makes a staged run directory: the job and both splits.
func trainRun(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, aiTrainJobFile), []byte(`{"name":"t"}`), 0o644)
	var rows strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&rows, `{"messages":[{"role":"user","content":"q%d"},{"role":"assistant","content":"a%d"}]}`+"\n", i, i)
	}
	os.WriteFile(filepath.Join(dir, "data", "train.jsonl"), []byte(rows.String()), 0o644)
	os.WriteFile(filepath.Join(dir, "data", "valid.jsonl"), []byte(rows.String()), 0o644)
	return dir
}

func driverJSON(t *testing.T, over map[string]any) string {
	t.Helper()
	cfg := map[string]any{
		"backend": "fake", "attempt": 0, "resumeFrom": 0, "maxSteps": 6, "checkpointEvery": 2,
		"fakeStepMs": 5, "seed": 1, "learningRate": 0.0001,
		"batch": map[string]any{"perDevice": 1, "accumulate": 1}, "rank": 2, "alpha": 4, "layers": 2,
		"targets": []any{"self_attn.q_proj", "self_attn.v_proj"},
		"shapes":  map[string]any{"layers": 2, "hidden": 8, "intermediate": 16, "qDim": 8, "kvDim": 4},
		"base":    map[string]any{"id": "test/base"},
	}
	for k, v := range over {
		cfg[k] = v
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

func watchToEnd(t *testing.T, dir string) ([]string, int64) {
	t.Helper()
	var all []string
	var off int64
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := aiTrainWatch(dir, off, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range v["lines"].([]any) {
			all = append(all, l.(string))
		}
		off = int64(v["offset"].(int))
		if !v["alive"].(bool) {
			return all, off
		}
	}
	t.Fatal("the trainer did not end")
	return nil, 0
}

func eventTypes(lines []string) []string {
	var out []string
	for _, l := range lines {
		var ev struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(l), &ev)
		out = append(out, ev.Type)
	}
	return out
}

func TestTrainReadLinesKeepsAPartialLineForLater(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, aiTrainEventsFile), []byte("{\"type\":\"hello\"}\n{\"type\":\"progress\"}\n{\"type\":\"pro"), 0o644)
	lines, off, err := aiTrainReadLines(dir, 0, 1<<20)
	if err != nil || len(lines) != 2 || off != int64(len("{\"type\":\"hello\"}\n{\"type\":\"progress\"}\n")) {
		t.Fatalf("%v %d %v", lines, off, err)
	}
	f, _ := os.OpenFile(filepath.Join(dir, aiTrainEventsFile), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("gress\"}\n")
	f.Close()
	lines, off2, _ := aiTrainReadLines(dir, off, 1<<20)
	if len(lines) != 1 || lines[0] != `{"type":"progress"}` || off2 <= off {
		t.Fatalf("%v %d", lines, off2)
	}
	lines, off3, _ := aiTrainReadLines(dir, off2, 1<<20)
	if len(lines) != 0 || off3 != off2 {
		t.Fatalf("%v %d", lines, off3)
	}
}

func TestTrainLaunchRunsTheTrainerAndASecondLaunchAttaches(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	v, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"fakeStepMs": 50}))
	if err != nil {
		t.Fatal(err)
	}
	if v["attached"].(bool) || v["pid"].(int) <= 0 {
		t.Fatalf("first launch %v", v)
	}
	again, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"fakeStepMs": 50}))
	if err != nil || !again["attached"].(bool) || again["pid"] != v["pid"] {
		t.Fatalf("second launch %v %v", again, err)
	}
	lines, _ := watchToEnd(t, dir)
	types := eventTypes(lines)
	if types[0] != "hello" || types[len(types)-1] != "done" {
		t.Fatalf("events %v", types)
	}
	if strings.Count(strings.Join(types, " "), "hello") != 1 {
		t.Fatalf("the second launch started a second trainer: %v", types)
	}
	// A finished run is not relaunched by the same attempt.
	if v, _ := aiTrainLaunch(dir, driverJSON(t, nil)); !v["attached"].(bool) {
		t.Fatal("a done attempt launched again")
	}
}

func TestTrainWatchEndsOnATerminalLine(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	if _, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"fakeMode": "error"})); err != nil {
		t.Fatal(err)
	}
	lines, _ := watchToEnd(t, dir)
	if got := eventTypes(lines); got[len(got)-1] != "error" {
		t.Fatalf("%v", got)
	}
}

func TestTrainWatchDoesNotConsumeAPartialLine(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	if _, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"fakeMode": "partial"})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { aiTrainCancelGrace = 100 * time.Millisecond; aiTrainCancel(dir) })
	v, err := aiTrainWatch(dir, 0, 600*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	lines := v["lines"].([]any)
	if len(lines) != 1 || !v["alive"].(bool) {
		t.Fatalf("%v alive=%v", lines, v["alive"])
	}
	hello := len(lines[0].(string)) + 1
	if v["offset"].(int) != hello {
		t.Fatalf("offset %d, want %d (the partial line stays unread)", v["offset"], hello)
	}
}

// The harness gate `ai-train-resume` runs this test and reads its
// `ASSERTIONS:` line. Its falsifying mutation sorts the checkpoints oldest
// first, so a relaunch would repeat work from the first checkpoint instead of
// resuming from the newest verified one.
func TestTrainResumesFromNewestVerifiedCheckpoint(t *testing.T) {
	fakeTrainer(t)
	n := 0
	check := func(ok bool, format string, args ...any) {
		t.Helper()
		n++
		if !ok {
			t.Fatalf(format, args...)
		}
	}
	dir := trainRun(t)
	_, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"maxSteps": 10, "checkpointEvery": 2, "fakeCrashAt": 7}))
	check(err == nil, "launch %v", err)
	lines, _ := watchToEnd(t, dir)
	types := eventTypes(lines)
	check(types[len(types)-1] == "progress", "the crashed attempt ended with %v", types)
	cks := aiTrainCheckpoints(dir)
	check(len(cks) == 3 && cks[0].step == 6 && cks[1].step == 4 && cks[2].step == 2, "checkpoints %+v, want 6, 4, 2 (newest first)", cks)
	// A torn newest checkpoint is skipped: the resume falls back to 4.
	os.WriteFile(filepath.Join(dir, "checkpoints", "0000006", "optimizer.safetensors"), []byte("torn"), 0o644)
	cks = aiTrainCheckpoints(dir)
	check(len(cks) == 2 && cks[0].step == 4 && cks[0].path == filepath.Join("checkpoints", "0000004"), "checkpoints after corruption %+v", cks)
	v, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"maxSteps": 10, "checkpointEvery": 2, "fakeCrashAt": 7,
		"attempt": 1, "resumeFrom": cks[0].step, "resumeDir": cks[0].path}))
	check(err == nil && !v["attached"].(bool) && v["resumeFrom"].(int) == 4, "relaunch %v %v", v, err)
	lines, _ = watchToEnd(t, dir)
	var hello struct {
		ResumeFrom int `json:"resumeFrom"`
	}
	for _, l := range lines {
		if strings.Contains(l, `"hello"`) {
			json.Unmarshal([]byte(l), &hello)
		}
	}
	check(hello.ResumeFrom == 4, "the relaunch resumed from %d", hello.ResumeFrom)
	got := eventTypes(lines)
	check(got[len(got)-1] == "done", "the resumed attempt ended with %v", got)
	var drv map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, aiTrainDriverFile))
	json.Unmarshal(b, &drv)
	check(drv["resumeDir"] == filepath.Join("checkpoints", "0000004"), "driver.json %v", drv)
	fmt.Printf("ASSERTIONS: %d\n", n)
}

func TestTrainCancelCheckpointsThenStops(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	if _, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"maxSteps": 100000, "checkpointEvery": 0, "fakeStepMs": 20})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if lines, _, _ := aiTrainReadLines(dir, 0, 1<<20); len(lines) > 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	signalled, err := aiTrainCancel(dir)
	if err != nil || !signalled {
		lines, _, _ := aiTrainReadLines(dir, 0, 1<<20)
		tr, _ := aiReadTrainer(dir)
		t.Fatalf("cancel %v %v; trainer %+v; events %v", signalled, err, tr, lines)
	}
	lines, _ := watchToEnd(t, dir)
	types := eventTypes(lines)
	if types[len(types)-1] != "cancelled" || types[len(types)-2] != "checkpoint" {
		t.Fatalf("events %v", types)
	}
	if !fileExists(filepath.Join(dir, aiTrainCancelFile)) {
		t.Fatal("no cancel file")
	}
}

// A trainer that sees the cancel file and stops before the cancel looks at it
// was still a live trainer the cancel stopped: the answer is True. The hook
// holds the cancel until the trainer has exited, the window CI hit.
func TestTrainCancelOfATrainerThatStopsAtOnceIsSignalled(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	if _, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"maxSteps": 100000, "checkpointEvery": 0, "fakeStepMs": 20})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if lines, _, _ := aiTrainReadLines(dir, 0, 1<<20); len(lines) > 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	tr, ok := aiReadTrainer(dir)
	if !ok || !aiTrainerAlive(tr) {
		t.Fatalf("the trainer is not running: %+v", tr)
	}
	saved := aiTrainCancelWritten
	t.Cleanup(func() { aiTrainCancelWritten = saved })
	aiTrainCancelWritten = func(string) {
		for i := 0; i < 500 && aiTrainerAlive(tr); i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}
	signalled, err := aiTrainCancel(dir)
	if aiTrainerAlive(tr) {
		t.Fatal("the trainer did not stop on the cancel file")
	}
	if err != nil || !signalled {
		t.Fatalf("a live trainer that stopped at once: cancel %v %v", signalled, err)
	}
}

func TestTrainCancelKillsATrainerThatIgnoresSIGTERM(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	if _, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"maxSteps": 100000, "fakeMode": "hang", "fakeStepMs": 20})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	aiTrainCancelGrace = 500 * time.Millisecond
	tr, _ := aiReadTrainer(dir)
	start := time.Now()
	if signalled, err := aiTrainCancel(dir); err != nil || !signalled {
		t.Fatalf("cancel %v %v", signalled, err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("cancel returned before the grace period")
	}
	for i := 0; i < 100 && aiTrainerAlive(tr); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if aiTrainerAlive(tr) {
		t.Fatal("the trainer survived SIGKILL")
	}
}

// A trainer.json whose pid now belongs to another process (here: this test)
// is not the trainer: it is not attached to and it is never signalled.
func TestTrainRecycledPidIsNotTheTrainer(t *testing.T) {
	dir := trainRun(t)
	tr := aiTrainer{Pid: os.Getpid(), Launch: "sky-train-0123456789abcdef", Attempt: 0}
	b, _ := json.Marshal(tr)
	os.WriteFile(filepath.Join(dir, aiTrainTrainerFile), b, 0o644)
	if aiTrainerAlive(tr) {
		t.Fatal("a recycled pid was taken for the trainer")
	}
	signalled, err := aiTrainCancel(dir)
	if err != nil || signalled {
		t.Fatalf("cancel signalled a stranger: %v %v", signalled, err)
	}
	state := aiTrainReadState(dir)
	if state["alive"].(bool) {
		t.Fatal("read state says alive")
	}
}

func TestTrainCancelOfNoRunIsNotRegistered(t *testing.T) {
	_, err := aiTrainCancel(t.TempDir())
	if f, ok := err.(*aiFailure); !ok || f.code != aiNotRegistered {
		t.Fatalf("%v", err)
	}
}

func TestTrainBackendPrecedence(t *testing.T) {
	t.Setenv("SKY_AI_TRAIN_BACKEND", "")
	b, src, err := aiTrainResolveBackend("fake")
	if err != nil || b != "fake" || src != "code" {
		t.Fatalf("code: %s %s %v", b, src, err)
	}
	t.Setenv("SKY_AI_TRAIN_BACKEND", "fake")
	b, src, err = aiTrainResolveBackend("")
	if err != nil || b != "fake" || src != "env" {
		t.Fatalf("env: %s %s %v", b, src, err)
	}
	t.Setenv("SKY_AI_TRAIN_BACKEND", "fake")
	b, src, err = aiTrainResolveBackend("mlx")
	if err != nil || b != "fake" || src != "env" {
		t.Fatalf("env over code: %s %s %v", b, src, err)
	}
	t.Setenv("SKY_AI_TRAIN_BACKEND", "tpu")
	_, _, err = aiTrainResolveBackend("fake")
	if f, ok := err.(*aiFailure); !ok || f.code != aiInvalidSetting || !strings.Contains(f.msg, "SKY_AI_TRAIN_BACKEND") {
		t.Fatalf("bad value: %v", err)
	}
	t.Setenv("SKY_AI_TRAIN_BACKEND", "vertex")
	if b, src, err := aiTrainResolveBackend(""); err != nil || b != "vertex" || src != "env" {
		t.Fatalf("vertex: %s %s %v", b, src, err)
	}
	t.Setenv("SKY_AI_TRAIN_BACKEND", "sagemaker")
	_, _, err = aiTrainResolveBackend("")
	if f, ok := err.(*aiFailure); !ok || f.code != aiInvalidSetting || !strings.Contains(f.msg, "not supported yet") {
		t.Fatalf("sagemaker: %v", err)
	}
	t.Setenv("SKY_AI_TRAIN_BACKEND", "")
	if runtime.GOOS == "darwin" {
		_, _, err = aiTrainResolveBackend("unsloth")
		if f, ok := err.(*aiFailure); !ok || f.code != aiNoAccelerator {
			t.Fatalf("unsloth on macOS: %v", err)
		}
		if v, _ := f2(err).attrValue("wanted"); v != "cuda" {
			t.Fatalf("wanted %q", v)
		}
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		if b, src, err := aiTrainResolveBackend(""); err != nil || b != "mlx" || src != "detected" {
			t.Fatalf("detect: %s %s %v", b, src, err)
		}
	}
	if runtime.GOOS == "linux" && !aiHasCUDA() {
		_, _, err = aiTrainResolveBackend("")
		if f, ok := err.(*aiFailure); !ok || f.code != aiNoAccelerator || !strings.Contains(f.msg, "fake") {
			t.Fatalf("no accelerator: %v", err)
		}
		_, _, err = aiTrainResolveBackend("mlx")
		if f, ok := err.(*aiFailure); !ok || f.code != aiNoAccelerator {
			t.Fatalf("mlx on linux: %v", err)
		}
	}
}

func f2(err error) *aiFailure { f, _ := err.(*aiFailure); return f }

func TestTrainStageSplitsAndChecksTheDigest(t *testing.T) {
	src := t.TempDir()
	blob := filepath.Join(src, "data.jsonl")
	body := `{"row":{"prompt":"a","completion":"b"},"split":"train"}` + "\n" +
		`{"row":{"prompt":"c","completion":"d"},"split":"eval"}` + "\n" +
		`{"row":{"prompt":"e","completion":"f"},"split":"test"}` + "\n" +
		`{"row":{"prompt":"g","completion":"h"},"split":"train"}` + "\n"
	os.WriteFile(blob, []byte(body), 0o644)
	sum, _ := aiFileSha256(blob)
	run := filepath.Join(t.TempDir(), "run-1")
	v, err := aiTrainStage(run, blob, sum, `{"job":1}`)
	if err != nil || v["train"] != 2 || v["eval"] != 1 {
		t.Fatalf("%v %v", v, err)
	}
	train, _ := os.ReadFile(filepath.Join(run, "data", "train.jsonl"))
	if string(train) != `{"prompt":"a","completion":"b"}`+"\n"+`{"prompt":"g","completion":"h"}`+"\n" {
		t.Fatalf("train.jsonl %q", train)
	}
	valid, _ := os.ReadFile(filepath.Join(run, "data", "valid.jsonl"))
	if string(valid) != `{"prompt":"c","completion":"d"}`+"\n" {
		t.Fatalf("valid.jsonl %q", valid)
	}
	// Staging again is a no-op, even after the blob is gone.
	os.Remove(blob)
	if v, err := aiTrainStage(run, blob, sum, `{"job":1}`); err != nil || v["train"] != 2 {
		t.Fatalf("restage %v %v", v, err)
	}
	// A changed version is ChecksumMismatch.
	os.WriteFile(blob, []byte(body+"\n"), 0o644)
	_, err = aiTrainStage(filepath.Join(t.TempDir(), "run-2"), blob, sum, `{}`)
	if f, ok := err.(*aiFailure); !ok || f.code != aiChecksumMismatch {
		t.Fatalf("%v", err)
	}
	// A line that is not a frozen row is DatasetInvalid, naming the line.
	os.WriteFile(blob, []byte("not json\n"), 0o644)
	bad, _ := aiFileSha256(blob)
	_, err = aiTrainStage(filepath.Join(t.TempDir(), "run-3"), blob, bad, `{}`)
	if f, ok := err.(*aiFailure); !ok || f.code != aiDatasetInvalid || !strings.Contains(f.msg, "line 1") {
		t.Fatalf("%v", err)
	}
}

func withTrainTestMode(t *testing.T) {
	t.Helper()
	initTestMode()
	saved := testModeOn
	testModeOn = true
	t.Cleanup(func() { testModeOn = saved })
}

func TestTrainTestModeSimulatesARunOfflineWithCrashAndResume(t *testing.T) {
	withTrainTestMode(t)
	dir := trainRun(t)
	os.WriteFile(filepath.Join(dir, "data", "train.jsonl"), []byte("{}\n{}\n{}\n{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "data", "valid.jsonl"), []byte("{}\n"), 0o644)
	cfg := map[string]any{"attempt": 0, "maxSteps": 8, "checkpointEvery": 3, "evalEvery": 4, "fakeCrashAt": 5, "seed": 1,
		"rank": 2, "alpha": 4, "layers": 2, "targets": []string{"self_attn.q_proj", "self_attn.v_proj"},
		"shapes":  map[string]any{"layers": 4, "hidden": 8, "intermediate": 16, "qDim": 8, "kvDim": 4},
		"batch":   map[string]any{"perDevice": 2, "accumulate": 1},
		"base":    map[string]any{"id": "test@base"},
		"backend": "fake"}
	b, _ := json.Marshal(cfg)
	v, err := aiTrainLaunch(dir, string(b))
	if err != nil || v["pid"].(int) != 0 {
		t.Fatalf("%v %v", v, err)
	}
	w, _ := aiTrainWatch(dir, 0, time.Second)
	if w["alive"].(bool) {
		t.Fatal("a simulated trainer is alive")
	}
	types := []string{}
	for _, l := range w["lines"].([]any) {
		types = append(types, eventTypes([]string{l.(string)})[0])
	}
	if strings.Join(types, ",") != "hello,progress,progress,progress,checkpoint,progress,metric,progress" {
		t.Fatalf("crashed attempt %v", types)
	}
	cks := aiTrainCheckpoints(dir)
	if len(cks) != 1 || cks[0].step != 3 {
		t.Fatalf("%+v", cks)
	}
	cfg["attempt"], cfg["resumeFrom"], cfg["resumeDir"] = 1, 3, cks[0].path
	b, _ = json.Marshal(cfg)
	if _, err := aiTrainLaunch(dir, string(b)); err != nil {
		t.Fatal(err)
	}
	w2, _ := aiTrainWatch(dir, int64(w["offset"].(int)), time.Second)
	lines := w2["lines"].([]any)
	last := lines[len(lines)-1].(string)
	if !strings.Contains(last, `"done"`) {
		t.Fatalf("resumed attempt ended with %s", last)
	}
	adapter, err := aiReadSafetensors(filepath.Join(dir, "adapter", "adapter_model.safetensors"))
	if err != nil || len(adapter) != 8 {
		t.Fatalf("adapter %d tensors %v", len(adapter), err)
	}
	a := adapter["base_model.model.model.layers.3.self_attn.v_proj.lora_A.weight"]
	bm := adapter["base_model.model.model.layers.3.self_attn.v_proj.lora_B.weight"]
	if fmt.Sprint(a.shape) != "[2 8]" || fmt.Sprint(bm.shape) != "[4 2]" {
		t.Fatalf("shapes %v %v", a.shape, bm.shape)
	}
	for _, x := range bm.data {
		if x != 0 {
			t.Fatal("the Fake adapter's B is not zero")
		}
	}
}

func TestTrainRunsListsNewestFirst(t *testing.T) {
	work := t.TempDir()
	for i, id := range []string{"a", "b"} {
		d := filepath.Join(work, id)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, aiTrainJobFile), []byte("{}"), 0o644)
		at := time.Now().Add(time.Duration(i) * time.Minute)
		os.Chtimes(filepath.Join(d, aiTrainJobFile), at, at)
	}
	os.MkdirAll(filepath.Join(work, "not-a-run"), 0o755)
	runs := aiTrainRunsIn(work)
	if len(runs) != 2 || runs[0].id != "b" || runs[1].id != "a" {
		t.Fatalf("%+v", runs)
	}
}

func TestTrainEnvCarriesNoSecret(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-not-leak")
	t.Setenv("PIP_INDEX_URL", "https://evil.example/simple")
	t.Setenv("HF_TOKEN", "hf_should_not_leak")
	env := strings.Join(aiTrainEnv(nil), "\n")
	for _, bad := range []string{"OPENAI_API_KEY", "PIP_INDEX_URL", "HF_TOKEN", "sk-should", "hf_should"} {
		if strings.Contains(env, bad) {
			t.Fatalf("the trainer environment carries %s", bad)
		}
	}
	if !strings.Contains(env, "HF_HUB_OFFLINE=1") {
		t.Fatal("the trainer is not offline")
	}
}

var _ = syscall.SIGTERM

// The cloud trainer image's runtime stage installs a C compiler: Triton,
// under Unsloth, compiles its GPU launcher with it on the first training
// step. A live L4 run without one failed at step 0 ("Failed to find C
// compiler").
func TestTrainerImageRuntimeStageHasACCompiler(t *testing.T) {
	b, err := os.ReadFile("ai_train_assets/image/trainer.dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	stages := strings.Split(string(b), "\nFROM ")
	runtime := stages[len(stages)-1]
	if !strings.Contains(runtime, "apt-get install") || !strings.Contains(runtime, " gcc ") || !strings.Contains(runtime, "libc6-dev") {
		t.Fatalf("the runtime stage installs no C compiler:\n%s", runtime)
	}
}

// The native trainer's guard SIGKILLs the trainer over its memory cap and
// writes guard.json first. However late its error line comes, a watch that
// finds the trainer gone ends on ModelTooLarge, never on a death the workflow
// would relaunch.
func TestTrainWatchTreatsTheGuardsKillAsTerminal(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	if _, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"fakeMode": "oom"})); err != nil {
		t.Fatal(err)
	}
	lines, _ := watchToEnd(t, dir)
	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	last := lines[len(lines)-1]
	for _, want := range []string{`"type":"error"`, `"code":"ModelTooLarge"`, `"peakBytes":2600000000`, `"capBytes":2500000000`} {
		if !strings.Contains(last, want) {
			t.Fatalf("the last line %s lacks %s", last, want)
		}
	}
	if !aiTrainTerminal(last) {
		t.Fatalf("%s is not terminal", last)
	}

	// A guard.json from an earlier attempt is not this attempt's: a launch
	// clears it, and a run that finishes is not read as killed.
	dir2 := trainRun(t)
	os.WriteFile(filepath.Join(dir2, "guard.json"), []byte(`{"capBytes":1,"code":"ModelTooLarge","peakBytes":2}`), 0o644)
	if _, err := aiTrainLaunch(dir2, driverJSON(t, nil)); err != nil {
		t.Fatal(err)
	}
	lines, _ = watchToEnd(t, dir2)
	if got := eventTypes(lines); got[len(got)-1] != "done" {
		t.Fatalf("events %v", got)
	}
	if fileExists(filepath.Join(dir2, "guard.json")) {
		t.Fatal("the stale guard.json is still there")
	}
}

// The Fake backend needs no Python and nothing installed: the launch starts
// this program again as the Fake trainer, which writes the protocol, verified
// checkpoints and a real PEFT adapter with zero B matrices. Red before the
// trainer moved into the runtime: a Fake launch with no toolchain python was
// NotProvisioned.
func TestFakeTrainerRunsInTheRuntimeWithoutPython(t *testing.T) {
	fakeTrainer(t)
	dir := trainRun(t)
	v, err := aiTrainLaunch(dir, driverJSON(t, map[string]any{"maxSteps": 4, "checkpointEvery": 2, "toolchain": "builtin-1"}))
	if err != nil {
		t.Fatalf("a Fake launch with no python: %v", err)
	}
	lines, _ := watchToEnd(t, dir)
	types := eventTypes(lines)
	if types[0] != "hello" || types[len(types)-1] != "done" {
		t.Fatalf("events %v", types)
	}
	var hello struct {
		Pid       int            `json:"pid"`
		Versions  map[string]any `json:"versions"`
		Toolchain string         `json:"toolchain"`
	}
	json.Unmarshal([]byte(lines[0]), &hello)
	if hello.Pid != v["pid"].(int) || hello.Versions["sky-fake-trainer"] != aiFakeTrainerVersion || hello.Toolchain != "builtin-1" {
		t.Fatalf("hello %+v, launched pid %v", hello, v["pid"])
	}
	if cks := aiTrainCheckpoints(dir); len(cks) != 1 || cks[0].step != 2 {
		t.Fatalf("checkpoints %+v", cks)
	}
	tensors, err := aiReadSafetensors(filepath.Join(dir, "adapter", "adapter_model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	// Rank 2 over 2 layers and 2 targets: q (8 -> 8) and v (8 -> 4).
	a := tensors["base_model.model.model.layers.1.self_attn.v_proj.lora_A.weight"]
	b := tensors["base_model.model.model.layers.1.self_attn.v_proj.lora_B.weight"]
	if len(tensors) != 8 || fmt.Sprint(a.shape) != "[2 8]" || fmt.Sprint(b.shape) != "[4 2]" {
		t.Fatalf("adapter tensors %d, A %v, B %v", len(tensors), a.shape, b.shape)
	}
	for _, x := range b.data {
		if x != 0 {
			t.Fatal("a Fake adapter's B is not zero")
		}
	}
}

// The Fake trainer refuses a driver.json for another backend.
func TestFakeTrainerRunsOnlyTheFakeBackend(t *testing.T) {
	dir := trainRun(t)
	os.WriteFile(filepath.Join(dir, aiTrainDriverFile), []byte(`{"backend":"mlx"}`), 0o644)
	var out strings.Builder
	if code := aiFakeTrainerMain([]string{"--run", dir, "--launch", "t"}, &out); code != 1 || !strings.Contains(out.String(), `"code":"TrainingFailed"`) {
		t.Fatalf("exit %d, %s", code, out.String())
	}
}
