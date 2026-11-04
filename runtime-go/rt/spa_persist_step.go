package rt

import "time"

// spaPersistMetrics contains counts and timings only. It never carries model,
// session, field values or encoded data. The observer is nil in normal use.
type spaPersistMetrics struct {
	Encodes      int
	EncodedBytes int
	EncodeNanos  int64
	WriteNanos   int64
	SessionNanos int64
}

// spaPersistStep is the portable persistence decision. The browser supplies
// its guarded encoder and storage writer. Keeping this seam portable lets the
// reference path and optimisations run against the same fault/codec fixtures.
// Update, rendering, Cmd execution and subscriptions are deliberately outside
// this function and must never be suppressed by a persistence decision.
func spaPersistStep(prev, next any, encode func(any) string, write func(string), protected []string, observe func(spaPersistMetrics)) bool {
	metrics := spaPersistMetrics{}
	encodeModel := func(model any) string {
		if observe == nil {
			return encode(model)
		}
		start := time.Now()
		out := encode(model)
		metrics.Encodes++
		metrics.EncodedBytes += len(out)
		metrics.EncodeNanos += time.Since(start).Nanoseconds()
		return out
	}
	nextJSON := encodeModel(next)
	if nextJSON != "" {
		if observe == nil {
			write(nextJSON)
		} else {
			start := time.Now()
			write(nextJSON)
			metrics.WriteNanos = time.Since(start).Nanoseconds()
		}
	}
	cleared := false
	if len(protected) != 0 {
		prevJSON := encodeModel(prev)
		if observe == nil {
			cleared = spaSessionClearedByStep(prevJSON, nextJSON, protected)
		} else {
			start := time.Now()
			cleared = spaSessionClearedByStep(prevJSON, nextJSON, protected)
			metrics.SessionNanos = time.Since(start).Nanoseconds()
		}
	}
	if observe != nil {
		observe(metrics)
	}
	return cleared
}
