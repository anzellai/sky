package rt

import "time"

// A single compiler-certified encoder owns this state. A cold call still uses
// the exact original encoder. Closed-field certificates never confer trust on
// custom codecs or unknown/foreign runtime representations.
type spaFieldPersistence struct {
	snapshot  *spaModelFieldSnapshot
	profiling bool
}

func (p *spaFieldPersistence) reset() { p.snapshot = nil }

type spaFieldPersistenceMetrics struct {
	FullEncodes int
	Profile     spaPersistMetrics
	FieldWork   spaFieldEncodingWork
}

// This portable controller preserves synchronous publication and the existing
// v2 format, byte cap, identity fencing and sign-out ordering. It does not yet
// change the browser storage adapter or promise flat sub-cap storage cost.
func (p *spaFieldPersistence) step(prev, next any, encode func(any) string, st spaKV, life *spaPersistLife, exclude, protected []string, capBytes int, generated bool) (cleared bool, metrics spaFieldPersistenceMetrics) {
	begin := func() time.Time {
		if p.profiling {
			return time.Now()
		}
		return time.Time{}
	}
	elapsed := func(start time.Time, dest *int64) {
		if !start.IsZero() {
			*dest += time.Since(start).Nanoseconds()
		}
	}
	defer func() {
		metrics.Profile.FieldEncodes = metrics.FieldWork.FieldEncodes
		metrics.Profile.FieldEncodedBytes = metrics.FieldWork.FieldEncodedBytes
		metrics.Profile.MaterializedBytes = metrics.FieldWork.MaterializedBytes
		metrics.Profile.EncodingCompared += metrics.FieldWork.ComparedNodes
	}()
	full := func(model any) string {
		start := begin()
		out := encode(model)
		elapsed(start, &metrics.Profile.EncodeNanos)
		metrics.FullEncodes++
		metrics.Profile.Encodes++
		metrics.Profile.EncodedBytes += len(out)
		return out
	}
	if !generated || capBytes <= 0 || capBytes > spaPersistMaxBytes {
		p.reset()
		cleared = spaPersistStep(prev, next, full, func(blob string) {
			spaPersistWrite(st, life, spaPersistStripFields(blob, exclude, protected), protected, capBytes)
		}, protected, nil)
		return
	}
	previous := p.snapshot
	start := begin()
	candidate, work, derived := previous.derive(next)
	elapsed(start, &metrics.Profile.EncodeNanos)
	if derived {
		metrics.Profile.FieldModelSteps++
	}
	metrics.FieldWork = work
	var nextJSON string
	calledFull := false
	if !derived {
		metadata := codecEnumRegistry()
		nextJSON, calledFull = full(next), true
		start = begin()
		candidate, _ = spaModelFieldsFromFull(next, nextJSON, metadata, spaPersistMaxBytes)
		elapsed(start, &metrics.Profile.EncodeNanos)
	}
	p.snapshot = candidate
	// Capture next's exact null/presence decision before publication. Storage
	// callbacks may change metadata; previous encoding belongs AFTER the write.
	start = begin()
	projection, projected := spaFieldsNullProjection(candidate, protected)
	elapsed(start, &metrics.Profile.SessionNanos)
	if candidate != nil && candidate.valid {
		start = begin()
		id, identityOK := candidate.protectedIdentityWithWork(protected, &metrics.FieldWork)
		payload, oversized, materialized, storedOK := candidate.storedJSON(exclude, protected, capBytes)
		elapsed(start, &metrics.Profile.WriteNanos)
		metrics.FieldWork.ComparedNodes += materialized.ComparedNodes
		metrics.FieldWork.FieldEncodes += materialized.FieldEncodes
		metrics.FieldWork.FieldEncodedBytes = spaSizeAdd(metrics.FieldWork.FieldEncodedBytes, materialized.FieldEncodedBytes)
		metrics.FieldWork.MaterializedBytes = materialized.MaterializedBytes
		metrics.FieldWork.RetainedBytes, metrics.FieldWork.PayloadBytes = materialized.RetainedBytes, materialized.PayloadBytes
		if identityOK && storedOK {
			size := uint64(len(payload))
			if oversized {
				size = uint64(capBytes) + 1
			}
			start = begin()
			spaPersistWriteKnownIdentity(st, life, payload, id, size, capBytes)
			elapsed(start, &metrics.Profile.WriteNanos)
		} else {
			if !calledFull {
				nextJSON, calledFull = full(next), true
			}
			if nextJSON != "" {
				start = begin()
				spaPersistWrite(st, life, spaPersistStripFields(nextJSON, exclude, protected), protected, capBytes)
				elapsed(start, &metrics.Profile.WriteNanos)
			}
			// Full fallback may invalidate an earlier summary after metadata changes.
			projection, projected = nextJSON, false
		}
	} else if candidate == nil {
		// Full was already called exactly once before any observable publication.
		if nextJSON != "" {
			start = begin()
			spaPersistWrite(st, life, spaPersistStripFields(nextJSON, exclude, protected), protected, capBytes)
			elapsed(start, &metrics.Profile.WriteNanos)
		}
		projection = nextJSON
	}
	if len(protected) == 0 {
		return
	}
	if projected && previous != nil {
		start = begin()
		same, compared := spaModelSame(previous.model, prev)
		metrics.Profile.EncodingCompared += compared
		if same {
			decision, known := spaModelFieldsSessionCleared(previous, candidate, protected)
			elapsed(start, &metrics.Profile.SessionNanos)
			if known {
				return decision, metrics
			}
		} else {
			elapsed(start, &metrics.Profile.SessionNanos)
		}
	}
	// Unknown previous validity/metadata keeps the original post-write call.
	// A certified invalid next has the same empty outcome as the Full encoder.
	if candidate != nil && !candidate.valid {
		projection = ""
	}
	prevJSON := full(prev)
	start = begin()
	cleared = spaSessionClearedByStep(prevJSON, projection, protected)
	elapsed(start, &metrics.Profile.SessionNanos)
	return
}
