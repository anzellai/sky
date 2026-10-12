//go:build !js

package rt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDatasetFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// readAll drives Ai_datasetReadLines the way Std.Ai.Dataset does: chunk after
// chunk from the returned offset until done.
func readAll(t *testing.T, path string, chunk int) ([]string, int) {
	t.Helper()
	var all []string
	offset, calls := 0, 0
	for {
		calls++
		if calls > 1000 {
			t.Fatal("ReadLines never reported done")
		}
		r := Ai_datasetReadLines(path, offset, chunk).(func() any)()
		if !isOkResult(r) {
			t.Fatalf("ReadLines failed: %v", r)
		}
		m := okValue(r).(map[string]any)
		for _, l := range m["lines"].([]any) {
			all = append(all, l.(string))
		}
		offset = m["next"].(int)
		if m["done"].(bool) {
			return all, calls
		}
	}
}

func TestAiDatasetReadLinesChunksAFileWithoutLosingALine(t *testing.T) {
	body := "{\"a\":1}\n{\"a\":2}\r\n\n{\"a\":3}\n{\"a\":4}"
	p := writeDatasetFile(t, body)
	for _, chunk := range []int{1, 2, 3, 10} {
		got, _ := readAll(t, p, chunk)
		want := []string{`{"a":1}`, `{"a":2}`, ``, `{"a":3}`, `{"a":4}`}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("chunk %d: lines %q, want %q", chunk, got, want)
		}
	}
	// A file that ends with a newline is done with its last line: no extra
	// empty read and no phantom empty row.
	p2 := writeDatasetFile(t, "x\ny\n")
	got, calls := readAll(t, p2, 2)
	if strings.Join(got, "|") != "x|y" || calls != 1 {
		t.Errorf("lines %q in %d calls, want x|y in 1", got, calls)
	}
}

func TestAiDatasetReadLinesFailsTypedForAMissingFile(t *testing.T) {
	r := Ai_datasetReadLines(filepath.Join(t.TempDir(), "absent.jsonl"), 0, 10).(func() any)()
	if isOkResult(r) {
		t.Fatal("a missing file read as Ok")
	}
	df, ok := DomainOf(errValue(r))
	if !ok || df.Domain != "ai" || df.Code != "DatasetInvalid" {
		t.Fatalf("domain failure %+v (ok %v), want ai/DatasetInvalid", df, ok)
	}
	if d, _ := df.Attr("dataset"); !strings.HasSuffix(d, "absent.jsonl") {
		t.Errorf("dataset attribute %q does not name the file", d)
	}
}

func TestAiDatasetCanonicalSortsKeysAndDropsSpacing(t *testing.T) {
	cases := map[string]string{
		`{ "b": 1, "a": {"z": [1, 2.50, "x"], "y": null}, "c": true }`: `{"a":{"y":null,"z":[1,2.50,"x"]},"b":1,"c":true}`,
		`{"html":"<a href=\"x\">&</a>","uni":"café"}`:                  `{"html":"<a href=\"x\">&</a>","uni":"café"}`,
		`  "plain"  `:          `"plain"`,
		`12345678901234567890`: `12345678901234567890`,
	}
	for in, want := range cases {
		r := Ai_datasetCanonical(in)
		if !isOkResult(r) {
			t.Fatalf("%s: %v", in, r)
		}
		if got := okValue(r).(string); got != want {
			t.Errorf("canonical(%s) = %s, want %s", in, got, want)
		}
	}
	// Two spellings of one value give one canonical form.
	a := okValue(Ai_datasetCanonical(`{"x":1,"y":[true]}`))
	b := okValue(Ai_datasetCanonical("{\n  \"y\" : [ true ],\n  \"x\" : 1\n}"))
	if a != b {
		t.Errorf("%v != %v", a, b)
	}
}

func TestAiDatasetCanonicalRejectsWithoutEchoingTheRow(t *testing.T) {
	for _, in := range []string{`{"email":"ada@example.com"`, `{"a":1} {"b":2}`, `secret-token-123`} {
		r := Ai_datasetCanonical(in)
		if isOkResult(r) {
			t.Fatalf("%q canonicalised", in)
		}
		df, ok := DomainOf(errValue(r))
		if !ok || df.Code != "DatasetInvalid" {
			t.Fatalf("%q: %+v", in, df)
		}
		msg := errValue(r).(skyErrorAdt).Fields[1].(skyErrorInfo).Message
		for _, leak := range []string{"ada@example.com", "secret-token", `"b"`} {
			if strings.Contains(msg, leak) {
				t.Errorf("the failure for %q repeats row text %q: %s", in, leak, msg)
			}
		}
	}
}

func TestAiDatasetSha256FileIsTheDigestOfTheBytes(t *testing.T) {
	body := strings.Repeat("{\"row\":\"abcdefghij\"}\n", 50000) // about 1 MB, streamed
	p := writeDatasetFile(t, body)
	r := Ai_datasetSha256File(p).(func() any)()
	if !isOkResult(r) {
		t.Fatal(r)
	}
	sum := sha256.Sum256([]byte(body))
	if got, want := okValue(r).(string), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("digest %s, want %s", got, want)
	}
	r = Ai_datasetSha256File(filepath.Join(t.TempDir(), "none")).(func() any)()
	if df, ok := DomainOf(errValue(r)); !ok || df.Code != "DatasetInvalid" {
		t.Errorf("missing file: %+v", df)
	}
}

func TestAiDatasetReadLinesRefusesAnOverlongLine(t *testing.T) {
	p := writeDatasetFile(t, strings.Repeat("x", aiDatasetMaxLine+10)+"\n")
	r := Ai_datasetReadLines(p, 0, 5).(func() any)()
	if df, ok := DomainOf(errValue(r)); !ok || df.Code != "DatasetInvalid" {
		t.Fatalf("an overlong line: %v", r)
	}
}

// errValue is the Err payload of a Result (a Task already run).
func errValue(r any) any {
	sr, _ := r.(SkyResult[any, any])
	return sr.ErrValue
}
