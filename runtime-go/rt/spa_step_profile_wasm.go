//go:build js

package rt

import "syscall/js"

var spaStepProfiling bool
var spaStepReference bool
var spaProfileSteps int
var spaProfileTotals spaStepMetrics

func spaProfileInit() {
	defer func() { _ = recover() }()
	v := js.Global().Get("__skyStepProfileEnabled")
	spaStepProfiling = v.Type() == js.TypeBoolean && v.Bool()
	reference := js.Global().Get("__skyStepReference")
	spaStepReference = spaStepProfiling && reference.Type() == js.TypeBoolean && reference.Bool()
	// The reference run also measures the reflective Codec.auto walker, not
	// the generated codecs (codec_auto_gen.go).
	autoCodecReference.Store(spaStepReference)
	spaProfileSteps, spaProfileTotals = 0, spaStepMetrics{}
	spaProfileDetailStart(spaStepProfiling)
}

func spaProfileReady() {
	defer func() { _ = recover() }()
	js.Global().Set("__skyStepProfileReady", true)
}

func spaProfilePublish(m *spaStepMetrics) {
	// A missing or throwing diagnostic global must not affect a transition.
	defer func() { _ = recover() }()
	spaProfileSteps++
	for i := spaProfilePhase(0); i < spaProfilePhases; i++ {
		spaProfileTotals.Calls[i] += m.Calls[i]
		spaProfileTotals.Nanos[i] += m.Nanos[i]
	}
	vm := &spaProfileTotals.ViewMemo
	vm.Frames += m.ViewMemo.Frames
	vm.Hits += m.ViewMemo.Hits
	vm.Builds += m.ViewMemo.Builds
	vm.Resets += m.ViewMemo.Resets
	p, t := m.Persist, &spaProfileTotals.Persist
	t.DeferredFieldEncodes += p.DeferredFieldEncodes
	t.DeferredFieldMarshals += p.DeferredFieldMarshals
	t.DeferredFieldEncodedBytes += p.DeferredFieldEncodedBytes
	t.DeferredEncodeNanos += p.DeferredEncodeNanos
	t.PreparedEncodes += p.PreparedEncodes
	t.PreparedFallbacks += p.PreparedFallbacks
	t.FieldMarshals += p.FieldMarshals
	t.ClosedFieldReuses += p.ClosedFieldReuses
	t.Encodes += p.Encodes
	t.EncodedBytes += p.EncodedBytes
	t.EncodeNanos += p.EncodeNanos
	t.WriteNanos += p.WriteNanos
	t.SessionNanos += p.SessionNanos
	t.ProjectionFields += p.ProjectionFields
	t.ProjectionBytes += p.ProjectionBytes
	t.EncodingReuses += p.EncodingReuses
	t.EncodingCompared += p.EncodingCompared
	t.FieldModelSteps += p.FieldModelSteps
	t.FieldEncodes += p.FieldEncodes
	t.FieldEncodedBytes += p.FieldEncodedBytes
	t.MaterializedBytes += p.MaterializedBytes
	t.BridgeBytes += p.BridgeBytes
	names := [...]string{"update", "view", "vnode", "style", "patch", "post"}
	stats := map[string]any{"steps": spaProfileSteps}
	for i, name := range names {
		stats[name+"Calls"] = spaProfileTotals.Calls[i]
		stats[name+"Nanos"] = float64(spaProfileTotals.Nanos[i])
	}
	stats["viewMemoFrames"] = vm.Frames
	stats["viewMemoHits"] = vm.Hits
	stats["viewMemoBuilds"] = vm.Builds
	stats["viewMemoResets"] = vm.Resets
	stats["deferredFieldEncodes"] = t.DeferredFieldEncodes
	stats["deferredFieldMarshals"] = t.DeferredFieldMarshals
	stats["deferredFieldEncodedBytes"] = float64(t.DeferredFieldEncodedBytes)
	stats["deferredEncodeNanos"] = float64(t.DeferredEncodeNanos)
	stats["preparedEncodes"] = t.PreparedEncodes
	stats["preparedFallbacks"] = t.PreparedFallbacks
	stats["fieldMarshals"] = t.FieldMarshals
	stats["closedFieldReuses"] = t.ClosedFieldReuses
	stats["modelEncodes"] = t.Encodes
	stats["modelEncodeReuses"] = t.EncodingReuses
	stats["encodeComparedNodes"] = t.EncodingCompared
	stats["encodedBytes"] = t.EncodedBytes
	stats["fieldModelSteps"] = t.FieldModelSteps
	stats["fieldEncodes"] = t.FieldEncodes
	stats["fieldEncodedBytes"] = float64(t.FieldEncodedBytes)
	stats["materializedBytes"] = float64(t.MaterializedBytes)
	stats["bridgeBytes"] = t.BridgeBytes
	stats["encodeNanos"] = float64(t.EncodeNanos)
	stats["writeNanos"] = float64(t.WriteNanos)
	stats["sessionNanos"] = float64(t.SessionNanos)
	stats["projectionFields"] = t.ProjectionFields
	stats["projectionBytes"] = t.ProjectionBytes
	stats["detail"] = spaProfileDetailSnapshot().jsValue()
	js.Global().Set("__skyStepProfile", js.ValueOf(stats))
}
