//go:build cgo && darwin

package rt

import (
	"strings"
	"testing"
)

// The webview shell writes the app's <html> attributes (Webview.appWithDocument);
// with none, the shell is byte-identical to the shell before the feature.
func TestWebviewShellWritesHtmlAttributes(t *testing.T) {
	plain := webviewPageWrap("<p>hi</p>")
	if !strings.HasPrefix(plain, `<!DOCTYPE html><html><head>`) || plain != webviewPageWrapDoc("<p>hi</p>", "") {
		t.Fatalf("a shell with no attributes must keep a bare <html>:\n%.120s", plain)
	}
	attrs := renderDocAttrs(documentAttrs(docFn(docAttrADT("lang", "he"), docAttrADT("dir", "rtl"), docAttrADT("class", `x"<`)), nil))
	page := webviewPageWrapDoc("<p>hi</p>", attrs)
	if !strings.HasPrefix(page, `<!DOCTYPE html><html lang="he" dir="rtl" class="x&#34;&lt;"><head>`) {
		t.Fatalf("the shell must write the escaped attributes:\n%.160s", page)
	}
}

// An update that changes the set evaluates a script that removes the old
// names and sets the new values as JSON string literals, never as code.
func TestWebviewDocJS(t *testing.T) {
	js := webviewDocJS(
		[]docAttr{{"lang", "en"}, {"class", "a"}},
		[]docAttr{{"lang", "ar"}, {"dir", "rtl"}, {"data-x", "</script>\"\u2028"}},
	)
	for _, want := range []string{
		`e.removeAttribute("class");`,
		`e.setAttribute("lang","ar");`,
		`e.setAttribute("dir","rtl");`,
		`e.setAttribute("data-x","\u003c/script\u003e\"\u2028");`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("webviewDocJS missing %s:\n%s", want, js)
		}
	}
	if strings.Contains(js, `removeAttribute("lang")`) {
		t.Fatalf("a name the new set keeps must not be removed:\n%s", js)
	}
}
