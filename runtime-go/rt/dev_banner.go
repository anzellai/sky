//go:build !js

package rt

import (
	"fmt"
	"html"
	"os"
	"strings"
)

// devBannerHTML returns the HTML snippet for the floating "🔍 Console"
// link injected at the bottom-right of every Sky.Live page (and every
// Sky.Http.Server text/html response) when the process is NOT running
// in production.
//
// Production-detection uses the same single-source-of-truth helper as
// /_sky/console auth-gating (productionFromEnv): ENV / SKY_ENV unset
// or one of {dev, development, local} -> dev, anything else -> prod.
// In prod we return "" so the banner disappears for staging /
// production deployments — no extra DOM noise, no info leak.
//
// The link target is `/_sky/console` on the SAME origin — the user
// app's runtime auto-mounts a reverse-proxy at that path (see
// maybeAutoMountConsole in subapp.go) to the bundled console child
// process. Same-origin keeps the banner working through tunnels,
// HTTPS, ngrok, fly.io, etc. without the user having to think about
// addressing. SKY_CONSOLE_URL overrides for ad-hoc setups (e.g.
// pointing at a remote shared console).
//
// Pure inline styling, no JS, no external assets — keeps the "walks
// our talk" promise of the Std.Ui console. The container is
// `position: fixed; z-index: 2147483646` (max int32 - 1 — leaves
// room for the existing status banner at max).
func devBannerHTML() string {
	if productionFromEnv() {
		return ""
	}
	if os.Getenv("SKY_DEV_BANNER") == "off" || os.Getenv("SKY_DEV_BANNER") == "0" {
		return ""
	}
	url := strings.TrimSpace(os.Getenv("SKY_CONSOLE_URL"))
	if url == "" {
		// Same-origin path. The runtime's reverse-proxy mount at
		// /_sky/console takes the request to the bundled child
		// console. target="_blank" gives users the dashboard
		// alongside their app, not replacing it.
		url = "/_sky/console"
	}
	// html.EscapeString defends against a malicious env value
	// breaking out of the href / title attribute.
	esc := html.EscapeString(url)
	return fmt.Sprintf(
		`<a id="__sky-dev-console" href="%s" target="_blank" rel="noopener" title="Sky Console (dev only)" `+
			`style="position:fixed;right:12px;bottom:12px;z-index:2147483646;`+
			`font:12px/1.4 ui-monospace,Menlo,monospace;`+
			`background:#1c2027;color:#7eb6ff;`+
			`border:1px solid #353b46;border-radius:6px;`+
			`padding:6px 10px;text-decoration:none;`+
			`box-shadow:0 2px 8px rgba(0,0,0,0.4);">`+
			`&#128269; Console</a>`,
		esc,
	)
}

// injectDevBanner inserts `banner` just before the closing </body>
// tag (case-insensitive). Falls back to appending if no </body> is
// found (some apps return body-only fragments). The result remains
// valid HTML in both cases.
func injectDevBanner(body, banner string) string {
	if banner == "" {
		return body
	}
	low := strings.ToLower(body)
	if idx := strings.LastIndex(low, "</body>"); idx >= 0 {
		return body[:idx] + banner + body[idx:]
	}
	return body + banner
}
