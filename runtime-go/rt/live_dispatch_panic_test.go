package rt

import (
	"strings"
	"testing"
	"time"
)

// L8 regression — a recovered dispatch panic must be OBSERVABLE, not a silent
// dead button. Pre-fix the recover wrote only to stderr (no structured log, no
// errId, no user signal), so a deterministic panic for a given Msg turned that
// control into a permanent no-op with nothing an operator could grep. Post-fix
// the recover emits a structured Error log with a correlation errId and sets a
// user-visible Notification (nested-recover-guarded so it can never crash the
// recovery). Here we assert the recover doesn't crash, drops the event, and
// surfaces the notification.
func TestDispatchPanicIsObservable(t *testing.T) {
	app := &liveApp{
		update:  func(msg, model any) any { panic("boom in update()") },
		view:    func(model any) any { return velement("div", nil, nil) },
		store:   newMemoryStore(time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	init := velement("div", nil, nil)
	assignSkyIDs(&init, "r")
	sess := &liveSession{
		model:     map[string]any{"Notification": "", "NotificationType": ""},
		handlers:  map[string]any{},
		prevTree:  &init,
		sseCh:     make(chan sseFrame, 1),
		cancelSub: make(chan struct{}),
	}

	// Must not crash the process — the panic is recovered.
	body := app.dispatch(sess, "SomeMsg")
	if body != "" {
		t.Fatalf("a panicking dispatch should drop the event (empty body), got %q", body)
	}
	m, ok := sess.model.(map[string]any)
	if !ok {
		t.Fatalf("model type changed unexpectedly: %T", sess.model)
	}
	note, _ := m["Notification"].(string)
	if !strings.Contains(note, "went wrong") || !strings.Contains(note, "ref ") {
		t.Fatalf("L8: dispatch panic should set a user-visible notification with a correlation ref, got %q", note)
	}
}
