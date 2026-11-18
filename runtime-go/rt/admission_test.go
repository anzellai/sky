//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// useAdmission installs a fresh gate for one test and puts the previous one
// back after it.
func useAdmission(t *testing.T, rpcLimit, apiLimit, retryMs int, hook any) {
	t.Helper()
	prev := admission
	admission = newAdmissionGate()
	admission.install(rpcLimit, apiLimit, retryMs, hook)
	t.Cleanup(func() { admission = prev })
}

// blockingRoute is a Server.rpc / Server.api route whose handler counts its
// runs and waits on `release` (closed by the test) before it answers.
func blockingRoute(mk func(string, any) any, spec string) (route any, runs *int, entered chan struct{}, release chan struct{}, mu *sync.Mutex) {
	mu = &sync.Mutex{}
	runs = new(int)
	entered = make(chan struct{}, 8)
	release = make(chan struct{})
	handler := func(req SkyRequest) any {
		return func() any {
			mu.Lock()
			*runs++
			mu.Unlock()
			entered <- struct{}{}
			<-release
			return SkyResponse{Status: 200, Body: "ran", ContentType: "text/plain"}
		}
	}
	return mk(spec, handler), runs, entered, release, mu
}

func rpcJSONPost(h http.Handler, origin string) *httptest.ResponseRecorder {
	return rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type": "application/json",
		"Origin":       origin,
	})
}

func waitEntered(t *testing.T, entered chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first handler never started")
	}
}

// Limit 1 on the Rpc class: while one RPC runs, a second is refused at once
// with 503 + Retry-After (whole seconds, rounded up) and the JSON body of a
// guard refusal, and its handler never runs. After the first returns, the
// class is free again and the in-flight count is back to 0.
func TestAdmission_RpcOverTheLimitIs503AndNeverRunsTheHandler(t *testing.T) {
	useAdmission(t, 1, 0, 1500, nil)
	resetCsrf(t)
	route, runs, entered, release, mu := blockingRoute(func(s string, h any) any { return Server_rpc(s, h) }, "POST /_rpc/Save")
	mux, _ := serverRouteMux([]any{route})
	h := CSRFMiddleware(mux)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- rpcJSONPost(h, "http://app.example") }()
	waitEntered(t, entered)

	rec := rpcJSONPost(h, "http://app.example")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("second RPC: status=%d Retry-After=%q, want 503 and 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), `"status":"overloaded"`) || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("refusal body: %s", rec.Body.String())
	}
	close(release)
	if first := <-done; first.Code != 200 {
		t.Fatalf("the admitted RPC: status %d", first.Code)
	}
	mu.Lock()
	n := *runs
	mu.Unlock()
	if n != 1 {
		t.Fatalf("the refused RPC ran its handler: runs=%d", n)
	}
	if got := admission.inFlight[admissionRpc].Load(); got != 0 {
		t.Fatalf("in-flight count = %d after both returned, want 0", got)
	}
	if rec := rpcJSONPost(h, "http://app.example"); rec.Code != 200 {
		t.Fatalf("after the burst the class admits again: %d", rec.Code)
	}
}

// The origin check comes first: a foreign-origin RPC is a 403 even while the
// class is full, never a 429 / 503 that would invite a retry.
func TestAdmission_BadOriginIs403NotAnAdmissionRefusal(t *testing.T) {
	useAdmission(t, 1, 0, 1000, nil)
	resetCsrf(t)
	route, _, entered, release, _ := blockingRoute(func(s string, h any) any { return Server_rpc(s, h) }, "POST /_rpc/Save")
	mux, _ := serverRouteMux([]any{route})
	h := CSRFMiddleware(mux)
	go rpcJSONPost(h, "http://app.example")
	waitEntered(t, entered)
	defer close(release)
	rec := rpcJSONPost(h, "https://evil.example")
	if rec.Code != http.StatusForbidden || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("foreign origin under load: status=%d Retry-After=%q, want 403 and none", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// A handler that panics still gives its slot back.
func TestAdmission_SlotIsReleasedAfterAHandlerPanic(t *testing.T) {
	useAdmission(t, 0, 1, 1000, nil)
	route := Server_api("GET /boom", func(req SkyRequest) any {
		return func() any { panic("boom") }
	})
	mux, _ := serverRouteMux([]any{route})
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
		if rec.Code != 500 {
			t.Fatalf("run %d: status %d, want the panic's 500 (not a 503 from a leaked slot)", i, rec.Code)
		}
	}
	if got := admission.inFlight[admissionApi].Load(); got != 0 {
		t.Fatalf("in-flight count = %d after a panic, want 0", got)
	}
}

// The hook reads the request and the load; Throttle is 429, Shed is 503, each
// with the hook's Retry-After, and Admit runs the handler. A refusal is
// counted in sky_admission_refused_total.
func TestAdmission_HookDecisionsMapToStatuses(t *testing.T) {
	var seen []string
	hook := func(req, session, cls, route, inFlight, limit any) any {
		r := req.(SkyRequest)
		seen = append(seen, AsString(cls)+"|"+AsString(route)+"|"+itoa(AsInt(inFlight))+"|"+itoa(AsInt(limit)))
		code := -1 // Admit
		switch r.Query["mode"] {
		case "throttle":
			code = 2 * 2500 // Throttle 2500
		case "shed":
			code = 2*800 + 1 // Shed 800
		}
		return func() any { return Ok[any, any](code) }
	}
	useAdmission(t, 0, 5, 1000, hook)
	runs := 0
	route := Server_api("GET /items", func(req SkyRequest) any {
		return func() any {
			runs++
			return SkyResponse{Status: 200, Body: "ok", ContentType: "text/plain"}
		}
	})
	mux, _ := serverRouteMux([]any{route})
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items?mode="+q, nil))
		return rec
	}
	before := refusedCount("api", "429")
	if rec := get("throttle"); rec.Code != 429 || rec.Header().Get("Retry-After") != "3" || !strings.Contains(rec.Body.String(), `"throttled"`) {
		t.Fatalf("Throttle: %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	if rec := get("shed"); rec.Code != 503 || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Shed: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := get("admit"); rec.Code != 200 || runs != 1 {
		t.Fatalf("Admit: %d runs=%d", rec.Code, runs)
	}
	if strings.Join(seen, ",") != "api|/items|1|5,api|/items|1|5,api|/items|1|5" {
		t.Fatalf("the hook saw %v", seen)
	}
	if refusedCount("api", "429") != before+1 {
		t.Fatal("a throttled request is counted in sky_admission_refused_total")
	}
}

// A hook that fails sheds the request (503), it does not let it through.
func TestAdmission_FailingHookSheds(t *testing.T) {
	useAdmission(t, 0, 0, 1000, func(req, session, cls, route, inFlight, limit any) any {
		return func() any { return Err[any, any]("no") }
	})
	runs := 0
	route := Server_api("GET /x", func(req SkyRequest) any {
		return func() any { runs++; return SkyResponse{Status: 200, Body: "ok"} }
	})
	mux, _ := serverRouteMux([]any{route})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 503 || runs != 0 {
		t.Fatalf("failing hook: status=%d runs=%d, want 503 and no run", rec.Code, runs)
	}
}

// No policy: unlimited, nothing counted (behaviour unchanged).
func TestAdmission_DefaultIsUnlimited(t *testing.T) {
	prev := admission
	admission = newAdmissionGate()
	t.Cleanup(func() { admission = prev })
	route := Server_api("GET /x", func(req SkyRequest) any {
		return func() any { return SkyResponse{Status: 200, Body: "ok"} }
	})
	mux, _ := serverRouteMux([]any{route})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 200 || admission.inFlight[admissionApi].Load() != 0 {
		t.Fatalf("default: status=%d", rec.Code)
	}
}

// The operator's variable wins over the code's limit; 0 turns it off.
func TestAdmission_OperatorEnvWinsOverTheCode(t *testing.T) {
	t.Setenv("SKY_ADMISSION_RPC_LIMIT", "7")
	t.Setenv("SKY_ADMISSION_API_LIMIT", "0")
	useAdmission(t, 2, 3, 1000, nil)
	if admission.effectiveLimit(admissionRpc) != 7 || admission.effectiveLimit(admissionApi) != 0 {
		t.Fatalf("limits = %d / %d, want 7 / 0", admission.effectiveLimit(admissionRpc), admission.effectiveLimit(admissionApi))
	}
	t.Setenv("SKY_ADMISSION_RPC_LIMIT", "not-a-number")
	admission.resolveLimits()
	if admission.effectiveLimit(admissionRpc) != 2 {
		t.Fatal("an unparsable variable leaves the code's limit")
	}
}

// App.api on Sky.Live (serveAPI) is behind the same Api class.
func TestAdmission_LiveApiRouteIsGated(t *testing.T) {
	useAdmission(t, 0, 1, 1000, nil)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	runs := 0
	app := newMirrorTestApp()
	ar := apiRoute{method: "GET", pattern: "/api/slow", handler: func(req SkyRequest) any {
		return func() any {
			runs++
			entered <- struct{}{}
			<-release
			return Ok[any, any](SkyResponse{Status: 200, Body: "ok"})
		}
	}}
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		app.serveAPI(ar, nil, rec, httptest.NewRequest(http.MethodGet, "/api/slow", nil))
		done <- rec.Code
	}()
	waitEntered(t, entered)
	rec := httptest.NewRecorder()
	app.serveAPI(ar, nil, rec, httptest.NewRequest(http.MethodGet, "/api/slow", nil))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("second Live api call: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	close(release)
	if code := <-done; code != 200 || runs != 1 {
		t.Fatalf("first call %d, runs %d", code, runs)
	}
	if admission.inFlight[admissionApi].Load() != 0 {
		t.Fatal("the Live api slot is released")
	}
}

func TestAdmission_DecisionCodesAndRetryAfter(t *testing.T) {
	if d := decodeAdmissionDecision(-1); d.status != 0 {
		t.Fatal("-1 is Admit")
	}
	if d := decodeAdmissionDecision(2 * 1200); d.status != 429 || d.retryAfterMs != 1200 {
		t.Fatalf("throttle: %+v", d)
	}
	if d := decodeAdmissionDecision(2*0 + 1); d.status != 503 || d.retryAfterMs != 0 {
		t.Fatalf("shed 0: %+v", d)
	}
	for ms, want := range map[int64]string{0: "0", 1: "1", 1000: "1", 1001: "2", -5: "0"} {
		if got := retryAfterSeconds(ms); got != want {
			t.Errorf("retryAfterSeconds(%d) = %q, want %q", ms, got, want)
		}
	}
}

func refusedCount(class, status string) float64 {
	for _, m := range telemetry.Default().Snapshot() {
		if m.Name == "sky_admission_refused_total" && m.Labels["class"] == class && m.Labels["status"] == status {
			return m.Value
		}
	}
	return 0
}

// sessionHook records the session the hook is given ("" is Nothing) and
// admits every request.
func sessionHook(seen *[]string) any {
	return func(req, session, cls, route, inFlight, limit any) any {
		*seen = append(*seen, AsString(session))
		return func() any { return Ok[any, any](-1) }
	}
}

// On a Sky.Spa split backend the hook gets the session id of a VERIFIED
// `sky_spa` token: a forged token, a token signed with another key, a
// signed-out token and no cookie all give Nothing (""). A client cannot dodge
// a per-session throttle by editing its cookie.
func TestAdmission_HookSeesTheVerifiedSpaSession(t *testing.T) {
	withMemoryRevocations(t)
	prevSecret := admissionSpaSecret
	admissionSpaSecret = func() any { return spaTestSecret }
	t.Cleanup(func() { admissionSpaSecret = prevSecret })
	admissionSpaSessions.Store(true)
	t.Cleanup(func() { admissionSpaSessions.Store(false) })
	var seen []string
	useAdmission(t, 0, 0, 1000, sessionHook(&seen))
	route := Server_api("GET /x", func(req SkyRequest) any {
		return func() any { return SkyResponse{Status: 200, Body: "ok"} }
	})
	mux, _ := serverRouteMux([]any{route})
	get := func(cookie string) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if cookie != "" {
			req.Header.Set("Cookie", "sky_spa="+cookie)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	}
	tok := spaSign(t, "", spaSignedIn())
	claims, _ := spaVerifyClaims(tok)
	sid := claimString(claims, "sid")
	other, _ := Spa_signSession(Secret{v: "another-key-another-key-another-key-0000"}, "", spaSignedIn()).(SkyResult[any, any])
	ended := spaSign(t, "", map[string]any{"p0": "someone else"})
	spaEnd(t, ended)
	get("")
	get(tok)
	get(tok[:len(tok)-2] + "xx")
	get(other.OkValue.(string))
	get(ended)
	if want := []string{"", sid, "", "", ""}; strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("the hook saw sessions %q, want %q", seen, want)
	}
	// A plain Sky.Http.Server program has no runtime session.
	admissionSpaSessions.Store(false)
	seen = nil
	get(tok)
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("a program that is not a Sky.Spa backend has no session, the hook saw %q", seen)
	}
}

// On Sky.Live the hook gets the session id the runtime resolves, only when
// the session store holds it: a made-up id of the right shape gives Nothing.
func TestAdmission_HookSeesTheLiveSession(t *testing.T) {
	var seen []string
	useAdmission(t, 0, 0, 1000, sessionHook(&seen))
	app := newMirrorTestApp()
	known := app.store.NewID()
	app.store.Set(known, &liveSession{model: "m", handlers: map[string]any{}})
	forged := app.store.NewID()
	ar := apiRoute{method: "GET", pattern: "/api/x", handler: func(req SkyRequest) any {
		return func() any { return Ok[any, any](SkyResponse{Status: 200, Body: "ok"}) }
	}}
	call := func(sid string) {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		if sid != "" {
			req.Header.Set("Cookie", app.cookieNameOrDefault()+"="+sid)
		}
		rec := httptest.NewRecorder()
		app.serveAPI(ar, nil, rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	}
	call("")
	call(known)
	call(forged)
	if want := []string{"", known, ""}; strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("the hook saw sessions %q, want %q", seen, want)
	}
}

// The store answers "does this session exist" without loading a model: the
// durable stores implement sessionPresence.
func TestSessionPresenceIsImplementedByTheDurableStores(t *testing.T) {
	var _ sessionPresence = (*sqliteStore)(nil)
	var _ sessionPresence = (*postgresStore)(nil)
	var _ sessionPresence = (*redisStore)(nil)
	st := newMemoryStore(time.Minute)
	defer st.Close()
	st.Set("a", &liveSession{})
	if !storeHasSession(st, "a") || storeHasSession(st, "b") || storeHasSession(st, "") {
		t.Fatal("storeHasSession must report only a stored id")
	}
}

// A visitor who still holds a Sky.Spa token in the legacy `sky_sid` cookie
// gets the same verified session as one with `sky_spa`: a v0.27.0 token under
// the old name, and a pre-v0.27.0 token (no sid) converted the way every RPC
// converts it (spaEffectiveToken). A forged legacy cookie, and a Sky.Live
// session id in `sky_sid`, give Nothing.
func TestAdmission_HookSeesTheLegacySpaSession(t *testing.T) {
	withMemoryRevocations(t)
	resetSpaLegacyCache(t)
	prevSecret := admissionSpaSecret
	admissionSpaSecret = func() any { return spaTestSecret }
	t.Cleanup(func() { admissionSpaSecret = prevSecret })
	admissionSpaSessions.Store(true)
	t.Cleanup(func() { admissionSpaSessions.Store(false) })
	var seen []string
	useAdmission(t, 0, 0, 1000, sessionHook(&seen))
	route := Server_api("GET /x", func(req SkyRequest) any {
		return func() any { return SkyResponse{Status: 200, Body: "ok"} }
	})
	mux, _ := serverRouteMux([]any{route})
	get := func(cookie string) string {
		seen = nil
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Cookie", "sky_sid="+cookie)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 || len(seen) != 1 {
			t.Fatalf("status %d, hook calls %d", rec.Code, len(seen))
		}
		return seen[0]
	}
	// A v0.27.0 token (with a sid) still under the legacy name.
	tok := spaSign(t, "", spaSignedIn())
	claims, _ := spaVerifyClaims(tok)
	if got := get(tok); got == "" || got != claimString(claims, "sid") {
		t.Fatalf("a v0.27.0 token in sky_sid gave session %q, want %q", got, claimString(claims, "sid"))
	}
	// A pre-v0.27.0 token: converted once, to the sid the RPC path also uses.
	legacy := legacySpaToken(t, spaTestSecret, 3600)
	converted := Spa_sessionToken(spaTestSecret, spaRequestWith(map[string]string{"sky_sid": legacy})).(string)
	cc, ok := spaVerifyClaims(converted)
	if !ok {
		t.Fatal("precondition: the legacy token converts")
	}
	if got := get(legacy); got == "" || got != claimString(cc, "sid") {
		t.Fatalf("a pre-v0.27.0 token gave session %q, want the converted sid %q", got, claimString(cc, "sid"))
	}
	// Forged: a legacy token signed with another key, and a damaged one.
	forged := legacySpaToken(t, Secret{v: "another-key-another-key-another-key-0000"}, 3600)
	if got := get(forged); got != "" {
		t.Fatalf("a forged legacy token gave session %q", got)
	}
	if got := get(legacy[:len(legacy)-2] + "xx"); got != "" {
		t.Fatalf("a damaged legacy token gave session %q", got)
	}
	if got := get("0123456789abcdef0123456789abcdef"); got != "" {
		t.Fatalf("a Sky.Live id in sky_sid gave session %q", got)
	}
}
