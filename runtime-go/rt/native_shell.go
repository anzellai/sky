package rt

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

// The native-shell protocol behind Std.Native.secureSet / secureGet /
// secureRemove / authenticate.
//
// These capabilities have no web API: a browser has no secure store and no
// biometric prompt a page may drive. They run in the wasm client and reach the
// NATIVE SHELL the client is hosted in, through the same bridge
// Std.Native.bridge uses:
//
//   - iOS: window.webkit.messageHandlers.skyNative (WKScriptMessageHandlerWithReply),
//     Keychain + LocalAuthentication in the generated Swift shell;
//   - Android: window.SkyNative.call(op, payload, cbId), an Android
//     Keystore AES-GCM key + BiometricPrompt in the generated Java shell;
//   - macOS desktop (Sky.Webview window over the client): window.__skyNative,
//     bound by the Go shell (webview.go) to the Keychain + LocalAuthentication
//     (native_desktop_darwin.go).
//
// Each op is a reserved bridge name ("sky:" prefix, so an app's own
// Native.bridge handler can never shadow one) with a JSON object payload of
// string fields. A shell replies with a string on success, or rejects with
// "<kind>: <message>", where kind is one of unavailable / cancelled / failed /
// invalid; anything else is an I/O fault. The decoding below is pure, so it is
// tested on every build with a mock shell (native_shell_test.go); the wasm
// kernels (native_wasm.go) only supply the JS transport.
//
// No fallback: when no shell answers, the result is Err Unavailable. A value an
// app asked to keep secret is never written to localStorage instead.

const (
	nativeOpSecureSet    = "sky:secureSet"
	nativeOpSecureGet    = "sky:secureGet"
	nativeOpSecureRemove = "sky:secureRemove"
	nativeOpAuthenticate = "sky:authenticate"
)

// nativeKeyMax bounds a secure-store key. The Keychain account attribute and
// an Android SharedPreferences key both take far more; the bound keeps a key a
// name, not a payload.
const nativeKeyMax = 256

// nativeShellReply is what a shell bridge answered.
type nativeShellReply struct {
	// Present is false when no native shell bridge exists (a plain browser).
	Present bool
	Ok      bool
	// Data is the reply on success, "<kind>: <message>" on failure.
	Data string
}

// nativeShellTransport sends one op with a JSON payload to the shell.
type nativeShellTransport func(op string, payload string) nativeShellReply

// nativeNoShell is the message for a runtime with no native shell.
func nativeNoShell(what string) any {
	return ErrUnavailable(what + " needs a native app shell (iOS, Android or the macOS " +
		"desktop app); this runtime has none. Sky does not fall back to localStorage.")
}

// nativeShellErr maps a shell rejection "<kind>: <message>" to a Sky Error.
func nativeShellErr(data string) any {
	kind, msg, found := strings.Cut(data, ":")
	if !found {
		return ErrIo(data)
	}
	msg = strings.TrimSpace(msg)
	switch strings.TrimSpace(kind) {
	case "unavailable":
		return ErrUnavailable(msg)
	case "cancelled":
		return ErrPermissionDenied("cancelled: " + msg)
	case "invalid":
		return ErrInvalidInput(msg)
	default:
		return ErrIo(data)
	}
}

// nativeValidKey refuses an empty, over-long or control-character key.
func nativeValidKey(key string) error {
	if key == "" {
		return errors.New("the key is empty")
	}
	if len(key) > nativeKeyMax {
		return errors.New("the key is longer than 256 bytes")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return errors.New("the key contains a control character")
		}
	}
	return nil
}

func nativePayload(fields map[string]string) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

func nativeSecureSetVia(t nativeShellTransport, key, value string) SkyResult[any, any] {
	if err := nativeValidKey(key); err != nil {
		return Err[any, any](ErrInvalidInput("Native.secureSet: " + err.Error()))
	}
	r := t(nativeOpSecureSet, nativePayload(map[string]string{"key": key, "value": value}))
	switch {
	case !r.Present:
		return Err[any, any](nativeNoShell("Native.secureSet"))
	case !r.Ok:
		return Err[any, any](nativeShellErr(r.Data))
	default:
		return Ok[any, any](struct{}{})
	}
}

// nativeSecureGetReply is the shell's success reply for sky:secureGet.
type nativeSecureGetReply struct {
	Found bool   `json:"found"`
	Value string `json:"value"`
}

func nativeSecureGetVia(t nativeShellTransport, key string) SkyResult[any, any] {
	if err := nativeValidKey(key); err != nil {
		return Err[any, any](ErrInvalidInput("Native.secureGet: " + err.Error()))
	}
	r := t(nativeOpSecureGet, nativePayload(map[string]string{"key": key}))
	switch {
	case !r.Present:
		return Err[any, any](nativeNoShell("Native.secureGet"))
	case !r.Ok:
		return Err[any, any](nativeShellErr(r.Data))
	}
	var rep nativeSecureGetReply
	if err := json.Unmarshal([]byte(r.Data), &rep); err != nil {
		return Err[any, any](ErrDecode("Native.secureGet: the shell replied " +
			"with something that is not {found, value}: " + err.Error()))
	}
	if !rep.Found {
		return Ok[any, any](Nothing[any]())
	}
	return Ok[any, any](Just[any](Secret{v: rep.Value}))
}

func nativeSecureRemoveVia(t nativeShellTransport, key string) SkyResult[any, any] {
	if err := nativeValidKey(key); err != nil {
		return Err[any, any](ErrInvalidInput("Native.secureRemove: " + err.Error()))
	}
	r := t(nativeOpSecureRemove, nativePayload(map[string]string{"key": key}))
	switch {
	case !r.Present:
		return Err[any, any](nativeNoShell("Native.secureRemove"))
	case !r.Ok:
		return Err[any, any](nativeShellErr(r.Data))
	default:
		return Ok[any, any](struct{}{})
	}
}

// nativeAuthenticateVia runs sky:authenticate. A "failed:" rejection (the
// biometric did not match and the system gave up) is Ok False: a normal answer.
// "cancelled:" is Err PermissionDenied; "unavailable:" is Err Unavailable.
func nativeAuthenticateVia(t nativeShellTransport, reason string) SkyResult[any, any] {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Err[any, any](ErrInvalidInput("Native.authenticate: the reason is empty; " +
			"the prompt shows it to the user"))
	}
	r := t(nativeOpAuthenticate, nativePayload(map[string]string{"reason": reason}))
	switch {
	case !r.Present:
		return Err[any, any](nativeNoShell("Native.authenticate"))
	case !r.Ok && strings.HasPrefix(strings.TrimSpace(r.Data), "failed:"):
		return Ok[any, any](false)
	case !r.Ok:
		return Err[any, any](nativeShellErr(r.Data))
	case strings.TrimSpace(r.Data) == "true":
		return Ok[any, any](true)
	default:
		return Err[any, any](ErrDecode("Native.authenticate: the shell replied " +
			r.Data + ", not true"))
	}
}

// ── the shell side (used by the macOS desktop shell) ─────────────────────────

// nativeSecureStore is a platform secret store.
type nativeSecureStore interface {
	Set(key, value string) error
	Get(key string) (value string, found bool, err error)
	Remove(key string) error
}

// nativeBiometric asks for a biometric confirmation. It returns nil on
// success, or an error whose message is "<kind>: <message>" (failed /
// cancelled / unavailable).
type nativeBiometric func(reason string) error

// nativeShellDispatch is the shell side of the protocol: it decodes one op
// and runs it against the platform store and biometric prompt. The reply or
// error message follows the protocol above, so the macOS desktop shell's
// Bind handler is this function over the Keychain.
func nativeShellDispatch(store nativeSecureStore, auth nativeBiometric, op, payload string) (string, error) {
	var p map[string]string
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", errors.New("invalid: the payload is not a JSON object of strings")
	}
	key := p["key"]
	switch op {
	case nativeOpSecureSet, nativeOpSecureGet, nativeOpSecureRemove:
		if store == nil {
			return "", errors.New("unavailable: this shell has no secure store")
		}
		if err := nativeValidKey(key); err != nil {
			return "", errors.New("invalid: " + err.Error())
		}
	case nativeOpAuthenticate:
		if auth == nil {
			return "", errors.New("unavailable: this shell has no biometric prompt")
		}
	default:
		return "", errors.New("invalid: unknown op " + op)
	}
	switch op {
	case nativeOpSecureSet:
		if err := store.Set(key, p["value"]); err != nil {
			return "", err
		}
		return "", nil
	case nativeOpSecureGet:
		v, found, err := store.Get(key)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(nativeSecureGetReply{Found: found, Value: v})
		return string(b), nil
	case nativeOpSecureRemove:
		if err := store.Remove(key); err != nil {
			return "", err
		}
		return "", nil
	default:
		if err := auth(p["reason"]); err != nil {
			return "", err
		}
		return "true", nil
	}
}
