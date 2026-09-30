//go:build !js

package rt

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"
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
	// retired holds the Sky.Live run ids (session ids) whose snapshot was
	// discarded: the session rotated to a new id, was evicted by the
	// revocation gate, or was ended. A persist for a retired id is dropped,
	// so a fire-and-forget write that was already queued when the id retired
	// can not bring the snapshot back. Value: the retire time, for pruning.
	retired   sync.Map
	retiredMu sync.Mutex
	retiredN  int
	// setupOnce runs the wiring's setup (create the snapshot table) before
	// a discard in a process that has not booted a run yet.
	setupOnce sync.Once
}

// durableStripes serialise the snapshot writes of one run id against its
// retirement. A persist takes the stripe of its run id, checks `retired`,
// and runs the write under the stripe; retire takes the same stripe, marks
// the id retired and deletes the snapshot. So no persist that passed the
// check can land after the delete. Striped (not one lock per id) so the
// table has a fixed size; two ids that share a stripe only serialise their
// writes.
var durableStripes [64]sync.Mutex

func durableStripe(runId string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runId))
	return &durableStripes[h.Sum32()%uint32(len(durableStripes))]
}

// durableRetiredKeep is how long a retired id is remembered. A queued
// persist runs within milliseconds; ten minutes is a wide margin.
const durableRetiredKeep = 10 * time.Minute

// durableCtxOf builds a ctx from a config's "Durable" field, or nil when the app
// is not durable.
func durableCtxOf(wiring any) *durableCtx {
	if wiring == nil {
		return nil
	}
	// A non-durable app carries a no-op wiring (enabled=False) rather than a
	// nil field, so gate on the flag.
	if enabled, ok := fieldOrNil(wiring, "Enabled").(bool); ok && !enabled {
		return nil
	}
	rid, _ := fieldOrNil(wiring, "RunId").(string)
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
	if d == nil || d.isRetired(runId) {
		return initModel
	}
	if setupTask := fieldOrNil(d.wiring, "Setup"); setupTask != nil {
		sky_call(setupTask, nil)
	}
	restoreFn := fieldOrNil(d.wiring, "Restore")
	applyFn := fieldOrNil(d.wiring, "ApplyRestore")
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
	if applyReq := fieldOrNil(d.wiring, "ApplyRestoreRequest"); applyReq != nil && req != nil {
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
	persistFn := fieldOrNil(d.wiring, "Persist")
	if persistFn == nil {
		return
	}
	d.runPersist(d.runId, SkyCall(persistFn, d.runId, model))
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
	persistFn := fieldOrNil(d.wiring, "Persist")
	if persistFn == nil {
		return
	}
	task := SkyCall(persistFn, runId, model)
	// goSky, not safeGo: safeGo is the terminal runtime's wrapper and ends the
	// process on a panic. runPersist recovers a panicking write itself.
	goSky("Durable.persist", func() {
		mu := durableStripe(runId)
		mu.Lock()
		defer mu.Unlock()
		if d.isRetired(runId) {
			return
		}
		d.runPersist(runId, task)
	})
}

// persistSync snapshots model for runId and waits for the write. Used by the
// session-id rotation, which must have the snapshot under the new id before
// it deletes the old one.
func (d *durableCtx) persistSync(runId string, model any) {
	if d == nil || runId == "" || d.isSuspended(runId) {
		return
	}
	persistFn := fieldOrNil(d.wiring, "Persist")
	if persistFn == nil {
		return
	}
	mu := durableStripe(runId)
	mu.Lock()
	defer mu.Unlock()
	if d.isRetired(runId) {
		return
	}
	d.runPersist(runId, SkyCall(persistFn, runId, model))
}

// retire drops the snapshot of runId and refuses every later persist for
// it. Synchronous: when it returns, the snapshot is gone and no queued write
// can recreate it. A suspended run (its snapshot failed to restore) keeps
// its snapshot: it is the only copy of the user's state (see `suspended`).
func (d *durableCtx) retire(runId string) {
	if d == nil || runId == "" {
		return
	}
	mu := durableStripe(runId)
	mu.Lock()
	defer mu.Unlock()
	d.markRetired(runId)
	if d.isSuspended(runId) {
		return
	}
	d.setupOnce.Do(func() {
		if setupTask := fieldOrNil(d.wiring, "Setup"); setupTask != nil {
			sky_call(setupTask, nil)
		}
	})
	discardFn := fieldOrNil(d.wiring, "Discard")
	if discardFn == nil {
		logEmit(logLevelError, "error",
			"Durable: the wiring has no discard function, so the snapshot of a retired session could not be deleted",
			map[string]any{"class": "DurableDiscardMissing", "runId": runId})
		return
	}
	res := sky_call(SkyCall(discardFn, runId), nil)
	if isErrResult(res) {
		logEmit(logLevelError, "error",
			fmt.Sprintf("Durable: deleting the snapshot of retired run %q failed (%v)", runId, extractErrResultValue(res)),
			map[string]any{"class": "DurableDiscardFailed", "runId": runId})
	}
}

// rotate moves the durable state of a Sky.Live session from oldId to newId:
// the current model is written under newId, then oldId is retired (its
// snapshot deleted, later writes refused). A suspended old run keeps its
// snapshot where it is, and the new id is suspended too, so nothing
// overwrites the kept copy.
func (d *durableCtx) rotate(oldId, newId string, model any) {
	if d == nil || oldId == "" || newId == "" || oldId == newId {
		return
	}
	if d.isSuspended(oldId) {
		d.suspended.Store(newId, true)
		d.markRetired(oldId)
		return
	}
	if model != nil {
		d.persistSync(newId, model)
	}
	d.retire(oldId)
}

func (d *durableCtx) isRetired(runId string) bool {
	_, ok := d.retired.Load(runId)
	return ok
}

func (d *durableCtx) markRetired(runId string) {
	now := time.Now()
	if _, loaded := d.retired.LoadOrStore(runId, now); loaded {
		return
	}
	d.retiredMu.Lock()
	d.retiredN++
	prune := d.retiredN > 4096
	if prune {
		d.retiredN = 0
	}
	d.retiredMu.Unlock()
	if !prune {
		return
	}
	cut := now.Add(-durableRetiredKeep)
	kept := 0
	d.retired.Range(func(k, v any) bool {
		if t, ok := v.(time.Time); ok && t.Before(cut) {
			d.retired.Delete(k)
		} else {
			kept++
		}
		return true
	})
	d.retiredMu.Lock()
	d.retiredN += kept
	d.retiredMu.Unlock()
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

// runPersist runs one snapshot write and recovers a panic in it. A write
// panics when the Model cannot be encoded (a NaN or infinite Float reaching
// Json.Encode is enough). The panic is logged classified and the run is
// SUSPENDED: no later write may replace the stored snapshot, and the process
// keeps serving. Before this, the async write ran on safeGo, the terminal
// runtime's wrapper, which answers any panic with ExitProcess(2), so one
// unencodable update ended a Sky.Live server.
func (d *durableCtx) runPersist(runId string, task any) {
	defer func() {
		if r := recover(); r != nil {
			d.suspended.Store(runId, true)
			logClassifiedPanic("sky.durable", "Durable.persist", r)
			tuiWarn("durable", fmt.Sprintf(
				"Durable: writing the snapshot for run %q failed (%v). This run writes no more snapshots; the last stored one is kept.",
				runId, r))
		}
	}()
	sky_call(task, nil)
}
