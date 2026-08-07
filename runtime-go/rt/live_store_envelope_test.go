package rt

// Phase-5c (grill A3/A4/A6): the session-blob VERSION ENVELOPE turns a stale or
// cross-version read into a CLEAN, logged reset — never a corrupt-decode. These
// tests encode the grill's exact attack scenarios as standing regressions.

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"os"
	"testing"
	"time"
)

func TestSessionEnvelope_RoundTripSameVersion(t *testing.T) {
	os.Unsetenv("SKY_DATA_SESSION_VERSION") // default schema version = 1
	model := map[string]any{"role": 0, "name": "alice"}
	blob, err := encodeSession(buildSess(model))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The envelope is present so ANY binary can gate on it without gob-decoding.
	if len(blob) < 8 || !bytes.Equal(blob[0:4], sessionEnvelopeMagic[:]) {
		t.Fatalf("encoded blob missing version-envelope magic: %x", blob)
	}
	if got := binary.BigEndian.Uint32(blob[4:8]); got != 1 {
		t.Fatalf("envelope version = %d, want 1", got)
	}
	sess, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decode at the same version should succeed: %v", err)
	}
	m, ok := sess.model.(map[string]any)
	if !ok || m["name"] != "alice" {
		t.Fatalf("model did not round-trip: %#v", sess.model)
	}
}

// A4 (priv-esc) + A3 (rolling-deploy): a v1 blob where role=0 means "guest",
// read after the app bumps sessionVersion to 2 (where 0 now means "admin" — a
// semantic int remap gob CANNOT see), MUST be refused. The stale bytes are never
// decoded into the new meaning. The same mechanism protects the rolling-deploy
// window (an old blob read by the new binary, or vice-versa).
func TestSessionEnvelope_VersionMismatchRefusesNeverCorrupts(t *testing.T) {
	os.Unsetenv("SKY_DATA_SESSION_VERSION") // encode at v1
	guestV1 := map[string]any{"role": 0}    // 0 == guest under v1
	blob, err := encodeSession(buildSess(guestV1))
	if err != nil {
		t.Fatalf("encode v1: %v", err)
	}
	// The app now ships v2 (role's int meaning was remapped — gob-invisible).
	os.Setenv("SKY_DATA_SESSION_VERSION", "2")
	defer os.Unsetenv("SKY_DATA_SESSION_VERSION")

	sess, err := decodeSession(blob)
	if !errors.Is(err, errSessionVersionMismatch) {
		t.Fatalf("a v1 blob under v2 MUST be refused with errSessionVersionMismatch "+
			"(no silent priv-esc); got sess=%#v err=%v", sess, err)
	}
	if sess != nil {
		t.Fatalf("a refused session must be nil (clean reset), got %#v", sess)
	}
}

// The reverse direction also refuses (rolling deploy: an OLD binary at v1 reading
// a NEW v2 blob) — never a corrupt-decode of a newer shape.
func TestSessionEnvelope_OldReaderRefusesNewerBlob(t *testing.T) {
	os.Setenv("SKY_DATA_SESSION_VERSION", "2")
	blobV2, err := encodeSession(buildSess(map[string]any{"role": 1}))
	if err != nil {
		t.Fatalf("encode v2: %v", err)
	}
	os.Unsetenv("SKY_DATA_SESSION_VERSION") // this binary is v1
	sess, err := decodeSession(blobV2)
	if !errors.Is(err, errSessionVersionMismatch) || sess != nil {
		t.Fatalf("a v1 binary reading a v2 blob must reset, not decode; got sess=%#v err=%v", sess, err)
	}
}

// Backward-compat: a legacy (pre-5c) blob with NO envelope still decodes on the
// v0 path, so the envelope's introduction is itself non-breaking.
func TestSessionEnvelope_LegacyBlobDecodes(t *testing.T) {
	os.Unsetenv("SKY_DATA_SESSION_VERSION")
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(storableSession{
		Model:    map[string]any{"name": "legacy"},
		LastSeen: time.Now(),
	}); err != nil {
		t.Fatalf("build legacy blob: %v", err)
	}
	legacy := buf.Bytes()
	// A legacy gob stream must not accidentally start with the envelope magic.
	if len(legacy) >= 4 && bytes.Equal(legacy[0:4], sessionEnvelopeMagic[:]) {
		t.Fatalf("legacy gob blob unexpectedly starts with the envelope magic")
	}
	sess, err := decodeSession(legacy)
	if err != nil {
		t.Fatalf("legacy blob should decode on the v0 path: %v", err)
	}
	if m, ok := sess.model.(map[string]any); !ok || m["name"] != "legacy" {
		t.Fatalf("legacy model did not round-trip: %#v", sess.model)
	}
}
