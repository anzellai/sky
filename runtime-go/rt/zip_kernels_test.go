package rt

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// zipRaw builds an archive with archive/zip directly, so a test can make
// what Zip.write refuses (unsafe names, duplicates, symlinks, odd methods).
func zipRaw(t *testing.T, build func(w *zip.Writer)) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func zipAdd(t *testing.T, w *zip.Writer, fh *zip.FileHeader, data []byte) {
	t.Helper()
	fw, err := w.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
}

func zipCodeOf(t *testing.T, v any) string {
	t.Helper()
	r := v.(SkyResult[any, any])
	if r.Tag == 0 {
		return ""
	}
	df, ok := DomainOf(r.ErrValue)
	if !ok || df.Domain != "zip" {
		t.Fatalf("not a zip domain error: %+v", r.ErrValue)
	}
	return df.Code
}

func zipReadDefault(data string) any {
	l := zipDefaultLimits
	return Zip_read(l.maxEntries, int(l.maxEntryBytes), int(l.maxTotalBytes), int(l.maxRatio), data)
}

func TestZipWriteReadRoundTrip(t *testing.T) {
	entries := []any{
		map[string]any{"path": "a.csv", "bytes": "id,name\n1,Ada\n"},
		map[string]any{"path": "dir/", "bytes": ""},
		map[string]any{"path": "dir/b.bin", "bytes": "\x00\xff\x01binary"},
	}
	for _, deflate := range []bool{true, false} {
		w := Zip_write(deflate, entries).(SkyResult[any, any])
		if w.Tag != 0 {
			t.Fatalf("write: %+v", w.ErrValue)
		}
		r := zipReadDefault(w.OkValue.(string)).(SkyResult[any, any])
		if r.Tag != 0 {
			t.Fatalf("read: %+v", r.ErrValue)
		}
		got := r.OkValue.([]any)
		if len(got) != 2 {
			t.Fatalf("read returns the two files (not the directory): %v", got)
		}
		if got[1].(map[string]any)["bytes"] != "\x00\xff\x01binary" || got[1].(map[string]any)["path"] != "dir/b.bin" {
			t.Fatalf("binary entry: %v", got[1])
		}
		l := Zip_list(w.OkValue).(SkyResult[any, any])
		infos := l.OkValue.([]any)
		if len(infos) != 3 || infos[1].(map[string]any)["isDirectory"] != true || infos[0].(map[string]any)["size"] != 14 {
			t.Fatalf("list: %v", infos)
		}
		if infos[0].(map[string]any)["modified"] != 315532800000 {
			t.Fatalf("fixed time: %v", infos[0].(map[string]any)["modified"])
		}
	}
}

func TestZipWriteIsByteIdenticalAcrossRuns(t *testing.T) {
	entries := []any{
		map[string]any{"path": "x.txt", "bytes": strings.Repeat("hello ", 1000)},
		map[string]any{"path": "y.txt", "bytes": "y"},
	}
	a := Zip_write(true, entries).(SkyResult[any, any]).OkValue
	b := Zip_write(true, entries).(SkyResult[any, any]).OkValue
	if a != b {
		t.Fatal("two writes of the same entries differ")
	}
}

func TestZipRefusesBombAndRatio(t *testing.T) {
	// One entry of 64 MiB of zeros: the ratio limit stops it early.
	bomb := zipRaw(t, func(w *zip.Writer) {
		zipAdd(t, w, &zip.FileHeader{Name: "zeros", Method: zip.Deflate}, make([]byte, 64<<20))
	})
	if c := zipCodeOf(t, zipReadDefault(bomb)); c != zipCodeRatioExceeded {
		t.Fatalf("bomb: got %q, want ratio_exceeded", c)
	}
	// With a high ratio the entry limit stops it.
	if c := zipCodeOf(t, Zip_read(10, 1<<20, 1<<30, 1_000_000, bomb)); c != zipCodeEntryTooLarge {
		t.Fatalf("entry limit: got %q", c)
	}
	// 42.zip style: many entries, each under the entry limit, over the total.
	many := zipRaw(t, func(w *zip.Writer) {
		for i := 0; i < 16; i++ {
			zipAdd(t, w, &zip.FileHeader{Name: "z" + string(rune('a'+i)), Method: zip.Deflate}, make([]byte, 1<<20))
		}
	})
	if c := zipCodeOf(t, Zip_read(100, 2<<20, 8<<20, 1_000_000, many)); c != zipCodeTotalTooLarge {
		t.Fatalf("total: got %q", c)
	}
	// A nested archive is returned as bytes, never opened.
	nested := zipRaw(t, func(w *zip.Writer) {
		zipAdd(t, w, &zip.FileHeader{Name: "inner.zip", Method: zip.Store}, []byte(many))
	})
	if c := zipCodeOf(t, zipReadDefault(nested)); c != "" {
		t.Fatalf("nested: got %q", c)
	}
	// A small entry may compress well (the ratio applies above 1 MiB).
	small := zipRaw(t, func(w *zip.Writer) {
		zipAdd(t, w, &zip.FileHeader{Name: "z", Method: zip.Deflate}, make([]byte, 1<<19))
	})
	if c := zipCodeOf(t, zipReadDefault(small)); c != "" {
		t.Fatalf("small: got %q", c)
	}
	if c := zipCodeOf(t, Zip_read(1, 1, 1, 1, many)); c != zipCodeTooManyEntries {
		t.Fatalf("entries: got %q", c)
	}
}

func TestZipRefusesUnsafePaths(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "a/../../b", "/etc/passwd", "C:/x", "c:x", "a\\b", "a\x00b", ""} {
		data := zipRaw(t, func(w *zip.Writer) {
			zipAdd(t, w, &zip.FileHeader{Name: name, Method: zip.Store}, []byte("x"))
		})
		if c := zipCodeOf(t, zipReadDefault(data)); c != zipCodeUnsafePath {
			t.Errorf("%q: got %q, want unsafe_path", name, c)
		}
		if c := zipCodeOf(t, Zip_list(data)); c != zipCodeUnsafePath {
			t.Errorf("list %q: got %q, want unsafe_path", name, c)
		}
		if c := zipCodeOf(t, Zip_write(true, []any{map[string]any{"path": name, "bytes": "x"}})); c != zipCodeUnsafePath {
			t.Errorf("write %q: got %q, want unsafe_path", name, c)
		}
	}
	link := zipRaw(t, func(w *zip.Writer) {
		fh := &zip.FileHeader{Name: "link", Method: zip.Store}
		fh.SetMode(0o777 | 1<<27) // fs.ModeSymlink
		zipAdd(t, w, fh, []byte("/etc/passwd"))
	})
	if c := zipCodeOf(t, zipReadDefault(link)); c != zipCodeUnsafePath {
		t.Fatalf("symlink: got %q", c)
	}
}

func TestZipRefusesDuplicatesEncryptedAndMethods(t *testing.T) {
	dup := zipRaw(t, func(w *zip.Writer) {
		zipAdd(t, w, &zip.FileHeader{Name: "a/b", Method: zip.Store}, []byte("1"))
		zipAdd(t, w, &zip.FileHeader{Name: "a/./b", Method: zip.Store}, []byte("2"))
	})
	if c := zipCodeOf(t, zipReadDefault(dup)); c != zipCodeDuplicatePath {
		t.Fatalf("duplicate: got %q", c)
	}
	if c := zipCodeOf(t, Zip_write(true, []any{
		map[string]any{"path": "a", "bytes": "1"}, map[string]any{"path": "./a", "bytes": "2"},
	})); c != zipCodeDuplicatePath {
		t.Fatalf("write duplicate: got %q", c)
	}
	enc := zipRaw(t, func(w *zip.Writer) {
		zipAdd(t, w, &zip.FileHeader{Name: "secret", Method: zip.Store, Flags: 0x1}, []byte("x"))
	})
	if c := zipCodeOf(t, zipReadDefault(enc)); c != zipCodeEncrypted {
		t.Fatalf("encrypted: got %q", c)
	}
	odd := zipRaw(t, func(w *zip.Writer) {
		w.RegisterCompressor(99, func(out io.Writer) (io.WriteCloser, error) { return nopWriteCloser{out}, nil })
		zipAdd(t, w, &zip.FileHeader{Name: "odd", Method: 99}, []byte("x"))
	})
	if c := zipCodeOf(t, zipReadDefault(odd)); c != zipCodeUnsupportedMethod {
		t.Fatalf("method: got %q", c)
	}
}

func TestZipRefusesNotZipAndCorrupt(t *testing.T) {
	if c := zipCodeOf(t, zipReadDefault("this is not an archive")); c != zipCodeNotZip {
		t.Fatalf("not zip: got %q", c)
	}
	good := Zip_write(false, []any{map[string]any{"path": "a.txt", "bytes": "hello world"}}).(SkyResult[any, any]).OkValue.(string)
	bad := strings.Replace(good, "hello world", "hellO world", 1)
	if c := zipCodeOf(t, zipReadDefault(bad)); c != zipCodeCorrupt {
		t.Fatalf("CRC: got %q", c)
	}
}

func TestZipReadEntry(t *testing.T) {
	data := Zip_write(true, []any{
		map[string]any{"path": "META-INF/container.xml", "bytes": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`},
		map[string]any{"path": "mimetype", "bytes": "application/epub+zip"},
	}).(SkyResult[any, any]).OkValue
	l := zipDefaultLimits
	r := Zip_readEntry(l.maxEntries, int(l.maxEntryBytes), int(l.maxTotalBytes), int(l.maxRatio), "META-INF/container.xml", data).(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatal(r.ErrValue)
	}
	m := r.OkValue.(SkyMaybe[any])
	if m.Tag != 0 || !strings.Contains(m.JustValue.(string), "OEBPS/content.opf") {
		t.Fatalf("entry: %+v", m)
	}
	r = Zip_readEntry(l.maxEntries, int(l.maxEntryBytes), int(l.maxTotalBytes), int(l.maxRatio), "missing", data).(SkyResult[any, any])
	if r.Tag != 0 || r.OkValue.(SkyMaybe[any]).Tag == 0 {
		t.Fatalf("missing entry is Ok Nothing: %+v", r)
	}
	// A non-positive limit is a plain InvalidInput, not a zip domain code.
	if r := Zip_readEntry(0, 1, 1, 1, "x", data).(SkyResult[any, any]); r.Tag == 0 {
		t.Fatal("a zero limit must be refused")
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
