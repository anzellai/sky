package rt

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// L9 regression — a sub-app (the inline console) must NOT inherit the HOST's
// durable store from the LIVE_STORE env. Pre-fix, an empty sub-app store fell
// through chooseStore to skyGetenv("LIVE_STORE"), so the console opened a SECOND
// pool against the host DB — and with the fail-loud store policy, a sub-app
// store-connect failure in production would crash the whole host.
//
// Red-on-bug: in production, the buggy path (inherit "postgres" → unreachable →
// fail-loud) fires storeFatalf; the fixed path (default "memory") does not.
func TestSubAppDoesNotInheritHostDurableStore(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_LIVE_STORE", "postgres")
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")

	// Fast retry + capture the fatal instead of exiting.
	oldA, oldSleep, oldFatal := storeConnectAttempts, storeSleep, storeFatalf
	storeConnectAttempts, storeSleep = 1, func(time.Duration) {}
	var fataled bool
	storeFatalf = func(string, ...any) { fataled = true }
	defer func() { storeConnectAttempts, storeSleep, storeFatalf = oldA, oldSleep, oldFatal }()

	noopUpd := func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} }
	cfg := map[string]any{
		"Init":          func(req any) any { return SkyTuple2{V0: "m", V1: cmdT{kind: "none"}} },
		"Update":        noopUpd,
		"View":          func(model any) any { return velement("div", nil, nil) },
		"Subscriptions": func(model any) any { return cmdT{kind: "none"} },
		"Routes":        []any{},
		"NotFound":      "home",
		"Store":         "",
		"StorePath":     "",
	}

	app := MountLiveSubAppInProcess(http.NewServeMux(), "/_sky/console", cfg)
	// The mount is process-wide: release it, or the next test in this
	// process that serves an app with the inline console finds
	// /_sky/console already taken and panics (seen under -count=2).
	t.Cleanup(func() { unmountInProcessSubApp("/_sky/console", app) })
	if fataled {
		t.Fatal("L9: sub-app inherited the host's postgres store and fail-loud fired — it must default to memory")
	}
	if _, ok := app.store.(*memoryStore); !ok {
		t.Fatalf("L9: sub-app with empty store should use *memoryStore, got %T", app.store)
	}
}

// A sub-app's store banner names the sub-app. The inline console keeps its
// sessions in its own in-process memory store (L9 above), and it used to print
// the host app's banner, `[sky.live] session store: memory (ttl=…)`, word for
// word. On a backend with no session store of its own (a Sky.Spa split backend
// is a Sky.Http.Server app) that was the ONLY store line in the log, so an
// operator who set `[live] store = "postgres"` read it as the app ignoring the
// setting and falling back to memory in production.
func TestSubAppStoreBannerNamesTheSubApp(t *testing.T) {
	t.Setenv("SKY_LIVE_STORE", "postgres")
	buf := captureLog(t)

	noopUpd := func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} }
	cfg := map[string]any{
		"Init":          func(req any) any { return SkyTuple2{V0: "m", V1: cmdT{kind: "none"}} },
		"Update":        noopUpd,
		"View":          func(model any) any { return velement("div", nil, nil) },
		"Subscriptions": func(model any) any { return cmdT{kind: "none"} },
		"Routes":        []any{},
		"NotFound":      "home",
	}
	app := MountLiveSubAppInProcess(http.NewServeMux(), "/_sky/console", cfg)
	t.Cleanup(func() { unmountInProcessSubApp("/_sky/console", app) })

	out := buf.String()
	if strings.Contains(out, "[sky.live] session store: ") {
		t.Fatalf("the console sub-app printed the host app's store banner, so it reads as the "+
			"app's own store:\n%s", out)
	}
	if !strings.Contains(out, "[sky.live] sub-app /_sky/console session store: memory (ttl=") {
		t.Fatalf("the console sub-app's store banner does not name the sub-app:\n%s", out)
	}
}
