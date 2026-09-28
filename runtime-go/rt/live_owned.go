//go:build !js

package rt

// live_owned.go — Sky.Live's side of runtime-owned resources and source
// subscriptions.
//
//   - owned: a child process or file watcher started while a session was in
//     scope belongs to that session and is released when the session ends
//     (markDone: eviction, delete, rotation-out, App.stop / Live.stop).
//   - activeSourceSubs: the running "subscribeSource" leaves (Process.events,
//     Watch.changes), reconciled after every dispatch like the WebSocket and
//     Http.Stream leaves. A runner delivers through deliverSubMsg, the same
//     dispatch + SSE-frame path the other external subscriptions take.

import (
	"context"
	"fmt"
)

// addOwned records a resource the session must release when it ends. A
// session that has already ended releases it at once.
func (s *liveSession) addOwned(key string, release func()) {
	s.ownedMu.Lock()
	if s.isDone() {
		s.ownedMu.Unlock()
		release()
		return
	}
	if s.owned == nil {
		s.owned = map[string]func(){}
	}
	s.owned[key] = release
	s.ownedMu.Unlock()
}

// removeOwned forgets a resource its owner already released.
func (s *liveSession) removeOwned(key string) {
	s.ownedMu.Lock()
	delete(s.owned, key)
	s.ownedMu.Unlock()
}

// releaseOwned releases every owned resource. Called from markDone.
func (s *liveSession) releaseOwned() {
	s.ownedMu.Lock()
	list := make([]func(), 0, len(s.owned))
	for _, fn := range s.owned {
		list = append(list, fn)
	}
	s.owned = nil
	s.ownedMu.Unlock()
	for _, fn := range list {
		func() {
			defer func() {
				if r := recover(); r != nil {
					LogRecoveredPanic("sky.live", "release of a session-owned resource", r)
				}
			}()
			fn()
		}()
	}
}

// isDone reports whether markDone already ran.
func (s *liveSession) isDone() bool {
	if s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// stopAllSourceSubs cancels every running source subscription. Non-blocking:
// each runner stops reading and releases its source on its own goroutine.
func (s *liveSession) stopAllSourceSubs() {
	s.activeSourceSubsMu.Lock()
	runners := make([]*sourceRunner, 0, len(s.activeSourceSubs))
	for _, r := range s.activeSourceSubs {
		runners = append(runners, r)
	}
	s.activeSourceSubs = nil
	s.activeSourceSubsMu.Unlock()
	for _, r := range runners {
		r.cancel()
	}
}

// applySourceSubsDiff reconciles the session's running source subscriptions
// against this dispatch's `subscriptions model`: a removed key is cancelled,
// a kept key keeps its runner (and its position in the stream) with the
// latest toMsg, a new key claims its source and starts a runner. A nil map
// cancels everything.
func (app *liveApp) applySourceSubsDiff(sess *liveSession, desired map[string]subT) {
	sess.activeSourceSubsMu.Lock()
	var cancel []*sourceRunner
	for key, r := range sess.activeSourceSubs {
		leaf, keep := desired[key]
		select {
		case <-r.done:
			// The runner ended on its own (its source ended); forget it so
			// a leaf that is still requested starts afresh below.
			delete(sess.activeSourceSubs, key)
			continue
		default:
		}
		if keep {
			if src, ok := subSourceOf(leaf); ok && src == r.src {
				r.setToMsg(leaf.toMsg)
				continue
			}
		}
		cancel = append(cancel, r)
		delete(sess.activeSourceSubs, key)
	}
	var added []subT
	for key, leaf := range desired {
		if _, running := sess.activeSourceSubs[key]; !running {
			added = append(added, leaf)
		}
	}
	sess.activeSourceSubsMu.Unlock()

	for _, r := range cancel {
		r.cancel()
	}
	if sess.isDone() {
		return
	}
	parentCtx := CurrentTraceContext()
	for _, leaf := range added {
		src, ok := subSourceOf(leaf)
		if !ok {
			continue
		}
		key := leaf.sourceKey
		r, err := startSourceRunner(key, src, leaf.toMsg,
			func(msg any, stop <-chan struct{}) bool {
				return app.deliverSourceMsg(sess, msg, stop, parentCtx)
			}, nil)
		if err != nil {
			logOnce("source-sub-refused-"+key, func() {
				fmt.Printf("[sky.sub] subscription %s ignored: %v\n", key, err)
			})
			continue
		}
		sess.activeSourceSubsMu.Lock()
		if sess.activeSourceSubs == nil {
			sess.activeSourceSubs = map[string]*sourceRunner{}
		}
		sess.activeSourceSubs[key] = r
		sess.activeSourceSubsMu.Unlock()
		if sess.isDone() {
			// markDone ran while this runner started: it missed the sweep.
			r.cancel()
		}
	}
}

// deliverSourceMsg runs one Msg from a source subscription through the
// session's update and ships the resulting frame.
func (app *liveApp) deliverSourceMsg(sess *liveSession, msg any, stop <-chan struct{}, parentCtx context.Context) bool {
	select {
	case <-stop:
		return false
	default:
	}
	if sess.isDone() {
		return false
	}
	RunWithTraceContext(parentCtx, func() {
		runWithLiveSession(sess, func() {
			app.deliverSubMsg(sess, msg)
		})
	})
	return true
}

// deliverSubMsg dispatches one Msg that arrived from outside a request (a
// source, WebSocket or Http.Stream subscription) and ships the frame. A
// frame the SSE channel cannot take is dropped, counted, and marks every
// connection out of sync so the client resyncs instead of silently missing
// it.
func (app *liveApp) deliverSubMsg(sess *liveSession, msg any) {
	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTreeBeforeDispatch := sess.prevTree
	body := app.dispatch(sess, msg)
	newTreeAfterDispatch := sess.prevTree
	var snap frameSnapshot
	var patches []Patch
	var haveFrame bool
	if body != "" && body != prevShipped {
		snap = sess.prepareFrameSnapshot(body)
		sess.lastShippedBody = body
		if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
			patches = liveDiff(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	// L7: persist the model this delivery changed.
	app.persistSession(sess)
	if !haveFrame {
		return
	}
	frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
	select {
	case sess.sseCh <- frame:
	default:
		recordSseDrop(sess.currentSID())
		sess.markAllConnsOutOfSync() // #9: ingress drop — every connection missed this frame
	}
}
