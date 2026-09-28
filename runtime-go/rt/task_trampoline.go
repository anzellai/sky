// task_trampoline.go — the Task representation and its one interpreter.
//
// A Sky `Task e a` used to be a Go thunk (`func() SkyResult[E, A]`), and every
// combinator forced its source and then its continuation INSIDE its own Go
// frame. Plain recursion —
//
//	step n = work |> Task.andThen (\_ -> step (n + 1))
//
// — therefore grew the goroutine stack by several frames per step (more at
// every typed boundary, where TaskCoerceT wrapped the thunk in another
// closure), and two million steps died with Go's fatal "stack overflow",
// which no recover site sees.
//
// Now a Task built by a combinator is DATA: a `*taskNode` inside the phantom
// struct `SkyTask[E, A]`. `forceTask` is the only place a Task is forced. It
// keeps pending continuations on its own heap-allocated frame stack, and when
// an `andThen` continuation returns the next task, the frame that produced it
// has already been popped — so tail recursion through a continuation runs in
// a constant Go stack AND a constant frame stack, however many steps it
// takes. Chains that nest their SOURCE (a fold that builds
// `((t |> map f) |> map g) …`) keep their pending frames on the heap stack,
// never on the Go stack.
//
// Kernel thunks (`func() any` returning a Result) are still funcs. They are
// LEAVES: the interpreter calls them once and reads their Result.
//
// Every Go site that forces or converts a value that may be a Task routes
// through the helpers here (`forceTask`, `taskNodeOf`, `taskNodeFor`,
// `isSkyTaskType`, `taskReflectValue`), so there is one classifier, not one
// per site. A site that cannot force what it was handed panics with an
// `rt.Coerce: expected a Task …` message (classified CoerceFailure); it never
// hands the unforced value on as if it were a result.
package rt

import (
	"fmt"
	"reflect"
	"strings"
)

type taskKind uint8

const (
	// taskPure: Ok val.
	taskPure taskKind = iota + 1
	// taskFail: Err val.
	taskFail
	// taskLeaf: val is an opaque thunk or value, forced by forceLeaf.
	taskLeaf
	// taskBind: Task.andThen — run src; on Ok v run the task fn(v).
	taskBind
	// taskMap: Task.map — run src; on Ok v produce Ok fn(v).
	taskMap
	// taskMapErr: Task.mapError — run src; on Err e produce Err fn(e).
	taskMapErr
	// taskCatch: Task.onError — run src; on Err e run the task fn(e).
	taskCatch
	// taskBindResult: Task.andThenResult — run src; on Ok v read the
	// Result fn(v).
	taskBindResult
	// taskFromResult: Task.fromResult — val is a Result.
	taskFromResult
	// taskLazy: Task.lazy — val is a pure thunk, called on every run.
	taskLazy
	// taskSeq: Task.sequence — val is the list of tasks, run in order.
	taskSeq
)

// taskNode is immutable once built: one node may be run many times and from
// several goroutines at once, so all per-run state lives in forceTask's frames.
type taskNode struct {
	kind taskKind
	val  any
	src  any
	fn   any
}

// SkyTask is a Sky `Task e a`. E and A are phantom: every instantiation has
// the same underlying type, so converting between instantiations copies one
// pointer and allocates nothing (TaskCoerceT). The struct is pointer-shaped,
// so boxing it into `any` does not allocate either.
type SkyTask[E any, A any] struct{ n *taskNode }

// skyTaskValue is implemented by every SkyTask instantiation. It lets code
// that holds a Task as `any` recognise it without enumerating E and A.
type skyTaskValue interface {
	skyTaskNode() *taskNode
	skyTaskWith(n *taskNode) any
}

func (t SkyTask[E, A]) skyTaskNode() *taskNode { return t.n }

func (t SkyTask[E, A]) skyTaskWith(n *taskNode) any { return SkyTask[E, A]{n: n} }

// RunAny forces the task and returns its value-erased result. It exists for
// code that holds a concrete SkyTask[E, A] behind an interface.
func (t SkyTask[E, A]) RunAny() SkyResult[any, any] { return forceTask(t) }

var skyTaskValueType = reflect.TypeOf((*skyTaskValue)(nil)).Elem()

// mkTask builds a node and returns it as an any-typed Task.
func mkTask(kind taskKind, val, src, fn any) any {
	return SkyTask[any, any]{n: &taskNode{kind: kind, val: val, src: src, fn: fn}}
}

// taskNodeOf reports whether v is a SkyTask (of any instantiation) and
// returns its node. A zero SkyTask{} reports (nil, true).
func taskNodeOf(v any) (*taskNode, bool) {
	switch t := v.(type) {
	case SkyTask[any, any]:
		return t.n, true
	case skyTaskValue:
		return t.skyTaskNode(), true
	}
	return nil, false
}

// taskNodeFor returns the node of a SkyTask, or wraps any other value (a
// kernel thunk, a resolved Result, a bare value) in a leaf node. It is the
// one rule every conversion INTO a SkyTask type uses.
func taskNodeFor(v any) *taskNode {
	if n, ok := taskNodeOf(v); ok {
		return n
	}
	return &taskNode{kind: taskLeaf, val: v}
}

// isThunk reports whether v is a zero-argument, one-result func: the shape
// of a kernel Task leaf.
func isThunk(v any) bool {
	switch v.(type) {
	case func() any, func() SkyResult[any, any]:
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.IsValid() && rv.Kind() == reflect.Func &&
		rv.Type().NumIn() == 0 && rv.Type().NumOut() == 1
}

// isTaskValue reports whether v is a Task: a SkyTask or a kernel thunk.
func isTaskValue(v any) bool {
	if _, ok := taskNodeOf(v); ok {
		return true
	}
	return isThunk(v)
}

// isSkyTaskType reports whether t is a SkyTask instantiation.
func isSkyTaskType(t reflect.Type) bool {
	return t != nil && t.Kind() == reflect.Struct &&
		strings.HasPrefix(t.Name(), "SkyTask[") && t.Implements(skyTaskValueType)
}

// taskReflectValue converts v to the SkyTask instantiation t (for which
// isSkyTaskType holds) as a reflect.Value. No allocation when v is already a
// SkyTask; a leaf node otherwise.
func taskReflectValue(v any, t reflect.Type) reflect.Value {
	z := reflect.Zero(t).Interface().(skyTaskValue)
	return reflect.ValueOf(z.skyTaskWith(taskNodeFor(v)))
}

// TaskCoerce converts any Task-shaped value to SkyTask[any, any].
func TaskCoerce(v any) SkyTask[any, any] {
	if t, ok := v.(SkyTask[any, any]); ok {
		return t
	}
	return SkyTask[any, any]{n: taskNodeFor(v)}
}

// TaskCoerceT converts any Task-shaped value to SkyTask[E, A]. The compiler
// emits it at every typed Task boundary. For a SkyTask of any instantiation
// it is a free phantom conversion: it copies the node pointer. Only a
// non-SkyTask input (a kernel `func() any` thunk) is wrapped, once, in a leaf
// node.
func TaskCoerceT[E any, A any](v any) SkyTask[E, A] {
	switch t := v.(type) {
	case SkyTask[E, A]:
		return t
	case SkyTask[any, any]:
		return SkyTask[E, A]{n: t.n}
	case skyTaskValue:
		return SkyTask[E, A]{n: t.skyTaskNode()}
	}
	return SkyTask[E, A]{n: &taskNode{kind: taskLeaf, val: v}}
}

// typedLeaf wraps a typed thunk as a leaf task, erasing its result so the
// interpreter reads it without reflect.
func typedLeaf[E any, A any](f func() SkyResult[E, A]) SkyTask[E, A] {
	return SkyTask[E, A]{n: &taskNode{kind: taskLeaf, val: func() SkyResult[any, any] {
		r := f()
		return SkyResult[any, any]{Tag: r.Tag, OkValue: r.OkValue, ErrValue: r.ErrValue}
	}}}
}

const zeroTaskMsg = "rt.Coerce: expected a Task, got a zero SkyTask (no computation)"

// resultOf reads a value produced by a thunk as a Task result: a Result of
// any instantiation is that Result; anything else is `Ok value` (the kernel
// trust boundary AnyTaskRun documents).
func resultOf(r any) SkyResult[any, any] {
	if res, ok := r.(SkyResult[any, any]); ok {
		return res
	}
	if tag, okV, errV := anyResultView(r); tag >= 0 {
		return SkyResult[any, any]{Tag: tag, OkValue: okV, ErrValue: errV}
	}
	return Ok[any, any](r)
}

// forceLeaf runs one leaf: a kernel thunk, a resolved Result, or a bare
// value. It never sees a SkyTask (forceTask dispatches those).
func forceLeaf(v any) SkyResult[any, any] {
	switch t := v.(type) {
	case nil:
		return Ok[any, any](nil)
	case SkyResult[any, any]:
		return t
	case func() SkyResult[any, any]:
		return t()
	case func() any:
		return resultOf(t())
	}
	// A concrete SkyResult[E, A] (resolved), or a typed thunk behind an
	// interface.
	if r, ok := v.(interface{ RunAny() SkyResult[any, any] }); ok {
		return r.RunAny()
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Func {
		// An unnamed typed thunk `func() SkyResult[E, A]`. Unreachable
		// under TinyGo: the tasks codegen and the runtime build are all
		// nodes or `func() any`, caught above.
		if rv.Type().NumIn() == 0 && rv.Type().NumOut() == 1 {
			return resultOf(rv.Call(nil)[0].Interface())
		}
		panic(fmt.Sprintf("rt.Coerce: expected a Task, got a function of type %s that is not a zero-argument thunk", rv.Type()))
	}
	return resultOf(v)
}

// forceLazy calls a Task.lazy thunk. `() -> a` reaches Go as a zero-argument
// func or as a one-argument func taking unit; a non-func value is an already
// evaluated `a`.
func forceLazy(thunk any) any {
	switch f := thunk.(type) {
	case func() any:
		return f()
	case func(any) any:
		return f(struct{}{})
	}
	if _, ok := taskNodeOf(thunk); ok {
		panic("rt.Coerce: expected a function for Task.lazy, got a Task")
	}
	rv := reflect.ValueOf(thunk)
	if rv.Kind() != reflect.Func {
		return thunk
	}
	if rv.Type().NumIn() == 0 {
		return SkyCall(thunk)
	}
	return SkyCall(thunk, struct{}{})
}

// taskFrame is one pending continuation of forceTask.
type taskFrame struct {
	n   *taskNode
	seq *taskSeqState
}

// taskSeqState is the per-run state of one Task.sequence.
type taskSeqState struct {
	xs  []any
	acc []any
}

// forceTask forces a Task and returns its value-erased result. It is the only
// Task interpreter: anyTaskInvoke, AnyTaskRun, SkyCall, sky_call and the
// typed companions all end here.
func forceTask(t any) SkyResult[any, any] {
	var buf [8]taskFrame
	stack := buf[:0]
	cur := t
	for {
		var res SkyResult[any, any]
		// Descend: follow sources until a result is produced.
	descend:
		for {
			n, isNode := taskNodeOf(cur)
			if !isNode {
				res = forceLeaf(cur)
				break
			}
			if n == nil {
				panic(zeroTaskMsg)
			}
			switch n.kind {
			case taskPure:
				res = Ok[any, any](n.val)
				break descend
			case taskFail:
				res = Err[any, any](n.val)
				break descend
			case taskLeaf:
				if _, inner := taskNodeOf(n.val); inner {
					cur = n.val
					continue
				}
				res = forceLeaf(n.val)
				break descend
			case taskLazy:
				res = Ok[any, any](forceLazy(n.val))
				break descend
			case taskFromResult:
				res = resultOf(n.val)
				break descend
			case taskSeq:
				xs := AsList(n.val)
				if len(xs) == 0 {
					res = Ok[any, any]([]any{})
					break descend
				}
				stack = append(stack, taskFrame{n: n, seq: &taskSeqState{xs: xs, acc: make([]any, 0, len(xs))}})
				cur = xs[0]
			case taskBind, taskMap, taskMapErr, taskCatch, taskBindResult:
				stack = append(stack, taskFrame{n: n})
				cur = n.src
			default:
				panic(fmt.Sprintf("rt.Coerce: expected a Task, got a task node of unknown kind %d", n.kind))
			}
		}
		// Unwind: feed the result to pending frames until one yields a new
		// task to run, or the stack is empty.
		next := false
		for len(stack) > 0 {
			top := len(stack) - 1
			f := stack[top]
			if f.n.kind == taskSeq && res.Tag == 0 {
				st := f.seq
				st.acc = append(st.acc, res.OkValue)
				if len(st.acc) < len(st.xs) {
					cur = st.xs[len(st.acc)]
					next = true
					break
				}
				res = Ok[any, any](st.acc)
			}
			stack[top] = taskFrame{}
			stack = stack[:top]
			switch f.n.kind {
			case taskBind:
				if res.Tag == 0 {
					cur = SkyCall(f.n.fn, res.OkValue)
					next = true
				}
			case taskCatch:
				if res.Tag != 0 {
					cur = SkyCall(f.n.fn, res.ErrValue)
					next = true
				}
			case taskMap:
				if res.Tag == 0 {
					res = Ok[any, any](SkyCall(f.n.fn, res.OkValue))
				}
			case taskMapErr:
				if res.Tag != 0 {
					res = Err[any, any](SkyCall(f.n.fn, res.ErrValue))
				}
			case taskBindResult:
				if res.Tag == 0 {
					res = resultOf(SkyCall(f.n.fn, res.OkValue))
				}
			}
			if next {
				break
			}
		}
		if !next {
			return res
		}
	}
}
