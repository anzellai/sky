package rt

import "time"

// spaPersistMetrics contains counts and timings only. It never carries model,
// session, field values or encoded data. The observer is nil in normal use.
type spaPersistMetrics struct {
	DeferredFieldEncodes      int
	DeferredFieldMarshals     int
	DeferredFieldEncodedBytes uint64
	DeferredEncodeNanos       int64 // diagnostic subrange of write/session assembly, never a second phase total
	PreparedEncodes           int
	PreparedFallbacks         int
	FieldMarshals             int
	ClosedFieldReuses         int
	Encodes                   int
	EncodedBytes              int
	EncodeNanos               int64
	WriteNanos                int64
	SessionNanos              int64
	ProjectionFields          int
	ProjectionBytes           int
	EncodingReuses            int
	EncodingCompared          int
	FieldModelSteps           int
	FieldEncodes              int
	FieldEncodedBytes         uint64
	MaterializedBytes         uint64
	BridgeBytes               int
}

// spaPersistStep is the portable persistence decision. The browser supplies
// its guarded encoder and storage writer. Keeping this seam portable lets the
// reference path and optimisations run against the same fault/codec fixtures.
// Update, rendering, Cmd execution and subscriptions are deliberately outside
// this function and must never be suppressed by a persistence decision.
func spaPersistStep(prev, next any, encode func(any) string, write func(string), protected []string, observe func(spaPersistMetrics)) bool {
	return spaPersistStepWithCodec(prev, next, encode, write, protected, false, observe)
}

func spaPersistStepWithCodec(prev, next any, encode func(any) string, write func(string), protected []string, generated bool, observe func(spaPersistMetrics)) bool {
	return spaPersistStepWithEncoding(prev, next, encode, write, protected, generated, nil, observe)
}

func spaPersistStepWithEncoding(prev, next any, encode func(any) string, write func(string), protected []string, generated bool, cache *spaModelEncoding, observe func(spaPersistMetrics)) bool {
	return spaPersistStepCore(prev, next, encode, write, spaSessionClearedByStep, protected, generated, cache, observe)
}

// spaPersistStepReference is the reference path's step: the same encoder
// calls, write and sign-out check in the same order as spaPersistStep with the
// spaPersistWrite(spaPersistStripFields(…)) writer, but the next JSON is
// parsed once for the identity, the exclusions and the sign-out check, and
// the previous JSON reuses the last step's parse when it is the same text.
func spaPersistStepReference(prev, next any, encode func(any) string, ref *spaReferencePersist, observe func(spaPersistMetrics)) bool {
	defer ref.finish()
	return spaPersistStepCore(prev, next, encode, ref.write, ref.cleared, ref.protected, false, nil, observe)
}

func spaPersistStepCore(prev, next any, encode func(any) string, write func(string), sessionCleared func(prevJSON, nextJSON string, protected []string) bool, protected []string, generated bool, cache *spaModelEncoding, observe func(spaPersistMetrics)) bool {
	metrics := spaPersistMetrics{}
	encodeModel := func(model any, isNext bool) string {
		var start time.Time
		if observe != nil {
			start = time.Now()
		}
		var out string
		reused, compared := false, 0
		if isNext && generated && cache != nil {
			out, reused, compared = cache.encodeNext(model)
		} else {
			out = encode(model)
		}
		if observe != nil {
			if reused {
				metrics.EncodingReuses++
			} else {
				metrics.Encodes++
				metrics.EncodedBytes += len(out)
			}
			metrics.EncodingCompared += compared
			metrics.EncodeNanos += time.Since(start).Nanoseconds()
		}
		return out
	}

	nextJSON := encodeModel(next, true)
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
	skipPrevious := false
	if generated && nextJSON != "" && len(protected) != 0 {
		var started time.Time
		if observe != nil {
			started = time.Now()
		}
		var fields, bytes int
		skipPrevious, fields, bytes = spaAutoSessionCannotClear(prev, next, protected)
		if observe != nil {
			metrics.ProjectionFields, metrics.ProjectionBytes = fields, bytes
			metrics.SessionNanos += time.Since(started).Nanoseconds()
		}
	}
	if len(protected) != 0 && !skipPrevious {
		prevJSON := encodeModel(prev, false)
		if observe == nil {
			cleared = sessionCleared(prevJSON, nextJSON, protected)
		} else {
			start := time.Now()
			cleared = sessionCleared(prevJSON, nextJSON, protected)
			metrics.SessionNanos += time.Since(start).Nanoseconds()
		}
	}
	if observe != nil {
		observe(metrics)
	}
	return cleared
}
