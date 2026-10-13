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
func TestServerListen_PrintsListeningOnlyOnceBound(t *testing.T) {
	// No inline console: it mounts once per process, and the other
	// Server.listen tests in this package mount it.
	t.Setenv("SKY_CONSOLE_AUTH", "off")
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default:
			}
		}
	}()
	for i := 0; i < 1; i++ { // one server: the console mounts once per process
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		task := Server_listen(port, []any{}).(func() any)
		go task()
		deadline := time.After(10 * time.Second)
	wait:
		for {
			select {
			case line := <-lines:
				if strings.Contains(strings.ToLower(line), "listening") && strings.Contains(line, fmt.Sprintf(":%d", port)) {
					break wait
				}
			case <-deadline:
				os.Stdout = old
				t.Fatalf("server %d printed no listening line", i)
			}
		}
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			os.Stdout = old
			t.Fatalf("server %d said it was listening but refused a connection: %v", i, err)
		}
		c.Close()
	}
}
