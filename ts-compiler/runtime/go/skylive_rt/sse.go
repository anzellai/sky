package skylive_rt

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
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
	sessLock    *SessionLocker
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
func NewSSEManager(app *LiveApp, store SessionStore, sessLock *SessionLocker) *SSEManager {
	return &SSEManager{
		connections: make(map[string]*SSEConn),
		app:         app,
		store:       store,
		sessLock:    sessLock,
	}
}

// HandleSSE handles GET /_sky/stream?sid=X requests.
func (m *SSEManager) HandleSSE(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	if sid == "" {
		http.Error(w, "missing sid", 400)
		return
	}

	// Session cookie validation — prevent eavesdropping on other sessions
	cookie, err := r.Cookie("sky_sid")
	if err != nil || cookie.Value != sid {
		http.Error(w, "invalid session", 403)
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

// WalkSubValue converts a Sub ADT value into a flat list of SubDefs.
// Sub is compiled as a Go struct with Tag field:
//
//	Tag 0 = SubNone
//	Tag 1 = SubTimer (SubTimerValue=interval, SubTimerValue1=msg)
//	Tag 2 = SubBatch (SubBatchValue=list of Sub values)
func WalkSubValue(sub any, msgTagToName func(int) string) []SubDef {
	if sub == nil {
		return nil
	}
	tag := extractSubTag(sub)
	switch tag {
	case 0: // SubNone
		return nil
	case 1: // SubTimer
		interval := extractIntField(sub, "SubTimerValue")
		msgVal := extractField(sub, "SubTimerValue1")
		msgName := ""
		if msgVal != nil {
			msgTag := extractSubTag(msgVal)
			if msgTag >= 0 && msgTagToName != nil {
				msgName = msgTagToName(msgTag)
			}
		}
		if interval > 0 && msgName != "" {
			return []SubDef{{
				Kind:     "timer",
				Interval: time.Duration(interval) * time.Millisecond,
				MsgName:  msgName,
			}}
		}
		return nil
	case 2: // SubBatch
		listVal := extractField(sub, "SubBatchValue")
		if listVal == nil {
			return nil
		}
		lst, ok := listVal.([]any)
		if !ok {
			return nil
		}
		var result []SubDef
		for _, item := range lst {
			result = append(result, WalkSubValue(item, msgTagToName)...)
		}
		return result
	}
	return nil
}

func extractSubTag(v any) int {
	switch val := v.(type) {
	case map[string]any:
		if t, ok := val["Tag"]; ok {
			switch n := t.(type) {
			case int:
				return n
			case float64:
				return int(n)
			}
		}
	}
	// Try struct via reflection
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Struct {
		f := rv.FieldByName("Tag")
		if f.IsValid() {
			switch f.Kind() {
			case reflect.Int, reflect.Int64, reflect.Int32:
				return int(f.Int())
			}
		}
	}
	return -1
}

func extractField(v any, fieldName string) any {
	if m, ok := v.(map[string]any); ok {
		return m[fieldName]
	}
	// For compiled Go structs, use reflection to access named fields
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Struct {
		f := rv.FieldByName(fieldName)
		if f.IsValid() {
			return f.Interface()
		}
	}
	return nil
}

func extractIntField(v any, fieldName string) int {
	val := extractField(v, fieldName)
	if val == nil {
		return 0
	}
	switch n := val.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func (m *SSEManager) processSubMsg(sid string, msgName string, msgArgs []json.RawMessage) {
	// Lock this session to prevent race with event handling (single-instance)
	m.sessLock.Lock(sid)
	defer m.sessLock.Unlock(sid)

	msg, err := m.app.DecodeMsg(msgName, msgArgs)
	if err != nil {
		log.Printf("[SSE] decode error for %s: %v", msgName, err)
		return
	}

	// Optimistic concurrency retry loop
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		sess, ok := m.store.Get(sid)
		if !ok {
			return
		}

		newModel, _ := m.app.Update(msg, sess.Model)
		newView := m.app.View(newModel)
		AssignSkyIDs(newView)
		patches := Diff(sess.PrevView, newView)

		sess.Model = newModel
		sess.PrevView = newView
		if m.store.Set(sid, sess) {
			if len(patches) > 0 {
				m.SendPatches(sid, patches, "", "")
			}
			return // success
		}
		// Version conflict — retry with fresh session
	}
}
