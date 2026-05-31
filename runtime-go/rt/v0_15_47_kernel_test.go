package rt

// Runtime tests for v0.15.47 stdlib quality-of-life batch (#380).
// Focused unit coverage for each kernel — Compression / Cache / Csv /
// Config / Random / Email (DRY_RUN). The pipeline-level coverage
// (Sky source → emitted Go → run) is in the example sweep and the
// Sky.Test fixtures under tests/Std/ and tests/Sky/Core/.

import (
	"os"
	"strings"
	"testing"
)

// ──────────────────── Compression ────────────────────

func TestCompressionGzipRoundTrip(t *testing.T) {
	in := "hello compression — round trip me"
	taskC := Compression_gzip(in).(func() any)
	rc := taskC()
	rcRes, ok := rc.(SkyResult[any, any])
	if !ok || rcRes.Tag != 0 {
		t.Fatalf("gzip failed: %v", rc)
	}
	compressed := rcRes.OkValue.(string)
	if compressed == in {
		t.Fatalf("gzip didn't compress: output == input")
	}

	taskD := Compression_gunzip(compressed).(func() any)
	rd := taskD()
	rdRes, ok := rd.(SkyResult[any, any])
	if !ok || rdRes.Tag != 0 {
		t.Fatalf("gunzip failed: %v", rd)
	}
	out := rdRes.OkValue.(string)
	if out != in {
		t.Fatalf("gunzip mismatch: got %q want %q", out, in)
	}
}

func TestCompressionZstdRoundTrip(t *testing.T) {
	in := strings.Repeat("Sky language stdlib batch v0.15.47 ", 50)
	taskC := Compression_zstdCompress(in).(func() any)
	rc := taskC().(SkyResult[any, any])
	if rc.Tag != 0 {
		t.Fatalf("zstdCompress failed: %v", rc.ErrValue)
	}
	compressed := rc.OkValue.(string)
	if len(compressed) >= len(in) {
		t.Logf("zstd didn't reduce size for short input: %d -> %d", len(in), len(compressed))
	}

	taskD := Compression_zstdDecompress(compressed).(func() any)
	rd := taskD().(SkyResult[any, any])
	if rd.Tag != 0 {
		t.Fatalf("zstdDecompress failed: %v", rd.ErrValue)
	}
	if rd.OkValue.(string) != in {
		t.Fatalf("zstd round-trip mismatch")
	}
}

func TestCompressionGunzipCorrupt(t *testing.T) {
	r := Compression_gunzip("not a gzip stream").(func() any)().(SkyResult[any, any])
	if r.Tag != 1 {
		t.Fatalf("gunzip should fail on corrupt input, got Ok")
	}
}

// ──────────────────── Cache ────────────────────

func TestCacheBasicGetPut(t *testing.T) {
	cfg := map[string]any{"maxEntries": 100, "ttlMs": 0, "maxBytes": 0}
	r := Cache_new(cfg).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("Cache_new failed: %v", r.ErrValue)
	}
	id := r.OkValue.(int)

	// Initial get → Nothing (miss)
	g := Cache_get(id, "alice").(func() any)().(SkyResult[any, any])
	if g.Tag != 0 {
		t.Fatalf("Cache_get failed: %v", g.ErrValue)
	}
	maybe := g.OkValue.(SkyMaybe[any])
	if maybe.Tag != 1 {
		t.Fatalf("Cache_get on empty cache: expected Nothing, got Just")
	}

	// Put → success
	p := Cache_put(id, "alice", 42).(func() any)().(SkyResult[any, any])
	if p.Tag != 0 {
		t.Fatalf("Cache_put failed: %v", p.ErrValue)
	}

	// Get → Just 42
	g2 := Cache_get(id, "alice").(func() any)().(SkyResult[any, any])
	if g2.Tag != 0 {
		t.Fatalf("Cache_get failed: %v", g2.ErrValue)
	}
	maybe2 := g2.OkValue.(SkyMaybe[any])
	if maybe2.Tag != 0 {
		t.Fatalf("Cache_get after put: expected Just, got Nothing")
	}
	if maybe2.JustValue.(int) != 42 {
		t.Fatalf("Cache_get value mismatch: got %v want 42", maybe2.JustValue)
	}
}

func TestCacheLRUEviction(t *testing.T) {
	cfg := map[string]any{"maxEntries": 2, "ttlMs": 0, "maxBytes": 0}
	r := Cache_new(cfg).(func() any)().(SkyResult[any, any])
	id := r.OkValue.(int)

	_ = Cache_put(id, "a", 1).(func() any)()
	_ = Cache_put(id, "b", 2).(func() any)()
	_ = Cache_put(id, "c", 3).(func() any)() // evicts "a"

	g := Cache_get(id, "a").(func() any)().(SkyResult[any, any])
	maybe := g.OkValue.(SkyMaybe[any])
	if maybe.Tag != 1 {
		t.Fatalf("expected 'a' evicted (Nothing), got Just")
	}

	g2 := Cache_get(id, "c").(func() any)().(SkyResult[any, any])
	maybe2 := g2.OkValue.(SkyMaybe[any])
	if maybe2.Tag != 0 || maybe2.JustValue.(int) != 3 {
		t.Fatalf("expected 'c' = Just 3, got %v", maybe2)
	}
}

func TestCacheStats(t *testing.T) {
	cfg := map[string]any{"maxEntries": 10, "ttlMs": 0, "maxBytes": 0}
	id := Cache_new(cfg).(func() any)().(SkyResult[any, any]).OkValue.(int)

	_ = Cache_put(id, "x", 1).(func() any)()
	_ = Cache_get(id, "x").(func() any)()     // hit
	_ = Cache_get(id, "miss").(func() any)()  // miss

	s := Cache_stats(id).(func() any)().(SkyResult[any, any])
	stats := s.OkValue.(map[string]any)
	if stats["hits"].(int) != 1 {
		t.Fatalf("expected 1 hit, got %v", stats["hits"])
	}
	if stats["misses"].(int) != 1 {
		t.Fatalf("expected 1 miss, got %v", stats["misses"])
	}
}

// ──────────────────── CSV ────────────────────

func TestCsvParseEncodeRoundTrip(t *testing.T) {
	src := "name,age\nAlice,30\nBob,25\n"
	r := Csv_parse(src).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("Csv_parse failed: %v", r.ErrValue)
	}
	rec := r.OkValue.(map[string]any)
	hdr := rec["header"].([]any)
	if len(hdr) != 2 || hdr[0].(string) != "name" || hdr[1].(string) != "age" {
		t.Fatalf("header mismatch: %v", hdr)
	}
	rows := rec["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("row count mismatch: %d", len(rows))
	}

	out := Csv_encode(rec).(string)
	if !strings.Contains(out, "name,age") || !strings.Contains(out, "Alice,30") {
		t.Fatalf("encode round-trip lost data: %q", out)
	}
}

func TestCsvParseWithDelimiter(t *testing.T) {
	src := "k;v\na;1\n"
	r := Csv_parseWithDelimiter(";", src).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("Csv_parseWithDelimiter failed: %v", r.ErrValue)
	}
}

// ──────────────────── Config ────────────────────

func TestConfigDecodeJsonField(t *testing.T) {
	doc := `{"host":"example.com","port":5432}`
	dec := Config_field("port", Config_int())
	r := Config_decodeJson(doc, dec).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("decodeJson failed: %v", r.ErrValue)
	}
	if r.OkValue.(int) != 5432 {
		t.Fatalf("expected 5432, got %v", r.OkValue)
	}
}

func TestConfigDecodeTomlField(t *testing.T) {
	doc := "host = \"example.com\"\nport = 5432\n"
	dec := Config_field("host", Config_string())
	r := Config_decodeToml(doc, dec).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("decodeToml failed: %v", r.ErrValue)
	}
	if r.OkValue.(string) != "example.com" {
		t.Fatalf("expected 'example.com', got %v", r.OkValue)
	}
}

func TestConfigDecodeYamlNested(t *testing.T) {
	doc := "db:\n  host: example.com\n  port: 5432\n"
	dec := Config_at([]any{"db", "host"}, Config_string())
	r := Config_decodeYaml(doc, dec).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("decodeYaml failed: %v", r.ErrValue)
	}
	if r.OkValue.(string) != "example.com" {
		t.Fatalf("expected 'example.com', got %v", r.OkValue)
	}
}

// ──────────────────── Random extensions ────────────────────

func TestRandomSeededIntDeterministic(t *testing.T) {
	a := Random_seededInt(42, 1, 100).(SkyTuple2)
	b := Random_seededInt(42, 1, 100).(SkyTuple2)
	if a.V0.(int) != b.V0.(int) {
		t.Fatalf("seededInt not deterministic: %v vs %v", a.V0, b.V0)
	}
}

func TestRandomSeededFloatRange(t *testing.T) {
	r := Random_seededFloat(42).(SkyTuple2)
	v := r.V0.(float64)
	if v < 0.0 || v >= 1.0 {
		t.Fatalf("seededFloat out of [0,1): %v", v)
	}
}

func TestRandomSeededChoiceEmpty(t *testing.T) {
	r := Random_seededChoice(42, []any{}).(SkyTuple2)
	m := r.V0.(SkyMaybe[any])
	if m.Tag != 1 {
		t.Fatalf("seededChoice on empty list should be Nothing")
	}
}

func TestRandomWeighted(t *testing.T) {
	pairs := []any{
		SkyTuple2{V0: 10.0, V1: "a"},
		SkyTuple2{V0: 0.0, V1: "skipped"},
	}
	for i := 0; i < 20; i++ {
		taskFn := Random_weighted(pairs).(func() any)
		r := taskFn().(SkyResult[any, any])
		m := r.OkValue.(SkyMaybe[any])
		if m.Tag != 0 || m.JustValue.(string) != "a" {
			t.Fatalf("weighted should pick 'a' every time (other weight is 0): got %v", m)
		}
	}
}

// ──────────────────── Email DRY_RUN ────────────────────

func TestEmailDryRunReturnsID(t *testing.T) {
	old := os.Getenv("SKY_EMAIL_DRY_RUN")
	_ = os.Setenv("SKY_EMAIL_DRY_RUN", "1")
	defer os.Setenv("SKY_EMAIL_DRY_RUN", old)

	provider := SkyADT{Tag: 0, SkyName: "Resend", Fields: []any{"test-key"}}
	msg := map[string]any{
		"from":        "noreply@example.com",
		"to":          []any{"alice@example.com"},
		"cc":          []any{},
		"bcc":         []any{},
		"subject":     "test",
		"textBody":    "hello",
		"htmlBody":    "",
		"attachments": []any{},
		"replyTo":     "",
	}
	r := Email_send(provider, msg).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("Email_send DRY_RUN failed: %v", r.ErrValue)
	}
	id := r.OkValue.(string)
	if !strings.HasPrefix(id, "dry-run-") {
		t.Fatalf("expected dry-run- prefix, got %q", id)
	}
}

// ──────────────────── String haystack-first (no runtime needed) ──────
// containsIn etc. are Sky-source aliases — coverage lives in the
// Sky.Test fixture tests/Sky/Core/StringInTest.sky.
