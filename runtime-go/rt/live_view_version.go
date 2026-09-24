//go:build !js

// live_view_version.go — render identity, handler history and the
// per-session bookkeeping that keeps a Sky.Live click bound to the view
// the user actually clicked on.
//
// # Why a view id exists
//
// Handler ids are positional: `<sky-id>.<event>`. A row's delete button
// at position 1 has the same id whatever row sits at position 1. When a
// click is resolved against the CURRENT render, a click the user made on
// an OLDER render (the reply to their previous click has not arrived yet)
// resolves to whatever now occupies that position — "tap x on a, then x
// on b" deleted c. That is silent data loss.
//
// Every render is therefore identified by a content id of its body (see
// liveViewID). The client stamps each event with the id of the body its
// DOM shows, and the server resolves the handler id against the handler
// map of THAT render. The session keeps the maps of the last
// liveHandlerHistory distinct renders. An id the session no longer holds
// is a desync (the client is refreshed and the action dropped) — never a
// different Msg.
//
// A content id, not a counter, because:
//   - two renders with byte-identical bodies have the same sky-ids, the
//     same events and the same Msg names, so one id serves both and the
//     newest closures win (the Elm rule: the latest view's handlers);
//   - it survives a restart and a replica move with no persisted state:
//     the rebuilt render of the same model has the same body, so the id
//     the browser holds resolves in the new process too.
//
// Delta frames also carry `base` (the id of the body the diff was
// computed against). The client applies a delta only on top of that body
// and buffers it otherwise, so a frame that arrives out of order is
// applied in order instead of being dropped (docs/skylive/architecture.md
// §View identity).

package rt

import (
	"hash/crc32"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// liveHandlerHistory is how many distinct renders a session always keeps the
// handler maps of. A click made on a render no longer held is refused
// (desync), never resolved against a newer render.
const liveHandlerHistory = 16

// liveHandlerRecent keeps a render's handler map for at least this long,
// beyond liveHandlerHistory, up to liveHandlerCap. A burst of clicks made on one
// render (30 queued taps; each processed tap renders again) used to outrun the
// 16-render window, and the late taps were refused as desyncs although the user
// made every one of them on a render the server had just sent. Time bounds the
// window a burst needs; the cap bounds the memory a session can hold.
const (
	liveHandlerRecent = 30 * time.Second
	liveHandlerCap    = 256
)

// handlerGen is the handler map of one distinct render body.
type handlerGen struct {
	view     string
	handlers map[string]any
	at       time.Time
}

var liveViewCastagnoli = crc32.MakeTable(crc32.Castagnoli)

// liveViewID is the content id of a rendered body: two independent
// 32-bit CRCs plus the length. Both CRCs are hardware-accelerated, and
// the byte view avoids copying the body.
func liveViewID(body string) string {
	b := unsafe.Slice(unsafe.StringData(body), len(body))
	a := crc32.ChecksumIEEE(b)
	c := crc32.Checksum(b, liveViewCastagnoli)
	return strconv.FormatUint(uint64(a)<<32|uint64(c), 36) + "-" + strconv.Itoa(len(body))
}

// recordRenderGeneration registers the handler map of the render whose
// body has id `view`. Caller holds sess.mu. The slice is rebuilt, never
// appended in place, so a copy of the old header taken for a rollback
// stays valid.
func (s *liveSession) recordRenderGeneration(view string) {
	now := time.Now()
	gens := make([]handlerGen, 0, liveHandlerHistory)
	gens = append(gens, handlerGen{view: view, handlers: s.handlers, at: now})
	for _, g := range s.handlerGens {
		if g.view == view {
			continue
		}
		if len(gens) >= liveHandlerCap {
			break
		}
		// Newest first: past the always-kept window, keep only recent renders.
		if len(gens) >= liveHandlerHistory && now.Sub(g.at) > liveHandlerRecent {
			break
		}
		gens = append(gens, g)
	}
	s.handlerGens = gens
}

// resolveHandler finds the Msg bound to handler id `hid` in the render
// the client clicked on. `view` == "" is a client that predates view
// ids (or a test posting a scraped id): the current render answers, as
// it always did. ok=false means the render is no longer held or it has
// no such handler: the caller answers with a desync.
func (s *liveSession) resolveHandler(view, hid string) (any, bool) {
	if view == "" {
		m, ok := s.handlers[hid]
		return m, ok
	}
	for _, g := range s.handlerGens {
		if g.view == view {
			m, ok := g.handlers[hid]
			return m, ok
		}
	}
	return nil, false
}

// ── Seq epoch (L7) ────────────────────────────────────────────────
//
// The client drops any frame whose seq is not above the largest it has
// applied. A session restored from a store starts from its persisted
// OutSeq, which can be older than what the previous process sent. The
// browser would then drop every fresh frame until a reload. (The
// app-wide broadcast counter is handled by the hello epoch, see
// liveProcessEpoch.)
//
// liveSeqFloor is a wall-clock floor (milliseconds x 1000). A restored
// session starts at or above it. A process
// that started later has a larger floor than any seq an earlier process
// could have issued, as long as it issued fewer than 1000 frames per
// millisecond of its uptime. 1000 x Unix millis stays below 2^53, so the
// value is exact in a JavaScript number.
func liveSeqFloor() int64 {
	return time.Now().UnixMilli() * 1000
}

// liveProcessEpoch identifies this process to the client (SSE hello
// `pe`). The app-wide broadcast counter restarts at 1 in every process;
// a client that sees a new epoch resets its broadcast guard.
var liveProcessEpoch = strconv.FormatInt(liveSeqFloor(), 36)

// restoredLocalSeq is the localSeq a session decoded from a store
// resumes at: the persisted value, lifted to the wall-clock floor.
func restoredLocalSeq(persisted int64) int64 {
	if f := liveSeqFloor(); f > persisted {
		return f
	}
	return persisted
}

// ── Persistence after every dispatch (L7) ─────────────────────────

// persistSession writes the session back to its store after a dispatch
// that did not come from handleEvent (Cmd.perform completion, the
// beacon batch, a Sub.every tick, pub/sub, stream and WebSocket
// delivery). Without it a persistent store kept the model of the last
// USER event, and a restart lost every change made since. Must be
// called WITHOUT sess.mu held (store.Set encodes the session).
func (app *liveApp) persistSession(sess *liveSession) {
	if app == nil || app.store == nil || sess == nil || sess.sid == "" {
		return
	}
	if sess.evicted.Load() {
		return
	}
	app.store.Set(sess.sid, sess)
}

// ── Classified dispatch panic feedback (L12) ──────────────────────

// pushDispatchError tells the session's tabs that a Msg failed, so the
// user sees that the click did something. The model's own Notification
// field (when the app has one) is still set by the recover in dispatch.
// Non-blocking: a full SSE buffer drops the banner, not the session.
func (sess *liveSession) pushDispatchError(ref string) {
	sess.lastDispatchErr = ref
	if sess.sseCh == nil {
		return
	}
	data := `{"ref":"` + jsonEscapeASCII(ref) + `"}`
	select {
	case sess.sseCh <- sseFrame{event: "skyerror", data: data}:
	default:
	}
}

// takeDispatchError returns and clears the ref of the last dispatch
// panic. Caller holds sess.mu.
func (sess *liveSession) takeDispatchError() string {
	r := sess.lastDispatchErr
	sess.lastDispatchErr = ""
	return r
}

func jsonEscapeASCII(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20:
			// Refs are hex; anything else is dropped rather than escaped.
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ── Sub.every reconciliation (SA-4, K4) ───────────────────────────
//
// `subscriptions` is re-evaluated after every dispatch. The old code
// honoured only the FIRST Sub.every leaf and cancelled + restarted it on
// every dispatch, so a 1 s clock that a 100 ms ticker kept interrupting
// never fired, and a second Sub.every never ran at all. Timers are now
// keyed by interval (plus an occurrence index for two timers with the
// same interval): a timer still requested keeps running with its phase,
// a timer no longer requested stops, a new one starts, and a kept timer
// dispatches the Msg constructor of the LATEST subscriptions result.

type everyReg struct {
	toMsg  atomic.Value // holds everyToMsg
	cancel chan struct{}
	once   sync.Once
}

type everyToMsg struct{ fn any }

func (r *everyReg) stop() { r.once.Do(func() { close(r.cancel) }) }

func (r *everyReg) currentToMsg() any {
	if v, ok := r.toMsg.Load().(everyToMsg); ok {
		return v.fn
	}
	return nil
}

// applyEverySubsDiff reconciles the running Sub.every timers with the
// leaves the latest `subscriptions model` returned.
func (app *liveApp) applyEverySubsDiff(sess *liveSession, leaves []subT) {
	desired := map[string]subT{}
	order := []string{}
	count := map[int]int{}
	for _, leaf := range leaves {
		if leaf.kind != "every" || leaf.ms <= 0 {
			continue
		}
		n := count[leaf.ms]
		count[leaf.ms] = n + 1
		key := strconv.Itoa(leaf.ms) + "#" + strconv.Itoa(n)
		desired[key] = leaf
		order = append(order, key)
	}
	sess.everyMu.Lock()
	if sess.everyRegs == nil {
		sess.everyRegs = map[string]*everyReg{}
	}
	for key, reg := range sess.everyRegs {
		if _, keep := desired[key]; !keep {
			reg.stop()
			delete(sess.everyRegs, key)
		}
	}
	var start []*everyReg
	var startMs []int
	for _, key := range order {
		leaf := desired[key]
		if reg, ok := sess.everyRegs[key]; ok {
			reg.toMsg.Store(everyToMsg{fn: leaf.toMsg})
			continue
		}
		reg := &everyReg{cancel: make(chan struct{})}
		reg.toMsg.Store(everyToMsg{fn: leaf.toMsg})
		sess.everyRegs[key] = reg
		start = append(start, reg)
		startMs = append(startMs, leaf.ms)
	}
	sess.everyMu.Unlock()
	done := sess.done
	for i, reg := range start {
		interval := time.Duration(startMs[i]) * time.Millisecond
		go app.runTimeEveryReg(sess, reg, interval, reg.cancel, done)
	}
}

// stopAllEvery stops every Sub.every timer of the session.
func (sess *liveSession) stopAllEvery() {
	sess.everyMu.Lock()
	regs := sess.everyRegs
	sess.everyRegs = nil
	sess.everyMu.Unlock()
	for _, r := range regs {
		r.stop()
	}
}

// ── Controlled-input convergence (UF-5) ───────────────────────────

// reconcileControlledInputs adds a `value` patch for every input the
// client reported (inputState) whose rendered model value differs from
// what the client shows. The structural diff compares the new render
// with the previous RENDER, so when `update` rejects or normalises an
// edit the model — and so the render — does not change, no patch is
// emitted, and the DOM kept the user's rejected text for ever. An
// input without a rendered `value` is uncontrolled and left alone.
// Checkboxes and radios are converged client-side (their state is the
// presence of the `checked` attribute; see __skyReassertChecked).
func reconcileControlledInputs(newTree *VNode, state map[string]inputStateEntry, patches []Patch) []Patch {
	if newTree == nil || len(state) == 0 {
		return patches
	}
	patched := map[string]bool{}
	for _, p := range patches {
		if p.Attrs != nil {
			if _, ok := p.Attrs["value"]; ok {
				patched[p.ID] = true
			}
		}
	}
	var walk func(n *VNode)
	walk = func(n *VNode) {
		if n == nil || n.Kind != "element" {
			return
		}
		if entry, ok := state[n.SkyID]; ok && n.SkyID != "" && !patched[n.SkyID] {
			if v, isCtl := controlledTextValue(n); isCtl && v != entry.Value {
				patches = append(patches, Patch{ID: n.SkyID, Attrs: map[string]string{"value": v}})
			}
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(newTree)
	return patches
}

// controlledTextValue is the model value a text-like control renders,
// and whether it renders one at all.
func controlledTextValue(n *VNode) (string, bool) {
	switch n.Tag {
	case "textarea":
		if v, ok := n.Attrs["value"]; ok {
			return v, true
		}
		if len(n.Children) == 1 && n.Children[0].Kind == "text" {
			return n.Children[0].Text, true
		}
		return "", false
	case "input":
		switch strings.ToLower(n.Attrs["type"]) {
		case "checkbox", "radio", "file", "submit", "button", "image", "reset":
			return "", false
		}
		v, ok := n.Attrs["value"]
		return v, ok
	}
	return "", false
}

// ── Select value across an options re-render (F4) ─────────────────

// markSelectedInSelectPatches re-renders the options of a <select>
// whose children a patch replaces, marking the option that matches the
// select's model value. The whole-tree render marks it (renderVNodeInto
// strips `value` from <select> and sets `selected` on the matching
// <option>), but a children-replace patch serialises the options alone,
// so the browser fell back to the first option and the control no
// longer showed the model.
func markSelectedInSelectPatches(newTree *VNode, patches []Patch) []Patch {
	if newTree == nil {
		return patches
	}
	var selects map[string]*VNode
	for i := range patches {
		if patches[i].HTML == nil {
			continue
		}
		if selects == nil {
			selects = map[string]*VNode{}
			var walk func(n *VNode)
			walk = func(n *VNode) {
				if n == nil || n.Kind != "element" {
					return
				}
				if n.Tag == "select" && n.SkyID != "" {
					selects[n.SkyID] = n
				}
				for j := range n.Children {
					walk(&n.Children[j])
				}
			}
			walk(newTree)
		}
		sel, ok := selects[patches[i].ID]
		if !ok {
			continue
		}
		want, has := sel.Attrs["value"]
		if !has || want == "" {
			continue
		}
		var sb strings.Builder
		for _, c := range sel.Children {
			if c.Kind == "element" && c.Tag == "option" {
				picked := c
				picked.Attrs = copyAttrs(c.Attrs)
				if picked.Attrs["value"] == want {
					picked.Attrs["selected"] = "selected"
				} else {
					delete(picked.Attrs, "selected")
				}
				renderVNodeInto(&sb, picked, nil)
			} else {
				renderVNodeInto(&sb, c, nil)
			}
		}
		html := sb.String()
		patches[i].HTML = &html
	}
	return patches
}

// liveDiff is diffTrees plus the Live-side post-passes every producer
// applies: select option marking. The HTTP event path additionally runs
// reconcileControlledInputs (it is the only path with fresh inputState).
func liveDiff(prev, next *VNode, clientState map[string]string) []Patch {
	return markSelectedInSelectPatches(next, diffTrees(prev, next, clientState))
}
