//go:build !js

package rt

// ai_fake_trainer.go — the Fake backend's trainer, in the runtime: no Python,
// nothing to install.
//
// A Fake attempt is the Sky program itself, started again as
// `<the program> --sky-ai-fake-trainer --run <dir> --launch <token>`: the
// init below sees the flag before main runs, trains, and exits. It is
// launched, detached and watched like any trainer (aiTrainSpawn), so the
// process handling, the protocol file, checkpoints, cancel, a crash and the
// durable relaunch are all real. Its loss is a fixed function of the step and
// the seed, so a resumed run reports the same numbers as an uninterrupted
// one, and its adapter is a real PEFT LoRA adapter for the base's shapes
// with zero B matrices: it converts to GGUF and loads in llama-server without
// changing the model's answers.
//
// Test mode (SKY_TEST_MODE) runs the same loop in the calling process,
// synchronously (aiTrainSimulate). driver.json's "fakeMode" gives the Go tests
// the trainers they need ("hang", "partial", "error", "oom"); Std.Ai.Train
// never writes it.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// aiFakeTrainerFlag is the first argument that turns a Sky program into the
// Fake trainer.
const aiFakeTrainerFlag = "--sky-ai-fake-trainer"

// aiFakeTrainerVersion versions the Fake trainer's behaviour (its loss curve,
// its checkpoint and adapter files); it is part of the toolchain id.
const aiFakeTrainerVersion = "1"

func init() {
	if len(os.Args) >= 2 && os.Args[1] == aiFakeTrainerFlag {
		ExitProcess(aiFakeTrainerMain(os.Args[2:], os.Stdout))
	}
}

// aiFakeToolchain is the Fake backend's Toolchain record: built in, always
// installed.
func aiFakeToolchain() map[string]any {
	return map[string]any{"name": "fake", "id": "builtin-" + aiFakeTrainerVersion, "python": "", "driverDir": "", "dir": "",
		"versions": []any{map[string]any{"name": "sky-fake-trainer", "version": aiFakeTrainerVersion}},
		"packages": 0, "installed": true, "trainer": ""}
}

// aiFakeTrainerCommand starts this program again as the Fake trainer.
func aiFakeTrainerCommand(runDir, token string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, aiTrainFail("cannot find this program to start the fake trainer: %v", err)
	}
	return exec.Command(exe, aiFakeTrainerFlag, "--run", runDir, "--launch", token), nil
}

// aiFakeHooks is what differs between the trainer process and test mode.
type aiFakeHooks struct {
	toolchain string
	versions  map[string]any
	pid       int
	stepDelay time.Duration
	cancelled func() bool
	// crash ends the attempt after its fakeCrashAt step with no terminal
	// line: the process exits 137, test mode returns.
	crash func()
}

// aiFakeTrainerMain is the trainer process. It returns the exit code.
func aiFakeTrainerMain(args []string, out io.Writer) int {
	run := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--run" {
			run = args[i+1]
		}
	}
	if run == "" {
		fmt.Fprintln(os.Stderr, "usage: "+aiFakeTrainerFlag+" --run <dir> --launch <token>")
		return 2
	}
	b, err := os.ReadFile(filepath.Join(run, aiTrainDriverFile))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg, err := aiTrainJSON(string(b))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	emit := aiFakeEmitter(out)
	if backend := aiJStr(cfg, "backend"); backend != "fake" {
		emit("error", map[string]any{"code": "TrainingFailed", "message": "the fake trainer runs the fake backend, not " + strconv.Quote(backend)})
		return 1
	}
	var term atomic.Bool
	mode := aiJStr(cfg, "fakeMode")
	if mode == "hang" {
		signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	} else {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-ch
			term.Store(true)
		}()
	}
	hello := func() {
		emit("hello", map[string]any{"backend": "fake", "toolchain": aiJStr(cfg, "toolchain"),
			"versions": map[string]any{"sky-fake-trainer": aiFakeTrainerVersion},
			"attempt":  aiJNum(cfg, "attempt", 0), "resumeFrom": aiJNum(cfg, "resumeFrom", 0),
			"totalSteps": aiJNum(cfg, "maxSteps", 0), "pid": os.Getpid()})
	}
	switch mode {
	case "partial":
		hello()
		io.WriteString(out, `{"v":1,"type":"progress","step":1`)
		time.Sleep(time.Hour)
		return 0
	case "error":
		hello()
		emit("error", map[string]any{"code": "TrainingFailed", "message": "scripted failure"})
		return 1
	case "oom":
		// What the native trainer's memory guard leaves when it kills the
		// trainer before its error line: guard.json and a SIGKILL's status.
		hello()
		os.WriteFile(filepath.Join(run, aiTrainGuardFile), []byte(`{"capBytes":2500000000,"code":"ModelTooLarge","peakBytes":2600000000}`+"\n"), 0o644)
		return 137
	}
	delay := 20 * time.Millisecond
	if _, ok := cfg["fakeStepMs"]; ok {
		delay = time.Duration(aiJNum(cfg, "fakeStepMs", 20)) * time.Millisecond
	}
	hooks := aiFakeHooks{
		toolchain: aiJStr(cfg, "toolchain"),
		versions:  map[string]any{"sky-fake-trainer": aiFakeTrainerVersion},
		pid:       os.Getpid(),
		stepDelay: delay,
		cancelled: func() bool {
			if mode == "hang" {
				return false
			}
			return term.Load() || fileExists(filepath.Join(run, aiTrainCancelFile))
		},
		crash: func() { ExitProcess(137) },
	}
	if err := aiFakeTrain(run, cfg, out, hooks); err != nil {
		fmt.Fprintln(os.Stderr, err)
		emit("error", map[string]any{"code": "TrainingFailed", "message": err.Error()})
		return 1
	}
	return 0
}

func aiFakeEmitter(out io.Writer) func(string, map[string]any) {
	return func(typ string, fields map[string]any) {
		fields["v"] = 1
		fields["type"] = typ
		b, _ := json.Marshal(fields)
		out.Write(append(b, '\n'))
	}
}

// aiFakeTrain is one Fake attempt: the protocol lines, verified checkpoints,
// a resume from the checkpoint driver.json names, and (when the attempt
// finishes) the adapter. An error line ends a failed attempt; the returned
// error is for a run directory it cannot write.
func aiFakeTrain(runDir string, cfg map[string]any, out io.Writer, h aiFakeHooks) error {
	emit := aiFakeEmitter(out)
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
	emit("hello", map[string]any{"backend": "fake", "toolchain": h.toolchain, "versions": h.versions,
		"attempt": attempt, "resumeFrom": start, "totalSteps": total, "pid": h.pid})
	if start > 0 {
		dir := aiJStr(cfg, "resumeDir")
		if dir == "" {
			dir = filepath.Join("checkpoints", fmt.Sprintf("%07d", start))
		}
		if !aiCheckpointVerified(filepath.Join(runDir, dir)) {
			emit("error", map[string]any{"code": "TrainingFailed", "message": "checkpoint " + dir + " does not match its manifest"})
			return nil
		}
	}
	every, evalEvery, crash := aiJNum(cfg, "checkpointEvery", 0), aiJNum(cfg, "evalEvery", 0), aiJNum(cfg, "fakeCrashAt", 0)
	lastCk := start
	epochLen := (nTrain + eff - 1) / eff
	checkpoint := func(step int) error {
		if err := aiTrainSimCheckpoint(runDir, step); err != nil {
			return err
		}
		emit("checkpoint", map[string]any{"step": step, "path": filepath.Join("checkpoints", fmt.Sprintf("%07d", step))})
		return nil
	}
	for step := start; step < total; step++ {
		if h.cancelled() {
			if step > lastCk {
				if err := checkpoint(step); err != nil {
					return err
				}
			}
			emit("cancelled", map[string]any{"step": step})
			return nil
		}
		t0 := time.Now()
		if h.stepDelay > 0 {
			time.Sleep(h.stepDelay)
		}
		done := step + 1
		tps := 1000.0
		if el := time.Since(t0).Seconds(); h.stepDelay > 0 && el > 0 {
			tps = math.Round(32*float64(eff)/el*1000) / 1000
		}
		emit("progress", map[string]any{"step": done, "total": total, "loss": loss(done), "learningRate": cfg["learningRate"],
			"tokensPerSecond": tps, "epoch": step / max(1, epochLen)})
		if evalEvery > 0 && done%evalEvery == 0 && done < total {
			emit("metric", map[string]any{"step": done, "name": "val_loss", "value": loss(done) + 0.08})
		}
		if every > 0 && done%every == 0 && done < total {
			if err := checkpoint(done); err != nil {
				return err
			}
			lastCk = done
		}
		if crash > 0 && attempt == 0 && done == crash {
			h.crash()
			return nil // test mode: no terminal line, as a killed trainer leaves it
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
