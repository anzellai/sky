package rt

// nil MUST preserve each original global read at its existing call position.
// Do not combine registration/name reads or capture a default snapshot once.
func codecAutoEnumRegisteredAt(name string, snapshot *codecEnumRegistrySnapshot) bool {
	if snapshot == nil {
		return isRegisteredEnum(name)
	}
	_, ok := snapshot.names[name]
	return ok
}

func codecAutoEnumNameAt(name string, ordinal int, snapshot *codecEnumRegistrySnapshot) (string, bool) {
	if snapshot == nil {
		return enumNameForOrdinal(name, ordinal)
	}
	names, ok := snapshot.names[name]
	if !ok || ordinal < 0 || ordinal >= len(names) {
		return "", false
	}
	return names[ordinal], true
}
