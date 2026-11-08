package rt

import (
	"reflect"
	"testing"
)

// #467 — rt.Coerce struct→struct narrow.
//
// `Server.json body |> Server.withStatus 201` is the documented idiom
// in CLAUDE.md / docs/stdlib.md for building a typed `Response`. The
// runtime kernels (Server_json + Server_withStatus) return the FFI
// struct `rt.SkyResponse`. The user-facing Sky stdlib declares
// `Sky.Http.Server.Response` as a `type alias`, which lowers to a
// distinct nominal struct `Sky_Http_Server_Response_R` with the same
// PascalCase field set (no StreamHandler — the user-side alias hides
// the streaming-bridge field).
//
// Typed codegen wraps the call result in `rt.Coerce[Sky_Http_Server_Response_R](...)`.
// Pre-fix, that Coerce dropped through to its strict panic with
// `expected ..._Response_R, got rt.SkyResponse` even though every
// field is a 1:1 PascalCase match.
//
// Fix: extend Coerce to call narrowStructToStruct (already used by
// coerceInner for the same direction) when both source and target
// are record-shaped structs. Mirrors the existing branch in
// coerceInner (rt.go:539) so the two coercion entry points no longer
// diverge for this shape.

// Mirror of the user-side typed alias the Sky compiler emits for
// `type alias Response = { status, body, headers, contentType }`
// (no StreamHandler — that field is intentionally hidden from the
// user-side surface).
type stubResponseR struct {
	Status      int
	Body        string
	Headers     map[string]string
	ContentType string
}

// Same plus the optional StreamHandler — exercises the "source has a
// field the target lacks" branch (narrowStructToStruct should still
// match on the overlapping fields).
type stubResponseWithStreamR struct {
	Status        int
	Body          string
	Headers       map[string]string
	ContentType   string
	StreamHandler any
}

func TestCoerceSkyResponseToTypedAlias(t *testing.T) {
	// Build the source the same way Server_json + Server_withStatus do.
	src := SkyResponse{
		Status:      201,
		Body:        `{"ok":true}`,
		Headers:     map[string]string{},
		ContentType: "application/json",
	}

	// Pre-fix this panicked with
	//   rt.Coerce: expected ..._Response_R, got rt.SkyResponse
	out := Coerce[stubResponseR](src)

	if out.Status != 201 {
		t.Fatalf("Status: want 201, got %d", out.Status)
	}
	if out.Body != `{"ok":true}` {
		t.Fatalf("Body: want JSON, got %q", out.Body)
	}
	if out.ContentType != "application/json" {
		t.Fatalf("ContentType: want application/json, got %q", out.ContentType)
	}
	if out.Headers == nil {
		t.Fatalf("Headers: want non-nil map, got nil")
	}
}

func TestCoerceSkyResponseToTypedAliasWithStream(t *testing.T) {
	// The user-side alias DOES carry StreamHandler (one possible future
	// surface). Verify the narrow copies it through too.
	src := SkyResponse{
		Status:        200,
		Body:          "chunked",
		Headers:       map[string]string{"X-Stream": "1"},
		ContentType:   "text/event-stream",
		StreamHandler: func() {}, // sentinel non-nil func value
	}

	out := Coerce[stubResponseWithStreamR](src)

	if out.Status != 200 {
		t.Fatalf("Status: want 200, got %d", out.Status)
	}
	if out.Body != "chunked" {
		t.Fatalf("Body: want chunked, got %q", out.Body)
	}
	if out.Headers["X-Stream"] != "1" {
		t.Fatalf("Headers[X-Stream]: want 1, got %q", out.Headers["X-Stream"])
	}
	if out.StreamHandler == nil {
		t.Fatalf("StreamHandler: want non-nil, got nil")
	}
}

// Mirror of `Sky_Http_Server_Request_R` — the inbound-request typed
// alias. Symmetric path to Response (rt.SkyRequest → typed alias).
type stubRequestR struct {
	Method     string
	Path       string
	Body       string
	RemoteAddr string
	Headers    map[string]any
	Params     map[string]any
	Query      map[string]any
	Cookies    map[string]string
}

func TestCoerceSkyRequestToTypedAlias(t *testing.T) {
	src := SkyRequest{
		Method:     "POST",
		Path:       "/api/todos",
		Body:       `{"text":"buy milk"}`,
		RemoteAddr: "127.0.0.1:54321",
		Headers:    map[string]any{"Content-Type": "application/json"},
		Params:     map[string]any{},
		Query:      map[string]any{},
		Cookies:    map[string]string{"session": "abc"},
	}

	out := Coerce[stubRequestR](src)

	if out.Method != "POST" {
		t.Fatalf("Method: want POST, got %q", out.Method)
	}
	if out.Path != "/api/todos" {
		t.Fatalf("Path: want /api/todos, got %q", out.Path)
	}
	if out.Headers["Content-Type"] != "application/json" {
		t.Fatalf("Headers[Content-Type]: want application/json, got %v", out.Headers["Content-Type"])
	}
	if out.Cookies["session"] != "abc" {
		t.Fatalf("Cookies[session]: want abc, got %q", out.Cookies["session"])
	}
}

// Defensive: confirm the previously-panicking call no longer panics
// — Cycle 6 PC's CoerceFailure classifier captured this path under
// `rt.Coerce: expected ... got rt.SkyResponse`. Belt-and-braces.
func TestCoerceSkyResponseDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic, got %v", r)
		}
	}()

	src := SkyResponse{Status: 418, Body: "I'm a teapot"}
	_ = Coerce[stubResponseR](src)
}

// Runtime containers use their own cross-instantiation reconstruction. An
// ordinary record's integer Tag field does not make it such a container.
type stubADT = SkyMaybe[any]

type stubGenericRecord struct {
	Tag  int
	Body string
}

func TestNarrowStructToStructSkipsADT(t *testing.T) {
	srcVal := stubGenericRecord{Tag: 5, Body: "hello"}
	if _, ok := narrowStructToStruct(reflect.ValueOf(srcVal), reflect.TypeOf(stubADT{})); ok {
		t.Fatal("ordinary record was narrowed into an actual container")
	}
	if _, ok := narrowStructToStruct(reflect.ValueOf(stubADT{Tag: 0, JustValue: 3}), reflect.TypeOf(srcVal)); ok {
		t.Fatal("actual container was reinterpreted as an ordinary record")
	}
	out := Coerce[stubGenericRecord](srcVal)
	if out.Tag != 5 || out.Body != "hello" {
		t.Fatalf("identity narrow failed: %+v", out)
	}
}
