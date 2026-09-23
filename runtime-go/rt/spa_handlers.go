package rt

// spaHandlerSlots holds the CURRENT event handlers of every rendered element in
// the Sky.Spa wasm client, keyed by sky-id then event name.
//
// Why it exists: the shared DOM diff (diffNodes) compares handlers by their
// constructor name only — `Pick "a1"` and `Pick "b2"` both read "Pick" — which
// is right for Sky.Live and the desktop webview, because they resolve the
// handler server-side from the latest render at dispatch time. The wasm client
// instead attaches a real JS listener per element, and that listener used to
// CAPTURE the message value at bind time. A payload-only change therefore
// produced no patch, no rebind, and the listener kept dispatching the payload of
// the first render (a list re-rendered with the same text but different ids sent
// the old id). Listeners now read the handler from this table when they fire,
// and the table is refreshed from the new tree after every patch, so the payload
// always follows the model.
type spaHandlerSlots map[string]map[string]any

// set records el's handlers under its sky-id. A nil or empty map clears the slot.
func (s spaHandlerSlots) set(id string, events map[string]any) {
	if id == "" {
		return
	}
	if len(events) == 0 {
		delete(s, id)
		return
	}
	s[id] = events
}

// lookup returns the current handler for (id, evt), or fallback when the slot
// has none (an element without a sky-id, or a listener racing a release).
func (s spaHandlerSlots) lookup(id, evt string, fallback any) any {
	if m, ok := s[id]; ok {
		if h, ok := m[evt]; ok {
			return h
		}
	}
	return fallback
}

// drop clears the slot for a released element.
func (s spaHandlerSlots) drop(id string) {
	delete(s, id)
}

// refresh re-records every element's handlers from a freshly rendered tree. It
// runs after the patches for that tree are applied, when the DOM node carrying a
// given sky-id corresponds to the new tree's node with that sky-id.
func (s spaHandlerSlots) refresh(root *VNode) {
	if root == nil {
		return
	}
	if root.SkyID != "" {
		s.set(root.SkyID, root.Events)
	}
	for i := range root.Children {
		s.refresh(&root.Children[i])
	}
}
