//go:build !js

// sessionless_reaper.go — the backstop that keeps the two sessionless
// registries (sessionlessStreams in http_stream.go, sessionlessSockets in
// websocket.go) from growing without bound.
//
// # The leak this closes
//
// A stream or socket opened from a plain Sky.Http.Server handler, a direct-Task
// caller or a test has NO live session, so it lives in a process-global map
// instead of on liveSession.streams / .sockets. Session-scoped handles are
// reclaimed by markDone at session teardown (closeAllStreams / closeAllSockets);
// the sessionless maps have no such owner. Removal depended ENTIRELY on user
// code reaching Http.Stream.close / WebSocket.close (or forEachChunk running to
// completion). Two things defeat that:
//
//   - An abrupt client drop, or a handler goroutine that opens a handle and
//     never drains it, leaves an OPEN handle — body/conn unclosed — in the map
//     forever, holding a socket and a net/http (or nhooyr) transport goroutine.
//   - Even a clean upstream EOF only CLOSES the handle (runSpool → Close); the
//     map ENTRY is dropped by unregister*, which only the drain/close paths
//     call. A closed-but-unmapped handle is a small permanent leak per stream.
//
// Refusing sessionless registration outright was considered and rejected: the
// synchronous-relay shape (#373) — a plain HTTP handler that opens an upstream
// stream and forEachChunk-relays it — is a first-class use case with no session
// by construction. So the sound fix is a reaper, not a ban.
//
// # Why the TTL is an IDLE bound, and why 10 minutes
//
// The reaper reaps a sessionless handle when EITHER:
//
//   - it is already Closed (reclaim the dead map entry — always safe), OR
//   - it is OPEN but has shown no sign of life (registration, or a delivered
//     chunk / frame) for sessionlessIdleTTL.
//
// The second is an IDLE bound, not a lifetime bound. A healthy stream or socket
// bumps lastActivityNano on every delivered event, so it never expires no
// matter how long it runs — an hours-long LLM completion or SSE relay is never
// touched. Only a handle that has been open AND silent for the whole TTL is
// reaped. 10 minutes is far longer than any real streaming API's inter-message
// gap (LLM tokens and SSE keepalives arrive seconds apart at most), so silence
// that long means the peer is gone; and it comfortably exceeds the 30 s header
// timeout and the 30 s consumer-stall timeout, so it never races a handle that
// is merely slow to start. Closing the body/conn cascades cleanly: a spool or
// reader goroutine blocked on Read wakes with an error, delivers its terminal
// event, and any forEachChunk drains to completion and unregisters.

package rt

import (
	"sync"
	"time"

	"sky-app/rt/periodic"
)

const (
	// sessionlessIdleTTL — an OPEN sessionless handle idle this long is
	// reaped. Idle = time since registration or last delivered event. See the
	// file header for why this is sound and why 10 minutes.
	sessionlessIdleTTL = 10 * time.Minute

	// sessionlessSweepInterval — how often the reaper walks the two maps. One
	// minute keeps the worst-case over-retention (TTL + interval) near the TTL
	// while costing one cheap map walk a minute on a process that opened at
	// least one sessionless handle.
	sessionlessSweepInterval = 1 * time.Minute
)

// reapableHandle is the shared shape the reaper needs from both handle types.
type reapableHandle interface {
	IsClosed() bool
	lastActivityUnixNano() int64
	Close()
}

var sessionlessReaperOnce sync.Once

// ensureSessionlessReaper starts the reaper exactly once, lazily, the first
// time a sessionless handle is registered. A process that never opens a
// sessionless stream or socket never spawns the goroutine.
func ensureSessionlessReaper() {
	sessionlessReaperOnce.Do(func() {
		go periodic.Every(periodic.Config{
			Name:     "http.sessionless-reaper",
			Interval: sessionlessSweepInterval,
			Report:   periodicReport,
			Work: func(now time.Time) error {
				sweepSessionless(now)
				return nil
			},
		})
	})
}

// sweepSessionless reaps expired/closed entries from both sessionless maps.
// Split out (and taking `now`) so tests can drive it deterministically without
// the ticker.
func sweepSessionless(now time.Time) {
	sweepSessionlessMap(&sessionlessStreams, now)
	sweepSessionlessMap(&sessionlessSockets, now)
}

// sweepSessionlessMap walks one map, deleting entries that are closed or
// idle-expired, closing the open ones it evicts.
func sweepSessionlessMap(m *sync.Map, now time.Time) {
	cutoff := now.Add(-sessionlessIdleTTL).UnixNano()
	m.Range(func(key, val any) bool {
		h, ok := val.(reapableHandle)
		if !ok {
			// Unknown value shape — remove it rather than leak it.
			m.Delete(key)
			return true
		}
		switch {
		case h.IsClosed():
			// Dead handle still mapped — reclaim the entry.
			m.Delete(key)
		case h.lastActivityUnixNano() < cutoff:
			// Open but silent past the idle TTL — close and evict. Closing the
			// body/conn cascades the handle's own goroutines to exit.
			h.Close()
			m.Delete(key)
		}
		return true
	})
}
