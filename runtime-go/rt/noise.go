// noise.go — Std.Crypto.Noise: the Noise_IK_25519_ChaChaPoly_SHA256
// handshake (The Noise Protocol Framework, revision 34), v0.26.2.
//
//	IK:
//	  <- s
//	  ...
//	  -> e, es, s, ss
//	  <- e, ee, se
//
// The initiator knows the responder's static public key in advance; the
// responder learns the initiator's static key from the first message. After
// the two messages both sides hold a Transport: one CipherState per direction
// with a 64-bit counter nonce, and Rekey (§11.3).
//
// # State values are single-use
//
// Sky values are immutable, so every step returns a new Handshake or
// Transport. Reusing an OLD value would reuse a ChaCha20-Poly1305 nonce (or
// an ephemeral key) — the one mistake this construction cannot survive. Each
// value therefore carries its position in the session, and a shared guard
// records the position the session has reached: an operation on any value
// other than the latest one returns an Err instead of encrypting. The guard is
// the only mutable part, it is updated with a compare-and-swap, and it is
// never observable except as that Err.
package rt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	noiseProtocolName = "Noise_IK_25519_ChaChaPoly_SHA256"
	noiseMaxMsg       = 65535
	noiseTagLen       = 16
	noiseMaxNonce     = ^uint64(0) // reserved for rekey (§5.1)
)

var errNoiseStale = errors.New("this state value was already used (a newer one exists); " +
	"use the value the last step returned — reusing an old one would reuse a nonce")

type noiseCipher struct {
	k    [32]byte
	hasK bool
	n    uint64
}

func (c *noiseCipher) nonce(n uint64) []byte {
	var iv [12]byte
	binary.LittleEndian.PutUint64(iv[4:], n)
	return iv[:]
}

func (c *noiseCipher) encrypt(ad, pt []byte) ([]byte, error) {
	if !c.hasK {
		return append([]byte(nil), pt...), nil
	}
	if c.n == noiseMaxNonce {
		return nil, errors.New("the nonce counter is exhausted; rekey or start a new session")
	}
	aead, _ := chacha20poly1305.New(c.k[:])
	out := aead.Seal(nil, c.nonce(c.n), pt, ad)
	c.n++
	return out, nil
}

func (c *noiseCipher) decrypt(ad, ct []byte) ([]byte, error) {
	if !c.hasK {
		return append([]byte(nil), ct...), nil
	}
	if c.n == noiseMaxNonce {
		return nil, errors.New("the nonce counter is exhausted; rekey or start a new session")
	}
	aead, _ := chacha20poly1305.New(c.k[:])
	pt, err := aead.Open(nil, c.nonce(c.n), ct, ad)
	if err != nil {
		return nil, errors.New("authentication failed (wrong key or a tampered message)")
	}
	c.n++
	return pt, nil
}

// rekey implements REKEY(k) = ENCRYPT(k, maxnonce, zerolen, zeros[32]) (§4.2).
func (c *noiseCipher) rekey() {
	aead, _ := chacha20poly1305.New(c.k[:])
	var zeros [32]byte
	out := aead.Seal(nil, c.nonce(noiseMaxNonce), zeros[:], nil)
	copy(c.k[:], out[:32])
}

type noiseSymmetric struct {
	cs noiseCipher
	ck [32]byte
	h  [32]byte
}

func noiseHmac(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// noiseHkdf2 is HKDF(chaining_key, ikm, 2) of §4.3.
func noiseHkdf2(ck, ikm []byte) (o1, o2 []byte) {
	tmp := noiseHmac(ck, ikm)
	o1 = noiseHmac(tmp, []byte{1})
	o2 = noiseHmac(tmp, o1, []byte{2})
	return
}

func (s *noiseSymmetric) mixHash(data []byte) {
	s.h = sha256.Sum256(append(s.h[:], data...))
}

func (s *noiseSymmetric) mixKey(ikm []byte) {
	ck, k := noiseHkdf2(s.ck[:], ikm)
	copy(s.ck[:], ck)
	copy(s.cs.k[:], k)
	s.cs.hasK = true
	s.cs.n = 0
}

func (s *noiseSymmetric) encryptAndHash(pt []byte) ([]byte, error) {
	ct, err := s.cs.encrypt(s.h[:], pt)
	if err != nil {
		return nil, err
	}
	s.mixHash(ct)
	return ct, nil
}

func (s *noiseSymmetric) decryptAndHash(ct []byte) ([]byte, error) {
	pt, err := s.cs.decrypt(s.h[:], ct)
	if err != nil {
		return nil, err
	}
	s.mixHash(ct)
	return pt, nil
}

// noiseGuard records the latest position of one session (or one direction of
// a transport); see the file header.
type noiseGuard struct{ at atomic.Uint64 }

func (g *noiseGuard) claim(pos uint64) bool { return g.at.CompareAndSwap(pos, pos+1) }

// NoiseHandshake is the Go value of `Std.Crypto.Noise.Handshake`. It holds
// secret keys, so it redacts itself like a Secret.
type NoiseHandshake struct {
	initiator bool
	step      uint64 // 0: before message 1, 1: before message 2, 2: complete, 3: split
	sym       noiseSymmetric
	s         string // static secret scalar
	sPub      string
	e         string // ephemeral secret scalar (drawn at construction)
	ePub      string
	rs        string // remote static (initiator: known; responder: learned)
	re        string
	guard     *noiseGuard
}

func (NoiseHandshake) String() string   { return "Noise.Handshake([REDACTED])" }
func (NoiseHandshake) GoString() string { return "Noise.Handshake([REDACTED])" }
func (NoiseHandshake) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "Noise.Handshake([REDACTED])")
}
func (NoiseHandshake) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (NoiseHandshake) GobEncode() ([]byte, error)   { return nil, errKeyNotStorable }
func (*NoiseHandshake) GobDecode([]byte) error      { return errKeyNotStorable }

// NoiseTransport is the Go value of `Std.Crypto.Noise.Transport`.
type NoiseTransport struct {
	send, recv       noiseCipher
	sendAt, recvAt   uint64
	sendGrd, recvGrd *noiseGuard
	hash             [32]byte
	peer             string
}

func (NoiseTransport) String() string   { return "Noise.Transport([REDACTED])" }
func (NoiseTransport) GoString() string { return "Noise.Transport([REDACTED])" }
func (NoiseTransport) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "Noise.Transport([REDACTED])")
}
func (NoiseTransport) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (NoiseTransport) GobEncode() ([]byte, error)   { return nil, errKeyNotStorable }
func (*NoiseTransport) GobDecode([]byte) error      { return errKeyNotStorable }

// noiseNew builds a handshake with a given ephemeral scalar. The kernels draw
// the ephemeral from crypto/rand; the Go tests pass the vectors' fixed ones.
func noiseNew(initiator bool, s, rs, prologue, e string) NoiseHandshake {
	hs := NoiseHandshake{initiator: initiator, s: s, sPub: kxPublic(s), e: e, ePub: kxPublic(e), rs: rs, guard: &noiseGuard{}}
	copy(hs.sym.h[:], noiseProtocolName) // exactly HASHLEN bytes: used as is (§5.2)
	hs.sym.ck = hs.sym.h
	hs.sym.mixHash([]byte(prologue))
	if initiator {
		hs.sym.mixHash([]byte(rs)) // pre-message "<- s"
	} else {
		hs.sym.mixHash([]byte(hs.sPub))
	}
	return hs
}

func noiseDH(sk, pk string) ([]byte, error) {
	out, err := kxShared([]byte(sk), []byte(pk))
	if err != nil {
		return nil, errors.New("the peer sent a low-order public key (the shared secret would be all zeros)")
	}
	return out, nil
}

// writeMessage produces the next handshake message. The receiver of hs is a
// copy; the caller gets the advanced copy back.
func (hs NoiseHandshake) writeMessage(payload []byte) (NoiseHandshake, []byte, error) {
	if !hs.guard.claim(hs.step) {
		return hs, nil, errNoiseStale
	}
	var out []byte
	fail := func(err error) (NoiseHandshake, []byte, error) {
		hs.guard.at.Store(^uint64(0)) // a failed step poisons the session
		return hs, nil, err
	}
	switch {
	case hs.initiator && hs.step == 0: // -> e, es, s, ss
		out = append(out, hs.ePub...)
		hs.sym.mixHash([]byte(hs.ePub))
		dh, err := noiseDH(hs.e, hs.rs)
		if err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
		ct, err := hs.sym.encryptAndHash([]byte(hs.sPub))
		if err != nil {
			return fail(err)
		}
		out = append(out, ct...)
		if dh, err = noiseDH(hs.s, hs.rs); err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
	case !hs.initiator && hs.step == 1: // <- e, ee, se
		out = append(out, hs.ePub...)
		hs.sym.mixHash([]byte(hs.ePub))
		dh, err := noiseDH(hs.e, hs.re)
		if err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
		if dh, err = noiseDH(hs.e, hs.rs); err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
	default:
		hs.guard.at.Store(hs.step) // not our turn: release the claim unchanged
		return hs, nil, errors.New("it is not this side's turn to write a handshake message")
	}
	ct, err := hs.sym.encryptAndHash(payload)
	if err != nil {
		return fail(err)
	}
	out = append(out, ct...)
	if len(out) > noiseMaxMsg {
		return fail(errors.New("the handshake message is longer than 65535 bytes"))
	}
	hs.step++
	return hs, out, nil
}

// readMessage consumes the peer's next handshake message.
func (hs NoiseHandshake) readMessage(msg []byte) (NoiseHandshake, []byte, error) {
	if !hs.guard.claim(hs.step) {
		return hs, nil, errNoiseStale
	}
	fail := func(err error) (NoiseHandshake, []byte, error) {
		hs.guard.at.Store(^uint64(0))
		return hs, nil, err
	}
	if len(msg) > noiseMaxMsg {
		return fail(errors.New("the handshake message is longer than 65535 bytes"))
	}
	switch {
	case !hs.initiator && hs.step == 0: // -> e, es, s, ss
		if len(msg) < 32+32+noiseTagLen+noiseTagLen {
			return fail(errors.New("the first handshake message is too short"))
		}
		hs.re = string(msg[:32])
		hs.sym.mixHash(msg[:32])
		dh, err := noiseDH(hs.s, hs.re)
		if err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
		rs, err := hs.sym.decryptAndHash(msg[32 : 32+32+noiseTagLen])
		if err != nil {
			return fail(err)
		}
		hs.rs = string(rs)
		if dh, err = noiseDH(hs.s, hs.rs); err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
		msg = msg[32+32+noiseTagLen:]
	case hs.initiator && hs.step == 1: // <- e, ee, se
		if len(msg) < 32+noiseTagLen {
			return fail(errors.New("the second handshake message is too short"))
		}
		hs.re = string(msg[:32])
		hs.sym.mixHash(msg[:32])
		dh, err := noiseDH(hs.e, hs.re)
		if err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
		if dh, err = noiseDH(hs.s, hs.re); err != nil {
			return fail(err)
		}
		hs.sym.mixKey(dh)
		msg = msg[32:]
	default:
		hs.guard.at.Store(hs.step)
		return hs, nil, errors.New("it is not this side's turn to read a handshake message")
	}
	pt, err := hs.sym.decryptAndHash(msg)
	if err != nil {
		return fail(err)
	}
	hs.step++
	return hs, pt, nil
}

// split turns a completed handshake into a Transport (§5.2 Split()).
func (hs NoiseHandshake) split() (NoiseTransport, error) {
	if hs.step != 2 {
		return NoiseTransport{}, errors.New("the handshake is not complete (two messages are needed)")
	}
	if !hs.guard.claim(2) {
		return NoiseTransport{}, errNoiseStale
	}
	k1, k2 := noiseHkdf2(hs.sym.ck[:], nil)
	var c1, c2 noiseCipher
	copy(c1.k[:], k1)
	copy(c2.k[:], k2)
	c1.hasK, c2.hasK = true, true
	t := NoiseTransport{hash: hs.sym.h, peer: hs.rs, sendGrd: &noiseGuard{}, recvGrd: &noiseGuard{}}
	if hs.initiator {
		t.send, t.recv = c1, c2
	} else {
		t.send, t.recv = c2, c1
	}
	return t, nil
}

func (t NoiseTransport) encrypt(pt []byte) (NoiseTransport, []byte, error) {
	if len(pt)+noiseTagLen > noiseMaxMsg {
		return t, nil, errors.New("the message is longer than 65535 bytes with its tag")
	}
	if !t.sendGrd.claim(t.sendAt) {
		return t, nil, errNoiseStale
	}
	ct, err := t.send.encrypt(nil, pt)
	if err != nil {
		t.sendGrd.at.Store(^uint64(0))
		return t, nil, err
	}
	t.sendAt++
	return t, ct, nil
}

func (t NoiseTransport) decrypt(ct []byte) (NoiseTransport, []byte, error) {
	if len(ct) > noiseMaxMsg {
		return t, nil, errors.New("the message is longer than 65535 bytes")
	}
	if !t.recvGrd.claim(t.recvAt) {
		return t, nil, errNoiseStale
	}
	pt, err := t.recv.decrypt(nil, ct)
	if err != nil {
		// A failed decrypt does not advance the nonce (the message was not
		// accepted), so the same value may try the next message.
		t.recvGrd.at.Store(t.recvAt)
		return t, nil, err
	}
	t.recvAt++
	return t, pt, nil
}

// ─── Kernels ───────────────────────────────────────────────────────

func asNoiseHs(v any) NoiseHandshake {
	if h, ok := v.(NoiseHandshake); ok {
		return h
	}
	panic("rt: expected a Noise Handshake")
}

func asNoiseT(v any) NoiseTransport {
	if t, ok := v.(NoiseTransport); ok {
		return t
	}
	panic("rt: expected a Noise Transport")
}

func noiseEphemeral() (string, error) {
	r := Kx_generate(nil).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		return "", errors.New("could not draw an ephemeral key")
	}
	return r.OkValue.(KxSecretKey).k, nil
}

// Noise.initiator : Kx.SecretKey -> Kx.PublicKey -> Bytes -> Task Error Handshake
// (own static key, the responder's static public key, prologue).
func Noise_initiator(s any, rs any, prologue any) any {
	return func() any {
		e, err := noiseEphemeral()
		if err != nil {
			return Err[any, any](ErrFfi("Noise.initiator: " + err.Error()))
		}
		return Ok[any, any](noiseNew(true, asKxSecret(s).k, asKxPublic(rs).k, AsString(prologue), e))
	}
}

// Noise.responder : Kx.SecretKey -> Bytes -> Task Error Handshake.
func Noise_responder(s any, prologue any) any {
	return func() any {
		e, err := noiseEphemeral()
		if err != nil {
			return Err[any, any](ErrFfi("Noise.responder: " + err.Error()))
		}
		return Ok[any, any](noiseNew(false, asKxSecret(s).k, "", AsString(prologue), e))
	}
}

// Noise.writeMessage : Bytes -> Handshake -> Result Error ( Handshake, Bytes ).
func Noise_writeMessage(payload any, hs any) any {
	next, msg, err := asNoiseHs(hs).writeMessage([]byte(AsString(payload)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Noise.writeMessage: " + err.Error()))
	}
	return Ok[any, any](SkyTuple2{V0: next, V1: string(msg)})
}

// Noise.readMessage : Bytes -> Handshake -> Result Error ( Handshake, Bytes ).
func Noise_readMessage(msg any, hs any) any {
	next, pt, err := asNoiseHs(hs).readMessage([]byte(AsString(msg)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Noise.readMessage: " + err.Error()))
	}
	return Ok[any, any](SkyTuple2{V0: next, V1: string(pt)})
}

// Noise.isComplete : Handshake -> Bool.
func Noise_isComplete(hs any) any { return asNoiseHs(hs).step >= 2 }

// Noise.peer : Handshake -> Maybe Kx.PublicKey — the remote static key, once
// it is known (the initiator knows it from the start; the responder after it
// reads the first message).
func Noise_peer(hs any) any {
	h := asNoiseHs(hs)
	if h.rs == "" {
		return Nothing[any]()
	}
	return Just[any](KxPublicKey{k: h.rs})
}

// Noise.transport : Handshake -> Result Error Transport.
func Noise_transport(hs any) any {
	t, err := asNoiseHs(hs).split()
	if err != nil {
		return Err[any, any](ErrInvalidInput("Noise.transport: " + err.Error()))
	}
	return Ok[any, any](t)
}

// Noise.transportPeer : Transport -> Kx.PublicKey.
func Noise_transportPeer(t any) any { return KxPublicKey{k: asNoiseT(t).peer} }

// Noise.handshakeHash : Transport -> Bytes — the handshake hash h, for
// channel binding (§11.2).
func Noise_handshakeHash(t any) any {
	h := asNoiseT(t).hash
	return string(h[:])
}

// Noise.encrypt : Bytes -> Transport -> Result Error ( Transport, Bytes ).
func Noise_encrypt(pt any, t any) any {
	next, ct, err := asNoiseT(t).encrypt([]byte(AsString(pt)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Noise.encrypt: " + err.Error()))
	}
	return Ok[any, any](SkyTuple2{V0: next, V1: string(ct)})
}

// Noise.decrypt : Bytes -> Transport -> Result Error ( Transport, Bytes ).
func Noise_decrypt(ct any, t any) any {
	next, pt, err := asNoiseT(t).decrypt([]byte(AsString(ct)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Noise.decrypt: " + err.Error()))
	}
	return Ok[any, any](SkyTuple2{V0: next, V1: string(pt)})
}

// Noise.rekeySend : Transport -> Result Error Transport — Rekey() of the
// sending CipherState (§11.3). Both sides must rekey at the same point.
func Noise_rekeySend(t any) any {
	tr := asNoiseT(t)
	if !tr.sendGrd.claim(tr.sendAt) {
		return Err[any, any](ErrInvalidInput("Noise.rekeySend: " + errNoiseStale.Error()))
	}
	tr.send.rekey()
	tr.sendAt++
	return Ok[any, any](tr)
}

// Noise.rekeyReceive : Transport -> Result Error Transport.
func Noise_rekeyReceive(t any) any {
	tr := asNoiseT(t)
	if !tr.recvGrd.claim(tr.recvAt) {
		return Err[any, any](ErrInvalidInput("Noise.rekeyReceive: " + errNoiseStale.Error()))
	}
	tr.recv.rekey()
	tr.recvAt++
	return Ok[any, any](tr)
}
