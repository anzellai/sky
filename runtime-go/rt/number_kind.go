package rt

import "sync"

// NumberKind is compiler-supplied evidence for an instantiated Sky number.
// It is selected from the checked type, never guessed from a value parameter.
type NumberKind uint8

const (
	NumberInt NumberKind = iota
	NumberFloat
)

// NumberLiteral constructs a literal in its domain before arithmetic runs.
func NumberLiteral(kind NumberKind, value int) any {
	switch kind {
	case NumberInt:
		return value
	case NumberFloat:
		return float64(value)
	default:
		panic("rt.AsNumber: invalid numeric representation")
	}
}

// NumberCaf preserves once-only CAF semantics independently for each ordered
// numeric instantiation. Each LazyCaf retains the usual session isolation.
type NumberCaf[T any] struct {
	mu    sync.Mutex
	cells map[string]*LazyCaf[T]
}

func (c *NumberCaf[T]) Get(kinds []NumberKind, compute func() T) T {
	bytes := make([]byte, len(kinds))
	for i, kind := range kinds {
		bytes[i] = byte(kind)
	}
	key := string(bytes)
	c.mu.Lock()
	if c.cells == nil {
		c.cells = make(map[string]*LazyCaf[T])
	}
	cell := c.cells[key]
	if cell == nil {
		cell = &LazyCaf[T]{}
		c.cells[key] = cell
	}
	c.mu.Unlock()
	return cell.Get(compute)
}
