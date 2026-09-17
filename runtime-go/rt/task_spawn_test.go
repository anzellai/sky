package rt

import (
	"sync"
	"testing"
	"time"
)

// Task_spawn must (a) be a Task Error () in shape, (b) run the given task on a
// background goroutine, and (c) return AT ONCE without waiting for that task.
// This is what lets the desktop runner keep the main goroutine (and its main OS
// thread) free to create the native webview while the Live server runs.
func TestTaskSpawn_RunsInBackgroundAndReturnsImmediately(t *testing.T) {
	if !isTaskShape(Task_spawn(AnyTaskSucceed(any(0)))) {
		t.Fatalf("Task_spawn: kernel must be Task Error ()")
	}

	// A task whose body blocks until we release it, and records that it ran.
	release := make(chan struct{})
	var ran sync.WaitGroup
	ran.Add(1)
	blocking := func() any {
		ran.Done()
		<-release // hold the spawned goroutine open
		return Ok[any, any](struct{}{})
	}

	// Forcing the spawn thunk must NOT block on `blocking`.
	done := make(chan any, 1)
	go func() { done <- Task_spawn(any(blocking)).(func() any)() }()

	select {
	case res := <-done:
		// Returned promptly with Ok(unit) while the spawned task is still blocked.
		tag, _, _ := anyResultView(res)
		if tag != 0 {
			t.Fatalf("Task_spawn: expected Ok, got tag %d", tag)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Task_spawn blocked on the spawned task instead of returning at once")
	}

	// The spawned task really did start on its own goroutine.
	waitDone := make(chan struct{})
	go func() { ran.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Task_spawn never ran the task on a background goroutine")
	}
	close(release)
}

// A panic inside the spawned task must be recovered, never crash the process.
func TestTaskSpawn_RecoversPanic(t *testing.T) {
	done := make(chan struct{})
	panicky := func() any {
		defer close(done)
		panic("boom from a background task")
	}
	// Must not panic out of the returned thunk.
	res := Task_spawn(any(panicky)).(func() any)()
	if tag, _, _ := anyResultView(res); tag != 0 {
		t.Fatalf("Task_spawn: expected Ok on spawn, got tag %d", tag)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("spawned task did not run")
	}
}
