//go:build !js

package rt

// ai_registry.go — `<AI_HOME>/engines.json`, the machine's running engines.
//
// Every engine a Sky program starts is recorded here: its pid, the pid of the
// program that holds it, its port, its model and the file that holds its API
// key. Three things read it:
//
//   - another Sky program that needs the same engine (same model, same launch
//     settings) uses the running one instead of loading the model again;
//   - `sky ai ps` lists engines it did not start;
//   - `sky ai stop <slot>` stops them.
//
// Liveness has two legs, as `clusters.json` has for PostgreSQL: the holder
// process and the engine process must both be alive. An entry that fails
// either leg is stale and is dropped on the next write.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type aiRegistryEntry struct {
	ID          string `json:"id"`
	Slot        string `json:"slot"`
	Key         string `json:"key"`
	Pid         int    `json:"pid"`
	HolderPid   int    `json:"holderPid"`
	Port        int    `json:"port"`
	Model       string `json:"model"`
	KeyFile     string `json:"keyFile"`
	Build       string `json:"build"`
	Accelerator string `json:"accelerator"`
	Context     int    `json:"context"`
	StartedAt   string `json:"startedAt"`
}

type aiRegistryFile struct {
	Engines []aiRegistryEntry `json:"engines"`
}

func aiRegistryPath() string { return filepath.Join(aiHome(), "engines.json") }

func aiRegistryRead() aiRegistryFile {
	var f aiRegistryFile
	if b, err := os.ReadFile(aiRegistryPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

// aiRegistryUpdate applies fn under the registry lock and writes the result
// atomically (temp file + rename). Stale entries are dropped on every write.
func aiRegistryUpdate(fn func(*aiRegistryFile)) {
	if err := os.MkdirAll(aiHome(), 0o755); err != nil {
		return
	}
	unlock, err := aiLockFile(aiRegistryPath()+".lock", 30*time.Second)
	if err != nil {
		return
	}
	defer unlock()
	f := aiRegistryRead()
	live := f.Engines[:0]
	for _, e := range f.Engines {
		if aiEntryAlive(e) {
			live = append(live, e)
		}
	}
	f.Engines = live
	fn(&f)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	tmp := aiRegistryPath() + ".tmp-" + aiRandomHex(4)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, aiRegistryPath()); err != nil {
		os.Remove(tmp)
	}
}

// aiEntryAlive is the two-legged liveness check.
func aiEntryAlive(e aiRegistryEntry) bool {
	return processAlive(e.Pid) && processAlive(e.HolderPid)
}

func aiRegistryAdd(e aiRegistryEntry) string {
	e.ID = aiRandomHex(6)
	aiRegistryUpdate(func(f *aiRegistryFile) { f.Engines = append(f.Engines, e) })
	return e.ID
}

func aiRegistryRemove(id string) {
	if id == "" {
		return
	}
	aiRegistryUpdate(func(f *aiRegistryFile) {
		out := f.Engines[:0]
		for _, e := range f.Engines {
			if e.ID != id {
				out = append(out, e)
			}
		}
		f.Engines = out
	})
}

// aiRegistryFindLive finds a live engine with the same key that another
// process holds.
func aiRegistryFindLive(key string) (aiRegistryEntry, bool) {
	for _, e := range aiRegistryRead().Engines {
		if e.Key == key && e.HolderPid != os.Getpid() && aiEntryAlive(e) {
			return e, true
		}
	}
	return aiRegistryEntry{}, false
}

// aiRegistryList is every recorded engine with its state: "running" (both
// legs alive), "orphaned" (the engine runs, its holder is gone) or "stale".
func aiRegistryList() []any {
	var out []any
	for _, e := range aiRegistryRead().Engines {
		state := "stale"
		switch {
		case aiEntryAlive(e):
			state = "running"
		case processAlive(e.Pid):
			state = "orphaned"
		}
		out = append(out, map[string]any{
			"slot":        e.Slot,
			"state":       state,
			"pid":         e.Pid,
			"holderPid":   e.HolderPid,
			"port":        e.Port,
			"model":       e.Model,
			"build":       e.Build,
			"accelerator": e.Accelerator,
			"context":     e.Context,
			"startedAt":   e.StartedAt,
		})
	}
	return out
}

// aiStopSlot stops the recorded engines of a slot ("" stops all of them) and
// returns how many it stopped. An engine this process holds is stopped
// directly. For one another process holds, the holder is asked to exit
// (SIGTERM, which ends its children); an engine still running after that, or
// one whose holder is gone, is ended by its process group.
func aiStopSlot(slot string) int {
	var targets []aiRegistryEntry
	for _, e := range aiRegistryRead().Engines {
		if (slot == "" || e.Slot == slot) && processAlive(e.Pid) {
			targets = append(targets, e)
		}
	}
	for _, t := range targets {
		if t.HolderPid == os.Getpid() {
			aiEnginesMu.Lock()
			e := aiEngines[t.Key]
			aiEnginesMu.Unlock()
			if e != nil {
				e.stop()
				continue
			}
		}
		if processAlive(t.HolderPid) && t.HolderPid != os.Getpid() {
			_ = sysSignalPid(t.HolderPid, syscall.SIGTERM)
			aiWaitGone(t.HolderPid, 10*time.Second)
		}
		if aiIsEngineProcess(t) {
			aiKillEngineGroup(t.Pid)
		}
		os.Remove(t.KeyFile)
	}
	ids := map[string]bool{}
	for _, t := range targets {
		ids[t.ID] = true
	}
	aiRegistryUpdate(func(f *aiRegistryFile) {
		out := f.Engines[:0]
		for _, e := range f.Engines {
			if !ids[e.ID] {
				out = append(out, e)
			}
		}
		f.Engines = out
	})
	return len(targets)
}

// aiIsEngineProcess reports whether pid is still the engine an entry
// recorded. The per-run key file path is on the engine's command line and is
// unique to that launch, so a recycled pid never matches. Without `ps` (a
// minimal container) it answers false: an engine is never killed on a guess.
func aiIsEngineProcess(e aiRegistryEntry) bool {
	if !processAlive(e.Pid) || e.KeyFile == "" {
		return false
	}
	cmd, ok := processCommand(e.Pid)
	return ok && strings.Contains(cmd, e.KeyFile)
}

// aiReapOrphans ends engines whose holder died without stopping them (a
// SIGKILL, a SIGPIPE, a power cut of the holder alone). An engine runs in its
// own process group, so it outlives a holder that could not run its exit
// path; the next Sky program that starts an engine on this machine ends it.
// It returns how many it ended.
func aiReapOrphans() int {
	var orphans []aiRegistryEntry
	for _, e := range aiRegistryRead().Engines {
		if !processAlive(e.HolderPid) && aiIsEngineProcess(e) {
			orphans = append(orphans, e)
		}
	}
	for _, o := range orphans {
		aiKillEngineGroup(o.Pid)
		os.Remove(o.KeyFile)
	}
	if len(orphans) > 0 {
		aiRegistryUpdate(func(*aiRegistryFile) {}) // drops the now-dead entries
	}
	return len(orphans)
}

// aiKillEngineGroup ends an engine's process group: SIGTERM, then SIGKILL.
func aiKillEngineGroup(pid int) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = procSignalGroup(p, syscall.SIGTERM)
	if !aiWaitGone(pid, 10*time.Second) {
		_ = procSignalGroup(p, syscall.SIGKILL)
		aiWaitGone(pid, 5*time.Second)
	}
}

func aiWaitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !processAlive(pid)
}
