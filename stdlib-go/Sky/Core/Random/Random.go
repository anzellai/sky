package sky_sky_core_random

import (
	"math/rand"
)

type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}
type SkyMaybe struct {
	Tag       int
	SkyName   string
	JustValue any
}

func ok(v any) SkyResult { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }
func just(v any) SkyMaybe { return SkyMaybe{Tag: 0, SkyName: "Just", JustValue: v} }
func nothing() SkyMaybe   { return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

// Int returns a Task (thunk) that generates random int in [lo, hi].
func Int(lo, hi any) any {
	return func() any {
		l, h := asInt(lo), asInt(hi)
		if h <= l { return ok(l) }
		return ok(l + rand.Intn(h-l+1))
	}
}

// Float returns a Task (thunk) that generates random float in [0, 1).
func Float(_ any) any {
	return func() any {
		return ok(rand.Float64())
	}
}

// Choice returns a Task (thunk) that picks a random element.
func Choice(items any) any {
	return func() any {
		list := asList(items)
		if len(list) == 0 { return ok(nothing()) }
		return ok(just(list[rand.Intn(len(list))]))
	}
}

// Shuffle returns a Task (thunk) that shuffles a list.
func Shuffle(items any) any {
	return func() any {
		list := asList(items)
		result := make([]any, len(list))
		copy(result, list)
		rand.Shuffle(len(result), func(i, j int) {
			result[i], result[j] = result[j], result[i]
		})
		return ok(result)
	}
}

func asInt(v any) int {
	switch x := v.(type) {
	case int: return x
	case float64: return int(x)
	default: return 0
	}
}

func asList(v any) []any {
	if l, ok := v.([]any); ok { return l }
	return nil
}
