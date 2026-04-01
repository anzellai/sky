package sky_wrappers

import (
	"context"
	"time"
)

func Sky_context_AfterFunc(arg0 any, arg1 any) func() bool {
	_arg0 := arg0.(context.Context)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func() {
		_skyFn1(nil)
	}
	return context.AfterFunc(_arg0, _arg1)
}

func Sky_context_Background() context.Context {
	return context.Background()
}

func Sky_context_Cause(arg0 any) SkyResult {
	_arg0 := arg0.(context.Context)
	err := context.Cause(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_context_TODO() context.Context {
	return context.TODO()
}

func Sky_context_WithCancel(arg0 any) any {
	_arg0 := arg0.(context.Context)
	_r0, _r1 := context.WithCancel(_arg0)
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_context_WithCancelCause(arg0 any) any {
	_arg0 := arg0.(context.Context)
	_r0, _r1 := context.WithCancelCause(_arg0)
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_context_WithDeadline(arg0 any, arg1 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(time.Time)
	_r0, _r1 := context.WithDeadline(_arg0, _arg1)
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_context_WithDeadlineCause(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(time.Time)
	_arg2 := arg2.(error)
	_r0, _r1 := context.WithDeadlineCause(_arg0, _arg1, _arg2)
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_context_WithTimeout(arg0 any, arg1 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(time.Duration)
	_r0, _r1 := context.WithTimeout(_arg0, _arg1)
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_context_WithTimeoutCause(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(time.Duration)
	_arg2 := arg2.(error)
	_r0, _r1 := context.WithTimeoutCause(_arg0, _arg1, _arg2)
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_context_WithValue(arg0 any, arg1 any, arg2 any) context.Context {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(any)
	_arg2 := arg2.(any)
	return context.WithValue(_arg0, _arg1, _arg2)
}

func Sky_context_WithoutCancel(arg0 any) context.Context {
	_arg0 := arg0.(context.Context)
	return context.WithoutCancel(_arg0)
}

func Sky_context_Canceled() any {
	return context.Canceled
}

func Sky_context_DeadlineExceeded() any {
	return context.DeadlineExceeded
}

func Sky_context_ContextDeadline(this any) any {
	_this := this.(context.Context)

	_val, _ok := _this.Deadline()
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_context_ContextDone(this any) <-chan struct{} {
	_this := this.(context.Context)

	return _this.Done()
}

func Sky_context_ContextErr(this any) SkyResult {
	_this := this.(context.Context)

	err := _this.Err()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_context_ContextValue(this any, arg0 any) any {
	_this := this.(context.Context)
	_arg0 := arg0.(any)
	return _this.Value(_arg0)
}

