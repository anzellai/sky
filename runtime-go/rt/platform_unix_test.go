//go:build unix

package rt

import "syscall"

// Process-group probes for the embedded-PostgreSQL gates. Split per platform
// so the test package also compiles for Windows (GOOS=windows go vet).
func testGetpgid(pid int) (int, error) { return syscall.Getpgid(pid) }
func testGetpgrp() int                 { return syscall.Getpgrp() }
