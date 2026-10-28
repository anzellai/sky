//go:build !js

package rt

// child_signal.go — the lazy SIGINT / SIGTERM / SIGHUP handler that ends a
// program's children when the program is interrupted.
//
// A child started with Sky.Core.Process, and a managed llama-server
// (ai_engine.go), runs in a process group of its own, so the terminal's
// Ctrl-C does not reach it and it outlives a parent that the signal killed.
// The runtime's exit paths (ExitProcess, the shutdown drain) end every child,
// but a signal's default action runs none of them: a `Task.run` program such
// as `sky ai start` died at once on Ctrl-C (exit 130) or SIGTERM (exit 143)
// and left its engine running under pid 1.
//
// The handler is installed lazily, when the program registers its first
// managed child. A program that never starts one keeps the default signal
// actions exactly.
//
// There is one owner of the termination sequence at a time. The app shapes
// that own their process (Sky.Live, Sky.Http.Server, Sky.Tui, the embedded
// PostgreSQL supervisor, `sky run --profile`) install their own handler, and
// their drain ends the children. They hold a claim (acquireSignalOwner) for
// as long as their handler is installed, and while any claim is held this
// handler leaves the signal to them. An app marked `App.withEmbedded`
// installs no handler and claims nothing: a child it starts on a Sky.Live
// session does not install this handler either (spawnProcess), so the guest
// keeps its promise. A child the HOST program starts does install it.
//
// On a signal it runs the before-child-kill hooks (the AI engines), kills the
// children, stops an embedded database and exits 128 + the signal number
// (130 for SIGINT, 143 for SIGTERM, 129 for SIGHUP). A second signal during
// that cleanup exits at once with the same convention.

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var (
	childSignalOnce sync.Once
	// childSigMu guards signalOwners, childSigCh's registration and
	// ownerBridge.
	childSigMu sync.Mutex
	// signalOwners counts the app-shape handlers that own the termination
	// sequence right now.
	signalOwners int
	// childSigs is the set childSigCh is registered for.
	childSigs []os.Signal
	// ownerBridge stays registered while any claim is held, so the signals
	// stay caught while the lazy handler's channel is stopped (between a
	// claim and its owner's signal.Notify, and between the owner's
	// signal.Stop and the release). It is never read.
	ownerBridge chan os.Signal
	// childSignalExit / childSignalForceExit are the two exits, replaceable
	// by a test.
	childSignalExit      = ExitProcess
	childSignalForceExit = os.Exit
	// childSigCh is the handler's channel once installed. startChildSignalLoop
	// runs its loop, replaceable by a test that inspects the channel instead.
	childSigCh           chan os.Signal
	startChildSignalLoop = func(ch chan os.Signal) { go childSignalLoop(ch) }
)

// acquireSignalOwner records that the caller's own signal handler owns the
// termination sequence (and that its drain ends the children). Call it
// BEFORE signal.Notify, and the returned release after signal.Stop. Release
// is idempotent.
func acquireSignalOwner() (release func()) {
	childSigMu.Lock()
	signalOwners++
	if signalOwners == 1 {
		// The lazy handler's channel receives nothing while a claim is held.
		// A copy it received of the owner's signal would otherwise be read
		// by its loop at any later time, after the release too, and end the
		// process with a status the owner did not choose. signal.Stop waits
		// for a delivery in progress, so no copy is in flight once it returns.
		sigs := childSigs
		if childSigCh == nil {
			sigs = childSignalSet()
		}
		if len(sigs) > 0 {
			ownerBridge = make(chan os.Signal, 1)
			signal.Notify(ownerBridge, sigs...)
		}
		if childSigCh != nil {
			signal.Stop(childSigCh)
		}
	}
	childSigMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			childSigMu.Lock()
			defer childSigMu.Unlock()
			signalOwners--
			if signalOwners > 0 {
				return
			}
			if childSigCh != nil && len(childSigs) > 0 {
				signal.Notify(childSigCh, childSigs...)
			}
			if ownerBridge != nil {
				signal.Stop(ownerBridge)
				ownerBridge = nil
			}
		})
	}
}

// childSignalSet is the signals the handler takes: SIGINT, SIGTERM and
// SIGHUP, less any the program was started with ignored. `nohup` ignores
// SIGHUP and a shell's background job ignores SIGINT, and signal.Notify on an
// ignored signal would un-ignore it.
func childSignalSet() []os.Signal {
	var out []os.Signal
	for _, s := range []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(s) {
			out = append(out, s)
		}
	}
	return out
}

// ensureChildSignalHandler installs the handler once. Called when a managed
// child is registered.
func ensureChildSignalHandler() {
	childSignalOnce.Do(func() {
		sigs := childSignalSet()
		if len(sigs) == 0 {
			return
		}
		ch := make(chan os.Signal, 4)
		childSigMu.Lock()
		childSigCh, childSigs = ch, sigs
		if signalOwners == 0 {
			// Under a claim the release registers it.
			signal.Notify(ch, sigs...)
		}
		childSigMu.Unlock()
		startChildSignalLoop(ch)
	})
}

func signalExitCode(s os.Signal) int {
	if n, ok := s.(syscall.Signal); ok {
		return 128 + int(n)
	}
	return 130
}

func childSignalLoop(ch chan os.Signal) {
	for sig := range ch {
		childSigMu.Lock()
		owned := signalOwners > 0
		childSigMu.Unlock()
		if owned {
			// An app shape's handler received the same signal and runs the
			// drain, which ends the children.
			continue
		}
		code := signalExitCode(sig)
		go func() {
			// A second signal during the cleanup: leave now.
			s := <-ch
			childSignalForceExit(signalExitCode(s))
		}()
		childSignalExit(code)
		return
	}
}
