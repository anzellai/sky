package rt

import "strings"

// Sub.onFragment — the URL fragment (the text after `#`) as a subscription.
//
// A browser never sends the fragment in a request, so neither Sky.Live's
// server nor the first paint of a Sky.Spa page can know it. The client reports
// it: when the page loads with a fragment, and on every `hashchange`. Sky.Live's
// browser client posts it as the `__skyFragment` event (live.go handleEvent);
// the Sky.Spa wasm client reads `location.hash` itself (live_wasm.go
// reconcileSubs). Either way the app's `subscriptions` must name a
// `Sub.onFragment toMsg` leaf, and the fragment reaches `update` as
// `toMsg fragment`. A terminal app has no URL: the leaf does nothing there.

// Sub_onFragment builds the "fragment" Sub. Sky-side surface:
//
//	Std.Sub.onFragment : (String -> msg) -> Sub msg
func Sub_onFragment(toMsg any) SkySub {
	return subT{kind: "fragment", toMsg: toMsg}
}

// fragmentToMsg is the `toMsg` of the "fragment" leaf of a Sub tree, or nil
// when the tree has none. Several leaves: the last one wins, as for a topic.
func fragmentToMsg(sub any) any {
	s, ok := sub.(subT)
	if !ok {
		return nil
	}
	switch s.kind {
	case "fragment":
		return s.toMsg
	case "batch":
		var found any
		for _, c := range s.batch {
			if f := fragmentToMsg(c); f != nil {
				found = f
			}
		}
		return found
	}
	return nil
}

// fragmentOf is the fragment of a `location.hash` value: the text after the
// leading `#`, as written in the URL (not percent-decoded, like Elm's
// `Url.fragment`). "" when there is none.
func fragmentOf(hash string) string {
	return strings.TrimPrefix(hash, "#")
}
