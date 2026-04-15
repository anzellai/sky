package rt

// Audit P2-5: session-store encoder rejects values that gob can't
// meaningfully persist. Pre-fix encoder blindly called
// gob.Encode(model); gob silently skipped funcs/chans/etc. so the
// stored bytes decoded back to a malformed model that crashed on
// first use. Post-fix: validateSessionValue walks the value graph
// and returns a clear error before any encode happens.

import (
	"strings"
	"testing"
	"time"
)

// buildSess wraps a model into the minimal liveSession shape needed
// to exercise encodeSession.
func buildSess(model any) *liveSession {
	return &liveSession{model: model, lastSeen: time.Now()}
}

func TestEncodeSession_RejectsClosureInModel(t *testing.T) {
	// Pre-fix: encoded cleanly; the closure round-tripped as nil.
	badModel := map[string]any{
		"name": "alice",
		"cb":   func() {},
	}
	_, err := encodeSession(buildSess(badModel))
	if err == nil {
		t.Fatal("encodeSession should reject a model containing a func")
	}
	if !strings.Contains(err.Error(), "func") {
		t.Fatalf("error should mention 'func': %q", err)
	}
	if !strings.Contains(err.Error(), "model[cb]") {
		t.Fatalf("error should name the offending path 'model[cb]': %q", err)
	}
}

func TestEncodeSession_RejectsChannelInModel(t *testing.T) {
	ch := make(chan int, 1)
	badModel := map[string]any{"events": ch}
	_, err := encodeSession(buildSess(badModel))
	if err == nil {
		t.Fatal("encodeSession should reject a model containing a chan")
	}
	if !strings.Contains(err.Error(), "chan") {
		t.Fatalf("error should mention 'chan': %q", err)
	}
}

func TestEncodeSession_AcceptsPlainRecord(t *testing.T) {
	// Sky's canonical shapes round-trip cleanly: primitive fields
	// + slices of primitives + nested maps.
	ok := map[string]any{
		"id":    42,
		"email": "alice@example.com",
		"roles": []any{"admin", "member"},
		"meta":  map[string]any{"created": int64(1700000000)},
	}
	if _, err := encodeSession(buildSess(ok)); err != nil {
		t.Fatalf("encodeSession should accept a plain record, got: %v", err)
	}
}

func TestEncodeSession_AcceptsSkyADTShapes(t *testing.T) {
	// SkyMaybe and SkyResult are structurally safe: Tag int +
	// typed payload that is itself session-safe.
	okMaybe := Just[any]("hello")
	if _, err := encodeSession(buildSess(okMaybe)); err != nil {
		t.Fatalf("SkyMaybe should round-trip, got: %v", err)
	}
	okResult := Ok[any, any]("payload")
	if _, err := encodeSession(buildSess(okResult)); err != nil {
		t.Fatalf("SkyResult should round-trip, got: %v", err)
	}
}

func TestEncodeSession_RoundTripModelDecodes(t *testing.T) {
	// End-to-end: encode, decode, assert key fields round-trip.
	orig := map[string]any{
		"id":    42,
		"email": "alice@example.com",
	}
	blob, err := encodeSession(buildSess(orig))
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	sess, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	decoded, ok := sess.model.(map[string]any)
	if !ok {
		t.Fatalf("decoded model wrong type: %T", sess.model)
	}
	if decoded["email"] != "alice@example.com" {
		t.Fatalf("email did not round-trip: %v", decoded["email"])
	}
}

func TestValidateSessionValue_RejectsNestedFunc(t *testing.T) {
	// Closure buried two levels deep still caught.
	v := []any{
		map[string]any{
			"field": []any{
				"string-ok",
				func(x int) int { return x + 1 },
			},
		},
	}
	err := validateSessionValue(v, "root")
	if err == nil {
		t.Fatal("nested func must be rejected")
	}
	if !strings.Contains(err.Error(), "func") {
		t.Fatalf("error should mention func: %q", err)
	}
}

func TestValidateSessionValue_AcceptsTypedNil(t *testing.T) {
	// A typed-nil pointer/interface in the model is semantically
	// "no value" and should round-trip unchanged, not trip the
	// validator on reflect.Ptr before checking IsNil.
	var nilPtr *int
	v := map[string]any{"maybe": nilPtr}
	if err := validateSessionValue(v, "root"); err != nil {
		t.Fatalf("typed-nil pointer should be accepted: %v", err)
	}
}
