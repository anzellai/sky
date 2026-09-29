package rt

// nav.go — Std.Nav: `pushUrl` / `replaceUrl` move the browser's address bar
// from `update`, without a page reload.
//
// The command is the same `cmdT` value on every target; each runner applies
// it:
//   - Sky.Live (live.go runCmd, live_nav_delivery.go): the server sends the
//     tab that caused the update an SSE "nav" frame. The browser client moves
//     the URL with the History API; a path or query change fetches the page
//     like a `sky-nav` link, so the server applies the route and `onNavigate`.
//   - Sky.Spa (live_wasm.go): the wasm client moves the URL and routes the
//     same way a link click does (spaNavigate).
//   - Terminal and plain webview targets have no address bar and ignore it.
//
// Only a same-origin reference is accepted: a path (`/orders/7`), a query
// (`?page=2`) or a fragment (`#top`; `#` alone clears the fragment). Anything
// else (an absolute URL, `//host`, a backslash, a control character) is
// refused where the command runs, with a classified error in the log
// (NavRejectedUrl), never followed.

import (
	"encoding/json"
	"strings"
)

// navCmd is the payload of a "nav" command.
type navCmd struct {
	URL     string `json:"url"`
	Replace bool   `json:"replace"`
}

// Nav_pushUrl builds the "nav" command that adds a history entry.
//
//	Std.Nav.pushUrl : String -> Cmd msg
func Nav_pushUrl(url any) SkyCmd {
	return cmdT{kind: "nav", payload: navCmd{URL: AsString(url)}}
}

// Nav_replaceUrl builds the "nav" command that replaces the current entry.
//
//	Std.Nav.replaceUrl : String -> Cmd msg
func Nav_replaceUrl(url any) SkyCmd {
	return cmdT{kind: "nav", payload: navCmd{URL: AsString(url), Replace: true}}
}

// navCmdOf reads the payload of a "nav" command.
func navCmdOf(c cmdT) (navCmd, bool) {
	nc, ok := c.payload.(navCmd)
	return nc, ok
}

// navTargetError is "" when url is a same-origin reference the runners
// follow, else why it is refused.
func navTargetError(url string) string {
	if url == "" {
		return "the URL is empty"
	}
	for _, r := range url {
		if r < 0x20 || r == 0x7f {
			return "the URL holds a control character"
		}
		if r == '\\' {
			return "the URL holds a backslash, which a browser reads as `/`"
		}
	}
	switch url[0] {
	case '/':
		if strings.HasPrefix(url, "//") {
			return "`//host` names another site"
		}
		return ""
	case '?', '#':
		return ""
	}
	return "only a path on this site (`/path`), a query (`?q`) or a fragment (`#id`) can be navigated to"
}

// navRejectedClass is the classified error a runner logs for a refused URL.
const navRejectedClass = "NavRejectedUrl"

// navRejectedMessage is the log line for a refused URL.
func navRejectedMessage(nc navCmd, why string) string {
	verb := "pushUrl"
	if nc.Replace {
		verb = "replaceUrl"
	}
	return "Std.Nav." + verb + " refused " + quoteForLog(nc.URL) + ": " + why +
		". The address bar is unchanged"
}

// logNavRejected writes the classified error (a var so a host test can
// observe it).
var logNavRejected = func(nc navCmd, why string) {
	logEmit(logLevelError, "error", navRejectedMessage(nc, why),
		map[string]any{"class": navRejectedClass, "url": nc.URL})
}

// quoteForLog quotes s for a log line, bounded so a long value cannot flood
// the log.
func quoteForLog(s string) string {
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

// navFrameData is the JSON a browser client receives for a nav command.
// encoding/json escapes `<`, `>`, `&` and the JS line separators, so the
// frame is data, never markup.
func navFrameData(nc navCmd) string {
	b, err := json.Marshal(nc)
	if err != nil {
		return `{"url":"","replace":false}`
	}
	return string(b)
}
