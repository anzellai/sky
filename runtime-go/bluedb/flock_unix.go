//go:build unix

package bluedb

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive, non-blocking advisory lock on f (held until f is
// closed). Two engines on one WAL file would corrupt it, so Open refuses the
// second. Advisory only — it guards against other bluedb opens, not arbitrary
// writers; a network filesystem may not honour it (keep the store on local disk).
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
