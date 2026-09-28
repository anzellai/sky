//go:build !js && !unix

package rt

import "errors"

// There are no process groups here; the gates that ask are Unix-only in
// practice (the embedded cluster does not run on this OS).
func testGetpgid(pid int) (int, error) { return 0, errors.New("no process groups on this OS") }
func testGetpgrp() int                 { return -1 }
