// qr.go — Std.Qr: a pure QR Code encoder (ISO/IEC 18004), v0.26.2.
//
// The encoder is plain Go with no cgo, no reflection and no fmt, so it builds
// for the server, for the wasm client and under TinyGo. Text is encoded in
// byte mode as UTF-8; the smallest version (1..40) that holds the text at the
// chosen error-correction level is used; the mask is chosen by the four
// penalty rules of ISO/IEC 18004 §7.8.3.
//
// The construction follows the structure of Project Nayuki's QR Code
// generator (MIT); the block table is ISO/IEC 18004:2015 Table 9.
package rt

import "errors"

// QrCode is the Go value of a Sky `Std.Qr.QrCode`: a square module matrix,
// row-major, true = dark.
type QrCode struct {
	size int
	mods []bool
}

// Error-correction levels, in the order Std.Qr passes them (Low=0 … High=3).
const (
	qrLow = iota
	qrMedium
	qrQuartile
	qrHigh
)

// qrFormatBits is the two-bit level indicator of the format information
// (ISO/IEC 18004 Table 12): L=01, M=00, Q=11, H=10.
var qrFormatBits = [4]int{1, 0, 3, 2}

// qrBlocks[level][version] = {number of blocks, EC codewords per block}.
// ISO/IEC 18004:2015 Table 9.
var qrBlocks = [4][41][2]int{
	{{0, 0}, {1, 7}, {1, 10}, {1, 15}, {1, 20}, {1, 26}, {2, 18}, {2, 20}, {2, 24}, {2, 30}, {4, 18}, {4, 20}, {4, 24}, {4, 26}, {4, 30}, {6, 22}, {6, 24}, {6, 28}, {6, 30}, {7, 28}, {8, 28}, {8, 28}, {9, 28}, {9, 30}, {10, 30}, {12, 26}, {12, 28}, {12, 30}, {13, 30}, {14, 30}, {15, 30}, {16, 30}, {17, 30}, {18, 30}, {19, 30}, {19, 30}, {20, 30}, {21, 30}, {22, 30}, {24, 30}, {25, 30}},
	{{0, 0}, {1, 10}, {1, 16}, {1, 26}, {2, 18}, {2, 24}, {4, 16}, {4, 18}, {4, 22}, {5, 22}, {5, 26}, {5, 30}, {8, 22}, {9, 22}, {9, 24}, {10, 24}, {10, 28}, {11, 28}, {13, 26}, {14, 26}, {16, 26}, {17, 26}, {17, 28}, {18, 28}, {20, 28}, {21, 28}, {23, 28}, {25, 28}, {26, 28}, {28, 28}, {29, 28}, {31, 28}, {33, 28}, {35, 28}, {37, 28}, {38, 28}, {40, 28}, {43, 28}, {45, 28}, {47, 28}, {49, 28}},
	{{0, 0}, {1, 13}, {1, 22}, {2, 18}, {2, 26}, {4, 18}, {4, 24}, {6, 18}, {6, 22}, {8, 20}, {8, 24}, {8, 28}, {10, 26}, {12, 24}, {16, 20}, {12, 30}, {17, 24}, {16, 28}, {18, 28}, {21, 26}, {20, 30}, {23, 28}, {23, 30}, {25, 30}, {27, 30}, {29, 30}, {34, 28}, {34, 30}, {35, 30}, {38, 30}, {40, 30}, {43, 30}, {45, 30}, {48, 30}, {51, 30}, {53, 30}, {56, 30}, {59, 30}, {62, 30}, {65, 30}, {68, 30}},
	{{0, 0}, {1, 17}, {1, 28}, {2, 22}, {4, 16}, {4, 22}, {4, 28}, {5, 26}, {6, 26}, {8, 24}, {8, 28}, {11, 24}, {11, 28}, {16, 22}, {16, 24}, {18, 24}, {16, 30}, {19, 28}, {21, 28}, {25, 26}, {25, 28}, {25, 30}, {34, 24}, {30, 30}, {32, 30}, {35, 30}, {37, 30}, {40, 30}, {42, 30}, {45, 30}, {48, 30}, {51, 30}, {54, 30}, {57, 30}, {60, 30}, {63, 30}, {66, 30}, {70, 30}, {74, 30}, {77, 30}, {81, 30}},
}

// qrRawDataModules is the number of modules a version has for data and EC
// codewords (everything but the function patterns and format/version info).
func qrRawDataModules(ver int) int {
	n := (16*ver+128)*ver + 64
	if ver >= 2 {
		align := ver/7 + 2
		n -= (25*align-10)*align - 55
		if ver >= 7 {
			n -= 36
		}
	}
	return n
}

// qrDataCodewords is the number of 8-bit data codewords (EC excluded).
func qrDataCodewords(ver, lvl int) int {
	b := qrBlocks[lvl][ver]
	return qrRawDataModules(ver)/8 - b[0]*b[1]
}

// qrAlignmentPositions lists the centre coordinates of the alignment
// patterns (ISO/IEC 18004 Annex E).
func qrAlignmentPositions(ver int) []int {
	if ver == 1 {
		return nil
	}
	n := ver/7 + 2
	step := (ver*8 + n*3 + 5) / (n*4 - 4) * 2
	out := make([]int, n)
	out[0] = 6
	pos := ver*4 + 17 - 7
	for i := n - 1; i >= 1; i-- {
		out[i] = pos
		pos -= step
	}
	return out
}

// ─── Reed–Solomon over GF(2^8), polynomial 0x11D ───────────────────

func gfMul(x, y byte) byte {
	var z int
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= int((y>>uint(i))&1) * int(x)
	}
	return byte(z)
}

func rsDivisor(degree int) []byte {
	res := make([]byte, degree)
	res[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			res[j] = gfMul(res[j], root)
			if j+1 < degree {
				res[j] ^= res[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return res
}

func rsRemainder(data, divisor []byte) []byte {
	res := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ res[0]
		copy(res, res[1:])
		res[len(res)-1] = 0
		for i := range res {
			res[i] ^= gfMul(divisor[i], factor)
		}
	}
	return res
}

// ─── Bit buffer ────────────────────────────────────────────────────

type qrBits []bool

func (b *qrBits) put(val, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, (val>>uint(i))&1 != 0)
	}
}

// ─── Encoder ───────────────────────────────────────────────────────

var errQrTooLong = errors.New("the text is too long for a QR code at this error-correction level")

// qrEncodeBytes encodes data in byte mode. mask is 0..7 to force a mask
// pattern, or -1 to choose it by the penalty rules.
func qrEncodeBytes(data []byte, lvl int, mask int) (QrCode, error) {
	if lvl < qrLow || lvl > qrHigh {
		return QrCode{}, errors.New("unknown error-correction level")
	}
	ver := 0
	for v := 1; v <= 40; v++ {
		cc := 8
		if v >= 10 {
			cc = 16
		}
		if len(data) < 1<<uint(cc) && 4+cc+8*len(data) <= qrDataCodewords(v, lvl)*8 {
			ver = v
			break
		}
	}
	if ver == 0 {
		return QrCode{}, errQrTooLong
	}
	return qrEncodeVersion(data, ver, lvl, mask), nil
}

func qrEncodeVersion(data []byte, ver, lvl, mask int) QrCode {
	cc := 8
	if ver >= 10 {
		cc = 16
	}
	capBits := qrDataCodewords(ver, lvl) * 8
	var bits qrBits
	bits.put(0x4, 4) // byte mode
	bits.put(len(data), cc)
	for _, b := range data {
		bits.put(int(b), 8)
	}
	term := capBits - len(bits)
	if term > 4 {
		term = 4
	}
	bits.put(0, term)
	bits.put(0, (8-len(bits)%8)%8)
	for pad := 0xEC; len(bits) < capBits; pad ^= 0xEC ^ 0x11 {
		bits.put(pad, 8)
	}
	cw := make([]byte, len(bits)/8)
	for i, bit := range bits {
		if bit {
			cw[i>>3] |= 1 << uint(7-(i&7))
		}
	}
	all := qrInterleave(cw, ver, lvl)

	q := newQrGrid(ver)
	q.drawFunctionPatterns(ver)
	q.drawCodewords(all)
	if mask < 0 {
		best, bestPenalty := 0, -1
		for m := 0; m < 8; m++ {
			q.applyMask(m)
			q.drawFormatBits(lvl, m)
			if p := q.penalty(); bestPenalty < 0 || p < bestPenalty {
				best, bestPenalty = m, p
			}
			q.applyMask(m) // XOR again undoes it
		}
		mask = best
	}
	q.applyMask(mask)
	q.drawFormatBits(lvl, mask)
	return QrCode{size: q.size, mods: q.mods}
}

// qrInterleave splits the data codewords into blocks, appends each block's
// Reed–Solomon codewords and interleaves the result (ISO/IEC 18004 §7.6).
func qrInterleave(data []byte, ver, lvl int) []byte {
	nBlocks, ecLen := qrBlocks[lvl][ver][0], qrBlocks[lvl][ver][1]
	raw := qrRawDataModules(ver) / 8
	nShort := nBlocks - raw%nBlocks
	shortLen := raw / nBlocks
	div := rsDivisor(ecLen)
	blocks := make([][]byte, nBlocks)
	k := 0
	for i := 0; i < nBlocks; i++ {
		dl := shortLen - ecLen
		if i >= nShort {
			dl++
		}
		d := data[k : k+dl]
		k += dl
		blk := make([]byte, 0, shortLen+1)
		blk = append(blk, d...)
		if i < nShort {
			blk = append(blk, 0) // placeholder, skipped below
		}
		blk = append(blk, rsRemainder(d, div)...)
		blocks[i] = blk
	}
	out := make([]byte, 0, raw)
	for i := 0; i < len(blocks[0]); i++ {
		for j, blk := range blocks {
			if i != shortLen-ecLen || j >= nShort {
				out = append(out, blk[i])
			}
		}
	}
	return out
}

type qrGrid struct {
	size int
	mods []bool
	fn   []bool
}

func newQrGrid(ver int) *qrGrid {
	s := ver*4 + 17
	return &qrGrid{size: s, mods: make([]bool, s*s), fn: make([]bool, s*s)}
}

func (q *qrGrid) setFn(x, y int, dark bool) {
	q.mods[y*q.size+x] = dark
	q.fn[y*q.size+x] = true
}

func qrAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func qrMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (q *qrGrid) drawFunctionPatterns(ver int) {
	for i := 0; i < q.size; i++ {
		q.setFn(6, i, i%2 == 0)
		q.setFn(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {q.size - 4, 3}, {3, q.size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < q.size && y >= 0 && y < q.size {
					d := qrMax(qrAbs(dx), qrAbs(dy))
					q.setFn(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	pos := qrAlignmentPositions(ver)
	n := len(pos)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if (i == 0 && j == 0) || (i == 0 && j == n-1) || (i == n-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.setFn(pos[i]+dx, pos[j]+dy, qrMax(qrAbs(dx), qrAbs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormatBits(0, 0) // reserve the area; overwritten after masking
	if ver >= 7 {
		rem := ver
		for i := 0; i < 12; i++ {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := ver<<12 | rem
		for i := 0; i < 18; i++ {
			dark := (bits>>uint(i))&1 != 0
			a, b := q.size-11+i%3, i/3
			q.setFn(a, b, dark)
			q.setFn(b, a, dark)
		}
	}
}

func (q *qrGrid) drawFormatBits(lvl, mask int) {
	data := qrFormatBits[lvl]<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>uint(i))&1 != 0 }
	for i := 0; i <= 5; i++ {
		q.setFn(8, i, bit(i))
	}
	q.setFn(8, 7, bit(6))
	q.setFn(8, 8, bit(7))
	q.setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.setFn(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		q.setFn(q.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.setFn(8, q.size-15+i, bit(i))
	}
	q.setFn(8, q.size-8, true) // the dark module
}

func (q *qrGrid) drawCodewords(data []byte) {
	i := 0
	for right := q.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < q.size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = q.size - 1 - vert
				}
				if !q.fn[y*q.size+x] && i < len(data)*8 {
					q.mods[y*q.size+x] = (data[i>>3]>>uint(7-(i&7)))&1 != 0
					i++
				}
			}
		}
	}
}

func qrMaskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

func (q *qrGrid) applyMask(mask int) {
	for y := 0; y < q.size; y++ {
		for x := 0; x < q.size; x++ {
			if !q.fn[y*q.size+x] && qrMaskBit(mask, x, y) {
				q.mods[y*q.size+x] = !q.mods[y*q.size+x]
			}
		}
	}
}

// penalty scores a masked symbol by the four rules of ISO/IEC 18004 §7.8.3.
func (q *qrGrid) penalty() int {
	s := q.size
	at := func(x, y int) bool { return q.mods[y*s+x] }
	p := 0
	line := make([]bool, s)
	for pass := 0; pass < 2; pass++ {
		for a := 0; a < s; a++ {
			for b := 0; b < s; b++ {
				if pass == 0 {
					line[b] = at(b, a)
				} else {
					line[b] = at(a, b)
				}
			}
			// Rule 1: a run of five or more same-colour modules.
			run := 1
			for b := 1; b <= s; b++ {
				if b < s && line[b] == line[b-1] {
					run++
					continue
				}
				if run >= 5 {
					p += 3 + run - 5
				}
				run = 1
			}
			// Rule 3: 1:1:3:1:1 finder-like pattern with four light
			// modules on either side (outside the symbol counts as light).
			for b := 0; b+7 <= s; b++ {
				if !(line[b] && !line[b+1] && line[b+2] && line[b+3] && line[b+4] && !line[b+5] && line[b+6]) {
					continue
				}
				lightBefore, lightAfter := true, true
				for k := 1; k <= 4; k++ {
					if b-k >= 0 && line[b-k] {
						lightBefore = false
					}
					if b+6+k < s && line[b+6+k] {
						lightAfter = false
					}
				}
				if lightBefore {
					p += 40
				}
				if lightAfter {
					p += 40
				}
			}
		}
	}
	// Rule 2: 2×2 blocks of one colour.
	for y := 0; y+1 < s; y++ {
		for x := 0; x+1 < s; x++ {
			c := at(x, y)
			if c == at(x+1, y) && c == at(x, y+1) && c == at(x+1, y+1) {
				p += 3
			}
		}
	}
	// Rule 4: dark-module proportion away from 50%.
	dark := 0
	for _, m := range q.mods {
		if m {
			dark++
		}
	}
	total := s * s
	k := (qrAbs(dark*20-total*10)+total-1)/total - 1
	p += k * 10
	return p
}

// ─── Kernels ───────────────────────────────────────────────────────

func asQr(v any) QrCode {
	if c, ok := v.(QrCode); ok {
		return c
	}
	panic("rt: expected a Std.Qr QrCode")
}

// Qr.encodeWith : Int -> String -> Result Error QrCode — the level index
// (Low=0 … High=3, mapped from the Sky ADT in Std/Qr.sky) and the text.
func Qr_encodeWith(level any, text any) any {
	c, err := qrEncodeBytes([]byte(AsString(text)), AsInt(level), -1)
	if err != nil {
		return Err[any, any](ErrInvalidInput("Qr.encode: " + err.Error()))
	}
	return Ok[any, any](c)
}

// Qr.size : QrCode -> Int — modules per side (21 for version 1 … 177).
func Qr_size(code any) any { return asQr(code).size }

// Qr.isDark : Int -> Int -> QrCode -> Bool — (column, row); False outside
// the symbol, so a renderer can draw a quiet zone without bounds checks.
func Qr_isDark(x any, y any, code any) any {
	c := asQr(code)
	xi, yi := AsInt(x), AsInt(y)
	if xi < 0 || yi < 0 || xi >= c.size || yi >= c.size {
		return false
	}
	return c.mods[yi*c.size+xi]
}

// Qr.rows : QrCode -> List (List Bool) — the matrix, top row first.
func Qr_rows(code any) any {
	c := asQr(code)
	out := make([]any, c.size)
	for y := 0; y < c.size; y++ {
		row := make([]any, c.size)
		for x := 0; x < c.size; x++ {
			row[x] = c.mods[y*c.size+x]
		}
		out[y] = row
	}
	return out
}
