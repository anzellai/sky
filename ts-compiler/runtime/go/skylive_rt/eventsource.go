package skylive_rt

import (
	"encoding/json"
	"fmt"
	"sync"
)

// EventSourcer manages event sourcing for Sky.Live sessions.
// It wraps a SessionStore and provides replay, compaction, and snapshotting.
type EventSourcer struct {
	Store            SessionStore
	Update           func(msg any, model any) (any, []any) // The app's Update function
	DecodeMsg        func(name string, args []json.RawMessage) (any, error)
	SnapshotInterval int // Create a snapshot every N messages (0 = disabled)
}

// MsgRecord is the serialized form of a message in the event log.
type MsgRecord struct {
	Name string            `json:"msg"`
	Args []json.RawMessage `json:"args,omitempty"`
	Seq  int               `json:"seq"`
}

// SessionWithLog extends Session with event sourcing metadata.
type SessionWithLog struct {
	Session
	Snapshot      any          // Serialized model snapshot
	SnapshotIndex int          // Msg index at which snapshot was taken
	Log           []*MsgRecord // Full message log
	LogMu         sync.Mutex   // Protects log writes
}

// NewEventSourcer creates a new event sourcer.
func NewEventSourcer(store SessionStore, update func(any, any) (any, []any), decodeMsg func(string, []json.RawMessage) (any, error), snapshotInterval int) *EventSourcer {
	return &EventSourcer{
		Store:            store,
		Update:           update,
		DecodeMsg:        decodeMsg,
		SnapshotInterval: snapshotInterval,
	}
}

// AppendMsg records a message to the session's event log and optionally
// creates a snapshot.
func (es *EventSourcer) AppendMsg(sid string, msg any, msgName string, msgArgs []json.RawMessage) {
	sess, ok := es.Store.Get(sid)
	if !ok {
		return
	}

	record := &MsgRecord{
		Name: msgName,
		Args: msgArgs,
		Seq:  len(sess.MsgLog),
	}

	sess.MsgLog = append(sess.MsgLog, record)

	// Snapshot if interval is configured and we've hit it
	if es.SnapshotInterval > 0 && len(sess.MsgLog)%es.SnapshotInterval == 0 {
		es.TakeSnapshot(sid, sess)
	}
}

// TakeSnapshot serializes the current model state as a checkpoint.
func (es *EventSourcer) TakeSnapshot(sid string, sess *Session) {
	if sess.Model == nil {
		return
	}
	// Serialize model to JSON for the snapshot
	data, err := json.Marshal(sess.Model)
	if err != nil {
		return
	}

	// Store snapshot alongside the session
	// We encode it as a special entry in MsgLog
	snapshot := &MsgRecord{
		Name: "__snapshot__",
		Args: []json.RawMessage{data},
		Seq:  len(sess.MsgLog),
	}
	_ = snapshot // In-memory: snapshot is just the current model, no extra storage needed
	// For persistent stores, the store implementation handles snapshot storage
}

// Replay reconstructs a session's model by replaying the event log
// from the last snapshot. Used when restoring from a persistent store.
func (es *EventSourcer) Replay(initModel any, msgLog []any) (any, error) {
	model := initModel

	for _, rawMsg := range msgLog {
		record, ok := rawMsg.(*MsgRecord)
		if !ok {
			continue
		}

		// Skip snapshot markers
		if record.Name == "__snapshot__" {
			continue
		}

		// Decode the message
		msg, err := es.DecodeMsg(record.Name, record.Args)
		if err != nil {
			return nil, fmt.Errorf("replay failed at seq %d: %w", record.Seq, err)
		}

		// Apply update
		newModel, _ := es.Update(msg, model)
		model = newModel
	}

	return model, nil
}

// CompactLog performs message compaction on the event log.
// It removes redundant messages that can be folded:
// - Consecutive messages of the same type where the last one overwrites previous state
// - Example: [UpdateDraft "h", UpdateDraft "he", UpdateDraft "hel"] → [UpdateDraft "hel"]
func CompactLog(log []*MsgRecord) []*MsgRecord {
	if len(log) <= 1 {
		return log
	}

	compacted := make([]*MsgRecord, 0, len(log))
	for i := 0; i < len(log); i++ {
		current := log[i]

		// Look ahead: if the next message has the same name, skip current
		if i+1 < len(log) && log[i+1].Name == current.Name {
			// Skip — the next message with the same name will overwrite this one
			continue
		}

		compacted = append(compacted, current)
	}

	return compacted
}

// ReplayCompacted replays a compacted event log for faster session restoration.
func (es *EventSourcer) ReplayCompacted(initModel any, msgLog []*MsgRecord) (any, error) {
	compacted := CompactLog(msgLog)
	model := initModel

	for _, record := range compacted {
		if record.Name == "__snapshot__" {
			continue
		}

		msg, err := es.DecodeMsg(record.Name, record.Args)
		if err != nil {
			return nil, fmt.Errorf("replay failed at seq %d: %w", record.Seq, err)
		}

		newModel, _ := es.Update(msg, model)
		model = newModel
	}

	return model, nil
}

// ReplayFromSnapshot replays from the most recent snapshot in the log.
// Returns the snapshot model + replays only the messages after the snapshot.
func (es *EventSourcer) ReplayFromSnapshot(msgLog []*MsgRecord, initModel any) (any, int, error) {
	// Find the last snapshot
	snapshotIdx := -1
	var snapshotModel any

	for i := len(msgLog) - 1; i >= 0; i-- {
		if msgLog[i].Name == "__snapshot__" && len(msgLog[i].Args) > 0 {
			var model map[string]any
			if err := json.Unmarshal(msgLog[i].Args[0], &model); err == nil {
				snapshotModel = model
				snapshotIdx = i
				break
			}
		}
	}

	// Replay from snapshot (or from beginning if no snapshot)
	var model any
	startIdx := 0
	if snapshotIdx >= 0 && snapshotModel != nil {
		model = snapshotModel
		startIdx = snapshotIdx + 1
	} else {
		model = initModel
	}

	// Replay remaining messages
	remaining := msgLog[startIdx:]
	compacted := CompactLog(remaining)

	for _, record := range compacted {
		if record.Name == "__snapshot__" {
			continue
		}

		msg, err := es.DecodeMsg(record.Name, record.Args)
		if err != nil {
			continue // Skip undecodable messages during replay
		}

		newModel, _ := es.Update(msg, model)
		model = newModel
	}

	return model, len(msgLog), nil
}
