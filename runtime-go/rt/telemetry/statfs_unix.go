//go:build unix

package telemetry

import (
	"path/filepath"
	"syscall"
)

// freeBytesForPath returns the free AND total bytes on the filesystem holding
// `path` (statfs on the containing directory, so it works whether or not the
// file exists yet). ok=false when the path is empty or statfs fails. Total lets
// the caller key a danger flag on a free-space RATIO (free/total), which is the
// standard "disk near full" signal and captures ALL disk use — WAL, app data,
// other databases — not just the figure the app can name. Unix implementation;
// a portable stub covers other GOOS.
func freeBytesForPath(path string) (free, total int64, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return 0, 0, false
	}
	// Bavail = blocks available to a non-root process; Blocks = total; Bsize =
	// block size. int64 casts are safe for any real filesystem size.
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), true
}
