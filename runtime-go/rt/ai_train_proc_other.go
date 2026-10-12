//go:build !js && !unix

package rt

import (
	"os"
	"syscall"
)

// aiTrainSysAttr: no sessions off Unix; the trainer is an ordinary child that
// the operating system lets outlive its parent.
func aiTrainSysAttr() *syscall.SysProcAttr { return nil }

// aiTrainSignal ends the trainer process (no process groups off Unix). The
// driver checkpoints on SIGTERM only on Unix; here a cancel is the cancel file
// the driver reads at every step, and this is the hard stop after the grace.
func aiTrainSignal(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if sig == syscall.SIGKILL {
		return p.Kill()
	}
	return nil
}
