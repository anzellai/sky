//go:build !js

package rt

// Sky.Live delivery of the <html> attributes (Live.withDocument,
// document_attrs.go).
//
// Each render computes the set once (commitRender) and compares it with the
// set of the render before. A different set marks the session "doc dirty".
// The next frame of a dispatch takes the mark and carries the new set as its
// "doc" operation, so the browser sets it on document.documentElement with no
// page reload. A delta frame whose render did not change the set carries
// nothing. A frame that carries the whole view (a full body, a resync, a
// mirror to another tab, a page) also carries the whole set, so a tab that
// missed a frame converges with the next full view.
//
// The set is derived from the model, so it is not stored with the session. A
// session read back from a store computes it again on its first render.

import (
	"encoding/base64"
	"encoding/json"
)

// refreshDoc recomputes the session's <html> attributes from its model.
// Caller holds s.mu (commitRender's contract).
func (s *liveSession) refreshDoc() {
	app := s.app.Load()
	if app == nil || app.document == nil {
		return
	}
	next := documentAttrs(app.document, s.model)
	if next == nil {
		// The function failed (documentAttrs logged it). Keep the set
		// the browser has.
		if !s.docKnown {
			return
		}
		next = s.docAttrs
	}
	if s.docKnown && sameDocAttrs(next, s.docAttrs) {
		return
	}
	s.docAttrs = next
	s.docKnown = true
	s.docDirty = true
}

// takeDocChange reports whether the set changed since the last frame that
// took the mark, and clears the mark. Caller holds s.mu.
func (s *liveSession) takeDocChange() bool {
	changed := s.docDirty
	s.docDirty = false
	return changed
}

// docWire is the set as it goes on the wire: never nil, so an empty set
// encodes as [] (the browser then removes every attribute it set).
func docWire(attrs []docAttr) []docAttr {
	if attrs == nil {
		return []docAttr{}
	}
	return attrs
}

// docCfg is the set for the page's #sky-live-cfg block: nil (the field is
// left out) when the app has no document function.
func (s *liveSession) docCfg() *[]docAttr {
	if !s.docKnown {
		return nil
	}
	w := docWire(s.docAttrs)
	return &w
}

// docStartTag is the attribute text of the page's <html> start tag.
func (s *liveSession) docStartTag() string {
	if !s.docKnown {
		return ""
	}
	return renderDocAttrs(s.docAttrs)
}

// appendLiveDocMetadata adds the set to a full-body HTML event reply as a
// trailing, inert comment (base64 JSON), the same form as the scroll
// commands (appendLiveScrollMetadata). It goes BEFORE the scroll comment:
// the client takes the scroll comment from the end first, then this one.
// An older client keeps an inert comment after the view root.
func appendLiveDocMetadata(body string, known bool, attrs []docAttr) string {
	if !known {
		return body
	}
	data, err := json.Marshal(docWire(attrs))
	if err != nil {
		return body
	}
	return body + "<!--sky-doc:" + base64.StdEncoding.EncodeToString(data) + "-->"
}
