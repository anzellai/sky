// cpace_field.go — arithmetic modulo 2^255-19, for the Elligator 2 map of
// Std.Crypto.Cpace.
//
// Vendored from the Go standard library,
// crypto/internal/fips140/edwards25519/field (fe.go and the portable
// fe_generic.go, Go 1.26.1), which cannot be imported from outside the
// standard library. Only the identifiers are renamed (Element → fe25519, the
// package-level helpers gain a fe prefix) so they cannot collide inside
// package rt. The arithmetic is constant-time, which matters here: the
// generator is derived from the password.
//
// Copyright (c) 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the Go LICENSE file:
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   - Redistributions of source code must retain the above copyright
//     notice, this list of conditions and the following disclaimer.
//   - Redistributions in binary form must reproduce the above
//     copyright notice, this list of conditions and the following disclaimer
//     in the documentation and/or other materials provided with the
//     distribution.
//   - Neither the name of Google LLC nor the names of its
//     contributors may be used to endorse or promote products derived from
//     this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
package rt

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"math/bits"
)

// fe25519 represents an element of the field GF(2^255-19). Note that this
// is not a cryptographically secure group, and should only be used to interact
// with edwards25519.Point coordinates.
//
// This type works similarly to math/big.Int, and all arguments and receivers
// are allowed to alias.
//
// The zero value is a valid zero element.
type fe25519 struct {
	// An element t represents the integer
	//     t.l0 + t.l1*2^51 + t.l2*2^102 + t.l3*2^153 + t.l4*2^204
	//
	// Between operations, all limbs are expected to be lower than 2^52.
	l0 uint64
	l1 uint64
	l2 uint64
	l3 uint64
	l4 uint64
}

const feMaskLow51 uint64 = (1 << 51) - 1

var fe25519Zero = &fe25519{0, 0, 0, 0, 0}

// Zero sets v = 0, and returns v.
func (v *fe25519) Zero() *fe25519 {
	*v = *fe25519Zero
	return v
}

var fe25519One = &fe25519{1, 0, 0, 0, 0}

// One sets v = 1, and returns v.
func (v *fe25519) One() *fe25519 {
	*v = *fe25519One
	return v
}

// reduce reduces v modulo 2^255 - 19 and returns it.
func (v *fe25519) reduce() *fe25519 {
	v.carryPropagate()

	// After the light reduction we now have a field element representation
	// v < 2^255 + 2^13 * 19, but need v < 2^255 - 19.

	// If v >= 2^255 - 19, then v + 19 >= 2^255, which would overflow 2^255 - 1,
	// generating a carry. That is, c will be 0 if v < 2^255 - 19, and 1 otherwise.
	c := (v.l0 + 19) >> 51
	c = (v.l1 + c) >> 51
	c = (v.l2 + c) >> 51
	c = (v.l3 + c) >> 51
	c = (v.l4 + c) >> 51

	// If v < 2^255 - 19 and c = 0, this will be a no-op. Otherwise, it's
	// effectively applying the reduction identity to the carry.
	v.l0 += 19 * c

	v.l1 += v.l0 >> 51
	v.l0 = v.l0 & feMaskLow51
	v.l2 += v.l1 >> 51
	v.l1 = v.l1 & feMaskLow51
	v.l3 += v.l2 >> 51
	v.l2 = v.l2 & feMaskLow51
	v.l4 += v.l3 >> 51
	v.l3 = v.l3 & feMaskLow51
	// no additional carry
	v.l4 = v.l4 & feMaskLow51

	return v
}

// Add sets v = a + b, and returns v.
func (v *fe25519) Add(a, b *fe25519) *fe25519 {
	v.l0 = a.l0 + b.l0
	v.l1 = a.l1 + b.l1
	v.l2 = a.l2 + b.l2
	v.l3 = a.l3 + b.l3
	v.l4 = a.l4 + b.l4
	return v.carryPropagate()
}

// Subtract sets v = a - b, and returns v.
func (v *fe25519) Subtract(a, b *fe25519) *fe25519 {
	// We first add 2 * p, to guarantee the subtraction won't underflow, and
	// then subtract b (which can be up to 2^255 + 2^13 * 19).
	v.l0 = (a.l0 + 0xFFFFFFFFFFFDA) - b.l0
	v.l1 = (a.l1 + 0xFFFFFFFFFFFFE) - b.l1
	v.l2 = (a.l2 + 0xFFFFFFFFFFFFE) - b.l2
	v.l3 = (a.l3 + 0xFFFFFFFFFFFFE) - b.l3
	v.l4 = (a.l4 + 0xFFFFFFFFFFFFE) - b.l4
	return v.carryPropagate()
}

// Negate sets v = -a, and returns v.
func (v *fe25519) Negate(a *fe25519) *fe25519 {
	return v.Subtract(fe25519Zero, a)
}

// Invert sets v = 1/z mod p, and returns v.
//
// If z == 0, Invert returns v = 0.
func (v *fe25519) Invert(z *fe25519) *fe25519 {
	// Inversion is implemented as exponentiation with exponent p − 2. It uses the
	// same sequence of 255 squarings and 11 multiplications as [Curve25519].
	var z2, z9, z11, z2_5_0, z2_10_0, z2_20_0, z2_50_0, z2_100_0, t fe25519

	z2.Square(z)             // 2
	t.Square(&z2)            // 4
	t.Square(&t)             // 8
	z9.Multiply(&t, z)       // 9
	z11.Multiply(&z9, &z2)   // 11
	t.Square(&z11)           // 22
	z2_5_0.Multiply(&t, &z9) // 31 = 2^5 - 2^0

	t.Square(&z2_5_0) // 2^6 - 2^1
	for i := 0; i < 4; i++ {
		t.Square(&t) // 2^10 - 2^5
	}
	z2_10_0.Multiply(&t, &z2_5_0) // 2^10 - 2^0

	t.Square(&z2_10_0) // 2^11 - 2^1
	for i := 0; i < 9; i++ {
		t.Square(&t) // 2^20 - 2^10
	}
	z2_20_0.Multiply(&t, &z2_10_0) // 2^20 - 2^0

	t.Square(&z2_20_0) // 2^21 - 2^1
	for i := 0; i < 19; i++ {
		t.Square(&t) // 2^40 - 2^20
	}
	t.Multiply(&t, &z2_20_0) // 2^40 - 2^0

	t.Square(&t) // 2^41 - 2^1
	for i := 0; i < 9; i++ {
		t.Square(&t) // 2^50 - 2^10
	}
	z2_50_0.Multiply(&t, &z2_10_0) // 2^50 - 2^0

	t.Square(&z2_50_0) // 2^51 - 2^1
	for i := 0; i < 49; i++ {
		t.Square(&t) // 2^100 - 2^50
	}
	z2_100_0.Multiply(&t, &z2_50_0) // 2^100 - 2^0

	t.Square(&z2_100_0) // 2^101 - 2^1
	for i := 0; i < 99; i++ {
		t.Square(&t) // 2^200 - 2^100
	}
	t.Multiply(&t, &z2_100_0) // 2^200 - 2^0

	t.Square(&t) // 2^201 - 2^1
	for i := 0; i < 49; i++ {
		t.Square(&t) // 2^250 - 2^50
	}
	t.Multiply(&t, &z2_50_0) // 2^250 - 2^0

	t.Square(&t) // 2^251 - 2^1
	t.Square(&t) // 2^252 - 2^2
	t.Square(&t) // 2^253 - 2^3
	t.Square(&t) // 2^254 - 2^4
	t.Square(&t) // 2^255 - 2^5

	return v.Multiply(&t, &z11) // 2^255 - 21
}

// Set sets v = a, and returns v.
func (v *fe25519) Set(a *fe25519) *fe25519 {
	*v = *a
	return v
}

// SetBytes sets v to x, where x is a 32-byte little-endian encoding. If x is
// not of the right length, SetBytes returns nil and an error, and the
// receiver is unchanged.
//
// Consistent with RFC 7748, the most significant bit (the high bit of the
// last byte) is ignored, and non-canonical values (2^255-19 through 2^255-1)
// are accepted. Note that this is laxer than specified by RFC 8032, but
// consistent with most Ed25519 implementations.
func (v *fe25519) SetBytes(x []byte) (*fe25519, error) {
	if len(x) != 32 {
		return nil, errors.New("edwards25519: invalid field element input size")
	}

	// Bits 0:51 (bytes 0:8, bits 0:64, shift 0, mask 51).
	v.l0 = binary.LittleEndian.Uint64(x[0:8])
	v.l0 &= feMaskLow51
	// Bits 51:102 (bytes 6:14, bits 48:112, shift 3, mask 51).
	v.l1 = binary.LittleEndian.Uint64(x[6:14]) >> 3
	v.l1 &= feMaskLow51
	// Bits 102:153 (bytes 12:20, bits 96:160, shift 6, mask 51).
	v.l2 = binary.LittleEndian.Uint64(x[12:20]) >> 6
	v.l2 &= feMaskLow51
	// Bits 153:204 (bytes 19:27, bits 152:216, shift 1, mask 51).
	v.l3 = binary.LittleEndian.Uint64(x[19:27]) >> 1
	v.l3 &= feMaskLow51
	// Bits 204:255 (bytes 24:32, bits 192:256, shift 12, mask 51).
	// Note: not bytes 25:33, shift 4, to avoid overread.
	v.l4 = binary.LittleEndian.Uint64(x[24:32]) >> 12
	v.l4 &= feMaskLow51

	return v, nil
}

// Bytes returns the canonical 32-byte little-endian encoding of v.
func (v *fe25519) Bytes() []byte {
	// This function is outlined to make the allocations inline in the caller
	// rather than happen on the heap.
	var out [32]byte
	return v.bytes(&out)
}

func (v *fe25519) bytes(out *[32]byte) []byte {
	t := *v
	t.reduce()

	// Pack five 51-bit limbs into four 64-bit words:
	//
	//  255    204    153    102     51      0
	//    ├──l4──┼──l3──┼──l2──┼──l1──┼──l0──┤
	//   ├───u3───┼───u2───┼───u1───┼───u0───┤
	// 256      192      128       64        0

	u0 := t.l1<<51 | t.l0
	u1 := t.l2<<(102-64) | t.l1>>(64-51)
	u2 := t.l3<<(153-128) | t.l2>>(128-102)
	u3 := t.l4<<(204-192) | t.l3>>(192-153)

	binary.LittleEndian.PutUint64(out[0*8:], u0)
	binary.LittleEndian.PutUint64(out[1*8:], u1)
	binary.LittleEndian.PutUint64(out[2*8:], u2)
	binary.LittleEndian.PutUint64(out[3*8:], u3)

	return out[:]
}

// Equal returns 1 if v and u are equal, and 0 otherwise.
func (v *fe25519) Equal(u *fe25519) int {
	sa, sv := u.Bytes(), v.Bytes()
	return subtle.ConstantTimeCompare(sa, sv)
}

// feMask64 returns 0xffffffff if cond is 1, and 0 otherwise.
func feMask64(cond int) uint64 { return ^(uint64(cond) - 1) }

// Select sets v to a if cond == 1, and to b if cond == 0.
func (v *fe25519) Select(a, b *fe25519, cond int) *fe25519 {
	m := feMask64(cond)
	v.l0 = (m & a.l0) | (^m & b.l0)
	v.l1 = (m & a.l1) | (^m & b.l1)
	v.l2 = (m & a.l2) | (^m & b.l2)
	v.l3 = (m & a.l3) | (^m & b.l3)
	v.l4 = (m & a.l4) | (^m & b.l4)
	return v
}

// Swap swaps v and u if cond == 1 or leaves them unchanged if cond == 0, and returns v.
func (v *fe25519) Swap(u *fe25519, cond int) {
	m := feMask64(cond)
	t := m & (v.l0 ^ u.l0)
	v.l0 ^= t
	u.l0 ^= t
	t = m & (v.l1 ^ u.l1)
	v.l1 ^= t
	u.l1 ^= t
	t = m & (v.l2 ^ u.l2)
	v.l2 ^= t
	u.l2 ^= t
	t = m & (v.l3 ^ u.l3)
	v.l3 ^= t
	u.l3 ^= t
	t = m & (v.l4 ^ u.l4)
	v.l4 ^= t
	u.l4 ^= t
}

// IsNegative returns 1 if v is negative, and 0 otherwise.
func (v *fe25519) IsNegative() int {
	return int(v.Bytes()[0] & 1)
}

// Absolute sets v to |u|, and returns v.
func (v *fe25519) Absolute(u *fe25519) *fe25519 {
	return v.Select(new(fe25519).Negate(u), u, u.IsNegative())
}

// Multiply sets v = x * y, and returns v.
func (v *fe25519) Multiply(x, y *fe25519) *fe25519 {
	feMul(v, x, y)
	return v
}

// Square sets v = x * x, and returns v.
func (v *fe25519) Square(x *fe25519) *fe25519 {
	feSquare(v, x)
	return v
}

// Mult32 sets v = x * y, and returns v.
func (v *fe25519) Mult32(x *fe25519, y uint32) *fe25519 {
	x0lo, x0hi := feMul51(x.l0, y)
	x1lo, x1hi := feMul51(x.l1, y)
	x2lo, x2hi := feMul51(x.l2, y)
	x3lo, x3hi := feMul51(x.l3, y)
	x4lo, x4hi := feMul51(x.l4, y)
	v.l0 = x0lo + 19*x4hi // carried over per the reduction identity
	v.l1 = x1lo + x0hi
	v.l2 = x2lo + x1hi
	v.l3 = x3lo + x2hi
	v.l4 = x4lo + x3hi
	// The hi portions are going to be only 32 bits, plus any previous excess,
	// so we can skip the carry propagation.
	return v
}

// feMul51 returns lo + hi * 2⁵¹ = a * b.
func feMul51(a uint64, b uint32) (lo uint64, hi uint64) {
	mh, ml := bits.Mul64(a, uint64(b))
	lo = ml & feMaskLow51
	hi = (mh << 13) | (ml >> 51)
	return
}

// Pow22523 set v = x^((p-5)/8), and returns v. (p-5)/8 is 2^252-3.
func (v *fe25519) Pow22523(x *fe25519) *fe25519 {
	var t0, t1, t2 fe25519

	t0.Square(x)             // x^2
	t1.Square(&t0)           // x^4
	t1.Square(&t1)           // x^8
	t1.Multiply(x, &t1)      // x^9
	t0.Multiply(&t0, &t1)    // x^11
	t0.Square(&t0)           // x^22
	t0.Multiply(&t1, &t0)    // x^31
	t1.Square(&t0)           // x^62
	for i := 1; i < 5; i++ { // x^992
		t1.Square(&t1)
	}
	t0.Multiply(&t1, &t0)     // x^1023 -> 1023 = 2^10 - 1
	t1.Square(&t0)            // 2^11 - 2
	for i := 1; i < 10; i++ { // 2^20 - 2^10
		t1.Square(&t1)
	}
	t1.Multiply(&t1, &t0)     // 2^20 - 1
	t2.Square(&t1)            // 2^21 - 2
	for i := 1; i < 20; i++ { // 2^40 - 2^20
		t2.Square(&t2)
	}
	t1.Multiply(&t2, &t1)     // 2^40 - 1
	t1.Square(&t1)            // 2^41 - 2
	for i := 1; i < 10; i++ { // 2^50 - 2^10
		t1.Square(&t1)
	}
	t0.Multiply(&t1, &t0)     // 2^50 - 1
	t1.Square(&t0)            // 2^51 - 2
	for i := 1; i < 50; i++ { // 2^100 - 2^50
		t1.Square(&t1)
	}
	t1.Multiply(&t1, &t0)      // 2^100 - 1
	t2.Square(&t1)             // 2^101 - 2
	for i := 1; i < 100; i++ { // 2^200 - 2^100
		t2.Square(&t2)
	}
	t1.Multiply(&t2, &t1)     // 2^200 - 1
	t1.Square(&t1)            // 2^201 - 2
	for i := 1; i < 50; i++ { // 2^250 - 2^50
		t1.Square(&t1)
	}
	t0.Multiply(&t1, &t0)     // 2^250 - 1
	t0.Square(&t0)            // 2^251 - 2
	t0.Square(&t0)            // 2^252 - 4
	return v.Multiply(&t0, x) // 2^252 - 3 -> x^(2^252-3)
}

// feSqrtM1 is 2^((p-1)/4), which squared is equal to -1 by Euler's Criterion.
var feSqrtM1 = &fe25519{1718705420411056, 234908883556509,
	2233514472574048, 2117202627021982, 765476049583133}

// SqrtRatio sets r to the non-negative square root of the ratio of u and v.
//
// If u/v is square, SqrtRatio returns r and 1. If u/v is not square, SqrtRatio
// sets r according to Section 4.3 of draft-irtf-cfrg-ristretto255-decaf448-00,
// and returns r and 0.
func (r *fe25519) SqrtRatio(u, v *fe25519) (R *fe25519, wasSquare int) {
	t0 := new(fe25519)

	// r = (u * v3) * (u * v7)^((p-5)/8)
	v2 := new(fe25519).Square(v)
	uv3 := new(fe25519).Multiply(u, t0.Multiply(v2, v))
	uv7 := new(fe25519).Multiply(uv3, t0.Square(v2))
	rr := new(fe25519).Multiply(uv3, t0.Pow22523(uv7))

	check := new(fe25519).Multiply(v, t0.Square(rr)) // check = v * r^2

	uNeg := new(fe25519).Negate(u)
	correctSignSqrt := check.Equal(u)
	flippedSignSqrt := check.Equal(uNeg)
	flippedSignSqrtI := check.Equal(t0.Multiply(uNeg, feSqrtM1))

	rPrime := new(fe25519).Multiply(rr, feSqrtM1) // r_prime = SQRT_M1 * r
	// r = CT_SELECT(r_prime IF flipped_sign_sqrt | flipped_sign_sqrt_i ELSE r)
	rr.Select(rPrime, rr, flippedSignSqrt|flippedSignSqrtI)

	r.Absolute(rr) // Choose the nonnegative square root.
	return r, correctSignSqrt | flippedSignSqrt
}

// feUint128 holds a 128-bit number as two 64-bit limbs, for use with the
// bits.Mul64 and bits.Add64 intrinsics.
type feUint128 struct {
	lo, hi uint64
}

// mul returns a * b.
func feMul64(a, b uint64) feUint128 {
	hi, lo := bits.Mul64(a, b)
	return feUint128{lo, hi}
}

// feAddMul returns v + a * b.
func feAddMul(v feUint128, a, b uint64) feUint128 {
	hi, lo := bits.Mul64(a, b)
	lo, c := bits.Add64(lo, v.lo, 0)
	hi, _ = bits.Add64(hi, v.hi, c)
	return feUint128{lo, hi}
}

// feMul19 returns v * 19.
func feMul19(v uint64) uint64 {
	// Using this approach seems to yield better optimizations than *19.
	return v + (v+v<<3)<<1
}

// feAddMul19 returns v + 19 * a * b, where a and b are at most 52 bits.
func feAddMul19(v feUint128, a, b uint64) feUint128 {
	hi, lo := bits.Mul64(feMul19(a), b)
	lo, c := bits.Add64(lo, v.lo, 0)
	hi, _ = bits.Add64(hi, v.hi, c)
	return feUint128{lo, hi}
}

// feAddMul38 returns v + 38 * a * b, where a and b are at most 52 bits.
func feAddMul38(v feUint128, a, b uint64) feUint128 {
	hi, lo := bits.Mul64(feMul19(a), b*2)
	lo, c := bits.Add64(lo, v.lo, 0)
	hi, _ = bits.Add64(hi, v.hi, c)
	return feUint128{lo, hi}
}

// feShr51 returns a >> 51. a is assumed to be at most 115 bits.
func feShr51(a feUint128) uint64 {
	return (a.hi << (64 - 51)) | (a.lo >> 51)
}

func feMulGeneric(v, a, b *fe25519) {
	a0 := a.l0
	a1 := a.l1
	a2 := a.l2
	a3 := a.l3
	a4 := a.l4

	b0 := b.l0
	b1 := b.l1
	b2 := b.l2
	b3 := b.l3
	b4 := b.l4

	// Limb multiplication works like pen-and-paper columnar multiplication, but
	// with 51-bit limbs instead of digits.
	//
	//                          a4   a3   a2   a1   a0  x
	//                          b4   b3   b2   b1   b0  =
	//                         ------------------------
	//                        a4b0 a3b0 a2b0 a1b0 a0b0  +
	//                   a4b1 a3b1 a2b1 a1b1 a0b1       +
	//              a4b2 a3b2 a2b2 a1b2 a0b2            +
	//         a4b3 a3b3 a2b3 a1b3 a0b3                 +
	//    a4b4 a3b4 a2b4 a1b4 a0b4                      =
	//   ----------------------------------------------
	//      r8   r7   r6   r5   r4   r3   r2   r1   r0
	//
	// We can then use the reduction identity (a * 2²⁵⁵ + b = a * 19 + b) to
	// reduce the limbs that would overflow 255 bits. r5 * 2²⁵⁵ becomes 19 * r5,
	// r6 * 2³⁰⁶ becomes 19 * r6 * 2⁵¹, etc.
	//
	// Reduction can be carried out simultaneously to multiplication. For
	// example, we do not compute r5: whenever the result of a multiplication
	// belongs to r5, like a1b4, we multiply it by 19 and add the result to r0.
	//
	//            a4b0    a3b0    a2b0    a1b0    a0b0  +
	//            a3b1    a2b1    a1b1    a0b1 19×a4b1  +
	//            a2b2    a1b2    a0b2 19×a4b2 19×a3b2  +
	//            a1b3    a0b3 19×a4b3 19×a3b3 19×a2b3  +
	//            a0b4 19×a4b4 19×a3b4 19×a2b4 19×a1b4  =
	//           --------------------------------------
	//              r4      r3      r2      r1      r0
	//
	// Finally we add up the columns into wide, overlapping limbs.

	// r0 = a0×b0 + 19×(a1×b4 + a2×b3 + a3×b2 + a4×b1)
	r0 := feMul64(a0, b0)
	r0 = feAddMul19(r0, a1, b4)
	r0 = feAddMul19(r0, a2, b3)
	r0 = feAddMul19(r0, a3, b2)
	r0 = feAddMul19(r0, a4, b1)

	// r1 = a0×b1 + a1×b0 + 19×(a2×b4 + a3×b3 + a4×b2)
	r1 := feMul64(a0, b1)
	r1 = feAddMul(r1, a1, b0)
	r1 = feAddMul19(r1, a2, b4)
	r1 = feAddMul19(r1, a3, b3)
	r1 = feAddMul19(r1, a4, b2)

	// r2 = a0×b2 + a1×b1 + a2×b0 + 19×(a3×b4 + a4×b3)
	r2 := feMul64(a0, b2)
	r2 = feAddMul(r2, a1, b1)
	r2 = feAddMul(r2, a2, b0)
	r2 = feAddMul19(r2, a3, b4)
	r2 = feAddMul19(r2, a4, b3)

	// r3 = a0×b3 + a1×b2 + a2×b1 + a3×b0 + 19×a4×b4
	r3 := feMul64(a0, b3)
	r3 = feAddMul(r3, a1, b2)
	r3 = feAddMul(r3, a2, b1)
	r3 = feAddMul(r3, a3, b0)
	r3 = feAddMul19(r3, a4, b4)

	// r4 = a0×b4 + a1×b3 + a2×b2 + a3×b1 + a4×b0
	r4 := feMul64(a0, b4)
	r4 = feAddMul(r4, a1, b3)
	r4 = feAddMul(r4, a2, b2)
	r4 = feAddMul(r4, a3, b1)
	r4 = feAddMul(r4, a4, b0)

	// After the multiplication, we need to reduce (carry) the five coefficients
	// to obtain a result with limbs that are at most slightly larger than 2⁵¹,
	// to respect the fe25519 invariant.
	//
	// Overall, the reduction works the same as carryPropagate, except with
	// wider inputs: we take the carry for each coefficient by shifting it right
	// by 51, and add it to the limb above it. The top carry is multiplied by 19
	// according to the reduction identity and added to the lowest limb.
	//
	// The largest coefficient (r0) will be at most 111 bits, which guarantees
	// that all carries are at most 111 - 51 = 60 bits, which fits in a uint64.
	//
	//     r0 = a0×b0 + 19×(a1×b4 + a2×b3 + a3×b2 + a4×b1)
	//     r0 < 2⁵²×2⁵² + 19×(2⁵²×2⁵² + 2⁵²×2⁵² + 2⁵²×2⁵² + 2⁵²×2⁵²)
	//     r0 < (1 + 19 × 4) × 2⁵² × 2⁵²
	//     r0 < 2⁷ × 2⁵² × 2⁵²
	//     r0 < 2¹¹¹
	//
	// Moreover, the top coefficient (r4) is at most 107 bits, so c4 is at most
	// 56 bits, and c4 * 19 is at most 61 bits, which again fits in a uint64 and
	// allows us to easily apply the reduction identity.
	//
	//     r4 = a0×b4 + a1×b3 + a2×b2 + a3×b1 + a4×b0
	//     r4 < 5 × 2⁵² × 2⁵²
	//     r4 < 2¹⁰⁷
	//

	c0 := feShr51(r0)
	c1 := feShr51(r1)
	c2 := feShr51(r2)
	c3 := feShr51(r3)
	c4 := feShr51(r4)

	rr0 := r0.lo&feMaskLow51 + feMul19(c4)
	rr1 := r1.lo&feMaskLow51 + c0
	rr2 := r2.lo&feMaskLow51 + c1
	rr3 := r3.lo&feMaskLow51 + c2
	rr4 := r4.lo&feMaskLow51 + c3

	// Now all coefficients fit into 64-bit registers but are still too large to
	// be passed around as an fe25519. We therefore do one last carry chain,
	// where the carries will be small enough to fit in the wiggle room above 2⁵¹.

	v.l0 = rr0&feMaskLow51 + feMul19(rr4>>51)
	v.l1 = rr1&feMaskLow51 + rr0>>51
	v.l2 = rr2&feMaskLow51 + rr1>>51
	v.l3 = rr3&feMaskLow51 + rr2>>51
	v.l4 = rr4&feMaskLow51 + rr3>>51
}

func feSquareGeneric(v, a *fe25519) {
	l0 := a.l0
	l1 := a.l1
	l2 := a.l2
	l3 := a.l3
	l4 := a.l4

	// Squaring works precisely like multiplication above, but thanks to its
	// symmetry we get to group a few terms together.
	//
	//                          l4   l3   l2   l1   l0  x
	//                          l4   l3   l2   l1   l0  =
	//                         ------------------------
	//                        l4l0 l3l0 l2l0 l1l0 l0l0  +
	//                   l4l1 l3l1 l2l1 l1l1 l0l1       +
	//              l4l2 l3l2 l2l2 l1l2 l0l2            +
	//         l4l3 l3l3 l2l3 l1l3 l0l3                 +
	//    l4l4 l3l4 l2l4 l1l4 l0l4                      =
	//   ----------------------------------------------
	//      r8   r7   r6   r5   r4   r3   r2   r1   r0
	//
	//            l4l0    l3l0    l2l0    l1l0    l0l0  +
	//            l3l1    l2l1    l1l1    l0l1 19×l4l1  +
	//            l2l2    l1l2    l0l2 19×l4l2 19×l3l2  +
	//            l1l3    l0l3 19×l4l3 19×l3l3 19×l2l3  +
	//            l0l4 19×l4l4 19×l3l4 19×l2l4 19×l1l4  =
	//           --------------------------------------
	//              r4      r3      r2      r1      r0

	// r0 = l0×l0 + 19×(l1×l4 + l2×l3 + l3×l2 + l4×l1) = l0×l0 + 19×2×(l1×l4 + l2×l3)
	r0 := feMul64(l0, l0)
	r0 = feAddMul38(r0, l1, l4)
	r0 = feAddMul38(r0, l2, l3)

	// r1 = l0×l1 + l1×l0 + 19×(l2×l4 + l3×l3 + l4×l2) = 2×l0×l1 + 19×2×l2×l4 + 19×l3×l3
	r1 := feMul64(l0*2, l1)
	r1 = feAddMul38(r1, l2, l4)
	r1 = feAddMul19(r1, l3, l3)

	// r2 = l0×l2 + l1×l1 + l2×l0 + 19×(l3×l4 + l4×l3) = 2×l0×l2 + l1×l1 + 19×2×l3×l4
	r2 := feMul64(l0*2, l2)
	r2 = feAddMul(r2, l1, l1)
	r2 = feAddMul38(r2, l3, l4)

	// r3 = l0×l3 + l1×l2 + l2×l1 + l3×l0 + 19×l4×l4 = 2×l0×l3 + 2×l1×l2 + 19×l4×l4
	r3 := feMul64(l0*2, l3)
	r3 = feAddMul(r3, l1*2, l2)
	r3 = feAddMul19(r3, l4, l4)

	// r4 = l0×l4 + l1×l3 + l2×l2 + l3×l1 + l4×l0 = 2×l0×l4 + 2×l1×l3 + l2×l2
	r4 := feMul64(l0*2, l4)
	r4 = feAddMul(r4, l1*2, l3)
	r4 = feAddMul(r4, l2, l2)

	c0 := feShr51(r0)
	c1 := feShr51(r1)
	c2 := feShr51(r2)
	c3 := feShr51(r3)
	c4 := feShr51(r4)

	rr0 := r0.lo&feMaskLow51 + feMul19(c4)
	rr1 := r1.lo&feMaskLow51 + c0
	rr2 := r2.lo&feMaskLow51 + c1
	rr3 := r3.lo&feMaskLow51 + c2
	rr4 := r4.lo&feMaskLow51 + c3

	v.l0 = rr0&feMaskLow51 + feMul19(rr4>>51)
	v.l1 = rr1&feMaskLow51 + rr0>>51
	v.l2 = rr2&feMaskLow51 + rr1>>51
	v.l3 = rr3&feMaskLow51 + rr2>>51
	v.l4 = rr4&feMaskLow51 + rr3>>51
}

// carryPropagate brings the limbs below 52 bits by applying the reduction
// identity (a * 2²⁵⁵ + b = a * 19 + b) to the l4 carry.
func (v *fe25519) carryPropagate() *fe25519 {
	// (l4>>51) is at most 64 - 51 = 13 bits, so (l4>>51)*19 is at most 18 bits, and
	// the final l0 will be at most 52 bits. Similarly for the rest.
	l0 := v.l0
	v.l0 = v.l0&feMaskLow51 + feMul19(v.l4>>51)
	v.l4 = v.l4&feMaskLow51 + v.l3>>51
	v.l3 = v.l3&feMaskLow51 + v.l2>>51
	v.l2 = v.l2&feMaskLow51 + v.l1>>51
	v.l1 = v.l1&feMaskLow51 + l0>>51

	return v
}

func feMul(v, x, y *fe25519) { feMulGeneric(v, x, y) }

func feSquare(v, x *fe25519) { feSquareGeneric(v, x) }
