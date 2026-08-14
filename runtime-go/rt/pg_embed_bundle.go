package rt

// Where the embedded PostgreSQL comes from, and how the supervisor decides
// whether a cluster is alive. Companion to pg_embed.go.
//
// The liveness half deliberately mirrors P2's Rust implementation
// (rust/crates/sky/src/db_cluster.rs): the same two-legged check, the same
// refusal to clear a live pid file, the same socket-length arithmetic measured
// on the socket FILE. Two different answers to "is a postmaster serving this
// data directory" is how a second postmaster ends up opening it.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ---------------------------------------------------------------------------
// The compiler-wiring seam
// ---------------------------------------------------------------------------

// EmbeddedPostgresBundle carries the PostgreSQL distribution baked into the
// binary by `sky build --embed`. Generated code sets it from a `go:embed`:
//
//	//go:embed postgres-bundle.tar.gz
//	var pgBundle embed.FS
//	func init() { rt.EmbeddedPostgresBundle = pgBundle }
//
// It is an fs.FS rather than a []byte so the 77MB bundle is streamed out of the
// binary's read-only data rather than copied onto the heap at init, and so a
// build that does not embed anything links no bundle at all — the nil check
// below is the whole cost of the non-embed build.
var EmbeddedPostgresBundle fs.FS

// EmbeddedPostgresBundleName is the path of the archive inside
// EmbeddedPostgresBundle. P2b's CI publishes `postgres-<version>-<platform>.tar.gz`,
// so generated code will normally set this to the exact artifact name; the
// default is the stable name a build can rename to instead.
var EmbeddedPostgresBundleName = "postgres-bundle.tar.gz"

// ---------------------------------------------------------------------------
// Binary discovery
// ---------------------------------------------------------------------------

// requiredPgBins is what a supervisable installation must hold. `psql` is
// deliberately absent: it links GNU readline (GPL-3.0) and P2b excludes it from
// the shipped bundle, so requiring it would reject Sky's own distribution.
// `pg_isready` is likewise not required — it is used for readiness when it is
// there, and a direct connection is the fallback when it is not.
var requiredPgBins = []string{"initdb", "pg_ctl", "postgres"}

type pgBins struct {
	binDir   string
	libDir   string
	shareDir string
	version  string
	major    int
}

func (b pgBins) tool(name string) string { return filepath.Join(b.binDir, name) }

// env is the environment PostgreSQL's own tools are run with.
//
// A relocated bundle needs three things the binaries cannot infer: where its
// shared libraries live (the loader path), where `initdb` finds postgres.bki
// and the timezone database, and where the server finds its extension modules.
// Without PGSHAREDIR an extracted bundle's initdb fails with "could not find
// the postgres.bki file", naming a path that belongs to the machine it was
// built on.
func (b pgBins) env() []string {
	out := os.Environ()
	if b.shareDir != "" {
		out = append(out, "PGSHAREDIR="+b.shareDir)
	}
	if b.libDir != "" {
		key := "LD_LIBRARY_PATH"
		if isDarwin() {
			key = "DYLD_LIBRARY_PATH"
		}
		if prev := os.Getenv(key); prev != "" {
			out = append(out, key+"="+b.libDir+string(os.PathListSeparator)+prev)
		} else {
			out = append(out, key+"="+b.libDir)
		}
	}
	return out
}

// discoverPgBins locates a usable installation, in order of decreasing
// explicitness:
//
//  1. SKY_POSTGRES_BIN — an operator's or a test's deliberate choice. Set but
//     incomplete is an ERROR, not a fall-through: quietly moving on would hand
//     the user a database from an installation they did not choose, which is
//     worse than the typo they made. (This is P2's rule; the same reasoning
//     applies here.)
//  2. the embedded bundle, extracted once into the data directory.
//  3. ~/.sky/postgres/<version>/bin — P3's provision cache.
//  4. PATH.
//
// The bundle outranks the cache and PATH because `--embed` means "this binary
// carries its database"; picking up whatever PostgreSQL happens to be installed
// on the host would make the deployed version depend on the host after all.
func discoverPgBins(cfg embedConfig) (pgBins, error) {
	if override := strings.TrimSpace(os.Getenv("SKY_POSTGRES_BIN")); override != "" {
		if missing := missingPgBins(override); len(missing) > 0 {
			return pgBins{}, fmt.Errorf(
				"sky --embed: SKY_POSTGRES_BIN=%s does not hold %s.\n"+
					"Sky will not fall through to another PostgreSQL: an explicit override that\n"+
					"quietly resolves somewhere else gives you a database you did not choose.\n"+
					"Point it at a directory holding %s, or unset it.",
				override, strings.Join(missing, ", "), strings.Join(requiredPgBins, ", "))
		}
		return interrogate(override)
	}
	if EmbeddedPostgresBundle != nil {
		dir, err := ensureBundleExtracted(cfg.runtimeIn)
		if err != nil {
			return pgBins{}, err
		}
		return interrogate(dir)
	}
	for _, dir := range cachedPgBinDirs() {
		if len(missingPgBins(dir)) == 0 {
			return interrogate(dir)
		}
	}
	if p, err := exec.LookPath("postgres"); err == nil {
		dir := filepath.Dir(p)
		if len(missingPgBins(dir)) == 0 {
			return interrogate(dir)
		}
	}
	return pgBins{}, fmt.Errorf(
		"sky --embed: this binary was not built with an embedded PostgreSQL, and no\n"+
			"PostgreSQL was found to stand in for one (need %s).\n"+
			"\n"+
			"Looked, in order:\n"+
			"  1. $SKY_POSTGRES_BIN                 (unset)\n"+
			"  2. the bundle compiled into this binary (absent — built without --embed)\n"+
			"  3. %s/postgres/<version>/bin\n"+
			"  4. $PATH\n"+
			"\n"+
			"Fix it with one of:\n"+
			"  • rebuild with the bundle:  sky build --embed src/Main.sky\n"+
			"  • point at an installation: SKY_POSTGRES_BIN=/path/to/pg/bin ./app --embed\n"+
			"  • drop --embed and give the app a DSN instead",
		strings.Join(requiredPgBins, ", "), skyHomeDir())
}

func missingPgBins(dir string) []string {
	var missing []string
	for _, b := range requiredPgBins {
		if !fileExists(filepath.Join(dir, b)) {
			missing = append(missing, b)
		}
	}
	return missing
}

func skyHomeDir() string {
	if h := strings.TrimSpace(os.Getenv("SKY_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sky"
	}
	return filepath.Join(home, ".sky")
}

func cachedPgBinDirs() []string {
	entries, err := os.ReadDir(filepath.Join(skyHomeDir(), "postgres"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(skyHomeDir(), "postgres", e.Name(), "bin"))
		}
	}
	// Newest major first, compared numerically per component so "9.6" does not
	// sort above "14".
	sortByVersionDesc(out)
	return out
}

func sortByVersionDesc(dirs []string) {
	key := func(p string) []int {
		v := filepath.Base(filepath.Dir(p))
		var out []int
		for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' }) {
			n, _ := strconv.Atoi(strings.TrimLeft(part, "abcdefghijklmnopqrstuvwxyz"))
			out = append(out, n)
		}
		return out
	}
	for i := 1; i < len(dirs); i++ {
		for j := i; j > 0 && less(key(dirs[j-1]), key(dirs[j])); j-- {
			dirs[j-1], dirs[j] = dirs[j], dirs[j-1]
		}
	}
}

func less(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// interrogate asks a located installation its version. Discovery and
// interrogation are separate steps because a directory can hold all three
// binaries and still refuse to run — wrong architecture, missing libraries, a
// bundle whose relocation went wrong — and that deserves a different message
// than "not found".
func interrogate(binDir string) (pgBins, error) {
	root := filepath.Dir(binDir)
	b := pgBins{
		binDir:   binDir,
		libDir:   dirIfExists(filepath.Join(root, "lib")),
		shareDir: dirIfExists(filepath.Join(root, "share")),
	}
	cmd := exec.Command(b.tool("postgres"), "--version")
	cmd.Env = b.env()
	out, err := cmd.Output()
	if err != nil {
		return pgBins{}, fmt.Errorf(
			"sky --embed: the PostgreSQL binaries in %s do not run (%v).\n"+
				"They may be for a different architecture, or missing a shared library.\n"+
				"Try: %s --version", binDir, err, b.tool("postgres"))
	}
	version, major, ok := parsePgVersion(string(out))
	if !ok {
		return pgBins{}, fmt.Errorf("sky --embed: cannot read a version out of %q", strings.TrimSpace(string(out)))
	}
	b.version, b.major = version, major
	return b, nil
}

func dirIfExists(p string) string {
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p
	}
	return ""
}

// parsePgVersion turns `postgres (PostgreSQL) 18.6 (Homebrew)` into
// ("18.6", 18).
//
// It takes the LEADING run of digits and dots rather than trimming non-digits
// off the right. The difference shows up on a pre-release: `18beta1` and
// `17rc1` end in a digit, so trimming from the right leaves them untouched and
// the parse then fails — which is what P2's Rust does today
// (`trim_end_matches(|c| !c.is_ascii_digit() && c != '.')`), turning a beta
// server into "cannot read a version" instead of a major of 18.
func parsePgVersion(out string) (string, int, bool) {
	for _, tok := range strings.Fields(out) {
		if len(tok) == 0 || tok[0] < '0' || tok[0] > '9' {
			continue
		}
		end := 0
		for end < len(tok) && (tok[end] == '.' || (tok[end] >= '0' && tok[end] <= '9')) {
			end++
		}
		v := strings.TrimSuffix(tok[:end], ".")
		major, err := strconv.Atoi(strings.Split(v, ".")[0])
		if err != nil {
			return "", 0, false
		}
		return v, major, true
	}
	return "", 0, false
}

// ---------------------------------------------------------------------------
// Bundle extraction
// ---------------------------------------------------------------------------

// ensureBundleExtracted unpacks the embedded distribution into `dest` once and
// returns its bin directory.
//
// The marker file is the whole idempotence story: extraction is 77MB of I/O and
// re-doing it on every boot would add seconds to every restart. It records the
// bundle's identity, so a binary rebuilt with a different PostgreSQL extracts
// over the top rather than running the old server against a new data directory.
func ensureBundleExtracted(dest string) (string, error) {
	marker := filepath.Join(dest, ".sky-bundle")
	want := EmbeddedPostgresBundleName
	if got, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(got)) == want {
		if dir, err := bundleBinDir(dest); err == nil {
			return dir, nil
		}
	}
	f, err := EmbeddedPostgresBundle.Open(want)
	if err != nil {
		return "", fmt.Errorf(
			"sky --embed: this binary carries an embedded-PostgreSQL bundle but %q is not\n"+
				"in it (%v). The build set rt.EmbeddedPostgresBundleName to a name the\n"+
				"go:embed did not include.", want, err)
	}
	defer f.Close()

	// A partial extraction left by a killed process must not be mistaken for a
	// complete one, so unpack into a sibling and rename into place.
	staging := dest + ".partial"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", fmt.Errorf("sky --embed: cannot create %s: %w", filepath.Dir(dest), err)
	}
	if err := extractTarGz(f, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("sky --embed: cannot unpack the PostgreSQL bundle: %w", err)
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(staging, dest); err != nil {
		return "", fmt.Errorf("sky --embed: cannot install the unpacked bundle at %s: %w", dest, err)
	}
	if err := os.WriteFile(marker, []byte(want+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("sky --embed: cannot write %s: %w", marker, err)
	}
	return bundleBinDir(dest)
}

// bundleBinDir finds `bin/` in an extracted bundle, at the top or one level
// down. P2b's archive carries a single top-level directory
// (`postgres-18.6-darwin-arm64/`), and depending on how a build tars it that
// component may or may not be stripped; both layouts resolve here rather than
// making the compiler-side follow-up get it exactly right.
func bundleBinDir(dest string) (string, error) {
	if len(missingPgBins(filepath.Join(dest, "bin"))) == 0 {
		return filepath.Join(dest, "bin"), nil
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", fmt.Errorf("cannot read the unpacked bundle at %s: %w", dest, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cand := filepath.Join(dest, e.Name(), "bin")
		if len(missingPgBins(cand)) == 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf(
		"the unpacked PostgreSQL bundle at %s has no bin/ directory holding %s",
		dest, strings.Join(requiredPgBins, ", "))
}

// extractTarGz unpacks a gzipped tar, preserving the executable bit and
// symlinks.
//
// Both matter and neither is optional. A go:embed FS cannot represent either —
// every file in one is mode 0444 and symlinks are simply absent — which is why
// the bundle travels as a tar inside the FS rather than as a directory tree:
// unpacked from an embed.FS directly, `postgres` would not be executable and
// `libpq.5.dylib` would not exist.
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// A symlink whose target escapes the destination is the same
			// traversal attack as a `../` member, one indirection later — and
			// the link has to be resolved against its OWN directory, because
			// `../lib/x` from `bin/` is perfectly legitimate.
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("bundle contains an absolute symlink %q → %q", h.Name, h.Linkname)
			}
			if !withinDir(dest, filepath.Join(filepath.Dir(target), h.Linkname)) {
				return fmt.Errorf("bundle symlink %q → %q escapes the destination directory", h.Name, h.Linkname)
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			source, err := safeJoin(dest, h.Linkname)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(source, target); err != nil {
				return err
			}
		default:
			// Devices, fifos and sockets have no business in a PostgreSQL
			// bundle; skipping them silently is safer than materialising them.
		}
	}
}

// safeJoin refuses any archive member that would land outside dest.
//
// It REJECTS rather than sanitises. Clamping `../../escaped` to `escaped` —
// which `filepath.Join(dest, filepath.Clean("/"+name))` quietly does — makes
// the file-member case harmless and the symlink case a hole: the link is
// created with its original target, so it still resolves outside dest and
// anything later written through it lands there. An archive that tries to
// escape is not one to unpack a repaired version of.
func safeJoin(dest, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("bundle entry %q is an absolute path", name)
	}
	target := filepath.Join(dest, name)
	if !withinDir(dest, target) {
		return "", fmt.Errorf("bundle entry %q escapes the destination directory", name)
	}
	return target, nil
}

// withinDir reports whether p is dest itself or something under it.
func withinDir(dest, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(dest), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ---------------------------------------------------------------------------
// Socket path derivation
// ---------------------------------------------------------------------------

// pgSocketBasename is what PostgreSQL names its socket. The port survives in
// the filename even for a socket-only cluster, and 5432 is what every client
// library assumes when handed a socket directory and no port.
const pgSocketBasename = ".s.PGSQL.5432"

// maxSocketPath is the longest socket path Sky will hand to PostgreSQL.
//
// The kernel ceiling is `sizeof(sun_path) - 1`: 107 bytes on Linux, 103 on
// macOS. This sits under both with room for the `.lock` file PostgreSQL creates
// alongside the socket, which is five bytes longer again. The budget is
// measured on the socket FILE, not the directory — a directory-only check is
// short by nineteen bytes, which is exactly the size of a bug that passes on
// the machine that wrote it and fails on a host with a longer prefix.
const maxSocketPath = 92

// socketDirFor derives a short, stable socket directory for a data directory.
//
// It is deliberately OUTSIDE the data directory. A data directory is meant to
// be somewhere durable and explicit — `/var/lib/something-with-a-long-name` —
// and a socket underneath it inherits that length and overflows sun_path with
// an error that names neither the limit nor the path.
//
// `xdg` and `fallbackBase` are parameters so the derivation, including the
// pathological case that motivates it, is testable without touching process
// state. The XDG branch is itself length-checked because it is the user's
// value: an unchecked branch just relocates the overflow. The fallback is the
// literal /tmp rather than os.TempDir(), which on macOS is a ~49-byte per-user
// path under /var/folders that spends half the budget before the hash.
func socketDirFor(dataDir, xdg, fallbackBase string) string {
	hash := pathHash(dataDir)
	if x := strings.TrimSpace(xdg); x != "" && filepath.IsAbs(x) {
		cand := filepath.Join(x, "sky", hash)
		if socketPathLen(cand) <= maxSocketPath {
			return cand
		}
	}
	return filepath.Join(fallbackBase, "sky-"+hash)
}

// socketPathLen is the byte length of the socket FILE inside dir.
func socketPathLen(dir string) int {
	return len(filepath.Join(dir, pgSocketBasename))
}

const (
	fnv128OffsetHi = 0x6c62272e07bb0142
	fnv128OffsetLo = 0x62b821756295c58d
	fnv128PrimeLo  = 0x0000013b
	fnv128PrimeSh  = 88 // the prime is 2^88 + 0x13b
)

// pathHash is FNV-1a/128 truncated to its top 64 bits, rendered as 16 hex
// characters — byte-for-byte the same derivation P2 uses in Rust, so the two
// implementations name the same socket directory for the same path.
//
// A stdlib hash is not usable here for the reason P2 gives: this value is
// PERSISTED (it names a directory a running postmaster is bound to), and Go's
// maphash is explicitly seeded per process while Rust's DefaultHasher is
// explicitly unstable across releases. Either would orphan every running
// cluster on an upgrade.
func pathHash(p string) string {
	hi, lo := uint64(fnv128OffsetHi), uint64(fnv128OffsetLo)
	for i := 0; i < len(p); i++ {
		lo ^= uint64(p[i])
		hi, lo = mul128Prime(hi, lo)
	}
	return fmt.Sprintf("%016x", hi)
}

// mul128Prime multiplies a 128-bit value by the FNV prime (2^88 + 0x13b),
// modulo 2^128.
func mul128Prime(hi, lo uint64) (uint64, uint64) {
	// (hi,lo) * 0x13b
	const k = uint64(fnv128PrimeLo)
	loLo := lo & 0xffffffff
	loHi := lo >> 32
	p0 := loLo * k
	p1 := loHi*k + (p0 >> 32)
	newLo := (p1 << 32) | (p0 & 0xffffffff)
	newHi := hi*k + (p1 >> 32)
	// plus (hi,lo) << 88  — only the low 40 bits of lo survive into hi.
	newHi += lo << (fnv128PrimeSh - 64)
	return newHi, newLo
}

// dsnForSocketDir is the DSN handed to the app.
//
// The URL form (not libpq's keyword form) because that is what `detectDriver`
// classifies as Postgres from the prefix alone; `?host=<dir>` is libpq's
// documented way to name a unix socket DIRECTORY and pgx honours it. No user
// and no password: local auth is trust — the 0700 socket directory IS the
// access control — and the client library defaults the role to the OS user,
// which is the superuser initdb created.
func dsnForSocketDir(socketDir string) string {
	return "postgresql:///postgres?host=" + socketDir
}

// pgSocketDirUnsafeChars are the characters that make a socket directory
// unusable rather than merely awkward. See prepareSocketDir.
const pgSocketDirUnsafeChars = "'\"`$\\ \t\n;&|()<>*?"

// socketDirIsShellSafe reports whether a path can survive being interpolated
// into a `/bin/sh` command line.
//
// `pg_ctl start` builds a command string and hands it to /bin/sh
// (`start_postmaster` in pg_ctl.c), so a path carrying a quote, a `$` or a
// space either breaks the start or executes something. Quoting cannot fix it;
// rejecting it with the reason is the only honest option.
//
// This supervisor exec's the postmaster directly and only uses `pg_ctl` to
// STOP — which does not shell out — so nothing here is currently handed to a
// shell. The check is kept anyway, and it is not decoration: the socket
// directory is derived identically by `sky db start` (P2), which does go
// through `pg_ctl start`, and the same data directory is served by both. A path
// this supervisor accepted and that verb refused would be a cluster that only
// one half of the toolchain can talk to. The half of the derivation that is not
// safe by construction is $XDG_RUNTIME_DIR.
func socketDirIsShellSafe(dir string) bool {
	return !strings.ContainsAny(dir, pgSocketDirUnsafeChars)
}

func prepareSocketDir(socketDir string) error {
	if !socketDirIsShellSafe(socketDir) {
		return fmt.Errorf(
			"sky --embed: the derived socket directory contains characters sky will not\n"+
				"pass through a shell:\n"+
				"  %s\n"+
				"pg_ctl runs PostgreSQL's own tooling via /bin/sh, so this cannot be quoted\n"+
				"safely. Set XDG_RUNTIME_DIR to a plain path and retry.", socketDir)
	}
	if n := socketPathLen(socketDir); n > maxSocketPath {
		return fmt.Errorf(
			"sky --embed: the derived socket path is %d bytes, over the %d-byte limit:\n"+
				"  %s\n"+
				"The kernel's sockaddr_un limit is 107 bytes on Linux and 103 on macOS.\n"+
				"Set XDG_RUNTIME_DIR to a shorter directory and retry.",
			n, maxSocketPath, filepath.Join(socketDir, pgSocketBasename))
	}
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return fmt.Errorf("sky --embed: cannot create the socket directory %s: %w", socketDir, err)
	}
	// The socket IS the access control: anything that can reach it talks to the
	// database as its superuser, because local auth is trust.
	_ = os.Chmod(socketDir, 0o700)
	return nil
}

// ---------------------------------------------------------------------------
// Data directory state and liveness
// ---------------------------------------------------------------------------

type dataDirState int

const (
	dataDirAbsent      dataDirState = iota // nothing there — initdb
	dataDirInitialised                     // PG_VERSION present
	dataDirRubble                          // something there, but no PG_VERSION
)

func inspectDataDir(dataDir string) (dataDirState, error) {
	if fileExists(filepath.Join(dataDir, "PG_VERSION")) {
		return dataDirInitialised, nil
	}
	entries, err := os.ReadDir(dataDir)
	if os.IsNotExist(err) {
		return dataDirAbsent, nil
	}
	if err != nil {
		return dataDirAbsent, fmt.Errorf("sky --embed: cannot read %s: %w", dataDir, err)
	}
	if len(entries) == 0 {
		return dataDirAbsent, nil
	}
	return dataDirRubble, nil
}

// checkMajorMatches refuses rather than attempts. A postmaster pointed at a
// data directory from another major does not migrate it, and PostgreSQL's own
// refusal ("database files are incompatible with server") names neither version
// nor a way out.
func checkMajorMatches(dataDir string, bins pgBins) error {
	b, err := os.ReadFile(filepath.Join(dataDir, "PG_VERSION"))
	if err != nil {
		return fmt.Errorf("sky --embed: cannot read %s: %w", filepath.Join(dataDir, "PG_VERSION"), err)
	}
	text := strings.TrimSpace(string(b))
	dirMajor, err := strconv.Atoi(strings.Split(text, ".")[0])
	if err != nil {
		return fmt.Errorf(
			"sky --embed: %s does not hold a PostgreSQL version (%q).\n"+
				"The data directory looks corrupt.", filepath.Join(dataDir, "PG_VERSION"), text)
	}
	if dirMajor == bins.major {
		return nil
	}
	return fmt.Errorf(
		"sky --embed: PostgreSQL major mismatch — this cluster cannot be started.\n"+
			"\n"+
			"  data directory: %s  (initialised by PostgreSQL %d)\n"+
			"  server:         %s  (PostgreSQL %d)\n"+
			"\n"+
			"A %d server will not open a %d data directory, and starting one against it\n"+
			"would not upgrade it. Choose one:\n"+
			"  • run the matching server:  SKY_POSTGRES_BIN=<path to %d's bin> ./app --embed\n"+
			"  • migrate with pg_upgrade, keeping the data\n"+
			"  • discard the data:  rm -rf %s",
		dataDir, dirMajor, bins.binDir, bins.major, bins.major, dirMajor, dirMajor, dataDir)
}

// parsePostmasterPid reads the pid off line 1 of a `postmaster.pid`.
func parsePostmasterPid(text string) (int, bool) {
	line, _, _ := strings.Cut(text, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func readPostmasterPid(dataDir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dataDir, "postmaster.pid"))
	if err != nil {
		return 0, false
	}
	return parsePostmasterPid(string(b))
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	// EPERM means the process exists and belongs to someone else — which is
	// still "alive", and reporting it dead would let a second postmaster open
	// the data directory.
	return err == nil || err == syscall.EPERM
}

// commandLooksLikePostgres is the second leg of the liveness check.
//
// Pid reuse is the entire reason it exists. After a SIGKILL the stale
// postmaster.pid still names a number, and the kernel is free to hand that
// number to something else; kill(pid, 0) then says "alive" about a shell. That
// is exactly what a killed app leaves behind, so it is exactly the case that
// must not be got wrong in either direction: believing the shell means refusing
// to boot forever, and not checking at all means deleting a live postmaster's
// pid file and letting a second one open the same data directory.
//
// It matches the EXECUTABLE, not the command line. P2's Rust asks whether the
// line CONTAINS "postgres" anywhere, and that is too generous by a wide margin:
// `./app --embed --data-dir /var/lib/postgres-data` contains it, and so does
// every `go test -run TestStopPostgres…` — which is how this function was first
// caught calling a Go test binary a postmaster. A false positive here is not
// cosmetic: `sky db ps` reports a database that is not there, and
// clearStalePidfile refuses to boot for as long as the recycled pid lives.
//
// The postmaster's own line is `<dir>/postgres -D …`; its auxiliary processes
// rename themselves to `postgres: checkpointer`, hence the trailing colon.
// Only the postmaster is ever named in postmaster.pid, but both are accepted
// because a caller may reasonably probe either.
func commandLooksLikePostgres(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	exe := strings.ToLower(strings.TrimSuffix(filepath.Base(fields[0]), ":"))
	return exe == "postgres" || exe == "postmaster"
}

func processCommand(pid int) (string, bool) {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	return s, s != ""
}

func isPostgresProcess(pid int) bool {
	if !processAlive(pid) {
		return false
	}
	// `ps` absent (a minimal container) → fall back to bare aliveness rather
	// than declaring a running cluster dead.
	cmd, ok := processCommand(pid)
	if !ok {
		return true
	}
	return commandLooksLikePostgres(cmd)
}

// runningPostmaster reports the pid of a postmaster currently serving dataDir.
func runningPostmaster(dataDir string) (int, bool) {
	pid, ok := readPostmasterPid(dataDir)
	if !ok {
		return 0, false
	}
	if !isPostgresProcess(pid) {
		return 0, false
	}
	return pid, true
}

// clearStalePidfile removes a postmaster.pid left behind by a SIGKILL, and only
// then.
//
// The liveness check is the safety interlock, not a formality: deleting a live
// postmaster's pid file would let a second postmaster open the same data
// directory, which is how a database gets corrupted rather than merely stopped.
func clearStalePidfile(dataDir string) error {
	pidfile := filepath.Join(dataDir, "postmaster.pid")
	pid, ok := readPostmasterPid(dataDir)
	if !ok {
		if fileExists(pidfile) {
			_ = os.Remove(pidfile)
		}
		return nil
	}
	if isPostgresProcess(pid) {
		return fmt.Errorf(
			"sky --embed: a live PostgreSQL process (pid %d) is already using %s.\n"+
				"Two postmasters must never open one data directory. Stop the other one first.",
			pid, dataDir)
	}
	if err := os.Remove(pidfile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sky --embed: cannot clear the stale pid file %s: %w", pidfile, err)
	}
	fmt.Fprintf(os.Stderr, "[sky.pg] cleared a stale postmaster.pid (pid %d is gone)\n", pid)
	return nil
}
