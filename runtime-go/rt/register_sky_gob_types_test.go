package rt

import (
	"bytes"
	"encoding/gob"
	"testing"
)

// L10a-codegen spec (TDD, red first): RegisterSkyGobTypes takes a whole-binary
// list of Sky-minted zero-values (every record-alias struct + ADT ctor, emitted
// by codegen into main.go) and gob-registers each type + its graph. This closes
// decode-side blindness to `any`-typed Model fields ACROSS processes: gob's
// name→type registry is process-local, so after a restart the new process must
// have independently registered the concrete type that only ever lived in an
// `any` field — the boot-time walk of the INIT value can't (the field is nil at
// init, so its static type is interface{}). The compiler-emitted exhaustive list
// gives every process that registration at boot.

type gobRegInner struct{ Label string } // stands for a record only ever in an any-field

// Establishes the gap: a concrete type in an `any` field that was never
// gob-registered fails to ENCODE ("type not registered for interface"). This is
// the failure a persisted session hits when a previously-nil any-field takes a
// concrete value.
func TestUnregisteredAnyFieldTypeFailsToEncode(t *testing.T) {
	type neverRegistered struct{ V int }
	err := gob.NewEncoder(&bytes.Buffer{}).Encode(struct{ X any }{X: neverRegistered{V: 1}})
	if err == nil {
		t.Skip("this gob version encoded an unregistered any-field type; the gap is version-specific")
	}
}

// After RegisterSkyGobTypes, that same shape round-trips through gob — the fix.
func TestRegisterSkyGobTypesEnablesAnyFieldRoundTrip(t *testing.T) {
	RegisterSkyGobTypes([]any{gobRegInner{}})

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(struct{ X any }{X: gobRegInner{Label: "hi"}}); err != nil {
		t.Fatalf("RegisterSkyGobTypes should register the type for interface encoding: %v", err)
	}
	var got struct{ X any }
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode failed after RegisterSkyGobTypes: %v", err)
	}
	inner, ok := got.X.(gobRegInner)
	if !ok || inner.Label != "hi" {
		t.Fatalf("any-field round-trip wrong: %#v", got.X)
	}
}

// RegisterSkyGobTypes must be safe to call repeatedly (codegen emits it once per
// boot, but tests + hot reload may re-run it) and tolerate a nil/empty list.
func TestRegisterSkyGobTypesIdempotentAndNilSafe(t *testing.T) {
	RegisterSkyGobTypes(nil)
	RegisterSkyGobTypes([]any{})
	RegisterSkyGobTypes([]any{gobRegInner{}})
	RegisterSkyGobTypes([]any{gobRegInner{}}) // repeat — must not panic
}
