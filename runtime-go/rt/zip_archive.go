//go:build !js

package rt

// zip_archive.go — the archive/zip half of Std.Zip: open, inflate under the
// limits, write, and the part-by-part archive Std.Office reads. zip_safe.go
// has the limits, the failures and the name check.
//
// Server only. This file is the one place the runtime imports archive/zip.
// Go runs the package init of every package a binary imports, whatever the
// linker drops, and archive/zip's init registers the flate writer, so an
// import in a file the Sky.Spa client compiles puts archive/zip and the whole
// deflate encoder into every wasm client. The js build gets zip_archive_js.go
// instead, and runtime-go/rt/wasm_client_deps_test.go fails when a
// server-only package reaches the js import graph again.

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"
)

// zipFixedTime is the modification time `write` stamps on every entry, so
// the same entries give the same bytes.
var zipFixedTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// zipOpen opens the archive and checks the entry count and every name.
func zipOpen(data string, maxEntries int) (*zip.Reader, []string, *zipFailure) {
	r, err := zip.NewReader(bytes.NewReader([]byte(data)), int64(len(data)))
	if err != nil {
		if errors.Is(err, zip.ErrFormat) {
			return nil, nil, zipFail(zipCodeNotZip, "Zip: the bytes are not a ZIP archive: "+err.Error())
		}
		return nil, nil, zipFail(zipCodeCorrupt, "Zip: the archive is corrupt: "+err.Error())
	}
	if len(r.File) > maxEntries {
		return nil, nil, zipFail(zipCodeTooManyEntries,
			"Zip: the archive has "+strconv.Itoa(len(r.File))+" entries, more than the limit of "+strconv.Itoa(maxEntries),
			[2]string{"entries", strconv.Itoa(len(r.File))}, [2]string{"limit", strconv.Itoa(maxEntries)})
	}
	names := make([]string, len(r.File))
	seen := make(map[string]bool, len(r.File))
	for i, f := range r.File {
		clean, fail := zipCleanName(f.Name)
		if fail != nil {
			return nil, nil, fail
		}
		if seen[clean] {
			return nil, nil, zipFail(zipCodeDuplicatePath, "Zip: the entry name "+strconv.Quote(f.Name)+" repeats another entry",
				[2]string{"path", f.Name})
		}
		seen[clean] = true
		names[i] = clean
	}
	return r, names, nil
}

func zipIsDir(f *zip.File) bool {
	return strings.HasSuffix(f.Name, "/") || f.Mode().IsDir()
}

// zipCheckEntry refuses an entry the reader will not inflate.
func zipCheckEntry(f *zip.File) *zipFailure {
	if f.Flags&0x1 != 0 {
		return zipFail(zipCodeEncrypted, "Zip: the entry "+strconv.Quote(f.Name)+" is encrypted", [2]string{"path", f.Name})
	}
	if f.Mode()&fs.ModeSymlink != 0 {
		return zipFail(zipCodeUnsafePath, "Zip: the entry "+strconv.Quote(f.Name)+" is a symbolic link", [2]string{"path", f.Name})
	}
	if f.Method != zip.Store && f.Method != zip.Deflate {
		return zipFail(zipCodeUnsupportedMethod,
			"Zip: the entry "+strconv.Quote(f.Name)+" uses compression method "+strconv.Itoa(int(f.Method))+" (only Store and Deflate are read)",
			[2]string{"path", f.Name}, [2]string{"method", strconv.Itoa(int(f.Method))})
	}
	return nil
}

// zipInflate reads one entry under the limits. used is what earlier entries
// inflated to.
func zipInflate(f *zip.File, lim zipLimits, used int64) ([]byte, *zipFailure) {
	rc, err := f.Open()
	if err != nil {
		return nil, zipFail(zipCodeCorrupt, "Zip: cannot open the entry "+strconv.Quote(f.Name)+": "+err.Error(), [2]string{"path", f.Name})
	}
	defer rc.Close()
	compressed := int64(f.CompressedSize64)
	var out bytes.Buffer
	buf := make([]byte, 32<<10)
	var n int64
	for {
		k, rerr := rc.Read(buf)
		if k > 0 {
			n += int64(k)
			if n > lim.maxEntryBytes {
				return nil, zipFail(zipCodeEntryTooLarge,
					"Zip: the entry "+strconv.Quote(f.Name)+" inflates to more than "+strconv.FormatInt(lim.maxEntryBytes, 10)+" bytes",
					[2]string{"path", f.Name}, [2]string{"limit", strconv.FormatInt(lim.maxEntryBytes, 10)})
			}
			if used+n > lim.maxTotalBytes {
				return nil, zipFail(zipCodeTotalTooLarge,
					"Zip: the archive inflates to more than "+strconv.FormatInt(lim.maxTotalBytes, 10)+" bytes",
					[2]string{"limit", strconv.FormatInt(lim.maxTotalBytes, 10)})
			}
			if n > zipRatioFloor && n > lim.maxRatio*max(compressed, 1) {
				return nil, zipFail(zipCodeRatioExceeded,
					"Zip: the entry "+strconv.Quote(f.Name)+" inflates more than "+strconv.FormatInt(lim.maxRatio, 10)+" times its compressed size",
					[2]string{"path", f.Name}, [2]string{"limit", strconv.FormatInt(lim.maxRatio, 10)})
			}
			out.Write(buf[:k])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, zipFail(zipCodeCorrupt, "Zip: the entry "+strconv.Quote(f.Name)+" is corrupt: "+rerr.Error(), [2]string{"path", f.Name})
		}
	}
	return out.Bytes(), nil
}

// zipReadAll reads every file entry (directories are left out).
func zipReadAll(data string, lim zipLimits) ([]zipEntry, *zipFailure) {
	r, names, fail := zipOpen(data, lim.maxEntries)
	if fail != nil {
		return nil, fail
	}
	out := []zipEntry{}
	var used int64
	for i, f := range r.File {
		if zipIsDir(f) {
			continue
		}
		if fail := zipCheckEntry(f); fail != nil {
			return nil, fail
		}
		b, fail := zipInflate(f, lim, used)
		if fail != nil {
			return nil, fail
		}
		used += int64(len(b))
		out = append(out, zipEntry{path: names[i], bytes: b})
	}
	return out, nil
}

// zipReadOne reads the file entry named want (after normalisation); found is
// false when there is none.
func zipReadOne(data string, lim zipLimits, want string) ([]byte, bool, *zipFailure) {
	r, names, fail := zipOpen(data, lim.maxEntries)
	if fail != nil {
		return nil, false, fail
	}
	want = path.Clean(want)
	for i, f := range r.File {
		if names[i] != want || zipIsDir(f) {
			continue
		}
		if fail := zipCheckEntry(f); fail != nil {
			return nil, false, fail
		}
		b, fail := zipInflate(f, lim, 0)
		if fail != nil {
			return nil, false, fail
		}
		return b, true, nil
	}
	return nil, false, nil
}

// zipWriteAll builds an archive. Every entry gets zipFixedTime, so the
// output depends only on the entries and the method. A path ending in "/"
// with no bytes is a directory entry.
func zipWriteAll(entries []zipEntry, deflate bool) ([]byte, *zipFailure) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		clean, fail := zipCleanName(e.path)
		if fail != nil {
			return nil, fail
		}
		if seen[clean] {
			return nil, zipFail(zipCodeDuplicatePath, "Zip.write: the path "+strconv.Quote(e.path)+" is given twice",
				[2]string{"path", e.path})
		}
		seen[clean] = true
		isDir := strings.HasSuffix(e.path, "/") && len(e.bytes) == 0
		name := clean
		method := zip.Store
		if isDir {
			name += "/"
		} else if deflate {
			method = zip.Deflate
		}
		fh := &zip.FileHeader{Name: name, Method: method, Modified: zipFixedTime}
		if isDir {
			fh.SetMode(fs.ModeDir | 0o755)
		} else {
			fh.SetMode(0o644)
		}
		fw, err := w.CreateHeader(fh)
		if err != nil {
			return nil, zipFail(zipCodeCorrupt, "Zip.write: "+err.Error())
		}
		if !isDir {
			if _, err := fw.Write(e.bytes); err != nil {
				return nil, zipFail(zipCodeCorrupt, "Zip.write: "+err.Error())
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, zipFail(zipCodeCorrupt, "Zip.write: "+err.Error())
	}
	return buf.Bytes(), nil
}

// zipArchive is an archive opened once and read part by part, as an Office
// package is: every name is checked at open (zipOpen), and the inflated size
// of the parts read so far counts against maxTotalBytes across calls.
type zipArchive struct {
	lim   zipLimits
	files map[string]*zip.File // by normalised name; directories left out
	used  int64
}

// zipOpenArchive opens data under lim for reading by part name.
func zipOpenArchive(data string, lim zipLimits) (*zipArchive, *zipFailure) {
	r, names, fail := zipOpen(data, lim.maxEntries)
	if fail != nil {
		return nil, fail
	}
	a := &zipArchive{lim: lim, files: make(map[string]*zip.File, len(r.File))}
	for i, f := range r.File {
		if !zipIsDir(f) {
			a.files[names[i]] = f
		}
	}
	return a, nil
}

// has reports whether the archive holds the file name.
func (a *zipArchive) has(name string) bool {
	_, ok := a.files[path.Clean(name)]
	return ok
}

// read inflates the file name under the limits; found is false when the
// archive has no such file.
func (a *zipArchive) read(name string) ([]byte, bool, *zipFailure) {
	f, ok := a.files[path.Clean(name)]
	if !ok {
		return nil, false, nil
	}
	if fail := zipCheckEntry(f); fail != nil {
		return nil, false, fail
	}
	b, fail := zipInflate(f, a.lim, a.used)
	if fail != nil {
		return nil, false, fail
	}
	a.used += int64(len(b))
	return b, true, nil
}

// zipListAll reads the central directory only, nothing inflated. O(entries).
func zipListAll(data string, maxEntries int) ([]zipInfo, *zipFailure) {
	r, names, fail := zipOpen(data, maxEntries)
	if fail != nil {
		return nil, fail
	}
	out := make([]zipInfo, len(r.File))
	for i, f := range r.File {
		var modified int
		if !f.Modified.IsZero() {
			modified = int(f.Modified.UnixMilli())
		}
		out[i] = zipInfo{
			path:           names[i],
			size:           int(f.UncompressedSize64),
			compressedSize: int(f.CompressedSize64),
			isDir:          zipIsDir(f),
			modified:       modified,
		}
	}
	return out, nil
}
