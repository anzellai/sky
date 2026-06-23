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

var (
	adtVariantRegistry   = make(map[string]AdtVariantFactory)
	adtVariantRegistryMu sync.RWMutex
)

// RegisterAdtVariant binds a SkyName to a factory function that
// constructs the typed variant struct from raw JSON arguments.
// Codegen calls this from init() for every ctor of every ADT whose
// emission shape is sealed-interface + variant struct (v0.17 P3+).
func RegisterAdtVariant(skyName string, factory AdtVariantFactory) {
	adtVariantRegistryMu.Lock()
	adtVariantRegistry[skyName] = factory
	adtVariantRegistryMu.Unlock()
}

// LookupAdtVariant returns the factory for a SkyName or (nil, false).
// Used by the wire-dispatch path to construct typed variants before
// falling back to legacy SkyADT.
func LookupAdtVariant(skyName string) (AdtVariantFactory, bool) {
	adtVariantRegistryMu.RLock()
	f, ok := adtVariantRegistry[skyName]
	adtVariantRegistryMu.RUnlock()
	return f, ok
}

// BuildAdtFromWire is the unified entry point for __sky_send
// dispatch sites. It tries the variant factory first, then the
// legacy SkyADT path, and returns (value, true) on success or
// (nil, false) if the Msg name is unknown to both registries.
//
// localTag is the per-app msgTags fallback (built lazily during
// previous dispatches as the renderer encounters ctor functions);
// pass -1 if unavailable.
func BuildAdtFromWire(msgName string, rawArgs []json.RawMessage, localTag int) (any, bool) {
	// Variant factory (v0.17 sealed-interface) takes priority.
	if factory, found := LookupAdtVariant(msgName); found {
		return factory(rawArgs), true
	}
	// Legacy SkyADT path: global registry first, per-app fallback second.
	tag := -1
	if t, ok := LookupAdtTag(msgName); ok {
		tag = t
	} else if localTag >= 0 {
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
