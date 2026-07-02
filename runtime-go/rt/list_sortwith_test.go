package rt

import (
	"reflect"
	"testing"
)

// List.sortWith : (a -> a -> Int) -> List a -> List a (Elm's List.sortWith).
// The comparator returns < 0 when a precedes b, > 0 when it follows, 0 equal.

func TestListSortWithDescending(t *testing.T) {
	// `\a b -> b - a` — descending numeric order.
	cmp := func(a, b any) any { return AsInt(b) - AsInt(a) }
	got := List_sortWith(cmp, []any{1, 4, 2, 5, 3})
	want := []any{5, 4, 3, 2, 1}
	if !reflect.DeepEqual(got.([]any), want) {
		t.Fatalf("sortWith descending = %v, want %v", got, want)
	}
}

func TestListSortWithAscending(t *testing.T) {
	// `\a b -> a - b` — ascending numeric order.
	cmp := func(a, b any) any { return AsInt(a) - AsInt(b) }
	got := List_sortWith(cmp, []any{3, 1, 2})
	want := []any{1, 2, 3}
	if !reflect.DeepEqual(got.([]any), want) {
		t.Fatalf("sortWith ascending = %v, want %v", got, want)
	}
}

func TestListSortWithStable(t *testing.T) {
	// All-equal comparator must preserve input order (SliceStable).
	cmp := func(_, _ any) any { return 0 }
	got := List_sortWith(cmp, []any{3, 1, 2})
	want := []any{3, 1, 2}
	if !reflect.DeepEqual(got.([]any), want) {
		t.Fatalf("sortWith stable = %v, want %v", got, want)
	}
}

func TestListSortWithEmpty(t *testing.T) {
	cmp := func(a, b any) any { return AsInt(a) - AsInt(b) }
	got := List_sortWith(cmp, []any{})
	if len(got.([]any)) != 0 {
		t.Fatalf("sortWith empty = %v, want []", got)
	}
}
