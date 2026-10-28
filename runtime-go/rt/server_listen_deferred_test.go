//go:build !js

package rt

// `Server.listen : Int -> List Route -> Task Error ()` must build a Task and do
// nothing else. The kernel used to bind the port and serve AT THE CALL, before
// any Task ran, so `Task.spawn (Server.listen port routes)` blocked the calling
// goroutine inside ListenAndServe: the spawn never happened and the rest of
// `main` never ran (found while testing the Task-based WebSocket client, v0.27).

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestServerListen_BuildsATaskAndDoesNotServe(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	done := make(chan any, 1)
	go func() { done <- Server_listen(port, []any{}) }()
	var v any
	select {
	case v = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Server_listen did not return: it is serving at the call instead of building a Task")
	}
	if _, ok := v.(func() any); !ok {
		t.Fatalf("Server_listen returned %T, want a Task thunk (func() any)", v)
	}
	if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		c.Close()
		t.Fatal("the port is bound before the Task ran")
	}
}

// The "listening" line is the readiness signal: `sky run`'s supervisor, the
// build-run gate and every flow test connect as soon as they read it. It used
// to be printed before the port was bound, and under load a client that read
// it was refused (v0.28.0: the `ai-device-partition` gate, right after a
// rebuild). A port that refuses the first connection after the line fails
// this test.
//
// The server runs in a child process (this test binary, re-run on
// TestServerListen_Child). A server started in this process never stops and
// keeps writing its startup report to os.Stdout, so swapping os.Stdout here
// to read the line raced with it (`go test -race`, the release gate-race).
func TestServerListen_PrintsListeningOnlyOnceBound(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestServerListen_Child$")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SKY_RT_LISTEN_CHILD_PORT=%d", port),
		// No inline console: the report is what matters, not the console.
		"SKY_CONSOLE_AUTH=off")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the child server exited without a listening line")
			}
			if !strings.Contains(strings.ToLower(line), "listening") || !strings.Contains(line, fmt.Sprintf(":%d", port)) {
				continue
			}
			c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				t.Fatalf("the server said it was listening but refused a connection: %v", err)
			}
			c.Close()
			return
		case <-deadline:
			t.Fatal("the child server printed no listening line")
		}
	}
}

// TestServerListen_Child is the server TestServerListen_PrintsListeningOnlyOnceBound
// runs in a child process. It does nothing in a normal run.
func TestServerListen_Child(t *testing.T) {
	p := os.Getenv("SKY_RT_LISTEN_CHILD_PORT")
	if p == "" {
		t.Skip("the child of TestServerListen_PrintsListeningOnlyOnceBound")
	}
	var port int
	if _, err := fmt.Sscanf(p, "%d", &port); err != nil {
		t.Fatal(err)
	}
	Server_listen(port, []any{}).(func() any)()
}
