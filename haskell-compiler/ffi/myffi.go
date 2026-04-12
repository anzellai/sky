package rt

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	// Pure: deterministic, no I/O
	Register("myffi.reverse", func(args []any) any {
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

	// Pure
	Register("myffi.shout", func(args []any) any {
		if len(args) == 0 {
			return ""
		}
		return strings.ToUpper(fmt.Sprintf("%v", args[0])) + "!!!"
	})

	// Effectful: reads the clock — MUST be called via Ffi.callTask
	Register("myffi.clock", func(args []any) any {
		return time.Now().UnixMilli()
	})

	// Intentionally panicky — verifies panic recovery at FFI boundary
	Register("myffi.boom", func(args []any) any {
		panic("intentional panic for testing")
	})
}
