//go:build !js && unix

package rt

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every engine a test starts ends with the test, and a managed engine ends
// with its holder.
//
// `TestAiOrphanedEnginesAreReapedByTheNextStart` left a fake llama-server
// running under pid 1 for about 45 hours: it forgets its engine on purpose
// (as a dead holder would) and relies on `aiReapOrphans`, so any failure
// before the reap, or a test binary killed by its timeout, left the engine
// behind. `aiTestEnv`'s cleanup stopped only the engines still in this
// process's table, and the engine's AI home was the test's own TempDir, so no
// later start ever reaped it.

const aiHolderRoleEnv = "AI_ENGINE_HOLDER_ROLE"
const aiHolderModelEnv = "AI_ENGINE_HOLDER_MODEL"

// TestAiEngineHolderHelper is the holder the watcher test kills: it starts a
// managed engine, reports its pid and waits. It does nothing when run directly.
func TestAiEngineHolderHelper(t *testing.T) {
	if os.Getenv(aiHolderRoleEnv) == "" {
		return
	}
	b := endpointBuilder("chat")
	b.hasLocal, b.local = true, localBuilder(os.Getenv(aiHolderModelEnv))
	s, err := aiSlotFor(b)
	if err != nil {
		os.Stdout.WriteString("ERR " + err.Error() + "\n")
		os.Exit(3)
	}
	if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
		os.Stdout.WriteString("ERR " + err.Error() + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("ENGINE_PID=" + strconv.Itoa(s.engine.pid) + "\n")
	os.Stdout.Sync()
	select {}
}

// A holder killed with SIGKILL runs no exit path. Its engine, in a process
// group of its own, used to keep running until the next start on the same
// AI home reaped it. The holder watcher ends it at once.
func TestAiEngineEndsWithAHolderKilledBySigkill(t *testing.T) {
	engineDir := fakeEngineDir(t, nil)
	home := t.TempDir()
	path, sum := writeModel(t, t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=^TestAiEngineHolderHelper$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		aiHolderRoleEnv+"=1",
		aiHolderModelEnv+"=file:"+path+"#sha256="+sum,
		"SKY_AI_HOME="+home,
		"SKY_AI_LLAMA_BIN="+engineDir,
		"ENV=",
		"SKY_ENV=",
	)
	out, err := cmd.StdoutPipe()
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
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "ENGINE_PID="); ok {
				n, _ := strconv.Atoi(v)
				pidCh <- n
				return
			}
			if strings.HasPrefix(sc.Text(), "ERR ") {
				t.Log(sc.Text())
			}
		}
		pidCh <- -1
	}()
	var engine int
	select {
	case engine = <-pidCh:
	case <-time.After(60 * time.Second):
		t.Fatal("the holder did not start an engine")
	}
	if engine <= 0 {
		t.Fatal("the holder did not start an engine")
	}
	// Ours to end if the watcher fails: the engine this test's holder started.
	t.Cleanup(func() {
		if processAlive(engine) {
			_ = syscall.Kill(-engine, syscall.SIGKILL)
		}
	})
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	if !aiWaitGone(engine, 10*time.Second) {
		t.Fatalf("the engine %d outlived its holder's SIGKILL", engine)
	}
}

// A test that forgets its engine (as the orphan test does) and then ends
// without reaping it must still not leave it running: aiTestEnv's cleanup
// ends every engine recorded in the test's AI home.
func TestAiTestEnvEndsAForgottenEngine(t *testing.T) {
	var orphan int
	t.Run("forget", func(t *testing.T) {
		_, model := aiTestEnv(t, nil)
		s := localSlot(t, model, nil)
		if _, err := s.post("/chat/completions", chatBody("x")); err != nil {
			t.Fatal(err)
		}
		orphan = s.engine.pid
		// The watcher would end it when this process dies; this test is
		// about the cleanup while the process lives, so forget the engine
		// the way the orphan test does and return without a reap.
		s.engine.mu.Lock()
		s.engine.retired = true
		s.engine.mu.Unlock()
		aiEnginesMu.Lock()
		aiEngines = map[string]*aiEngine{}
		aiEnginesMu.Unlock()
	})
	if orphan <= 0 {
		t.Fatal("no engine started")
	}
	t.Cleanup(func() {
		if processAlive(orphan) {
			_ = syscall.Kill(-orphan, syscall.SIGKILL)
		}
	})
	if !aiWaitGone(orphan, 15*time.Second) {
		t.Fatalf("the forgotten engine %d outlived its test", orphan)
	}
}
