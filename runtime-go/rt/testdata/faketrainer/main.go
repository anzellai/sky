// Command faketrainer stands in for the Python training driver in the Go
// tests of ai_train.go. It is launched the way the driver is
// (`--run <dir> --launch <token>`), reads driver.json, and writes the same
// protocol, checkpoints and adapter. driver.json's "fakeMode" picks a
// behaviour the tests need:
//
//	""        train maxSteps steps of fakeStepMs each, then done
//	"hang"    like "", but ignore SIGTERM (a trainer that must be killed)
//	"partial" write one line without its newline, then sleep
//	"error"   write an error line and exit 1
//
// fakeCrashAt crashes the first attempt after that step (exit 137, no
// terminal line). SIGTERM checkpoints and writes `cancelled`.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	run := ""
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--run" {
			run = os.Args[i+1]
		}
	}
	b, err := os.ReadFile(filepath.Join(run, "driver.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var cfg struct {
		Attempt         int    `json:"attempt"`
		ResumeFrom      int    `json:"resumeFrom"`
		MaxSteps        int    `json:"maxSteps"`
		CheckpointEvery int    `json:"checkpointEvery"`
		FakeCrashAt     int    `json:"fakeCrashAt"`
		FakeStepMs      int    `json:"fakeStepMs"`
		FakeMode        string `json:"fakeMode"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	emit := func(m map[string]any) {
		m["v"] = 1
		out, _ := json.Marshal(m)
		os.Stdout.Write(append(out, '\n'))
	}
	term := make(chan os.Signal, 1)
	if cfg.FakeMode == "hang" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(term, syscall.SIGTERM)
	}
	emit(map[string]any{"type": "hello", "backend": "fake", "attempt": cfg.Attempt, "resumeFrom": cfg.ResumeFrom, "totalSteps": cfg.MaxSteps, "pid": os.Getpid()})
	switch cfg.FakeMode {
	case "partial":
		os.Stdout.Write([]byte(`{"v":1,"type":"progress","step":1`))
		time.Sleep(time.Hour)
	case "error":
		emit(map[string]any{"type": "error", "code": "TrainingFailed", "message": "scripted failure"})
		os.Exit(1)
	}
	last := cfg.ResumeFrom
	for step := cfg.ResumeFrom; step < cfg.MaxSteps; step++ {
		select {
		case <-term:
			if step > last {
				checkpoint(run, step)
				emit(map[string]any{"type": "checkpoint", "step": step, "path": fmt.Sprintf("checkpoints/%07d", step)})
			}
			emit(map[string]any{"type": "cancelled", "step": step})
			os.Exit(0)
		default:
		}
		time.Sleep(time.Duration(cfg.FakeStepMs) * time.Millisecond)
		done := step + 1
		emit(map[string]any{"type": "progress", "step": done, "total": cfg.MaxSteps, "loss": 2.0 / float64(done)})
		if cfg.CheckpointEvery > 0 && done%cfg.CheckpointEvery == 0 && done < cfg.MaxSteps {
			checkpoint(run, done)
			emit(map[string]any{"type": "checkpoint", "step": done, "path": fmt.Sprintf("checkpoints/%07d", done)})
			last = done
		}
		if cfg.FakeCrashAt > 0 && cfg.Attempt == 0 && done == cfg.FakeCrashAt {
			os.Exit(137)
		}
	}
	os.MkdirAll(filepath.Join(run, "adapter"), 0o755)
	os.WriteFile(filepath.Join(run, "adapter", "adapter_model.safetensors"), []byte("adapter"), 0o644)
	emit(map[string]any{"type": "done", "step": cfg.MaxSteps, "trainLoss": 0.5, "valLoss": 0.6})
}

func checkpoint(run string, step int) {
	dir := filepath.Join(run, "checkpoints", fmt.Sprintf("%07d", step))
	os.MkdirAll(dir, 0o755)
	files := map[string]string{
		"adapter.safetensors":   fmt.Sprintf("adapter-%d", step),
		"optimizer.safetensors": fmt.Sprintf("optimizer-%d", step),
		"state.json":            fmt.Sprintf(`{"step":%d}`, step),
	}
	sums := map[string]string{}
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		s := sha256.Sum256([]byte(body))
		sums[name] = hex.EncodeToString(s[:])
	}
	m, _ := json.Marshal(map[string]any{"step": step, "files": sums})
	os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644)
}
