//go:build !js

package rt

import (
	"context"
	"strings"
	"testing"
	"time"
)

// C-1b: a Sky.Live `Cmd.perform` runs its Task on its own goroutine
// (`go app.runPerform(...)`). A classified panic in that Task (a division by
// zero, a CoerceFailure) had no recover on that goroutine, so it ended the
// whole server process. The perform must log the classified panic, tell the
// session's tabs that the action failed, and keep the session (and the
// server) alive.

// callRunPerform runs one perform on this goroutine and reports whether a
// panic escaped it (which, on the production `go` goroutine, ends the process).
func callRunPerform(app *liveApp, sess *liveSession, task, toMsg any) (escaped any) {
	defer func() { escaped = recover() }()
	app.runPerform(sess, task, toMsg, context.Background())
	return nil
}

func TestRunPerform_TaskPanicKeepsTheSession(t *testing.T) {
	app := performTestApp(func(model any) any {
		return velement("div", nil, []any{vtext(AsString(model))})
	})
	app.update = func(msg, model any) any {
		return SkyTuple2{V0: msg, V1: cmdT{kind: "none"}}
	}
	sess := performTestSession(app)
	sess.done = make(chan struct{})

	panicking := func() any { panic("runtime error: integer divide by zero") }
	toMsg := func(r any) any { return "never" }
	if esc := callRunPerform(app, sess, panicking, toMsg); esc != nil {
		t.Fatalf("a panic in a Cmd.perform Task escaped runPerform (it would end the server): %v", esc)
	}

	// The tabs are told the action failed.
	select {
	case f := <-sess.sseCh:
		if f.event != "skyerror" || !strings.Contains(f.data, "ref") {
			t.Fatalf("first frame after a perform panic: %q %q, want a skyerror frame", f.event, f.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no skyerror frame after a perform panic")
	}

	// The session still works: the next perform updates the model.
	ok := func() any { return Ok[any, any]("after") }
	toMsg2 := func(r any) any { return "after" }
	if esc := callRunPerform(app, sess, ok, toMsg2); esc != nil {
		t.Fatalf("second perform panicked: %v", esc)
	}
	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()
	if model != "after" {
		t.Fatalf("session model after a recovered perform panic: %v, want \"after\"", model)
	}
}

// A panic in toMsg (the Result -> Msg function) is the same class.
func TestRunPerform_ToMsgPanicKeepsTheSession(t *testing.T) {
	app := performTestApp(func(model any) any {
		return velement("div", nil, []any{vtext("x")})
	})
	sess := performTestSession(app)
	sess.done = make(chan struct{})
	task := func() any { return Ok[any, any](1) }
	toMsg := func(r any) any { panic("CoerceFailure: source int cannot be cast to target string") }
	if esc := callRunPerform(app, sess, task, toMsg); esc != nil {
		t.Fatalf("a panic in a Cmd.perform toMsg escaped runPerform: %v", esc)
	}
}
