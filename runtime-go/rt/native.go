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

// ShareContent is the Go counterpart of Std.Native.ShareContent
// (`{ title : String, text : String, url : String }`). Same alias mechanism as
// NativeCoords, but in the ARGUMENT direction: the codegen aliases the emitted
// `Std_Native_ShareContent_R` record struct to this type
// (lower.rs runtime_backed_record), so the Sky record a caller builds arrives at
// the Native_share kernel as a plain `rt.ShareContent` it narrows to by a
// reflection-free assertion. Field ORDER + Go types MUST match the Sky record's
// declaration order (title, text, url : String → Title, Text, Url string).
type ShareContent struct {
	Title string
	Text  string
	Url   string
}

// BatteryStatus is the Go counterpart of Std.Native.BatteryStatus
// (`{ charging : Bool, level : Float }`). Runtime-backed record
// (Std_Native_BatteryStatus_R → rt.BatteryStatus, lower.rs), so the
// Native_batteryStatus kernel's value narrows reflection-free. Field ORDER +
// types MUST match the Sky record's declaration order (charging, level →
// Charging bool, Level float64).
type BatteryStatus struct {
	Charging bool
	Level    float64
}

// PickedFile is the Go counterpart of Std.Native.PickedFile
// (`{ name : String, mime : String, dataUrl : String }`) — a file the user chose
// from the picker / camera / gallery, its bytes carried as a base64 `data:` URL
// (usable directly as an `Ui.image` src or decoded with Encoding.base64Decode).
// Runtime-backed record (Std_Native_PickedFile_R → rt.PickedFile, lower.rs);
// field ORDER + types match the Sky record (name, mime, dataUrl → Name, Mime,
// DataUrl string).
type PickedFile struct {
	Name    string
	Mime    string
	DataUrl string
}
