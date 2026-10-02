//go:build !js

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

	"sky-app/rt/periodic"

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

// consoleNow is the console-auth clock. A var so the revocation and
// re-check gates can move time without sleeping.
var consoleNow = time.Now

// consoleAppRecheckInterval bounds how long a console cookie stands in
// for the app's own `App.withConsoleAuth` check under
// SKY_CONSOLE_AUTH=app. The check re-runs at most this often per cookie
// id; when it answers Nothing (the admin signed out, or lost the role)
// the cookie is refused, its id revoked and the browser copy cleared.
// So console access ends within this interval of a sign-out or a
// demotion, never the cookie's full 4-hour life.
const consoleAppRecheckInterval = 60 * time.Second

// consoleCookieRegistryCap bounds each per-id table below. Entries
// expire with their cookie (at most consoleAuthCookieV2MaxAge), so the
// cap is only reached by a flood of sign-ins inside one cookie life.
const consoleCookieRegistryCap = 65536

// consoleCookieRegistry is a bounded id -> unix-time table. It backs
// two things:
//
//   - revoked: ids `_logout` (or a failed app re-check) ended, kept
//     until the cookie would have expired anyway;
//   - checked: when the app's console check last said yes for an id.
//
// It is in-process memory. With several replicas a `_logout` revokes the
// id on the replica that served it; under sticky sessions that is the
// replica the browser talks to. A copy of the cookie replayed against
// ANOTHER replica is still bounded in app mode, because that replica has
// no `checked` entry for the id and re-runs the app's check at once.
type consoleCookieRegistry struct {
	mu sync.Mutex
	m  map[string]int64
}

func (g *consoleCookieRegistry) get(id string) (int64, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[id]
	return v, ok
}

// put stores v for id. expiresAt maps an entry to the unix time its
// cookie expires; expired entries are pruned when the table is full.
func (g *consoleCookieRegistry) put(id string, v int64, expiresAt func(int64) int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = make(map[string]int64)
	}
	if _, exists := g.m[id]; !exists && len(g.m) >= consoleCookieRegistryCap {
		now := consoleNow().Unix()
		for k, e := range g.m {
			if expiresAt(e) <= now {
				delete(g.m, k)
			}
		}
		// Still full: drop the entry whose cookie expires soonest.
		for len(g.m) >= consoleCookieRegistryCap {
			var victim string
			var soonest int64
			for k, e := range g.m {
				if victim == "" || expiresAt(e) < soonest {
					victim, soonest = k, expiresAt(e)
				}
			}
			delete(g.m, victim)
		}
	}
	g.m[id] = v
}

func (g *consoleCookieRegistry) del(id string) {
	g.mu.Lock()
	delete(g.m, id)
	g.mu.Unlock()
}

func (g *consoleCookieRegistry) reset() {
	g.mu.Lock()
	g.m = nil
	g.mu.Unlock()
}

var (
	// revoked: id -> the cookie's own expiry (unix seconds).
	consoleRevokedIDs consoleCookieRegistry
	// checked: id -> when the app's check last admitted it (unix seconds).
	consoleCheckedIDs consoleCookieRegistry
)

func revokedExpiry(exp int64) int64 { return exp }

func checkedExpiry(at int64) int64 { return at + int64(consoleAppRecheckInterval.Seconds()) }

// consoleCookieRevoked reports whether a cookie value names a revoked id.
func consoleCookieRevoked(value string) bool {
	c, ok := parseConsoleCookie(value)
	if !ok {
		return false
	}
	_, revoked := consoleRevokedIDs.get(c.id)
	return revoked
}

// revokeConsoleCookie ends a cookie on the server: every later request
// that carries it is refused, whatever the browser does with its copy.
func revokeConsoleCookie(c consoleCookieClaims) {
	consoleRevokedIDs.put(c.id, c.exp, revokedExpiry)
	consoleCheckedIDs.del(c.id)
	wakeConsoleStreams(c.id)
}

// consoleOpenStreams registers every open console stream by its cookie id,
// so a revocation reaches the streams already open, not only the next
// request. An entry lives exactly as long as its stream (gateSSE).
var consoleOpenStreams = struct {
	mu sync.Mutex
	m  map[string]map[chan struct{}]struct{}
}{}

// watchConsoleCookieRevocation registers the stream that r opened under its
// console cookie id. The returned channel fires when that id is revoked, and
// revoked reports whether it is. A revoked id ends the stream outright: the
// credential it was admitted under is gone, and the client's reload lets the
// gate decide afresh (in app mode the app's check may admit the browser
// again with a new cookie). A request with no console cookie gets a nil
// channel (it never fires) and a revoked that is always false.
func watchConsoleCookieRevocation(r *http.Request) (wake <-chan struct{}, revoked func() bool, release func()) {
	never := func() bool { return false }
	c, err := r.Cookie(consoleAuthCookieV2Name)
	if err != nil {
		return nil, never, func() {}
	}
	claims, ok := parseConsoleCookie(c.Value)
	if !ok {
		return nil, never, func() {}
	}
	ch := make(chan struct{}, 1)
	consoleOpenStreams.mu.Lock()
	if consoleOpenStreams.m == nil {
		consoleOpenStreams.m = make(map[string]map[chan struct{}]struct{})
	}
	set := consoleOpenStreams.m[claims.id]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		consoleOpenStreams.m[claims.id] = set
	}
	set[ch] = struct{}{}
	consoleOpenStreams.mu.Unlock()
	isRevoked := func() bool {
		_, gone := consoleRevokedIDs.get(claims.id)
		return gone
	}
	return ch, isRevoked, func() {
		consoleOpenStreams.mu.Lock()
		if set := consoleOpenStreams.m[claims.id]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(consoleOpenStreams.m, claims.id)
			}
		}
		consoleOpenStreams.mu.Unlock()
	}
}

// wakeConsoleStreams tells every open stream of a revoked cookie id to end.
func wakeConsoleStreams(id string) {
	consoleOpenStreams.mu.Lock()
	defer consoleOpenStreams.mu.Unlock()
	for ch := range consoleOpenStreams.m[id] {
		select {
		case ch <- struct{}{}:
		default: // a wake is already pending
		}
	}
}

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

// consoleAuthModelOf, when set, finds the signed-in app model for a console
// request. The app-mode check then receives it as a second argument
// (`check req model`), so an app whose sign-in lives in its model, not in a
// cookie of its own, can decide from `model.session`. nil keeps the
// one-argument `check req` shape of Live.withConsoleAuth and
// Server.setConsoleAuth.
var consoleAuthModelOf atomic.Value // *func(*http.Request) any

// SetConsoleAuthModel registers how to find a console request's signed-in
// model. Sky.Live registers its session lookup; a Sky.Spa split backend does
// not call this (it composes the model in Sky before registering a
// one-argument check). nil clears it.
func SetConsoleAuthModel(of func(*http.Request) any) {
	if of == nil {
		consoleAuthModelOf.Store((*func(*http.Request) any)(nil))
		return
	}
	consoleAuthModelOf.Store(&of)
}

func getConsoleAuthModelOf() func(*http.Request) any {
	p, _ := consoleAuthModelOf.Load().(*func(*http.Request) any)
	if p == nil {
		return nil
	}
	return *p
}

// Server_setConsoleAuth — `Server.setConsoleAuth check : Task Error ()`.
// Registers the app-mode console callback for a Sky.Http.Server app (and
// the backend of a Sky.Spa split), which has no Live.app config to carry
// it. Sky.Live registers the same callback from its config at boot.
func Server_setConsoleAuth(check any) any {
	return func() any {
		SetConsoleAuthCallback(check)
		return Ok[any, any](struct{}{})
	}
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
		// A desktop window (std_app_desktop.go) never mounts the dev-open
		// console: any local process could read it on the loopback port.
		// An explicit SKY_CONSOLE_AUTH still chooses a mode.
		if desktopWindowActive() {
			return consoleAuthModeOff
		}
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
	consoleRevokedIDs.reset()
	consoleCheckedIDs.reset()
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
	// An operator-set SKY_CONSOLE_TOKEN is the shared secret of the whole
	// deployment. Every process that holds it must accept the cookie any of
	// them issued: two upstream slots behind one proxy, the old and the new
	// process of a rolling redeploy, replicas behind a load balancer. The salt
	// used to be the build commit, or the executable path when the commit was
	// "dev" (which it always was — nothing stamped it). Slots in different
	// directories, or a new commit, then derived different keys, so a console
	// tab that moved to another process was refused (401) on its SSE and event
	// requests — a login loop the page could not explain. A fixed salt binds
	// the key to the secret alone.
	salt := []byte("sky-console-cookie-v1")
	if secret == "" {
		// Dev-mode fallback / app-mode without an explicit token —
		// reach for the auto-generated dev token. Production callers
		// who didn't set SKY_CONSOLE_TOKEN AND aren't in app-mode
		// shouldn't reach this path (resolveConsoleAuthMode declines
		// to unset-prod first), but the safe default is to mint a
		// random key in memory rather than panic.
		secret = ensureDevConsoleToken()
		// The dev token is per project; the executable path keeps two
		// local binaries of the same project from sharing a key.
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

// consoleCookieClaims is what a verified console cookie carries.
type consoleCookieClaims struct {
	id      string // random per issued cookie; the revocation key
	subject string
	exp     int64 // unix seconds
}

// newConsoleCookieID returns 16 random bytes, base64url. A failing
// system RNG is a panic: an id that is not random would let one
// revocation end someone else's cookie, or none at all.
func newConsoleCookieID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("console cookie id: system RNG failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// signCookieValue formats a session cookie body:
// <idB64>.<subjectB64>.<expUnix>.<hmacB64>. The HMAC binds (id,
// subject, exp); a tampered field fails verifyCookieValue at the next
// request. The id is what `_logout` and the app re-check revoke.
func signCookieValue(key []byte, subject string, ttl time.Duration) string {
	return signConsoleCookie(key, consoleCookieClaims{
		id:      newConsoleCookieID(),
		subject: subject,
		exp:     consoleNow().Add(ttl).Unix(),
	})
}

func signConsoleCookie(key []byte, c consoleCookieClaims) string {
	payload := fmt.Sprintf("%s.%s.%d", c.id, base64.RawURLEncoding.EncodeToString([]byte(c.subject)), c.exp)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// parseConsoleCookie splits a cookie value WITHOUT checking its
// signature. Only for looking an id up in the revocation table.
func parseConsoleCookie(value string) (consoleCookieClaims, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 || parts[0] == "" {
		return consoleCookieClaims{}, false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[2], "%d", &exp); err != nil {
		return consoleCookieClaims{}, false
	}
	sub, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return consoleCookieClaims{}, false
	}
	return consoleCookieClaims{id: parts[0], subject: string(sub), exp: exp}, true
}

// verifyConsoleCookie checks signature, expiry and revocation. The
// hmac compare is constant-time. ok=false on any failure.
func verifyConsoleCookie(key []byte, value string) (consoleCookieClaims, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return consoleCookieClaims{}, false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "." + parts[1] + "." + parts[2]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[3])) != 1 {
		return consoleCookieClaims{}, false
	}
	c, ok := parseConsoleCookie(value)
	if !ok {
		return consoleCookieClaims{}, false
	}
	if consoleNow().Unix() >= c.exp {
		return consoleCookieClaims{}, false
	}
	if _, revoked := consoleRevokedIDs.get(c.id); revoked {
		return consoleCookieClaims{}, false
	}
	return c, true
}

// verifyCookieValue is verifyConsoleCookie returning the subject only.
func verifyCookieValue(key []byte, value string) (string, bool) {
	c, ok := verifyConsoleCookie(key, value)
	return c.subject, ok
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
func setConsoleV2Cookie(w http.ResponseWriter, key []byte, subject string) consoleCookieClaims {
	claims := consoleCookieClaims{
		id:      newConsoleCookieID(),
		subject: subject,
		exp:     consoleNow().Add(consoleAuthCookieV2MaxAge).Unix(),
	}
	value := signConsoleCookie(key, claims)
	// `__Host-` mandates Secure (RFC 6265bis §4.1.3.2) — a client
	// rejects the cookie outright without it. The shared predicate
	// returns true for the name prefix, in dev as well as production.
	sameSite := consoleCookieSameSite()
	http.SetCookie(w, &http.Cookie{
		Name:     consoleAuthCookieV2Name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecureFor(nil, consoleAuthCookieV2Name, sameSite),
		SameSite: sameSite,
		MaxAge:   int(consoleAuthCookieV2MaxAge.Seconds()),
	})
	return claims
}

// consoleCookieSameSite returns SameSite=None when SKY_CONSOLE_EMBED_ORIGIN
// is set (the operator opted into iframe embedding from another origin —
// SkyDeploy's dashboard does this), and SameSite=Strict otherwise.
//
// Why: SameSite=Strict cookies are blocked by Chrome / Safari / Firefox in
// cross-origin iframe contexts even when the iframe is same-site by
// eTLD+1 — the browsers treat the iframe document as a "third-party"
// cookie context. The handshake form-POST → 303 → cookie sequence
// succeeds at the server, but the browser refuses to send the cookie back
// when the iframe (re)fetches /_sky/console. Net effect: blank iframe.
//
// SameSite=None (paired with the already-present Secure flag, required by
// the spec) keeps cookies usable in iframe embed. The dashboard's framer
// is pinned via the embed-origin allowlist, so the cookie can't leak to
// an arbitrary third party.
func consoleCookieSameSite() http.SameSite {
	if consoleEmbedAllowed() {
		return http.SameSiteNoneMode
	}
	return http.SameSiteStrictMode
}

// clearConsoleV2Cookie zeros the cookie (logout, denial, mode change).
func clearConsoleV2Cookie(w http.ResponseWriter) {
	sameSite := consoleCookieSameSite()
	http.SetCookie(w, &http.Cookie{
		Name:     consoleAuthCookieV2Name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		// `__Host-` mandates Secure; a clear must match the set's
		// attributes or the client keeps the original cookie.
		Secure:   cookieSecureFor(nil, consoleAuthCookieV2Name, sameSite),
		SameSite: sameSite,
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
	if base := skyGetenv("LIVE_BASE_PATH"); base != "" {
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
//
// A console cookie stands in for the callback for at most
// consoleAppRecheckInterval per cookie id. Past that the callback runs
// again with THIS request: yes keeps the cookie, Nothing refuses with
// 403, revokes the id and clears the cookie. Without the re-check the
// cookie was a 4-hour bearer token that outlived the app sign-out.
func evaluateAppMode(w http.ResponseWriter, r *http.Request, st *consoleAuthState) bool {
	cb := getConsoleAuthCallback()
	if cb == nil {
		// app-mode requested but no callback wired (e.g. Sky.Http.Server
		// app, or a Sky.Live app that didn't set `consoleAuth`). Fail
		// closed.
		writeConsoleAuthDenied(w, "no consoleAuth callback wired; use SKY_CONSOLE_AUTH=token or set the field on Live.app cfg")
		return false
	}
	if c, err := r.Cookie(consoleAuthCookieV2Name); err == nil {
		if claims, ok := verifyConsoleCookie(st.signKey, c.Value); ok {
			now := consoleNow().Unix()
			if at, seen := consoleCheckedIDs.get(claims.id); seen && now-at < int64(consoleAppRecheckInterval.Seconds()) {
				return true
			}
			if _, still := invokeConsoleAuthCallback(cb, r); still {
				consoleCheckedIDs.put(claims.id, now, checkedExpiry)
				return true
			}
			revokeConsoleCookie(claims)
			writeConsoleAuthDenied(w, "consoleAuth callback no longer admits this session; sign in to the app again")
			recordConsoleAuthEvent(r, "revoked", claims.subject)
			return false
		}
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
	// Mint a cookie so the next requests inside the re-check window skip
	// the callback.
	claims := setConsoleV2Cookie(w, st.signKey, identity.Subject)
	consoleCheckedIDs.put(claims.id, consoleNow().Unix(), checkedExpiry)
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
// shape) and force the Task.
//
// FAIL CLOSED. It allows only when every step is positively recognised:
// the task yields a Result whose tag is Ok, the Ok value is a Maybe whose
// tag is Just, and the identity has a non-empty subject. Anything else
// (Err, Nothing, a panic, a shape it cannot read) denies.
//
// This used to compare ADT tags against the strings "Err", "Nothing" and
// "Just". Typed Result and Maybe values carry an INT tag (0 = Ok/Just,
// 1 = Err/Nothing), so none of those comparisons ever matched: Nothing and
// Err both fell through to "allow" with an empty identity, and every
// request to an app-mode console was let in.
func invokeConsoleAuthCallback(cb any, r *http.Request) (id ConsoleIdentity, allowed bool) {
	defer func() {
		if rec := recover(); rec != nil {
			// Caller logs the deny via recordConsoleAuthEvent.
			id, allowed = ConsoleIdentity{}, false
		}
	}()
	req := buildConsoleAuthRequest(r)
	var taskAny any
	if modelOf := getConsoleAuthModelOf(); modelOf != nil {
		taskAny = sky_call2(cb, req, modelOf(r))
	} else {
		taskAny = sky_call(cb, req)
	}
	if taskAny == nil {
		return ConsoleIdentity{}, false
	}
	// Force the Task — same shape as Sky.Core.Task.run on the Sky
	// side: Task is `func() any` returning `Result Error a`.
	resultTag, okValue, _ := anyResultView(AnyTaskRun(taskAny))
	if resultTag != 0 {
		return ConsoleIdentity{}, false // Err, or not a Result at all
	}
	maybeTag, just := anyMaybeView(okValue)
	if maybeTag != 0 {
		return ConsoleIdentity{}, false // Nothing, or not a Maybe at all
	}
	identity := extractConsoleIdentity(just)
	if strings.TrimSpace(identity.Subject) == "" {
		return ConsoleIdentity{}, false
	}
	return identity, true
}

// buildConsoleAuthRequest gives the callback the same SkyRequest a
// Sky.Http.Server handler receives (rt_server.go), so its typed
// `Request` parameter reads cookies, headers, path and query exactly as
// a route handler does. The body is not read: the console gate runs
// before the console's own handlers.
//
// It used to build a map[string]any. The typed emitter converts the
// callback's argument with rt.Coerce[<Request record>], which cannot turn
// that map into the record, so every real callback panicked before it
// ran. Together with the tag bug that made the gate allow everything.
func buildConsoleAuthRequest(r *http.Request) SkyRequest {
	req := SkyRequest{
		Method:     r.Method,
		Path:       r.URL.Path,
		Headers:    make(map[string]any, len(r.Header)),
		Params:     make(map[string]any),
		Query:      make(map[string]any),
		Cookies:    make(map[string]string),
		Form:       make(map[string]string),
		RemoteAddr: r.RemoteAddr,
	}
	for k, v := range r.Header {
		if len(v) > 0 {
			req.Headers[k] = v[0]
		}
	}
	for _, c := range r.Cookies() {
		req.Cookies[c.Name] = c.Value
	}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			req.Query[k] = v[0]
		}
	}
	return req
}

// extractConsoleIdentity walks a Sky-side `Identity` record (a Go
// map / struct produced by record-literal codegen) into the Go
// shape consumed by the cookie + audit log.
func extractConsoleIdentity(v any) ConsoleIdentity {
	out := ConsoleIdentity{Claims: map[string]string{}}
	if v == nil {
		return out
	}
	if sub := fieldOrNil(v, "Subject"); sub != nil {
		out.Subject = fmt.Sprintf("%v", sub)
	}
	if email := fieldOrNil(v, "Email"); email != nil {
		out.Email = fmt.Sprintf("%v", email)
	}
	// Claims is `Dict String String` — emitted as map[string]any or
	// map[string]string depending on the lowerer.
	if claims := fieldOrNil(v, "Claims"); claims != nil {
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

// jtiJanitorInterval is the JTI-prune period. A var so the regression gate can
// drive several cycles in milliseconds — the defect is only visible ACROSS
// cycles, and the shipped second one is five minutes after the first.
var jtiJanitorInterval = 5 * time.Minute

// startJTIJanitor spawns the consumed-JTI pruner.
//
// It was a bare `for { time.Sleep(...); pruneConsumedJTI() }` with no recover.
// `pruneConsumedJTI` walks a sync.Map and deletes from it; a panic in there —
// or in anything it grows to call — ended the janitor for the process
// lifetime, and `consumedJTI` then grew without bound for as long as the
// process ran. Low severity because the map only grows with successful URL
// handshakes, but it is the same shape as the rest of the class and is fixed
// with the same mechanism rather than left as the one that got away.
//
// The loop has no stop channel: it genuinely runs for the process lifetime. A
// nil periodic.Config.Stop is how that is spelled — select on a nil channel
// blocks forever — rather than an unreachable case nobody maintains.
func startJTIJanitor() {
	jtiJanitorOnce.Do(func() {
		go periodic.Every(periodic.Config{
			Name:     "console.jti-janitor",
			Interval: jtiJanitorInterval,
			Report:   periodicReport,
			Work:     func(time.Time) error { pruneConsumedJTI(); return nil },
		})
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
	// Sign out: revoke the cookie's id on the server (so a copy of it is
	// refused too), clear the browser's __Host-sky_console cookie, then bounce
	// to the console landing — which re-renders the login form now that the
	// cookie is gone. Accepts GET (a "Sign out" link) or POST; revoking an
	// auth cookie is idempotent and safe, and a CSRF-triggered logout is at
	// worst a re-login. Only an HMAC-valid cookie is revoked, so the table
	// cannot be filled with made-up ids.
	safeMount(mux, "/_sky/console/_logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(consoleAuthCookieV2Name); err == nil {
			if claims, ok := verifyConsoleCookie(loadConsoleAuthState().signKey, c.Value); ok {
				revokeConsoleCookie(claims)
			}
		}
		clearConsoleV2Cookie(w)
		http.Redirect(w, r, "/_sky/console/", http.StatusSeeOther)
	})
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
