package rt

import (
	"net/url"
	"strconv"
	"strings"
)

// spa_payload.go — the portable (host-testable) pieces of the Sky.Spa DOM
// driver in dom_render_wasm.go: event payload shaping, the file-size message,
// and the route path the client matches.

// payloadString turns an event payload into the String a `String -> msg`
// handler takes. A checkbox's Bool arrives as "true" / "false", matching
// what Sky.Live sends for the same event.
func payloadString(p any) string {
	switch v := p.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

// payloadBool turns an event payload into the Bool a `Bool -> msg` handler
// (Html.Events.onCheck) takes.
func payloadBool(p any) bool {
	switch v := p.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "on"
	}
	return false
}

// spaHumanBytes renders a byte count for a user-facing message. The file-size
// limit message used integer megabytes, so a limit under 1 MB read "Max 0MB".
func spaHumanBytes(n int) string {
	switch {
	case n >= 1000*1000:
		return trimDecimal(float64(n)/1e6) + " MB"
	case n >= 1000:
		return trimDecimal(float64(n)/1e3) + " KB"
	case n == 1:
		return "1 byte"
	}
	return strconv.Itoa(n) + " bytes"
}

func trimDecimal(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// spaFileTooLargeMessage is the message shown when a picked file exceeds
// Ui.fileMaxSize / Events.fileMaxSize.
func spaFileTooLargeMessage(max int) string {
	return "That file is too large. The maximum size is " + spaHumanBytes(max) + "."
}

// spaRoutePath turns the browser's location.pathname (percent-encoded: a
// non-ASCII segment arrives as `J%C3%B6rg`) into the DECODED path the route
// table matches — the same string Go's net/http hands the server as
// r.URL.Path, which Sky.Live's matchRoute and the Spa SSR resolver
// (Spa_ssrResolveModel, fed req.path) match. Without this the SSR page and
// the Live page captured `Jörg` while the Spa client captured `J%C3%B6rg`
// for the same URL (SPA-5). An undecodable path is used as it is.
func spaRoutePath(pathname string) string {
	if !strings.Contains(pathname, "%") {
		return pathname
	}
	dec, err := url.PathUnescape(pathname)
	if err != nil {
		return pathname
	}
	return dec
}
