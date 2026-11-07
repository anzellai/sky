package rt

import "time"

// State belongs to exactly one compiler-proven encoder configured at app boot.
// The current model owner is replaced after every next invocation, even failure.
type spaMixedPersistence struct {
	encoder         SpaPreparedModelEncoder
	cache           *spaMixedModelCache
	profiling       bool
	generatedClosed bool
}

func (p *spaMixedPersistence) reset() { p.cache = nil }

func (p *spaMixedPersistence) step(prev, next any, full func(any) string, st spaKV, life *spaPersistLife, exclude, protected []string, capBytes int) (cleared bool, metrics spaPersistMetrics) {
	// Original Full may coerce an erased input BEFORE entering Codec.auto. Only
	// its exact assertion-compatible Go input representation can bypass that
	// wrapper. A mismatch must take Full before ANY preparation effects.
	if !spaEncoderModelMatches(p.encoder.InputType, prev) || !spaEncoderModelMatches(p.encoder.InputType, next) {
		p.reset()
		var observe func(spaPersistMetrics)
		if p.profiling {
			observe = func(m spaPersistMetrics) { metrics = m }
		}
		cleared = spaPersistStep(prev, next, full, func(blob string) {
			spaPersistWrite(st, life, spaPersistStripFields(blob, exclude, protected), protected, capBytes)
		}, protected, observe)
		return
	}
	previousCache := p.cache
	options := spaMixedEncodeOptions{Exclude: exclude, Protected: protected, CapBytes: capBytes, Profile: p.profiling}
	addWork := func(result spaMixedEncoded, previous bool) {
		work := result.Work
		metrics.DeferredFieldEncodes += work.DeferredConversions
		metrics.DeferredFieldMarshals += work.DeferredMarshals
		metrics.DeferredFieldEncodedBytes = spaSizeAdd(metrics.DeferredFieldEncodedBytes, work.DeferredEncodedBytes)
		metrics.DeferredEncodeNanos += work.DeferredNanos
		metrics.PreparedEncodes += work.Prepares
		metrics.PreparedFallbacks += work.PreparedFallbacks
		metrics.Encodes += work.PreparedFallbacks
		metrics.EncodedBytes += len(result.FullJSON)
		metrics.FieldEncodes += work.Conversions
		metrics.FieldMarshals += work.Marshals
		metrics.ClosedFieldReuses += work.ClosedReuses
		metrics.EncodingCompared += work.ComparedNodes
		metrics.FieldEncodedBytes = spaSizeAdd(metrics.FieldEncodedBytes, work.EncodedBytes)
		metrics.MaterializedBytes = spaSizeAdd(metrics.MaterializedBytes, work.MaterializedBytes)
		metrics.EncodeNanos += work.EncodeNanos
		if previous {
			metrics.SessionNanos += work.BuildNanos
		} else {
			metrics.WriteNanos += work.BuildNanos
		}
	}
	candidate := spaEncodeMixed(p.encoder, previousCache, next, options)
	addWork(candidate, false)
	p.cache = candidate.NextCache
	if candidate.Valid && !candidate.Reference {
		metrics.FieldModelSteps++
	}
	if candidate.Valid {
		var start time.Time
		if p.profiling {
			start = time.Now()
		}
		if candidate.Reference {
			spaPersistWrite(st, life, spaPersistStripFields(candidate.FullJSON, exclude, protected), protected, capBytes)
		} else {
			spaPersistWriteKnownIdentity(st, life, candidate.Payload, candidate.Identity, candidate.EncodedBytes, capBytes)
		}
		if p.profiling {
			metrics.WriteNanos += time.Since(start).Nanoseconds()
		}
	}
	if len(protected) == 0 {
		return
	}
	// Only the original literal/canonical proof PLUS both runtime domains can
	// skip a previous invocation. Prepared-only or mixed models retain every
	// strict preparation and unknown effect, after publication.
	if p.generatedClosed && candidate.Valid && !candidate.Reference && spaClosedEncoderModelsEligible(true, p.encoder.InputType, prev, next) {
		var start time.Time
		if p.profiling {
			start = time.Now()
		}
		cannotClear, fields, bytes := spaAutoSessionCannotClear(prev, next, protected)
		metrics.ProjectionFields, metrics.ProjectionBytes = fields, bytes
		if p.profiling {
			metrics.SessionNanos += time.Since(start).Nanoseconds()
		}
		if cannotClear {
			return
		}
	}
	// Unknown encoders can observe every previous logical invocation, even when
	// session nullness looks unchanged. Preserve its strict preparation and all
	// unknown effects AFTER publication, independently of next failure.
	options.PreviousOnly = true
	if previousCache == nil {
		previousCache = candidate.NextCache
	}
	old := spaEncodeMixed(p.encoder, previousCache, prev, options)
	addWork(old, true)
	if !old.Valid || !candidate.Valid {
		return
	}
	oldProjection, nextProjection := old.NullProjection, candidate.NullProjection
	if old.Reference {
		oldProjection = old.FullJSON
	}
	if candidate.Reference {
		nextProjection = candidate.FullJSON
	}
	var start time.Time
	if p.profiling {
		start = time.Now()
	}
	cleared = spaSessionClearedByStep(oldProjection, nextProjection, protected)
	if p.profiling {
		metrics.SessionNanos += time.Since(start).Nanoseconds()
	}
	return
}
