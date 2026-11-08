//go:build !js

package rt

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"
)

type msgLogJSONCapture struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *msgLogJSONCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (w *msgLogJSONCapture) owned(t *testing.T, marker string) []byte {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var found [][]byte
	for _, p := range w.writes {
		if bytes.Contains(p, []byte(marker)) {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("owned record writes=%d, want1", len(found))
	}
	return append([]byte(nil), found[0]...)
}

type msgLogJSONError struct {
	text  string
	calls int
}

func (e *msgLogJSONError) Error() string { e.calls++; return e.text }

type msgLogJSONFailWriter struct{ calls int }

func (w *msgLogJSONFailWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("public writer failure")
}

func TestMsgLogServerlessJSONRecords(t *testing.T) {
	cases := []struct {
		label, marker, name, request, description string
		lifecycle                                 bool
	}{
		{"Plain", "MsgLogJSONPlainFixture", "MsgLogJSONPlainFixture", "public-request", "public error", false},
		{"EscapedError", "MsgLogJSONErrorFixture", "MsgLogJSONErrorFixture", "public-request", "bad \"value\"\nnext\\path\tend", true},
		{"EscapedMetadata", "MsgLogJSONMetadataFixture", "MsgLogJSONMetadataFixture\"\nname<&>", "public\"request\nnext\\path", "public error", false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			withServerlessEnv(t, map[string]string{"K_SERVICE": "public-json-fixture"})
			capture := &msgLogJSONCapture{}
			prior := serverlessStderr
			serverlessStderr = func() io.Writer { return capture }
			t.Cleanup(func() { serverlessStderr = prior })
			var message any = SkyADT{SkyName: tc.name}
			if tc.lifecycle {
				message = Live_lifecycle(message)
			}
			model := map[string]any{"x": 0}
			ctx := BeginMsgLog(message, model)
			ctx.TraceID = tc.request
			failure := &msgLogJSONError{text: tc.description}
			ObserveMsgLog(ctx, model, cmdT{kind: "none"}, failure)
			if failure.calls != 1 {
				t.Fatalf("error formatter calls=%d, want1", failure.calls)
			}
			raw := capture.owned(t, tc.marker)
			if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 {
				t.Error("owned record must contain exactly one final LF")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal("actual serverless record is not valid JSON")
			}
			wantKeys := []string{"ts", "level", "msg", "req_id", "name", "noop", "lifecycle", "latency_ms", "error"}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
				t.Fatal("record object missing")
			}
			var keys []string
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					t.Fatal("record key decode failed")
				}
				name, ok := key.(string)
				if !ok {
					t.Fatal("record key is not a string")
				}
				keys = append(keys, name)
				var value json.RawMessage
				if decoder.Decode(&value) != nil {
					t.Fatal("record value decode failed")
				}
			}
			if len(keys) != len(wantKeys) || len(fields) != len(wantKeys) {
				t.Fatal("record field count changed")
			}
			for i, key := range wantKeys {
				if keys[i] != key {
					t.Fatal("record field order changed")
				}
			}
			stringsWant := map[string]string{"level": "error", "msg": "msg_dispatch", "req_id": tc.request, "name": tc.name, "error": tc.description}
			for key, want := range stringsWant {
				var got string
				if json.Unmarshal(fields[key], &got) != nil || got != want {
					t.Fatal("record string did not round-trip")
				}
			}
			var stamp string
			if json.Unmarshal(fields["ts"], &stamp) != nil {
				t.Fatal("timestamp type changed")
			}
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				t.Fatal("timestamp format changed")
			}
			for key, want := range map[string]bool{"noop": false, "lifecycle": tc.lifecycle} {
				var got bool
				if json.Unmarshal(fields[key], &got) != nil || got != want {
					t.Fatal("record boolean did not round-trip")
				}
			}
			number := string(fields["latency_ms"])
			value, err := strconv.ParseFloat(number, 64)
			dot := bytes.LastIndexByte([]byte(number), '.')
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || dot < 1 || len(number)-dot-1 != 3 {
				t.Fatal("latency must remain a finite nonnegative three-decimal JSON number")
			}
		})
	}
}

func TestMsgLogServerlessJSONWriteFailureIsBestEffort(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_SERVICE": "public-json-fixture"})
	writer := &msgLogJSONFailWriter{}
	prior := serverlessStderr
	serverlessStderr = func() io.Writer { return writer }
	t.Cleanup(func() { serverlessStderr = prior })
	model := map[string]any{"x": 0}
	ctx := BeginMsgLog(SkyADT{SkyName: "MsgLogJSONWriterFixture"}, model)
	ctx.TraceID = "public-request"
	ObserveMsgLog(ctx, model, cmdT{kind: "none"}, errors.New("public error"))
	if writer.calls != 1 {
		t.Fatalf("best-effort writes=%d, want1", writer.calls)
	}
}
