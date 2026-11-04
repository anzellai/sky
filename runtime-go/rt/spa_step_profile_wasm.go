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
	spaProfileSteps, spaProfileTotals = 0, spaStepMetrics{}
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
	p, t := m.Persist, &spaProfileTotals.Persist
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
	js.Global().Set("__skyStepProfile", js.ValueOf(stats))
}
