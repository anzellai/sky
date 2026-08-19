//go:build !unix

package telemetry

// freeBytesForPath has no portable non-Unix implementation (Windows would need
// GetDiskFreeSpaceEx). The size report degrades gracefully: free space is
// reported as unknown, absolute sizes + growth rate still ship.
func freeBytesForPath(path string) (free, total int64, ok bool) {
	return 0, 0, false
}
