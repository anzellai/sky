package rt

// codec_auto_backstop.go — B-1 runtime backstop for `Codec.auto`.
//
// The checker (S3b's Encodable bound) refuses these types at compile time.
// This is the runtime half, for every path the checker cannot see (a value
// behind `any`, a stdlib path, a stale build):
//
//   - a function has no JSON form;
//   - a `Secret`, a `Std.Crypto` key and a crypto protocol state keep their
//     bytes in unexported fields, so the reflective encoder wrote `{}` and
//     the decoder rebuilt an EMPTY value from any object: silent data loss,
//     and on decode a value the client chose;
//   - a runtime handle (Process, Watcher, the Sync handles, WebSocket,
//     WebSocketServer, StreamId, StreamWriter, Cache) is an id that names a
//     live resource of THIS process. Decoding one from client input would let
//     the client choose which resource the app touches.
//
// Encoding one is a classified JsonEncodeFailure panic (Codec.toJson cannot
// return an Err); decoding one is an Err.

import (
	"fmt"
	"reflect"
	"strings"
)

// codecHandleTypes are the Go type names of the runtime handle types.
var codecHandleTypes = map[string]bool{
	"Sky_Core_Process_Process":                  true,
	"Std_Watch_Watcher":                         true,
	"Std_Sync_Ref":                              true,
	"Std_Sync_Mutex":                            true,
	"Std_Sync_Queue":                            true,
	"Sky_Core_WebSocket_WebSocket":              true,
	"Sky_Http_Server_WebSocket_WebSocketServer": true,
	"Sky_Core_Http_Stream_StreamId":             true,
	"Sky_Http_Server_Stream_StreamWriter":       true,
	"Std_Cache_Cache":                           true,
}

// codecHandleCtors are the constructor names of the handle types (the
// SkyName of their runtime value), with and without the `__Internal` suffix
// an opaque constructor carries.
var codecHandleCtors = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range []string{"Process", "Watcher", "WebSocket", "WebSocketServer",
		"StreamId", "StreamWriter", "Cache", "Ref", "Mutex", "Queue"} {
		m[n] = true
		m[n+"__Internal"] = true
	}
	// Stdlib handles are legacy SkyADT values; a user union is a sealed
	// variant and never matches here. The Sync handles have no bare
	// constructor, so only their `__Internal` names are listed.
	delete(m, "Ref")
	delete(m, "Mutex")
	delete(m, "Queue")
	return m
}()

// codecIsHandleADT reports whether v is a runtime handle value.
func codecIsHandleADT(v any) bool {
	adt, ok := v.(SkyADT)
	return ok && codecHandleCtors[adt.SkyName]
}

// codecOpaqueStruct reports whether t is a struct with fields, none of them
// exported: a Secret, a key, a protocol state. Encoding it gives `{}`.
func codecOpaqueStruct(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).PkgPath == "" {
			return false
		}
	}
	return true
}

func codecOpaqueTypeName(t reflect.Type) string {
	n := t.Name()
	if n == "" {
		n = t.String()
	}
	return n
}

// codecUnencodableError explains the refusal.
func codecUnencodableError(what string) error {
	return fmt.Errorf("Codec.auto: cannot encode %s: it has no data a codec can carry "+
		"(a function, a Secret, a key, a crypto state or a runtime handle). "+
		"Keep it out of records that are saved or sent", what)
}

// codecUndecodableError explains the decode refusal.
func codecUndecodableError(what string) error {
	return fmt.Errorf("Codec.auto: cannot decode %s: a function, a Secret, a key, a crypto state "+
		"or a runtime handle is never built from JSON", what)
}

// codecHandleDeclared reports whether a field's declared Go type is a handle.
func codecHandleDeclared(declaredType string) bool {
	if i := strings.LastIndexByte(declaredType, '.'); i >= 0 {
		declaredType = declaredType[i+1:]
	}
	return codecHandleTypes[declaredType]
}
