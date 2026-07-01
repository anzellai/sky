package rt

import (
	"sync"
	"sync/atomic"
)

// v0.17 Phase 4, Stage 1 — per-Msg typed dispatch registries.
//
// Background.  The Sky.Live wire dispatch path (and every TEA-shaped
// backend — Sky.Tui, Sky.Cli, Sky.Webview) routes every user event
// through a reflection-driven adapter ('sky_call' / 'sky_call2' /
// 'adaptFuncValue' / 'reflect.MakeFunc').  That adapter is correct
// but pays per-dispatch costs (reflect.ValueOf, reflect.Type.NumIn,
// reflect.MakeFunc, per-arg narrowing) that are already statically
// knowable from the Msg ADT shape.
//
// Stage 1 ships the runtime side of the perMsgTypedDispatch lever
// (`docs/v0.17-roadmap/phase4-per-msg-dispatch.md`):
//
//   * 'RegisterMsgUpdate' — register the typed update dispatch
//     table for an ADT type name (a tag → typed handler map).
//     Stage 1 codegen passes nil; Stage 2 fills with typed arms.
//   * 'RegisterMsgVariant' — record the (ADT, variant) → (tag, arity)
//     mapping so the wire decoder can pick the typed shape without
//     reflect.New + reflect.Type.In(0) walks.
//   * 'RegisterMsgDecoder' — register a typed wire decoder per
//     variant (filled in by Stage 5 codegen — Stage 1 just exposes
//     the registry slot + helper).
//
// All three registries are guarded by a sync.RWMutex (write
// happens once per package load in init(); read happens per
// dispatch on the hot path).  Reads do NOT happen before main()
// (all init() blocks finish before main runs in Go), so there's
// no race window between population and consumption — but the
// mutex guards future "lazy register after main()" cases (e.g.
// plugin-loaded modules from a future 'sky plugin' surface).
//
// Stage 1 contract: NO existing dispatch path consults these
// registries yet.  Population is a no-op observable on the wire
// — the runtime sees the writes but skip-fires on lookup until
// Stage 6 wires the fast-path consult into 'sky_call' /
// 'sky_call2'.  This is intentional: Stage 1 ships ONLY the
// scaffolding + observable codegen change, decoupled from the
// hot-path runtime change.

// ── RegisterMsgUpdate ────────────────────────────────────────

// MsgUpdateDispatch is the value type for the typed update
// dispatch table.  Stage 1 stores 'any' so codegen can pass nil
// (placeholder).  Stage 2+ will tighten this to a typed map type
// once the per-variant typed update arms are emitted.  Storing
// 'any' here avoids a Stage 1 breaking-change when the typed map
// shape settles — the lookup path narrows back to the concrete
// map type at consumption time.
var msgUpdateDispatch = make(map[string]any)
var msgUpdateDispatchMu sync.RWMutex

// RegisterMsgUpdate associates an ADT type name (qualified Go
// name, e.g. "Main_Msg") with its typed update dispatch table.
// Called from generated Go init() blocks per ADT.
//
// Stage 1: table is nil — Stage 1 codegen only proves that the
// init() emission reaches this function and that the registry
// accepts the write.  Production Stage 2+ passes a typed map.
//
// Idempotent: repeated registration overwrites the previous
// table (last-write-wins).  Sky doesn't emit duplicate ADT
// registrations, but the contract is friendly to future
// hot-reload scenarios.
func RegisterMsgUpdate(adtName string, table any) {
	msgUpdateDispatchMu.Lock()
	msgUpdateDispatch[adtName] = table
	msgUpdateDispatchMu.Unlock()
}

// LookupMsgUpdate returns the typed update dispatch table for
// the given ADT name, or (nil, false) when absent.
//
// Stage 6 of Phase 4 calls this from 'sky_call2' to fast-path
// the update dispatch.  Stage 1 exposes the lookup for the unit
// tests + future stages — no production call site consults it
// yet.
func LookupMsgUpdate(adtName string) (any, bool) {
	msgUpdateDispatchMu.RLock()
	table, ok := msgUpdateDispatch[adtName]
	msgUpdateDispatchMu.RUnlock()
	return table, ok
}

// MsgUpdateRegistrySize returns the count of registered ADT
// update tables.  Test-facing helper — production code uses
// 'LookupMsgUpdate' directly.
func MsgUpdateRegistrySize() int {
	msgUpdateDispatchMu.RLock()
	n := len(msgUpdateDispatch)
	msgUpdateDispatchMu.RUnlock()
	return n
}

// ── RegisterMsgVariant ───────────────────────────────────────

// MsgVariantInfo carries the per-variant metadata the wire path
// needs: tag index + payload arity.  Codegen at init() time
// populates this via RegisterMsgVariant per (ADT, ctor) pair.
type MsgVariantInfo struct {
	Tag   int
	Arity int
}

// Keyed by "<adtName>:<ctorName>" — a single registry covers
// every ADT × ctor pairing.  Using a string key avoids a nested
// map shape that would race on inner-map writes during init().
var msgVariantInfo = make(map[string]MsgVariantInfo)
var msgVariantInfoMu sync.RWMutex

// Stage 5 reverse registry: ctorName → adtName.  Populated as a
// side effect of RegisterMsgVariant so the sky_call2 fast-path
// consumer can resolve "given Msg value with SkyName=Foo, which
// ADT does Foo belong to?" in O(1).  Phase 4 Stage 6 uses this
// to find the right LookupMsgUpdate table; Stage 5 ships only
// the population + lookup for fast-path probing.
//
// Last-write-wins, mirroring RegisterMsgVariant.  Ambiguous
// ctor names across ADTs (the same ctor name on two different
// ADTs) are forbidden by Sky's namespacing — every ctor name
// is globally unique in the emitted Go program — so collisions
// here would indicate a codegen bug rather than user code.
var msgCtorToAdt = make(map[string]string)
var msgCtorToAdtMu sync.RWMutex

// RegisterMsgVariant records the (ADT, ctor) → (tag, arity)
// mapping.  Stage 5 codegen consults this to short-circuit
// reflect.Type.NumIn lookups in 'applyMsgArgs' / 'decodeMsgArg'.
//
// Stage 1: Sky codegen emits the line per ADT × ctor; runtime
// consumers don't read yet.  Population is the observable.
//
// Stage 5: additionally populates the reverse ctor → adt map
// (msgCtorToAdt) so the sky_call2 fast-path consumer can
// resolve the ADT name from a Msg value's SkyName.
func RegisterMsgVariant(adtName, ctorName string, tag, arity int) {
	key := adtName + ":" + ctorName
	msgVariantInfoMu.Lock()
	msgVariantInfo[key] = MsgVariantInfo{Tag: tag, Arity: arity}
	msgVariantInfoMu.Unlock()

	msgCtorToAdtMu.Lock()
	msgCtorToAdt[ctorName] = adtName
	msgCtorToAdtMu.Unlock()
}

// LookupAdtByCtor returns the ADT name for a given constructor
// name (e.g. "Inc" → "Main_Msg").  Stage 5 sky_call2 consumer
// uses this to derive the registry key for LookupMsgUpdate from
// a SkyADT's SkyName field.
//
// Returns ("", false) when the ctor name isn't registered.
func LookupAdtByCtor(ctorName string) (string, bool) {
	msgCtorToAdtMu.RLock()
	adt, ok := msgCtorToAdt[ctorName]
	msgCtorToAdtMu.RUnlock()
	return adt, ok
}

// LookupMsgVariant returns the (tag, arity) for a registered
// (ADT, ctor) pair, or (zero, false) when absent.
func LookupMsgVariant(adtName, ctorName string) (MsgVariantInfo, bool) {
	key := adtName + ":" + ctorName
	msgVariantInfoMu.RLock()
	info, ok := msgVariantInfo[key]
	msgVariantInfoMu.RUnlock()
	return info, ok
}

// MsgVariantRegistrySize returns the count of registered
// (ADT, ctor) entries.  Test-facing helper.
func MsgVariantRegistrySize() int {
	msgVariantInfoMu.RLock()
	n := len(msgVariantInfo)
	msgVariantInfoMu.RUnlock()
	return n
}

// ── RegisterMsgDecoder ───────────────────────────────────────

// MsgDecoder is the type of a per-ctor wire decoder.  Stage 5
// codegen emits one per Msg variant with non-nil ctor parameters:
//
//	func Main_Msg_decode_DoSignIn(raw []JsonRawMessage) (any, error) {
//	    var v0 Main_AuthCreds_R
//	    if err := JsonUnmarshal(raw[0], &v0); err != nil { return nil, err }
//	    return Main_Msg_DoSignIn(v0), nil
//	}
//
// The wire path (Stage 6) consults LookupMsgDecoder before falling
// back to the reflect.New + reflect.Type.In(0) decode path in
// 'applyMsgArgs'.
type MsgDecoder func(raw []JsonRawMessage) (any, error)

var msgDecoders = make(map[string]MsgDecoder)
var msgDecodersMu sync.RWMutex

// RegisterMsgDecoder associates a constructor name with its
// typed wire decoder.  Keyed by bare ctor name (the same key
// 'msgDisplayName' / 'LookupAdtTag' use) so wire-side dispatch
// can look up via the in-band ctor name without first resolving
// the ADT.
//
// Stage 1: registry is exposed but no codegen emits decoders
// yet (Stage 5).  Tests verify the register / lookup surface
// independently of codegen.
func RegisterMsgDecoder(ctorName string, dec MsgDecoder) {
	msgDecodersMu.Lock()
	msgDecoders[ctorName] = dec
	msgDecodersMu.Unlock()
}

// LookupMsgDecoder returns the wire decoder for a constructor
// name, or (nil, false) when absent.  Production consumer
// (Stage 6) falls back to the reflect path on miss.
func LookupMsgDecoder(ctorName string) (MsgDecoder, bool) {
	msgDecodersMu.RLock()
	dec, ok := msgDecoders[ctorName]
	msgDecodersMu.RUnlock()
	return dec, ok
}

// MsgDecoderRegistrySize returns the count of registered
// decoders.  Test-facing helper.
func MsgDecoderRegistrySize() int {
	msgDecodersMu.RLock()
	n := len(msgDecoders)
	msgDecodersMu.RUnlock()
	return n
}

// ── Stage 5: sky_call2 fast-path consumer infrastructure ─────
//
// Phase 4 Stage 5 wires the LookupMsgUpdate consumer into
// 'sky_call2' (live.go).  The consumer's job is to fast-path
// the update dispatch when the Msg ADT has a registered typed
// dispatch table — short-circuiting reflect.MakeFunc and
// per-arg coerceReflectArg costs.
//
// Stage 5 ships only the *infrastructure*: the helper that
// classifies the call site as fast-path-eligible and looks up
// the registered table.  The actual table consumption (typed
// arm invocation, payload extraction from SkyADT.Fields, return
// shape narrowing) lands in Stage 6 alongside the rt.Coerce
// drop.  Stage 5 always returns 'ok=false', so sky_call2 falls
// through to the existing reflect path unchanged — no observable
// behavior change at this stage.
//
// The two-stage split keeps the diff diagnosable: if Stage 6
// regresses an example, the bisect points at the typed-arm
// consumption, not at the eligibility classifier.

// fastPathProbeCounter records how many sky_call2 calls were
// fast-path-eligible (table existed) vs how many fell through
// (no table registered for the Msg ADT, or 'a' wasn't a SkyADT).
// Test-facing only; production code does not consult these.
// Atomics avoid an extra lock on the hot path.
var fastPathEligibleCount uint64
var fastPathFallthroughCount uint64

// tryFastPathMsgUpdate is the Stage 5 consumer infrastructure
// invoked at 'sky_call2' entry.  It classifies the call site
// and looks up the typed dispatch table:
//
//   * If 'a' is a SkyADT and its SkyName resolves via
//     LookupAdtByCtor → LookupMsgUpdate to a non-nil typed
//     table, increment fastPathEligibleCount and return
//     (table, true).
//   * Otherwise increment fastPathFallthroughCount and return
//     (nil, false).
//
// Stage 5 callers (sky_call2) IGNORE the returned table and
// always fall through to the reflect path — the lookup is pure
// observation for telemetry / probe purposes.  Stage 6 changes
// sky_call2 to consume the table when ok==true.
//
// Cost on the hot path: one type assertion ('a.(SkyADT)') +
// one RLock'd map read + one RLock'd map read.  The fast-path
// classification is intentionally cheap so a Stage 6 'ok' path
// can pay for itself.
func tryFastPathMsgUpdate(a any) (any, bool) {
	adt, isSkyADT := a.(SkyADT)
	if !isSkyADT {
		atomic.AddUint64(&fastPathFallthroughCount, 1)
		return nil, false
	}
	adtName, ok := LookupAdtByCtor(adt.SkyName)
	if !ok {
		atomic.AddUint64(&fastPathFallthroughCount, 1)
		return nil, false
	}
	table, ok := LookupMsgUpdate(adtName)
	if !ok || table == nil {
		atomic.AddUint64(&fastPathFallthroughCount, 1)
		return nil, false
	}
	atomic.AddUint64(&fastPathEligibleCount, 1)
	return table, true
}

// FastPathProbeStats returns the (eligible, fallthrough) counts
// observed by tryFastPathMsgUpdate since process start.
// Test-facing only.
func FastPathProbeStats() (eligible, fallthrough_ uint64) {
	return atomic.LoadUint64(&fastPathEligibleCount),
		atomic.LoadUint64(&fastPathFallthroughCount)
}

// FastPathProbeReset zeros the probe counters.  Test-facing only.
func FastPathProbeReset() {
	atomic.StoreUint64(&fastPathEligibleCount, 0)
	atomic.StoreUint64(&fastPathFallthroughCount, 0)
}
