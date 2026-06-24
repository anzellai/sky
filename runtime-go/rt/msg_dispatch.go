package rt

import (
	"sync"
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

// RegisterMsgVariant records the (ADT, ctor) → (tag, arity)
// mapping.  Stage 5 codegen consults this to short-circuit
// reflect.Type.NumIn lookups in 'applyMsgArgs' / 'decodeMsgArg'.
//
// Stage 1: Sky codegen emits the line per ADT × ctor; runtime
// consumers don't read yet.  Population is the observable.
func RegisterMsgVariant(adtName, ctorName string, tag, arity int) {
	key := adtName + ":" + ctorName
	msgVariantInfoMu.Lock()
	msgVariantInfo[key] = MsgVariantInfo{Tag: tag, Arity: arity}
	msgVariantInfoMu.Unlock()
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
