//go:build !js

package rt

import "testing"

func TestFfiOpaqueFunctionProvenanceDoesNotChangeModelHash(t *testing.T) {
	function := func(xs []int) int { return len(xs) }
	if hashAny(function) != hashAny(FfiRet(function)) {
		t.Fatal("private opaque function provenance changed model hash")
	}
}
