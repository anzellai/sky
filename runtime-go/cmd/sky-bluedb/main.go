// sky-bluedb — an OFFLINE inspector + editor for a BlueDB store file.
//
// It opens the store with the engine's normal (exclusive) lock, so it operates
// on a store whose app is STOPPED, or on a copied/backup file. A LIVE store
// (running Sky app) holds the lock — this tool then fails fast with guidance:
// mutate a live store THROUGH the app (its console / admin action), never with a
// second writer (that would corrupt the WAL).
//
// It links the real engine (sky-app/bluedb), so reads and writes share one
// format implementation — no drift. Build standalone:
//
//	go build -o bluedb ./runtime-go/cmd/sky-bluedb
//	bluedb data/app.blue stats
//
// Usage:
//
//	bluedb <path> stats
//	bluedb <path> keys   [prefix] [--limit N] [--json]
//	bluedb <path> scan   [prefix] [--limit N] [--json]
//	bluedb <path> get    <key>              [--json] [--raw]
//	bluedb <path> put    <key> <value>      [--stdin]
//	bluedb <path> delete <key>              [--yes]
//	bluedb <path> compact                   [--yes]
//	bluedb <path> verify                    [--json]
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"sky-app/bluedb"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type flags struct {
	limit   int
	json    bool
	raw     bool
	yes     bool
	stdin   bool
	url     string // remote mode: the running app's base URL
	token   string // remote mode: SKY_ADMIN_TOKEN bearer
	envFile string // read url/token from a .env file
}

func run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	positional, f, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintf(stderr, "bluedb: %v\n\n%s", err, usage)
		return 2
	}
	if f.limit == 0 {
		f.limit = 100 // a sane default cap for keys/scan so a huge store doesn't flood
	}
	resolveRemoteConfig(&f) // --env file + process-env fallbacks for url/token

	// Remote mode: talk to a RUNNING app's admin endpoint (zero-downtime live
	// inspect/edit) instead of opening the file. Positional[0] is the store path
	// ON the remote (or the literal "stores" to list them).
	if f.url != "" {
		return runRemote(positional, f, stdin, stdout, stderr)
	}

	if len(positional) < 2 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	path, cmd := positional[0], positional[1]
	rest := positional[2:]

	// verify is a READ-ONLY integrity scan — it must NOT go through Open (Open
	// would truncate a torn tail or refuse a corrupt file). Scan the raw path.
	if cmd == "verify" {
		return cmdVerify(path, f, stdout, stderr)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755) // engine Open won't create the parent dir
	}
	db, err := bluedb.Open(path, bluedb.Options{Sync: true})
	if err != nil {
		if errors.Is(err, bluedb.ErrLocked) {
			fmt.Fprintf(stderr, "bluedb: %q is open by a running app (exclusive lock).\n"+
				"  Inspect/edit a LIVE store through the app itself (its console / admin action) —\n"+
				"  a second writer would corrupt the WAL. Otherwise stop the app, or operate on a copy.\n", path)
			return 3
		}
		fmt.Fprintf(stderr, "bluedb: open %q: %v\n", path, err)
		return 1
	}
	defer db.Close()

	switch cmd {
	case "stats":
		return cmdStats(db, path, f, stdout)
	case "keys":
		return cmdKeys(db, rest, f, stdout)
	case "scan":
		return cmdScan(db, rest, f, stdout)
	case "get":
		return cmdGet(db, rest, f, stdout, stderr)
	case "put":
		return cmdPut(db, rest, f, stdin, stdout, stderr)
	case "delete", "del", "rm":
		return cmdDelete(db, rest, f, stdin, stdout, stderr)
	case "compact":
		return cmdCompact(db, f, stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "bluedb: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func parseFlags(args []string) (positional []string, f flags, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			f.json = true
		case a == "--raw":
			f.raw = true
		case a == "--yes" || a == "-y" || a == "--force":
			f.yes = true
		case a == "--stdin":
			f.stdin = true
		case a == "--limit":
			if i+1 >= len(args) {
				return nil, f, errors.New("--limit needs a value")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil {
				return nil, f, fmt.Errorf("--limit: %v", e)
			}
			f.limit = n
		case strings.HasPrefix(a, "--limit="):
			n, e := strconv.Atoi(strings.TrimPrefix(a, "--limit="))
			if e != nil {
				return nil, f, fmt.Errorf("--limit: %v", e)
			}
			f.limit = n
		case a == "--url":
			if i+1 >= len(args) {
				return nil, f, errors.New("--url needs a value")
			}
			i++
			f.url = args[i]
		case strings.HasPrefix(a, "--url="):
			f.url = strings.TrimPrefix(a, "--url=")
		case a == "--token":
			if i+1 >= len(args) {
				return nil, f, errors.New("--token needs a value")
			}
			i++
			f.token = args[i]
		case strings.HasPrefix(a, "--token="):
			f.token = strings.TrimPrefix(a, "--token=")
		case a == "--env":
			if i+1 >= len(args) {
				return nil, f, errors.New("--env needs a value")
			}
			i++
			f.envFile = args[i]
		case strings.HasPrefix(a, "--env="):
			f.envFile = strings.TrimPrefix(a, "--env=")
		case strings.HasPrefix(a, "--"):
			return nil, f, fmt.Errorf("unknown flag %q", a)
		default:
			positional = append(positional, a)
		}
	}
	return positional, f, nil
}

func cmdStats(db *bluedb.DB, path string, f flags, out io.Writer) int {
	batches, writes, checkpoints := db.Stats()
	walSize := fileSize(path)
	snapSize := fileSize(path + ".snap")
	if f.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"keys": db.Len(), "wal_bytes": walSize, "snap_bytes": snapSize,
			"batches": batches, "writes": writes, "checkpoints": checkpoints,
		})
		return 0
	}
	fmt.Fprintf(out, "keys:        %d\n", db.Len())
	fmt.Fprintf(out, "wal bytes:   %d\n", walSize)
	fmt.Fprintf(out, "snap bytes:  %d\n", snapSize)
	fmt.Fprintf(out, "batches:     %d\n", batches)
	fmt.Fprintf(out, "writes:      %d\n", writes)
	fmt.Fprintf(out, "checkpoints: %d\n", checkpoints)
	return 0
}

func cmdKeys(db *bluedb.DB, pos []string, f flags, out io.Writer) int {
	prefix := ""
	if len(pos) > 0 {
		prefix = pos[0]
	}
	var ks []string
	db.Scan([]byte(prefix), nil, f.limit, func(k, _ []byte) bool {
		ks = append(ks, string(k))
		return true
	})
	if f.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(ks)
		return 0
	}
	for _, k := range ks {
		fmt.Fprintln(out, k)
	}
	return 0
}

func cmdScan(db *bluedb.DB, pos []string, f flags, out io.Writer) int {
	prefix := ""
	if len(pos) > 0 {
		prefix = pos[0]
	}
	if f.json {
		enc := json.NewEncoder(out)
		db.Scan([]byte(prefix), nil, f.limit, func(k, v []byte) bool {
			_ = enc.Encode(map[string]any{"key": string(k), "value": jsonValue(v)})
			return true
		})
		return 0
	}
	db.Scan([]byte(prefix), nil, f.limit, func(k, v []byte) bool {
		fmt.Fprintf(out, "%s\t%s\n", string(k), displayValue(v, false))
		return true
	})
	return 0
}

func cmdGet(db *bluedb.DB, pos []string, f flags, out, errOut io.Writer) int {
	if len(pos) < 1 {
		fmt.Fprintln(errOut, "bluedb: get needs a <key>")
		return 2
	}
	v, ok := db.Get([]byte(pos[0]))
	if !ok {
		fmt.Fprintf(errOut, "bluedb: key %q not found\n", pos[0])
		return 4
	}
	if f.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(jsonValue(v))
		return 0
	}
	fmt.Fprintln(out, displayValue(v, f.raw))
	return 0
}

func cmdPut(db *bluedb.DB, pos []string, f flags, stdin io.Reader, out, errOut io.Writer) int {
	if len(pos) < 1 {
		fmt.Fprintln(errOut, "bluedb: put needs a <key> (and a value or --stdin)")
		return 2
	}
	key := pos[0]
	var val []byte
	if f.stdin {
		b, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(errOut, "bluedb: read stdin: %v\n", err)
			return 1
		}
		val = b
	} else if len(pos) >= 2 {
		val = []byte(pos[1])
	} else {
		fmt.Fprintln(errOut, "bluedb: put needs a <value> arg or --stdin")
		return 2
	}
	// F7: reject a key reaching into the reserved index/manifest keyspace — the
	// CLI writes via the engine db.Put, bypassing the kernel's NUL guard, so
	// without this a `bluedb put` could corrupt the index/manifest/seq keyspace.
	if strings.ContainsRune(key, 0) {
		fmt.Fprintln(errOut, "bluedb: key must not contain NUL (reserved for the index keyspace)")
		return 2
	}
	if err := db.Put([]byte(key), val); err != nil {
		fmt.Fprintf(errOut, "bluedb: put %q: %v\n", key, err)
		return 1
	}
	fmt.Fprintf(out, "put %s (%d bytes)\n", key, len(val))
	return 0
}

func cmdDelete(db *bluedb.DB, pos []string, f flags, stdin io.Reader, out, errOut io.Writer) int {
	if len(pos) < 1 {
		fmt.Fprintln(errOut, "bluedb: delete needs a <key>")
		return 2
	}
	key := pos[0]
	if _, ok := db.Get([]byte(key)); !ok {
		fmt.Fprintf(errOut, "bluedb: key %q not found (nothing deleted)\n", key)
		return 4
	}
	if !f.yes && !confirm(stdin, out, fmt.Sprintf("delete key %q?", key)) {
		fmt.Fprintln(out, "aborted")
		return 0
	}
	if err := db.Delete([]byte(key)); err != nil {
		fmt.Fprintf(errOut, "bluedb: delete %q: %v\n", key, err)
		return 1
	}
	fmt.Fprintf(out, "deleted %s\n", key)
	return 0
}

func cmdCompact(db *bluedb.DB, f flags, stdin io.Reader, out, errOut io.Writer) int {
	if !f.yes && !confirm(stdin, out, "compact (snapshot + truncate WAL) now?") {
		fmt.Fprintln(out, "aborted")
		return 0
	}
	if err := db.Checkpoint(); err != nil {
		fmt.Fprintf(errOut, "bluedb: compact: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "compacted")
	return 0
}

// cmdVerify runs the read-only integrity scanner (bluedb.Verify) and prints a
// human (or --json) report. Exit code: 0 when Open would succeed (clean, or a
// torn tail the engine recovers), non-zero on corruption / unsupported version
// so scripts + CI can gate on it.
func cmdVerify(path string, f flags, out, errOut io.Writer) int {
	rep, err := bluedb.Verify(path)
	if err != nil {
		fmt.Fprintf(errOut, "bluedb: verify %q: %v\n", path, err)
		return 1
	}
	if f.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		if rep.OK {
			return 0
		}
		return 1
	}

	if !rep.WalExists {
		fmt.Fprintln(out, "wal:              (absent — fresh / never-written store)")
	} else {
		if rep.WalVersion > 0 {
			fmt.Fprintf(out, "wal version:      %d\n", rep.WalVersion)
		} else {
			fmt.Fprintln(out, "wal version:      0 (legacy headerless)")
		}
		fmt.Fprintf(out, "wal bytes:        %d\n", rep.WalBytes)
		fmt.Fprintf(out, "records scanned:  %d\n", rep.WalRecords)
	}
	fmt.Fprintf(out, "wal status:       %s\n", rep.WalStatus)
	if rep.FirstBadOffset >= 0 {
		fmt.Fprintf(out, "first bad offset: %d\n", rep.FirstBadOffset)
	}
	if rep.Detail != "" {
		fmt.Fprintf(out, "detail:           %s\n", rep.Detail)
	}
	if rep.SnapExists {
		fmt.Fprintf(out, "snapshot:         %s (coveredSeq %d)\n", rep.SnapStatus, rep.SnapCoveredSeq)
	} else {
		fmt.Fprintln(out, "snapshot:         absent")
	}

	if rep.OK {
		if rep.WalStatus == bluedb.VerifyTornTail {
			fmt.Fprintln(out, "OK — a torn tail will be truncated + recovered on next Open (safe).")
		} else {
			fmt.Fprintln(out, "OK — Open would succeed.")
		}
		return 0
	}
	fmt.Fprintln(errOut, "NOT OK — Open would refuse. Restore from backup, or operate on a copy.")
	return 1
}

// displayValue renders a value losslessly-ish for humans: valid UTF-8 as-is;
// otherwise a "<binary N bytes>" marker (or hex when --raw), so a gob session
// blob / compressed / encrypted value never corrupts the terminal or masquerades
// as text.
func displayValue(v []byte, raw bool) string {
	if utf8.Valid(v) {
		return string(v)
	}
	if raw {
		const hex = "0123456789abcdef"
		var b strings.Builder
		for _, c := range v {
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
		return b.String()
	}
	return fmt.Sprintf("<binary %d bytes>", len(v))
}

// jsonValue picks the most honest JSON representation of a stored value: parsed
// JSON if it is valid JSON (the Std.BlueDB / Codec convention), else the raw
// string if UTF-8, else a typed marker object — never invalid JSON.
func jsonValue(v []byte) any {
	if utf8.Valid(v) {
		var parsed any
		if json.Unmarshal(v, &parsed) == nil {
			return parsed
		}
		return string(v)
	}
	return map[string]any{"_binary_bytes": len(v)}
}

func confirm(stdin io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	r := bufio.NewReader(stdin)
	line, _ := r.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

const usage = `sky-bluedb — offline inspector + editor for a BlueDB store (app must be stopped)

  bluedb <path> stats
  bluedb <path> keys   [prefix] [--limit N] [--json]
  bluedb <path> scan   [prefix] [--limit N] [--json]
  bluedb <path> get    <key>              [--json] [--raw]
  bluedb <path> put    <key> <value>      [--stdin]
  bluedb <path> delete <key>              [--yes]
  bluedb <path> compact                   [--yes]
  bluedb <path> verify                    [--json]

A live store (running app) holds an exclusive lock; edit it through the app, not here.
verify is read-only (never opens/truncates); exits non-zero on corruption.
`
