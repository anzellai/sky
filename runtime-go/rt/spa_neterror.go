package rt

// spaIsNetworkErr reports whether a Cmd.perform result is an Err carrying a
// Sky.Core.Error of kind Network — i.e. the Sky.Spa client could not REACH the
// server: a fetch rejection (backend down, DNS, CORS), which fetchBlocking maps
// to Err(ErrNetwork(...)) = makeError(1, "Network", …). These are the transient,
// retry-able failures the built-in connection overlay offers to retry.
//
// It deliberately does NOT fire on an app-level Err (a Decode failure, or a
// validated 4xx the backend answered) — those are for the app's own `update` to
// handle, not a blanket "retry the network" prompt. Kept build-tag-free (no
// js.Value) so it is unit-tested on the host; the overlay it gates lives in
// spa_neterror_wasm.go.
func spaIsNetworkErr(result SkyResult[SkyADT, any]) bool {
	if result.Tag != 1 { // 0 = Ok, 1 = Err
		return false
	}
	kind := AdtField(result.ErrValue, 0) // Fields[0] of Sky.Core.Error = the ErrorKind
	return EnumTagIs(kind, 1)            // 1 = Network
}

// spaAppendRetry adds a failed perform's retry action to the pending list, in
// failure order. Every failure is kept (SPA-7): an earlier failed perform is
// never replaced by a later one. Portable so the ordering is host-tested.
func spaAppendRetry(queue []func(), retry func()) []func() {
	if retry == nil {
		return queue
	}
	return append(queue, retry)
}

// spaRunRetries re-runs the pending retry actions in order. Each action blocks
// until its perform settles, so the next one starts only after it — the order
// the performs originally failed in is the order they are retried in.
func spaRunRetries(rs []func()) {
	for _, r := range rs {
		if r != nil {
			r()
		}
	}
}
