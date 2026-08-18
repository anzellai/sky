package main

import (
	"regexp"
	"strings"
	"testing"
)

// The page skyforum actually served at 50c8dcee, trimmed to the three handler
// ids that matter. The first `.click` is the site title, wired to
// `Navigate HomePage` -- a Msg that is a NO-OP on the home page. The old
// handler rule ("first hid ending .click") chose it, and three archived runs
// (docs/perf/runs/attribution-20260815/viewsize/forum-r*) recorded 5,394
// interactions of a user clicking a logo that changes nothing, then supplied
// the 94-element point of the published "cost tracks view size" claim.
const forumHome = `<div sky-id="r.1#div.0#div.0#div" data-sky-hid="r.1#div.0#div.0#div.0#div.click" ` +
	`style="display: flex; flex-direction: column; color: rgba(255, 255, 255, 1);">skyforum</div>` +
	`<button sky-id="r.1#div.0#div.2#div.0#button" data-sky-hid="r.1#div.0#div.2#div.0#button.click" ` +
	`style="display: flex; padding: 2px;">sign in</button>` +
	`<button sky-id="r.1#div.1#div.0#div.1#div.0#button" data-sky-hid="r.1#div.1#div.0#div.1#div.0#button.click" ` +
	`style="display: flex; flex-direction: column; background-color: rgba(240, 240, 240, 1);">▲</button>` +
	`<form sky-id="r.1#div.1#form" data-sky-hid="r.1#div.1#form.submit"></form>`

func TestPickHandlerByContextNamesTheUpvoteButton(t *testing.T) {
	hid, err := pickHandler([]byte(forumHome), ".click", regexp.MustCompile(">▲<"))
	if err != nil {
		t.Fatalf("pickHandler: %v", err)
	}
	const want = "r.1#div.1#div.0#div.1#div.0#button.click"
	if hid != want {
		t.Errorf("hid = %q, want %q -- the context regex has to reach past the "+
			"inline style Std.Ui emits to the element's own text", hid, want)
	}
}

// The regression this file exists for: without a context the rule is "first
// .click", and on this page that is the no-op site title. The test pins the
// behaviour so it is impossible to reintroduce silently -- it is fine as a
// DEFAULT, and catastrophic as an unexamined one, which is why -self-check
// now requires a patch on all four of its interactions.
func TestPickHandlerWithoutContextTakesTheFirstClick(t *testing.T) {
	hid, err := pickHandler([]byte(forumHome), ".click", nil)
	if err != nil {
		t.Fatalf("pickHandler: %v", err)
	}
	if hid != "r.1#div.0#div.0#div.0#div.click" {
		t.Errorf("hid = %q, want the first .click on the page", hid)
	}
}

func TestPickHandlerBySuffixFindsTheFormSubmit(t *testing.T) {
	hid, err := pickHandler([]byte(forumHome), ".submit", nil)
	if err != nil {
		t.Fatalf("pickHandler: %v", err)
	}
	if hid != "r.1#div.1#form.submit" {
		t.Errorf("hid = %q, want the form submit", hid)
	}
}

// A handler that cannot be found is an ERROR, never a fallback to whichever
// other handler happens to be first. Substituting one silently is precisely
// how the corpus came to hold three runs of an empty exchange.
func TestPickHandlerRefusesToSubstitute(t *testing.T) {
	_, err := pickHandler([]byte(forumHome), ".click", regexp.MustCompile(">no such element<"))
	if err == nil {
		t.Fatal("pickHandler returned a handler for a context that matches nothing; " +
			"it must fail instead of quietly choosing another")
	}
	for _, want := range []string{"4 hids", "3 with that suffix", "will not"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q -- the message has to say what "+
				"WAS on the page so the caller can pick a real context", err, want)
		}
	}
}

func TestPickHandlerReportsAPageWithNoHandlers(t *testing.T) {
	_, err := pickHandler([]byte("<html><body>nothing interactive</body></html>"), ".click", nil)
	if err == nil || !strings.Contains(err.Error(), "no data-sky-hid") {
		t.Fatalf("err = %v, want a no-handlers-on-the-page error", err)
	}
}

// The context window has to be long enough to cross Std.Ui's inline style
// attribute (150-250 bytes in the pages measured) and short enough not to
// spill into the next sibling's text, or a context regex would match the
// wrong element and the run would measure the wrong handler.
func TestHidContextWindowClearsAnInlineStyle(t *testing.T) {
	if hidContextWindow < 260 {
		t.Errorf("hidContextWindow = %d; Std.Ui inline styles run to ~250 bytes, "+
			"so a shorter window cannot reach an element's text", hidContextWindow)
	}
	if hidContextWindow > 400 {
		t.Errorf("hidContextWindow = %d; too long and the regex matches the NEXT "+
			"element's text and names the wrong handler", hidContextWindow)
	}
}
