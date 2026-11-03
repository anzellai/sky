package main

// numeric_overflow_flow.rs copies this file into the fixture's generated Go
// (sky-out/) and rebuilds it. It replaces the Decimal_toString kernel with one
// that panics with an UNCLASSIFIED message, which no stdlib kernel does on
// purpose: Ffi.callPure turns that panic into an `Err`, and the bare-typed
// `Decimal.toString : Decimal -> String` then hands the `Err` to rt.AsString.

import "sky-app/rt"

func init() {
	rt.RegisterPure("Decimal_toString", func([]any) any {
		panic("flow: an unclassified kernel failure")
	})
}
