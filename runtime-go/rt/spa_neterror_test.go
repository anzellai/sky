package rt

import "testing"

// The built-in Spa retry overlay must fire on a "can't reach the server"
// failure (ErrNetwork) but NOT on app-level errors the app should handle itself,
// nor on success.
func TestSpaIsNetworkErr(t *testing.T) {
	netErr, ok := ErrNetwork("http.post: failed to fetch").(SkyADT)
	if !ok {
		t.Fatalf("ErrNetwork should be a SkyADT, got %T", ErrNetwork(""))
	}
	if !spaIsNetworkErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: netErr}) {
		t.Error("a Network Err (backend unreachable) must arm the retry overlay")
	}

	decErr, _ := ErrDecode("bad json").(SkyADT)
	if spaIsNetworkErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: decErr}) {
		t.Error("a Decode Err is app-level and must NOT trigger the network overlay")
	}

	notFound, _ := ErrNotFound().(SkyADT)
	if spaIsNetworkErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: notFound}) {
		t.Error("a NotFound Err must NOT trigger the network overlay")
	}

	if spaIsNetworkErr(SkyResult[SkyADT, any]{Tag: 0, OkValue: "fine"}) {
		t.Error("an Ok result must NOT trigger the network overlay")
	}
}
