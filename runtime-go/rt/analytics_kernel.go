// Package rt — Std.Analytics runtime kernels.
//
// P1+ (capture core): render a tracked event to a structured `[analytics]`
// line on stderr — the default local/debug sink — with PII redacted. Later
// phases route events through the consent gate, context enrichment, and the
// pluggable sinks (console SQLite store + external providers). See
// docs/rfcs/std-analytics.md.
package rt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
)

// analyticsSink is where the P1 local/debug sink writes its `[analytics]`
// lines. Defaults to stderr; a package-level var so tests can capture output
// (and the seam the real pluggable sinks will replace in a later phase).
var analyticsSink io.Writer = os.Stderr

// analyticsEmit writes one event (name + already-rendered props) to the
// default local/debug sink: a structured `[analytics]` JSON line on stderr.
// Both `track` (typed prop builders) and `trackEvent` (reflective derive)
// funnel through here, so downstream everything is uniform. HTML escaping is
// off — these are human-read lines and the `<pii:…>` marker must read cleanly.
//
// Identity + consent are applied HERE, uniformly (default-safe, RFC §11):
//   - every event carries the session's random `anonymous_id`;
//   - a `user_id` is attached ONLY when consent is Granted and identify ran;
//   - a Denied consent DROPS the event entirely (no capture).
func analyticsEmit(name string, props map[string]any) {
	st := currentAnalyticsState()
	consent, anonID, userID := st.snapshot()
	if consent == consentDenied {
		return // no capture without consent
	}
	if props == nil {
		props = map[string]any{}
	}
	payload := map[string]any{
		"event":        name,
		"props":        props,
		"anonymous_id": anonID,
		"ts":           time.Now().UnixMilli(),
	}
	if consent == consentGranted && userID != "" {
		payload["user_id"] = userID
	}
	if ctx := st.contextSnapshot(); ctx != nil {
		payload["context"] = ctx
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return
	}
	analyticsStoreInsert(payload) // persist (console DB / override) — no-op if unconfigured
	analyticsFanOut(buf.String()) // fan the JSON line out to configured sinks
}

// ── pluggable sinks (P3) ─────────────────────────────────────────────────
//
// Destinations are process-global (app-wide), UNLIKE identity/consent which
// are per-session. `Sink = StderrSink | FileSink path | Custom fn` — the
// `Custom (String -> Task Error ())` variant hands the JSON line to a Sky
// function, so ANY destination (a provider HTTP POST, a queue) lives in
// userland without the stdlib hardcoding vendors. Default is [StderrSink].
// The already-rendered line has PII redacted, so every sink is safe today;
// per-sink PII clearance (raw PII to a cleared sink) is a later refinement.

type analyticsSinkT struct {
	kind string // "stderr" | "file" | "custom"
	path string // file path (kind == file)
	fn   any    // Sky `String -> Task Error ()` (kind == custom)
}

var (
	analyticsSinksMu sync.RWMutex
	analyticsSinks   = []analyticsSinkT{{kind: "stderr"}}
	analyticsFileMu  sync.Mutex // serialise concurrent file appends
)

// analyticsFanOut writes the rendered JSON line to every configured sink.
func analyticsFanOut(line string) {
	analyticsSinksMu.RLock()
	sinks := analyticsSinks
	analyticsSinksMu.RUnlock()
	for _, s := range sinks {
		switch s.kind {
		case "stderr":
			fmt.Fprint(analyticsSink, "[analytics] "+line)
		case "file":
			analyticsWriteFile(s.path, line)
		case "custom":
			// Synchronous + panic-shielded: guarantees delivery (a fire-and-
			// forget goroutine would be lost when a CLI one-shot exits). Keep the
			// handler fast, or drive heavy sends via Cmd.perform / a queue — a
			// batching async spool is a documented later refinement.
			func(fn any, payload string) {
				defer func() { _ = recover() }()
				anyTaskInvoke(SkyCall(fn, payload))
			}(s.fn, strings.TrimRight(line, "\n"))
		}
	}
}

func analyticsWriteFile(path, line string) {
	analyticsFileMu.Lock()
	defer analyticsFileMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line) // line already carries a trailing newline (JSONL)
}

// Analytics_configure implements:
//
//	Std.Analytics.configure : List Sink -> Task Error ()
//
// Sets the process-global sink list. Called once at startup, before track
// calls; an empty / unparseable list falls back to [StderrSink].
func Analytics_configure(sinksArg any) any {
	return func() any {
		var out []analyticsSinkT
		rv := reflect.ValueOf(derefPointer(unwrapAny(sinksArg)))
		if rv.IsValid() && rv.Kind() == reflect.Slice {
			for i := 0; i < rv.Len(); i++ {
				name, _, fields, ok := unwrapADTShape(unwrapAny(rv.Index(i).Interface()))
				if !ok {
					continue
				}
				switch name {
				case "StderrSink":
					out = append(out, analyticsSinkT{kind: "stderr"})
				case "FileSink":
					if len(fields) == 1 {
						out = append(out, analyticsSinkT{kind: "file", path: fmt.Sprintf("%v", fields[0])})
					}
				case "Custom":
					if len(fields) == 1 {
						out = append(out, analyticsSinkT{kind: "custom", fn: fields[0]})
					}
				}
			}
		}
		if len(out) == 0 {
			out = []analyticsSinkT{{kind: "stderr"}}
		}
		analyticsSinksMu.Lock()
		analyticsSinks = out
		analyticsSinksMu.Unlock()
		return Ok[any, any](struct{}{})
	}
}

// analyticsPageViewsFromCfg reads the opt-in `analytics = { pageViews = True }`
// field off a Live.app cfg record. Absent → false (no auto-tracking).
func analyticsPageViewsFromCfg(cfg any) bool {
	a := Field(cfg, "Analytics")
	if a == nil {
		return false
	}
	b, _ := Field(a, "PageViews").(bool)
	return b
}

// analyticsIdentifyFromCfg reads the optional `model -> Maybe String` resolver
// set by `Live.withAnalyticsIdentify`. Absent → nil.
func analyticsIdentifyFromCfg(cfg any) any {
	return Field(cfg, "AnalyticsIdentify")
}

// analyticsApplyIdentity calls the app's `identify` resolver with the current
// model and, when it returns `Just id`, stamps the session's analytics user id —
// so an already-authenticated session's auto page-views (including the very first
// render, before any Msg runs) carry the user without a manual `identify` call.
// The config field IS the app's explicit opt-in for attributing the identity it
// already holds.
func analyticsApplyIdentity(resolver, model any) {
	if resolver == nil {
		return
	}
	tag, payload := anyMaybeView(SkyCall(resolver, model))
	if tag != 0 { // Nothing / not a Maybe → leave anonymous
		return
	}
	if uid := fmt.Sprintf("%v", unwrapAny(payload)); uid != "" {
		currentAnalyticsState().setUserID(uid)
	}
}

// analyticsSetContext records device + anonymised-IP context on the current
// session, attached to every subsequent event. IP is truncated BEFORE storage
// (GDPR-style: a full IP is personal data) — the raw address never lands in an
// event. Called from handleInitial (which has the request) under the analytics
// opt-in, with the session stamped.
func analyticsSetContext(userAgent, remoteAddr string) {
	ctx := map[string]any{}
	if userAgent != "" {
		ctx["user_agent"] = userAgent
	}
	if ip := analyticsAnonIP(remoteAddr); ip != "" {
		ctx["ip"] = ip
	}
	if len(ctx) > 0 {
		currentAnalyticsState().setContext(ctx)
	}
}

// analyticsAnonIP truncates a client IP to its network prefix: the last octet
// of an IPv4 (1.2.3.4 -> 1.2.3.0), the last 80 bits of an IPv6 (keep the /48) —
// the standard "IP anonymisation" GA/Matomo apply. Returns "" for an
// unparseable address, so nothing personal leaks by accident.
func analyticsAnonIP(remoteAddr string) string {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		v4[3] = 0
		return v4.String()
	}
	if v6 := ip.To16(); v6 != nil {
		for i := 6; i < 16; i++ {
			v6[i] = 0
		}
		return v6.String()
	}
	return ""
}

// analyticsTrackPageView emits a consent-gated `page_view` for the Sky.Live
// auto-capture path. Called from handleInitial with the session already stamped.
func analyticsTrackPageView(path, referrer string) {
	props := map[string]any{"path": path}
	if referrer != "" {
		props["referrer"] = referrer
	}
	analyticsEmit("page_view", props)
}

// ── session-scoped identity + consent (P2, default-safe) ─────────────────

type analyticsConsent int

const (
	// consentAnonymous: capture anonymously (anon id only, no identity, no
	// export). Identity is recorded by `identify` but not attached. Opt IN to
	// this posture with `setConsent Anonymous` when you show a consent banner.
	consentAnonymous analyticsConsent = iota
	// consentGranted is the DEFAULT (v0.19.1): attach identity (user_id + the
	// identify profile) + export. The DX-friendly default — an app that turns
	// analytics on gets full capture; privacy-conscious apps show a consent
	// banner and downgrade to `Anonymous`/`Denied` until the user opts in.
	consentGranted
	// consentDenied: do not capture at all.
	consentDenied
)

// analyticsSessionState is per-session (Sky.Live) or per-process (CLI). Default
// posture is `Granted` — an app that enables analytics captures fully; call
// `setConsent Anonymous`/`Denied` (e.g. behind a consent banner) to restrict.
type analyticsSessionState struct {
	mu       sync.Mutex
	consent  analyticsConsent
	explicit bool // true once the app calls setConsent — see restoreAnalyticsState
	anonID   string
	userID   string
	context  map[string]any // device / anonymised IP, attached to every event
}

func newAnalyticsState() *analyticsSessionState {
	return &analyticsSessionState{consent: consentGranted, anonID: analyticsNewAnonID()}
}

// restoreAnalyticsState rebuilds a session's analytics state from persisted
// values (consent posture + anon/user id), so a DB-backed session store keeps an
// identified user across a restart / replica reshuffle — matching the auth
// `identity` round-trip. A blank anonID (pre-v0.19 blob) mints a fresh one.
//
// Consent is only honoured from the store when it was set EXPLICITLY (the app
// called `setConsent`, e.g. behind a consent banner). A session that merely rode
// the framework default follows the CURRENT default (Granted) on restore, so a
// default change reaches already-persisted sessions and a stale default is never
// mistaken for a user's choice. Pre-`explicit`-flag blobs decode with
// explicit=false → they pick up the current default too (the intended migration
// for sessions persisted under the old Anonymous default).
func restoreAnalyticsState(consent int, explicit bool, anonID, userID string) *analyticsSessionState {
	if anonID == "" {
		anonID = analyticsNewAnonID()
	}
	c := consentGranted
	if explicit {
		c = analyticsConsent(consent)
	}
	return &analyticsSessionState{
		consent:  c,
		explicit: explicit,
		anonID:   anonID,
		userID:   userID,
	}
}

func (s *analyticsSessionState) snapshot() (analyticsConsent, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consent, s.anonID, s.userID
}

func (s *analyticsSessionState) setConsent(c analyticsConsent) {
	s.mu.Lock()
	s.consent = c
	s.explicit = true // an app-set posture is honoured verbatim on restore
	s.mu.Unlock()
}

func (s *analyticsSessionState) consentExplicit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.explicit
}

func (s *analyticsSessionState) setUserID(u string) {
	s.mu.Lock()
	s.userID = u
	s.mu.Unlock()
}

func (s *analyticsSessionState) setContext(c map[string]any) {
	s.mu.Lock()
	s.context = c
	s.mu.Unlock()
}

func (s *analyticsSessionState) contextSnapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.context) == 0 {
		return nil
	}
	cp := make(map[string]any, len(s.context))
	for k, v := range s.context {
		cp[k] = v
	}
	return cp
}

var (
	analyticsInitMu    sync.Mutex // guards lazy-init of per-session + process state
	analyticsProcState *analyticsSessionState
)

// currentAnalyticsState returns the CURRENT session's analytics state — for a
// Sky.Live app the goroutine-local session stamp keys it PER SESSION, so one
// user's identity never bleeds into another's events; for a CLI / non-Live
// Task (no live session in scope) it returns the single process-global state.
func currentAnalyticsState() *analyticsSessionState {
	if sess := currentLiveSession(); sess != nil {
		analyticsInitMu.Lock()
		defer analyticsInitMu.Unlock()
		if sess.analytics == nil {
			sess.analytics = newAnalyticsState()
		}
		return sess.analytics
	}
	analyticsInitMu.Lock()
	defer analyticsInitMu.Unlock()
	if analyticsProcState == nil {
		analyticsProcState = newAnalyticsState()
	}
	return analyticsProcState
}

// analyticsNewAnonID mints a random anonymous id — deliberately SEPARATE from
// the auth session token, so the session cookie never lands in analytics.
func analyticsNewAnonID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "anon"
	}
	return "anon_" + hex.EncodeToString(b[:])
}

// Analytics_identify implements:
//
//	Std.Analytics.identify : String -> List Prop -> Task Error ()
//
// Records the user id for subsequent events (attached only under Granted
// consent) and, WHEN consent is Granted, emits an `identify` profile event with
// the traits. Identity is NEVER attached automatically — this explicit call is
// the only way user id enters analytics.
func Analytics_identify(userIDArg, traitsArg any) any {
	return func() any {
		uid := fmt.Sprintf("%v", unwrapAny(userIDArg))
		st := currentAnalyticsState()
		st.setUserID(uid)
		if consent, _, _ := st.snapshot(); consent == consentGranted {
			analyticsEmit("identify", analyticsReadProps(traitsArg))
		}
		return Ok[any, any](struct{}{})
	}
}

// Analytics_setConsent implements:
//
//	Std.Analytics.setConsent : Consent -> Task Error ()
//
// Sets the session's consent posture. `Anonymous` (default) captures
// anonymously; `Granted` attaches identity; `Denied` drops all capture.
func Analytics_setConsent(consentArg any) any {
	return func() any {
		// `Consent = Anonymous | Granted | Denied` is an all-nullary enum, so the
		// Can.Enum optimisation lowers it to a bare int TAG (Anonymous=0,
		// Granted=1, Denied=2 in declaration order), NOT a named SkyADT. EnumTagIs
		// reads either representation.
		st := currentAnalyticsState()
		switch {
		case EnumTagIs(consentArg, 1):
			st.setConsent(consentGranted)
		case EnumTagIs(consentArg, 2):
			st.setConsent(consentDenied)
		default:
			st.setConsent(consentAnonymous)
		}
		return Ok[any, any](struct{}{})
	}
}

// Analytics_track implements:
//
//	Std.Analytics.track : Event -> Task Error ()
//
// The event is `{ name : String, props : List Prop }`, each `Prop` a
// `{ key : String, value : PropValue }`. PII props are redacted to their kind.
func Analytics_track(eventArg any) any {
	return func() any {
		name := fmt.Sprintf("%v", recordField(eventArg, "Name", "name"))
		analyticsEmit(name, analyticsReadProps(recordField(eventArg, "Props", "props")))
		return Ok[any, any](struct{}{})
	}
}

// Analytics_trackEvent implements:
//
//	Std.Analytics.trackEvent : a -> Task Error ()
//
// The payload is DERIVED reflectively from a value of the app's own typed
// event union: the constructor name becomes a snake_case event name
// (`ProductViewed` → `"product_viewed"`), and the variant's record payload's
// fields become typed props. Money is rendered lossless, Pii is redacted by
// type, and a plain `String` field is NEVER treated as PII (only a `Pii`-typed
// value redacts). No encoder needed for the common case; reach for
// `track`+`event` to shape the payload by hand.
func Analytics_trackEvent(ev any) any {
	return func() any {
		name, _, fields, ok := unwrapADTShape(unwrapAny(ev))
		if !ok {
			// Not an ADT value (a bare record / scalar) — derive from it directly.
			analyticsEmit("event", analyticsDeriveProps(ev))
			return Ok[any, any](struct{}{})
		}
		props := map[string]any{}
		switch len(fields) {
		case 0:
			// Nullary variant (e.g. `AppOpened`) — a name with no props.
		case 1:
			// The common shape: a single record payload — ProductViewed {…}.
			props = analyticsDeriveProps(fields[0])
		default:
			// Positional payloads → arg0, arg1, …
			for i, f := range fields {
				props[fmt.Sprintf("arg%d", i)] = analyticsRenderGoValue(f)
			}
		}
		analyticsEmit(analyticsSnakeCase(name), props)
		return Ok[any, any](struct{}{})
	}
}

// ── typed-prop path (`track`) ────────────────────────────────────────────

// analyticsReadProps turns the Sky `List Prop` into a JSON-ready map,
// rendering each typed PropValue and redacting PII.
func analyticsReadProps(v any) map[string]any {
	out := map[string]any{}
	rv := reflect.ValueOf(derefPointer(unwrapAny(v)))
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return out
	}
	for i := 0; i < rv.Len(); i++ {
		p := rv.Index(i).Interface()
		key := fmt.Sprintf("%v", recordField(p, "Key", "key"))
		out[key] = analyticsRenderValue(recordField(p, "Value", "value"))
	}
	return out
}

// analyticsRenderValue renders one PropValue (a Sky ADT) to a JSON value.
// PII is redacted to its kind, never its value.
func analyticsRenderValue(v any) any {
	tag, fields := reflectExtractCtor(v)
	if len(fields) == 0 {
		return nil
	}
	switch tag {
	case "VString":
		return fmt.Sprintf("%v", fields[0])
	case "VInt":
		return AsInt(fields[0])
	case "VFloat":
		return AsFloat(fields[0])
	case "VBool":
		return AsBool(fields[0])
	case "VMoney":
		// Lossless "ISO_CODE AMOUNT" (e.g. "USD 19.99") — same rendering
		// Std.Db uses for SqlMoney, so revenue round-trips exactly.
		return sqlMoneyToString(fields[0])
	case "VPii":
		pTag, _ := reflectExtractCtor(fields[0])
		return "<pii:" + analyticsPiiKind(pTag) + ">"
	default:
		return fmt.Sprintf("%v", fields[0])
	}
}

func analyticsPiiKind(tag string) string {
	switch tag {
	case "PiiEmail":
		return "email"
	case "PiiRaw":
		return "raw"
	default:
		return "pii"
	}
}

// ── reflective-derive path (`trackEvent`) ────────────────────────────────

// analyticsDeriveProps reflects a variant's payload into props. A record
// (struct with domain fields, or a map) becomes one prop per field; anything
// else (a scalar / an ADT like Money) becomes a single "value" prop.
func analyticsDeriveProps(v any) map[string]any {
	out := map[string]any{}
	u := derefPointer(unwrapAny(v))
	// An ADT payload (Money, Pii, an enum) is a value, not a record.
	if _, _, _, isADT := unwrapADTShape(u); isADT {
		out["value"] = analyticsRenderGoValue(u)
		return out
	}
	rv := reflect.ValueOf(u)
	switch rv.Kind() {
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if k.Kind() == reflect.String {
				out[analyticsSnakeCase(k.String())] = analyticsRenderGoValue(rv.MapIndex(k).Interface())
			}
		}
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported
				continue
			}
			out[analyticsSnakeCase(f.Name)] = analyticsRenderGoValue(rv.Field(i).Interface())
		}
	default:
		out["value"] = analyticsRenderGoValue(u)
	}
	return out
}

// analyticsRenderGoValue renders an arbitrary Sky value to a JSON value by its
// runtime type: Money lossless, Pii redacted, scalars as-is, other ADTs by
// their constructor name, everything else via %v.
func analyticsRenderGoValue(v any) any {
	u := derefPointer(unwrapAny(v))
	if name, _, fields, isADT := unwrapADTShape(u); isADT {
		switch name {
		case "Money":
			return sqlMoneyToString(u)
		case "PiiEmail", "PiiRaw":
			return "<pii:" + analyticsPiiKind(name) + ">"
		default:
			// A nullary enum (a status/kind) → its constructor name; a
			// single-payload wrapper → the payload; else the name.
			if len(fields) == 1 {
				return analyticsRenderGoValue(fields[0])
			}
			return name
		}
	}
	rv := reflect.ValueOf(u)
	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	default:
		return fmt.Sprintf("%v", u)
	}
}

// analyticsSnakeCase converts a Sky constructor / field name to snake_case:
// `ProductViewed` → `product_viewed`, `OrderId` → `order_id`, `id` → `id`.
func analyticsSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
