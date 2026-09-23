//go:build !js

// teaLoop — the shared update core of the terminal TEA backends
// (Sky.Cli, Sky.Tui program, Sky.Tui app) and Sky.Webview.
//
// It owns the parts every single-process loop must get the same way:
//
//   - guard: `withGuard` runs BEFORE update on every backend. A rejected
//     Msg skips update and stamps `notification` / `notificationType`
//     on the model (when the model has those fields), the same contract
//     Sky.Live applies.
//   - Cmd execution: Cmd.perform runs its Task on a goroutine and feeds
//     the resulting Msg back; Cmd.publish delivers to this app's own
//     Sub.subscribeTopic subscriber (the in-process bus of a single
//     program); Cmd.publishNoEcho is a no-op here, because the only
//     subscriber is the publisher itself.
//   - in-flight accounting: every perform / publish is counted until its
//     Msg is queued, so a loop can tell "nothing left to happen" apart
//     from "a result is still on its way" (Sky.Cli exits on stdin EOF
//     only once the in-flight work has landed).
//   - durable snapshots after each update.

package rt

import (
	"sync/atomic"
)

// teaTopicMsg carries a published payload to the loop. The loop resolves
// it against the CURRENT topic subscriptions when it dequeues it.
type teaTopicMsg struct {
	topic   string
	payload any
}

type teaLoop struct {
	msgCh    chan any
	subs     *subManager
	updateFn any
	guardFn  any
	dur      *durableCtx
	inflight atomic.Int64
	// wake is signalled (non-blocking) whenever an in-flight effect
	// finishes, so a loop waiting to exit re-checks idleness.
	wake chan struct{}
}

func newTeaLoop(msgCh chan any, updateFn, guardFn any, dur *durableCtx) *teaLoop {
	return &teaLoop{
		msgCh:    msgCh,
		subs:     newSubManager(msgCh),
		updateFn: updateFn,
		guardFn:  guardFn,
		dur:      dur,
		wake:     make(chan struct{}, 1),
	}
}

func (l *teaLoop) effectDone() {
	l.inflight.Add(-1)
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// send queues msg, tolerating a loop that has already gone away.
func (l *teaLoop) send(msg any) {
	defer func() { _ = recover() }()
	l.msgCh <- msg
}

// runCmd executes a Cmd value.
func (l *teaLoop) runCmd(cmd any) {
	c, ok := cmd.(cmdT)
	if !ok {
		return
	}
	switch c.kind {
	case "batch":
		for _, sub := range c.batch {
			l.runCmd(sub)
		}
	case "perform":
		// safeGo: a panic inside the user's Task or its toMsg handler
		// won't bypass the deferred terminal restore.
		l.inflight.Add(1)
		task, toMsg := c.task, c.toMsg
		safeGo("Cmd.perform task", func() {
			defer l.effectDone()
			result := sky_call(task, nil)
			if msg := sky_call(toMsg, result); msg != nil {
				l.send(msg)
			}
		})
	case "publish":
		// Delivered through msgCh so it is ordered after the update that
		// published it and resolved against the subscriptions of the
		// model current at delivery time.
		l.inflight.Add(1)
		m := teaTopicMsg{topic: c.topic, payload: c.payload}
		safeGo("Cmd.publish", func() {
			defer l.effectDone()
			l.send(m)
		})
	case "publishNoEcho":
		// The only subscriber of a single-process program is the
		// publisher, which publishNoEcho skips by definition.
	}
}

// resolve turns a queued value into the Msg to apply. A published payload
// becomes the subscriber's Msg; a payload on a topic nobody subscribes to
// is dropped (ok=false), exactly as a Sky.Live broker with no subscriber.
func (l *teaLoop) resolve(msg any) (any, bool) {
	tm, isTopic := msg.(teaTopicMsg)
	if !isTopic {
		return msg, msg != nil
	}
	handler := l.subs.topicHandler(tm.topic)
	if handler == nil {
		return nil, false
	}
	out := tm.payload
	if isFunc(handler) {
		out = sky_call(handler, tm.payload)
	} else {
		out = handler
	}
	return out, out != nil
}

// apply runs guard + update for one Msg, executes the resulting Cmd and
// snapshots a durable model. Returns the new model.
func (l *teaLoop) apply(msg, model any) any {
	if l.guardFn != nil && isFunc(l.guardFn) {
		g := sky_call2(l.guardFn, msg, model)
		if isErrResult(g) {
			reason := extractErrResultValue(g)
			// RecordUpdate is a no-op when the field doesn't exist on
			// the model (the app opts in by declaring the fields).
			return RecordUpdate(model, map[string]any{
				"Notification":     reason,
				"NotificationType": "error",
			})
		}
	}
	// Tier-1 auto-trace: each Msg is one interaction, the ROOT span of
	// its trace in a terminal app.
	return WithMsgSpan(msgDisplayName(msg), func() any {
		res := SkyCall(l.updateFn, msg, model)
		newModel := tupleFirst(res)
		if cmd := tupleSecond(res); cmd != nil {
			l.runCmd(cmd)
		}
		l.dur.persistFixed(newModel)
		return newModel
	})
}

// idle reports that no Msg is queued and no effect is in flight.
func (l *teaLoop) idle() bool {
	return l.inflight.Load() == 0 && len(l.msgCh) == 0
}
