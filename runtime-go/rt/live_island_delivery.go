//go:build !js

package rt

// live_island_delivery.go — the Cmd.toIsland delivery contract on Sky.Live.
//
// A widget command must reach the widget once and in order, or the client
// must learn that one was lost and resync the island (remount it from its
// current props and tell the app with the island event "resync"). A command
// is never lost silently.
//
// Why a contract and not a lossless queue: a command can be lost at five
// places, and three of them are outside any queue the server could hold —
// the session's ingress channel (full when the relay falls behind), a
// connection's buffer (full when the tab reads slowly), the queue kept while
// no tab is connected (bounded), the handover of that queue to a new
// connection, and the frames still buffered for a connection that dies.
// Blocking the producer instead would stall `update` behind the slowest tab.
// So every loss is made DETECTABLE, with one mechanism:
//
//   - every command gets a per-island sequence number g (1, 2, 3, ...) when
//     it is pushed (islandFrame), carried in the frame as "seq";
//   - each connection keeps, per island, the highest g it was sent or lost
//     (its high-water mark, hwm): a frame delivered to its buffer and a frame
//     dropped for it both raise it, under the connection's lock;
//   - the connection writes an "islandsync" event {"e": epoch, "r": reason,
//     "s": {id: g}}: on connect (the baseline: what this tab should already
//     have), after a drop (at once: the drop signals the connection's
//     resync) and with every heartbeat. Before it states the hwm map it
//     writes every frame still in its buffer, so the map never claims a
//     frame the tab has not been given (islandSyncFor);
//   - the client (island_client.go) remounts an island whose map entry is
//     above the last seq it received, or whose next frame skips a seq, and
//     then ignores frames at or below the resync point.
//
// The epoch is per session object: after a server restart (a session read
// back from a store) it changes, and the client resyncs every island that
// had received commands, since the old process's buffers are gone.

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// islandState is a session's island bookkeeping. g is guarded by gmu (the
// producer side); f, noConnLost and epoch by the session's sseConnMu (the
// relay side).
type islandState struct {
	gmu sync.Mutex
	g   map[string]int64
	// f: per island, the highest seq the relay has handled (fanned out,
	// queued for a later connection, or known lost). A connecting tab should
	// already have everything up to it, except the queued commands it is
	// about to receive.
	f map[string]int64
	// noConnLost: per island, the lowest seq lost while no tab was
	// connected (the queue's overflow, an ingress drop); the next
	// connection's baseline starts below it, so that tab sees the gap.
	noConnLost map[string]int64
	epoch      string
}

// connIslands is a connection's island bookkeeping, guarded by mu.
type connIslands struct {
	mu   sync.Mutex
	hwm  map[string]int64
	base map[string]int64
}

func (s *liveSession) islandEpoch() string {
	if s.islands.epoch == "" {
		b := make([]byte, 8)
		_, _ = crand.Read(b)
		s.islands.epoch = hex.EncodeToString(b)
	}
	return s.islands.epoch
}

// islandFrame numbers a command and builds its SSE frame.
func (s *liveSession) islandFrame(ic islandCmd) sseFrame {
	s.islands.gmu.Lock()
	if s.islands.g == nil {
		s.islands.g = map[string]int64{}
	}
	s.islands.g[ic.ID]++
	ic.Seq = s.islands.g[ic.ID]
	s.islands.gmu.Unlock()
	return sseFrame{event: "island", data: islandFrameData(ic), island: ic.ID, g: ic.Seq}
}

// pushIslandCmd is Cmd.toIsland on Sky.Live: number the command and hand it
// to the relay. A full ingress channel loses it; every connection then
// learns of the loss (islandLost) and resyncs.
func (s *liveSession) pushIslandCmd(ic islandCmd) {
	if s.sseCh == nil {
		return
	}
	fr := s.islandFrame(ic)
	select {
	case s.sseCh <- fr:
	default:
		recordSseDrop(s.currentSID())
		s.islandLost(fr.island, fr.g)
		s.markAllConnsOutOfSync()
	}
}

// islandLost records a command that no connection received.
func (s *liveSession) islandLost(id string, g int64) {
	s.sseConnMu.Lock()
	defer s.sseConnMu.Unlock()
	s.islandHandledLocked(id, g)
	if len(s.sseConns) == 0 {
		s.islandNoConnLostLocked(id, g)
		return
	}
	for _, c := range s.sseConns {
		c.islands.mu.Lock()
		raiseMark(c.islands.hwm, id, g)
		c.islands.mu.Unlock()
	}
}

func raiseMark(m map[string]int64, id string, g int64) {
	if g > m[id] {
		m[id] = g
	}
}

// islandHandledLocked raises the relay's mark (caller holds sseConnMu).
func (s *liveSession) islandHandledLocked(id string, g int64) {
	if s.islands.f == nil {
		s.islands.f = map[string]int64{}
	}
	raiseMark(s.islands.f, id, g)
}

func (s *liveSession) islandNoConnLostLocked(id string, g int64) {
	if s.islands.noConnLost == nil {
		s.islands.noConnLost = map[string]int64{}
	}
	if cur, ok := s.islands.noConnLost[id]; !ok || g < cur {
		s.islands.noConnLost[id] = g
	}
}

// islandSendLocked delivers an island frame to one connection, or drops it,
// raising the connection's mark either way. Caller holds sseConnMu; it
// reports whether the frame fitted.
func islandSendLocked(c *sseConn, fr sseFrame) bool {
	c.islands.mu.Lock()
	defer c.islands.mu.Unlock()
	if c.islands.hwm == nil {
		c.islands.hwm = map[string]int64{}
	}
	raiseMark(c.islands.hwm, fr.island, fr.g)
	select {
	case c.ch <- fr:
		return true
	default:
		return false
	}
}

// initConnIslandsLocked sets a new connection's baseline and mark from the
// relay's state and hands it the queued commands. Caller holds sseConnMu.
// It reports whether the connection must be resynced at once (it inherits
// lost commands, or the queue did not fit its buffer).
func (s *liveSession) initConnIslandsLocked(c *sseConn) bool {
	c.islands.hwm = map[string]int64{}
	c.islands.base = map[string]int64{}
	for id, g := range s.islands.f {
		c.islands.hwm[id] = g
		c.islands.base[id] = g
	}
	resync := false
	for id, g := range s.islands.noConnLost {
		if g-1 < c.islands.base[id] {
			c.islands.base[id] = g - 1
		}
		resync = true
	}
	seen := map[string]bool{}
	for _, fr := range s.islandPending {
		if fr.island != "" && !seen[fr.island] {
			seen[fr.island] = true
			if fr.g-1 < c.islands.base[fr.island] {
				c.islands.base[fr.island] = fr.g - 1
			}
		}
	}
	for _, fr := range s.islandPending {
		if fr.island == "" {
			select {
			case c.ch <- fr:
			default:
				resync = true
			}
			continue
		}
		if !islandSendLocked(c, fr) {
			resync = true
		}
	}
	s.islandPending = nil
	s.islands.noConnLost = nil
	return resync
}

// islandHelloBase is what connection id should already have, per island.
func (s *liveSession) islandHelloBase(id uint64) map[string]int64 {
	s.sseConnMu.Lock()
	c := s.sseConns[id]
	s.sseConnMu.Unlock()
	out := map[string]int64{}
	if c == nil {
		return out
	}
	c.islands.mu.Lock()
	for k, v := range c.islands.base {
		out[k] = v
	}
	c.islands.mu.Unlock()
	return out
}

// islandSyncFor takes connection id's mark, then empties its buffer: the
// frames to write BEFORE the sync map (so the map never claims a frame the
// tab has not been given), and the map. A frame buffered after the mark was
// taken carries a higher seq than the map and is written after it.
func (s *liveSession) islandSyncFor(id uint64) ([]sseFrame, map[string]int64) {
	s.sseConnMu.Lock()
	c := s.sseConns[id]
	s.sseConnMu.Unlock()
	out := map[string]int64{}
	if c == nil {
		return nil, out
	}
	c.islands.mu.Lock()
	for k, v := range c.islands.hwm {
		out[k] = v
	}
	c.islands.mu.Unlock()
	var frames []sseFrame
	for {
		select {
		case fr := <-c.ch:
			frames = append(frames, fr)
			continue
		default:
		}
		break
	}
	return frames, out
}

// islandSyncData is the "islandsync" event's data.
func (s *liveSession) islandSyncData(reason string, m map[string]int64) string {
	s.sseConnMu.Lock()
	e := s.islandEpoch()
	s.sseConnMu.Unlock()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]int64, len(m))
	for _, k := range keys {
		ordered[k] = m[k]
	}
	b, _ := json.Marshal(map[string]any{"e": e, "r": reason, "s": ordered})
	return string(b)
}

// writeSSEEvent writes one SSE event (newlines in data escaped, as every
// frame of the stream is) and flushes.
func writeSSEEvent(w io.Writer, flusher http.Flusher, event, data string) error {
	if event == "" {
		event = "patch"
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, strings.ReplaceAll(data, "\n", "\\n")); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// writeIslandSync writes connection id's buffered frames, then its island
// sync map.
func (s *liveSession) writeIslandSync(w io.Writer, flusher http.Flusher, id uint64, reason string) error {
	frames, m := s.islandSyncFor(id)
	for _, fr := range frames {
		if err := writeSSEEvent(w, flusher, fr.event, fr.data); err != nil {
			return err
		}
	}
	if len(m) == 0 && reason == "heartbeat" {
		return nil
	}
	return writeSSEEvent(w, flusher, "islandsync", s.islandSyncData(reason, m))
}
