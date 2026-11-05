package rt

// One entry owned only by one active list invocation. It never enters a
// returned JSON tree, a global cache, a model, or a retained cache certificate.
// Replacing epoch drops the sole old snapshot; no history or callbacks exist.
type codecDeclaredOwnerMemo struct {
	declared string
	epoch    *codecEnumRegistrySnapshot
	owner    string
}

func codecAutoDeclaredOwnerMemoAt(declared string, snapshot *codecEnumRegistrySnapshot, memo *codecDeclaredOwnerMemo) string {
	owner, _ := codecAutoDeclaredOwnerMemoResultAt(declared, snapshot, memo)
	return owner
}

// The hit bit lets isolated diagnostic tests count actual memo branches without
// production counter increments, callbacks or observer state. Callers discard it.
func codecAutoDeclaredOwnerMemoResultAt(declared string, snapshot *codecEnumRegistrySnapshot, memo *codecDeclaredOwnerMemo) (string, bool) {
	if memo == nil || memo.declared != declared {
		return codecAutoDeclaredOwnerAt(declared, snapshot), false
	}
	if codecAutoDeclaredOwnerPrimitive(declared) {
		return "", false
	}
	// CRITICAL: perform the original live atomic registry read at EVERY lookup
	// position before comparing epoch identity. A pin performs no global read.
	if snapshot == nil {
		snapshot = codecEnumRegistry()
	}
	if memo.epoch == snapshot {
		return memo.owner, true
	}
	owner := snapshot.owners[declared]
	memo.epoch, memo.owner = snapshot, owner
	return owner, false
}
