//go:build !js

package rt

import "testing"

// On a non-client (native) build, Native.geolocation has no location service to
// call, so it must return a Task that yields Err — never a silent zero-value
// coord or a panic. (The real js/wasm implementation is exercised e2e.)
func TestNativeGeolocationIsErrOffClient(t *testing.T) {
	thunk, ok := Native_geolocation(nil).(func() any)
	if !ok {
		t.Fatalf("Native_geolocation must return a Task thunk, got %T", Native_geolocation(nil))
	}
	res, ok := thunk().(SkyResult[any, any])
	if !ok {
		t.Fatalf("the task must yield a SkyResult, got %T", thunk())
	}
	if res.Tag != 1 {
		t.Errorf("off-client geolocation must be Err (Tag 1), got Tag %d", res.Tag)
	}
}

// NativeCoords must keep the field order + types the codegen alias relies on
// (Std_Native_Coords_R → rt.NativeCoords). Constructing it positionally is the
// compile-time guard.
func TestNativeCoordsShape(t *testing.T) {
	c := NativeCoords{37.42, -122.08, 12.0}
	if c.Lat != 37.42 || c.Lng != -122.08 || c.Accuracy != 12.0 {
		t.Error("NativeCoords fields are Lat, Lng, Accuracy in that order")
	}
}

// ShareContent must keep the field order + types the codegen alias relies on
// (Std_Native_ShareContent_R → rt.ShareContent). Constructing it positionally is
// the compile-time guard.
func TestShareContentShape(t *testing.T) {
	s := ShareContent{"Sky", "check this out", "https://sky-lang.org"}
	if s.Title != "Sky" || s.Text != "check this out" || s.Url != "https://sky-lang.org" {
		t.Error("ShareContent fields are Title, Text, Url in that order")
	}
}

// BatteryStatus must keep the field order + types the codegen alias relies on
// (Std_Native_BatteryStatus_R → rt.BatteryStatus). Positional construction is the
// compile-time guard.
func TestBatteryStatusShape(t *testing.T) {
	b := BatteryStatus{true, 0.85}
	if b.Charging != true || b.Level != 0.85 {
		t.Error("BatteryStatus fields are Charging, Level in that order")
	}
}

// PickedFile must keep the field order + types the codegen alias relies on
// (Std_Native_PickedFile_R → rt.PickedFile).
func TestPickedFileShape(t *testing.T) {
	f := PickedFile{"a.png", "image/png", "data:image/png;base64,AAAA"}
	if f.Name != "a.png" || f.Mime != "image/png" || f.DataUrl != "data:image/png;base64,AAAA" {
		t.Error("PickedFile fields are Name, Mime, DataUrl in that order")
	}
}

// Every Std.Native capability is client-only: off a client build each must
// return a Task that yields Err, never a silent zero-value or a panic.
func TestNativeCapabilitiesAreErrOffClient(t *testing.T) {
	cases := []struct {
		name   string
		kernel func(any) any
	}{
		{"clipboardWrite", Native_clipboardWrite},
		{"clipboardRead", Native_clipboardRead},
		{"vibrate", Native_vibrate},
		{"share", Native_share},
		{"storageGet", Native_storageGet},
		{"storageRemove", Native_storageRemove},
		{"isOnline", Native_isOnline},
		{"language", Native_language},
		{"setTitle", Native_setTitle},
		{"prefersDarkMode", Native_prefersDarkMode},
		{"openUrl", Native_openUrl},
		{"batteryStatus", Native_batteryStatus},
		{"pickFile", Native_pickFile},
		{"pickImage", Native_pickImage},
		{"capturePhoto", Native_capturePhoto},
	}
	assertErrThunk := func(name string, v any) {
		thunk, ok := v.(func() any)
		if !ok {
			t.Fatalf("Native_%s must return a Task thunk, got %T", name, v)
		}
		res, ok := thunk().(SkyResult[any, any])
		if !ok {
			t.Fatalf("Native_%s task must yield a SkyResult, got %T", name, thunk())
		}
		if res.Tag != 1 {
			t.Errorf("off-client Native_%s must be Err (Tag 1), got Tag %d", name, res.Tag)
		}
	}
	for _, c := range cases {
		assertErrThunk(c.name, c.kernel(nil))
	}
	// The 2-arg capabilities.
	assertErrThunk("storageSet", Native_storageSet(nil, nil))
	assertErrThunk("notify", Native_notify(nil, nil))
	assertErrThunk("bridge", Native_bridge(nil, nil))
}
