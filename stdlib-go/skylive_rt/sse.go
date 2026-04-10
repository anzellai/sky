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
			if m.isSubActiveForSession(sid, sub) {
				m.processSubMsg(sid, sub.MsgName, sub.MsgArgs)
			}
		}
	}
}

// isSubActiveForSession re-evaluates subscriptions for the session's current
// model and checks whether the given subscription is still active.
func (m *SSEManager) isSubActiveForSession(sid string, sub SubDef) bool {
	if m.app.Subscriptions == nil {
		return true // no subscriptions function means always active
	}
	sess, ok := m.store.Get(sid)
	if !ok {
		return false
	}
	subValue := m.app.Subscriptions(sess.Model)
	activeSubs := WalkSubValue(subValue, m.app.MsgTagToName)
	for _, active := range activeSubs {
		if active.MsgName == sub.MsgName && active.Kind == sub.Kind {
			return true
		}
	}
	return false
}

// WalkSubValue converts a Sub ADT value into a flat list of SubDefs.
// Sub values are compiled as map[string]any with SkyName field:
//
//	SkyName "SubNone"   → no subscription
//	SkyName "SubTimer"  → V0=interval(int), V1=msg(map with SkyName)
//	SkyName "SubBatch"  → V0=list of Sub values
//
// Also supports struct-based encoding with Tag field for backwards compat.
func WalkSubValue(sub any, msgTagToName func(int) string) []SubDef {
	if sub == nil {
		return nil
	}

	// Try map-based encoding first (SkyName field)
	skyName := extractSkyName(sub)
	if skyName != "" {
		return walkSubByName(sub, skyName, msgTagToName)
	}

	// Fallback to tag-based encoding
	tag := extractSubTag(sub)
	switch tag {
	case 0: // SubNone
		return nil
	case 1: // SubTimer
		return walkSubTimer(sub, "SubTimerValue", "SubTimerValue1", msgTagToName)
	case 2: // SubBatch
		return walkSubBatch(sub, "SubBatchValue", msgTagToName)
	}
	return nil
}

func walkSubByName(sub any, skyName string, msgTagToName func(int) string) []SubDef {
	switch skyName {
	case "SubNone":
		return nil
	case "SubTimer":
		return walkSubTimer(sub, "V0", "V1", msgTagToName)
	case "SubBatch":
		return walkSubBatch(sub, "V0", msgTagToName)
	}
	return nil
}

func walkSubTimer(sub any, intervalField string, msgField string, msgTagToName func(int) string) []SubDef {
	interval := extractIntField(sub, intervalField)
	msgVal := extractField(sub, msgField)
	msgName := ""
	if msgVal != nil {
		// Prefer SkyName from the msg value (the actual constructor name)
		msgName = extractSkyName(msgVal)
		if msgName == "" {
			// Fallback to tag-based lookup
			msgTag := extractSubTag(msgVal)
			if msgTag >= 0 && msgTagToName != nil {
				msgName = msgTagToName(msgTag)
			}
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
}

func walkSubBatch(sub any, listField string, msgTagToName func(int) string) []SubDef {
	listVal := extractField(sub, listField)
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

func extractSkyName(v any) string {
	if m, ok := v.(map[string]any); ok {
		if n, ok := m["SkyName"].(string); ok {
			return n
		}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Struct {
		f := rv.FieldByName("SkyName")
		if f.IsValid() && f.Kind() == reflect.String {
			return f.String()
		}
	}
	return ""
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

		newModel, cmds := m.app.Update(msg, sess.Model)
		newView := m.app.View(newModel)
		AssignSkyIDs(newView)
		patches := Diff(sess.PrevView, newView)

		sess.Model = newModel
		sess.PrevView = newView
		if m.store.Set(sid, sess) {
			if len(patches) > 0 {
				m.SendPatches(sid, patches, "", "")
			}
			if len(cmds) > 0 {
				go func() { m.SpawnCmds(sid, cmds) }()
			}
			return // success
		}
		// Version conflict — retry with fresh session
	}
}


// SpawnCmds processes a list of Cmd actions, spawning goroutines for
// async CmdPerform commands. Called after a successful model save.
func (m *SSEManager) SpawnCmds(sid string, cmds []any) {
	for _, cmd := range cmds {
		action, ok := cmd.(map[string]any)
		if !ok {
			continue
		}
		skyName, _ := action["SkyName"].(string)
		if skyName == "CmdPerform" {
			task := action["task"]
			toMsg, ok := action["toMsg"].(func(any) any)
			if !ok {
				continue
			}
			go m.runCmdTask(sid, task, toMsg)
		}
	}
}


// runCmdTask executes a Task thunk in a goroutine and dispatches
// the result as a Msg through the full update/view/diff/SSE cycle.
func (m *SSEManager) runCmdTask(sid string, task any, toMsg func(any) any) {
	// Execute the task with panic recovery
	var result any
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Cmd] Task panic (sid=%s): %v", sid[:8], r)
				// Build an Err-like result. The toMsg mapper expects
				// a SkyResult which lives in the main package; use a
				// map fallback that sky_asSkyResult can handle.
				result = map[string]any{
					"Tag": 1, "SkyName": "Err",
					"ErrValue": fmt.Sprintf("task panic: %v", r),
				}
			}
		}()
		if thunk, ok := task.(func() any); ok {
			result = thunk()
		} else if thunk2, ok := task.(func(any) any); ok {
			result = thunk2(struct{}{})
		} else {
			result = task
		}
	}()

	// Apply the toMsg mapper to convert Result -> Msg
	var msg any
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Cmd] toMsg panic (sid=%s): %v", sid[:8], r)
			}
		}()
		msg = toMsg(result)
	}()
	if msg == nil {
		return
	}

	// Dispatch the Msg through the full update cycle
	m.processCmdResult(sid, msg)
}


// processCmdResult dispatches a pre-built Msg (from a completed Cmd)
// through the update/view/diff/SSE cycle with session locking and
// optimistic concurrency. Structurally identical to processSubMsg
// but takes a Msg value instead of decoding from name+args.
func (m *SSEManager) processCmdResult(sid string, msg any) {
	m.sessLock.Lock(sid)
	defer m.sessLock.Unlock(sid)

	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		sess, ok := m.store.Get(sid)
		if !ok {
			return // session expired — silently drop
		}
		if m.app.FixModel != nil {
			sess.Model = m.app.FixModel(sess.Model)
		}

		newModel, cmds := m.app.Update(msg, sess.Model)
		newView := m.app.View(newModel)
		AssignSkyIDs(newView)
		patches := Diff(sess.PrevView, newView)

		sess.Model = newModel
		sess.PrevView = newView
		if m.store.Set(sid, sess) {
			if len(patches) > 0 {
				m.SendPatches(sid, patches, "", "")
			}
			// Recursively spawn any new commands from this update
			if len(cmds) > 0 {
				go func() { m.SpawnCmds(sid, cmds) }()
			}
			return // success
		}
		// Version conflict — retry with fresh session
	}
}
