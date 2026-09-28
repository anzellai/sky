package rt

import (
	"encoding/json"
	"errors"
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
