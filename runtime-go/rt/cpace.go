// cpace.go — Std.Crypto.Cpace: the CPace balanced PAKE, cipher suite
// CPACE-X25519-SHA512, initiator-responder setting (draft-irtf-cfrg-cpace-21),
// v0.26.2.
//
// CPace turns a low-entropy password shared by two parties into a strong
// shared key (the intermediate session key, ISK) without exposing the
// password to an offline dictionary attack. This implementation follows the
// draft and passes its Appendix B.1 test vectors; it has NOT had an external
// security review, and the draft is not yet an RFC. Std.Crypto.Cpace says so
// in its documentation.
//
// Wire format: a message is lv_cat(Y, AD) — the party's 32-byte public share
// followed by its associated data, each prefixed with its LEB128 length. That
// is exactly the party's half of transcript_ir, so the transcript is the two
// messages concatenated.
package rt

import (
	"crypto/sha512"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

const cpaceDSI = "CPace255"

// cpacePrependLen is prepend_len (draft §6.3, Appendix A.1): the LEB128
// encoding of the length, then the data.
func cpacePrependLen(data []byte) []byte {
	n := len(data)
	var out []byte
	for {
		if n < 128 {
			out = append(out, byte(n))
		} else {
			out = append(out, byte(n&0x7f)|0x80)
		}
		n >>= 7
		if n == 0 {
			break
		}
	}
	return append(out, data...)
}

// cpaceLvCat is lv_cat (Appendix A.1.3).
func cpaceLvCat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, cpacePrependLen(p)...)
	}
	return out
}

// cpaceReadLv reads one prepend_len field, returning the data and the rest.
func cpaceReadLv(b []byte) ([]byte, []byte, bool) {
	n, shift := 0, 0
	for i := 0; i < len(b) && i < 4; i++ {
		n |= int(b[i]&0x7f) << uint(shift)
		if b[i]&0x80 == 0 {
			rest := b[i+1:]
			if n > len(rest) {
				return nil, nil, false
			}
			return rest[:n], rest[n:], true
		}
		shift += 7
	}
	return nil, nil, false
}

// cpaceGeneratorString is generator_string (§8.1, Appendix A.2) for SHA-512
// (input block size 128 bytes).
func cpaceGeneratorString(prs, ci, sid []byte) []byte {
	zpad := 128 - 1 - len(cpacePrependLen(prs)) - len(cpacePrependLen([]byte(cpaceDSI)))
	if zpad < 0 {
		zpad = 0
	}
	return cpaceLvCat([]byte(cpaceDSI), prs, make([]byte, zpad), ci, sid)
}

// cpaceElligator2 maps a field element to a Curve25519 u-coordinate with the
// Elligator 2 map (RFC 9380 §6.7.1; the draft's Appendix A.5 form):
// v = -A / (1 + Z·r²) with Z = 2, and x = v when v³ + A·v² + v is a square,
// else x = -v - A. Constant-time: both branches are computed and selected.
func cpaceElligator2(r *fe25519) *fe25519 {
	var a, two, one, t, v, gv, x2, x fe25519
	a.fe25519Set(486662)
	two.fe25519Set(2)
	one.One()
	t.Square(r)
	t.Multiply(&t, &two)
	t.Add(&t, &one) // 1 + 2r² (never zero: -1/2 is not a square mod p)
	t.Invert(&t)
	v.Multiply(&a, &t)
	v.Negate(&v) // v = -A / (1 + 2r²)
	gv.Square(&v)
	var av fe25519
	av.Multiply(&a, &v)
	gv.Add(&gv, &av)
	gv.Add(&gv, &one)
	gv.Multiply(&gv, &v) // v³ + A·v² + v = v·(v² + A·v + 1)
	_, isSquare := new(fe25519).SqrtRatio(&gv, &one)
	x2.Negate(&v)
	x2.Subtract(&x2, &a) // -v - A
	x.Select(&v, &x2, isSquare)
	return &x
}

// fe25519Set sets v to a small integer.
func (v *fe25519) fe25519Set(n uint64) *fe25519 {
	*v = fe25519{l0: n}
	return v
}

// cpaceGenerator is G_X25519.calculate_generator (§8.2).
func cpaceGenerator(prs, ci, sid []byte) []byte {
	h := sha512.Sum512(cpaceGeneratorString(prs, ci, sid))
	u := h[:32]
	u[31] &= 0x7f // decodeUCoordinate: clear bit 255
	var r fe25519
	if _, err := r.SetBytes(u); err != nil {
		panic("rt: CPace generator: " + err.Error())
	}
	return cpaceElligator2(&r).Bytes()
}

// cpaceShare computes Y = X25519(y, g).
func cpaceShare(y, g []byte) ([]byte, error) {
	out, err := curve25519.X25519(y, g)
	if err != nil {
		return nil, errors.New("the password-derived generator is degenerate")
	}
	return out, nil
}

// cpaceIsk computes ISK = SHA-512(lv_cat(DSI || "_ISK", sid, K) || transcript_ir).
func cpaceIsk(sid, k, msgA, msgB []byte) []byte {
	in := cpaceLvCat([]byte(cpaceDSI+"_ISK"), sid, k)
	in = append(in, msgA...)
	in = append(in, msgB...)
	h := sha512.Sum512(in)
	return h[:]
}

// cpaceParse splits a message into its public share and associated data.
func cpaceParse(msg []byte) (y, ad []byte, err error) {
	y, rest, ok := cpaceReadLv(msg)
	if !ok || len(y) != 32 {
		return nil, nil, errors.New("the message is not a CPace message (a 32-byte share and associated data)")
	}
	ad, rest, ok = cpaceReadLv(rest)
	if !ok || len(rest) != 0 {
		return nil, nil, errors.New("the message is not a CPace message (a 32-byte share and associated data)")
	}
	return y, ad, nil
}

// CpaceState is the Go value of `Std.Crypto.Cpace.Pending`: the initiator's
// secret scalar and first message, waiting for the responder's reply. It
// redacts itself, refuses gob, and can be finished once.
type CpaceState struct {
	ya   string
	sid  string
	msgA string
	used *noiseGuard
}

func (CpaceState) String() string               { return "Cpace.Pending([REDACTED])" }
func (CpaceState) GoString() string             { return "Cpace.Pending([REDACTED])" }
func (CpaceState) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "Cpace.Pending([REDACTED])") }
func (CpaceState) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (CpaceState) GobEncode() ([]byte, error)   { return nil, errKeyNotStorable }
func (*CpaceState) GobDecode([]byte) error      { return errKeyNotStorable }

func cpaceStartWith(prs, ci, sid, ad, ya []byte) (CpaceState, []byte, error) {
	g := cpaceGenerator(prs, ci, sid)
	yA, err := cpaceShare(ya, g)
	if err != nil {
		return CpaceState{}, nil, err
	}
	msgA := cpaceLvCat(yA, ad)
	return CpaceState{ya: string(ya), sid: string(sid), msgA: string(msgA), used: &noiseGuard{}}, msgA, nil
}

func cpaceRespondWith(prs, ci, sid, ad, msgA, yb []byte) (isk, msgB []byte, err error) {
	yA, _, err := cpaceParse(msgA)
	if err != nil {
		return nil, nil, err
	}
	g := cpaceGenerator(prs, ci, sid)
	yB, err := cpaceShare(yb, g)
	if err != nil {
		return nil, nil, err
	}
	k, err := kxShared(yb, yA)
	if err != nil {
		return nil, nil, errors.New("the initiator's share is a low-order point; abort")
	}
	msgB = cpaceLvCat(yB, ad)
	return cpaceIsk(sid, k, msgA, msgB), msgB, nil
}

func (st CpaceState) finish(msgB []byte) ([]byte, error) {
	if !st.used.claim(0) {
		return nil, errors.New("this Pending value was already finished; start a new exchange")
	}
	yB, _, err := cpaceParse(msgB)
	if err != nil {
		return nil, err
	}
	k, err := kxShared([]byte(st.ya), yB)
	if err != nil {
		return nil, errors.New("the responder's share is a low-order point; abort")
	}
	return cpaceIsk([]byte(st.sid), k, []byte(st.msgA), msgB), nil
}

func cpaceScalar() ([]byte, error) {
	r := Kx_generate(nil).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		return nil, errors.New("could not draw a scalar")
	}
	return []byte(r.OkValue.(KxSecretKey).k), nil
}

// ─── Kernels ───────────────────────────────────────────────────────

// Cpace.start : Secret -> Bytes -> Bytes -> Bytes -> Task Error ( Pending, Bytes )
// (password, channel identifier, session identifier, associated data).
func Cpace_start(prs any, ci any, sid any, ad any) any {
	return func() any {
		ya, err := cpaceScalar()
		if err != nil {
			return Err[any, any](ErrFfi("Cpace.start: " + err.Error()))
		}
		st, msg, err := cpaceStartWith([]byte(secretReveal(prs)), []byte(AsString(ci)), []byte(AsString(sid)), []byte(AsString(ad)), ya)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Cpace.start: " + err.Error()))
		}
		return Ok[any, any](SkyTuple2{V0: st, V1: string(msg)})
	}
}

// Cpace.respond : Secret -> Bytes -> Bytes -> Bytes -> Bytes -> Task Error ( Secret, Bytes )
// (password, channel identifier, session identifier, associated data, the
// initiator's message) → (ISK, the reply).
func Cpace_respond(prs any, ci any, sid any, ad any, msgA any) any {
	return func() any {
		yb, err := cpaceScalar()
		if err != nil {
			return Err[any, any](ErrFfi("Cpace.respond: " + err.Error()))
		}
		isk, msgB, err := cpaceRespondWith([]byte(secretReveal(prs)), []byte(AsString(ci)), []byte(AsString(sid)), []byte(AsString(ad)), []byte(AsString(msgA)), yb)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Cpace.respond: " + err.Error()))
		}
		return Ok[any, any](SkyTuple2{V0: Secret{v: string(isk)}, V1: string(msgB)})
	}
}

// Cpace.finish : Pending -> Bytes -> Result Error Secret.
func Cpace_finish(st any, msgB any) any {
	s, ok := st.(CpaceState)
	if !ok {
		panic("rt: expected a Cpace Pending value")
	}
	isk, err := s.finish([]byte(AsString(msgB)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Cpace.finish: " + err.Error()))
	}
	return Ok[any, any](Secret{v: string(isk)})
}

// Cpace.messageData : Bytes -> Result Error Bytes — the associated data a
// message carries (sent in clear), so a responder can pick the password for
// the identity the initiator names.
func Cpace_messageData(msg any) any {
	_, ad, err := cpaceParse([]byte(AsString(msg)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Cpace.messageData: " + err.Error()))
	}
	return Ok[any, any](string(ad))
}
