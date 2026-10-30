//go:build !js && !((darwin && !ios) || linux)

package rt

// releaseOrphanShm: no System V shared memory to release on this OS (the
// embedded PostgreSQL bundles are Linux and macOS only).
func releaseOrphanShm(shmid int) bool { return false }
