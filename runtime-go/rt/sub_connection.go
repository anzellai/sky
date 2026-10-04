package rt

// Sub.connection — the client's connection state as a subscription.
//
// The Sky.Spa client re-sends a request that fails transiently by itself
// (spa_retry.go) and shows its own quiet "Reconnecting…" indicator and, once
// the retry budget is spent, the red "Can't reach the server" bar. An app that
// wants its own indicator subscribes:
//
//	Std.Sub.connection : (ConnectionState -> msg) -> Sub msg
//	type ConnectionState = Online | Reconnecting | Offline { pending : Int }
//
// The Sky side (sky-stdlib/Std/Sub.sky) builds the ConnectionState from the
// two Ints this kernel delivers (a state code and the pending count), so the
// runtime never builds a Sky record. A change of state reaches `update` as
// `toMsg state`, ahead of a hold RPC (spaSched.dispatchUrgent). Sky.Live and
// the terminal targets keep their own connection handling: the leaf never
// fires there, so the app's `Online` default stands.

// Sub_connectionCodes builds the "connection" Sub. Sky-side surface (internal,
// wrapped by `Sub.connection`):
//
//	connectionCodes : (Int -> Int -> msg) -> Sub msg
func Sub_connectionCodes(toMsg any) SkySub {
	return subT{kind: "connection", toMsg: toMsg}
}

// connectionToMsg is the `toMsg` of the "connection" leaf of a Sub tree, or nil
// when the tree has none. Several leaves: the last one wins.
func connectionToMsg(sub any) any {
	s, ok := sub.(subT)
	if !ok {
		return nil
	}
	switch s.kind {
	case "connection":
		return s.toMsg
	case "batch":
		var found any
		for _, c := range s.batch {
			if f := connectionToMsg(c); f != nil {
				found = f
			}
		}
		return found
	}
	return nil
}

// spaConnMsg is the Msg a "connection" leaf's toMsg makes for a state.
func spaConnMsg(toMsg any, code, pending int) any {
	return sky_call2(toMsg, code, pending)
}
