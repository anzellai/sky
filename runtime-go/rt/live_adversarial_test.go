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
// ordered seq values. sess.mu serialises dispatch, so nextOutSeq
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
		sseCh:     make(chan string, 64),
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
	a := sess.nextOutSeq()  // simulate event reply 1
	sseFrame := encodeSSEFrame(sess, "<p>sub</p>") // subscription tick
	b := sess.nextOutSeq()  // simulate event reply 2

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
		sseCh:     make(chan string, 1),
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
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy request rejected: status %d, body %s", rr.Code, rr.Body.String())
	}
	// The response still carries a seq (server advances outSeq for
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
