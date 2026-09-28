//go:build !js

package rt

// A Json `Value` kept in a Sky.Live model must survive the session store.
//
// `Json.Decode.value` (v0.27) makes it natural to keep a raw JSON tree in a
// model. rt.JsonValue carries that tree in an UNEXPORTED field, and gob
// refuses a struct with no exported fields, so encodeSession failed: a
// DB-backed store (sqlite / postgres / redis) silently fell back to the
// in-process copy, and the session was lost on the next restart or on another
// replica. These tests push a model holding Values through every store's real
// serialisation path and read it back from a store instance with a cold cache.
//
// The same file pins the other kernel values a model can hold whose Go shape
// has unexported fields: Decimal (and so Money) must round-trip; a Secret must
// NEVER be written, in clear or otherwise.

import (
	"bytes"
	"encoding/gob"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// jvSessModel stands for a compiled record-alias Model. A Sky `Json.Value`
// field lowers to `any` (goty.rs), so the Value sits behind an interface.
type jvSessModel struct {
	Note    string
	Decoded any
	Built   any
	Raw     any
	Price   any
}

const jvDecodedText = `{"big":9007199254740993,"dec":0.1000000000000000055511151231257827,"list":[1,true,null,"s",{"k":2.50}]}`

func jvTestModel(t *testing.T) jvSessModel {
	t.Helper()
	decoded := decodeOK(t, JsonDec_value(), jvDecodedText)
	built := JsonEnc_object([]any{
		SkyTuple2{V0: "zeta", V1: JsonEnc_int(1)},
		SkyTuple2{V0: "alpha", V1: JsonEnc_list([]any{JsonEnc_string("x"), JsonEnc_float(2.5), JsonEnc_null()})},
		SkyTuple2{V0: "mid", V1: JsonEnc_bool(true)},
	})
	rawRes := JsonEnc_raw(`{"z":1,"a":[2,3]}`).(SkyResult[any, any])
	if rawRes.Tag != 0 {
		t.Fatalf("Encode.raw: %v", rawRes.ErrValue)
	}
	d, _ := decimal.NewFromString("12345.6789")
	money := SkyADT{Tag: 0, SkyName: "Money", Fields: []any{decimalBox(d), SkyADT{Tag: 2, SkyName: "GBP"}}}
	return jvSessModel{Note: "n", Decoded: decoded, Built: built, Raw: rawRes.OkValue, Price: money}
}

// jvWant is what each Value must re-encode to after the round trip: the
// exact document, with object keys in the order the builder gave them.
var jvWant = map[string]string{
	"Decoded": jvDecodedText,
	"Built":   `{"zeta":1,"alpha":["x",2.5,null],"mid":true}`,
	"Raw":     `{"z":1,"a":[2,3]}`,
}

func assertJvModel(t *testing.T, got any) {
	t.Helper()
	m, ok := got.(jvSessModel)
	if !ok {
		t.Fatalf("restored model is %T, want jvSessModel", got)
	}
	for name, v := range map[string]any{"Decoded": m.Decoded, "Built": m.Built, "Raw": m.Raw} {
		if _, isJV := v.(JsonValue); !isJV {
			t.Fatalf("%s restored as %T, want JsonValue", name, v)
		}
		if enc := AsString(JsonEnc_encode(0, v)); enc != jvWant[name] {
			t.Fatalf("%s changed in the session store:\n want %s\n  got %s", name, jvWant[name], enc)
		}
	}
	// A restored Value still works with the decoders.
	big := decodeOK(t, JsonDec_field("big", JsonDec_int()), AsString(JsonEnc_encode(0, m.Decoded)))
	if big != 9007199254740993 {
		t.Fatalf("big restored as %v", big)
	}
	money, ok := m.Price.(SkyADT)
	if !ok || money.SkyName != "Money" || len(money.Fields) != 2 {
		t.Fatalf("Money restored as %#v", m.Price)
	}
	if amt := decimalUnbox(money.Fields[0]); amt.String() != "12345.6789" {
		t.Fatalf("Money amount restored as %s", amt.String())
	}
	if cur, _ := money.Fields[1].(SkyADT); cur.SkyName != "GBP" {
		t.Fatalf("Money currency restored as %#v", money.Fields[1])
	}
}

// TestJsonValue_SessionEncodeRoundTrip — the codec itself (every DB-backed
// store calls encodeSession / decodeSession).
func TestJsonValue_SessionEncodeRoundTrip(t *testing.T) {
	blob, err := encodeSession(buildSess(jvTestModel(t)))
	if err != nil {
		t.Fatalf("encodeSession refused a model holding Json Values: %v", err)
	}
	sess, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decodeSession: %v", err)
	}
	assertJvModel(t, sess.model)
}

// TestJsonValue_MemoryStoreKeepsValue — the memory store keeps the model by
// reference (no serialisation), so the Value is the same one.
func TestJsonValue_MemoryStoreKeepsValue(t *testing.T) {
	store := newMemoryStore(time.Minute)
	defer store.Close()
	store.Set("sid-jv", buildSess(jvTestModel(t)))
	sess, ok := store.Get("sid-jv")
	if !ok {
		t.Fatal("memory store lost the session")
	}
	assertJvModel(t, sess.model)
}

// TestJsonValue_SqliteStoreSurvivesRestart — write with one store instance,
// read with a second one on the same file (a restarted process: cold cache,
// so Get must decode from disk).
func TestJsonValue_SqliteStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	writer, err := newSQLiteStore(path, time.Hour, 0)
	if err != nil {
		t.Fatalf("newSQLiteStore: %v", err)
	}
	writer.Set("sid-jv", buildSess(jvTestModel(t)))
	_ = writer.Close()

	reader, err := newSQLiteStore(path, time.Hour, 0)
	if err != nil {
		t.Fatalf("newSQLiteStore (reader): %v", err)
	}
	defer reader.Close()
	sess, ok := reader.Get("sid-jv")
	if !ok {
		t.Fatal("the session was not persisted: a restarted process cannot find it")
	}
	assertJvModel(t, sess.model)
}

// TestJsonValue_RedisStoreRoundTrip — through Redis (miniredis) with the
// in-process cache cleared, so Get decodes the stored blob.
func TestJsonValue_RedisStoreRoundTrip(t *testing.T) {
	store, _ := withRedis(t)
	store.Set("sid-jv", buildSess(jvTestModel(t)))
	store.memMu.Lock()
	delete(store.memCache, "sid-jv")
	store.memMu.Unlock()
	sess, ok := store.Get("sid-jv")
	if !ok {
		t.Fatal("the session was not persisted to Redis")
	}
	assertJvModel(t, sess.model)
}

// TestJsonValue_PostgresStoreCrossInstance — a second instance (cold cache)
// reads what the first wrote. Needs SKY_TEST_POSTGRES_DSN, like the other
// real-Postgres store tests (live_store_postgres_test.go, which is behind the
// `integration` tag; this one is not, so it runs whenever the DSN is set).
func TestJsonValue_PostgresStoreCrossInstance(t *testing.T) {
	dsn := os.Getenv("SKY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SKY_TEST_POSTGRES_DSN unset — skipping real-Postgres integration test")
	}
	writer, err := newPostgresStore(dsn, time.Hour, 0)
	if err != nil {
		t.Fatalf("newPostgresStore: %v", err)
	}
	writer.Delete("sid-jv-pg")
	writer.Set("sid-jv-pg", buildSess(jvTestModel(t)))
	defer writer.Close()
	reader, err := newPostgresStore(dsn, time.Hour, 0)
	if err != nil {
		t.Fatalf("newPostgresStore (reader): %v", err)
	}
	defer reader.Close()
	sess, ok := reader.Get("sid-jv-pg")
	if !ok {
		t.Fatal("the session was not persisted to Postgres")
	}
	assertJvModel(t, sess.model)
	reader.Delete("sid-jv-pg")
}

// TestSecret_NeverWrittenToASessionStore — a Secret in a model is refused by
// the session encoder with an error that names it and its path, and the
// secret text appears in no encoded form. The DB-backed stores then keep the
// session in process only (the documented fallback), which is the safe
// failure: a secret written to a store would sit there in clear.
func TestSecret_NeverWrittenToASessionStore(t *testing.T) {
	const plain = "s3cr3t-must-not-leak-0123456789"
	model := jvSessModel{Note: "n", Price: Secret{v: plain}}
	blob, err := encodeSession(buildSess(model))
	if err == nil {
		t.Fatalf("encodeSession accepted a Secret (%d bytes)", len(blob))
	}
	if !strings.Contains(err.Error(), "Secret") || !strings.Contains(err.Error(), "model.Price") {
		t.Fatalf("error should name the Secret and its path, got: %v", err)
	}
	if bytes.Contains(blob, []byte(plain)) {
		t.Fatal("the secret text reached the encoded blob")
	}
	// Defence in depth: gob itself refuses a Secret, whatever path reaches it
	// (the Redis pub/sub broker gob-encodes payloads too).
	var buf bytes.Buffer
	if gobErr := gobEncodeForTest(&buf, Secret{v: plain}); gobErr == nil {
		t.Fatal("gob encoded a Secret")
	}
	if bytes.Contains(buf.Bytes(), []byte(plain)) {
		t.Fatal("the secret text reached a gob stream")
	}

	// sqlite: the session stays usable in process, and nothing is on disk.
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := newSQLiteStore(path, time.Hour, 0)
	if err != nil {
		t.Fatalf("newSQLiteStore: %v", err)
	}
	store.Set("sid-secret", buildSess(model))
	if _, ok := store.Get("sid-secret"); !ok {
		t.Fatal("the in-process fallback lost the session")
	}
	_ = store.Close()
	disk, _ := os.ReadFile(path)
	if bytes.Contains(disk, []byte(plain)) {
		t.Fatal("the secret text was written to the sqlite file")
	}
}

func gobEncodeForTest(w io.Writer, v any) error {
	return gob.NewEncoder(w).Encode(v)
}

// TestJsonValueRestartHelper is the re-exec target of
// TestJsonValue_DecodesInAFreshProcess. It does nothing in a normal run.
func TestJsonValueRestartHelper(t *testing.T) {
	path := os.Getenv("SKY_JV_RESTART_FILE")
	if path == "" {
		return
	}
	// A fresh process: nothing has been encoded here, so decoding works only
	// if the runtime registers these kernel value types at boot.
	RegisterSkyGobTypes([]any{jvSessModel{}})
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	sess, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("DECODE_ERR: %v", err)
	}
	assertJvModel(t, sess.model)
	t.Log("JV_DECODE_OK")
}

// TestJsonValue_DecodesInAFreshProcess — the restart case the in-process
// tests cannot reach: gob's name-to-type registry is per process, and the
// codegen boot list (RegisterSkyGobTypes) names only Sky-minted types. A
// restarted process must still know JsonValue and decimal.Decimal.
func TestJsonValue_DecodesInAFreshProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary; skipped under -short")
	}
	blob, err := encodeSession(buildSess(jvTestModel(t)))
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	path := filepath.Join(t.TempDir(), "session.gob")
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestJsonValueRestartHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "SKY_JV_RESTART_FILE="+path)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "JV_DECODE_OK") {
		t.Fatalf("a fresh process could not restore the session: %v\n%s", err, out)
	}
}
