//go:build js

package rt

// xml_stream_js.go — the Sky.Spa wasm client's xmlStreamSafe.
//
// The client does not carry encoding/xml (xml_stream.go is server only; see
// its header for why an import there would put the package into every wasm
// client). The Sky.Spa split classifies every `Xml_` and `Office_` kernel as a
// server effect (spa_partition.rs), so a correct build never reaches this on
// the client. If one does, the call is a classified Unavailable Err in the
// domain "xml" with the code "server_only", never a panic.
func xmlStreamSafe(src string, htmlEntities bool, lim xmlLimits, ev xmlEvents) *xmlFailure {
	return xmlFail(xmlCodeServerOnly,
		"Xml.parse runs on the server only: a Sky.Spa client does not carry the XML parser")
}
