//go:build !js && (linux || darwin)

package rt

import (
	"os/exec"
	"testing"
	"time"
)

// TestProcessAliveTreatsAZombieAsGone: a process that has exited but that
// its parent has not reaped yet is a zombie. kill(pid, 0) still succeeds on
// it, so processAlive used to answer "alive". `sky ai stop` asks the holder
// (`sky ai start`, a process it did not start and cannot reap) to exit, then
// waits for it with aiWaitGone: the holder exited at once, but as a zombie
// of its own parent it counted as alive, and every stop waited out the full
// 10 s budget. A zombie runs nothing and holds no port, data directory or
// engine, so it is gone.
func TestProcessAliveTreatsAZombieAsGone(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Wait() // reap it at the end

	// The child exits at once. Not waiting for it leaves a zombie.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !procIsZombie(pid) {
		time.Sleep(10 * time.Millisecond)
	}
	if !procIsZombie(pid) {
		t.Fatalf("pid %d did not become a zombie within 5s", pid)
	}
	if processAlive(pid) {
		t.Fatalf("processAlive(%d) = true for an exited, unreaped child (a zombie)", pid)
	}
	if !aiWaitGone(pid, 2*time.Second) {
		t.Fatalf("aiWaitGone(%d) waited out its budget on a zombie", pid)
	}
}

// TestProcessAliveStillSeesARunningProcess guards the other direction: a
// live child is alive, and is not a zombie.
func TestProcessAliveStillSeesARunningProcess(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	if !processAlive(cmd.Process.Pid) || procIsZombie(cmd.Process.Pid) {
		t.Fatalf("a running child is reported gone (alive=%v zombie=%v)",
			processAlive(cmd.Process.Pid), procIsZombie(cmd.Process.Pid))
	}
}
