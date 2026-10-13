//go:build !js && !unix

package rt

import "os"

// aiWatchEngine: no process-group watcher off unix; an orphaned engine is
// reaped by the next start (aiReapOrphans).
func aiWatchEngine(pid int) *os.File { return nil }
