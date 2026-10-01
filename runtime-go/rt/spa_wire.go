package rt

// spa_wire.go — the Sky.Spa wire-schema handshake (E-4).
//
// A tab that stays open across a deploy runs the OLD wasm against the NEW
// backend. When the generated wire records changed (a server branch gained a
// follow-up, a request lost a field), the old client's requests still decode
// (extra JSON fields are ignored), so the failure was silent: the follow-up
// writes of the changed branches never ran until the user reloaded.
//
// The handshake:
//
//   - the build computes a hash of the generated wire schema (not of the
//     binary, so replicas with the same schema never conflict). The SSR page
//     carries it as `<meta name="sky-wire" content="<hash>">`, and the
//     generated backend registers it at boot with Spa_setWireHash;
//   - the wasm client sends it as `X-Sky-Wire` on every `/_rpc/` request
//     (http_wasm.go);
//   - a request whose `X-Sky-Wire` differs from the backend's is answered
//     409 with `X-Sky-Status: reload` before the handler runs (rpc_guard.go).
//     The client reloads, at most once per spaWireReloadEvery per tab, and
//     after the reload it tells the user that their last action was not
//     sent. A Msg is never replayed automatically (it may not be idempotent);
//   - a request with NO `X-Sky-Wire` comes from a tab built before v0.27.0,
//     which cannot be told to reload. Spa_isLegacyRpc tells the generated
//     backend so, and the backend then runs the server-bound follow-ups of
//     the branch inline, as v0.26.1 did, and sends an empty `spaFollow_`
//     (`"[]"`) in the response (so a v0.27 client whose header was stripped
//     cannot run them a second time).

import (
	"strings"
	"sync/atomic"
)

// spaWireHeader is the request header carrying the client's wire hash.
const spaWireHeader = "X-Sky-Wire"

// spaWireReloadEvery is the reload guard: a tab reloads for a wire mismatch at
// most once in this many milliseconds, so two replicas on different schemas
// behind a round-robin balancer cannot put it in a reload loop.
const spaWireReloadEvery int64 = 30_000

// spaWireHash is the backend's wire hash ("" = no handshake: nothing is
// checked).
var spaWireHash atomic.Value // string

func currentSpaWireHash() string {
	s, _ := spaWireHash.Load().(string)
	return s
}

// Spa_setWireHash — `Spa_setWireHash : String -> Task Error ()`. The generated
// backend's `main` registers the build's wire hash before the server starts.
func Spa_setWireHash(h any) any {
	return func() any {
		spaWireHash.Store(strings.TrimSpace(AsString(h)))
		return Ok[any, any](struct{}{})
	}
}

// spaWireVerdict classifies a `/_rpc/` request by its X-Sky-Wire header.
type spaWireVerdict int

const (
	spaWireOK       spaWireVerdict = iota // same schema, or no handshake
	spaWireLegacy                         // no header: a pre-v0.27 tab
	spaWireMismatch                       // another schema: reload
)

func spaWireCheck(header string) spaWireVerdict {
	want := currentSpaWireHash()
	if want == "" {
		return spaWireOK
	}
	got := strings.TrimSpace(header)
	switch {
	case got == "":
		return spaWireLegacy
	case got != want:
		return spaWireMismatch
	}
	return spaWireOK
}

// Spa_isLegacyRpc — `Spa_isLegacyRpc : Request -> Bool`. True when the
// backend has a wire hash and the request carries no X-Sky-Wire: a tab from
// before v0.27.0. The generated handler then runs server-bound follow-ups
// inline and sends an empty `spaFollow_` (`"[]"`) in the response.
func Spa_isLegacyRpc(req any) any {
	r, ok := asSkyRequest(req)
	if !ok {
		return false
	}
	h := ""
	for k, v := range r.Headers {
		if strings.EqualFold(k, spaWireHeader) {
			h = AsString(v)
		}
	}
	return spaWireCheck(h) == spaWireLegacy
}

// spaWireShouldReload is the client's reload guard: reload when the last
// wire reload of this tab (lastMs, 0 = never) is at least spaWireReloadEvery
// ago. A timestamp in the future (a clock change) counts as "long ago".
func spaWireShouldReload(lastMs, nowMs int64) bool {
	if lastMs <= 0 || lastMs > nowMs {
		return true
	}
	return nowMs-lastMs >= spaWireReloadEvery
}
