package rt

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Sub.onBreakpoint and Sub.onQuery — two URL / window facts as subscriptions.
//
// Sub.onBreakpoint: the viewport width, reduced to the app's own breakpoints.
// The app lists widths (`[ 600, 900 ]`); it receives the largest listed width
// the viewport is at least (0 below all of them), once at load and again only
// when a resize crosses a listed width. Never a Msg per pixel. Sky.Live's
// browser client learns the list from the page config and from the
// `X-Sky-Breakpoints` header of each event answer, and posts the width as the
// `__skyViewport` event only when its bucket changes; the server computes the
// bucket again from the reported width (the report is untrusted) and dispatches
// only a changed bucket (live.go handleEvent). The Sky.Spa wasm client reads
// `innerWidth` itself (live_wasm.go).
//
// Sub.onQuery: the URL query string, parsed (first value of a repeated key,
// percent-decoded, as `Http.parseQuery`). Sky.Live's server sees the query of
// every page request, the first load and every sky-nav / Std.Nav / Back and
// Forward fetch, and dispatches it in the same step as the route (live.go
// routeAndNavigate); the Sky.Spa wasm client reads `location.search` at load
// and after each navigation. Delivered at load when the query is not empty,
// and on every change after that. A terminal app has no URL or window: both
// leaves do nothing there.

// Sub_onBreakpoint builds the "breakpoint" Sub. Sky-side surface:
//
//	Std.Sub.onBreakpoint : List Int -> (Int -> msg) -> Sub msg
func Sub_onBreakpoint(widths any, toMsg any) SkySub {
	return subT{kind: "breakpoint", breakpoints: normaliseBreakpoints(asList(widths)), toMsg: toMsg}
}

// Sub_onQuery builds the "query" Sub. Sky-side surface:
//
//	Std.Sub.onQuery : (Dict String String -> msg) -> Sub msg
func Sub_onQuery(toMsg any) SkySub {
	return subT{kind: "query", toMsg: toMsg}
}

// normaliseBreakpoints keeps the positive widths, sorted and without repeats.
// A width of 0 or less is never crossed, so it is dropped.
func normaliseBreakpoints(items []any) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(items))
	for _, it := range items {
		w := AsInt(it)
		if w <= 0 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.Ints(out)
	return out
}

// breakpointLeaf is the "breakpoint" leaf of a Sub tree: its `toMsg` and its
// widths. ok is false when the tree has none. Several leaves: the last wins.
func breakpointLeaf(sub any) (toMsg any, widths []int, ok bool) {
	s, isSub := sub.(subT)
	if !isSub {
		return nil, nil, false
	}
	switch s.kind {
	case "breakpoint":
		return s.toMsg, s.breakpoints, s.toMsg != nil
	case "batch":
		for _, c := range s.batch {
			if t, w, found := breakpointLeaf(c); found {
				toMsg, widths, ok = t, w, true
			}
		}
	}
	return toMsg, widths, ok
}

// queryToMsg is the `toMsg` of the "query" leaf of a Sub tree, or nil when the
// tree has none. Several leaves: the last one wins.
func queryToMsg(sub any) any {
	s, ok := sub.(subT)
	if !ok {
		return nil
	}
	switch s.kind {
	case "query":
		return s.toMsg
	case "batch":
		var found any
		for _, c := range s.batch {
			if f := queryToMsg(c); f != nil {
				found = f
			}
		}
		return found
	}
	return nil
}

// breakpointOf is the largest width in `widths` (sorted) that `width` is at
// least, or 0 when it is below all of them.
func breakpointOf(width int, widths []int) int {
	b := 0
	for _, w := range widths {
		if width >= w {
			b = w
		}
	}
	return b
}

// breakpointsHeader renders widths as the comma list the Sky.Live client
// reads from `X-Sky-Breakpoints` and the page config.
func breakpointsHeader(widths []int) string {
	parts := make([]string, len(widths))
	for i, w := range widths {
		parts[i] = strconv.Itoa(w)
	}
	return strings.Join(parts, ",")
}

// queryDict parses a raw query string (with or without the leading `?`) into
// a Sky `Dict String String`: percent-decoded, the first value of a repeated
// key, as `Http.parseQuery`. Malformed pairs are skipped (best effort, like
// net/url.ParseQuery). The input comes from the client: it is data only.
func queryDict(raw string) any {
	raw = strings.TrimPrefix(raw, "?")
	values, _ := url.ParseQuery(raw)
	d := Dict_empty()
	for k, vs := range values {
		v := ""
		if len(vs) > 0 {
			v = vs[0]
		}
		d = Dict_insert(k, v, d)
	}
	return d
}
