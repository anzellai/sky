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
