//go:build !js

package rt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// liveDocTestApp is newBindingTestApp with Live.withDocument: the model
// "seed" is an English page, and a model with a "!" (after a click) is an
// Arabic, right-to-left page.
func liveDocTestApp() *liveApp {
	app := newBindingTestApp("sky_sid")
	app.document = func(model any) any {
		s, _ := model.(string)
		if strings.Contains(s, "!") {
			return []any{docAttrADT("lang", "ar"), docAttrADT("dir", "rtl"), docAttrADT("data-theme", `d"<&`)}
		}
		return []any{docAttrADT("lang", "en"), docAttrADT("dir", "ltr")}
	}
	return app
}

// The first page writes the attributes on <html> and hands the client the
// set in its config block.
func TestLiveDocumentFirstPageWritesHtmlAttributes(t *testing.T) {
	app := liveDocTestApp()
	defer app.store.Close()
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	page := rr.Body.String()
	if !strings.HasPrefix(page, `<!DOCTYPE html><html lang="en" dir="ltr"><head>`) {
		t.Fatalf("the page must start with the app's <html> attributes:\n%.200s", page)
	}
	if !strings.Contains(page, `"doc":[{"name":"lang","value":"en"},{"name":"dir","value":"ltr"}]`) {
		t.Fatalf("the config block must carry the set:\n%s", page)
	}
}

// An app with no Live.withDocument renders the page exactly as before: a bare
// <html> start tag and no "doc" in the config block.
func TestLiveDocumentAbsentKeepsThePageAsBefore(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	defer app.store.Close()
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	page := rr.Body.String()
	if !strings.HasPrefix(page, `<!DOCTYPE html><html><head>`) {
		t.Fatalf("a page with no document function must keep a bare <html>:\n%.200s", page)
	}
	if strings.Contains(page, `"doc"`) || strings.Contains(page, "sky-doc") {
		t.Fatalf("a page with no document function must carry no doc:\n%s", page)
	}
}

// A click that changes the set sends the new set as the "doc" operation of
// the reply. A click whose view changes but whose set does not sends no doc.
func TestLiveDocumentSendsTheSetOnlyWhenItChanges(t *testing.T) {
	app := liveDocTestApp()
	defer app.store.Close()
	sid, cookie := mintSession(t, app, "sky_sid")

	rr := postEvent(app, cookie, eventBody(sid, clickHandlerID(t, app, sid)))
	if rr.Code != http.StatusOK {
		t.Fatalf("event 1: status %d: %s", rr.Code, rr.Body.String())
	}
	var reply struct {
		Patches []any     `json:"patches"`
		Doc     []docAttr `json:"doc"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &reply); err != nil {
		t.Fatalf("event 1: reply is not JSON: %v\n%s", err, rr.Body.String())
	}
	want := []docAttr{{"lang", "ar"}, {"dir", "rtl"}, {"data-theme", `d"<&`}}
	if !sameDocAttrs(reply.Doc, want) {
		t.Fatalf("event 1: doc = %+v, want %+v\n%s", reply.Doc, want, rr.Body.String())
	}

	// The second click changes the view (the label) but not the set.
	rr = postEvent(app, cookie, eventBody(sid, clickHandlerID(t, app, sid)))
	if rr.Code != http.StatusOK {
		t.Fatalf("event 2: status %d: %s", rr.Code, rr.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("event 2: reply is not JSON: %v\n%s", err, rr.Body.String())
	}
	if _, has := raw["doc"]; has {
		t.Fatalf("event 2: an unchanged set must not be sent:\n%s", rr.Body.String())
	}
	if string(raw["patches"]) == "[]" {
		t.Fatalf("event 2: the view changed, so the reply must carry patches:\n%s", rr.Body.String())
	}
}

// The SSE wire forms: a delta frame carries the set only when it changed; a
// full-body frame carries it whenever the app has a document function, so a
// tab that missed a frame converges.
func TestLiveDocumentFrameEncoding(t *testing.T) {
	set := []docAttr{{"lang", "fr"}}
	delta := encodePatchesEventFromSnapshot(frameSnapshot{seq: 1, doc: set, docKnown: true}, nil)
	if strings.Contains(delta, `"doc"`) {
		t.Fatalf("an unchanged delta frame must carry no doc: %s", delta)
	}
	delta = encodePatchesEventFromSnapshot(frameSnapshot{seq: 2, doc: set, docKnown: true, docChanged: true}, nil)
	if !strings.Contains(delta, `"doc":[{"name":"lang","value":"fr"}]`) {
		t.Fatalf("a changed delta frame must carry the set: %s", delta)
	}
	full := encodeSSEFrameFromSnapshot(frameSnapshot{seq: 3, body: "<p>x</p>", doc: nil, docKnown: true})
	if !strings.Contains(full, `"doc":[]`) {
		t.Fatalf("a full frame of an app with an empty set must carry []: %s", full)
	}
	none := encodeSSEFrameFromSnapshot(frameSnapshot{seq: 4, body: "<p>x</p>"})
	if strings.Contains(none, `"doc"`) {
		t.Fatalf("an app with no document function sends no doc: %s", none)
	}
}

// A full-body HTML reply carries the set as a trailing comment before the
// scroll comment, so the client can take both from the end.
func TestLiveDocumentHTMLReplyMetadata(t *testing.T) {
	body := appendLiveDocMetadata("<div>v</div>", true, []docAttr{{"lang", "ar"}})
	if !strings.HasPrefix(body, "<div>v</div><!--sky-doc:") || !strings.HasSuffix(body, "-->") {
		t.Fatalf("unexpected doc metadata: %s", body)
	}
	if got := appendLiveDocMetadata("<div>v</div>", false, nil); got != "<div>v</div>" {
		t.Fatalf("no document function must add nothing: %s", got)
	}
}
