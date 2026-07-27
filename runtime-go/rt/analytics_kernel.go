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
	consent, anonID, userID := currentAnalyticsState().snapshot()
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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err == nil {
		fmt.Fprint(analyticsSink, "[analytics] "+buf.String())
	}
}

// ── session-scoped identity + consent (P2, default-safe) ─────────────────

type analyticsConsent int

const (
	// consentAnonymous is the DEFAULT: capture anonymously (anon id only, no
	// identity, no export). Identity is recorded by `identify` but not attached.
	consentAnonymous analyticsConsent = iota
	// consentGranted: attach identity (user_id + the identify profile).
	consentGranted
	// consentDenied: do not capture at all.
	consentDenied
)

// analyticsSessionState is per-session (Sky.Live) or per-process (CLI). Default
// posture is anonymous: a random anon id, no identity, until the app calls
// `identify` and `setConsent Granted`.
type analyticsSessionState struct {
	mu      sync.Mutex
	consent analyticsConsent
	anonID  string
	userID  string
}

func newAnalyticsState() *analyticsSessionState {
	return &analyticsSessionState{consent: consentAnonymous, anonID: analyticsNewAnonID()}
}

func (s *analyticsSessionState) snapshot() (analyticsConsent, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consent, s.anonID, s.userID
}

func (s *analyticsSessionState) setConsent(c analyticsConsent) {
	s.mu.Lock()
	s.consent = c
	s.mu.Unlock()
}

func (s *analyticsSessionState) setUserID(u string) {
	s.mu.Lock()
	s.userID = u
	s.mu.Unlock()
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
