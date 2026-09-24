//go:build !js

package rt

import (
	"fmt"
	"sync"
)

// Zero-annotation durable TEA. `Std.App.withDurable` threads a durable wiring
// record (setup / restore / persist / applyRestore / runId, built from the pure-
// Sky Std.Durable snapshot primitives) into a backend loop's config. The loop
// restores the Model when a session/app starts and snapshots it after each
// update, with no change to the user's model / msg / update.
//
// The wiring is a Sky record read field-by-field via rt.Field, and its closures
// are invoked through the same sky_call path the loop already uses for
// init/update/view and Cmd.perform — so no new narrowing site is introduced.

// durableCtx holds the wiring plus the run id a given loop uses (a fixed id for
// Cli/Tui; the session id for Live, passed per call).
type durableCtx struct {
	wiring any
	runId  string
	// suspended holds the run ids whose stored snapshot failed to restore
	// (the Model no longer decodes it, or the read failed). Such a run
	// boots from init and NEVER writes a snapshot, so the stored one is
	// kept byte-for-byte until the operator migrates or removes it. A
	// silent reset-and-overwrite would destroy the only copy of the
	// user's state.
	suspended sync.Map
}

// durableCtxOf builds a ctx from a config's "Durable" field, or nil when the app
// is not durable.
func durableCtxOf(wiring any) *durableCtx {
	if wiring == nil {
		return nil
	}
	// A non-durable app carries a no-op wiring (enabled=False) rather than a
	// nil field, so gate on the flag.
	if enabled, ok := Field(wiring, "Enabled").(bool); ok && !enabled {
		return nil
	}
	rid, _ := Field(wiring, "RunId").(string)
	return &durableCtx{wiring: wiring, runId: rid}
}

// boot runs setup (create the snapshot table, once) then restores the model for
// runId, returning the restored model or initModel when there is no snapshot.
func (d *durableCtx) boot(runId string, initModel any) any {
	return d.bootWith(runId, nil, initModel)
}

// bootWith is boot with the request that created a Sky.Live session (nil
// for Cli / Tui).
func (d *durableCtx) bootWith(runId string, req any, initModel any) any {
	if d == nil {
		return initModel
	}
	if setupTask := Field(d.wiring, "Setup"); setupTask != nil {
		sky_call(setupTask, nil)
	}
	restoreFn := Field(d.wiring, "Restore")
	applyFn := Field(d.wiring, "ApplyRestore")
	if restoreFn == nil || applyFn == nil {
		return initModel
	}
	// restore : runId -> Task Error (Maybe model); force it, then let the Sky
	// applyRestore adapter turn the Result into the model (Go never inspects a
	// Result/Maybe itself).
	res := sky_call(SkyCall(restoreFn, runId), nil)
	if isErrResult(res) {
		d.suspended.Store(runId, true)
		durableReportRestoreFailure(runId, fmt.Sprintf("%v", extractErrResultValue(res)))
		return initModel
	}
	// Sky.Live: a request-derived Model (App.withRequest) must not be
	// replaced by the snapshot's copy of an EARLIER request's fields. The
	// Live wiring carries `applyRestoreRequest`, which applies the snapshot
	// and then re-runs the request hook over it for the current request.
	if applyReq := Field(d.wiring, "ApplyRestoreRequest"); applyReq != nil && req != nil {
		return SkyCall(applyReq, req, res, initModel)
	}
	return SkyCall(applyFn, res, initModel)
}

// bootRequest is boot for a Sky.Live fresh session: req is the request that
// created the session (the same value Live's init received), so a restore
// can re-derive the fields App.withRequest takes from the CURRENT request.
func (d *durableCtx) bootRequest(runId string, req any, initModel any) any {
	return d.bootWith(runId, req, initModel)
}

// bootFixed / persistFixed use the ctx's own fixed run id (Cli / Tui). Both are
// nil-safe so a non-durable app is a no-op.
func (d *durableCtx) bootFixed(initModel any) any {
	if d == nil {
		return initModel
	}
	return d.boot(d.runId, initModel)
}

// persistFixed snapshots synchronously (Cli / Tui). These backends are low
// frequency and exit on EOF, so a synchronous write guarantees the last state is
// saved before the loop can return — no fire-and-forget race on exit.
func (d *durableCtx) persistFixed(model any) {
	if d == nil {
		return
	}
	if d.isSuspended(d.runId) {
		return
	}
	persistFn := Field(d.wiring, "Persist")
	if persistFn == nil {
		return
	}
	sky_call(SkyCall(persistFn, d.runId, model), nil)
}

// persist snapshots model for runId, fire-and-forget (write-if-newer, so a lost
// race cannot regress the stored state).
func (d *durableCtx) persist(runId string, model any) {
	if d == nil {
		return
	}
	if d.isSuspended(runId) {
		return
	}
	persistFn := Field(d.wiring, "Persist")
	if persistFn == nil {
		return
	}
	task := SkyCall(persistFn, runId, model)
	safeGo("Durable.persist", func() { sky_call(task, nil) })
}

func (d *durableCtx) isSuspended(runId string) bool {
	_, ok := d.suspended.Load(runId)
	return ok
}

// durableReportRestoreFailure logs the classified DurableRestoreFailed error
// (and queues it for a terminal app's exit summary, since the alternate
// screen hides a log line written while it is up).
func durableReportRestoreFailure(runId, reason string) {
	msg := fmt.Sprintf("DurableRestoreFailed: the durable snapshot for run %q could not be restored (%s). "+
		"Booting from init. The stored snapshot is kept unchanged, and this run does not write snapshots "+
		"until the snapshot is migrated or removed.", runId, reason)
	logEmit(logLevelError, "error", msg, map[string]any{"class": "DurableRestoreFailed", "runId": runId})
	tuiWarn("durable", msg)
}
