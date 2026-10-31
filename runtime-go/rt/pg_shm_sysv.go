//go:build !js && ((darwin && !ios) || linux)

package rt

import (
	"os"

	"golang.org/x/sys/unix"
)

// releaseOrphanShm removes the System V shared-memory segment a dead
// postmaster left behind. Same rule as PostgreSQL's own start-up check: the
// segment must belong to this user and have nothing attached. Anything else
// is left alone.
func releaseOrphanShm(shmid int) bool {
	var ds unix.SysvShmDesc
	if _, err := unix.SysvShmCtl(shmid, unix.IPC_STAT, &ds); err != nil {
		return false
	}
	if ds.Perm.Uid != uint32(os.Getuid()) || ds.Nattch != 0 {
		return false
	}
	_, err := unix.SysvShmCtl(shmid, unix.IPC_RMID, nil)
	return err == nil
}
