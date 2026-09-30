//go:build !js

package rt

// startup_warnings.go — lines a subsystem adds to the start-up report before
// the listener announces itself (for example a Sky.Spa signing key that
// production cannot persist, spa_session_secret.go). They are printed under
// the bind line in every mode, production included, because they name a
// deployment problem an operator must fix.

import "sync"

var (
	startupWarningsMu sync.Mutex
	startupWarnings   []string
)

// addStartupWarning records one report line (deduplicated).
func addStartupWarning(line string) {
	startupWarningsMu.Lock()
	defer startupWarningsMu.Unlock()
	for _, l := range startupWarnings {
		if l == line {
			return
		}
	}
	startupWarnings = append(startupWarnings, line)
}

// startupWarningLines returns the recorded lines, formatted as report lines.
func startupWarningLines() []string {
	startupWarningsMu.Lock()
	defer startupWarningsMu.Unlock()
	out := make([]string, 0, len(startupWarnings))
	for _, l := range startupWarnings {
		out = append(out, "  warning      "+l)
	}
	return out
}
