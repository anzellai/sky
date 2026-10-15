//go:build !js

package rt

// System tools the runtime runs, found without trusting PATH.
//
// A process can start with a stripped PATH: a systemd unit with
// `Environment=PATH=`, a launchd job, a supervisor that clears the environment
// before exec. A bare `exec.Command("sysctl", …)` then fails with "executable
// file not found", and the caller reads that as "no answer". On macOS that
// turned machine-memory detection off ("machine memory could not be detected;
// left on the Go defaults"), so a production process ran without its derived
// GOMEMLIMIT and its embedded cluster was sized as an unknown machine.
//
// Each tool is tried at its usual absolute paths first, then on PATH.

import (
	"os"
	"os/exec"
)

// Absolute locations of the system tools the runtime runs. macOS keeps sysctl
// in /usr/sbin; Linux distributions keep it in /usr/sbin or /sbin. ps is in
// /bin on macOS and on most Linux images, /usr/bin on some.
var (
	sysctlPaths = []string{"/usr/sbin/sysctl", "/sbin/sysctl"}
	psPaths     = []string{"/bin/ps", "/usr/bin/ps"}
)

// systemTool returns the first candidate that is an executable regular file,
// else the PATH lookup of name, else name itself (so the exec reports the real
// "not found" error).
func systemTool(name string, candidates ...string) string {
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}
