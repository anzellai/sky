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
		`"reconnecting", "Reconnecting…"`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing required SSE-state marker: %q", want)
		}
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
