package rt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// mockStore is an in-memory nativeSecureStore.
type mockStore struct {
	m    map[string]string
	fail error
}

func (s *mockStore) Set(k, v string) error {
	if s.fail != nil {
		return s.fail
	}
	s.m[k] = v
	return nil
}

func (s *mockStore) Get(k string) (string, bool, error) {
	if s.fail != nil {
		return "", false, s.fail
	}
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *mockStore) Remove(k string) error {
	if s.fail != nil {
		return s.fail
	}
	delete(s.m, k)
	return nil
}

// mockShell wires the client side of the protocol straight into the shell
// side (nativeShellDispatch), the way a real bridge carries it, and records
// the ops and payloads that crossed.
type mockShell struct {
	store *mockStore
	auth  nativeBiometric
	seen  []string
}

func (m *mockShell) transport(op, payload string) nativeShellReply {
	m.seen = append(m.seen, op+" "+payload)
	var store nativeSecureStore
	if m.store != nil {
		store = m.store
	}
	out, err := nativeShellDispatch(store, m.auth, op, payload)
	if err != nil {
		return nativeShellReply{Present: true, Ok: false, Data: err.Error()}
	}
	return nativeShellReply{Present: true, Ok: true, Data: out}
}

func errKind(t *testing.T, r SkyResult[any, any]) (int, string) {
	t.Helper()
	if r.Tag != 1 {
		t.Fatalf("expected Err, got Ok %v", r.OkValue)
	}
	e, ok := r.ErrValue.(skyErrorAdt)
	if !ok {
		t.Fatalf("Err is %T, not a Sky Error", r.ErrValue)
	}
	kind, _ := e.Fields[0].(int)
	info, _ := e.Fields[1].(skyErrorInfo)
	return kind, info.Message
}

const (
	kindIo               = 0
	kindDecode           = 3
	kindPermissionDenied = 6
	kindInvalidInput     = 7
	kindUnavailable      = 9
)

func TestSecureStoreRoundTripsThroughAMockShell(t *testing.T) {
	shell := &mockShell{store: &mockStore{m: map[string]string{}}}

	if r := nativeSecureSetVia(shell.transport, "refresh", "tok-123"); r.Tag != 0 {
		t.Fatalf("secureSet: %+v", r)
	}
	r := nativeSecureGetVia(shell.transport, "refresh")
	if r.Tag != 0 {
		t.Fatalf("secureGet: %+v", r)
	}
	m, ok := r.OkValue.(SkyMaybe[any])
	if !ok || m.Tag != 0 {
		t.Fatalf("secureGet must be Just, got %#v", r.OkValue)
	}
	sec, ok := m.JustValue.(Secret)
	if !ok {
		t.Fatalf("secureGet must yield a Secret, got %T", m.JustValue)
	}
	if secretReveal(sec) != "tok-123" {
		t.Fatalf("round trip lost the value")
	}
	if got := sec.String(); strings.Contains(got, "tok-123") {
		t.Fatalf("the Secret must redact itself, printed %q", got)
	}

	if r := nativeSecureRemoveVia(shell.transport, "refresh"); r.Tag != 0 {
		t.Fatalf("secureRemove: %+v", r)
	}
	r = nativeSecureGetVia(shell.transport, "refresh")
	if m := r.OkValue.(SkyMaybe[any]); m.Tag != 1 {
		t.Fatalf("a removed key must read as Nothing, got %#v", r.OkValue)
	}
	// Removing an absent key is Ok.
	if r := nativeSecureRemoveVia(shell.transport, "never-set"); r.Tag != 0 {
		t.Fatalf("removing an absent key: %+v", r)
	}

	// The reserved op names crossed the bridge, and the value crossed only in
	// the secureSet payload.
	for _, want := range []string{"sky:secureSet ", "sky:secureGet ", "sky:secureRemove "} {
		found := false
		for _, s := range shell.seen {
			found = found || strings.HasPrefix(s, want)
		}
		if !found {
			t.Errorf("op %q never crossed the bridge: %v", want, shell.seen)
		}
	}
}

// With no native shell (a plain browser, a server) every op is Err
// Unavailable, and nothing is written anywhere.
func TestNoShellIsUnavailableNeverAFallback(t *testing.T) {
	noShell := func(string, string) nativeShellReply { return nativeShellReply{} }
	for name, r := range map[string]SkyResult[any, any]{
		"secureSet":    nativeSecureSetVia(noShell, "k", "v"),
		"secureGet":    nativeSecureGetVia(noShell, "k"),
		"secureRemove": nativeSecureRemoveVia(noShell, "k"),
		"authenticate": nativeAuthenticateVia(noShell, "Unlock"),
	} {
		kind, msg := errKind(t, r)
		if kind != kindUnavailable {
			t.Errorf("%s: kind %d, want Unavailable", name, kind)
		}
		if !strings.Contains(msg, "localStorage") {
			t.Errorf("%s: the message must say there is no localStorage fallback: %q", name, msg)
		}
	}
	// The non-client kernels are the same no-shell path.
	for name, thunk := range map[string]any{
		"secureSet":    Native_secureSet("k", Secret_fromString("v")),
		"secureGet":    Native_secureGet("k"),
		"secureRemove": Native_secureRemove("k"),
		"authenticate": Native_authenticate("Unlock"),
	} {
		r := thunk.(func() any)().(SkyResult[any, any])
		if kind, _ := errKind(t, r); kind != kindUnavailable {
			t.Errorf("Native_%s off-client: kind %d, want Unavailable", name, kind)
		}
	}
}

func TestSecureStoreRefusesABadKeyBeforeTheShell(t *testing.T) {
	shell := &mockShell{store: &mockStore{m: map[string]string{}}}
	for _, key := range []string{"", strings.Repeat("k", 257), "a\nb"} {
		if kind, _ := errKind(t, nativeSecureSetVia(shell.transport, key, "v")); kind != kindInvalidInput {
			t.Errorf("key %q: kind %d, want InvalidInput", key, kind)
		}
	}
	if len(shell.seen) != 0 {
		t.Errorf("a bad key must not reach the shell: %v", shell.seen)
	}
}

// The value handed to the kernel is revealed into the payload, never the
// redacted rendering.
func TestSecureSetKernelSendsTheRevealedValue(t *testing.T) {
	var got string
	capture := func(op, payload string) nativeShellReply {
		var p map[string]string
		_ = json.Unmarshal([]byte(payload), &p)
		got = p["value"]
		return nativeShellReply{Present: true, Ok: true}
	}
	v := secretReveal(Secret_fromString("hunter2"))
	nativeSecureSetVia(capture, "pw", v)
	if got != "hunter2" {
		t.Fatalf("payload value %q, want the revealed secret", got)
	}
}

func TestShellErrorsKeepTheirKinds(t *testing.T) {
	shell := &mockShell{store: &mockStore{m: map[string]string{}, fail: errors.New("unavailable: the keychain is locked")}}
	if kind, msg := errKind(t, nativeSecureSetVia(shell.transport, "k", "v")); kind != kindUnavailable || !strings.Contains(msg, "locked") {
		t.Errorf("unavailable: kind %d msg %q", kind, msg)
	}
	shell.store.fail = errors.New("keychain status -25308")
	if kind, _ := errKind(t, nativeSecureGetVia(shell.transport, "k")); kind != kindIo {
		t.Errorf("an unclassified shell fault is Io, got %d", kind)
	}
	bad := func(string, string) nativeShellReply {
		return nativeShellReply{Present: true, Ok: true, Data: "not json"}
	}
	if kind, _ := errKind(t, nativeSecureGetVia(bad, "k")); kind != kindDecode {
		t.Errorf("a malformed get reply is Decode, got %d", kind)
	}
}

// Biometrics: success, a failed match (Ok False), a cancel (PermissionDenied)
// and no hardware (Unavailable) are four distinct outcomes.
func TestAuthenticateDistinguishesCancelFailedUnavailable(t *testing.T) {
	cases := []struct {
		name    string
		authErr error
		tag     int
		okValue any
		kind    int
	}{
		{"success", nil, 0, true, 0},
		{"failed", errors.New("failed: the face did not match"), 0, false, 0},
		{"cancelled", errors.New("cancelled: the user tapped Cancel"), 1, nil, kindPermissionDenied},
		{"unavailable", errors.New("unavailable: no biometrics enrolled"), 1, nil, kindUnavailable},
	}
	for _, c := range cases {
		var gotReason string
		shell := &mockShell{auth: func(reason string) error {
			gotReason = reason
			return c.authErr
		}}
		r := nativeAuthenticateVia(shell.transport, "Unlock your vault")
		if gotReason != "Unlock your vault" {
			t.Errorf("%s: the reason did not reach the prompt: %q", c.name, gotReason)
		}
		if r.Tag != c.tag {
			t.Errorf("%s: tag %d, want %d (%+v)", c.name, r.Tag, c.tag, r)
			continue
		}
		if c.tag == 0 && r.OkValue != c.okValue {
			t.Errorf("%s: Ok %v, want %v", c.name, r.OkValue, c.okValue)
		}
		if c.tag == 1 {
			if kind, _ := errKind(t, r); kind != c.kind {
				t.Errorf("%s: kind %d, want %d", c.name, kind, c.kind)
			}
		}
	}
	if kind, _ := errKind(t, nativeAuthenticateVia((&mockShell{}).transport, "  ")); kind != kindInvalidInput {
		t.Errorf("an empty reason is InvalidInput, got %d", kind)
	}
	// A shell with no biometric prompt says so.
	if kind, _ := errKind(t, nativeAuthenticateVia((&mockShell{}).transport, "Unlock")); kind != kindUnavailable {
		t.Errorf("no prompt is Unavailable, got %d", kind)
	}
}

func TestShellDispatchRefusesUnknownOpsAndBadPayloads(t *testing.T) {
	store := &mockStore{m: map[string]string{}}
	if _, err := nativeShellDispatch(store, nil, "sky:format", "{}"); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid:") {
		t.Errorf("unknown op: %v", err)
	}
	if _, err := nativeShellDispatch(store, nil, nativeOpSecureGet, "[1]"); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid:") {
		t.Errorf("bad payload: %v", err)
	}
	if _, err := nativeShellDispatch(nil, nil, nativeOpSecureGet, `{"key":"k"}`); err == nil ||
		!strings.HasPrefix(err.Error(), "unavailable:") {
		t.Errorf("no store: %v", err)
	}
}

// mockScanner is the mobile shells' side of sky:scanCode: it reads the
// payload the way the Swift and Java scanners do and answers with a fixed
// outcome.
type mockScanner struct {
	payload map[string]string
	reply   nativeShellReply
}

func (m *mockScanner) transport(op, payload string) nativeShellReply {
	if op != nativeOpScanCode {
		return nativeShellReply{Present: true, Ok: false, Data: "invalid: unknown op " + op}
	}
	_ = json.Unmarshal([]byte(payload), &m.payload)
	return m.reply
}

// Native.scanCode: a scanned code is [format, text], a closed scanner is [],
// and the formats and prompt reach the shell.
func TestScanCodeReturnsTheCodeOrNothing(t *testing.T) {
	sc := &mockScanner{reply: nativeShellReply{Present: true, Ok: true,
		Data: `{"found":true,"format":"qr","text":"sky-pair:tablet-1:k3y"}`}}
	r := nativeScanCodeVia(sc.transport, []string{"qr", "ean13"}, " Scan the pairing code ")
	if r.Tag != 0 {
		t.Fatalf("scan: %+v", r)
	}
	got, _ := r.OkValue.([]any)
	if len(got) != 2 || got[0] != "qr" || got[1] != "sky-pair:tablet-1:k3y" {
		t.Fatalf("scan result %v, want [qr, sky-pair:tablet-1:k3y]", r.OkValue)
	}
	if sc.payload["formats"] != "qr,ean13" || sc.payload["prompt"] != "Scan the pairing code" {
		t.Errorf("payload %v", sc.payload)
	}

	// The user closed the scanner: Ok [] (Nothing on the Sky side).
	sc.reply = nativeShellReply{Present: true, Ok: true, Data: `{"found":false}`}
	r = nativeScanCodeVia(sc.transport, nil, "")
	if xs, ok := r.OkValue.([]any); r.Tag != 0 || !ok || len(xs) != 0 {
		t.Fatalf("cancel: %+v", r)
	}
	// No formats asked for means every format.
	if sc.payload["formats"] != strings.Join(nativeCodeFormats, ",") {
		t.Errorf("no formats must ask for all of them: %q", sc.payload["formats"])
	}
}

func TestScanCodeErrorKinds(t *testing.T) {
	cases := []struct {
		name  string
		reply nativeShellReply
		kind  int
	}{
		{"no shell", nativeShellReply{}, kindUnavailable},
		{"simulator", nativeShellReply{Present: true, Data: "unavailable: this device cannot scan codes"}, kindUnavailable},
		{"camera refused", nativeShellReply{Present: true, Data: "denied: camera access is off for this app"}, kindPermissionDenied},
		{"bad reply", nativeShellReply{Present: true, Ok: true, Data: "not json"}, kindDecode},
		{"unasked format", nativeShellReply{Present: true, Ok: true, Data: `{"found":true,"format":"code39","text":"X"}`}, kindDecode},
	}
	for _, c := range cases {
		sc := &mockScanner{reply: c.reply}
		transport := sc.transport
		if !c.reply.Present {
			transport = func(string, string) nativeShellReply { return nativeShellReply{} }
		}
		kind, msg := errKind(t, nativeScanCodeVia(transport, []string{"qr"}, ""))
		if kind != c.kind {
			t.Errorf("%s: kind %d (%q), want %d", c.name, kind, msg, c.kind)
		}
	}
	// An unknown format is refused before the shell is asked.
	sc := &mockScanner{}
	if kind, _ := errKind(t, nativeScanCodeVia(sc.transport, []string{"qr", "barcode"}, "")); kind != kindInvalidInput {
		t.Errorf("unknown format: kind %d, want InvalidInput", kind)
	}
	if sc.payload != nil {
		t.Errorf("an unknown format must not reach the shell: %v", sc.payload)
	}
	// Off the client (a server, a CLI): Unavailable.
	r := Native_scanCode([]any{"qr"}, "Scan").(func() any)().(SkyResult[any, any])
	if kind, _ := errKind(t, r); kind != kindUnavailable {
		t.Errorf("Native_scanCode off-client: kind %d, want Unavailable", kind)
	}
	// The macOS desktop shell has no camera scanner.
	if _, err := nativeShellDispatch(nil, nil, nativeOpScanCode, `{"formats":"qr"}`); err == nil ||
		!strings.HasPrefix(err.Error(), "unavailable:") {
		t.Errorf("desktop scan: %v", err)
	}
}

// The wire names are the Std.Native.CodeFormat constructors, in order. The
// Sky side maps each constructor to its name (`formatName`) and back
// (`formatFromName`); a name added on one side only would make a scanned code
// fail to decode.
func TestNativeCodeFormatsMatchTheStdlib(t *testing.T) {
	src, err := os.ReadFile("../../sky-stdlib/Std/Native.sky")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "formatName : CodeFormat -> String")
	if start < 0 {
		t.Fatal("Std.Native has no formatName")
	}
	end := strings.Index(body[start:], "\n\n\n")
	var names []string
	for _, line := range strings.Split(body[start:start+end], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"") && strings.HasSuffix(line, "\"") {
			names = append(names, strings.Trim(line, "\""))
		}
	}
	if strings.Join(names, ",") != strings.Join(nativeCodeFormats, ",") {
		t.Fatalf("Std.Native formatName %v, runtime %v", names, nativeCodeFormats)
	}
}

// Native.notify goes through the native-shell protocol (sky:notify), so a
// shell answers only once the notification is posted. Before v0.27.0 the
// Android bridge was a synchronous call: on Android 13 and later it answered
// Ok while the POST_NOTIFICATIONS prompt still showed, and NotificationManager
// dropped the notification. The shells now wait for the prompt's answer; a
// refusal is Err PermissionDenied, and only where no shell has native
// notifications does the Web Notification API run.
func TestNotifyGoesThroughTheShellAndKeepsItsAnswer(t *testing.T) {
	var ops []string
	var payload map[string]string
	webCalls := 0
	web := func() SkyResult[any, any] {
		webCalls++
		return Ok[any, any](struct{}{})
	}
	shell := func(reply nativeShellReply) nativeShellTransport {
		return func(op, p string) nativeShellReply {
			ops = append(ops, op)
			_ = json.Unmarshal([]byte(p), &payload)
			return reply
		}
	}

	// Posted: Ok, with the title and body on the wire.
	r := nativeNotifyVia(shell(nativeShellReply{Present: true, Ok: true}), web, "Sky", "Your order shipped")
	if r.Tag != 0 {
		t.Fatalf("posted: %+v", r)
	}
	if len(ops) != 1 || ops[0] != nativeOpNotify ||
		payload["title"] != "Sky" || payload["body"] != "Your order shipped" {
		t.Fatalf("wire: ops %v payload %v", ops, payload)
	}

	// The user refused the prompt: PermissionDenied, and no Web fallback.
	r = nativeNotifyVia(shell(nativeShellReply{Present: true,
		Data: "denied: the user did not allow notifications"}), web, "t", "b")
	if kind, _ := errKind(t, r); kind != kindPermissionDenied {
		t.Errorf("denied: kind %d, want PermissionDenied", kind)
	}
	// Posting failed: an I/O error.
	r = nativeNotifyVia(shell(nativeShellReply{Present: true,
		Data: "failed: the notification service refused it"}), web, "t", "b")
	if kind, _ := errKind(t, r); kind != kindIo {
		t.Errorf("failed: kind %d, want Io", kind)
	}
	if webCalls != 0 {
		t.Fatalf("a shell's answer must not fall back to the Web API (%d calls)", webCalls)
	}

	// No shell (a browser) and a shell with no native notifications (the
	// macOS desktop window): the Web Notification API.
	nativeNotifyVia(func(string, string) nativeShellReply { return nativeShellReply{} }, web, "t", "b")
	nativeNotifyVia(func(op, p string) nativeShellReply {
		_, err := nativeShellDispatch(nil, nil, op, p)
		return nativeShellReply{Present: true, Ok: err == nil, Data: fmt.Sprint(err)}
	}, web, "t", "b")
	if webCalls != 2 {
		t.Errorf("no native notifications: %d Web API calls, want 2", webCalls)
	}
}

// E-16: an iOS or Android shell built by v0.26.1 does not know the
// `sky:notify` op ("skyNative: no native handler for 'sky:notify'" on iOS,
// "no native handler for 'sky:notify'" on Android), but it does have the old
// notify entry point. A v0.27 wasm loaded from the backend into such a shell
// falls back to that entry point instead of failing.
func TestNativeNotifyFallsBackToAShellBuiltBeforeV027(t *testing.T) {
	webCalls, legacyCalls := 0, 0
	web := func() SkyResult[any, any] { webCalls++; return Ok[any, any](struct{}{}) }
	legacy := func(title, body string) (SkyResult[any, any], bool) {
		legacyCalls++
		if title != "Sky" || body != "shipped" {
			t.Fatalf("legacy notify got %q %q", title, body)
		}
		return Ok[any, any](struct{}{}), true
	}
	for _, rej := range []string{
		"skyNative: no native handler for 'sky:notify'",
		"no native handler for 'sky:notify'",
	} {
		old := func(string, string) nativeShellReply {
			return nativeShellReply{Present: true, Ok: false, Data: rej}
		}
		r := nativeNotifyWith(old, legacy, web, "Sky", "shipped")
		if r.Tag != 0 {
			t.Fatalf("%q: a v0.26.1 shell's notify failed: %+v", rej, r)
		}
	}
	if legacyCalls != 2 || webCalls != 0 {
		t.Fatalf("legacy calls %d, web calls %d; want 2 and 0", legacyCalls, webCalls)
	}
	// A shell with neither protocol: a named Err that says to update the app.
	none := func(string, string) (SkyResult[any, any], bool) { return SkyResult[any, any]{}, false }
	old := func(string, string) nativeShellReply {
		return nativeShellReply{Present: true, Data: "no native handler for 'sky:notify'"}
	}
	r := nativeNotifyWith(old, none, web, "t", "b")
	if _, msg := errKind(t, r); !strings.Contains(msg, "update the app") {
		t.Fatalf("an old shell with no notify: %q, want a message to update the app", msg)
	}
}
