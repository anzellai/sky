//go:build !js

package rt

// live_nav_delivery.go — Std.Nav on Sky.Live: deliver a "nav" frame to ONE
// tab of the session.
//
// A navigation moves one browser tab. The frame goes to the tab that caused
// the update (the origin tab stamped on the request, live_session_rotation.go),
// or, for work no tab started (a Time.every tick, a pub/sub delivery), to the
// session's oldest connection. That tab fetches the new page like a
// `sky-nav` link, and the page fetch mirrors the new view to the session's
// other tabs (handleInitial), so the route and `onNavigate` run once.
//
// A tab that is not connected yet (an `init` command runs before the page's
// SSE connection is up) gets its frame when its connection registers; the
// newest command for a tab wins, and the map is bounded.

// navPendingMax bounds the tabs a session keeps a pending navigation for.
const navPendingMax = 16

// pushNav sends nc to the tab `tab`, or, when tab is "" (no tab started the
// work), to the session's oldest connection. A named tab that is not
// connected yet gets the frame when it connects; another tab is never moved
// in its place.
func (s *liveSession) pushNav(nc navCmd, tab string) {
	fr := sseFrame{event: "nav", data: navFrameData(nc)}
	s.sseConnMu.Lock()
	var target *sseConn
	var targetID uint64
	for id, c := range s.sseConns {
		if tab != "" {
			if c.tab == tab {
				target = c
				break
			}
			continue
		}
		if target == nil || id < targetID {
			target, targetID = c, id
		}
	}
	if target == nil {
		s.keepPendingNavLocked(tab, fr)
		s.sseConnMu.Unlock()
		return
	}
	s.sseConnMu.Unlock()
	select {
	case target.ch <- fr:
	default:
		// The tab's buffer is full: it is resynced with the current view, but
		// the navigation is not part of the view. Say so.
		recordSseDrop(s.currentSID())
		target.outOfSync.Store(true)
		signalResync(target)
		logEmit(logLevelError, "error",
			"Std.Nav: the navigation to "+quoteForLog(nc.URL)+
				" was dropped because the tab's event buffer is full",
			map[string]any{"class": "NavDropped", "url": nc.URL})
	}
}

// keepPendingNavLocked stores fr for tab (the newest wins). Caller holds
// sseConnMu.
func (s *liveSession) keepPendingNavLocked(tab string, fr sseFrame) {
	if s.navPending == nil {
		s.navPending = map[string]sseFrame{}
	}
	if _, ok := s.navPending[tab]; !ok && len(s.navPending) >= navPendingMax {
		for k := range s.navPending {
			delete(s.navPending, k)
			break
		}
	}
	s.navPending[tab] = fr
}

// deliverPendingNavLocked hands a newly registered connection the navigation
// kept for its tab (or one kept with no tab named). Caller holds sseConnMu.
func (s *liveSession) deliverPendingNavLocked(c *sseConn) {
	if len(s.navPending) == 0 {
		return
	}
	fr, ok := s.navPending[c.tab]
	key := c.tab
	if !ok {
		fr, ok = s.navPending[""]
		key = ""
	}
	if !ok {
		return
	}
	delete(s.navPending, key)
	select {
	case c.ch <- fr:
	default:
	}
}
