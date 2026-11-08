package rt

import "time"

// Only numeric counts and timings are collected. Profiling is opt-in before
// browser boot; these diagnostics never participate in application decisions.
type spaProfilePhase uint8

const (
	spaProfileUpdate spaProfilePhase = iota
	spaProfileView
	spaProfileVNode
	spaProfileStyle
	spaProfilePatch
	spaProfilePost
	spaProfilePhases
)

type spaStepMetrics struct {
	Calls    [spaProfilePhases]int
	Nanos    [spaProfilePhases]int64
	Persist  spaPersistMetrics
	ViewMemo spaViewMemoMetrics
}

var spaActiveStepMetrics *spaStepMetrics

func spaProfileBeginPhase() time.Time {
	if spaActiveStepMetrics == nil {
		return time.Time{}
	}
	return time.Now()
}

func spaProfileEndPhase(start time.Time, phase spaProfilePhase) {
	if spaActiveStepMetrics != nil && !start.IsZero() {
		spaActiveStepMetrics.Calls[phase]++
		spaActiveStepMetrics.Nanos[phase] += time.Since(start).Nanoseconds()
	}
}
