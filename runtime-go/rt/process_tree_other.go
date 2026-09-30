//go:build unix && !linux && !darwin && !js

package rt

import "errors"

// process_tree_other.go — a Unix without a process listing the runtime knows
// (the BSDs, illumos): the sweep fails closed and kills nothing beyond the
// child's group.

var errNoProcessListing = errors.New("no process listing on this OS")

func procSnapshot() ([]procInfo, error)                { return nil, errNoProcessListing }
func procEnvValue(pid int, name string) (string, bool) { return "", false }
func procStartTime(pid int) uint64                     { return 0 }
func procKillVerified(p procInfo)                      {}
