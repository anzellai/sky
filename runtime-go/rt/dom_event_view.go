package rt

// dom_event_view.go — the handler view one DOM event is dispatched against, for
// the Sky.Spa wasm client (dom_render_wasm.go). Portable, so the host tests pin
// it (dom_event_view_test.go).
//
// The bug this closes (audit H-2). The client renders SYNCHRONOUSLY inside the
// listener that dispatched the Msg (live_wasm.go step). The browser computes an
// event's propagation path once, before dispatch, and then walks it; a listener
// added to a node later in the path while the event is in flight still fires.
// A children-reconcile patch keeps a DOM node and renames it (KidOp), and
// applyAttrs then binds the new element's listeners on it. So one click on a
// blog card ran the card's `Navigate (BlogPost slug)`, the patch turned the
// card's kept ANCESTOR into the post page's "Back to blog" element, bound its
// click listener, and the same click, bubbling on, ran `Navigate BlogIndex`:
// the page opened and at once went back. A listener that was already bound
// had the same fault in a quieter form: it read its handler by the node's
// sky-id when it fired, so a renamed node dispatched the Msg of the element it
// had become, not of the element the user clicked.
//
// The rule now, as in Sky.Live and Elm (where the patch arrives after the
// event): an event runs the handlers of the view it was delivered to. The first
// Sky listener an event reaches records, for every node on the event's path,
// the handlers that node had at that moment. Every later Sky listener for the
// same event reads its handler from that record: a node that had no handler
// for the event then runs nothing, and a node that had one runs that one.
// Nested handlers keep working (an inner and an outer onClick both run, as on
// Sky.Live), each with the Msg of the clicked view.

// domEventView is the recorded handler view of one in-flight event.
type domEventView[N any] struct {
	nodes    []N
	handlers []map[string]any // handlers[i] belongs to nodes[i]; nil = none
}

// domEventViews holds the views of the events in flight. An event can start
// while another is dispatching (a render that removes the focused node fires
// blur/focusout synchronously), so several are kept. The oldest is dropped past
// max: a finished event's view is never read again, and no real dispatch nests
// anywhere near max events deep.
type domEventViews[N any] struct {
	seq   int
	order []int
	views map[int]*domEventView[N]
	max   int
}

func newDomEventViews[N any](max int) *domEventViews[N] {
	return &domEventViews[N]{views: map[int]*domEventView[N]{}, max: max}
}

// open records the handler view for an event whose propagation path is path
// (target first). idOf reads a node's sky-id (false when it has none). It
// returns the token the caller stores on the event object.
func (v *domEventViews[N]) open(path []N, idOf func(N) (string, bool), slots spaHandlerSlots) int {
	view := &domEventView[N]{}
	for _, n := range path {
		id, ok := idOf(n)
		if !ok {
			continue
		}
		view.nodes = append(view.nodes, n)
		view.handlers = append(view.handlers, slots[id])
	}
	v.seq++
	tok := v.seq
	v.views[tok] = view
	v.order = append(v.order, tok)
	for len(v.order) > v.max {
		delete(v.views, v.order[0])
		v.order = v.order[1:]
	}
	return tok
}

// lookup returns the handler node had for evt when the event with token tok
// started. known is false when the view is not held (never opened, or dropped);
// the caller then falls back to the current handler. ok is false when the node
// had no handler for evt in the recorded view: the listener must run nothing.
func (v *domEventViews[N]) lookup(tok int, node N, eq func(a, b N) bool, evt string) (h any, ok, known bool) {
	view, held := v.views[tok]
	if !held {
		return nil, false, false
	}
	for i, n := range view.nodes {
		if eq(n, node) {
			h, ok = view.handlers[i][evt]
			return h, ok, true
		}
	}
	return nil, false, true
}
