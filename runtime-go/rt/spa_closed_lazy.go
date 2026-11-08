package rt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

const spaClosedMaxPinnedEpochs = 4

// This ledger belongs to ONE invocation, not any published cache. Current
// single-epoch prior caches normally need only one pin; the bound is defensive.
type spaClosedLazyPins struct {
	epochs [spaClosedMaxPinnedEpochs]*codecEnumRegistrySnapshot
	count  int
}

func (p *spaClosedLazyPins) claim(epoch *codecEnumRegistrySnapshot) bool {
	if epoch == nil {
		return false
	}
	for i := 0; i < p.count; i++ {
		if p.epochs[i] == epoch {
			return true
		}
	}
	if p.count == len(p.epochs) {
		return false
	}
	p.epochs[p.count] = epoch
	p.count++
	return true
}

type spaClosedDeferredWork struct {
	Conversions, Marshals int
	EncodedBytes          uint64
	Nanos                 int64 // subrange of BuildNanos, never added to phase totals again
}

// Invocation-local ONLY. Cache nodes never point to this value, its plan,
// budget or snapshot. target is a NEW node, never the prior certificate node.
type spaClosedLazyLeaf struct {
	value    reflect.Value
	declared string
	snapshot *codecEnumRegistrySnapshot
	budget   *spaClosedEncodingBudget
	target   *spaClosedEncodingNode
	size     uint64
	null     bool
	rawKind  spaClosedRawKind
	resolved bool
	raw      string
	err      error
}

func (l *spaClosedLazyLeaf) resolve() (ok bool) {
	if l.resolved {
		return l.err == nil
	}
	l.resolved = true
	l.err = fmt.Errorf("closed lazy resolution did not complete")
	budget := l.budget
	var started time.Time
	if budget.Profile {
		started = time.Now()
	}
	defer func() {
		if !started.IsZero() {
			budget.Deferred.Nanos += time.Since(started).Nanoseconds()
		}
		// Clear even on panic. Parent's independent invocation recovery merges
		// attempted deferred work before returning failure, without FX replay.
		l.value = reflect.Value{}
		l.snapshot = nil
		l.target = nil
		l.budget = nil
	}()
	budget.Deferred.Conversions++
	raw, err := codecAutoEncodeTypedAt(l.value, l.declared, true, l.snapshot)
	if err != nil {
		l.err = fmt.Errorf("closed lazy conversion invariant: %w", err)
		return false
	}
	kind := spaClosedRawOther
	if raw == nil {
		kind = spaClosedRawNil
	} else if _, sentinel := raw.(codecJustNull); sentinel {
		kind = spaClosedRawJustNull
	}
	budget.Deferred.Marshals++
	encoded, err := json.Marshal(raw)
	budget.Deferred.EncodedBytes = spaSizeAdd(budget.Deferred.EncodedBytes, uint64(len(encoded)))
	if err != nil {
		l.err = fmt.Errorf("closed lazy marshal invariant: %w", err)
		return false
	}
	if uint64(len(encoded)) != l.size || spaIsNullRaw(encoded) != l.null || kind != l.rawKind {
		l.err = fmt.Errorf("closed lazy certificate invariant")
		return false
	}
	l.raw = string(encoded)
	if l.target != nil {
		// Capacity was reserved in A. No unknown output retention can consume
		// these bytes; publication occurs only after this function returns.
		l.target.raw = l.raw
	}
	l.err = nil
	return true
}
