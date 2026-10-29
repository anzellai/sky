//go:build !js

package rt

// A pure-Go lossless WebP (VP8L) encoder, following RFC 9649 ("WebP Image
// Format"), section 3 onwards.
//
// The encoder tries a small set of transform pipelines and keeps the
// smallest bitstream:
//
//   - colour indexing (palette, with pixel bundling for 2/4/16 colours) when
//     the image has at most 256 distinct colours;
//   - subtract-green followed by the spatial predictor transform (all 14
//     modes, chosen per tile);
//   - no transform at all.
//
// Every entropy-coded image (the main image and every sub-image) is coded
// with LZ77 backward references found through a hash chain (using the 2D
// neighbourhood distance codes where they apply), an optional colour cache
// whose size is chosen by exact cost, and five canonical Huffman codes
// limited to 15 bits. A single prefix-code group is used for the whole image.

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"math/bits"
	"sort"
)

const (
	vp8lMaxDim       = 16384
	vp8lNumLiterals  = 256
	vp8lNumLengths   = 24
	vp8lNumDistances = 40
	vp8lMinMatch     = 3
	vp8lMaxMatch     = 4096
	// The largest distance value the 40 distance prefix codes can carry is
	// 2^20; plain distances are sent as distance+120.
	vp8lMaxDistance = 1<<20 - 120
	vp8lCacheMul    = 0x1e35a7bd
	vp8lMaxCache    = 10
	vp8lChainDepth  = 48
	vp8lMaxCodeLen  = 15
	vp8lMaxCLCLen   = 7

	// vp8lCodeLengthOrder is the order in which the code-length code
	// lengths are written (RFC 9649 section 3.7.2.1.2).
	vp8lCodeLengthOrder = "\x11\x12\x00\x01\x02\x03\x04\x05\x10\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f"

	// vp8lDistanceTable maps distance codes 1..120 to (dy, 8-dx) pairs
	// (RFC 9649 section 3.6.2).
	vp8lDistanceTable = "" +
		"\x18\x07\x17\x19\x28\x06\x27\x29\x16\x1a" +
		"\x26\x2a\x38\x05\x37\x39\x15\x1b\x36\x3a" +
		"\x25\x2b\x48\x04\x47\x49\x14\x1c\x35\x3b" +
		"\x46\x4a\x24\x2c\x58\x45\x4b\x34\x3c\x03" +
		"\x57\x59\x13\x1d\x56\x5a\x23\x2d\x44\x4c" +
		"\x55\x5b\x33\x3d\x68\x02\x67\x69\x12\x1e" +
		"\x66\x6a\x22\x2e\x54\x5c\x43\x4d\x65\x6b" +
		"\x32\x3e\x78\x01\x77\x79\x53\x5d\x11\x1f" +
		"\x64\x6c\x42\x4e\x76\x7a\x21\x2f\x75\x7b" +
		"\x31\x3f\x63\x6d\x52\x5e\x00\x74\x7c\x41" +
		"\x4f\x10\x20\x62\x6e\x30\x73\x7d\x51\x5f" +
		"\x40\x72\x7e\x61\x6f\x50\x71\x7f\x60\x70"
)

// webpLosslessEncode writes img as a lossless WebP (RIFF/WEBP/VP8L) file.
func webpLosslessEncode(w io.Writer, img image.Image) error {
	if img == nil {
		return errors.New("webp: nil image")
	}
	b := img.Bounds()
	width, height := b.Dx(), b.Dy()
	if width < 1 || height < 1 || width > vp8lMaxDim || height > vp8lMaxDim {
		return fmt.Errorf("webp: lossless image size %dx%d is outside 1..%d", width, height, vp8lMaxDim)
	}
	argb := vp8lToARGB(img)
	data := vp8lEncodeBitstream(argb, width, height)
	return vp8lWriteRIFF(w, data)
}

// vp8lToARGB converts img to non-premultiplied 0xAARRGGBB pixels in
// row-major order, starting at the top-left corner of its bounds.
func vp8lToARGB(img image.Image) []uint32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]uint32, w*h)
	if n, ok := img.(*image.NRGBA); ok {
		for y := 0; y < h; y++ {
			off := n.PixOffset(b.Min.X, b.Min.Y+y)
			row := n.Pix[off : off+4*w]
			for x := 0; x < w; x++ {
				p := row[4*x : 4*x+4 : 4*x+4]
				out[y*w+x] = uint32(p[3])<<24 | uint32(p[0])<<16 | uint32(p[1])<<8 | uint32(p[2])
			}
		}
		return out
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			out[y*w+x] = uint32(c.A)<<24 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
		}
	}
	return out
}

// vp8lWriteRIFF wraps a VP8L bitstream in the simple RIFF/WEBP container.
func vp8lWriteRIFF(w io.Writer, data []byte) error {
	size := len(data)
	pad := size & 1
	hdr := make([]byte, 20)
	copy(hdr[0:], "RIFF")
	vp8lPutLE32(hdr[4:], uint32(4+8+size+pad))
	copy(hdr[8:], "WEBPVP8L")
	vp8lPutLE32(hdr[16:], uint32(size))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if pad != 0 {
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

func vp8lPutLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// ---------------------------------------------------------------------------
// Bit writer (LSB first, as VP8L requires).

type vp8lBitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

func (bw *vp8lBitWriter) write(v uint32, n uint) {
	if n == 0 {
		return
	}
	bw.acc |= uint64(v&(1<<n-1)) << bw.n
	bw.n += n
	for bw.n >= 8 {
		bw.buf = append(bw.buf, byte(bw.acc))
		bw.acc >>= 8
		bw.n -= 8
	}
}

func (bw *vp8lBitWriter) bitLen() int { return len(bw.buf)*8 + int(bw.n) }

func (bw *vp8lBitWriter) bytes() []byte {
	out := bw.buf
	if bw.n > 0 {
		out = append(out, byte(bw.acc))
	}
	return out
}

// ---------------------------------------------------------------------------
// Top level: try the candidate pipelines and keep the smallest.

func vp8lEncodeBitstream(argb []uint32, w, h int) []byte {
	alpha := uint32(0)
	for _, p := range argb {
		if p>>24 != 0xff {
			alpha = 1
			break
		}
	}
	var best []byte
	try := func(body func(bw *vp8lBitWriter)) {
		bw := &vp8lBitWriter{}
		bw.write(0x2f, 8)
		bw.write(uint32(w-1), 14)
		bw.write(uint32(h-1), 14)
		bw.write(alpha, 1)
		bw.write(0, 3) // version
		body(bw)
		out := bw.bytes()
		if best == nil || len(out) < len(best) {
			best = out
		}
	}
	if pal, ok := vp8lPalette(argb); ok {
		try(func(bw *vp8lBitWriter) { vp8lWritePaletted(bw, argb, w, h, pal) })
	}
	try(func(bw *vp8lBitWriter) { vp8lWritePredicted(bw, argb, w, h, true) })
	try(func(bw *vp8lBitWriter) { vp8lWritePredicted(bw, argb, w, h, false) })
	try(func(bw *vp8lBitWriter) {
		bw.write(0, 1) // no transforms
		vp8lWriteEntropyImage(bw, argb, w, h, true)
	})
	return best
}

// ---------------------------------------------------------------------------
// Colour-indexing transform.

// vp8lPalette returns the sorted distinct colours of argb when there are at
// most 256 of them.
func vp8lPalette(argb []uint32) ([]uint32, bool) {
	seen := make(map[uint32]struct{}, 256)
	for _, p := range argb {
		if _, ok := seen[p]; !ok {
			if len(seen) == 256 {
				return nil, false
			}
			seen[p] = struct{}{}
		}
	}
	pal := make([]uint32, 0, len(seen))
	for p := range seen {
		pal = append(pal, p)
	}
	sort.Slice(pal, func(i, j int) bool { return pal[i] < pal[j] })
	return pal, true
}

func vp8lWritePaletted(bw *vp8lBitWriter, argb []uint32, w, h int, pal []uint32) {
	n := len(pal)
	bw.write(1, 1) // transform present
	bw.write(3, 2) // COLOR_INDEXING_TRANSFORM
	bw.write(uint32(n-1), 8)
	delta := make([]uint32, n)
	index := make(map[uint32]uint32, n)
	for i, p := range pal {
		index[p] = uint32(i)
		if i == 0 {
			delta[i] = p
		} else {
			delta[i] = vp8lSub(p, pal[i-1])
		}
	}
	vp8lWriteEntropyImage(bw, delta, n, 1, false)

	xbits := 0
	switch {
	case n <= 2:
		xbits = 3
	case n <= 4:
		xbits = 2
	case n <= 16:
		xbits = 1
	}
	pw := (w + 1<<xbits - 1) >> xbits
	bpp := uint(8 >> xbits)
	xmask := 1<<xbits - 1
	packed := make([]uint32, pw*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := index[argb[y*w+x]]
			packed[y*pw+x>>xbits] |= idx << (uint(x&xmask) * bpp)
		}
	}
	for i, v := range packed {
		packed[i] = 0xff000000 | v<<8
	}
	bw.write(0, 1) // no more transforms
	vp8lWriteEntropyImage(bw, packed, pw, h, true)
}

// ---------------------------------------------------------------------------
// Subtract-green and predictor transforms.

// vp8lWritePredicted codes argb with the predictor transform, preceded by
// the subtract-green transform when subGreen is set.
func vp8lWritePredicted(bw *vp8lBitWriter, argb []uint32, w, h int, subGreen bool) {
	px := argb
	if subGreen {
		px = make([]uint32, len(argb))
		for i, p := range argb {
			g := (p >> 8) & 0xff
			r := ((p >> 16) - g) & 0xff
			b := (p - g) & 0xff
			px[i] = p&0xff00ff00 | r<<16 | b
		}
		bw.write(1, 1) // transform present
		bw.write(2, 2) // SUBTRACT_GREEN_TRANSFORM
	}

	tbits := 4
	if w*h > 1<<20 {
		tbits = 5
	}
	modes, residual := vp8lPredict(px, w, h, tbits)
	bw.write(1, 1)
	bw.write(0, 2) // PREDICTOR_TRANSFORM
	bw.write(uint32(tbits-2), 3)
	tw, th := vp8lTiles(w, tbits), vp8lTiles(h, tbits)
	sub := make([]uint32, len(modes))
	for i, m := range modes {
		sub[i] = 0xff000000 | uint32(m)<<8
	}
	vp8lWriteEntropyImage(bw, sub, tw, th, false)

	bw.write(0, 1) // no more transforms
	vp8lWriteEntropyImage(bw, residual, w, h, true)
}

func vp8lTiles(size, bits int) int { return (size + 1<<bits - 1) >> bits }

// vp8lPredict picks a predictor mode per tile and returns the modes and the
// residual image.
func vp8lPredict(px []uint32, w, h, tbits int) ([]uint8, []uint32) {
	tw, th := vp8lTiles(w, tbits), vp8lTiles(h, tbits)
	modes := make([]uint8, tw*th)
	size := 1 << tbits
	var accum, bestHist [4][256]uint32
	accN, bestN := 0, 0
	for ty := 0; ty < th; ty++ {
		y0, y1 := ty*size, min((ty+1)*size, h)
		if y0 == 0 {
			y0 = 1 // row 0 has a fixed predictor
		}
		for tx := 0; tx < tw; tx++ {
			x0, x1 := tx*size, min((tx+1)*size, w)
			if x0 == 0 {
				x0 = 1 // column 0 has a fixed predictor
			}
			best, bestCost := 0, math.Inf(1)
			for m := 0; m < 14; m++ {
				var th [4][256]uint32
				n := 0
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						i := y*w + x
						r := vp8lSub(px[i], vp8lPredictor(m, px[i-1], px[i-w], px[i-w+1], px[i-w-1]))
						th[0][r>>24]++
						th[1][r>>16&0xff]++
						th[2][r>>8&0xff]++
						th[3][r&0xff]++
						n++
					}
				}
				// Estimated bits of the tile under the image-wide residual
				// statistics gathered so far, including this tile.
				lt := math.Log2(float64(accN + n + 1))
				cost := 0.0
				for c := range th {
					for v, k := range th[c] {
						if k != 0 {
							cost += float64(k) * (lt - math.Log2(float64(accum[c][v]+k)))
						}
					}
				}
				if cost < bestCost {
					best, bestCost, bestHist, bestN = m, cost, th, n
				}
			}
			for c := range accum {
				for v, k := range bestHist[c] {
					accum[c][v] += k
				}
			}
			accN += bestN
			modes[ty*tw+tx] = uint8(best)
		}
	}

	res := make([]uint32, len(px))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			var pred uint32
			switch {
			case x == 0 && y == 0:
				pred = 0xff000000
			case y == 0:
				pred = px[i-1]
			case x == 0:
				pred = px[i-w]
			default:
				m := int(modes[(y>>tbits)*tw+(x>>tbits)])
				pred = vp8lPredictor(m, px[i-1], px[i-w], px[i-w+1], px[i-w-1])
			}
			res[i] = vp8lSub(px[i], pred)
		}
	}
	return modes, res
}

// vp8lResidualCost estimates the coding cost of a residual pixel as the sum
// of the magnitudes of its signed channel values.
func vp8lResidualCost(r uint32) int {
	c := 0
	for s := uint(0); s < 32; s += 8 {
		v := int(int8(r >> s))
		if v < 0 {
			v = -v
		}
		c += v
	}
	return c
}

// vp8lPredictor returns the prediction for one of the 14 modes, given the
// left, top, top-right and top-left pixels (RFC 9649 section 4.1).
func vp8lPredictor(mode int, l, t, tr, tl uint32) uint32 {
	switch mode {
	case 0:
		return 0xff000000
	case 1:
		return l
	case 2:
		return t
	case 3:
		return tr
	case 4:
		return tl
	case 5:
		return vp8lAvg2(vp8lAvg2(l, tr), t)
	case 6:
		return vp8lAvg2(l, tl)
	case 7:
		return vp8lAvg2(l, t)
	case 8:
		return vp8lAvg2(tl, t)
	case 9:
		return vp8lAvg2(t, tr)
	case 10:
		return vp8lAvg2(vp8lAvg2(l, tl), vp8lAvg2(t, tr))
	case 11:
		return vp8lSelect(l, t, tl)
	case 12:
		return vp8lClampFull(l, t, tl)
	default:
		return vp8lClampHalf(vp8lAvg2(l, t), tl)
	}
}

// vp8lAvg2 is the per-channel floor average.
func vp8lAvg2(a, b uint32) uint32 { return (((a ^ b) & 0xfefefefe) >> 1) + (a & b) }

// vp8lSub is the per-channel difference modulo 256.
func vp8lSub(a, b uint32) uint32 {
	var r uint32
	for s := uint(0); s < 32; s += 8 {
		r |= ((a>>s - b>>s) & 0xff) << s
	}
	return r
}

func vp8lAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func vp8lClamp(x int) uint32 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return uint32(x)
}

func vp8lSelect(l, t, tl uint32) uint32 {
	pl, pt := 0, 0
	for s := uint(0); s < 32; s += 8 {
		c := int(tl >> s & 0xff)
		pl += vp8lAbs(c - int(t>>s&0xff))
		pt += vp8lAbs(c - int(l>>s&0xff))
	}
	if pl < pt {
		return l
	}
	return t
}

func vp8lClampFull(a, b, c uint32) uint32 {
	var r uint32
	for s := uint(0); s < 32; s += 8 {
		r |= vp8lClamp(int(a>>s&0xff)+int(b>>s&0xff)-int(c>>s&0xff)) << s
	}
	return r
}

func vp8lClampHalf(a, b uint32) uint32 {
	var r uint32
	for s := uint(0); s < 32; s += 8 {
		x, y := int(a>>s&0xff), int(b>>s&0xff)
		r |= vp8lClamp(x+(x-y)/2) << s
	}
	return r
}

// ---------------------------------------------------------------------------
// LZ77 backward references.

// vp8lToken is one LZ77 element: a literal pixel (length 0) or a copy of
// length pixels from dist pixels back.
type vp8lToken struct {
	length uint32
	dist   uint32
}

func vp8lBackwardRefs(argb []uint32, w int) []vp8lToken {
	n := len(argb)
	hbits := uint(bits.Len(uint(n)) + 1)
	if hbits < 8 {
		hbits = 8
	}
	if hbits > 18 {
		hbits = 18
	}
	head := make([]int32, 1<<hbits)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, n)
	hash := func(i int) uint32 {
		x := argb[i]*0x9e3779b1 ^ (argb[i+1]*0x85ebca6b + 0x27d4eb2f)
		return (x * vp8lCacheMul) >> (32 - hbits)
	}
	ins := 0
	insertTo := func(j int) {
		for ; ins < j; ins++ {
			if ins+1 < n {
				hv := hash(ins)
				prev[ins] = head[hv]
				head[hv] = int32(ins)
			}
		}
	}
	find := func(i int) (int, int) {
		maxLen := min(n-i, vp8lMaxMatch)
		if maxLen < vp8lMinMatch {
			return 0, 0
		}
		bestL, bestD := 0, 0
		try := func(c int) {
			d := i - c
			if c < 0 || d < 1 || d > vp8lMaxDistance || bestL >= maxLen || argb[c+bestL] != argb[i+bestL] {
				return
			}
			l := 0
			for l < maxLen && argb[c+l] == argb[i+l] {
				l++
			}
			if l > bestL {
				bestL, bestD = l, d
			}
		}
		try(i - 1)
		if w > 1 {
			try(i - w)
		}
		c := head[hash(i)]
		for steps := 0; c >= 0 && steps < vp8lChainDepth && bestL < maxLen; steps++ {
			if i-int(c) > vp8lMaxDistance {
				break
			}
			try(int(c))
			c = prev[c]
		}
		return bestL, bestD
	}

	toks := make([]vp8lToken, 0, n/2+1)
	for i := 0; i < n; {
		insertTo(i)
		l, d := find(i)
		if l >= vp8lMinMatch && i+1 < n {
			insertTo(i + 1)
			if l2, _ := find(i + 1); l2 > l {
				toks = append(toks, vp8lToken{})
				i++
				continue
			}
		}
		if l >= vp8lMinMatch {
			toks = append(toks, vp8lToken{length: uint32(l), dist: uint32(d)})
			i += l
		} else {
			toks = append(toks, vp8lToken{})
			i++
		}
	}
	return toks
}

// vp8lDistanceCodes maps pixel distances to the smallest 2D neighbourhood
// distance code (1..120) that denotes them for an image of width w.
func vp8lDistanceCodes(w int) map[uint32]uint32 {
	m := make(map[uint32]uint32, 120)
	for i := 0; i < 120; i++ {
		c := int(vp8lDistanceTable[i])
		d := (c>>4)*w + 8 - c&0xf
		if d >= 1 {
			if _, ok := m[uint32(d)]; !ok {
				m[uint32(d)] = uint32(i + 1)
			}
		}
	}
	return m
}

// vp8lPrefix splits a length or distance value (>= 1) into its prefix code,
// extra-bit count and extra-bit value (RFC 9649 section 3.6.1).
func vp8lPrefix(v uint32) (uint32, uint, uint32) {
	if v <= 4 {
		return v - 1, 0, 0
	}
	d := v - 1
	hi := uint(bits.Len32(d) - 1)
	second := (d >> (hi - 1)) & 1
	nb := hi - 1
	return uint32(2*hi) + second, nb, d & (1<<nb - 1)
}

// ---------------------------------------------------------------------------
// Entropy-coded image.

// vp8lWalk turns tokens into prefix-coded symbols, either counting them into
// hist (when bw is nil, returning the number of extra bits) or writing them
// with codes.
func vp8lWalk(toks []vp8lToken, argb []uint32, cacheBits int, dm map[uint32]uint32,
	hist *[5][]uint32, bw *vp8lBitWriter, codes *[5]vp8lHuffCode) int {
	extraBits := 0
	emit := func(tree int, sym uint32) {
		if bw == nil {
			hist[tree][sym]++
		} else {
			codes[tree].put(bw, sym)
		}
	}
	extra := func(nb uint, v uint32) {
		if bw == nil {
			extraBits += int(nb)
		} else {
			bw.write(v, nb)
		}
	}
	var cache []uint32
	shift := uint(32 - cacheBits)
	if cacheBits > 0 {
		cache = make([]uint32, 1<<cacheBits)
	}
	pos := 0
	for _, t := range toks {
		if t.length == 0 {
			p := argb[pos]
			pos++
			if cache != nil {
				k := (p * vp8lCacheMul) >> shift
				if cache[k] == p {
					emit(0, vp8lNumLiterals+vp8lNumLengths+k)
					continue
				}
				cache[k] = p
			}
			emit(0, p>>8&0xff)
			emit(1, p>>16&0xff)
			emit(2, p&0xff)
			emit(3, p>>24)
			continue
		}
		code, nb, ev := vp8lPrefix(t.length)
		emit(0, vp8lNumLiterals+code)
		extra(nb, ev)
		dcode, ok := dm[t.dist]
		if !ok {
			dcode = t.dist + 120
		}
		code, nb, ev = vp8lPrefix(dcode)
		emit(4, code)
		extra(nb, ev)
		if cache != nil {
			for _, p := range argb[pos : pos+int(t.length)] {
				cache[(p*vp8lCacheMul)>>shift] = p
			}
		}
		pos += int(t.length)
	}
	return extraBits
}

// vp8lPlan is a fully decided coding of one entropy-coded image.
type vp8lPlan struct {
	cacheBits int
	codes     [5]vp8lHuffCode
	bits      int
}

func vp8lMakePlan(toks []vp8lToken, argb []uint32, cacheBits int, dm map[uint32]uint32) *vp8lPlan {
	var hist [5][]uint32
	sizes := [5]int{vp8lNumLiterals + vp8lNumLengths, vp8lNumLiterals, vp8lNumLiterals, vp8lNumLiterals, vp8lNumDistances}
	if cacheBits > 0 {
		sizes[0] += 1 << cacheBits
	}
	for i := range hist {
		hist[i] = make([]uint32, sizes[i])
	}
	extra := vp8lWalk(toks, argb, cacheBits, dm, &hist, nil, nil)
	p := &vp8lPlan{cacheBits: cacheBits}
	hdr := &vp8lBitWriter{}
	total := extra
	for i := range hist {
		p.codes[i] = vp8lBuildCode(hist[i])
		p.codes[i].writeHeader(hdr)
		if !p.codes[i].single {
			for s, f := range hist[i] {
				total += int(f) * int(p.codes[i].depth[s])
			}
		}
	}
	p.bits = total + hdr.bitLen()
	return p
}

// vp8lWriteEntropyImage codes argb (w x h). The main image (top) carries the
// meta prefix-code bit; sub-images do not.
func vp8lWriteEntropyImage(bw *vp8lBitWriter, argb []uint32, w, h int, top bool) {
	toks := vp8lBackwardRefs(argb, w)
	dm := vp8lDistanceCodes(w)
	var best *vp8lPlan
	for cb := 0; cb <= vp8lMaxCache; cb++ {
		p := vp8lMakePlan(toks, argb, cb, dm)
		if best == nil || p.bits < best.bits {
			best = p
		}
		if cb > 0 && p.bits > best.bits+best.bits/8 {
			break // a larger cache is unlikely to help further
		}
	}
	if best.cacheBits > 0 {
		bw.write(1, 1)
		bw.write(uint32(best.cacheBits), 4)
	} else {
		bw.write(0, 1)
	}
	if top {
		bw.write(0, 1) // one prefix-code group, no meta prefix image
	}
	for i := range best.codes {
		best.codes[i].writeHeader(bw)
	}
	vp8lWalk(toks, argb, best.cacheBits, dm, nil, bw, &best.codes)
}

// ---------------------------------------------------------------------------
// Canonical Huffman codes.

type vp8lHuffCode struct {
	depth  []uint8  // code lengths as written in the header
	code   []uint16 // bit-reversed canonical codes
	used   []int    // symbols with a non-zero length, ascending
	single bool     // exactly one symbol: it is coded with zero bits
}

func vp8lBuildCode(freq []uint32) vp8lHuffCode {
	return vp8lCodeFromDepth(vp8lHuffLengths(freq, vp8lMaxCodeLen))
}

func vp8lCodeFromDepth(depth []uint8) vp8lHuffCode {
	c := vp8lHuffCode{depth: depth, code: make([]uint16, len(depth))}
	var count [vp8lMaxCodeLen + 1]int
	for s, d := range depth {
		if d > 0 {
			count[d]++
			c.used = append(c.used, s)
		}
	}
	c.single = len(c.used) == 1
	var next [vp8lMaxCodeLen + 1]int
	code := 0
	for l := 1; l <= vp8lMaxCodeLen; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	for _, s := range c.used {
		d := depth[s]
		v := next[d]
		next[d]++
		c.code[s] = uint16(bits.Reverse16(uint16(v)) >> (16 - d))
	}
	return c
}

func (c *vp8lHuffCode) put(bw *vp8lBitWriter, sym uint32) {
	if c.single {
		return
	}
	bw.write(uint32(c.code[sym]), uint(c.depth[sym]))
}

// writeHeader writes the code: the simple form for 0, 1 or 2 symbols below
// 256, otherwise the normal form through the code-length code.
func (c *vp8lHuffCode) writeHeader(bw *vp8lBitWriter) {
	u := c.used
	if len(u) == 0 {
		// An unused tree still needs a valid code: one symbol, 0.
		bw.write(1, 1)
		bw.write(0, 1)
		bw.write(0, 1)
		bw.write(0, 1)
		return
	}
	if len(u) <= 2 && u[len(u)-1] < 256 {
		bw.write(1, 1)
		bw.write(uint32(len(u)-1), 1)
		if u[0] < 2 {
			bw.write(0, 1)
			bw.write(uint32(u[0]), 1)
		} else {
			bw.write(1, 1)
			bw.write(uint32(u[0]), 8)
		}
		if len(u) == 2 {
			bw.write(uint32(u[1]), 8)
		}
		return
	}
	bw.write(0, 1)
	syms, extras := vp8lRLE(c.depth)
	hist := make([]uint32, 19)
	for _, s := range syms {
		hist[s]++
	}
	clc := vp8lCodeFromDepth(vp8lHuffLengths(hist, vp8lMaxCLCLen))
	num := 4
	for i := 0; i < 19; i++ {
		if clc.depth[vp8lCodeLengthOrder[i]] != 0 && i+1 > num {
			num = i + 1
		}
	}
	bw.write(uint32(num-4), 4)
	for i := 0; i < num; i++ {
		bw.write(uint32(clc.depth[vp8lCodeLengthOrder[i]]), 3)
	}
	bw.write(0, 1) // max_symbol: code all symbols
	for i, s := range syms {
		clc.put(bw, uint32(s))
		switch s {
		case 16:
			bw.write(uint32(extras[i]), 2)
		case 17:
			bw.write(uint32(extras[i]), 3)
		case 18:
			bw.write(uint32(extras[i]), 7)
		}
	}
}

// vp8lRLE codes a code-length sequence with the literal lengths 0..15 and
// the repeat codes 16 (previous non-zero, 3..6), 17 (zeros, 3..10) and 18
// (zeros, 11..138).
func vp8lRLE(depth []uint8) (syms, extras []uint8) {
	emit := func(s, e uint8) {
		syms = append(syms, s)
		extras = append(extras, e)
	}
	prev := uint8(8) // the decoder's initial "previous non-zero length"
	for i := 0; i < len(depth); {
		v := depth[i]
		j := i
		for j < len(depth) && depth[j] == v {
			j++
		}
		run := j - i
		i = j
		if v == 0 {
			for run >= 11 {
				k := min(run, 138)
				emit(18, uint8(k-11))
				run -= k
			}
			if run >= 3 {
				emit(17, uint8(run-3))
				run = 0
			}
			for ; run > 0; run-- {
				emit(0, 0)
			}
			continue
		}
		if prev != v {
			emit(v, 0)
			prev = v
			run--
		}
		for run >= 3 {
			k := min(run, 6)
			emit(16, uint8(k-3))
			run -= k
		}
		for ; run > 0; run-- {
			emit(v, 0)
		}
	}
	return syms, extras
}

// vp8lHuffLengths builds Huffman code lengths for freq, no longer than
// limit. Lengths are made shorter by raising the smallest counts until the
// tree fits. A single used symbol gets length 1.
func vp8lHuffLengths(freq []uint32, limit int) []uint8 {
	depth := make([]uint8, len(freq))
	type leaf struct {
		w   uint64
		sym int
	}
	var leaves []leaf
	for s, f := range freq {
		if f > 0 {
			leaves = append(leaves, leaf{uint64(f), s})
		}
	}
	m := len(leaves)
	if m == 0 {
		return depth
	}
	if m == 1 {
		depth[leaves[0].sym] = 1
		return depth
	}
	weight := make([]uint64, 2*m-1)
	parent := make([]int, 2*m-1)
	d := make([]int, 2*m-1)
	for floor := uint64(1); ; floor *= 2 {
		ls := make([]leaf, m)
		for i, l := range leaves {
			ls[i] = leaf{max(l.w, floor), l.sym}
		}
		sort.Slice(ls, func(i, j int) bool {
			if ls[i].w != ls[j].w {
				return ls[i].w < ls[j].w
			}
			return ls[i].sym < ls[j].sym
		})
		for i := range ls {
			weight[i] = ls[i].w
		}
		li, ii, next := 0, m, m
		pick := func() int {
			if li < m && (ii >= next || weight[li] <= weight[ii]) {
				li++
				return li - 1
			}
			ii++
			return ii - 1
		}
		for next < 2*m-1 {
			a := pick()
			b := pick()
			weight[next] = weight[a] + weight[b]
			parent[a], parent[b] = next, next
			next++
		}
		d[2*m-2] = 0
		maxD := 0
		for k := 2*m - 3; k >= 0; k-- {
			d[k] = d[parent[k]] + 1
			if k < m && d[k] > maxD {
				maxD = d[k]
			}
		}
		if maxD <= limit {
			for i := 0; i < m; i++ {
				depth[ls[i].sym] = uint8(d[i])
			}
			return depth
		}
	}
}
