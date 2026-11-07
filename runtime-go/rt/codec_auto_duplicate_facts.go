package rt

import (
	"reflect"
	"sync"
)

// Only the direct record-key collision sentence is an immutable type fact.
// Recursive checks still run in their original order: they can invoke variant
// factories, observe registry changes, and inspect the current blank value.
// Neither their results nor any model, callback or eligibility is retained here.
type codecDuplicateFactKey struct {
	typ   reflect.Type
	snake bool
}

type codecDuplicateFacts struct {
	mu         sync.RWMutex
	facts      map[codecDuplicateFactKey]string
	bytes      int
	maxEntries int
	maxBytes   int
}

const codecDuplicateFactMaxResultBytes = 8 << 10

// Both key modes fit ordinary large compiler catalogs. These are retention
// limits, not semantic limits: a full cache always uses the original scanner.
var codecAutoRecordDuplicateFacts = codecDuplicateFacts{maxEntries: 4096, maxBytes: 64 << 10}

var codecDuplicateCanonicalCarrier = reflect.TypeOf(reflect.TypeOf(0))

func (c *codecDuplicateFacts) problem(t reflect.Type, snake bool) string {
	// reflect.Type is an interface: an embedded wrapper can have effectful
	// methods or be noncomparable. Check its concrete carrier BEFORE invoking
	// a method or using it as a map key. Preserve the original fallback exactly,
	// including its panic for nil or a non-record type.
	if reflect.TypeOf(t) != codecDuplicateCanonicalCarrier {
		return codecAutoRecordDuplicateKeyUncached(t, snake)
	}
	key := codecDuplicateFactKey{typ: t, snake: snake}
	c.mu.RLock()
	problem, found := c.facts[key]
	c.mu.RUnlock()
	if found {
		return problem
	}

	// No lock spans a scan. Concurrent misses may repeat pure work; publication
	// reserves entry and string budgets under one lock, without eviction.
	problem = codecAutoRecordDuplicateKeyUncached(t, snake)
	if len(problem) > codecDuplicateFactMaxResultBytes {
		return problem
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prior, ok := c.facts[key]; ok {
		return prior
	}
	if c.maxEntries <= 0 || len(c.facts) >= c.maxEntries || len(problem) > c.maxBytes-c.bytes {
		return problem
	}
	if c.facts == nil {
		c.facts = make(map[codecDuplicateFactKey]string)
	}
	c.facts[key] = problem
	c.bytes += len(problem)
	return problem
}
