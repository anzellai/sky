package rt

import (
	"encoding/json"
	"sync"
)

// Only codecAutoFields registers immutable TYPE SCHEMA names. MarshalJSON
// performs lookups only: arbitrary dynamic object/Dict keys are never inserted.
// Equal names across schemas/modes share one canonical encoding. Retained names
// are a subset of existing field-plan names, and values contain escaped schema
// strings only; there are no object/model/value/tuple-argument references.
var codecAutoSchemaKeys sync.Map // string -> string

func codecRegisterSchemaKey(name string) {
	if _, ok := codecAutoSchemaKeys.Load(name); ok {
		return
	}
	// A builtin string cannot call a user Marshaler. Any future error leaves
	// this name uncached so it is observed at its ORIGINAL Phase-B position.
	encoded, err := json.Marshal(name)
	if err != nil {
		return
	}
	codecAutoSchemaKeys.LoadOrStore(name, string(encoded))
}
