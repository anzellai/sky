//go:build unix && !js && !linux && !darwin

package rt

import (
	"errors"
	"os"
)

// procOpenPTY: PTY allocation is implemented for Linux and macOS. Other Unix
// systems get a clear error, never a panic.
func procOpenPTY(cols, rows int) (master, slave *os.File, err error) {
	return nil, nil, errors.New("a PTY is supported on Linux and macOS only")
}
