package rt

import "testing"

// The fragment leaf is found through Sub.batch, the last leaf wins, and a tree
// without one yields nil (no delivery).
func TestFragmentToMsgFindsTheLeaf(t *testing.T) {
	first := func(s any) any { return "first:" + s.(string) }
	last := func(s any) any { return "last:" + s.(string) }
	if fragmentToMsg(Sub_none()) != nil {
		t.Fatal("Sub.none has no fragment leaf")
	}
	if fragmentToMsg(Sub_every(1000, "Tick")) != nil {
		t.Fatal("Sub.every has no fragment leaf")
	}
	tree := Sub_batch([]any{Sub_every(1000, "Tick"), Sub_onFragment(first), Sub_batch([]any{Sub_onFragment(last)})})
	got := fragmentToMsg(tree)
	if got == nil {
		t.Fatal("the batched fragment leaf is not found")
	}
	if got.(func(any) any)("x") != "last:x" {
		t.Fatal("the last fragment leaf must win")
	}
}

func TestFragmentOfStripsTheHash(t *testing.T) {
	for in, want := range map[string]string{"": "", "#": "", "#a": "a", "#k=v%20w": "k=v%20w", "a": "a"} {
		if got := fragmentOf(in); got != want {
			t.Errorf("fragmentOf(%q) = %q, want %q", in, got, want)
		}
	}
}
