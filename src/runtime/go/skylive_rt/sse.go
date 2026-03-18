package skylive_rt

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// SubDef defines a subscription. For V2, only timer-based subscriptions
// are supported. Each tick fires the specified Msg.
type SubDef struct {
	Kind     string        // "timer" | "none"
	Interval time.Duration // For timer subs
	MsgName  string        // Msg constructor name to fire
	MsgArgs  []json.RawMessage
}

// SSEManager manages Server-Sent Event connections for sessions
// that have active subscriptions.
type SSEManager struct {
	mu          sync.RWMutex
	connections map[string]*SSEConn // sid → connection
	app         *LiveApp
	store       SessionStore
}

// SSEConn represents a single SSE connection.
type SSEConn struct {
	sid     string
	w       http.ResponseWriter
	flusher http.Flusher
	done    chan struct{}
	closed  bool
	mu      sync.Mutex
}

// NewSSEManager creates a new SSE manager.
func NewSSEManager(app *LiveApp, store SessionStore) *SSEManager {
	return &SSEManager{
		connections: make(map[string]*SSEConn),
		app:         app,
		store:       store,
	}
}

// HandleSSE handles GET /_sky/stream?sid=X requests.
func (m *SSEManager) HandleSSE(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	if sid == "" {
		http.Error(w, "missing sid", 400)
		return
	}

	_, ok := m.store.Get(sid)
	if !ok {
		http.Error(w, "session not found", 404)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	conn := &SSEConn{
		sid:     sid,
		w:       w,
		flusher: flusher,
		done:    make(chan struct{}),
	}

	m.mu.Lock()
	// Close existing connection for this sid if any
	if existing, ok := m.connections[sid]; ok {
		existing.Close()
	}
	m.connections[sid] = conn
	m.mu.Unlock()

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Wait for disconnect
	ctx := r.Context()
	select {
	case <-ctx.Done():
	case <-conn.done:
	}

	m.mu.Lock()
	delete(m.connections, sid)
	m.mu.Unlock()
}

// SendPatches sends patches to a specific session's SSE connection.
func (m *SSEManager) SendPatches(sid string, patches []Patch, url string, title string) {
	m.mu.RLock()
	conn, ok := m.connections[sid]
	m.mu.RUnlock()
	if !ok {
		return
	}

	resp := EventResponse{
		Patches: patches,
		URL:     url,
		Title:   title,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.closed {
		return
	}

	fmt.Fprintf(conn.w, "data: %s\n\n", data)
	conn.flusher.Flush()
}

// Close closes an SSE connection.
func (c *SSEConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
}

// RunSubscriptions starts a goroutine that processes timer-based
// subscriptions for all active sessions.
func (m *SSEManager) RunSubscriptions(subs []SubDef) {
	for _, sub := range subs {
		if sub.Kind == "timer" && sub.Interval > 0 {
			go m.runTimerSub(sub)
		}
	}
}

func (m *SSEManager) runTimerSub(sub SubDef) {
	ticker := time.NewTicker(sub.Interval)
	defer ticker.Stop()

	for range ticker.C {
		m.mu.RLock()
		sids := make([]string, 0, len(m.connections))
		for sid := range m.connections {
			sids = append(sids, sid)
		}
		m.mu.RUnlock()

		for _, sid := range sids {
			m.processSubMsg(sid, sub.MsgName, sub.MsgArgs)
		}
	}
}

func (m *SSEManager) processSubMsg(sid string, msgName string, msgArgs []json.RawMessage) {
	sess, ok := m.store.Get(sid)
	if !ok {
		return
	}

	msg, err := m.app.DecodeMsg(msgName, msgArgs)
	if err != nil {
		log.Printf("SSE sub decode error for %s: %v", msgName, err)
		return
	}

	// Run update
	newModel, _ := m.app.Update(msg, sess.Model)

	// Render and diff
	newView := m.app.View(newModel)
	AssignSkyIDs(newView)
	patches := Diff(sess.PrevView, newView)

	// Update session
	sess.Model = newModel
	sess.PrevView = newView

	// Send patches via SSE if there are any changes
	if len(patches) > 0 {
		m.SendPatches(sid, patches, "", "")
	}
}
