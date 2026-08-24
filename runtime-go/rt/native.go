package rt

// NativeCoords is the Go counterpart of Std.Native.Coords
// (`{ lat : Float, lng : Float, accuracy : Float }`). The codegen aliases the
// emitted `Std_Native_Coords_R` record struct to this type
// (lower.rs runtime_backed_record), so the value the Native_geolocation kernel
// produces narrows to it by a reflection-free assertion. The field ORDER and Go
// types MUST stay identical to the Sky record (lat, lng, accuracy : Float →
// Lat, Lng, Accuracy float64) or the alias is unsound.
type NativeCoords struct {
	Lat      float64
	Lng      float64
	Accuracy float64
}
