package rt

// Adversarial scenarios beyond the per-step tests. Covers the cross-
// cutting properties of the input authority protocol that only show
// up when multiple channels interact: two concurrent events, deep
// tree alignment, seq ordering across dispatch paths. Browser-driven
// tests (focus preservation, sendBeacon flush, stale-drop) land in
// a follow-up; these pin the server invariants they rely on.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentEventsSerialise — two /_sky/event requests fired
// concurrently against the same session MUST produce strictly
// ordered seq values. sess.mu serialises dispatch, so nextLocalSeq
// runs under the lock per request; any interleaving that produces
// duplicate or out-of-order seqs would mean the lock is being
// released too early.
func TestConcurrentEventsSerialise(t *testing.T) {
	viewFn := func(model any) any {
		return velement("button",
			[]any{eventPair{name: "click", msg: "Click"}},
			[]any{vtext("go")})
	}
	app := &liveApp{
		update: func(msg, model any) any {
			// Flip the model slightly so the view body differs from
			// render to render; otherwise the no-op suppression would
			// short-circuit and the test would only exercise one path.
			if s, ok := model.(string); ok {
				return SkyTuple2{V0: s + ".", V1: cmdT{kind: "none"}}
			}
			return SkyTuple2{V0: "x", V1: cmdT{kind: "none"}}
		},
		view:    viewFn,
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	init := sky_call(viewFn, "seed").(VNode)
	assignSkyIDs(&init, "r")
	handlers := map[string]any{}
	_ = renderVNode(init, handlers)
	sess := &liveSession{
		model:     "seed",
		handlers:  handlers,
		prevTree:  &init,
		sseCh:     make(chan sseFrame, 64),
		cancelSub: make(chan struct{}),
	}
	app.store.Set("sid-conc", sess)
	clickHid := init.SkyID + ".click"

	const N = 20
	seqs := make([]int64, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			reqBody := `{"sessionId":"sid-conc","seq":` + itoa(i+1) +
				`,"msg":"","args":[],"handlerId":"` + clickHid + `"}`
			req := httptest.NewRequest(http.MethodPost, "/_sky/event",
				strings.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			// The session cookie is the authority for which session an
			// event dispatches into (boundSessionID) — a real browser
			// always sends it alongside the body sid.
			req.Header.Set("Cookie", "sky_sid=sid-conc")
			rr := httptest.NewRecorder()
			app.handleEvent(rr, req)

			// Parse the seq out of whichever response format landed.
			if strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
				var env map[string]any
				_ = json.Unmarshal(rr.Body.Bytes(), &env)
				if v, ok := env["seq"].(float64); ok {
					seqs[i] = int64(v)
				}
			} else {
				if s := rr.Header().Get("X-Sky-Seq"); s != "" {
					var n int64
					_ = json.Unmarshal([]byte(s), &n)
					seqs[i] = n
				}
			}
		}()
	}
	wg.Wait()

	// Every seq must be unique and in range [1, N]. Order of arrival
	// isn't guaranteed — we just assert no collisions and strict
	// coverage of the range.
	seen := map[int64]bool{}
	for _, s := range seqs {
		if s == 0 {
			t.Errorf("some requests got zero seq: %v", seqs)
			continue
		}
		if seen[s] {
			t.Errorf("duplicate seq %d emitted: %v", s, seqs)
		}
		seen[s] = true
	}
	if len(seen) != N {
		t.Errorf("got %d distinct seqs, want %d (missing coverage)", len(seen), N)
	}
}

// TestSeqCountsCoverEveryOutgoingFrame — encodeSSEFrame (used by
// subscription ticks and Cmd.perform) and writeEventJSON/HTML (used
// by event replies) MUST bump the same counter. If a Cmd completes
// between two events, its SSE frame gets a seq that sits between the
// two event reply seqs — the client applies in that order and the
// DOM reflects the server's actual mutation order.
func TestSeqCountsCoverEveryOutgoingFrame(t *testing.T) {
	sess := &liveSession{}
	a := sess.nextLocalSeq()                       // simulate event reply 1
	sseFrame := encodeSSEFrame(sess, "<p>sub</p>") // subscription tick
	b := sess.nextLocalSeq()                       // simulate event reply 2

	var env map[string]any
	if err := json.Unmarshal([]byte(sseFrame), &env); err != nil {
		t.Fatalf("frame invalid: %v", err)
	}
	sseSeq := int64(env["seq"].(float64))
	if a != 1 || sseSeq != 2 || b != 3 {
		t.Errorf("outgoing seqs not interleaved monotonically: event=%d, sse=%d, event=%d",
			a, sseSeq, b)
	}
}

// TestDiffAlignsInsideNestedForm — the clientState alignment works
// at arbitrary depth. A deeply-nested form's email input still gets
// its value patch suppressed when the client's reported value
// matches the server's intent.
func TestDiffAlignsInsideNestedForm(t *testing.T) {
	mk := func(val string) VNode {
		return elWithAttrs("div", nil,
			elWithAttrs("main", nil,
				elWithAttrs("form", nil,
					elWithAttrs("fieldset", nil,
						elWithAttrs("input", map[string]string{
							"name":  "email",
							"value": val,
						}),
					),
				),
			),
		)
	}
	old := mk("stale@old.com")
	new_ := mk("a@b.com")
	assignSkyIDs(&old, "r")
	assignSkyIDs(&new_, "r")

	// Dig to the email input inside the nested structure.
	input := &new_.Children[0].Children[0].Children[0].Children[0]
	if input.Tag != "input" {
		t.Fatalf("test tree shape wrong — expected input at deepest leaf, got %q", input.Tag)
	}
	emailID := input.SkyID

	patches := diffTrees(&old, &new_, map[string]string{
		emailID: "a@b.com", // client says DOM already shows this
	})
	for _, p := range patches {
		if p.ID == emailID && p.Attrs != nil {
			if _, ok := p.Attrs["value"]; ok {
				t.Errorf("nested form: value patch must be suppressed when client matches, got %+v", p.Attrs)
			}
		}
	}
}

// TestLegacyFieldsPreserved — request envelope must accept and dispatch
// old-style events that don't carry seq / inputState / batch. Ensures
// the protocol bump is backward-compatible for servers running
// alongside pre-upgrade clients.
func TestLegacyFieldsPreserved(t *testing.T) {
	viewFn := func(model any) any {
		return velement("button",
			[]any{eventPair{name: "click", msg: "Click"}},
			[]any{vtext("x")})
	}
	app := &liveApp{
		update: func(msg, model any) any {
			if s, ok := model.(string); ok {
				return SkyTuple2{V0: s + "!", V1: cmdT{kind: "none"}}
			}
			return SkyTuple2{V0: "x", V1: cmdT{kind: "none"}}
		},
		view:    viewFn,
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	init := sky_call(viewFn, "seed").(VNode)
	assignSkyIDs(&init, "r")
	handlers := map[string]any{}
	_ = renderVNode(init, handlers)
	sess := &liveSession{
		model:     "seed",
		handlers:  handlers,
		prevTree:  &init,
		sseCh:     make(chan sseFrame, 1),
		cancelSub: make(chan struct{}),
	}
	app.store.Set("sid-legacy", sess)
	clickHid := init.SkyID + ".click"

	// Pre-upgrade payload — no seq, no inputState, no batch.
	reqBody := `{"sessionId":"sid-legacy","msg":"","args":[],"handlerId":"` +
		clickHid + `"}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event",
		strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "sky_sid=sid-legacy")
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy request rejected: status %d, body %s", rr.Code, rr.Body.String())
	}
	// The response still carries a seq (server advances localSeq for
	// every reply), but respondingTo must be omitted since the client
	// didn't supply one.
	if strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		var env map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &env)
		if _, present := env["respondingTo"]; present {
			t.Errorf("respondingTo must be absent for legacy client: %+v", env)
		}
		if env["seq"] == nil {
			t.Errorf("seq still required in response envelope: %+v", env)
		}
	}
}

// ── /_sky/event body cap — pinned as a NUMBER ────────────────────────
//
// handleEvent bounds the event payload with `maxBody := int64(5 << 20)`
// (live.go, just above `http.MaxBytesReader`). That constant is a
// PRODUCT decision, not an implementation detail: `Event.onFile` /
// `Event.onImage` ship the picked file as a base64 data URL through this
// same channel, so a ~4 MiB image (~5.4 MiB base64) is the sizing case.
// Shrinking it — say to `64 << 10` — silently 413s every non-trivial
// upload, and until these tests existed NOTHING in the package exercised
// a body between 64 KiB and 5 MiB against this handler, so the whole Go
// suite stayed green through that mutation.
//
// The pin is deliberately two-sided: at-cap must PASS and cap+1 must
// 413. A one-sided "something gets rejected" assertion would survive any
// shrink of the constant.

// liveEventDefaultMaxBody mirrors handleEvent's `int64(5 << 20)` default.
// It is duplicated here on purpose — a test that read the value out of
// the source under test could not detect a change to it.
const liveEventDefaultMaxBody = 5 << 20 // 5 MiB, exactly 5242880 bytes

// eventBodyPaddedTo builds a well-formed /_sky/event JSON envelope for
// (sid, hid) whose total encoded length is EXACTLY n bytes. The padding
// rides in an extra `pad` member; encoding/json ignores unknown fields,
// so the request stays a valid dispatch all the way through the handler.
func eventBodyPaddedTo(t *testing.T, sid, hid string, n int) string {
	t.Helper()
	head := `{"sessionId":"` + sid + `","seq":1,"msg":"","args":[],"handlerId":"` + hid + `","pad":"`
	tail := `"}`
	pad := n - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("cannot build a %d-byte body: envelope alone is %d bytes", n, len(head)+len(tail))
	}
	body := head + strings.Repeat("a", pad) + tail
	if len(body) != n {
		t.Fatalf("padding arithmetic wrong: built %d bytes, wanted %d", len(body), n)
	}
	return body
}

// TestHandleEventBodyCapIsFiveMiB pins the DEFAULT cap at both edges.
//
//   - a body of exactly 5 MiB dispatches (200) — this is the assertion a
//     shrink of `5 << 20` to `64 << 10` breaks;
//   - a body of 5 MiB + 1 is rejected 413 — which is what stops the first
//     assertion from being vacuous (it proves a cap exists at all, and
//     that it sits at this exact byte).
func TestHandleEventBodyCapIsFiveMiB(t *testing.T) {
	// Neutralise any ambient override so the DEFAULT is what is measured.
	// Empty parses as "not a positive int", so handleEvent keeps 5 << 20.
	t.Setenv(skyEnvName("LIVE_MAX_BODY_BYTES"), "")

	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, sid)

	atCap := eventBodyPaddedTo(t, sid, hid, liveEventDefaultMaxBody)
	if rr := postEvent(app, cookie, atCap); rr.Code != http.StatusOK {
		t.Errorf("a %d-byte body (exactly the default cap, live.go `maxBody := int64(5 << 20)`) "+
			"got status %d, want 200. The cap constant must be 5<<20 = %d bytes; "+
			"body %q", len(atCap), rr.Code, liveEventDefaultMaxBody, truncateForMsg(rr.Body.String()))
	}

	overCap := eventBodyPaddedTo(t, sid, hid, liveEventDefaultMaxBody+1)
	if rr := postEvent(app, cookie, overCap); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a %d-byte body (cap+1, cap = 5<<20 = %d) got status %d, want 413. "+
			"Without this the at-cap assertion above proves nothing; body %q",
			len(overCap), liveEventDefaultMaxBody, rr.Code, truncateForMsg(rr.Body.String()))
	}
}

// TestHandleEventBodyCapEnvOverride pins the documented escape hatch,
// <PREFIX>_LIVE_MAX_BODY_BYTES. An override that silently did nothing
// would look identical to a working one in every other test, because
// every other body in the suite is a few hundred bytes.
func TestHandleEventBodyCapEnvOverride(t *testing.T) {
	const override = 4096
	t.Setenv(skyEnvName("LIVE_MAX_BODY_BYTES"), "4096")

	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, sid)

	atCap := eventBodyPaddedTo(t, sid, hid, override)
	if rr := postEvent(app, cookie, atCap); rr.Code != http.StatusOK {
		t.Errorf("%s=4096: a %d-byte body got status %d, want 200",
			skyEnvName("LIVE_MAX_BODY_BYTES"), len(atCap), rr.Code)
	}

	overCap := eventBodyPaddedTo(t, sid, hid, override+1)
	if rr := postEvent(app, cookie, overCap); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("%s=4096: a %d-byte body got status %d, want 413 — the override did not move the cap",
			skyEnvName("LIVE_MAX_BODY_BYTES"), len(overCap), rr.Code)
	}

	// And the override must be BELOW the default here, so a body that the
	// default would accept is now refused. Otherwise "413 at 4097" could
	// be explained by some unrelated bound rather than by the override.
	underDefault := eventBodyPaddedTo(t, sid, hid, 64<<10)
	if rr := postEvent(app, cookie, underDefault); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("%s=4096: a %d-byte body (well under the 5<<20 default) got status %d, want 413",
			skyEnvName("LIVE_MAX_BODY_BYTES"), len(underDefault), rr.Code)
	}
}

func truncateForMsg(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
