package rt

// v0.16.0 PR 3 — Sky Console production auth gate.
//
// Three modes selected by SKY_CONSOLE_AUTH:
//
//   token  → __Host-sky_console cookie with HKDF-derived signing key
//            from (SKY_CONSOLE_TOKEN, app build ID). Login is a POST
//            form (NOT GET — query strings leak via Referer). Default
//            for single-tenant deployments.
//   app    → row-poly optional `consoleAuth` callback on Live.app cfg.
//            Framework invokes it per request; Nothing → 403 + audit;
//            Just identity → set cookie + allow.
//   off    → console doesn't mount at all (telemetry still buffers
//            into the in-RAM rings + SQLite if SKY_CONSOLE_DB_PATH
//            set; the UI surface is just not exposed).
//
// Production gate: ENV != dev/development/local AND SKY_CONSOLE_AUTH
// unset → mount declines + emits `console.disabled reason=auth-unset`
// warn log. No silent open-to-the-world.
//
// Dev gate (ENV unset / dev / development / local) AND
// SKY_CONSOLE_AUTH unset → default to token-mode with a per-process
// random token written to .sky/console-token (gitignored). Zero-
// config in dev; opt-in to a stable token via env if you want
// shareable URLs.
//
// The hardened URL-handshake (existing console_auth.go) keeps the
// SkyDeploy iframe pattern working but adds:
//   - one-shot JTI via sync.Map (replays denied)
//   - aud-claim match against the runtime's build commit hash
//   - opt-in via SKY_CONSOLE_EMBED_ORIGIN — unset → URL handshake
//     entirely disabled. Closes the cookie/JWT confusion attack
//     surface from the v0.16 design debate.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/hkdf"
)

// consoleAuthMode is the resolved auth mode for a single binary's
// console mount. Snapshotted once at MountEmbeddedConsole time so
// runtime env mutation can't toggle it half-way through a request.
type consoleAuthMode int

const (
	consoleAuthModeOff       consoleAuthMode = iota // mount declined
	consoleAuthModeToken                            // __Host- cookie + login form
	consoleAuthModeApp                              // Sky-side callback
	consoleAuthModeDevOpen                          // dev: auto-token + zero-config
	consoleAuthModeUnsetProd                        // ENV=prod + SKY_CONSOLE_AUTH unset → decline
)

// consoleAuthCookieV2Name is the v0.16.0 token-mode session cookie.
// Distinct from the v0.15.x sky_console_sid name so the migration is
// clean — a stale v0.15.x cookie does NOT silently auth into the
// v0.16.0 console.
//
// The `__Host-` prefix is RFC 6265bis: browsers REQUIRE Secure +
// Path=/ + no Domain attr on any cookie with that prefix. The
// Path=/_sky/console exception below is honoured by every modern
// browser (Chrome/Firefox/Safari all permit a stricter Path while
// keeping the prefix's other guarantees). Cross-domain attacks
// against this cookie are structurally impossible.
const consoleAuthCookieV2Name = "__Host-sky_console"

// consoleAuthCookieV2MaxAge is the session lifetime — 4 hours per
// EMBEDDED.md L78. Long enough for sustained ops debugging, short
// enough to limit cookie-theft blast radius.
const consoleAuthCookieV2MaxAge = 4 * time.Hour

// consoleAuthCallback is the global handle to the app's `consoleAuth`
// field (set from Sky.Live's liveAppRun via SetConsoleAuthCallback).
// Sky.Http.Server apps have no Live.app cfg so this stays nil —
// `app`-mode is meaningless there and evaluateConsoleAuth falls
// through to token / off.
var consoleAuthCallback atomic.Value // any (the Sky callback)

// SetConsoleAuthCallback stores the app's `consoleAuth` field for
// later invocation. Called once during Sky.Live boot. nil OK (the
// common case for apps that don't set the field).
func SetConsoleAuthCallback(cb any) {
	if cb == nil {
		consoleAuthCallback.Store((*any)(nil))
		return
	}
	consoleAuthCallback.Store(&cb)
}

func getConsoleAuthCallback() any {
	v := consoleAuthCallback.Load()
	if v == nil {
		return nil
	}
	p, ok := v.(*any)
	if !ok || p == nil {
		return nil
	}
	return *p
}

// resolveConsoleAuthMode decides the auth posture for THIS binary.
// Reads env vars + the production gate; does NOT touch the request.
// Snapshotted once per mount; SIGHUP-driven re-snapshot is a v0.16.5
// follow-up.
func resolveConsoleAuthMode() consoleAuthMode {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("SKY_CONSOLE_AUTH")))
	prod := productionFromEnv()
	switch raw {
	case "off":
		return consoleAuthModeOff
	case "token":
		return consoleAuthModeToken
	case "app":
		return consoleAuthModeApp
	case "":
		// Unset — gate by env.
		if prod {
			return consoleAuthModeUnsetProd
		}
		// Dev. Zero-config: a random token gets generated + persisted
		// to .sky/console-token. Subsequent dev runs read the same
		// token so URLs stay stable across rebuilds.
		return consoleAuthModeDevOpen
	default:
		// Unknown value — refuse to silently fall back to something
		// more permissive. Caller logs.
		return consoleAuthModeOff
	}
}

// describeConsoleAuthMode is for log lines + decline pages.
func describeConsoleAuthMode(m consoleAuthMode) string {
	switch m {
	case consoleAuthModeOff:
		return "off"
	case consoleAuthModeToken:
		return "token"
	case consoleAuthModeApp:
		return "app"
	case consoleAuthModeDevOpen:
		return "dev-open"
	case consoleAuthModeUnsetProd:
		return "unset-prod"
	}
	return "unknown"
}

// consoleAuthState holds the resolved mode + cryptographic material
// for cookie signing. Cached so per-request work stays cheap.
type consoleAuthState struct {
	mode    consoleAuthMode
	signKey []byte // HKDF-derived per (secret, build ID, "sky-console-cookie")
}

var (
	consoleAuthStateMu     sync.RWMutex
	consoleAuthStateCached *consoleAuthState
)

// loadConsoleAuthState resolves + caches the per-binary auth state.
// First call does the work; subsequent calls return the cached value.
// On unknown env values it logs a warn line and falls back to off.
func loadConsoleAuthState() *consoleAuthState {
	consoleAuthStateMu.RLock()
	cached := consoleAuthStateCached
	consoleAuthStateMu.RUnlock()
	if cached != nil {
		return cached
	}
	consoleAuthStateMu.Lock()
	defer consoleAuthStateMu.Unlock()
	if consoleAuthStateCached != nil {
		return consoleAuthStateCached
	}
	mode := resolveConsoleAuthMode()
	st := &consoleAuthState{mode: mode}
	if mode == consoleAuthModeToken || mode == consoleAuthModeDevOpen || mode == consoleAuthModeApp {
		st.signKey = deriveConsoleSigningKey()
	}
	consoleAuthStateCached = st
	return st
}

// ResetConsoleAuthStateForTesting clears the snapshotted auth state
// so individual tests can re-run resolveConsoleAuthMode against
// different env vars. Test-only; not part of the public API.
func ResetConsoleAuthStateForTesting() {
	consoleAuthStateMu.Lock()
	consoleAuthStateCached = nil
	consoleAuthStateMu.Unlock()
}

// deriveConsoleSigningKey derives a 32-byte HMAC-SHA256 signing key
// using HKDF over (SKY_CONSOLE_TOKEN OR dev-token, build commit hash
// as salt, "sky-console-cookie" as info).
//
// In dev mode the secret is the auto-generated/persisted token from
// .sky/console-token. In token mode the user-supplied env var. App
// mode reuses the same signing key for its post-callback session
// cookie (no second secret to provision).
func deriveConsoleSigningKey() []byte {
	secret := strings.TrimSpace(os.Getenv("SKY_CONSOLE_TOKEN"))
	if secret == "" {
		// Dev-mode fallback / app-mode without an explicit token —
		// reach for the auto-generated dev token. Production callers
		// who didn't set SKY_CONSOLE_TOKEN AND aren't in app-mode
		// shouldn't reach this path (resolveConsoleAuthMode declines
		// to unset-prod first), but the safe default is to mint a
		// random key in memory rather than panic.
		secret = ensureDevConsoleToken()
	}
	bi := currentBuildInfo()
	salt := []byte(bi.Commit)
	if len(salt) == 0 || string(salt) == "dev" {
		// Stable salt even on local dev: use the executable path.
		// Two simultaneous local binaries with different paths get
		// distinct keys.
		if exe, err := os.Executable(); err == nil {
			salt = []byte(exe)
		}
	}
	r := hkdf.New(sha256.New, []byte(secret), salt, []byte("sky-console-cookie"))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		// HKDF over SHA-256 cannot fail in practice; treat as panic.
		panic(fmt.Sprintf("sky.console: HKDF derivation failed: %v", err))
	}
	return out
}

// ensureDevConsoleToken returns the dev-mode auto-generated token,
// generating + persisting one to .sky/console-token if absent. The
// file is created with 0600 perms (owner-only). Failures fall back
// to an in-memory random token (each restart invalidates URLs).
//
// EMBEDDED.md L168-170: "Apps that did NOTHING in v0.15.x: same
// behaviour. Embedded console in dev …" — this preserves zero-
// config dev access.
func ensureDevConsoleToken() string {
	const fileName = ".sky/console-token"
	if b, err := os.ReadFile(fileName); err == nil && len(b) >= 32 {
		return strings.TrimSpace(string(b))
	}
	tok := randomDevToken()
	_ = os.MkdirAll(filepath.Dir(fileName), 0o700)
	// Best-effort write; ignore errors (read-only CWD, sandboxed dev
	// env, …) — the in-memory token is still usable for one process
	// lifetime.
	_ = os.WriteFile(fileName, []byte(tok), 0o600)
	return tok
}

// randomDevToken — 32-byte hex token. Cryptographically random.
func randomDevToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// rand.Read should never fail on a healthy host; fall back to
		// a process-id-derived value so we don't panic in CI sandboxes.
		return fmt.Sprintf("dev-fallback-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ──── Cookie signing ─────────────────────────────────────────────

// signCookieValue formats a session cookie body: <subjectB64>.<expUnix>.<hmacB64>.
// HMAC binds (subject, exp); a tampered subject or expiry fails
// verifyCookieValue at the next request.
func signCookieValue(key []byte, subject string, ttl time.Duration) string {
	expUnix := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s.%d", base64.RawURLEncoding.EncodeToString([]byte(subject)), expUnix)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// verifyCookieValue parses + checks a v2 console cookie. Returns
// (subject, ok=true) on success. ok=false on any failure (split,
// b64, hmac mismatch, expired). The hmac compare is constant-time.
func verifyCookieValue(key []byte, value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", false
	}
	subjectB64, expStr, sigStr := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(subjectB64 + "." + expStr))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sigStr)) != 1 {
		return "", false
	}
	// Expired?
	var exp int64
	if _, err := fmt.Sscanf(expStr, "%d", &exp); err != nil {
		return "", false
	}
	if time.Now().Unix() >= exp {
		return "", false
	}
	sub, err := base64.RawURLEncoding.DecodeString(subjectB64)
	if err != nil {
		return "", false
	}
	return string(sub), true
}

// setConsoleV2Cookie writes the v2 cookie to w. The __Host- prefix
// REQUIRES Path=/ per RFC 6265bis §4.1.3.2 — sub-paths cause
// RFC-compliant clients (curl, modern Go http.Client, browsers
// implementing the latest draft) to reject the cookie outright.
// The path restriction we WANT — only send the cookie back to
// /_sky/console/* — comes from the SameSite=Strict + HttpOnly +
// Secure trio plus the inline-mounted console being the ONLY
// surface that reads consoleAuthCookieV2Name. So scope at Path=/
// is safe; the cookie can't leak via cross-site nav or non-Secure
// traffic.
func setConsoleV2Cookie(w http.ResponseWriter, key []byte, subject string) {
	value := signCookieValue(key, subject, consoleAuthCookieV2MaxAge)
	http.SetCookie(w, &http.Cookie{
		Name:     consoleAuthCookieV2Name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(consoleAuthCookieV2MaxAge.Seconds()),
	})
}

// clearConsoleV2Cookie zeros the cookie (logout, denial, mode change).
func clearConsoleV2Cookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     consoleAuthCookieV2Name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// ──── Three-mode gate ────────────────────────────────────────────

// evaluateConsoleAuth is the per-request gate. Returns ok=true when
// the request may proceed (token / cookie / callback all approved).
// On rejection, writes a response (401 / 403 / 503) AND returns
// false.
//
// modeOverride !=nil lets test code force a mode; nil → use the
// cached snapshot.
func evaluateConsoleAuth(w http.ResponseWriter, r *http.Request) bool {
	st := loadConsoleAuthState()

	// Sub-app context: a sub-app shouldn't host its own console
	// (parent owns it). This is short-circuited at mount time but
	// the per-request guard is cheap.
	if base := os.Getenv("SKY_LIVE_BASE_PATH"); base != "" {
		http.NotFound(w, r)
		return false
	}

	if IsServerless() {
		// Container scheduler will reap the binary between requests
		// — the 1Hz polling console UI cannot work.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unavailable","hint":"dashboard requires always-on CPU; use OTLP push instead"}`))
		return false
	}

	switch st.mode {
	case consoleAuthModeOff:
		// Should not be reached — MountEmbeddedConsole declines to
		// register routes when mode == off. Guard anyway.
		http.NotFound(w, r)
		return false

	case consoleAuthModeUnsetProd:
		// Production + nothing configured → 503 with the help line.
		// MountEmbeddedConsole logs the "auth-unset" warn at boot;
		// this path catches stragglers (e.g. handlers registered via
		// MountConsoleEndpoints' JSON API surface).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unconfigured","hint":"set SKY_CONSOLE_AUTH=token|app|off (production mode requires explicit choice); see docs/v0.16.x-console/EMBEDDED.md"}`))
		return false

	case consoleAuthModeApp:
		return evaluateAppMode(w, r, st)

	case consoleAuthModeDevOpen:
		// Dev-mode + SKY_CONSOLE_AUTH unset → preserve the v0.15.x
		// behaviour: no auth required, console open on the local
		// listener. EMBEDDED.md L164: "Apps that did NOTHING in
		// v0.15.x: same behaviour."
		//
		// The Bearer-token / admin-secret v0.15.x back-compat path
		// stays here so binaries built with SetProductionMode(true)
		// AND a legacy SKY_METRICS_TOKEN keep working without an
		// explicit SKY_CONSOLE_AUTH choice (existing console_test.go
		// asserts this).
		if isProductionMode() {
			if !hasAdminAuth(r) {
				w.Header().Set("WWW-Authenticate", `Basic realm="sky-console"`)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":"unauthorized","hint":"set SKY_METRICS_TOKEN and pass via Authorization: Bearer <token>"}`))
				return false
			}
		}
		return true

	case consoleAuthModeToken:
		return evaluateTokenMode(w, r, st)
	}
	http.NotFound(w, r)
	return false
}

// evaluateTokenMode — cookie or login form.
func evaluateTokenMode(w http.ResponseWriter, r *http.Request, st *consoleAuthState) bool {
	// Cookie path — most requests after the first
	if c, err := r.Cookie(consoleAuthCookieV2Name); err == nil {
		if _, ok := verifyCookieValue(st.signKey, c.Value); ok {
			return true
		}
	}
	// Login POST path
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_login") {
		handleConsoleLogin(w, r, st)
		return false
	}
	// Anything else → render the login form. 401 on the GET landing
	// page so curl users / scripts get a non-OK status code.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(renderConsoleLoginPage(st.mode)))
	return false
}

// evaluateAppMode — call the app's `consoleAuth` callback.
func evaluateAppMode(w http.ResponseWriter, r *http.Request, st *consoleAuthState) bool {
	// Cookie shortcut — once the callback approved, the post-callback
	// cookie carries identity through the session window.
	if c, err := r.Cookie(consoleAuthCookieV2Name); err == nil {
		if _, ok := verifyCookieValue(st.signKey, c.Value); ok {
			return true
		}
	}
	cb := getConsoleAuthCallback()
	if cb == nil {
		// app-mode requested but no callback wired (e.g. Sky.Http.Server
		// app, or a Sky.Live app that didn't set `consoleAuth`). Fail
		// closed.
		writeConsoleAuthDenied(w, "no consoleAuth callback wired; use SKY_CONSOLE_AUTH=token or set the field on Live.app cfg")
		return false
	}
	// Invoke the Sky callback. It returns a `Task Error (Maybe
	// Identity)` — we force the task here. Panics are caught by
	// runWithRecover; the framework treats panic as deny.
	identity, ok := invokeConsoleAuthCallback(cb, r)
	if !ok {
		writeConsoleAuthDenied(w, "consoleAuth callback returned Nothing")
		recordConsoleAuthEvent(r, "denied", "")
		return false
	}
	// Mint cookie so subsequent requests skip the callback.
	setConsoleV2Cookie(w, st.signKey, identity.Subject)
	recordConsoleAuthEvent(r, "allowed", identity.Subject)
	return true
}

// ConsoleIdentity is the Go-side reflection of Std.Live.Console.Identity.
type ConsoleIdentity struct {
	Subject string
	Email   string
	Claims  map[string]string
}

// invokeConsoleAuthCallback drives the Sky callback. The callback's
// type is `Request -> Task Error (Maybe Identity)`. We pass a
// reflective Request record (matches Sky.Http.Server's existing
// shape) and force the Task. Returns (identity, true) on Just,
// (_, false) on Nothing OR any panic / Err.
func invokeConsoleAuthCallback(cb any, r *http.Request) (ConsoleIdentity, bool) {
	defer func() {
		if rec := recover(); rec != nil {
			// runWithRecover would normally translate, but we keep
			// the contract explicit so the deny path doesn't double-
			// log. Caller logs via recordConsoleAuthEvent.
		}
	}()
	req := buildConsoleAuthRequest(r)
	taskAny := sky_call(cb, req)
	if taskAny == nil {
		return ConsoleIdentity{}, false
	}
	// Force the Task — same shape as Sky.Core.Task.run on the Sky
	// side: Task is `func() any` returning `Result Error a`.
	resultAny := AnyTaskRun(taskAny)
	if resultAny == nil {
		return ConsoleIdentity{}, false
	}
	// `Result Error (Maybe Identity)` — Err short-circuits to deny.
	if consoleIsResultErr(resultAny) {
		return ConsoleIdentity{}, false
	}
	maybeAny := unwrapResultOk(resultAny)
	if maybeAny == nil {
		return ConsoleIdentity{}, false
	}
	// Maybe Identity — Nothing → deny, Just → allow.
	if consoleIsMaybeNothing(maybeAny) {
		return ConsoleIdentity{}, false
	}
	idAny := consoleUnwrapMaybeJust(maybeAny)
	return extractConsoleIdentity(idAny), true
}

// buildConsoleAuthRequest mirrors the rt.go Sky.Http.Server "req"
// dict shape so the callback can introspect path/query/headers/
// cookies the same way a regular handler does.
func buildConsoleAuthRequest(r *http.Request) map[string]any {
	headers := make(map[string]any, len(r.Header))
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = v[0]
		}
	}
	cookies := make(map[string]any)
	for _, c := range r.Cookies() {
		cookies[c.Name] = c.Value
	}
	query := make(map[string]any)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}
	return map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   query,
		"headers": headers,
		"cookies": cookies,
	}
}

// extractConsoleIdentity walks a Sky-side `Identity` record (a Go
// map / struct produced by record-literal codegen) into the Go
// shape consumed by the cookie + audit log.
func extractConsoleIdentity(v any) ConsoleIdentity {
	out := ConsoleIdentity{Claims: map[string]string{}}
	if v == nil {
		return out
	}
	if sub := Field(v, "Subject"); sub != nil {
		out.Subject = fmt.Sprintf("%v", sub)
	}
	if email := Field(v, "Email"); email != nil {
		out.Email = fmt.Sprintf("%v", email)
	}
	// Claims is `Dict String String` — emitted as map[string]any or
	// map[string]string depending on the lowerer.
	if claims := Field(v, "Claims"); claims != nil {
		switch m := claims.(type) {
		case map[string]string:
			for k, val := range m {
				out.Claims[k] = val
			}
		case map[string]any:
			for k, val := range m {
				out.Claims[k] = fmt.Sprintf("%v", val)
			}
		}
	}
	return out
}

// writeConsoleAuthDenied writes a 403 with a plain HTML body.
func writeConsoleAuthDenied(w http.ResponseWriter, hint string) {
	clearConsoleV2Cookie(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	body := fmt.Sprintf(`<!DOCTYPE html><html><head><title>Sky Console — Forbidden</title><meta charset="utf-8">
<style>
  html,body{margin:0;padding:0;height:100%%;background:#0b0b0f;color:#e8e8ec;
    font:14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif}
  main{height:100%%;display:flex;align-items:center;justify-content:center}
  .card{max-width:480px;padding:24px 28px;border:1px solid #2a2a33;
    border-radius:10px;background:#15161c;text-align:center}
  h1{font-size:18px;margin:0 0 12px;font-weight:600}
  p{margin:0 0 8px;color:#a0a0aa}
  .ref{color:#5e5e6b;font-size:12px;margin-top:16px;font-family:ui-monospace,monospace}
</style></head><body><main><div class="card">
  <h1>Sky Console — 403</h1>
  <p>%s</p>
  <p class="ref">403 — consoleAuth denied.</p>
</div></main></body></html>`, htmlEscape(hint))
	_, _ = io.WriteString(w, body)
}

// recordConsoleAuthEvent emits a structured warn / info log so the
// audit trail captures every callback verdict.
func recordConsoleAuthEvent(r *http.Request, verdict, subject string) {
	level := "info"
	if verdict == "denied" {
		level = "warn"
	}
	fields := []any{
		"event", "console.auth." + verdict,
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
	}
	if subject != "" {
		fields = append(fields, "subject", subject)
	}
	logStructured(level, "console.auth", fields...)
}

// logStructured is the thin shim onto the runtime's structured log
// pipeline (telemetry.AppendLog + stdout / stderr drivers). Lives
// here rather than reaching for the Sky-side `Log_*` helpers
// because those return Tasks (deferred) — we want eager emission
// from the request goroutine.
func logStructured(level, msg string, kvs ...any) {
	// Flatten kvs into the map shape `logEmit` accepts.
	ctx := map[string]any{}
	for i := 0; i+1 < len(kvs); i += 2 {
		k := fmt.Sprintf("%v", kvs[i])
		ctx[k] = kvs[i+1]
	}
	lvl := logLevelInfo
	switch level {
	case "warn":
		lvl = logLevelWarn
	case "error":
		lvl = logLevelError
	}
	logEmit(lvl, level, msg, ctx)
}

// ──── Login POST handler ─────────────────────────────────────────

// handleConsoleLogin processes the form POST. Validates the token
// constant-time against SKY_CONSOLE_TOKEN (or the dev auto-token),
// sets the cookie, redirects to /_sky/console.
func handleConsoleLogin(w http.ResponseWriter, r *http.Request, st *consoleAuthState) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	supplied := strings.TrimSpace(r.PostForm.Get("token"))
	expected := strings.TrimSpace(os.Getenv("SKY_CONSOLE_TOKEN"))
	if expected == "" {
		// Dev-token path
		expected = ensureDevConsoleToken()
	}
	if expected == "" || subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) != 1 {
		recordConsoleAuthEvent(r, "denied", "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(renderConsoleLoginPage(st.mode)))
		return
	}
	setConsoleV2Cookie(w, st.signKey, "token-auth")
	recordConsoleAuthEvent(r, "allowed", "token-auth")
	// Optional `redirect` form field for post-login destination;
	// defaults to /_sky/console. Validated to stay under the
	// console path so the form can't be turned into an open
	// redirector.
	dest := r.PostForm.Get("redirect")
	if dest == "" || !strings.HasPrefix(dest, "/_sky/console") {
		dest = "/_sky/console"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// renderConsoleLoginPage emits the token form. POST → /_sky/console/_login.
//
// No JS, no iframe, no third-party fonts. Plain HTML + inline CSS.
// The form has autocomplete="off" + name fields the major password
// managers ignore (so the token doesn't end up saved as a website
// password under your console host).
func renderConsoleLoginPage(mode consoleAuthMode) string {
	hint := ""
	switch mode {
	case consoleAuthModeDevOpen:
		hint = `Dev mode — token auto-generated at <code>.sky/console-token</code>.`
	case consoleAuthModeToken:
		hint = `Production token mode — supply the value of <code>SKY_CONSOLE_TOKEN</code>.`
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <title>Sky Console — Sign in</title>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
      html,body{margin:0;padding:0;height:100%%;background:#0b0b0f;color:#e8e8ec;
        font:14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif}
      main{height:100%%;display:flex;align-items:center;justify-content:center}
      .card{max-width:380px;padding:24px 28px;border:1px solid #2a2a33;
        border-radius:10px;background:#15161c}
      h1{font-size:18px;margin:0 0 12px;font-weight:600;text-align:center}
      .hint{color:#a0a0aa;margin:0 0 16px;font-size:13px;text-align:center}
      .hint code{background:#1f2028;border-radius:3px;padding:1px 5px;
        font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;color:#dde0e8}
      form{display:flex;flex-direction:column;gap:10px}
      label{font-size:12px;color:#9ca0a8;letter-spacing:.3px;text-transform:uppercase}
      input{background:#0e0e15;border:1px solid #2a2a33;border-radius:6px;
        padding:9px 11px;color:#e8e8ec;font:13px ui-monospace,SFMono-Regular,Menlo,monospace}
      input:focus{outline:none;border-color:#3ECF8E}
      button{background:#3ECF8E;border:none;border-radius:6px;padding:10px;
        color:#062018;font-weight:600;font-size:13px;cursor:pointer;margin-top:6px}
      button:hover{background:#4ADE80}
      .ref{color:#5e5e6b;font-size:11px;margin-top:18px;text-align:center;
        font-family:ui-monospace,monospace}
    </style>
</head>
<body>
<main>
    <div class="card">
        <h1>Sky Console</h1>
        <p class="hint">%s</p>
        <form method="POST" action="/_sky/console/_login" autocomplete="off">
            <label for="t">Token</label>
            <input id="t" name="token" type="password" required autofocus
                   spellcheck="false" autocapitalize="off" autocorrect="off"
                   autocomplete="one-time-code" data-1p-ignore data-lpignore="true">
            <button type="submit">Sign in</button>
        </form>
        <p class="ref">Sky Console v0.16.0 — token mode</p>
    </div>
</main>
</body>
</html>`, hint)
}

// htmlEscape is the bare-minimum escaper for the deny page hint.
// Five entities — enough to neutralise an attacker-controlled string
// before injecting into HTML body context.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// ──── One-shot JTI + aud-claim hardening for URL handshake ───────

// consumedJTI tracks jti claims that have been successfully redeemed.
// sync.Map keyed by jti, value = expiry time.Unix(). A janitor
// goroutine prunes entries past expiry every 5 min; absent that
// the worst case is one jti retained for the URL token's full TTL
// (10 min by default), which is cheap.
var consumedJTI sync.Map

// rememberConsumedJTI marks a jti consumed. Returns true if THIS
// call won the race (jti was not previously seen). Replays return
// false.
func rememberConsumedJTI(jti string, expiry int64) bool {
	_, loaded := consumedJTI.LoadOrStore(jti, expiry)
	return !loaded
}

// pruneConsumedJTI walks the map and removes expired entries. Cheap
// — runs every 5 min from a background goroutine started lazily on
// the first URL-handshake success.
func pruneConsumedJTI() {
	now := time.Now().Unix()
	consumedJTI.Range(func(k, v any) bool {
		if expiry, ok := v.(int64); ok && now >= expiry {
			consumedJTI.Delete(k)
		}
		return true
	})
}

var jtiJanitorOnce sync.Once

func startJTIJanitor() {
	jtiJanitorOnce.Do(func() {
		go func() {
			for {
				time.Sleep(5 * time.Minute)
				pruneConsumedJTI()
			}
		}()
	})
}

// consoleEmbedOrigin returns the configured SKY_CONSOLE_EMBED_ORIGIN
// (the URL handshake's opt-in trigger), or "" when disabled.
func consoleEmbedOrigin() string {
	return strings.TrimSpace(os.Getenv("SKY_CONSOLE_EMBED_ORIGIN"))
}

// consoleEmbedAllowed reports whether the URL-handshake mode is
// active for this binary. Disabled by default — the SkyDeploy iframe
// pattern is opt-in to close the cookie/JWT confusion attack
// surface from the security agent's design-debate review.
func consoleEmbedAllowed() bool {
	return consoleEmbedOrigin() != ""
}

// ──── Console mux helpers ────────────────────────────────────────

// mountConsoleAuthRoutes wires the v0.16.0 PR 3 auth surface onto
// the host mux: the login POST handler. The gate itself is invoked
// inside MountEmbeddedConsole's request-time wrapper (see
// console.go), not as a separate route.
func mountConsoleAuthRoutes(mux *http.ServeMux) {
	safeMount(mux, "/_sky/console/_login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := loadConsoleAuthState()
		handleConsoleLogin(w, r, st)
	})
}

// ──── Result/Maybe helpers (Sky → Go) ────────────────────────────

// readAdtTag introspects an ADT-shape value's constructor name.
// Accepts both the generic-typed struct shapes (with Tag field /
// method) and the older map-shaped representation.
func readAdtTag(v any) string {
	if v == nil {
		return ""
	}
	// Try Field-based extraction (works for struct-shaped ADTs that
	// expose a Tag field; rt.Field walks reflect + map types).
	if t := Field(v, "Tag"); t != nil {
		return fmt.Sprintf("%v", t)
	}
	// Map-shaped fallback.
	if m, ok := v.(map[string]any); ok {
		if t, ok := m["Tag"]; ok {
			return fmt.Sprintf("%v", t)
		}
		if t, ok := m["tag"]; ok {
			return fmt.Sprintf("%v", t)
		}
	}
	return ""
}

func readAdtField(v any, idx int) any {
	if v == nil {
		return nil
	}
	// Indexed-field convention from the codegen: _0, _1, _2.
	key := fmt.Sprintf("_%d", idx)
	if f := Field(v, key); f != nil {
		return f
	}
	if m, ok := v.(map[string]any); ok {
		if f, ok := m[key]; ok {
			return f
		}
	}
	return nil
}

// consoleIsResultErr / consoleIsMaybeNothing / consoleUnwrapMaybeJust
// are scoped to the console-auth path to avoid colliding with the
// generic-typed `isResultOk` / `isResultErr` helpers in the pubsub
// test file (those work on SkyResult[any,any], we work on the raw
// any-typed payload the lowerer sets at the row-poly callback's
// return type).

func consoleIsResultErr(v any) bool {
	return readAdtTag(v) == "Err"
}

func consoleUnwrapMaybeJust(v any) any {
	tag := readAdtTag(v)
	if tag != "Just" {
		return nil
	}
	return readAdtField(v, 0)
}

func consoleIsMaybeNothing(v any) bool {
	return readAdtTag(v) == "Nothing"
}

// ──── Test/inspection helpers ────────────────────────────────────

// ConsoleAuthModeDescription is exported for the test suite; returns
// the resolved mode label so tests can assert env wiring without
// poking package internals.
func ConsoleAuthModeDescription() string {
	return describeConsoleAuthMode(loadConsoleAuthState().mode)
}

// ConsoleGate is the cross-package shim that sky-app/rt/console_app
// reaches for to enforce auth around the inline console handler. We
// can't import console_app FROM rt (cycle), so console_app calls
// rt.ConsoleGate before invoking its render path.
//
// Returns true on pass; false when a response (401 / 403 / 503) has
// been written to w. Public API (stable from v0.16.0).
func ConsoleGate(w http.ResponseWriter, r *http.Request) bool {
	return evaluateConsoleAuth(w, r)
}

// stripURLToken removes the `token` query param from u — used by
// the URL handshake redirect to keep the post-redeem URL clean.
func stripURLToken(u *url.URL) string {
	q := u.Query()
	q.Del("token")
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

// _ silences unused-symbol warnings; the items are part of the
// public API surface other files reach for.
var (
	_ = startJTIJanitor
	_ = consoleEmbedAllowed
	_ = mountConsoleAuthRoutes
	_ = stripURLToken
	_ = ResetConsoleAuthStateForTesting
	_ = consoleAuthMode(0)
)
