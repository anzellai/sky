//go:build !js

package rt

// `Server.listen : Int -> List Route -> Task Error ()` must build a Task and do
// nothing else. The kernel used to bind the port and serve AT THE CALL, before
// any Task ran, so `Task.spawn (Server.listen port routes)` blocked the calling
// goroutine inside ListenAndServe: the spawn never happened and the rest of
// `main` never ran (found while testing the Task-based WebSocket client, v0.27).

import (
	"fmt"
	"net"
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
