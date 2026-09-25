package rt

// CurryN turns an n-ary function over an argument slice into the curried,
// boxed-closure form every Sky function value takes at an `any` slot: a
// `func(any) any` that takes one argument per call and, after the n-th, returns
// f(args). The compiler emits it for a curried function value of arity >= 3
// (a record constructor handed to Codec.object, a boxed multi-argument
// function) in place of a nest of n closures, because the nest costs the Go
// compiler O(n^2): each level captures every parameter before it.
//
// Every partial application is independent: a step copies the arguments so
// far into a new slice, so applying the same partial twice (`let f = Ctor a`
// then `f b` and `f c`) never lets one call see the other's argument. That is
// the same O(n^2) copying the closure nest did through its captures.
//
// n < 1 is a compiler bug; it returns a function that applies f to the single
// argument, so a stray call still has a value rather than panicking.
func CurryN(n int, f func([]any) any) func(any) any {
	if n < 1 {
		n = 1
	}
	return curryStep(n, f, nil)
}

func curryStep(n int, f func([]any) any, acc []any) func(any) any {
	return func(a any) any {
		next := make([]any, len(acc)+1)
		copy(next, acc)
		next[len(acc)] = a
		if len(next) == n {
			return f(next)
		}
		return curryStep(n, f, next)
	}
}
