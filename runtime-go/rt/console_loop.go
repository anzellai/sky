// console_loop.go — inline console TEA update loop (v0.16.1 PR 8-B).
//
// PR 3 (v0.16.1) shipped an isolated SSE channel + POST endpoint for
// /_sky/console. PR 8 attaches a producer to that channel: a
// goroutine that:
//
//   1. Reads ConsoleEvent values off `consoleSSE.eventCh` (POSTed by
//      the browser via /_sky/console/_event).
//   2. Resolves the per-tab consoleLoopSession (model + prev tree)
//      keyed on the SSE cookie sid.
//   3. Decodes the wire payload into a Msg value via console_app's
//      RegisterConsoleAppHooks-supplied DecodeMsg (or falls back to
//      the generic SkyADT-from-name path).
//   4. Calls hooks.Update(msg, sess.model) → (newModel, cmd).
//   5. Renders hooks.View(newModel) and diffs against the previous
//      view's VNode tree to produce a patches list.
//   6. Broadcasts the resulting frame via ConsoleSSEBroadcast — same
//      `event: patches` wire shape Sky.Live uses, so the client-side
//      __skyApplyPatches mechanism is reusable verbatim.
//   7. Walks the returned Cmd:
//      - `none` → no-op.
//      - `batch` → recurse into each child.
//      - `perform task toMsg` → spawn a goroutine running the Task;
//        on completion the result is wrapped in toMsg and fed back
//        through Step 4 (via dispatchConsoleMsg → another iteration
//        of the loop).
//      - `publish` / `publishNoEcho` → ignored (pub/sub belongs to
//        the host app's broker, not the console plane).
//
// SESSION ISOLATION. The loop maintains its OWN session map
// (consoleLoopSessions) keyed on the SSE-channel cookie sid. This is
// independent of:
//   - The host app's liveStore (`sky_sid` cookie, user pages).
//   - The PR3 consoleSSE.sessions map (SSE channel transport state,
//     no model attached). PR3's map stores the broadcast write-end;
//     we treat it as the "is this sid known?" / fan-out registry.
//
// SYNC vs ASYNC. update + view + diff + broadcast run synchronously
// on the loop goroutine — same shape as Sky.Live's runPerform path.
// Cmd.perform's Task is spawned in a per-cmd goroutine; its result
// Msg is fed back through dispatchConsoleMsg, which serializes with
// other dispatch via the per-session lock. This keeps the wire-
// ordering invariant the client relies on (server seq ≤ frame seq).

package rt

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// consoleLoopSession tracks one inline-console tab's model + render
// state. Independent of consoleSSE.sessions (PR3) which holds only
// the SSE channel for outbound frames.
//
// The mutex serialises Update calls against concurrent dispatches
// from the SSE event channel + Cmd-spawned goroutines (each
// runConsoleTask fold-back). Without it a Tick dispatch + a Click
// dispatch racing for the same session would clobber model state.
type consoleLoopSession struct {
	sid      string
	mu       sync.Mutex
	model    any            // opaque to rt; State_Model_R inside console_app
	prevTree *VNode         // last rendered + assigned VNode for diff baseline
	prevBody string         // raw HTML — full-snapshot fallback when prev is nil
	handlers map[string]any // per-hid typed Msg lookup; populated per render
	seq      int64          // monotonic frame seq for this session
	closed   atomic.Bool
}

// nextSeq returns the next frame seq under the session lock. Caller
// MUST hold sess.mu.
func (s *consoleLoopSession) nextSeq() int64 {
	s.seq++
	return s.seq
}

// consoleLoopState carries the update-loop's globals. Kept in a
// dedicated struct so test resets are deterministic.
type consoleLoopState struct {
	mu       sync.RWMutex
	sessions map[string]*consoleLoopSession
	started  atomic.Bool
	stop     chan struct{}
	wg       sync.WaitGroup
}

var consoleLoop = &consoleLoopState{
	sessions: map[string]*consoleLoopSession{},
	stop:     make(chan struct{}),
}

// ResetConsoleLoopStateForTesting wipes the session map + stops any
// running goroutine. Test-only.
func ResetConsoleLoopStateForTesting() {
	consoleLoop.mu.Lock()
	wasStarted := consoleLoop.started.Load()
	consoleLoop.sessions = map[string]*consoleLoopSession{}
	if wasStarted {
		close(consoleLoop.stop)
	}
	consoleLoop.started.Store(false)
	consoleLoop.stop = make(chan struct{})
	consoleLoop.mu.Unlock()
	if wasStarted {
		consoleLoop.wg.Wait()
	}
}

// StartConsoleUpdateLoop spawns the goroutine that reads
// ConsoleEventChannel + applies update + broadcasts. Idempotent;
// safe to call multiple times.
//
// Returns immediately. The goroutine runs until ResetConsoleLoopStateForTesting
// is called (test path) or the process exits (production path —
// the goroutine is a daemon, no shutdown hook).
//
// Pre-conditions checked by caller (startConsoleUpdateLoopFromMount):
//   - ConsoleAppHooksRegistered() — otherwise nothing to dispatch into.
//   - ConsoleEventChannel() != nil — i.e. MountConsoleSSE has run.
func StartConsoleUpdateLoop() {
	if !consoleLoop.started.CompareAndSwap(false, true) {
		return
	}
	consoleLoop.wg.Add(1)
	go func() {
		defer consoleLoop.wg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				// Loop panic = compiler-bug-level event. Log loudly
				// + reset started so a future MountConsoleSSE call
				// can re-spawn.
				fmt.Printf("[sky.console] update loop panic recovered: %v\n", rec)
				consoleLoop.started.Store(false)
			}
		}()
		runConsoleUpdateLoop()
	}()
}

// runConsoleUpdateLoop is the goroutine body. Drains
// ConsoleEventChannel + dispatches each event through update + diff
// + broadcast. Exits on stop signal.
func runConsoleUpdateLoop() {
	ch := ConsoleEventChannel()
	if ch == nil {
		// MountConsoleSSE not yet wired — exit silently. Caller
		// re-spawns when wire surface comes up.
		consoleLoop.started.Store(false)
		return
	}
	consoleLoop.mu.RLock()
	stop := consoleLoop.stop
	consoleLoop.mu.RUnlock()
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			handleConsoleEventDispatch(ev)
		}
	}
}

// handleConsoleEventDispatch is the per-event entry into the TEA
// loop. Defers panic recovery so a bad Msg shape doesn't kill the
// goroutine; logs + drops the event instead.
//
// v0.16.1 PR9 dispatch order:
//
//  1. **Hid lookup (preferred)**. If `ev.Hid` is set AND the session's
//     handlers map (populated at last render) has a matching entry,
//     use that typed Msg directly. No wire-decode required — the
//     Msg is the value the renderer originally captured for that
//     element + event. Bypasses the brittle "name + args" wire
//     shape entirely.
//  2. **Name+args wire fallback**. For admin-tool smoke probes
//     posting `{msg, args}` without a hid (and as a fallback when
//     the hid is unknown — e.g. the user clicked an element that
//     was rendered in a previous frame but no longer exists in the
//     current render), drop into decodeConsoleEventMsg.
//
// The session is resolved BEFORE hid lookup so we have access to
// the handlers map.
func handleConsoleEventDispatch(ev ConsoleEvent) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Printf("[sky.console] event dispatch panic recovered: %v (sid=%s)\n", rec, ev.SessionID)
		}
	}()
	hooks, ok := loadConsoleAppHooks()
	if !ok {
		return
	}
	sess := getOrCreateConsoleLoopSession(ev.SessionID, hooks)

	if ev.Hid != "" {
		if msg, found := lookupHandlerMsg(sess, ev.Hid); found {
			dispatchConsoleMsg(sess, msg, hooks)
			return
		}
		// Unknown hid — log + fall through to the name+args fallback
		// (admin smoke probes may post a hid AND name). If the
		// fallback also fails, the event is dropped silently.
		fmt.Printf("[sky.console] hid %q not in session handlers (sid=%s); trying name fallback\n", ev.Hid, ev.SessionID)
	}

	msg, decoded := decodeConsoleEventMsg(ev, hooks)
	if !decoded {
		return
	}
	dispatchConsoleMsg(sess, msg, hooks)
}

// lookupHandlerMsg fetches the typed Msg for a hid under the session
// lock. Returns (nil, false) when the hid isn't in the current
// handlers map (e.g. the rendered tree shifted between client
// observation and POST arrival).
func lookupHandlerMsg(sess *consoleLoopSession, hid string) (any, bool) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.handlers == nil {
		return nil, false
	}
	msg, ok := sess.handlers[hid]
	return msg, ok
}

// decodeConsoleEventMsg turns a wire envelope into a typed Msg value.
//
// Wire shape (settled in PR8-C client JS):
//
//	{"msg": "<MsgCtorName>", "args": [<arg0>, <arg1>, ...]}
//
// where args are wire-encoded primitives or records (JSON shapes).
//
// Resolution order:
//  1. If hooks.DecodeMsg is non-nil, delegate to it. console_app
//     can route typed-record Args through the same shape live.go
//     uses for HTTP /_sky/event (decodeMsgArg + applyMsgArgs).
//  2. Fallback: build a SkyADT directly from the name via
//     LookupAdtTag — same path live.go's dispatchEventJSON uses for
//     direct-send (no handler ID).
//
// Returns (msg, false) when neither path resolves — caller drops
// silently after a structured log line.
func decodeConsoleEventMsg(ev ConsoleEvent, hooks *ConsoleAppHooks) (any, bool) {
	if hooks.DecodeMsg != nil {
		msg, ok := hooks.DecodeMsg(ev.Payload)
		if ok {
			return msg, true
		}
	}
	// Generic fallback. Wire payload shape:
	//   {"msg": "<Ctor>", "args": [...]}
	rawName, _ := ev.Payload["msg"].(string)
	if rawName == "" {
		return nil, false
	}
	tag, found := LookupAdtTag(rawName)
	if !found {
		// Sentinel msg names are swallowed silently — they're
		// liveness probes / framework book-keeping, not user-
		// authored Msgs.
		if len(rawName) >= 5 && rawName[:5] == "__sky" {
			return nil, false
		}
		fmt.Printf("[sky.console] unknown Msg constructor %q (sid=%s); dropping event\n", rawName, ev.SessionID)
		return nil, false
	}
	var fields []any
	if rawArgs, ok := ev.Payload["args"].([]any); ok {
		fields = append(fields, rawArgs...)
	}
	return SkyADT{Tag: tag, SkyName: rawName, Fields: fields}, true
}

// getOrCreateConsoleLoopSession returns the existing session for sid
// OR initialises a new one by calling hooks.InitFromRequest. The
// init Cmd is run synchronously to fire the initial fetches; its
// resulting Msgs feed back through dispatchConsoleMsg.
func getOrCreateConsoleLoopSession(sid string, hooks *ConsoleAppHooks) *consoleLoopSession {
	consoleLoop.mu.RLock()
	if sess, ok := consoleLoop.sessions[sid]; ok {
		consoleLoop.mu.RUnlock()
		return sess
	}
	consoleLoop.mu.RUnlock()
	consoleLoop.mu.Lock()
	defer consoleLoop.mu.Unlock()
	if sess, ok := consoleLoop.sessions[sid]; ok {
		return sess
	}
	// Build the starter model. InitFromRequest may panic on the
	// generated code path; recover so the loop survives.
	var model, cmd any
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("[sky.console] init_ panic recovered: %v (sid=%s)\n", rec, sid)
			}
		}()
		req := map[string]any{"path": "/_sky/console", "query": ""}
		model, cmd = hooks.InitFromRequest(req)
	}()
	sess := &consoleLoopSession{
		sid:   sid,
		model: model,
	}
	consoleLoop.sessions[sid] = sess
	// Fire the initial Cmd async — let the session register first.
	if cmd != nil {
		go runConsoleCmd(sess, cmd, hooks)
	}
	return sess
}

// dispatchConsoleMsg runs one update step + broadcasts + spawns any
// Cmd. Holds sess.mu across update + render so concurrent dispatches
// can't interleave model mutations. Cmd execution is deferred until
// AFTER sess.mu is released.
func dispatchConsoleMsg(sess *consoleLoopSession, msg any, hooks *ConsoleAppHooks) {
	if sess.closed.Load() {
		return
	}
	var pendingCmd any
	var pendingFrame []byte
	func() {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("[sky.console] update panic recovered: %v (sid=%s)\n", rec, sess.sid)
			}
		}()
		newModel, cmd := hooks.Update(msg, sess.model)
		sess.model = newModel
		pendingCmd = cmd
		// Render new view → VNode → diff against prev.
		//
		// v0.16.1 PR9: the renderer's handlers map MUST be captured
		// (not discarded into a "dummy") so the next click on a
		// hid-tagged element can resolve to its typed Msg. We swap
		// it onto the session AFTER a successful render so a panic
		// half-way through doesn't leave the session with a partial
		// map that would later fail every hid lookup.
		var newTree *VNode
		var newBody string
		var newHandlers map[string]any
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					fmt.Printf("[sky.console] view panic recovered: %v (sid=%s)\n", rec, sess.sid)
				}
			}()
			htmlVal := hooks.View(newModel)
			vn := HtmlToVNode(htmlVal)
			assignSkyIDs(&vn, "console")
			applyStyleInjections(&vn)
			handlers := map[string]any{}
			body := renderVNode(vn, handlers)
			newTree = &vn
			newBody = body
			newHandlers = handlers
		}()
		if newTree == nil {
			// view panicked; bail without broadcasting a stale
			// frame.
			return
		}
		seq := sess.nextSeq()
		pendingFrame = buildConsoleSSEFrame(sess, newTree, newBody, seq)
		sess.prevTree = newTree
		sess.prevBody = newBody
		sess.handlers = newHandlers
	}()
	if pendingFrame != nil {
		ConsoleSSEBroadcast(pendingFrame)
	}
	if pendingCmd != nil {
		go runConsoleCmd(sess, pendingCmd, hooks)
	}
}

// buildConsoleSSEFrame produces the wire-formatted SSE frame for the
// just-computed view. Reuses the host's `event: patches` shape via
// encodePatchesEventFromSnapshot so __skyApplyPatches consumes it
// without modification.
//
// Frame format (matches host's chooseSSEFrame contract):
//
//   - First render OR prev tree absent → `event: patch` with full
//     body wrapped in the standard {seq, body, ackInputs} envelope.
//     Client renders via __skyPatch innerHTML replace.
//   - Subsequent renders → `event: patches` with structural diff
//     produced by diffTrees. Client consumes via __skyApplyPatches.
//
// Caller MUST hold sess.mu. The frame is enqueued for broadcast on
// release.
func buildConsoleSSEFrame(sess *consoleLoopSession, newTree *VNode, newBody string, seq int64) []byte {
	if sess.prevTree == nil {
		// First frame for this session: full-body snapshot via the
		// legacy event:patch envelope. Client's __skyPatch consumes
		// this verbatim and replaces #sky-root's HTML.
		snap := frameSnapshot{seq: seq, body: newBody}
		data := encodeSSEFrameFromSnapshot(snap)
		escaped := jsonEscapeNewlines(data)
		return []byte("event: patch\ndata: " + escaped + "\n\n")
	}
	patches := diffTrees(sess.prevTree, newTree, nil)
	// patches==nil is impossible (diffTrees returns []), but be
	// defensive.
	if patches == nil {
		patches = []Patch{}
	}
	if patchesAreFullReplace(patches) {
		// Diff degenerated to a single root-level innerHTML replace.
		// Cheaper to ship the full body.
		snap := frameSnapshot{seq: seq, body: newBody}
		data := encodeSSEFrameFromSnapshot(snap)
		escaped := jsonEscapeNewlines(data)
		return []byte("event: patch\ndata: " + escaped + "\n\n")
	}
	snap := frameSnapshot{seq: seq, body: newBody}
	data := encodePatchesEventFromSnapshot(snap, patches)
	escaped := jsonEscapeNewlines(data)
	return []byte("event: patches\ndata: " + escaped + "\n\n")
}

// jsonEscapeNewlines mirrors live.go's SSE data-line escaping:
// every literal \n becomes the two-char `\n` sequence so the SSE
// framing's newline-delimited contract holds.
func jsonEscapeNewlines(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, '\\', 'n')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// runConsoleCmd walks a Cmd value + executes its Tasks. Result Msgs
// fold back through dispatchConsoleMsg so they go through the same
// lock + diff + broadcast path as the originating event.
//
// Reuses the host's pattern (live.go runCmd) but on the console
// plane's session map. Pub/sub kinds (`publish`, `publishNoEcho`)
// are deliberately NOT routed — the console doesn't participate in
// the host app's broker; it has its own diff loop.
func runConsoleCmd(sess *consoleLoopSession, cmd any, hooks *ConsoleAppHooks) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Printf("[sky.console] runCmd panic recovered: %v (sid=%s)\n", rec, sess.sid)
		}
	}()
	c, ok := cmd.(cmdT)
	if !ok {
		return
	}
	switch c.kind {
	case "none":
		return
	case "batch":
		for _, sub := range c.batch {
			runConsoleCmd(sess, sub, hooks)
		}
	case "perform":
		// Task execution. sky_call(task, nil) invokes the zero-arg
		// SkyTask wrapper → SkyResult; toMsg wraps the result into
		// a Msg ADT that dispatchConsoleMsg can consume.
		runConsolePerform(sess, c.task, c.toMsg, hooks)
	case "publish", "publishNoEcho":
		// Intentional no-op — see function comment.
		return
	}
}

// runConsolePerform runs a Sky Task and folds its result back
// through update via dispatchConsoleMsg. Defers panics so a
// misbehaving Task doesn't take down the dispatcher.
//
// task : SkyTask  (zero-arg Sky func → SkyResult)
// toMsg : Result e a → Msg  (curried lambda; sky_call applies result)
func runConsolePerform(sess *consoleLoopSession, task, toMsg any, hooks *ConsoleAppHooks) {
	var result any
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("[sky.console] perform task panic recovered: %v (sid=%s)\n", rec, sess.sid)
			}
		}()
		result = sky_call(task, nil)
	}()
	if result == nil {
		// Panicked or returned bare nil — drop without re-dispatch.
		return
	}
	var resultMsg any
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("[sky.console] perform toMsg panic recovered: %v (sid=%s)\n", rec, sess.sid)
			}
		}()
		resultMsg = sky_call(toMsg, result)
	}()
	if resultMsg == nil {
		return
	}
	dispatchConsoleMsg(sess, resultMsg, hooks)
}

// AssignSkyIDsForConsole exposes the package-internal assignSkyIDs
// walker to console_app. Stamps every element node in the tree with
// a stable "console.<path>" key so the renderer's hid output stays
// consistent across the initial GET (mount.go) and subsequent
// update-loop renders (dispatchConsoleMsg).
//
// The prefix MUST match the prefix used at render time. We use
// "console" to keep the host's "r"-namespaced ids distinct so a
// stray host-side click handler can't grab a console hid.
func AssignSkyIDsForConsole(n *VNode) {
	assignSkyIDs(n, "console")
}

// ApplyStyleInjectionsForConsole exposes the package-internal
// applyStyleInjections walker to console_app so its initial-GET
// render path produces the same hoisted-style HTML the update loop
// emits. Without this, first-paint and subsequent renders would
// differ on pseudo-class / animation / media-query trees and the
// first diff would churn the entire DOM unnecessarily.
func ApplyStyleInjectionsForConsole(n *VNode) {
	applyStyleInjections(n)
}

// SeedConsoleLoopSession populates a freshly-minted session with the
// model + rendered VNode + handlers map produced by an initial GET
// to /_sky/console. Called from console_app/mount.go right after
// `HtmlRenderWithHandlers` produces the first body — the session
// then has everything it needs to:
//
//   - Resolve hid lookups for the first click (typed Msg available
//     from the moment the page hits the browser, no re-init).
//   - Diff subsequent renders against the body the browser already
//     has (so PR9-E re-renders emit `event: patches` against the
//     correct baseline rather than triggering a full-body replace
//     on every interaction).
//
// Sid MUST be the same value the SSE channel cookie carries; mount.go
// passes the cookie value through. If the session already exists
// (e.g. the user reloads the page) the seed is treated as a NEW
// initial snapshot — model + prevTree + handlers all replaced. This
// is safe because the browser has just replaced its DOM with the new
// body anyway.
//
// Safe to call before ConsoleAppHooks are registered (the loop will
// pick the session up on first dispatch).
func SeedConsoleLoopSession(sid string, model any, tree *VNode, body string, handlers map[string]any) {
	if sid == "" {
		return
	}
	consoleLoop.mu.Lock()
	defer consoleLoop.mu.Unlock()
	sess, ok := consoleLoop.sessions[sid]
	if !ok {
		sess = &consoleLoopSession{sid: sid}
		consoleLoop.sessions[sid] = sess
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.model = model
	sess.prevTree = tree
	sess.prevBody = body
	sess.handlers = handlers
}

// consoleLoopSessionCount returns the number of live sessions on the
// loop. Test helper; mirrors connectedConsoleSSEClients but on the
// model-bearing map (which can outlive the SSE channel's transport
// state when a tab disconnects).
func consoleLoopSessionCount() int {
	consoleLoop.mu.RLock()
	defer consoleLoop.mu.RUnlock()
	return len(consoleLoop.sessions)
}

// consoleLoopSessionExists reports whether sid is registered. Test
// helper.
func consoleLoopSessionExists(sid string) bool {
	consoleLoop.mu.RLock()
	defer consoleLoop.mu.RUnlock()
	_, ok := consoleLoop.sessions[sid]
	return ok
}

// consoleLoopGetSession returns the session for sid OR nil. Test
// helper — production callers go through dispatchConsoleMsg which
// takes the per-session lock.
func consoleLoopGetSession(sid string) *consoleLoopSession {
	consoleLoop.mu.RLock()
	defer consoleLoop.mu.RUnlock()
	return consoleLoop.sessions[sid]
}

// consoleEventEnvelope helps tests build wire payloads without
// having to round-trip through JSON.
type consoleEventEnvelope struct {
	Msg  string            `json:"msg"`
	Args []json.RawMessage `json:"args,omitempty"`
}

// marshalConsoleEventEnvelope helps tests encode a wire envelope for
// /_sky/console/_event POST bodies.
func marshalConsoleEventEnvelope(msg string, args ...json.RawMessage) []byte {
	env := consoleEventEnvelope{Msg: msg, Args: args}
	b, _ := json.Marshal(env)
	return b
}
