//go:build !js

package rt

import (
	"fmt"
	"os"
)

// routedPageStop reports a routed page mismatch on stderr and stops the
// server through ExitProcess, which stops an embedded PostgreSQL and the
// app's child processes first (a raw os.Exit would orphan them).
func routedPageStop(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	ExitProcess(1)
}
