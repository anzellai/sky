//go:build !js

package rt

// Embedded Sky.Live mode (v0.27): a Live app run as one Task inside a larger
// Task program (`Task.spawn (App.run app)` next to other work in `main`).
//
// A normal Live run owns the process: it installs a SIGINT/SIGTERM/SIGHUP
// handler whose shutdown tears down process-wide state, a second signal calls
// ExitProcess(130), a port already in use calls ExitProcess(1), and the
// console boot invariant can exit at start. None of that is acceptable for a
// guest in someone else's process. In embedded mode (`Live.withEmbedded`,
// `App.withEmbedded`) Live installs no signal handler and never exits: a
// failure to start is the Task's `Err`, and the host owns shutdown.
//
// Every check runs the app in a CHILD process (the standard re-exec helper
// pattern): a regression here means an exit or a signal handler, and neither
// can be observed safely inside the test process itself.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

const liveEmbeddedRoleEnv = "SKY_LIVE_EMBEDDED_ROLE"

// liveEmbeddedCfg is the smallest AppConfig liveAppRun accepts.
func liveEmbeddedCfg(port int, embedded bool) map[string]any {
	cfg := map[string]any{
		"Init": func(any) any {
			return SkyTuple2{V0: map[string]any{"n": 0}, V1: Cmd_none()}
		},
		"Update": func(_ any) any {
			return func(m any) any { return SkyTuple2{V0: m, V1: Cmd_none()} }
		},
		"View":          func(any) any { return nil },
		"Subscriptions": func(any) any { return Sub_none() },
		"NotFound":      "not-found",
		"Routes":        []any{},
		"Port":          port,
	}
	if embedded {
		cfg = Live_withEmbedded(cfg).(map[string]any)
	}
	return cfg
}

// TestLiveEmbeddedHelper is the re-exec target; it does nothing in a normal
// run. It starts liveAppRun and reports on stdout.
func TestLiveEmbeddedHelper(t *testing.T) {
	role := os.Getenv(liveEmbeddedRoleEnv)
	if role == "" {
		return
	}
	var port int
	fmt.Sscanf(os.Getenv("SKY_LIVE_EMBEDDED_PORT"), "%d", &port)
	if os.Getenv("SKY_LIVE_EMBEDDED_UNLINK_CONSOLE") == "1" {
		// Stand for a binary that lost the console_app blank import: no
		// inline console can mount (observability is off in this role, so
		// the legacy shell does not mount either).
		RegisterInlineConsoleCfgProvider(nil)
	}
	embedded := role != "normal"
	done := make(chan SkyResult[any, any], 1)
	go func() {
		done <- liveAppRun(liveEmbeddedCfg(port, embedded)).(SkyResult[any, any])
	}()
	switch role {
	case "embedded", "normal":
		// Wait for the listener, then announce it and park: the parent sends
		// a signal and watches how the process reacts.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
				c.Close()
				fmt.Println("LIVE_READY")
				break
			}
			select {
			case r := <-done:
				fmt.Printf("LIVE_RETURNED_EARLY tag=%d err=%v\n", r.Tag, r.ErrValue)
				os.Exit(3)
			case <-time.After(20 * time.Millisecond):
			}
		}
		select {
		case r := <-done:
			fmt.Printf("LIVE_RETURNED tag=%d\n", r.Tag)
		case <-time.After(20 * time.Second):
			fmt.Println("LIVE_STILL_RUNNING")
		}
		os.Exit(0)
	case "embedded-boot-fail":
		select {
		case r := <-done:
			if r.Tag == 0 {
				fmt.Println("LIVE_OK_UNEXPECTED")
				os.Exit(0)
			}
			fmt.Printf("LIVE_ERR %s\n", errorMessage(r.ErrValue))
			os.Exit(0)
		case <-time.After(30 * time.Second):
			fmt.Println("LIVE_NO_RESULT")
			os.Exit(0)
		}
	}
}

// errorMessage pulls the message out of a Sky Error value.
func errorMessage(e any) string {
	if adt, ok := e.(skyErrorAdt); ok && len(adt.Fields) == 2 {
		if info, ok := adt.Fields[1].(skyErrorInfo); ok {
			return info.Message
		}
	}
	return fmt.Sprintf("%v", e)
}

func freeLivePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

// startLiveChild starts the helper and waits for LIVE_READY.
func startLiveChild(t *testing.T, role string, port int, extraEnv ...string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLiveEmbeddedHelper$")
	cmd.Env = append(os.Environ(), liveEmbeddedRoleEnv+"="+role,
		fmt.Sprintf("SKY_LIVE_EMBEDDED_PORT=%d", port), "SKY_CONSOLE_AUTH=", "ENV=")
	cmd.Env = append(cmd.Env, extraEnv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out := &strings.Builder{}
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			out.WriteString(sc.Text() + "\n")
			if !sent && strings.Contains(sc.Text(), "LIVE_READY") {
				ready <- true
				sent = true
			}
		}
		if !sent {
			ready <- false
		}
	}()
	select {
	case ok := <-ready:
		if !ok {
			_ = cmd.Wait()
			t.Fatalf("the child never became ready:\n%s", out.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("timed out waiting for the child:\n%s", out.String())
	}
	return cmd, out
}

// TestLiveEmbedded_InstallsNoSignalHandler — SIGTERM to an embedded Live
// process takes Go's default action (the process dies by the signal): Live
// did not catch it. The control, a normal Live run, catches the same signal
// and shuts itself down.
func TestLiveEmbedded_InstallsNoSignalHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	t.Run("embedded: signal not caught", func(t *testing.T) {
		cmd, out := startLiveChild(t, "embedded", freeLivePort(t))
		_ = cmd.Process.Signal(syscall.SIGTERM)
		err := cmd.Wait()
		ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("embedded Live caught SIGTERM (a signal handler is installed); exit=%v\n%s", err, out.String())
		}
		if strings.Contains(out.String(), "shutting down") {
			t.Fatalf("embedded Live ran its own shutdown sequence:\n%s", out.String())
		}
	})
	t.Run("normal: signal caught (control)", func(t *testing.T) {
		cmd, out := startLiveChild(t, "normal", freeLivePort(t))
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if ws.Signaled() {
			t.Fatalf("control: a normal Live run did not catch SIGTERM:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "shutting down") {
			t.Fatalf("control: no shutdown line from a normal Live run:\n%s", out.String())
		}
	})
}

// runBootFailChild runs the helper in the boot-failure role and returns its
// output and exit error.
func runBootFailChild(t *testing.T, port int, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLiveEmbeddedHelper$")
	cmd.Env = append(os.Environ(), liveEmbeddedRoleEnv+"=embedded-boot-fail",
		fmt.Sprintf("SKY_LIVE_EMBEDDED_PORT=%d", port), "SKY_CONSOLE_AUTH=", "ENV=")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestLiveEmbedded_PortInUseIsAnErr — the port is taken: embedded Live
// returns Err from its Task (the host decides what to do) instead of calling
// ExitProcess(1).
func TestLiveEmbedded_PortInUseIsAnErr(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	out, err := runBootFailChild(t, port)
	if err != nil {
		t.Fatalf("embedded Live exited the process on a taken port (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "LIVE_ERR") || !strings.Contains(out, "already in use") {
		t.Fatalf("want LIVE_ERR naming the taken port, got:\n%s", out)
	}
}

// TestLiveEmbedded_ConsoleInvariantIsAnErr — SKY_CONSOLE_AUTH=token with no
// console mounted (the child unregisters the console provider and turns the
// legacy shell off) is a boot-invariant failure. A
// normal run exits 1; embedded Live returns it as the Task's Err.
func TestLiveEmbedded_ConsoleInvariantIsAnErr(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	out, err := runBootFailChild(t, freeLivePort(t), "SKY_CONSOLE_AUTH=token",
		"SKY_CONSOLE_TOKEN=abcdefabcdefabcdefabcdefabcdefab", "SKY_OBSERVABILITY_DISABLED=1",
		"SKY_LIVE_EMBEDDED_UNLINK_CONSOLE=1")
	if err != nil {
		t.Fatalf("embedded Live exited the process on the console invariant (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "LIVE_ERR") || !strings.Contains(out, "console") {
		t.Fatalf("want LIVE_ERR naming the console invariant, got:\n%s", out)
	}
}

// TestLiveEmbedded_UnreachableStoreInProductionIsAnErr — a configured
// session store that is unreachable in production refuses to start. A normal
// run exits (fatalfAndExit); embedded Live returns the refusal as Err.
func TestLiveEmbedded_UnreachableStoreInProductionIsAnErr(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	out, err := runBootFailChild(t, freeLivePort(t),
		"ENV=production", "SKY_LIVE_STORE=nosuchstore", "SKY_CONSOLE_AUTH=off", "SKY_ADMIN_TOKEN=x")
	if err != nil {
		t.Fatalf("embedded Live exited the process on a store refusal (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "LIVE_ERR") || !strings.Contains(out, "session store") {
		t.Fatalf("want LIVE_ERR naming the session store, got:\n%s", out)
	}
}
