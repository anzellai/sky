//go:build !js && unix

package rt

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// aiEngineWatchScript ends the engine's process group when its holder is
// gone. It blocks reading a pipe whose only writer is the holder; the read
// returns when the holder closes it (an intentional stop) or when the holder
// dies by any means, SIGKILL included, because the kernel closes the dead
// process's descriptors. An engine that is already gone is left alone.
// The watcher ignores the SIGTERM it sends to its own group, then ends the
// group (itself with it) with SIGKILL if the engine has not stopped.
const aiEngineWatchScript = `trap '' TERM
read _ignored
kill -0 "$1" 2>/dev/null || exit 0
kill -TERM -"$1" 2>/dev/null
i=0
while [ $i -lt 50 ] && kill -0 "$1" 2>/dev/null; do sleep 0.1; i=$((i+1)); done
kill -KILL -"$1" 2>/dev/null`

// aiWatchEngine ties a managed engine to this process. The engine runs in a
// process group of its own (spawnProcess), so neither a terminal's Ctrl-C
// nor the death of its holder reaches it: before the watcher, a holder
// killed without running its exit path (SIGKILL, a test binary's timeout)
// left the engine running under pid 1 until the next program that started
// an engine on the same AI home reaped it (aiReapOrphans), and with a
// per-run home (a test's t.TempDir) no such program ever came. The watcher
// joins the engine's group, so it keeps the group id from being reused and
// is ended with the group by every intentional stop. The returned file is
// the pipe's write end: close it when the engine is stopped. nil when the
// watcher cannot start (the engine is then only reaped by the next start,
// as before).
func aiWatchEngine(pid int) *os.File {
	r, w, err := os.Pipe()
	if err != nil {
		return nil
	}
	cmd := exec.Command("/bin/sh", "-c", aiEngineWatchScript, "sky-ai-engine-watch", strconv.Itoa(pid))
	cmd.Stdin = r
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pid}
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil
	}
	r.Close()
	// Reap the watcher when it exits, so it never stays a zombie.
	go func() { _ = cmd.Wait() }()
	return w
}
