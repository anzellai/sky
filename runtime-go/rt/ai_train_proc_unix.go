//go:build !js && unix

package rt

import (
	"os"
	"syscall"
)

// aiTrainSysAttr starts the trainer in a session of its own (Setsid): it has
// no controlling terminal, so a Ctrl-C in the terminal that ran `sky ai train`
// reaches the watcher and not the trainer, and it outlives the program that
// launched it. Its pid is its process group, so a cancel signals every worker
// it started.
func aiTrainSysAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// aiTrainSignal sends sig to the trainer's process group.
func aiTrainSignal(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return procSignalGroup(p, sig)
}
