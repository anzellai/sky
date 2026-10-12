//go:build !js && unix

package rt

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const childSignalRoleEnv = "SKY_TEST_CHILD_SIGNAL_ROLE"

// TestChildSignalHelper is the subprocess the tests below signal. It does
// nothing when run directly.
func TestChildSignalHelper(t *testing.T) {
	role := os.Getenv(childSignalRoleEnv)
	if role == "" {
		return
	}
	spawn := func() {
		h, err := spawnProcess(procSpec{program: "/bin/sleep", args: []string{"300"}, ringSize: 4096})
		if err != nil {
			fmt.Printf("SPAWN_ERR %v\n", err)
			os.Exit(3)
		}
		fmt.Printf("CHILD_PID=%d\n", h.pid)
	}
	switch role {
	case "none":
		fmt.Println("CHILD_PID=0")
	case "spawn":
		spawn()
	case "spawn-wedged":
		// An owner whose stop never returns: a second signal must still end
		// the process at once.
		registerBeforeChildKill("test.wedged", func() { select {} })
		spawn()
	case "spawn-owner":
		// An app shape that owns the termination sequence: its handler runs
		// and decides the exit status, the lazy handler stays out of it.
		release := acquireSignalOwner()
		defer release()
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		spawn()
		go func() {
			<-ch
			killAllChildProcesses()
			os.Exit(7)
		}()
	}
	os.Stdout.Sync()
	select {}
}

// startChildSignalHelper runs the helper in role and returns it with the pid
// of the child it spawned (0 for role "none").
func startChildSignalHelper(t *testing.T, role string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildSignalHelper$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), childSignalRoleEnv+"="+role)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	pidCh := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "CHILD_PID="); ok {
				n, _ := strconv.Atoi(v)
				pidCh <- n
				return
			}
		}
		pidCh <- -1
	}()
	select {
	case pid := <-pidCh:
		if pid < 0 {
			t.Fatalf("helper %q printed no CHILD_PID", role)
		}
		if pid > 0 {
			// The child is ours to end if the helper left it behind.
			t.Cleanup(func() {
				if processAlive(pid) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
		}
		return cmd, pid
	case <-time.After(30 * time.Second):
		t.Fatalf("helper %q did not start", role)
	}
	return nil, 0
}

func waitHelper(t *testing.T, cmd *exec.Cmd, budget time.Duration) syscall.WaitStatus {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("helper did not exit within %s", budget)
	}
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ws
}

func waitPidGone(pid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processAlive(pid)
}

// TestChildSignal_SigtermEndsTheChildren is the regression for `sky ai start`
// orphaning its engine. A program with a managed child that receives SIGTERM
// must end the child and exit 143. Before the fix it had no handler: the
// signal killed it at once and the child kept running under pid 1.
func TestChildSignal_SigtermEndsTheChildren(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGTERM, 143}, {syscall.SIGINT, 130}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			cmd, child := startChildSignalHelper(t, "spawn")
			if !processAlive(child) {
				t.Fatalf("child %d is not running", child)
			}
			_ = cmd.Process.Signal(tc.sig)
			ws := waitHelper(t, cmd, 20*time.Second)
			if ws.Signaled() || ws.ExitStatus() != tc.code {
				t.Errorf("helper exit: signaled=%v (%v) status=%d, want exit %d",
					ws.Signaled(), ws.Signal(), ws.ExitStatus(), tc.code)
			}
			if !waitPidGone(child, 5*time.Second) {
				t.Fatalf("child %d still runs after its parent took %v", child, tc.sig)
			}
		})
	}
}

// TestChildSignal_NoChildrenKeepsTheDefault: a program that never starts a
// child installs no handler, so SIGTERM keeps its default action.
func TestChildSignal_NoChildrenKeepsTheDefault(t *testing.T) {
	cmd, _ := startChildSignalHelper(t, "none")
	_ = cmd.Process.Signal(syscall.SIGTERM)
	ws := waitHelper(t, cmd, 20*time.Second)
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("a program without children did not keep the default SIGTERM action: signaled=%v status=%d",
			ws.Signaled(), ws.ExitStatus())
	}
}

// TestChildSignal_SecondSignalExitsAtOnce: when the cleanup hangs, a second
// signal ends the process at once.
func TestChildSignal_SecondSignalExitsAtOnce(t *testing.T) {
	cmd, _ := startChildSignalHelper(t, "spawn-wedged")
	_ = cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)
	if cmd.ProcessState != nil {
		t.Fatal("helper exited although its cleanup is wedged")
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	ws := waitHelper(t, cmd, 5*time.Second)
	if ws.Signaled() || ws.ExitStatus() != 143 {
		t.Fatalf("second signal: signaled=%v status=%d, want exit 143", ws.Signaled(), ws.ExitStatus())
	}
}

// TestChildSignal_DefersToAnOwner: while an app shape owns the termination
// sequence, the lazy handler does not act: the owner's exit status wins.
func TestChildSignal_DefersToAnOwner(t *testing.T) {
	cmd, child := startChildSignalHelper(t, "spawn-owner")
	_ = cmd.Process.Signal(syscall.SIGTERM)
	ws := waitHelper(t, cmd, 20*time.Second)
	if ws.Signaled() || ws.ExitStatus() != 7 {
		t.Fatalf("owner did not decide the exit: signaled=%v status=%d, want 7", ws.Signaled(), ws.ExitStatus())
	}
	if !waitPidGone(child, 5*time.Second) {
		t.Fatalf("child %d still runs", child)
	}
}
