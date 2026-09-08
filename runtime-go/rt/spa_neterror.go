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

// spaReportableTransportErr reports whether a Cmd.perform / Spa.postJson result
// is a COMPLETED round-trip that FAILED with a non-network error — a 5xx the
// backend answered (Spa.decodeResponse maps a non-2xx to Err), or a response
// body the shared codec could not decode. These are the transport failures the
// generated `Applied<Msg> (Err _) -> ( model, Cmd.none )` arm keeps the model
// for; before this they vanished with no user-visible effect and nothing an
// author could grep, because — unlike Sky.Live, whose server dispatch handles a
// failed effect — the auto-split client has no app-level result Msg to route the
// Err to (effects are synchronous inline `Task.run`, so the source carries no
// `Cmd.perform task ToMsg` and therefore no error case).
//
// It deliberately EXCLUDES the network class: a "cannot reach the server" Err
// already arms the retry overlay (spaIsNetworkErr), which is the user-visible
// signal for that class, so logging it again would double-signal. Kept
// build-tag-free (no js.Value) so it is unit-tested on the host, like
// spaIsNetworkErr; the console.error it gates lives in performTask
// (live_wasm.go).
func spaReportableTransportErr(result SkyResult[SkyADT, any]) bool {
	if result.Tag != 1 { // Ok — nothing to report
		return false
	}
	return !spaIsNetworkErr(result) // network is surfaced by the retry overlay
}

// spaTransportErrText renders the human message of a failed RPC result's Error
// (kind + message), for the loud transport-error log. Reuses the shared Error
// renderer so the text matches every other Error surface in the runtime.
func spaTransportErrText(result SkyResult[SkyADT, any]) string {
	return Basics_errorToStringT(result.ErrValue)
}
