//go:build !unix

package bluedb

import "os"

// lockFile is a no-op on non-unix platforms (no advisory-lock guard). The
// same-process path-dedup in the kernel still prevents the common double-open.
func lockFile(f *os.File) error { return nil }
