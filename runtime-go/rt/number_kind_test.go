package rt

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestNumberLiteralSelectsDomainBeforeArithmetic(t *testing.T) {
	const big = 9007199254740993
	for _, kind := range []NumberKind{NumberInt, NumberFloat} {
		left := Add(NumberLiteral(kind, big), NumberLiteral(kind, 1))
		value := Sub(left, NumberLiteral(kind, big))
		switch kind {
		case NumberInt:
			n, ok := value.(int)
			if !ok || n != 1 {
				t.Errorf("Int computation: %T %v", value, value)
			}
		case NumberFloat:
			n, ok := value.(float64)
			if !ok || n != 0 {
				t.Errorf("Float computation: %T %v", value, value)
			}
		}
	}
}

func TestNumberCafComputesOncePerConcurrentInstantiation(t *testing.T) {
	var cell NumberCaf[any]
	var counts [2]atomic.Int32
	type result struct {
		kind  NumberKind
		value any
	}
	results := make(chan result, 100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		for _, kind := range []NumberKind{NumberInt, NumberFloat} {
			wg.Add(1)
			go func(kind NumberKind) {
				defer wg.Done()
				value := cell.Get([]NumberKind{kind}, func() any {
					counts[kind].Add(1)
					return NumberLiteral(kind, 9007199254740993)
				})
				results <- result{kind, value}
			}(kind)
		}
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.kind == NumberInt {
			n, ok := result.value.(int)
			if !ok || n != 9007199254740993 {
				t.Errorf("Int CAF: %T %v", result.value, result.value)
			}
		} else {
			n, ok := result.value.(float64)
			if !ok || n != 9007199254740992 {
				t.Errorf("Float CAF: %T %v", result.value, result.value)
			}
		}
	}
	for i := range counts {
		if counts[i].Load() != 1 {
			t.Errorf("kind %d computed %d times", i, counts[i].Load())
		}
	}
}

func TestNumberCafKeepsOrderedIndependentParameters(t *testing.T) {
	var cell NumberCaf[string]
	first := []NumberKind{NumberFloat, NumberInt}
	second := []NumberKind{NumberInt, NumberFloat}
	if got := cell.Get(first, func() string { return "Float Int" }); got != "Float Int" {
		t.Fatal(got)
	}
	if got := cell.Get(second, func() string { return "Int Float" }); got != "Int Float" {
		t.Fatal(got)
	}
	if got := cell.Get(first, func() string { t.Error("cached instantiation recomputed"); return "wrong" }); got != "Float Int" {
		t.Fatal(got)
	}
}
