package rt

import (
	"encoding/gob"
	"encoding/json"
	"sync"
)

// v0.17 iter 63 — re-exports of encoding/json + encoding/gob APIs
// that 'emitSealedIfaceUnion's init() block needs to call.  Routing
// through rt.* keeps the emitted main.go's import list minimal
// (only "sky-app/rt"); the underlying encoding/json + encoding/gob
// imports live here in the runtime where they're already in scope.

// JsonRawMessage is the type-alias re-export of encoding/json.RawMessage.
// emitSealedIfaceUnion's per-variant factory uses it as the
// rawArgs parameter type.
type JsonRawMessage = json.RawMessage

// JsonUnmarshal is the function re-export of encoding/json.Unmarshal.
// emitSealedIfaceUnion's per-variant factory uses it to decode each
// raw arg into the destination Go variable.
func JsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// GobRegister is the function re-export of encoding/gob.Register.
// emitSealedIfaceUnion's init() block calls it for every variant
// struct so the gob codec can decode session-store payloads that
// contain those struct types.
func GobRegister(value any) {
	gob.Register(value)
}

// AdtVariantFactory constructs a typed Sky ADT variant value from
// raw JSON arguments. Codegen emits one per Ctor at init() time
// (v0.17 sealed-interface emission, P3+):
//
//	func init() {
//	    rt.RegisterAdtVariant("Increment", func(raw []json.RawMessage) any {
//	        return Main_Msg_Increment_V{}
//	    })
//	    rt.RegisterAdtVariant("UpdateEmail", func(raw []json.RawMessage) any {
//	        var v0 string
//	        if len(raw) >= 1 { _ = json.Unmarshal(raw[0], &v0) }
//	        return Main_Msg_UpdateEmail_V{V0: v0}
//	    })
//	}
//
// Wire-dispatch (live.go's __sky_send single + batched paths)
// consults this registry FIRST: if a factory is registered for the
// requested Msg name, it constructs a typed variant struct that the
// user's update can type-switch on. If no factory is registered
// (pre-v0.17 codegen), falls back to the legacy LookupAdtTag +
// SkyADT{Tag, SkyName, Fields:[]any{}} shape — both code paths stay
// live for cross-version compatibility through the v0.17 transition.
type AdtVariantFactory func(rawArgs []json.RawMessage) any

// AdtCtorKey namespaces a constructor by the ADT that OWNS it.
//
// The bare constructor name is NOT a key. Constructor names are not
// unique across a program: `runtime-go/rt/console_app/main.go` alone
// registers 55 `Std_Ui_*` names (`Text`, `Node`, `Empty`, `Raw`, `Min`,
// `Max`, `Fill`, …), and `AlignLeft` exists in both `Std.Ui.HAlign` and
// `Std.Css.TextAlign` (rust/crates/lower/src/lower.rs:412-416). Keyed on
// the bare name these registries were last-write-wins, so which ADT a
// name resolved to was decided by Go `init()` order — and the wire
// dispatch path feeds them a CLIENT-SUPPLIED string (live.go:4841).
type AdtCtorKey struct {
	Adt  string // owning ADT's Go type name, e.g. "Main_Msg"
	Ctor string // constructor name, e.g. "Submit"
}

var (
	adtVariantRegistry   = make(map[AdtCtorKey]AdtVariantFactory)
	adtVariantRegistryMu sync.RWMutex
)

// RegisterAdtVariant binds an (owning ADT, constructor) pair to a
// factory that constructs the typed variant struct from raw JSON
// arguments. Codegen calls this from init() for every ctor of every ADT
// whose emission shape is sealed-interface + variant struct.
//
// Duplicate registration of the SAME (adt, ctor) is expected and
// tolerated: an ADT declared in the stdlib is emitted into more than one
// Go package (e.g. `Std_Ui_Element` in both `main` and
// `rt/console_app`), so both packages run an init() for it. The two
// factories are generated from one declaration and are behaviourally
// identical, so the first registration is kept and later ones are
// ignored — which makes the result independent of init() order.
// `RegisterAdtTag` enforces that the accompanying tags agree, which is
// the observable that could actually differ.
func RegisterAdtVariant(adtName, ctorName string, factory AdtVariantFactory) {
	key := AdtCtorKey{Adt: adtName, Ctor: ctorName}
	adtVariantRegistryMu.Lock()
	if _, exists := adtVariantRegistry[key]; !exists {
		adtVariantRegistry[key] = factory
	}
	adtVariantRegistryMu.Unlock()
}

// LookupAdtVariant returns the factory for a constructor WITHIN the
// named ADT, or (nil, false). There is deliberately no bare-name
// lookup: resolving a constructor without naming its ADT is the defect
// this registry shape exists to prevent.
func LookupAdtVariant(adtName, ctorName string) (AdtVariantFactory, bool) {
	adtVariantRegistryMu.RLock()
	f, ok := adtVariantRegistry[AdtCtorKey{Adt: adtName, Ctor: ctorName}]
	adtVariantRegistryMu.RUnlock()
	return f, ok
}

// BuildAdtFromWire is the unified entry point for __sky_send
// dispatch sites. It tries the variant factory first, then the
// legacy SkyADT path, and returns (value, true) on success or
// (nil, false) if the Msg name is unknown to both registries.
//
// SECURITY PROPERTY — `msgName` is a client-supplied wire string
// (live.go:4841). It is resolved ONLY within `adtName`, the ADT the
// dispatch site expects. A wire string can therefore never select a
// constructor belonging to an ADT the handler did not ask for, however
// the name collides across the program.
//
// An empty `adtName` means the caller could not determine which ADT it
// expects. Both process-global registries are then skipped entirely —
// searching them without an ADT to scope to IS the defect — and only
// `localTag` remains. That degrades safely rather than failing shut:
// `localTag` comes from the per-app `msgTags` cache, which is populated
// exclusively from messages this app has ALREADY dispatched through a
// render-time handler (live.go:5302), so it cannot name a constructor
// of a foreign ADT.
//
// localTag is that per-app fallback; pass -1 if unavailable.
func BuildAdtFromWire(adtName, msgName string, rawArgs []json.RawMessage, localTag int) (any, bool) {
	tag := -1
	if adtName != "" {
		// Variant factory (v0.17 sealed-interface) takes priority.
		if factory, found := LookupAdtVariant(adtName, msgName); found {
			return factory(rawArgs), true
		}
		// Legacy SkyADT path, scoped to the same ADT.
		if t, ok := LookupAdtTag(adtName, msgName); ok {
			tag = t
		}
	}
	if tag < 0 && localTag >= 0 {
		tag = localTag
	}
	if tag < 0 {
		return nil, false
	}
	var fields []any
	for _, raw := range rawArgs {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			fields = append(fields, v)
		}
	}
	return SkyADT{Tag: tag, SkyName: msgName, Fields: fields}, true
}

// IsFinalisedAdt reports whether msg is already a constructed ADT
// value (either legacy SkyADT or new SkyVariant). The wire-dispatch
// paths use this to decide whether applyMsgArgs (curried-ctor
// application) should run: finalised ADTs skip that step. Without
// the SkyVariant branch, a sealed-iface variant struct returned by
// BuildAdtFromWire would be passed to applyMsgArgs, which would
// try to reflect-call it as a function and panic.
func IsFinalisedAdt(msg any) bool {
	if _, ok := msg.(SkyADT); ok {
		return true
	}
	if _, ok := msg.(SkyVariant); ok {
		return true
	}
	return false
}
