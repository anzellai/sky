package rt

// Regression tests for the Msg decode error handling. Before this
// layer, a type-mismatched wire arg (e.g. a radio's onInput sending
// a bool into a String -> Msg constructor) would panic deep in
// reflect.Call. The outer panic-recover around /_sky/event caught
// the crash but gave no useful diagnostic and silently dropped the
// event. Now applyMsgArgs inspects the first parameter type up front
// and returns a msgDecodeError sentinel when the types don't match,
// with a targeted log line. dispatch() recognises the sentinel and
// returns "" (no model mutation, no re-render).

import (
	"encoding/json"
	"testing"
	"time"
)

// A Msg constructor with a String parameter — the common
// UpdateAuthRole-style shape from the sendcrafts bug report.
func stringMsg(v string) any {
	return SkyADT{Tag: 1, SkyName: "GotString", Fields: []any{v}}
}

// TestApplyMsgArgs_MismatchReturnsSentinel — bool arg into a String
// constructor must return msgDecodeError, not panic.
func TestApplyMsgArgs_MismatchReturnsSentinel(t *testing.T) {
	// Wire arg: [true] — boolean, as radio onInput sends.
	raw := json.RawMessage("true")
	result := applyMsgArgs(any(stringMsg), []json.RawMessage{raw}, "")
	if _, ok := result.(msgDecodeError); !ok {
		t.Errorf("expected msgDecodeError sentinel, got %T %v", result, result)
	}
}

// TestApplyMsgArgs_StringArgSucceeds — the happy path. A String arg
// into a String -> Msg constructor dispatches cleanly.
func TestApplyMsgArgs_StringArgSucceeds(t *testing.T) {
	raw := json.RawMessage(`"guardian"`)
	result := applyMsgArgs(any(stringMsg), []json.RawMessage{raw}, "")
	adt, ok := result.(SkyADT)
	if !ok {
		t.Fatalf("expected SkyADT, got %T %v", result, result)
	}
	if adt.SkyName != "GotString" || len(adt.Fields) != 1 || adt.Fields[0] != "guardian" {
		t.Errorf("bad ADT payload: %+v", adt)
	}
}

// TestApplyMsgArgs_AnyParamAccepts — most Sky curried lambdas take
// `any` at the reflect level (that's what the lowerer emits). The
// type check must NOT reject those; it's only real Go-typed params
// (e.g. `func(s string) Msg`) where a wrong-type arg causes panic.
func TestApplyMsgArgs_AnyParamAccepts(t *testing.T) {
	anyFn := func(v any) any {
		return SkyADT{Tag: 0, SkyName: "GotAny", Fields: []any{v}}
	}
	raw := json.RawMessage("42")
	result := applyMsgArgs(any(anyFn), []json.RawMessage{raw}, "")
	if _, bad := result.(msgDecodeError); bad {
		t.Errorf("any-param fn should accept any wire arg, got msgDecodeError")
	}
	adt, ok := result.(SkyADT)
	if !ok || adt.SkyName != "GotAny" {
		t.Errorf("expected ADT, got %T %v", result, result)
	}
}

// TestApplyMsgArgs_NarrowsFormDataMap — `<form onSubmit=...>` extracts
// formData and JSON-encodes it as an object; json.Unmarshal returns
// `map[string]interface {}`. Sky-side Msg constructors are typed
// `Dict String String -> Msg` and the typed-codegen lowers them to
// `map[string]string`. Without map narrowing, applyMsgArgs would
// reject the wire arg and the form submit would silently drop.
//
// Surfaced by the sendcrafts auth-form refactor: switching password
// inputs from per-keystroke onInput to per-submit onSubmit (to avoid
// password-manager extension churn) ran into this gap.
func TestApplyMsgArgs_NarrowsFormDataMap(t *testing.T) {
	// Constructor expects `map[string]string`, what typed-codegen emits
	// for a Sky `Dict String String -> Msg` constructor.
	dictMsg := func(d map[string]string) any {
		return SkyADT{Tag: 0, SkyName: "Submitted", Fields: []any{d}}
	}
	// Wire payload: form-encoded JSON object.
	raw := json.RawMessage(`{"email":"alice@example.com","password":"hunter2"}`)
	result := applyMsgArgs(any(dictMsg), []json.RawMessage{raw}, "")
	adt, ok := result.(SkyADT)
	if !ok {
		t.Fatalf("expected SkyADT, got %T %v", result, result)
	}
	if adt.SkyName != "Submitted" || len(adt.Fields) != 1 {
		t.Fatalf("bad ADT shape: %+v", adt)
	}
	got, ok := adt.Fields[0].(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string after narrowing, got %T %v", adt.Fields[0], adt.Fields[0])
	}
	if got["email"] != "alice@example.com" || got["password"] != "hunter2" {
		t.Errorf("narrowed map missing fields: %+v", got)
	}
}

// TestApplyMsgArgs_NarrowsFormDataToRecordStruct — `<form onSubmit=...>`
// can also feed a typed record-alias Msg constructor:
//
//   type alias AuthCreds = { email : String, password : String }
//   type Msg = DoSignIn AuthCreds
//
// Sky lowers `AuthCreds` to a Go struct (`State_AuthCreds_R{Email, Password}`)
// and the constructor expects that struct, but the wire arg is JSON-decoded
// to `map[string]interface {}`. Field names map case-insensitively
// ("email" wire → "Email" struct field).
//
// Surfaced after the v0.9.8 map→map narrowing landed: sendcrafts had
// already migrated DoSignIn to take a typed AuthCreds record (cleaner
// than threading a Dict) and the form submit then hit this gap.
func TestApplyMsgArgs_NarrowsFormDataToRecordStruct(t *testing.T) {
	type AuthCreds struct {
		Email    string
		Password string
	}
	credsMsg := func(c AuthCreds) any {
		return SkyADT{Tag: 0, SkyName: "DoSignIn", Fields: []any{c}}
	}
	raw := json.RawMessage(`{"email":"hello@anzel.me","password":"hunter2"}`)
	result := applyMsgArgs(any(credsMsg), []json.RawMessage{raw}, "")
	adt, ok := result.(SkyADT)
	if !ok {
		t.Fatalf("expected SkyADT, got %T %v", result, result)
	}
	if adt.SkyName != "DoSignIn" || len(adt.Fields) != 1 {
		t.Fatalf("bad ADT shape: %+v", adt)
	}
	got, ok := adt.Fields[0].(AuthCreds)
	if !ok {
		t.Fatalf("expected AuthCreds, got %T %v", adt.Fields[0], adt.Fields[0])
	}
	if got.Email != "hello@anzel.me" || got.Password != "hunter2" {
		t.Errorf("narrowed record missing fields: %+v", got)
	}
}

// TestDispatch_DropsMsgDecodeError — dispatch() receiving the sentinel
// must return "" and NOT touch model state.
func TestDispatch_DropsMsgDecodeError(t *testing.T) {
	viewFn := func(model any) any {
		return velement("p", nil, []any{vtext("v=" + fmt_sprint(model))})
	}
	originalModel := "initial"
	updateCalls := 0
	app := &liveApp{
		update: func(msg, model any) any {
			updateCalls++
			return SkyTuple2{V0: "MUTATED", V1: cmdT{kind: "none"}}
		},
		view:    viewFn,
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	sess := &liveSession{
		model:     originalModel,
		handlers:  map[string]any{},
		sseCh:     make(chan string, 1),
		cancelSub: make(chan struct{}),
	}
	body := app.dispatch(sess, msgDecodeError{})
	if body != "" {
		t.Errorf("decode error must return empty body, got %q", body)
	}
	if updateCalls != 0 {
		t.Errorf("update must not be called on decode error; called %d times", updateCalls)
	}
	if sess.model != originalModel {
		t.Errorf("model mutated on decode error: %v -> %v", originalModel, sess.model)
	}
}

// TestDispatch_RecoversFromPanic — a user-code panic inside update
// must be caught, logged, and not propagate. Session state stays
// consistent; body returns "" so the client sees no change.
func TestDispatch_RecoversFromPanic(t *testing.T) {
	originalModel := "initial"
	app := &liveApp{
		update: func(msg, model any) any {
			panic("deliberate panic from update()")
		},
		view: func(model any) any {
			return velement("p", nil, []any{vtext("x")})
		},
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	sess := &liveSession{
		model:     originalModel,
		handlers:  map[string]any{},
		sseCh:     make(chan string, 1),
		cancelSub: make(chan struct{}),
	}
	// Pick any Msg — the update() panic fires regardless.
	body := app.dispatch(sess, SkyADT{Tag: 0, SkyName: "Anything"})
	if body != "" {
		t.Errorf("panic must yield empty body, got %q", body)
	}
	if sess.model != originalModel {
		t.Errorf("model mutated on panic: %v -> %v", originalModel, sess.model)
	}
}

// TestArgAssignable_Coverage — the type-check helper must accept:
// - assignable types (string → string, int → int)
// - any arg into an interface{} param
// - nil into pointer/slice/interface params
// and reject clear mismatches (bool → string).
func TestArgAssignable_Coverage(t *testing.T) {
	stringFn := func(s string) any { return s }
	intFn := func(n int) any { return n }
	anyFn := func(v any) any { return v }
	ptrFn := func(p *int) any { return p }

	cases := []struct {
		name string
		fn   any
		arg  any
		want bool
	}{
		{"string->string OK", stringFn, "hi", true},
		{"bool->string FAIL", stringFn, true, false},
		{"int->string FAIL", stringFn, 42, false},
		{"int->int OK", intFn, 42, true},
		{"float->int FAIL", intFn, 3.14, false},
		{"any accepts string", anyFn, "hi", true},
		{"any accepts bool", anyFn, true, true},
		{"any accepts nil", anyFn, nil, true},
		{"nil into *int OK", ptrFn, nil, true},
		{"nil into string FAIL", stringFn, nil, false},
	}
	for _, tc := range cases {
		if got := argAssignableToFunc(tc.fn, tc.arg); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// fmt_sprint — small helper so the view fn can stringify whatever
// model value we throw at it without depending on the Sky runtime's
// kernel `fmt` wiring.
func fmt_sprint(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "???"
}
