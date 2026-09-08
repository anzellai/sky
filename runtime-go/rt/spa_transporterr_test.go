package rt

import (
	"strings"
	"testing"
)

// Fix 4 regression — a failed RPC must NOT be swallowed. The generated client
// `update` maps `Applied<Msg> (Err _) -> ( model, Cmd.none )`, so a completed
// round-trip that FAILED (a 5xx, a body decode error) disappeared with no
// user-visible effect. The perform choke point now reports every such transport
// Err loudly (keeping the model). Network errors are excluded because the retry
// overlay already surfaces them.
//
// RED before the fix: spaReportableTransportErr did not exist and performTask
// had no branch for a non-network Err, so the failure was silent.
func TestSpaReportableTransportErr(t *testing.T) {
	// A 5xx the backend answered → Spa.decodeResponse yields an Unexpected Err.
	http500, _ := ErrUnexpected("HTTP 500: internal error").(SkyADT)
	if !spaReportableTransportErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: http500}) {
		t.Error("a non-network transport failure (5xx) must be reported, not swallowed")
	}

	// A response body the codec could not decode.
	decErr, _ := ErrDecode("bad json").(SkyADT)
	if !spaReportableTransportErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: decErr}) {
		t.Error("a decode failure of the RPC response must be reported, not swallowed")
	}

	// A network Err is surfaced by the retry overlay, so it is NOT re-reported here.
	netErr, _ := ErrNetwork("failed to fetch").(SkyADT)
	if spaReportableTransportErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: netErr}) {
		t.Error("a network Err is handled by the retry overlay and must NOT be double-signalled")
	}

	// A successful RPC has nothing to report.
	if spaReportableTransportErr(SkyResult[SkyADT, any]{Tag: 0, OkValue: "resp"}) {
		t.Error("an Ok result must not be reported as a transport error")
	}
}

// The loud log must carry the real Error message so an author can grep it.
func TestSpaTransportErrText(t *testing.T) {
	http500, _ := ErrUnexpected("HTTP 500: boom").(SkyADT)
	got := spaTransportErrText(SkyResult[SkyADT, any]{Tag: 1, ErrValue: http500})
	if !strings.Contains(got, "HTTP 500: boom") {
		t.Fatalf("transport error text should carry the backend message, got %q", got)
	}
}
