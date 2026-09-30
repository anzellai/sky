// process_handle_id.go — ids for the runtime's handle types.
//
// Seven Sky types name a runtime resource by an `Int` the runtime hands out:
// `Process`, `Watcher`, `WebSocket`, `WebSocketServer`, `StreamId`,
// `StreamWriter` and `Cache`. (The three `Std.Sync` types carry the Go
// pointer itself, not an id, so they cannot be forged or restored; see
// sync_kernel.go.) The id used to be a per-boot counter starting at 1.
// A handle is an ordinary value, so a Sky.Live session store saved it with the
// model, and after a restart (or on another replica) `Process 3` named
// whatever the third process of the NEW boot was, usually another visitor's
// terminal (A-1). The same held for the five older types, whose constructors
// were even exported, so app code could build any id it liked (A-1b).
//
// The fix has three parts, all here:
//
//   - Every id is a random 62-bit value from crypto/rand (newHandleID). An id
//     from an earlier boot, another replica or a guess resolves to nothing. It
//     stays an `Int`, so a v0.26.1 session that stored one still decodes; its
//     handle just gives `Err` (handleNotLive).
//   - `Process`, `Watcher`, `WebSocket` and `StreamId` made inside a Sky.Live
//     session are owned by that session (the *liveSession, not its id
//     string, so they survive the sign-in rotation). handleCallerAllowed
//     (process_handle_owner.go) lets a caller inside a session use only its
//     own handles; a caller with no session in scope (a Task program, an HTTP
//     handler) holds the handle as a capability. `WebSocketServer` and
//     `StreamWriter` are made by an HTTP handler, never in a session, and a
//     `Cache` is process-wide shared state (usually a top-level binding), so
//     those three have no owner: the random id is their protection.
//   - A handle that does not resolve, or is refused, is `Err`, never another
//     resource.

package rt

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
)

// handleIDMask keeps 62 bits: positive in a Go int64 and in a Sky Int, with
// room to spare.
const handleIDMask = (int64(1) << 62) - 1

// newHandleID returns a random, non-zero 62-bit id. A failing entropy source
// is not survivable for an unguessable id, so it panics (crypto/rand does not
// fail on any platform Sky targets).
func newHandleID() int64 {
	var b [8]byte
	for {
		if _, err := cryptorand.Read(b[:]); err != nil {
			panic("rt.newHandleID: crypto/rand failed: " + err.Error())
		}
		if id := int64(binary.LittleEndian.Uint64(b[:])) & handleIDMask; id != 0 {
			return id
		}
	}
}

// handleNotLive is the Err for a handle id that names nothing in this server:
// closed, from an earlier boot or another replica, or never issued.
func handleNotLive(kind string, id int64) any {
	return ErrInvalidInput(fmt.Sprintf(
		"%s: this handle is not live in this server (id %d: it was closed, its session ended, or it came from another run). "+
			"In v0.27.0 handle ids are random and do not survive a restart: open the resource again. "+
			"see docs/migration/v0.27.md#handle-ids-are-random",
		kind, id))
}

// handleOwnerRefused is the Err for a handle another Sky.Live session owns.
func handleOwnerRefused(kind string) any {
	return ErrPermissionDenied(kind + ": this handle belongs to another session, or was opened outside any session. " +
		"In v0.27.0 a handle opened in a Sky.Live session is private to it: open it in the session that uses it. " +
		"see docs/migration/v0.27.md#handles-belong-to-their-session")
}
