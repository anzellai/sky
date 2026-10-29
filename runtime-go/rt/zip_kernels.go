package rt

// zip_kernels.go — the Std.Zip kernels (sky-stdlib/Std/Zip.sky).
//
// Each returns a `Result Error _` value directly, not a Task: reading and
// writing an archive parses and builds bytes and has no outside effect (the
// "Result for parsing" rule). The Limits record crosses the boundary as four
// Ints and the Method as a Bool, so the kernels carry no Sky ADT or record in.
// A record out (Entry, EntryInfo) is a map[string]any the call site narrows,
// like Image.dimensions.
//
// The Sky.Spa split classifies every `Zip_` kernel as a server effect
// (spa_partition.rs EFFECT_KERNELS).

import "strconv"

func zipLimitsOf(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio any) (zipLimits, any) {
	lim := zipLimits{
		maxEntries:    AsInt(maxEntries),
		maxEntryBytes: int64(AsInt(maxEntryBytes)),
		maxTotalBytes: int64(AsInt(maxTotalBytes)),
		maxRatio:      int64(AsInt(maxRatio)),
	}
	if lim.maxEntries <= 0 || lim.maxEntryBytes <= 0 || lim.maxTotalBytes <= 0 || lim.maxRatio <= 0 {
		return lim, Err[any, any](ErrInvalidInput("Zip: every limit must be positive, got maxEntries " +
			strconv.Itoa(lim.maxEntries) + ", maxEntryBytes " + strconv.FormatInt(lim.maxEntryBytes, 10) +
			", maxTotalBytes " + strconv.FormatInt(lim.maxTotalBytes, 10) + ", maxRatio " + strconv.FormatInt(lim.maxRatio, 10)))
	}
	return lim, nil
}

func zipEntriesValue(entries []zipEntry) any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = map[string]any{"path": e.path, "bytes": string(e.bytes)}
	}
	return out
}

// Zip_list is `Zip.list : Bytes -> Result Error (List EntryInfo)`: the
// central directory only, nothing inflated. O(entries).
func Zip_list(data any) any {
	r, names, fail := zipOpen(asBytesString(data), zipDefaultLimits.maxEntries)
	if fail != nil {
		return Err[any, any](fail.skyError())
	}
	out := make([]any, len(r.File))
	for i, f := range r.File {
		var modified int
		if !f.Modified.IsZero() {
			modified = int(f.Modified.UnixMilli())
		}
		out[i] = map[string]any{
			"path":           names[i],
			"size":           int(f.UncompressedSize64),
			"compressedSize": int(f.CompressedSize64),
			"isDirectory":    zipIsDir(f),
			"modified":       modified,
		}
	}
	return Ok[any, any](out)
}

// Zip_read is the primitive behind `Zip.read` / `Zip.readWith`.
func Zip_read(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio, data any) any {
	lim, bad := zipLimitsOf(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio)
	if bad != nil {
		return bad
	}
	entries, fail := zipReadAll(asBytesString(data), lim)
	if fail != nil {
		return Err[any, any](fail.skyError())
	}
	return Ok[any, any](zipEntriesValue(entries))
}

// Zip_readEntry is the primitive behind `Zip.readEntry`.
func Zip_readEntry(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio, name, data any) any {
	lim, bad := zipLimitsOf(maxEntries, maxEntryBytes, maxTotalBytes, maxRatio)
	if bad != nil {
		return bad
	}
	b, found, fail := zipReadOne(asBytesString(data), lim, AsString(name))
	if fail != nil {
		return Err[any, any](fail.skyError())
	}
	if !found {
		return Ok[any, any](Nothing[any]())
	}
	return Ok[any, any](Just[any](string(b)))
}

// Zip_write is the primitive behind `Zip.write` / `Zip.writeWith`: deflate
// is True for `Deflate`.
func Zip_write(deflate, entries any) any {
	var in []zipEntry
	for _, e := range asList(entries) {
		in = append(in, zipEntry{
			path:  AsString(recordField(e, "Path", "path")),
			bytes: []byte(asBytesString(recordField(e, "Bytes", "bytes"))),
		})
	}
	out, fail := zipWriteAll(in, AsBool(deflate))
	if fail != nil {
		return Err[any, any](fail.skyError())
	}
	return Ok[any, any](string(out))
}
