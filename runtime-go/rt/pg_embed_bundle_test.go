package rt

// Gates for unpacking the embedded distribution and for locating one.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	link     string
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typeflag
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: typ, Linkname: e.link}
		if typ == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The executable bit and symlinks are the reason the bundle travels as a tar
// inside the embedded FS rather than as a directory tree: a go:embed FS makes
// every file mode 0444 and cannot represent a symlink at all. Unpacked from one
// directly, `postgres` would not be runnable and `libpq.5.dylib` would not
// exist.
func TestExtractPreservesTheExecutableBitAndSymlinks(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "runtime")
	archive := makeTarGz(t, []tarEntry{
		{name: "bin/", mode: 0o755, typeflag: tar.TypeDir},
		{name: "bin/postgres", body: "#!/bin/sh\nexit 0\n", mode: 0o755},
		{name: "share/postgres.bki", body: "catalogs", mode: 0o644},
		{name: "lib/libpq.5.17.dylib", body: "lib", mode: 0o644},
		{name: "lib/libpq.5.dylib", mode: 0o777, typeflag: tar.TypeSymlink, link: "libpq.5.17.dylib"},
	})
	if err := extractTarGz(bytes.NewReader(archive), dest); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(filepath.Join(dest, "bin", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("postgres came out mode %v — it cannot be executed", st.Mode().Perm())
	}
	if st, err := os.Stat(filepath.Join(dest, "share", "postgres.bki")); err != nil {
		t.Fatal(err)
	} else if st.Mode().Perm()&0o111 != 0 {
		t.Errorf("a data file came out executable: %v", st.Mode().Perm())
	}
	link, err := os.Readlink(filepath.Join(dest, "lib", "libpq.5.dylib"))
	if err != nil {
		t.Fatalf("the symlink was not recreated: %v", err)
	}
	if link != "libpq.5.17.dylib" {
		t.Errorf("symlink points at %q", link)
	}
}

// A bundle is data that ends up on disk with the app's privileges. Two members
// must never be honoured.
func TestExtractRefusesToEscapeTheDestination(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{"traversal", []tarEntry{{name: "../../escaped", body: "x", mode: 0o644}}},
		{"absolute symlink", []tarEntry{
			{name: "lib/evil", mode: 0o777, typeflag: tar.TypeSymlink, link: "/etc/passwd"}}},
		{"relative symlink that escapes", []tarEntry{
			{name: "lib/evil", mode: 0o777, typeflag: tar.TypeSymlink, link: "../../../../etc/passwd"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "runtime")
			err := extractTarGz(bytes.NewReader(makeTarGz(t, c.entries)), dest)
			if err == nil {
				t.Fatal("the member was accepted")
			}
			if !strings.Contains(err.Error(), "escapes") && !strings.Contains(err.Error(), "absolute symlink") {
				t.Errorf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestExtractRejectsSomethingThatIsNotAnArchive(t *testing.T) {
	err := extractTarGz(strings.NewReader("this is not a gzip stream"), filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("want a gzip complaint, got %v", err)
	}
}

// Extraction is 77MB of I/O; doing it on every boot would add seconds to every
// restart. The marker makes it happen once, and makes a rebuilt binary carrying
// a different PostgreSQL replace it rather than run the old server.
func TestBundleIsExtractedOnceAndAgainWhenTheBundleChanges(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "runtime")
	first := makeTarGz(t, []tarEntry{
		{name: "bin/initdb", body: "v1", mode: 0o755},
		{name: "bin/pg_ctl", body: "v1", mode: 0o755},
		{name: "bin/postgres", body: "v1", mode: 0o755},
	})
	withBundle(t, "postgres-18.6-darwin-arm64.tar.gz", first)

	binDir, err := ensureBundleExtracted(dest)
	if err != nil {
		t.Fatal(err)
	}
	if binDir != filepath.Join(dest, "bin") {
		t.Fatalf("bin dir = %s", binDir)
	}

	// A marker file that mentions the same bundle short-circuits: prove it by
	// scribbling on the unpacked copy and watching it survive.
	pgWriteFile(t, filepath.Join(dest, "bin", "postgres"), "touched")
	if _, err := ensureBundleExtracted(dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "bin", "postgres")); string(b) != "touched" {
		t.Error("the bundle was unpacked a second time for no reason")
	}

	// A new bundle name means a new PostgreSQL: unpack over the top.
	withBundle(t, "postgres-19.1-darwin-arm64.tar.gz", makeTarGz(t, []tarEntry{
		{name: "bin/initdb", body: "v2", mode: 0o755},
		{name: "bin/pg_ctl", body: "v2", mode: 0o755},
		{name: "bin/postgres", body: "v2", mode: 0o755},
	}))
	if _, err := ensureBundleExtracted(dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "bin", "postgres")); string(b) != "v2" {
		t.Errorf("the new bundle was not unpacked (got %q)", b)
	}
}

// P2b's CI tars the bundle with its own directory at the top
// (`postgres-18.6-darwin-arm64/bin/...`). Whether a build strips that component
// or not should not be something the compiler-side follow-up has to get exactly
// right.
func TestBundleBinDirIsFoundAtEitherDepth(t *testing.T) {
	for _, prefix := range []string{"", "postgres-18.6-darwin-arm64/"} {
		root := t.TempDir()
		dest := filepath.Join(root, "runtime")
		withBundle(t, "b.tar.gz", makeTarGz(t, []tarEntry{
			{name: prefix + "bin/initdb", body: "x", mode: 0o755},
			{name: prefix + "bin/pg_ctl", body: "x", mode: 0o755},
			{name: prefix + "bin/postgres", body: "x", mode: 0o755},
		}))
		got, err := ensureBundleExtracted(dest)
		if err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
		if want := filepath.Join(dest, prefix, "bin"); filepath.Clean(got) != filepath.Clean(want) {
			t.Errorf("prefix %q: bin dir = %s, want %s", prefix, got, want)
		}
	}
}

func TestBundleMissingTheNamedArchiveSaysSo(t *testing.T) {
	withBundle(t, "expected.tar.gz", makeTarGz(t, []tarEntry{{name: "bin/postgres", body: "x", mode: 0o755}}))
	EmbeddedPostgresBundleName = "a-different-name.tar.gz"
	_, err := ensureBundleExtracted(filepath.Join(t.TempDir(), "runtime"))
	if err == nil {
		t.Fatal("a missing archive was not reported")
	}
	if !strings.Contains(err.Error(), "a-different-name.tar.gz") {
		t.Errorf("the error does not name what was looked for:\n%v", err)
	}
}

func withBundle(t *testing.T, name string, archive []byte) {
	t.Helper()
	prevFS, prevName := EmbeddedPostgresBundle, EmbeddedPostgresBundleName
	EmbeddedPostgresBundle = fs.FS(fstest.MapFS{name: &fstest.MapFile{Data: archive}})
	EmbeddedPostgresBundleName = name
	t.Cleanup(func() {
		EmbeddedPostgresBundle, EmbeddedPostgresBundleName = prevFS, prevName
	})
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// An explicit override that does not hold the binaries is a typo, and falling
// through to the next candidate would hand the operator a database from an
// installation they did not choose. Requiring all three is likewise correct
// rather than pedantic: /opt/homebrew/opt/libpq/bin really does ship pg_ctl and
// initdb with no server behind them.
func TestAnIncompleteSKYPOSTGRESBINIsAnErrorNotAFallThrough(t *testing.T) {
	dir := t.TempDir()
	for _, b := range []string{"initdb", "pg_ctl"} { // no `postgres`
		pgWriteFile(t, filepath.Join(dir, b), "#!/bin/sh\n")
	}
	t.Setenv("SKY_POSTGRES_BIN", dir)
	// A perfectly good PostgreSQL on PATH must NOT rescue the typo.
	withBundle(t, "b.tar.gz", makeTarGz(t, []tarEntry{
		{name: "bin/initdb", body: "x", mode: 0o755},
		{name: "bin/pg_ctl", body: "x", mode: 0o755},
		{name: "bin/postgres", body: "x", mode: 0o755},
	}))

	_, err := discoverPgBins(embedConfig{runtimeIn: filepath.Join(t.TempDir(), "runtime")})
	if err == nil {
		t.Fatal("an incomplete SKY_POSTGRES_BIN was accepted")
	}
	for _, want := range []string{dir, "postgres", "will not fall through"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

func TestNoPostgresAnywhereNamesEveryPlaceItLooked(t *testing.T) {
	t.Setenv("SKY_POSTGRES_BIN", "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SKY_HOME", t.TempDir())
	prev := EmbeddedPostgresBundle
	EmbeddedPostgresBundle = nil
	t.Cleanup(func() { EmbeddedPostgresBundle = prev })

	_, err := discoverPgBins(embedConfig{runtimeIn: filepath.Join(t.TempDir(), "runtime")})
	if err == nil {
		t.Fatal("discovery succeeded with no PostgreSQL anywhere")
	}
	for _, want := range []string{"SKY_POSTGRES_BIN", "$PATH", "sky build --embed", "built without --embed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q:\n%v", want, err)
		}
	}
}

func TestTheProvisionCacheIsSearchedNewestMajorFirst(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"9.6", "14", "18.6", "16"} {
		dir := filepath.Join(home, "postgres", v, "bin")
		pgMkdirAll(t, dir)
		for _, b := range requiredPgBins {
			pgWriteFile(t, filepath.Join(dir, b), "x")
		}
	}
	t.Setenv("SKY_HOME", home)
	got := cachedPgBinDirs()
	var versions []string
	for _, d := range got {
		versions = append(versions, filepath.Base(filepath.Dir(d)))
	}
	want := []string{"18.6", "16", "14", "9.6"}
	if strings.Join(versions, ",") != strings.Join(want, ",") {
		t.Fatalf("cache order = %v, want %v (9.6 must not sort above 14)", versions, want)
	}
}
