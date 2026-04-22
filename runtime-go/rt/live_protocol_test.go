package rt

// Step 2 of docs/skylive/input-authority-protocol.md — wire-format
// scaffolding. These tests pin the new fields end-to-end: parsing
// req.inputState + req.batch on the server side, monotonic outSeq,
// ack-inputs eviction for unmounted elements, and the JSON / HTML /
// SSE envelope shapes. Behaviour remains legacy-compatible: no patch
// filtering yet (that's step 3), no stale-drop yet (step 4), but the
// metadata flows on the wire so later steps can activate filters
// without another protocol change.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOutSeqMonotonic(t *testing.T) {
	s := &liveSession{}
	a := s.nextOutSeq()
	b := s.nextOutSeq()
	c := s.nextOutSeq()
	if a != 1 || b != 2 || c != 3 {
		t.Errorf("nextOutSeq non-monotonic: got %d,%d,%d, want 1,2,3", a, b, c)
	}
}

func TestIngestInputStateKeepsMaxSeq(t *testing.T) {
	s := &liveSession{}
	s.ingestInputState(map[string]inputStateEntry{
		"r.0#input:email": {Value: "a@b.com", Seq: 5},
		"r.0#input:name":  {Value: "Alice", Seq: 3},
	})
	// Older snapshot for email — must NOT regress.
	s.ingestInputState(map[string]inputStateEntry{
		"r.0#input:email": {Value: "old", Seq: 2},
	})
	// Newer snapshot for email — SHOULD advance.
	s.ingestInputState(map[string]inputStateEntry{
		"r.0#input:email": {Value: "a@b.com", Seq: 9},
	})
	if s.inputSeqs["r.0#input:email"] != 9 {
		t.Errorf("email seq = %d, want 9", s.inputSeqs["r.0#input:email"])
	}
	if s.inputSeqs["r.0#input:name"] != 3 {
		t.Errorf("name seq = %d, want 3", s.inputSeqs["r.0#input:name"])
	}
}

func TestAckInputsEvictsUnmounted(t *testing.T) {
	tree := el("form", nil,
		el("input", map[string]string{"name": "email"}),
	)
	assignSkyIDs(&tree, "r")
	emailID := tree.Children[0].SkyID
	// Session knows about two inputs, but only one is still rendered.
	sess := &liveSession{
		prevTree: &tree,
		inputSeqs: map[string]int64{
			emailID:            7,
			"r.0#input:stale": 3, // unmounted in this render
		},
	}
	ack := ackInputsForPrevTree(sess)
	if ack[emailID] != 7 {
		t.Errorf("email ack missing: %+v", ack)
	}
	if _, present := ack["r.0#input:stale"]; present {
		t.Errorf("stale ack not evicted: %+v", ack)
	}
	if _, present := sess.inputSeqs["r.0#input:stale"]; present {
		t.Errorf("stale id not removed from session state: %+v", sess.inputSeqs)
	}
}

func TestEncodeSSEFrameShape(t *testing.T) {
	sess := &liveSession{}
	raw := encodeSSEFrame(sess, "<div>hi</div>")
	var frame map[string]any
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatalf("frame is not valid JSON: %v (%s)", err, raw)
	}
	seq, ok := frame["seq"].(float64)
	if !ok || int64(seq) != 1 {
		t.Errorf("frame.seq = %v, want 1", frame["seq"])
	}
	if frame["body"] != "<div>hi</div>" {
		t.Errorf("frame.body = %v", frame["body"])
	}
	if _, present := frame["ackInputs"]; present {
		t.Errorf("empty ack inputs should be omitted, got %+v", frame)
	}
}

func TestWriteEventJSONEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	text := "new"
	patches := []Patch{{ID: "r.0#p", Text: &text}}
	writeEventJSON(rr, 42, 17, map[string]int64{"r.0#input:email": 17}, patches)

	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q, want application/json", got)
	}
	var envelope map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rr.Body.String())
	}
	if envelope["seq"].(float64) != 42 {
		t.Errorf("seq = %v, want 42", envelope["seq"])
	}
	if envelope["respondingTo"].(float64) != 17 {
		t.Errorf("respondingTo = %v, want 17", envelope["respondingTo"])
	}
	ack := envelope["ackInputs"].(map[string]any)
	if ack["r.0#input:email"].(float64) != 17 {
		t.Errorf("ackInputs = %+v", ack)
	}
	if envelope["patches"] == nil {
		t.Errorf("patches missing")
	}
}

func TestWriteEventJSONNoPatchesEmitsEmptyArray(t *testing.T) {
	rr := httptest.NewRecorder()
	writeEventJSON(rr, 1, 0, nil, nil)
	var envelope map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &envelope)
	arr, ok := envelope["patches"].([]any)
	if !ok || len(arr) != 0 {
		t.Errorf("patches = %v, want []", envelope["patches"])
	}
	// respondingTo omitted when request seq was 0 (legacy client).
	if _, present := envelope["respondingTo"]; present {
		t.Errorf("respondingTo must omit when 0: %+v", envelope)
	}
}

func TestWriteEventHTMLSetsProtocolHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	writeEventHTML(rr, 99, map[string]int64{"r.0#input:q": 5}, "<p>ok</p>")
	if rr.Header().Get("Content-Type") != "text/html" {
		t.Errorf("content-type = %q", rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("X-Sky-Seq") != "99" {
		t.Errorf("X-Sky-Seq = %q", rr.Header().Get("X-Sky-Seq"))
	}
	var ack map[string]int64
	if err := json.Unmarshal([]byte(rr.Header().Get("X-Sky-Ack-Inputs")), &ack); err != nil {
		t.Fatalf("ack header not JSON: %v", err)
	}
	if ack["r.0#input:q"] != 5 {
		t.Errorf("ack header = %+v", ack)
	}
	if rr.Body.String() != "<p>ok</p>" {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestWriteEventHTMLOmitsEmptyAck(t *testing.T) {
	rr := httptest.NewRecorder()
	writeEventHTML(rr, 5, nil, "<p>ok</p>")
	if got := rr.Header().Get("X-Sky-Ack-Inputs"); got != "" {
		t.Errorf("ack header should be absent when map is empty, got %q", got)
	}
}

// TestHandleEventRoundTripsSeq — a full /_sky/event request with the
// new protocol fields flows through the handler and the response
// envelope carries seq + respondingTo. The view renders a form whose
// email input stays mounted across dispatches so ackInputsForPrevTree
// keeps the email id and the envelope can echo it back.
func TestHandleEventRoundTripsSeq(t *testing.T) {
	// View: a form with an email input that survives every render, so
	// the input's sky-id stays in prevTree and ackInputs keeps it.
	viewFn := func(model any) any {
		return velement("form", nil, []any{
			velement("input",
				[]any{attrPair{key: "name", val: "email"}},
				nil),
			velement("button",
				[]any{eventPair{name: "click", msg: "ClickMsg"}},
				[]any{vtext("Go")}),
		})
	}
	app := &liveApp{
		update: func(msg, model any) any {
			// Flip model so dispatch registers a fresh render instead of
			// the byte-identical-body no-op shortcut.
			next := model
			if s, ok := model.(string); ok {
				next = s + "!"
			}
			return SkyTuple2{V0: next, V1: cmdT{kind: "none"}}
		},
		view:    viewFn,
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	// Seed prevTree by rendering once — matches what dispatchRoot does
	// on the initial page load.
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
	app.store.Set("sid-1", sess)

	// Click handler id is the button's sky-id + ".click". The button
	// sits at index 1 in the form, and the form at the root position
	// we fed to assignSkyIDs. Read the actual id off the rendered tree
	// so the test isn't tied to the internal id grammar.
	buttonSkyID := init.Children[1].SkyID
	clickHid := buttonSkyID + ".click"
	emailSkyID := init.Children[0].SkyID

	reqBody := `{
		"sessionId": "sid-1",
		"seq": 42,
		"msg": "",
		"args": [],
		"handlerId": "` + clickHid + `",
		"inputState": {"` + emailSkyID + `": {"value": "x", "seq": 42}}
	}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event",
		strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		var envelope map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("body: %v (%s)", err, rr.Body.String())
		}
		if envelope["seq"] == nil {
			t.Errorf("envelope missing seq: %+v", envelope)
		}
		if envelope["respondingTo"].(float64) != 42 {
			t.Errorf("respondingTo = %v, want 42", envelope["respondingTo"])
		}
		if ack, ok := envelope["ackInputs"].(map[string]any); !ok {
			t.Errorf("ackInputs missing from envelope: %+v", envelope)
		} else if ack[emailSkyID].(float64) != 42 {
			t.Errorf("ackInputs[email] = %v, want 42", ack[emailSkyID])
		}
	} else {
		if rr.Header().Get("X-Sky-Seq") == "" {
			t.Errorf("HTML fallback missing X-Sky-Seq header")
		}
	}
}

// TestHandleEventBatch — a sendBeacon-style batch returns 204 and
// ingests the outer inputState snapshot into the session before
// processing entries. Each batched entry dispatches under sess.mu
// and rebuilds the handler map, so only the first entry's handler
// lookup is guaranteed to succeed — verify the 204 response and the
// inputState ingest (which happens before the batch runs).
func TestHandleEventBatch(t *testing.T) {
	viewFn := func(model any) any {
		return velement("form", nil, []any{
			velement("input",
				[]any{attrPair{key: "name", val: "email"}},
				nil),
		})
	}
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
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
		sseCh:     make(chan string, 16),
		cancelSub: make(chan struct{}),
	}
	app.store.Set("sid-2", sess)

	emailSkyID := init.Children[0].SkyID
	// A direct-send entry (no handlerId) uses the ADT-lookup fallback
	// inside dispatchBatched, so the batch path can be exercised even
	// when the rendered view has no event handlers. The Msg constructor
	// is resolved via the (empty here) tag registry → SkyADT{Tag:-1}.
	reqBody := `{
		"sessionId": "sid-2",
		"inputState": {"` + emailSkyID + `": {"value": "a@b.com", "seq": 5}},
		"batch": [
			{"msg": "NoopMsg", "args": [], "handlerId": ""}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event",
		strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rr.Code)
	}
	if sess.inputSeqs[emailSkyID] != 5 {
		t.Errorf("batch inputState not ingested: %+v (want %s=5)",
			sess.inputSeqs, emailSkyID)
	}
}
