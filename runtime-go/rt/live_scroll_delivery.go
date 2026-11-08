//go:build !js

package rt

import (
	"encoding/base64"
	"encoding/json"
)

// A scroll belongs to the render produced by its dispatch and to one tab.
// It is transient delivery data, never part of the durable session model.
type liveScrollDelivery struct {
	Tab     string    `json:"tab"`
	Command scrollCmd `json:"command"`
}

// Caller holds s.mu, as for runCmd and outgoing render capture.
func (s *liveSession) recordScroll(command scrollCmd) {
	tab := currentLiveOriginTab()
	if tab == "" {
		s.sseConnMu.Lock()
		var oldest uint64
		for id, connection := range s.sseConns {
			if connection.tab != "" && (tab == "" || id < oldest) {
				tab, oldest = connection.tab, id
			}
		}
		s.sseConnMu.Unlock()
	}
	if tab != "" {
		s.dispatchScrolls = append(s.dispatchScrolls, liveScrollDelivery{Tab: tab, Command: command})
	}
}

func (s *liveSession) hasDispatchScrolls() bool { return len(s.dispatchScrolls) != 0 }

func (s *liveSession) takeDispatchScrolls() []liveScrollDelivery {
	commands := s.dispatchScrolls
	s.dispatchScrolls = nil
	return commands
}

// Generic snapshots are also used for mirrors and resyncs, which must never
// steal or replay commands. Only the dispatch's own outgoing capture uses this.
func (s *liveSession) prepareDispatchFrameSnapshot(body string) frameSnapshot {
	snapshot := s.prepareFrameSnapshot(body)
	snapshot.scrolls = s.takeDispatchScrolls()
	return snapshot
}

func (s *liveSession) prepareDispatchFrameSnapshotWithGlobalSeq(body string, globalSeq int64) frameSnapshot {
	snapshot := s.prepareFrameSnapshotWithGlobalSeq(body, globalSeq)
	snapshot.scrolls = s.takeDispatchScrolls()
	return snapshot
}

// Full-HTML event replies retain their legacy body protocol. A non-executable
// trailing comment carries arbitrary Unicode ids without response-header size
// limits. Base64 cannot terminate an HTML comment. New clients remove it before
// patching; older clients merely retain an inert comment after the view root.
func appendLiveScrollMetadata(body string, commands []liveScrollDelivery) string {
	if len(commands) == 0 {
		return body
	}
	data, err := json.Marshal(commands)
	if err != nil {
		// The payload contains only strings and booleans.
		logEmit(logLevelError, "error", "Std.Nav: scroll command encoding failed", map[string]any{"class": "ScrollEncodeFailure"})
		return body
	}
	return body + "<!--sky-scroll:" + base64.StdEncoding.EncodeToString(data) + "-->"
}
