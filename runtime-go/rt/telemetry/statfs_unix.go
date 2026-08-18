//go:build unix

package telemetry

import (
	"path/filepath"
	"syscall"
)

// freeBytesForPath returns the free bytes available to an unprivileged writer
// on the filesystem holding `path` (statfs on the containing directory, so it
// works whether or not the file exists yet). ok=false when the path is empty
// or statfs fails. Unix implementation; a portable stub covers other GOOS.
func freeBytesForPath(path string) (free int64, ok bool) {
	if path == "" {
		return 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return 0, false
	}
	// Bavail = blocks available to a non-root process; Bsize = block size.
	// int64 cast is safe for any real filesystem size.
	return int64(st.Bavail) * int64(st.Bsize), true
}
