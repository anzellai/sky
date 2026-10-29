package rt

// zip_safe.go — the safe ZIP reader and the deterministic writer behind
// Std.Zip.
//
// An archive is untrusted input, so the reader:
//
//   - never trusts a declared size: it counts the bytes while it inflates,
//     through a limited reader, and stops at the first limit crossed;
//   - refuses a compression ratio above maxRatio once an entry has inflated
//     past 1 MiB (a zip bomb inflates a small entry to gigabytes);
//   - refuses an unsafe name (zip-slip): absolute, a `..` segment, a
//     backslash, a drive letter, a NUL, or a name that repeats another after
//     normalisation; and refuses symlink and encrypted entries and methods
//     other than Store and Deflate. archive/zip checks each CRC.
//
// Failures are `Error` values with kind InvalidInput and a domain failure in
// the domain "zip" with one of the codes below.
//
// The archive/zip half (open, inflate, write) is in zip_archive.go, which is
// server only. This file has no build tag and must import nothing the Sky.Spa
// client does not already carry.

import (
	"path"
	"strconv"
	"strings"
)

const (
	zipCodeNotZip            = "not_zip"
	zipCodeTooManyEntries    = "too_many_entries"
	zipCodeEntryTooLarge     = "entry_too_large"
	zipCodeTotalTooLarge     = "total_too_large"
	zipCodeRatioExceeded     = "ratio_exceeded"
	zipCodeUnsafePath        = "unsafe_path"
	zipCodeDuplicatePath     = "duplicate_path"
	zipCodeEncrypted         = "encrypted"
	zipCodeUnsupportedMethod = "unsupported_method"
	zipCodeCorrupt           = "corrupt"
	// zipCodeServerOnly: the call ran in a Sky.Spa wasm client, which does
	// not carry archive/zip (zip_archive_js.go). Kind Unavailable.
	zipCodeServerOnly = "server_only"
)

// zipRatioFloor is the inflated size above which maxRatio applies (1 MiB):
// a small entry may compress very well without being a bomb.
const zipRatioFloor = 1 << 20

// zipLimits bounds one read.
type zipLimits struct {
	maxEntries    int
	maxEntryBytes int64
	maxTotalBytes int64
	maxRatio      int64
}

// zipDefaultLimits is `Std.Zip.defaultLimits`: 10,000 entries, 50 MiB an
// entry, 200 MiB in all, a ratio of 200.
var zipDefaultLimits = zipLimits{
	maxEntries:    10_000,
	maxEntryBytes: 50 << 20,
	maxTotalBytes: 200 << 20,
	maxRatio:      200,
}

type zipFailure struct {
	code  string
	msg   string
	attrs [][2]string
}

func zipFail(code, msg string, attrs ...[2]string) *zipFailure {
	return &zipFailure{code: code, msg: msg, attrs: attrs}
}

func (f *zipFailure) skyError() any {
	df := DomainFailure{Domain: "zip", Code: f.code, RetryAfterMs: -1, Attrs: f.attrs}
	kind := errKindInvalidInput
	if f.code == zipCodeServerOnly {
		kind = errKindUnavailable
	}
	return ErrDomain(kind, f.msg, df)
}

// zipCleanName checks one entry name and returns its normal form (no "./",
// no trailing "/"). A directory entry keeps isDir.
func zipCleanName(name string) (string, *zipFailure) {
	unsafe := func(why string) (string, *zipFailure) {
		return "", zipFail(zipCodeUnsafePath, "Zip: the entry name "+strconv.Quote(name)+" is not safe: "+why,
			[2]string{"path", name})
	}
	if name == "" {
		return unsafe("it is empty")
	}
	if strings.ContainsRune(name, 0) {
		return unsafe("it contains NUL")
	}
	if strings.ContainsRune(name, '\\') {
		return unsafe("it contains a backslash")
	}
	if strings.HasPrefix(name, "/") {
		return unsafe("it is absolute")
	}
	if len(name) >= 2 && name[1] == ':' && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) {
		return unsafe("it starts with a drive letter")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return unsafe("it has a .. segment")
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == "" {
		return unsafe("it names no file")
	}
	return clean, nil
}

// zipEntry is one file read from (or written to) an archive.
type zipEntry struct {
	path  string
	bytes []byte
}

// zipInfo is one entry of the central directory, as `Zip.list` reports it.
type zipInfo struct {
	path           string
	size           int
	compressedSize int
	isDir          bool
	modified       int // Unix milliseconds; 0 when the entry has no time
}
