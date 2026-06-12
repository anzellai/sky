package rt

// Regression test for #532 — msgDisplayName returned "makeFuncStub"
// for reflect.MakeFunc-wrapped closures (the typical shape for
// partial-applied Msg constructors used as form onSubmit handlers).
// The bogus name routed onto the wire as the Msg name, the server's
// LookupAdtTag failed silently, and the form submit dropped with an
// empty patch set.  Returning "" forces the client to fall back to
// the per-binding-site handlerId — the canonical robust path.

import (
	"reflect"
	"testing"
)

func TestMsgDisplayName_ReflectMakeFuncStub(t *testing.T) {
	// reflect.MakeFunc-wrapped closures get "reflect.makeFuncStub"
	// from runtime.FuncForPC. Pre-fix this returned "makeFuncStub";
	// post-fix it returns "" so handlerId fallback kicks in.
	makeFuncWrapped := reflect.MakeFunc(
		reflect.TypeOf((func(any) any)(nil)),
		func(in []reflect.Value) []reflect.Value {
			return []reflect.Value{reflect.ValueOf("ok").Convert(reflect.TypeOf((*any)(nil)).Elem())}
		},
	).Interface()

	got := msgDisplayName(makeFuncWrapped)
	if got != "" {
		t.Errorf("msgDisplayName(reflect.MakeFunc closure) = %q, want \"\" (handlerId fallback)", got)
	}
}

func TestMsgDisplayName_NamedFuncStillResolves(t *testing.T) {
	// A normal Msg-constructor func still resolves via the last-`_`
	// trim, so the #532 fix doesn't regress typed Msg constructor
	// names. Real Sky Msg ctors look like `Msg_UpdateEmail`; the
	// trim chops everything up to and including the last `_`.
	got := msgDisplayName(Msg_UpdateEmailStub532)
	if got != "UpdateEmailStub532" {
		t.Errorf("msgDisplayName(named func) = %q, want %q",
			got, "UpdateEmailStub532")
	}
}

// Named test func deliberately at package scope so runtime.FuncForPC
// produces "<pkg>.Msg_UpdateEmailStub532".  Sky's Msg constructor
// convention is `<Mod>_<Name>` (single underscore separating the
// module qualifier from the constructor identifier).
func Msg_UpdateEmailStub532(_ string) any { return nil }

func TestMsgDisplayName_SkyNameField(t *testing.T) {
	// ADT struct value with SkyName is unchanged — this is the
	// canonical Msg representation, no func involved.
	type Msg struct {
		Tag     int
		SkyName string
	}
	got := msgDisplayName(Msg{Tag: 1, SkyName: "Increment"})
	if got != "Increment" {
		t.Errorf("msgDisplayName(Msg struct) = %q, want %q", got, "Increment")
	}
}

func TestMsgDisplayName_Nil(t *testing.T) {
	if got := msgDisplayName(nil); got != "" {
		t.Errorf("msgDisplayName(nil) = %q, want \"\"", got)
	}
}
