package rt

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	// Explicitly pure: deterministic, no I/O, no mutable state. Safe for
	// RegisterPure because the caller has audited that Go's strings.ToUpper,
	// rune conversion, and slice reversal have no side effects.
	RegisterPure("myffi.reverse", func(args []any) any {
		if len(args) == 0 {
			return ""
		}
		s := fmt.Sprintf("%v", args[0])
		runes := []rune(s)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return string(runes)
	})

	// Pure — audited.
	RegisterPure("myffi.shout", func(args []any) any {
		if len(args) == 0 {
			return ""
		}
		return strings.ToUpper(fmt.Sprintf("%v", args[0])) + "!!!"
	})

	// Effect-unknown: reads the clock. Register (not RegisterPure) → callable
	// only via Ffi.callTask.
	Register("myffi.clock", func(args []any) any {
		return time.Now().UnixMilli()
	})

	// Intentionally panicky — verifies panic recovery at FFI boundary.
	// Register (effect-unknown) so the recovery path triggers via callTask.
	Register("myffi.boom", func(args []any) any {
		panic("intentional panic for testing")
	})
}
