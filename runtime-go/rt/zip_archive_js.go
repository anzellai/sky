//go:build js

package rt

// zip_archive_js.go — the Sky.Spa wasm client's twins of zip_archive.go.
//
// The client does not carry archive/zip (see zip_archive.go's header for why
// an import there would put it, and the deflate encoder, into every wasm
// client). The Sky.Spa split classifies every `Zip_` and `Office_` kernel as
// a server effect (spa_partition.rs), so a correct build never reaches these
// on the client. If one does, the call is a classified Unavailable Err in the
// domain "zip" with the code "server_only", never a panic.

func zipServerOnly(what string) *zipFailure {
	return zipFail(zipCodeServerOnly,
		what+" runs on the server only: a Sky.Spa client does not carry archive/zip")
}

func zipListAll(data string, maxEntries int) ([]zipInfo, *zipFailure) {
	return nil, zipServerOnly("Zip.list")
}

func zipReadAll(data string, lim zipLimits) ([]zipEntry, *zipFailure) {
	return nil, zipServerOnly("Zip.read")
}

func zipReadOne(data string, lim zipLimits, want string) ([]byte, bool, *zipFailure) {
	return nil, false, zipServerOnly("Zip.readEntry")
}

func zipWriteAll(entries []zipEntry, deflate bool) ([]byte, *zipFailure) {
	return nil, zipServerOnly("Zip.write")
}

// zipArchive has no parts on the client: zipOpenArchive always fails, so no
// value of it is ever read.
type zipArchive struct{}

func zipOpenArchive(data string, lim zipLimits) (*zipArchive, *zipFailure) {
	return nil, zipServerOnly("Std.Office")
}

func (a *zipArchive) has(name string) bool { return false }

func (a *zipArchive) read(name string) ([]byte, bool, *zipFailure) {
	return nil, false, zipServerOnly("Std.Office")
}
