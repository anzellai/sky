//go:build !js

package rt

// ai_engine.go — the managed llama-server: start, health, supervise, idle
// unload, stop.
//
// An engine is a child process, never in-process native code: a C abort in
// the engine is an EngineCrashed failure for the calls in flight, not a dead
// app. The child is started through the Sky.Core.Process machinery
// (spawnProcess), so it runs in its own process group and every runtime exit
// path ends it (killAllChildProcesses). It binds 127.0.0.1 only, on a free
// port, behind a fresh random API key that reaches it through a 0600 file
// (`--api-key-file`), never the command line (which `ps` shows). It starts
// with a cleared environment plus an allow-list, so no operator secret
// reaches it.
//
// Supervision. A child that exits while it should be running is a crash. In
// development the engine restarts with backoff, at most three times a
// minute; after that it stays down and every call reports the crash. Outside
// development the app exits non-zero, as it does when its embedded
// PostgreSQL dies, so the platform supervisor restarts the whole tree instead
// of hiding a fault.
//
// Idle unload. An engine with an idle window stops after that long with no
// call, and the next call starts it again.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// aiKnobs are the supervisor's timings and host hooks. They sit behind one
// mutex so a test (which uses a fake engine and short timings) can change
// them while engine goroutines read them.
type aiKnobs struct {
	healthTimeout  time.Duration
	healthPoll     time.Duration
	idleTick       time.Duration
	restartBackoff time.Duration
	crashWindow    time.Duration
	maxCrashes     int
	startTimeout   time.Duration // includes a first model download
	// fatalExit ends the app when an engine crashes outside development.
	fatalExit func(msg string)
	// availableBytes is the memory an engine may use on this host.
	availableBytes func() int64
}

var (
	aiKnobsMu sync.Mutex
	aiKnobsV  = aiKnobs{
		healthTimeout:  5 * time.Minute,
		healthPoll:     100 * time.Millisecond,
		idleTick:       time.Second,
		restartBackoff: time.Second,
		crashWindow:    time.Minute,
		maxCrashes:     3,
		startTimeout:   30 * time.Minute,
		fatalExit: func(msg string) {
			fmt.Fprintln(os.Stderr, "[sky] "+msg)
			ExitProcess(1)
		},
		availableBytes: aiHostAvailableBytes,
	}
)

func aiK() aiKnobs {
	aiKnobsMu.Lock()
	defer aiKnobsMu.Unlock()
	return aiKnobsV
}

func aiSetKnobs(fn func(*aiKnobs)) {
	aiKnobsMu.Lock()
	fn(&aiKnobsV)
	aiKnobsMu.Unlock()
}

func aiStartTimeout() time.Duration { return aiK().startTimeout }

// aiEngineEnv is the engine child's whole environment: what it needs to run
// and to find its accelerator, and nothing else.
func aiEngineEnv() []string {
	keep := []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "SYSTEMROOT", "LANG", "LC_ALL",
		"LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH",
		"CUDA_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES", "ROCR_VISIBLE_DEVICES",
		"GPU_DEVICE_ORDINAL", "GGML_VK_VISIBLE_DEVICES"}
	var out []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// aiEngine is one engine (a model loaded with one set of launch settings).
type aiEngine struct {
	key  string
	slot string
	spec aiLocalSpec

	mu           sync.Mutex
	state        string        // "stopped", "starting", "ready", "down"
	starting     chan struct{} // closed when the current start attempt ends
	startErr     error
	proc         *procHandle
	pid          int
	port         int
	apiKey       string
	keyFile      string
	bin          aiEngineBinary
	fit          aiFit
	lastUse      time.Time
	inflight     int
	stopping     bool
	crashes      []time.Time
	restarts     int
	gaveUp       bool
	lastExit     string // the last crash, for status and errors
	lastExitCode int    // its exit code (-1 when a signal ended it)
	attached     bool   // using an engine another process holds (engines.json)
	regID        string
	idleOn       bool
	retired      bool // forgotten by aiResetEngines: background loops end
	// retireWhenIdle: a slot moved to another adapter while calls were still
	// running on this engine; the last of them stops it.
	retireWhenIdle bool
}

// aiRetireEngine stops an engine a slot no longer uses (its registry alias
// moved to another adapter): at once when it is idle, else when its last
// running call ends. A later call that needs it starts it again.
func aiRetireEngine(e *aiEngine) {
	e.mu.Lock()
	busy := e.inflight > 0
	if busy {
		e.retireWhenIdle = true
	}
	e.mu.Unlock()
	if !busy {
		go e.stop()
	}
}

var (
	aiEnginesMu sync.Mutex
	aiEngines   = map[string]*aiEngine{}
	aiEngineIDs = map[int64]*aiEngine{}
	aiEngineSeq int64
)

// aiEngineKey identifies an engine for sharing: the same model with the same
// launch settings is one engine.
func aiEngineKey(spec aiLocalSpec) string {
	key := fmt.Sprintf("%s|ctx=%d|gpu=%s|np=%d|emb=%v|port=%d",
		spec.model.sha256, spec.context, spec.gpu, spec.parallel, spec.embeddings, spec.port)
	if spec.adapter.sha256 != "" {
		key += "|lora=" + spec.adapter.sha256
	}
	return key
}

// aiEngineFor returns this process's engine for spec, creating it (stopped).
func aiEngineFor(slot string, spec aiLocalSpec) *aiEngine {
	key := aiEngineKey(spec)
	aiEnginesMu.Lock()
	defer aiEnginesMu.Unlock()
	if e, ok := aiEngines[key]; ok {
		return e
	}
	aiExitHookOnce.Do(func() { RegisterResourceCloser("ai.engines", aiStopAllEngines) })
	e := &aiEngine{key: key, slot: slot, spec: spec, state: "stopped"}
	aiEngines[key] = e
	aiEngineSeq++
	aiEngineIDs[aiEngineSeq] = e
	return e
}

func aiEngineID(e *aiEngine) int64 {
	aiEnginesMu.Lock()
	defer aiEnginesMu.Unlock()
	for id, x := range aiEngineIDs {
		if x == e {
			return id
		}
	}
	return 0
}

func aiEngineByID(id int64) (*aiEngine, error) {
	aiEnginesMu.Lock()
	defer aiEnginesMu.Unlock()
	if e, ok := aiEngineIDs[id]; ok {
		return e, nil
	}
	return nil, aiFailf(aiInvalidSetting, "no local engine %d in this program", id).with("setting", "engine")
}

// ensure makes the engine ready, starting it when it is not. Concurrent
// callers share one start attempt.
func (e *aiEngine) ensure(ctx context.Context) error {
	for {
		e.mu.Lock()
		switch e.state {
		case "ready":
			e.mu.Unlock()
			return nil
		case "starting":
			ch := e.starting
			e.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return aiFailf(aiTimedOut, "the local engine for %s did not become ready in time", e.spec.model.file)
			}
		case "stopping":
			e.mu.Unlock()
			select {
			case <-time.After(20 * time.Millisecond):
				continue
			case <-ctx.Done():
				return aiFailf(aiTimedOut, "the local engine for %s was still stopping", e.spec.model.file)
			}
		case "down":
			if e.gaveUp {
				msg, exitCode, crashes := e.lastExit, e.lastExitCode, len(e.crashes)
				e.mu.Unlock()
				f := aiFailf(aiEngineCrashed, "the local engine crashed %d times within %s and stays down: %s", crashes, aiK().crashWindow, msg).
					with("logTail", msg)
				if exitCode >= 0 {
					f.withInt("exitCode", exitCode)
				}
				return f
			}
		}
		// stopped, or down with a restart allowed: start it.
		if e.state == "down" {
			e.restarts++
		}
		e.state = "starting"
		e.starting = make(chan struct{})
		ch := e.starting
		e.mu.Unlock()

		err := e.start(ctx)

		e.mu.Lock()
		if err != nil {
			if e.state == "starting" {
				e.state = "stopped"
			}
			e.startErr = err
		} else {
			e.state = "ready"
			e.lastUse = time.Now()
		}
		close(ch)
		e.mu.Unlock()
		return err
	}
}

// start fetches, verifies, fits, launches and health-checks the engine. It
// first looks for the same engine already running for another process on
// this machine (engines.json) and uses that one instead.
func (e *aiEngine) start(ctx context.Context) error {
	aiReapOrphans()
	if ent, ok := aiRegistryFindLive(e.key); ok {
		if key, err := os.ReadFile(ent.KeyFile); err == nil {
			k := strings.TrimSpace(string(key))
			if aiHealthy(ent.Port, k) {
				e.mu.Lock()
				e.attached, e.port, e.apiKey, e.pid = true, ent.Port, k, ent.Pid
				e.fit = aiFit{accelerator: ent.Accelerator, contextTokens: ent.Context}
				e.bin = aiEngineBinary{build: ent.Build}
				e.mu.Unlock()
				return nil
			}
		}
	}

	bin, err := aiDiscoverEngine(ctx)
	if err != nil {
		return err
	}
	modelPath, err := aiEnsureModel(ctx, e.spec.model)
	if err != nil {
		return err
	}
	fit, err := aiFitFor(modelPath, e.spec, bin.variant)
	if err != nil {
		return err
	}
	if a := e.spec.adapter; a.path != "" {
		// The adapter file is hashed before each load, like a file: model,
		// because it may have been replaced since the registry checked it.
		if got, herr := aiFileSha256(a.path); herr != nil || got != a.sha256 {
			return aiFailf(aiChecksumMismatch, "the adapter %s at %s does not hash to sha256 %s (got %q); nothing was loaded", a.label, a.path, a.sha256, got).
				with("expected", a.sha256).with("actual", got)
		}
		// An adapter Std.Ai.Train converted records the serving model it was
		// converted for; loaded on any other model it would answer nonsense.
		if want := aiAdapterServingSha(a.path); want != "" && want != e.spec.model.sha256 {
			return aiFailf(aiAdapterMismatch,
				"the adapter %s was converted for the model sha256 %s, and this engine runs sha256 %s; serve it on its own base model",
				a.label, want, e.spec.model.sha256).
				with("adapter", a.label).with("adapterBase", want).with("engineBase", e.spec.model.sha256)
		}
	}
	port := e.spec.port
	if port == 0 {
		if port, err = aiFreePort(); err != nil {
			return aiFailf(aiEngineFailed, "no free port on 127.0.0.1: %v", err)
		}
	}
	apiKey := aiRandomHex(32)
	runDir := filepath.Join(aiHome(), "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return aiFailf(aiEngineFailed, "cannot create %s: %v", runDir, err)
	}
	keyFile := filepath.Join(runDir, aiRandomHex(8)+".key")
	if err := os.WriteFile(keyFile, []byte(apiKey+"\n"), 0o600); err != nil {
		return aiFailf(aiEngineFailed, "cannot write the engine key file: %v", err)
	}

	args := []string{
		"-m", modelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--api-key-file", keyFile,
		"-c", strconv.Itoa(fit.contextTokens),
		"-ngl", strconv.Itoa(fit.gpuLayers),
		"-np", strconv.Itoa(e.spec.parallel),
		"--no-webui",
	}
	if e.spec.embeddings {
		args = append(args, "--embeddings", "--pooling", "mean")
	}
	if e.spec.adapter.path != "" {
		args = append(args, "--lora", e.spec.adapter.path)
	}
	var env [][2]string
	for _, kv := range aiEngineEnv() {
		k, v, _ := strings.Cut(kv, "=")
		env = append(env, [2]string{k, v})
	}
	spec := procSpec{program: bin.path, args: args, env: env, clearEnv: true, ringSize: 256 << 10}

	// Spawn on a goroutine of its own: spawnProcess ties a child to the
	// Sky.Live session of the calling goroutine, and an engine belongs to
	// the whole app, not to the session whose call happened to start it.
	type spawned struct {
		h   *procHandle
		err any
	}
	done := make(chan spawned, 1)
	go func() {
		h, perr := spawnProcess(spec)
		done <- spawned{h, perr}
	}()
	sp := <-done
	if sp.err != nil {
		os.Remove(keyFile)
		return aiFailf(aiEngineFailed, "cannot start %s: %v", bin.path, sp.err)
	}
	h := sp.h
	if err := aiWaitHealthy(ctx, h, port, apiKey); err != nil {
		h.shutdown()
		os.Remove(keyFile)
		return err
	}

	e.mu.Lock()
	e.proc, e.pid, e.port, e.apiKey, e.keyFile = h, h.pid, port, apiKey, keyFile
	e.bin, e.fit, e.attached, e.stopping = bin, fit, false, false
	e.mu.Unlock()
	e.regID = aiRegistryAdd(aiRegistryEntry{
		Slot: e.slot, Key: e.key, Pid: h.pid, HolderPid: os.Getpid(), Port: port,
		Model: e.spec.model.String(), KeyFile: keyFile, Build: bin.build,
		Accelerator: fit.accelerator, Context: fit.contextTokens,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
	go e.supervise(h)
	e.startIdleLoop()
	return nil
}

// aiFitFor plans the engine's memory from the model file and the host.
func aiFitFor(modelPath string, spec aiLocalSpec, variant string) (aiFit, error) {
	meta, err := readGGUFMeta(modelPath)
	if err != nil {
		return aiFit{}, aiFailf(aiInvalidSetting, "model %s: %v", modelPath, err).with("setting", "model")
	}
	st, err := os.Stat(modelPath)
	if err != nil {
		return aiFit{}, aiFailf(aiNotProvisioned, "model %s: %v", modelPath, err).with("what", modelPath).with("command", "sky ai provision")
	}
	accel, err := aiAccelerator(variant)
	if err != nil {
		return aiFit{}, err
	}
	return aiPlanFit(meta, st.Size(), spec.context, spec.gpu, accel, aiK().availableBytes())
}

func aiFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var aiEngineClient = &http.Client{Transport: &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	MaxIdleConnsPerHost: 64,
	IdleConnTimeout:     90 * time.Second,
}}

// aiHealthy asks an engine's /health once.
func aiHealthy(port int, key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := aiEngineClient.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == 200
}

// aiWaitHealthy polls /health until the engine answers 200, the child exits
// (EngineCrashed with its log tail) or the deadline passes.
func aiWaitHealthy(ctx context.Context, h *procHandle, port int, key string) error {
	k := aiK()
	deadline := time.Now().Add(k.healthTimeout)
	for {
		select {
		case <-h.exited:
			code, _ := h.exitInfo()
			return aiFailf(aiEngineCrashed, "llama-server exited with code %d while loading the model: %s", code, h.logTail(1500)).
				withInt("exitCode", code).with("logTail", h.logTail(1500))
		default:
		}
		if aiHealthy(port, key) {
			return nil
		}
		if time.Now().After(deadline) {
			return aiFailf(aiTimedOut, "llama-server did not report healthy within %s: %s", k.healthTimeout, h.logTail(1500)).
				withInt("afterMs", int(k.healthTimeout/time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return aiFailf(aiTimedOut, "llama-server did not report healthy before the start deadline: %s", h.logTail(1500))
		case <-h.exited:
		case <-time.After(k.healthPoll):
		}
	}
}

// exitInfo reads a reaped child's exit code and signal.
func (h *procHandle) exitInfo() (code, sig int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exitCode, h.exitSig
}

// logTail is the last n bytes of the child's stderr and stdout, for an error
// message (llama-server logs to stderr).
func (h *procHandle) logTail(n int) string {
	var parts []string
	for _, r := range []*outRing{h.out[procStreamStderr], h.out[procStreamStdout]} {
		if r == nil {
			continue
		}
		_, end, _ := r.snapshot()
		from := end - int64(n)
		if from < 0 {
			from = -1
		}
		c := r.readFrom(from, n)
		if len(c.data) > 0 {
			parts = append(parts, string(c.data))
		}
	}
	t := aiTail(strings.Join(parts, "\n"), n)
	if t == "" {
		return "(no output)"
	}
	return t
}

// supervise waits for the child to exit and decides what an exit means.
func (e *aiEngine) supervise(h *procHandle) {
	<-h.exited
	code, sig := h.exitInfo()
	e.mu.Lock()
	if e.proc != h {
		e.mu.Unlock()
		return
	}
	intentional := e.stopping || e.retired
	e.proc = nil
	keyFile := e.keyFile
	e.mu.Unlock()
	aiRegistryRemove(e.regID)
	os.Remove(keyFile)
	if intentional {
		return
	}
	msg := fmt.Sprintf("llama-server (pid %d) exited with code %d", h.pid, code)
	if sig != 0 {
		msg = fmt.Sprintf("llama-server (pid %d) was killed by signal %d", h.pid, sig)
	}
	msg += ": " + h.logTail(1500)

	k := aiK()
	e.mu.Lock()
	now := time.Now()
	e.lastExit = msg
	e.lastExitCode = code
	if sig != 0 {
		e.lastExitCode = -1 // killed by a signal: no exit code
	}
	e.state = "down"
	recent := e.crashes[:0]
	for _, t := range e.crashes {
		if now.Sub(t) < k.crashWindow {
			recent = append(recent, t)
		}
	}
	e.crashes = append(recent, now)
	n := len(e.crashes)
	if n > k.maxCrashes {
		e.gaveUp = true
	}
	gaveUp := e.gaveUp
	e.mu.Unlock()

	if !aiDevelopment() {
		k.fatalExit("Std.Ai.Local: " + msg + "; outside development the app exits so its supervisor restarts it")
		return
	}
	if gaveUp {
		fmt.Fprintf(os.Stderr, "[sky] Std.Ai.Local: the engine crashed %d times within %s and stays down: %s\n", n, k.crashWindow, msg)
		return
	}
	go func() {
		time.Sleep(k.restartBackoff << (n - 1))
		e.mu.Lock()
		if e.state != "down" || e.gaveUp {
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		if err := e.ensure(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[sky] Std.Ai.Local: restart failed: %v\n", err)
		}
	}()
}

// startIdleLoop runs the idle-unload timer for an engine that has a window.
func (e *aiEngine) startIdleLoop() {
	if e.spec.idleUnload <= 0 {
		return
	}
	e.mu.Lock()
	if e.idleOn {
		e.mu.Unlock()
		return
	}
	e.idleOn = true
	e.mu.Unlock()
	go func() {
		t := time.NewTicker(aiK().idleTick)
		defer t.Stop()
		for range t.C {
			e.mu.Lock()
			if e.retired {
				e.mu.Unlock()
				return
			}
			idle := e.state == "ready" && !e.attached && e.inflight == 0 && time.Since(e.lastUse) >= e.spec.idleUnload
			e.mu.Unlock()
			if idle {
				e.stop()
			}
		}
	}()
}

// stop ends the engine (an intentional stop, never a crash). An attached
// engine belongs to another process and is only let go.
func (e *aiEngine) stop() {
	e.mu.Lock()
	if e.state == "starting" {
		ch := e.starting
		e.mu.Unlock()
		<-ch
		e.mu.Lock()
	}
	h := e.proc
	e.stopping = true
	e.gaveUp = false
	e.crashes = nil
	if e.attached || h == nil {
		e.attached = false
		e.state = "stopped"
		e.mu.Unlock()
		return
	}
	// "stopping" until the process, its registry entry and its key are gone:
	// a call that arrives meanwhile waits and then starts a fresh engine.
	e.state = "stopping"
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.state == "stopping" {
			e.state = "stopped"
		}
		e.mu.Unlock()
	}()
	if h != nil {
		_ = procSignalGroup(h.cmd.Process, syscall.SIGTERM)
		select {
		case <-h.exited:
		case <-time.After(10 * time.Second):
		}
		h.shutdown()
		// The supervisor tidies these too; doing it here as well means a
		// stop has finished when it returns, with nothing left behind.
		aiRegistryRemove(e.regID)
		e.mu.Lock()
		kf := e.keyFile
		e.mu.Unlock()
		os.Remove(kf)
	}
}

// post sends one call to the engine. A failure is classified: a dead child
// is EngineCrashed (with its exit code and log tail), a 503 while loading is
// Overloaded, a deadline is TimedOut.
func (e *aiEngine) post(ctx context.Context, timeout time.Duration, path, body string) (string, error) {
	e.mu.Lock()
	port, key, h := e.port, e.apiKey, e.proc
	e.inflight++
	e.lastUse = time.Now()
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.inflight--
		e.lastUse = time.Now()
		retire := e.inflight == 0 && e.retireWhenIdle
		if retire {
			e.retireWhenIdle = false
		}
		e.mu.Unlock()
		if retire {
			go e.stop()
		}
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/v1%s", port, path)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", aiFailf(aiEngineFailed, "bad engine request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := aiEngineClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if rerr == nil {
			switch {
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				return string(data), nil
			case resp.StatusCode == 503 || resp.StatusCode == 429:
				f := aiFailf(aiOverloaded, "the local engine is busy (HTTP %d): %s", resp.StatusCode, aiTail(string(data), 300))
				f.httpStatus = resp.StatusCode
				return "", f
			default:
				f := aiFailf(aiRemoteRejected, "the local engine answered HTTP %d: %s", resp.StatusCode, aiTail(string(data), 500))
				f.httpStatus = resp.StatusCode
				return "", f
			}
		}
		err = rerr
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return "", aiFailf(aiTimedOut, "the local engine gave no answer within %s", timeout).
			withInt("afterMs", int(timeout/time.Millisecond))
	}
	// The child may have died under the call: wait briefly for the reaper.
	if h != nil {
		select {
		case <-h.exited:
		case <-time.After(500 * time.Millisecond):
		}
		select {
		case <-h.exited:
			code, _ := h.exitInfo()
			return "", aiFailf(aiEngineCrashed, "llama-server exited with code %d during the call: %s", code, h.logTail(1500)).
				withInt("exitCode", code).with("logTail", h.logTail(1500))
		default:
		}
	}
	e.mu.Lock()
	if e.attached {
		// The engine another process held went away: forget it, so the next
		// call starts an engine of our own.
		e.attached = false
		e.state = "stopped"
	}
	e.mu.Unlock()
	return "", aiFailf(aiUnreachable, "the local engine on 127.0.0.1:%d: %v", port, aiStripURL(err)).
		with("target", fmt.Sprintf("http://127.0.0.1:%d", port))
}

// status is the Sky `Status` record.
func (e *aiEngine) status() any {
	e.mu.Lock()
	defer e.mu.Unlock()
	idle := 0
	if e.state == "ready" && !e.lastUse.IsZero() {
		idle = int(time.Since(e.lastUse) / time.Second)
	}
	return map[string]any{
		"state":         e.state,
		"pid":           e.pid,
		"port":          e.port,
		"model":         e.spec.model.String(),
		"build":         e.bin.build,
		"accelerator":   e.fit.accelerator,
		"contextTokens": e.fit.contextTokens,
		"gpuLayers":     e.fit.gpuLayers,
		"restarts":      e.restarts,
		"idleSeconds":   idle,
		"shared":        e.attached,
		"lastCrash":     e.lastExit,
	}
}

// aiStopAllEngines stops every engine this process started (an exit hook;
// killAllChildProcesses also ends them, this tidies the registry and keys).
func aiStopAllEngines() {
	aiEnginesMu.Lock()
	var es []*aiEngine
	for _, e := range aiEngines {
		es = append(es, e)
	}
	aiEnginesMu.Unlock()
	for _, e := range es {
		e.stop()
	}
}

// aiResetEngines stops and forgets every engine (tests).
func aiResetEngines() {
	aiStopAllEngines()
	aiEnginesMu.Lock()
	for _, e := range aiEngineIDs {
		e.mu.Lock()
		e.retired = true
		e.mu.Unlock()
	}
	aiEngines = map[string]*aiEngine{}
	aiEngineIDs = map[int64]*aiEngine{}
	aiEnginesMu.Unlock()
}

var aiExitHookOnce sync.Once
