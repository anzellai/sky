//go:build unix && !js

package rt

import (
	"os"
	"syscall"
)

// aiDiskAvailable is the free bytes on the volume holding dir (0 when it
// cannot be read).
func aiDiskAvailable(dir string) int64 {
	_ = os.MkdirAll(dir, 0o755)
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
