package rt

// Regression tests for the Sky.Live status banner + POST retry queue.
// We can't drive a JS runtime from Go without bringing in chromedp,
// so these are substring assertions on the embedded init script:
// they catch accidental removals or renames during refactors. The
// actual banner UX behaviour (does the banner show on disconnect?
// does the queue replay?) needs a manual browser test against any
// Sky.Live example — see docs/sky-live.md §Connection status banner.

import (
	"os"
	"strings"
	"testing"
)

// TestLiveJS_StatusBannerMarkers asserts the embedded init script
// contains the banner DOM injection + state machine identifiers.
// Catches the case where someone deletes one of the helper functions
// or renames it during a refactor, which would silently break the
// reconnect UX (banner never shows; users see clicks die silently).
func TestLiveJS_StatusBannerMarkers(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		// State variable + setter
		`var __skyStatus = "connected";`,
		`function __skySetStatus(state, msg) {`,
		// Banner DOM injection
		`function __skyInjectStatusBanner() {`,
		`el.id = "__sky-status";`,
		`role`, // role="status"
		`aria-live`,
		// State variants — all three must be present so the banner
		// can express every state.
		`sky-status--connected`,
		`sky-status--reconnecting`,
		`sky-status--offline`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing required banner marker: %q", want)
		}
	}
}

// TestLiveJS_QueueAndRetryMarkers asserts the POST retry queue is
// present + wired up. If any of these go missing, network blips and
// deploy restarts will silently lose user clicks again.
func TestLiveJS_QueueAndRetryMarkers(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		`var __skyEventQueue = [];`,
		`function __skyPostEvent(body) {`,
		`function __skyOnPostSuccess() {`,
		`function __skyOnPostFailure(body) {`,
		`function __skyScheduleRetry() {`,
		`function __skyDrainQueue() {`,
		`Math.pow(2, __skyRetryAttempts - 1)`, // exponential backoff
		`__skyEventQueue.shift()`,             // FIFO
		`__skyEventQueue.push(body)`,
		// SSE-open drains the queue (early reconnect signal).
		`if (__skyEventQueue.length > 0) __skyDrainQueue();`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing required queue/retry marker: %q", want)
		}
	}
}

// TestLiveJS_SSEStateTransitions asserts the SSE open/error handlers
// flip __skyStatus. Without these, the banner would only react to
// POST failures and miss the SSE-only outage case (server still
// accepting POSTs but SSE blocked by a flaky proxy).
func TestLiveJS_SSEStateTransitions(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		`__skySSE.addEventListener("open"`,
		`__skySSE.addEventListener("error"`,
		`__skyStatusGraceTimer`, // 500ms grace before showing reconnecting
		`"reconnecting", __skyMsgReconnecting`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing required SSE-state marker: %q", want)
		}
	}
}

// TestLiveJS_ClosedReadyStateForcesReopen guards the Caddy/Nginx 502
// recovery path. EventSource's spec says any non-200 response (502 from
// a reverse proxy when upstream is down, 504 timeout, wrong content
// type) permanently closes the connection — readyState becomes
// CLOSED(2) and the browser does NOT auto-retry. Without the
// readyState===2 branches in both the error handler and the watchdog,
// stopping the upstream behind Caddy and restarting it leaves the page
// permanently disconnected (the user-reported bug).
func TestLiveJS_ClosedReadyStateForcesReopen(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		// Error handler: if browser closed permanently, force reopen.
		`if (__skySSE && __skySSE.readyState === 2) {`,
		`__skyForceReopenSSE();`,
		// Watchdog: same backstop for the case where the error event
		// was missed (race during initial handshake, browser quirks).
		`if (__skySSE.readyState === 2)`,
		// Watchdog also covers the no-SSE-and-no-reopen-pending state.
		`if (!__skySSE && __skySseReopenTimer === null)`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing CLOSED-readyState recovery marker: %q", want)
		}
	}
}

// TestLiveJS_SessionLossProbe asserts the session-loss-detect probe is
// wired into the force-reopen path. Without it, a server restart with
// the memory store (or a sky.toml [live] store change) leaves every
// browser stuck at "Reconnecting…" forever — every reopen 404s on
// session-not-found, every reopen attempt counts as a retry, and after
// MaxAttempts the page sits at "offline" with no recovery short of
// the user manually refreshing. The probe POSTs a fake Msg, reads the
// 404 + X-Sky-Live: 1 + "session not found" body, and triggers
// window.location.reload() to recover automatically.
func TestLiveJS_SessionLossProbe(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		// Probe function exists and is invoked on every force-reopen.
		`function __skyProbeSessionLost()`,
		`__skyProbeSessionLost();`,
		// One-shot guard so a burst of failed reopens doesn't kick off
		// multiple reloads.
		`var __skyProbedReload = false;`,
		// The probe targets /_sky/event with a sentinel Msg name.
		`"__skySessionPing"`,
		// Body match — distinguishes session-not-found from
		// handler-not-found (both are 404 + X-Sky-Live).
		`indexOf("session not found")`,
		// Reload trigger.
		`window.location.reload();`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing session-loss-probe marker: %q", want)
		}
	}
}

// TestLiveJS_HandshakeAndHeartbeat asserts the wedge-detection plumbing
// is wired into the embedded init script: a hello handler, a heartbeat
// handler, the watchdog interval, and the force-reopen path that fires
// when the proxy returns 200-OK without a real SSE stream. These are
// the load-bearing pieces for recovering from a misbehaving reverse
// proxy that would otherwise leave the client stuck at "Reconnecting…"
// indefinitely. See docs/skylive/architecture.md §SSE wedge detection.
func TestLiveJS_HandshakeAndHeartbeat(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		`__skySSE.addEventListener("hello"`,
		`__skySSE.addEventListener("heartbeat"`,
		`function __skyOpenSSE()`,
		`function __skyForceReopenSSE()`,
		`function __skyWatchdog()`,
		`__skyHelloOk`,
		`__skyOpenAt`,
		`__skyLastSseAt`,
		`setInterval(__skyWatchdog, 5000)`,
		// On healed network, a successful POST also reopens SSE so a
		// server-pushed UI doesn't stay silently broken.
		`if (__skySSE === null)`,
		// Wedge detection: refuse to apply non-Sky-Live POST responses.
		`var skyMark = r.headers.get("X-Sky-Live")`,
		`throw new Error("non-sky response`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing wedge-detection marker: %q", want)
		}
	}
}

// TestLiveJS_I18nDefaults asserts the English defaults are templated
// into the script when no cfg.status override is supplied. Catches the
// case where someone removes a default and the banner ships empty
// strings.
func TestLiveJS_I18nDefaults(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		`var __skyMsgReconnecting = "Reconnecting…";`,
		`var __skyMsgOffline = "Connection lost — refresh to retry";`,
		`var __skyHelloTimeoutMs = 8000;`,
		`var __skyHeartbeatTtlMs = 35000;`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing i18n default: %q", want)
		}
	}
}

// TestLiveJS_NoLiteralClosingScriptTag is a regression guard against
// the Cycle 4 HS bug: a comment inside the inline JS bundle contained
// a literal `</script>` substring (`bootstrapping comment</script>
// patterns`), and the HTML parser closed the inline `<script>...</script>`
// wrapper at that point — the bottom 40 KB of JS landed as plain
// text + the wire driver never bound any handler.
//
// The fix escapes the close tag in the comment text (e.g. `<\/script>`).
// This test pins that the rendered JS string never contains a literal
// `</script>` — every closing-script-tag-like substring must be
// escaped in a way that the HTML parser doesn't see it.
func TestLiveJS_NoLiteralClosingScriptTag(t *testing.T) {
	js := liveJSWithCfg("sid-test", loadLiveBannerConfig())
	if strings.Contains(js, "</script>") {
		// Find the offending region to make the failure actionable.
		idx := strings.Index(js, "</script>")
		start := idx - 60
		if start < 0 {
			start = 0
		}
		end := idx + 60
		if end > len(js) {
			end = len(js)
		}
		t.Fatalf("inline JS bundle contains literal `</script>` at offset %d — "+
			"HTML parser would close the wrapper script tag here.\n"+
			"Escape via `<\\/script>` in the JS source.\n"+
			"Context: ...%q...", idx, js[start:end])
	}
}

// TestLiveJS_I18nOverrides asserts user-supplied banner strings round-
// trip through liveJSWithCfg JSON-encoded so non-ASCII (the whole point
// of this knob) survives intact, and embedded quotes / newlines /
// </script> sentinels can't escape the JS string context. The banner
// renders via textContent so the DOM path is XSS-safe; this test guards
// the JS-context escaping (which the JSON encoder already provides).
func TestLiveJS_I18nOverrides(t *testing.T) {
	cfg := loadLiveBannerConfig()
	cfg.Reconnecting = "Reconnexion en cours…"
	cfg.Offline = `Connexion perdue — actualisez la page`
	js := liveJSWithCfg("sid", cfg)
	if !strings.Contains(js, `"Reconnexion en cours…"`) &&
		!strings.Contains(js, `"Reconnexion en cours…"`) {
		t.Errorf("user-supplied reconnecting string missing or wrongly encoded\n%s", js)
	}
	if !strings.Contains(js, `Connexion perdue`) {
		t.Errorf("user-supplied offline string missing")
	}
	// Adversarial: a string that would close the <script> if templated
	// raw must be JSON-escaped. </script> in JSON is "</script>"
	// or "<\/script>"; either form is safe.
	cfg.Reconnecting = `</script><img src=x onerror=alert(1)>`
	js2 := liveJSWithCfg("sid", cfg)
	if strings.Contains(js2, "</script><img") {
		t.Errorf("adversarial reconnecting string leaked unescaped: must be JSON-encoded\n%s", js2)
	}
}

// TestResolveBannerStrings asserts the cfg.status overlay reads
// PascalCase fields off both struct-shaped and map-shaped records.
// Sky's typed-codegen emits inline records as Go structs (PascalCase
// fields); maps come from an older code path used for some FFI shapes.
// Both must work because we don't control which one the user ends up
// with depending on whether they wrote the record inline or via a
// type alias.
func TestResolveBannerStrings(t *testing.T) {
	base := liveBannerConfig{Reconnecting: "x", Offline: "y"}

	// Struct shape (typical typed-codegen output).
	type statusRec struct {
		Reconnecting string
		Offline      string
	}
	type appCfg struct {
		Status statusRec
	}
	got := resolveBannerStrings(base, appCfg{Status: statusRec{
		Reconnecting: "Reconnexion…",
		Offline:      "Hors ligne",
	}})
	if got.Reconnecting != "Reconnexion…" || got.Offline != "Hors ligne" {
		t.Errorf("struct overlay failed: %+v", got)
	}

	// Partial overlay: missing field falls back to the base default.
	got = resolveBannerStrings(base, appCfg{Status: statusRec{
		Reconnecting: "Reconnexion…",
	}})
	if got.Reconnecting != "Reconnexion…" {
		t.Errorf("partial overlay reconnecting wrong: %q", got.Reconnecting)
	}
	if got.Offline != "y" {
		t.Errorf("partial overlay offline should fall back to base, got %q", got.Offline)
	}

	// No status field at all → all defaults preserved.
	type bareCfg struct{ Init any }
	got = resolveBannerStrings(base, bareCfg{})
	if got.Reconnecting != "x" || got.Offline != "y" {
		t.Errorf("no-status cfg must preserve defaults, got %+v", got)
	}

	// Map-shaped status (rare but possible at the FFI boundary).
	got = resolveBannerStrings(base, map[string]any{
		"Status": map[string]any{
			"Reconnecting": "Map-reconnexion",
			"Offline":      "Map-hors-ligne",
		},
	})
	if got.Reconnecting != "Map-reconnexion" || got.Offline != "Map-hors-ligne" {
		t.Errorf("map overlay failed: %+v", got)
	}
}

// TestLiveJS_BannerEnvVars verifies each SKY_LIVE_* env var maps
// onto the corresponding JS const. Catches typos in env-var names
// or accidental swaps (BASE_MS into MAX_MS slot) during refactors.
func TestLiveJS_BannerEnvVars(t *testing.T) {
	withEnv(t, map[string]string{
		"SKY_LIVE_BANNER":             "on",
		"SKY_LIVE_RETRY_BASE_MS":      "250",
		"SKY_LIVE_RETRY_MAX_MS":       "8000",
		"SKY_LIVE_RETRY_MAX_ATTEMPTS": "5",
		"SKY_LIVE_QUEUE_MAX":          "20",
	}, func() {
		js := liveJS("test-sid")
		want := []string{
			`var __skyBannerEnabled = true;`,
			`var __skyRetryBaseMs = 250;`,
			`var __skyRetryMaxMs = 8000;`,
			`var __skyRetryMaxAttempts = 5;`,
			`var __skyEventQueueMax = 20;`,
		}
		for _, w := range want {
			if !strings.Contains(js, w) {
				t.Errorf("env-templated JS missing: %q", w)
			}
		}
	})
}

// TestLiveJS_BannerOptOut: SKY_LIVE_BANNER=off flips __skyBannerEnabled
// to false; queue + retry stay active so events still replay (just
// without the chrome). Apps that render their own connection UI rely
// on this opt-out.
func TestLiveJS_BannerOptOut(t *testing.T) {
	cases := []string{"off", "0", "false"}
	for _, val := range cases {
		t.Run(val, func(t *testing.T) {
			withEnv(t, map[string]string{"SKY_LIVE_BANNER": val}, func() {
				js := liveJS("test-sid")
				if !strings.Contains(js, `var __skyBannerEnabled = false;`) {
					t.Errorf("SKY_LIVE_BANNER=%q should disable banner", val)
				}
				// Queue must still be wired — silent retries keep
				// working even when the user opts out of the chrome.
				if !strings.Contains(js, `function __skyPostEvent(body) {`) {
					t.Error("queue + retry should stay wired when banner is off")
				}
			})
		})
	}
}

// TestLiveJS_BannerInvalidEnvFallsBack: invalid SKY_LIVE_RETRY_*
// values (non-numeric, negative, zero) fall back to defaults rather
// than emit invalid JS or break the page. Validates parsePositiveInt
// gates the templated values.
func TestLiveJS_BannerInvalidEnvFallsBack(t *testing.T) {
	cases := []map[string]string{
		{"SKY_LIVE_RETRY_BASE_MS": "abc"},
		{"SKY_LIVE_RETRY_MAX_MS": "-100"},
		{"SKY_LIVE_RETRY_MAX_ATTEMPTS": "0"},
		{"SKY_LIVE_QUEUE_MAX": ""},
	}
	for i, env := range cases {
		t.Run(t.Name()+"_"+strString(i), func(t *testing.T) {
			withEnv(t, env, func() {
				js := liveJS("test-sid")
				// Defaults: 500 / 16000 / 10 / 50
				want := []string{
					`var __skyRetryBaseMs = 500;`,
					`var __skyRetryMaxMs = 16000;`,
					`var __skyRetryMaxAttempts = 10;`,
					`var __skyEventQueueMax = 50;`,
				}
				for _, w := range want {
					if !strings.Contains(js, w) {
						t.Errorf("invalid env %v should fall back: missing %q", env, w)
					}
				}
			})
		})
	}
}

// withEnv sets the given env vars for the duration of fn, restoring
// the prior values on exit (or unsetting them when they were unset).
func withEnv(t *testing.T, vars map[string]string, fn func()) {
	t.Helper()
	prior := map[string]string{}
	priorSet := map[string]bool{}
	for k := range vars {
		if v, ok := os.LookupEnv(k); ok {
			prior[k] = v
			priorSet[k] = true
		}
	}
	for k, v := range vars {
		os.Setenv(k, v)
	}
	defer func() {
		for k := range vars {
			if priorSet[k] {
				os.Setenv(k, prior[k])
			} else {
				os.Unsetenv(k)
			}
		}
	}()
	fn()
}

func strString(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return "many"
}
