//go:build js

package rt

import "syscall/js"

var spaStepProfiling bool
var spaProfileSteps int
var spaProfileTotals spaStepMetrics

func spaProfileInit() {
	defer func() { _ = recover() }()
	v := js.Global().Get("__skyStepProfileEnabled")
	spaStepProfiling = v.Type() == js.TypeBoolean && v.Bool()
	spaProfileSteps, spaProfileTotals = 0, spaStepMetrics{}
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
	names := [...]string{"update", "view", "vnode", "style", "patch", "post"}
	stats := map[string]any{"steps": spaProfileSteps}
	for i, name := range names {
		stats[name+"Calls"] = spaProfileTotals.Calls[i]
		stats[name+"Nanos"] = float64(spaProfileTotals.Nanos[i])
	}
	stats["modelEncodes"] = t.Encodes
	stats["encodedBytes"] = t.EncodedBytes
	stats["encodeNanos"] = float64(t.EncodeNanos)
	stats["writeNanos"] = float64(t.WriteNanos)
	stats["sessionNanos"] = float64(t.SessionNanos)
	js.Global().Set("__skyStepProfile", js.ValueOf(stats))
}
