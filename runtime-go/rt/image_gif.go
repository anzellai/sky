// Package rt — Std.Image animated GIF: scan, compose, process, write.
//
// image/gif decodes a whole animation into memory (gif.DecodeAll) and writes
// one from memory (gif.EncodeAll). Both hold every frame at once, and the
// decoder allocates before it can count frames. This file processes a GIF
// one frame at a time instead:
//
//  1. gifScan walks the block structure (no LZW decode): the logical screen,
//     the NETSCAPE loop count, and each frame's byte range, delay and
//     disposal. It refuses too many frames, or too many pixels in all,
//     before any frame is decoded (image_limits.go).
//  2. Each frame is decoded on its own, by handing image/gif a GIF that
//     holds the header and that one frame (gifScan.decodeFrame).
//  3. gifComposer draws it onto the logical screen and applies the frame's
//     disposal afterwards (none, background = clear to transparent,
//     previous = restore what was under the frame). The operation (resize,
//     crop, trim, colour to alpha) runs on the COMPOSED frame, so partial
//     frames and offsets come out right.
//  4. Each result is mapped to a palette of its own (gifQuantise: exact when
//     it has 256 colours or fewer, else median cut with Floyd-Steinberg
//     dithering) and appended by gifWriter, which writes the frame at once.
//
// The output keeps each frame's delay and disposal and the loop count.
// Comment, plain-text and application extensions other than the NETSCAPE
// loop are not written. Memory holds the screen, the saved area for
// "restore previous", one composed frame and one output frame.
//
//go:build !js

package rt

import (
	"bytes"
	"compress/lzw"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"strconv"
)

// GIF disposal methods (GIF89a section 23).
const (
	gifDisposalNone       = 1
	gifDisposalBackground = 2
	gifDisposalPrevious   = 3
)

type gifFrameInfo struct {
	start, end int    // the image descriptor through its last data sub-block
	gce        []byte // the raw graphic control extension, or nil
	delay      int    // in 1/100 s
	disposal   byte
	rect       image.Rectangle
}

type gifScanResult struct {
	width, height int
	headerEnd     int // header, logical screen descriptor and global table
	loopCount     int // -1: no NETSCAPE extension
	frames        []gifFrameInfo
}

// gifScanLimits: maxFrames <= 0 or maxFramePixels <= 0 disables that check.
type gifScanLimits struct {
	maxFrames      int
	maxFramePixels int64
	stopAfter      int // > 0: stop once this many frames are found
}

var gifDefaultLimits = gifScanLimits{maxFrames: imageMaxFrames, maxFramePixels: imageMaxFramePixels}

var errGifTruncated = errors.New("truncated GIF")

// gifSkipSubBlocks returns the offset after a run of data sub-blocks and its
// zero-length terminator.
func gifSkipSubBlocks(data []byte, p int) (int, error) {
	for {
		if p >= len(data) {
			return 0, errGifTruncated
		}
		n := int(data[p])
		p++
		if n == 0 {
			return p, nil
		}
		p += n
	}
}

// gifScan walks a GIF's blocks. It decodes no image data.
func gifScan(op string, data []byte, lim gifScanLimits) (*gifScanResult, any) {
	bad := func(msg string) (*gifScanResult, any) {
		return nil, Err[any, any](ErrInvalidInput(op + ": not a decodable GIF: " + msg))
	}
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return bad("no GIF header")
	}
	s := &gifScanResult{
		width:     int(data[6]) | int(data[7])<<8,
		height:    int(data[8]) | int(data[9])<<8,
		loopCount: -1,
	}
	if s.width == 0 || s.height == 0 {
		return bad("zero logical screen size")
	}
	if e := imageCheckPixels(op, s.width, s.height); e != nil {
		return nil, e
	}
	p := 13
	if data[10]&0x80 != 0 {
		p += 3 * (1 << (int(data[10]&7) + 1))
	}
	if p > len(data) {
		return bad("truncated colour table")
	}
	s.headerEnd = p
	var gce []byte
	var err error
	screenPx := int64(s.width) * int64(s.height)
	for {
		if p >= len(data) {
			// A missing trailer is common; what was read stands.
			break
		}
		switch data[p] {
		case 0x3B:
			if len(s.frames) == 0 {
				return bad("no image")
			}
			return s, nil
		case 0x21:
			if p+1 >= len(data) {
				return bad("truncated extension")
			}
			label := data[p+1]
			switch label {
			case 0xF9:
				if p+8 > len(data) || data[p+2] != 4 {
					return bad("bad graphic control extension")
				}
				gce = data[p : p+8]
				p += 7
				if p >= len(data) || data[p] != 0 {
					return bad("bad graphic control extension")
				}
				p++
			case 0xFF:
				q := p + 2
				if q < len(data) && data[q] == 11 && q+12 <= len(data) &&
					(string(data[q+1:q+12]) == "NETSCAPE2.0" || string(data[q+1:q+12]) == "ANIMEXTS1.0") {
					r := q + 12
					if r+4 <= len(data) && data[r] == 3 && data[r+1] == 1 {
						s.loopCount = int(data[r+2]) | int(data[r+3])<<8
					}
				}
				if p, err = gifSkipSubBlocks(data, p+2); err != nil {
					return bad(err.Error())
				}
			default:
				if p, err = gifSkipSubBlocks(data, p+2); err != nil {
					return bad(err.Error())
				}
			}
		case 0x2C:
			if p+10 > len(data) {
				return bad("truncated image descriptor")
			}
			f := gifFrameInfo{start: p, gce: gce}
			left := int(data[p+1]) | int(data[p+2])<<8
			top := int(data[p+3]) | int(data[p+4])<<8
			fw := int(data[p+5]) | int(data[p+6])<<8
			fh := int(data[p+7]) | int(data[p+8])<<8
			f.rect = image.Rect(left, top, left+fw, top+fh)
			packed := data[p+9]
			p += 10
			if packed&0x80 != 0 {
				p += 3 * (1 << (int(packed&7) + 1))
			}
			p++ // LZW minimum code size
			if p > len(data) {
				return bad("truncated image")
			}
			if p, err = gifSkipSubBlocks(data, p); err != nil {
				return bad(err.Error())
			}
			f.end = p
			if gce != nil {
				f.disposal = (gce[3] >> 2) & 7
				f.delay = int(gce[4]) | int(gce[5])<<8
			}
			gce = nil
			s.frames = append(s.frames, f)
			n := len(s.frames)
			if lim.maxFrames > 0 && n > lim.maxFrames {
				return nil, imageLimitErr(op, imageCodeTooManyFrames, "frames (or more)",
					int64(lim.maxFrames), int64(n))
			}
			if lim.maxFramePixels > 0 && int64(n)*screenPx > lim.maxFramePixels {
				return nil, imageLimitErr(op, imageCodeTooManyFramePixels,
					"pixels in its frames (or more), "+strconv.Itoa(s.width)+"x"+strconv.Itoa(s.height)+
						" x "+strconv.Itoa(n)+" frames,",
					lim.maxFramePixels, int64(n)*screenPx)
			}
			if lim.stopAfter > 0 && n >= lim.stopAfter {
				return s, nil
			}
		default:
			return bad("unknown block 0x" + strconv.FormatInt(int64(data[p]), 16))
		}
	}
	if len(s.frames) == 0 {
		return bad("no image")
	}
	return s, nil
}

// decodeFrame decodes frame i alone: a GIF of the header, the frame's
// graphic control extension and the frame.
func (s *gifScanResult) decodeFrame(data []byte, i int) (*image.Paletted, error) {
	f := s.frames[i]
	var b bytes.Buffer
	b.Grow(s.headerEnd + len(f.gce) + f.end - f.start + 1)
	b.WriteString("GIF89a")
	b.Write(data[6:s.headerEnd])
	b.Write(f.gce)
	b.Write(data[f.start:f.end])
	b.WriteByte(0x3B)
	img, err := gif.Decode(&b)
	if err != nil {
		return nil, err
	}
	p, ok := img.(*image.Paletted)
	if !ok {
		return nil, errors.New("frame is not paletted")
	}
	return p, nil
}

// gifComposer keeps the logical screen between frames.
type gifComposer struct {
	screen *image.NRGBA
	saved  *image.NRGBA // the area under a "restore previous" frame
	rect   image.Rectangle
}

func newGifComposer(w, h int) *gifComposer {
	return &gifComposer{screen: image.NewNRGBA(image.Rect(0, 0, w, h))}
}

// compose draws a frame (its opaque pixels) and returns a copy of the
// screen; the frame's disposal is applied to the screen afterwards.
func (c *gifComposer) compose(frame *image.Paletted, disposal byte) *image.NRGBA {
	r := frame.Rect.Intersect(c.screen.Rect)
	if disposal == gifDisposalPrevious {
		c.saved = image.NewNRGBA(r)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(c.saved.Pix[c.saved.PixOffset(r.Min.X, y):c.saved.PixOffset(r.Max.X, y)],
				c.screen.Pix[c.screen.PixOffset(r.Min.X, y):c.screen.PixOffset(r.Max.X, y)])
		}
	}
	var pal [256]color.NRGBA
	for i, col := range frame.Palette {
		if i < 256 {
			pal[i] = color.NRGBAModel.Convert(col).(color.NRGBA)
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := frame.Pix[frame.PixOffset(r.Min.X, y):]
		dst := c.screen.Pix[c.screen.PixOffset(r.Min.X, y):]
		for x := 0; x < r.Dx(); x++ {
			col := pal[src[x]]
			if col.A == 0 {
				continue
			}
			d := dst[4*x : 4*x+4 : 4*x+4]
			d[0], d[1], d[2], d[3] = col.R, col.G, col.B, col.A
		}
	}
	out := image.NewNRGBA(c.screen.Rect)
	copy(out.Pix, c.screen.Pix)
	switch disposal {
	case gifDisposalBackground:
		for y := r.Min.Y; y < r.Max.Y; y++ {
			clear(c.screen.Pix[c.screen.PixOffset(r.Min.X, y):c.screen.PixOffset(r.Max.X, y)])
		}
	case gifDisposalPrevious:
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(c.screen.Pix[c.screen.PixOffset(r.Min.X, y):c.screen.PixOffset(r.Max.X, y)],
				c.saved.Pix[c.saved.PixOffset(r.Min.X, y):c.saved.PixOffset(r.Max.X, y)])
		}
		c.saved = nil
	}
	return out
}

// gifEachFrame decodes and composes every frame in order and calls fn with
// frame i composed. It stops at the first error fn returns.
func gifEachFrame(op string, data []byte, s *gifScanResult, fn func(i int, composed *image.NRGBA) any) any {
	c := newGifComposer(s.width, s.height)
	for i := range s.frames {
		// The scan counted the frames; this is the count while decoding.
		if i >= imageMaxFrames {
			return imageLimitErr(op, imageCodeTooManyFrames, "frames (or more)", imageMaxFrames, int64(i+1))
		}
		frame, err := s.decodeFrame(data, i)
		if err != nil {
			return Err[any, any](ErrInvalidInput(op + ": not a decodable GIF: frame " +
				strconv.Itoa(i+1) + ": " + err.Error()))
		}
		if bad := fn(i, c.compose(frame, s.frames[i].disposal)); bad != nil {
			return bad
		}
	}
	return nil
}

// gifFirstFrame is the first frame composed on the logical screen: what a
// still output (JPEG, PNG, WebP) of a GIF shows.
func gifFirstFrame(op string, data []byte) (image.Image, any) {
	s, bad := gifScan(op, data, gifScanLimits{stopAfter: 1})
	if bad != nil {
		return nil, bad
	}
	var first image.Image
	bad = gifEachFrame(op, data, s, func(_ int, composed *image.NRGBA) any {
		first = composed
		return nil
	})
	if bad != nil {
		return nil, bad
	}
	return first, nil
}

// gifWriter writes a GIF one frame at a time.
type gifWriter struct {
	buf    bytes.Buffer
	w, h   int
	frames int
}

// newGifWriter writes the header, a logical screen with no global colour
// table and, when loopCount >= 0, the NETSCAPE2.0 loop extension.
func newGifWriter(w, h, loopCount int) *gifWriter {
	g := &gifWriter{w: w, h: h}
	g.buf.WriteString("GIF89a")
	g.buf.Write([]byte{byte(w), byte(w >> 8), byte(h), byte(h >> 8), 0x70, 0, 0})
	if loopCount >= 0 {
		g.buf.Write([]byte{0x21, 0xFF, 11})
		g.buf.WriteString("NETSCAPE2.0")
		g.buf.Write([]byte{3, 1, byte(loopCount), byte(loopCount >> 8), 0})
	}
	return g
}

// frame appends one full-screen frame with its own colour table.
func (g *gifWriter) frame(p *image.Paletted, transparent int, delay int, disposal byte) error {
	if len(p.Palette) == 0 || len(p.Palette) > 256 {
		return errors.New("palette size out of range")
	}
	if delay < 0 {
		delay = 0
	}
	if delay > 0xFFFF {
		delay = 0xFFFF
	}
	packed := (disposal & 7) << 2
	ti := byte(0)
	if transparent >= 0 {
		packed |= 1
		ti = byte(transparent)
	}
	g.buf.Write([]byte{0x21, 0xF9, 4, packed, byte(delay), byte(delay >> 8), ti, 0})
	// Colour table size: the smallest power of two >= the palette, at least 2.
	bits := 1
	for 1<<bits < len(p.Palette) {
		bits++
	}
	b := p.Rect
	g.buf.Write([]byte{0x2C, 0, 0, 0, 0,
		byte(b.Dx()), byte(b.Dx() >> 8), byte(b.Dy()), byte(b.Dy() >> 8),
		0x80 | byte(bits-1)})
	for i := 0; i < 1<<bits; i++ {
		if i < len(p.Palette) {
			r, gg, bb, _ := p.Palette[i].RGBA()
			g.buf.Write([]byte{byte(r >> 8), byte(gg >> 8), byte(bb >> 8)})
		} else {
			g.buf.Write([]byte{0, 0, 0})
		}
	}
	lit := bits
	if lit < 2 {
		lit = 2
	}
	g.buf.WriteByte(byte(lit))
	var data bytes.Buffer
	lw := lzw.NewWriter(&data, lzw.LSB, lit)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if _, err := lw.Write(p.Pix[p.PixOffset(b.Min.X, y) : p.PixOffset(b.Min.X, y)+b.Dx()]); err != nil {
			return err
		}
	}
	if err := lw.Close(); err != nil {
		return err
	}
	d := data.Bytes()
	for len(d) > 0 {
		n := len(d)
		if n > 255 {
			n = 255
		}
		g.buf.WriteByte(byte(n))
		g.buf.Write(d[:n])
		d = d[n:]
	}
	g.buf.WriteByte(0)
	g.frames++
	return nil
}

func (g *gifWriter) finish() string {
	g.buf.WriteByte(0x3B)
	return g.buf.String()
}

// gifQuantise maps an image to at most 256 colours, with index `transparent`
// (or -1) for pixels whose alpha is under 128. An image with 256 colours or
// fewer (counting transparent as one) keeps its colours exactly; otherwise a
// median cut picks the palette and Floyd-Steinberg dithering maps to it.
func gifQuantise(img image.Image) (*image.Paletted, int) {
	src, ok := img.(*image.NRGBA)
	if !ok || src.Rect.Min != (image.Point{}) {
		b := img.Bounds()
		n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				n.SetNRGBA(x, y, color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA))
			}
		}
		src = n
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	hasTransparent := false
	exact := map[uint32]int{}
	exactOK := true
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride : y*src.Stride+4*w]
		for x := 0; x < w; x++ {
			if row[4*x+3] < 128 {
				hasTransparent = true
				continue
			}
			if exactOK {
				key := uint32(row[4*x])<<16 | uint32(row[4*x+1])<<8 | uint32(row[4*x+2])
				if _, seen := exact[key]; !seen {
					if len(exact) >= 256 {
						exactOK = false
						continue
					}
					exact[key] = len(exact)
				}
			}
		}
	}
	limit := 256
	if hasTransparent {
		limit = 255
	}
	out := image.NewPaletted(image.Rect(0, 0, w, h), nil)
	if exactOK && len(exact) <= limit {
		pal := make(color.Palette, len(exact))
		for key, i := range exact {
			pal[i] = color.RGBA{uint8(key >> 16), uint8(key >> 8), uint8(key), 255}
		}
		transparent := -1
		if hasTransparent {
			transparent = len(pal)
			pal = append(pal, color.RGBA{})
		}
		if len(pal) == 0 {
			pal = color.Palette{color.RGBA{}}
		}
		out.Palette = pal
		for y := 0; y < h; y++ {
			row := src.Pix[y*src.Stride:]
			for x := 0; x < w; x++ {
				if row[4*x+3] < 128 {
					out.Pix[y*out.Stride+x] = uint8(transparent)
					continue
				}
				key := uint32(row[4*x])<<16 | uint32(row[4*x+1])<<8 | uint32(row[4*x+2])
				out.Pix[y*out.Stride+x] = uint8(exact[key])
			}
		}
		return out, transparent
	}
	pal := gifMedianCut(src, limit)
	transparent := -1
	if hasTransparent {
		transparent = len(pal)
	}
	gifDither(src, out, pal, transparent)
	full := make(color.Palette, 0, len(pal)+1)
	for _, c := range pal {
		full = append(full, color.RGBA{c[0], c[1], c[2], 255})
	}
	if hasTransparent {
		full = append(full, color.RGBA{})
	}
	out.Palette = full
	return out, transparent
}

type gifBox struct {
	lo, hi int // a range of gifBucket indexes in the sorted slice
	count  int
}

type gifBucket struct {
	key            int // 5 bits per channel
	count          int
	sumR, sumG, sB int
}

// gifMedianCut picks up to `limit` colours from the opaque pixels.
func gifMedianCut(src *image.NRGBA, limit int) [][3]uint8 {
	var hist [32768]gifBucket
	w, h := src.Rect.Dx(), src.Rect.Dy()
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride:]
		for x := 0; x < w; x++ {
			if row[4*x+3] < 128 {
				continue
			}
			r, g, b := int(row[4*x]), int(row[4*x+1]), int(row[4*x+2])
			k := (r>>3)<<10 | (g>>3)<<5 | b>>3
			e := &hist[k]
			e.key = k
			e.count++
			e.sumR += r
			e.sumG += g
			e.sB += b
		}
	}
	buckets := make([]gifBucket, 0, 4096)
	for _, e := range hist {
		if e.count > 0 {
			buckets = append(buckets, e)
		}
	}
	if len(buckets) == 0 {
		return [][3]uint8{{0, 0, 0}}
	}
	chanOf := func(b gifBucket, c int) int { return (b.key >> (10 - 5*c)) & 31 }
	boxes := []gifBox{{lo: 0, hi: len(buckets)}}
	for _, b := range buckets {
		boxes[0].count += b.count
	}
	for len(boxes) < limit {
		// Split the box with the most pixels that has more than one bucket.
		best := -1
		for i, bx := range boxes {
			if bx.hi-bx.lo > 1 && (best < 0 || bx.count > boxes[best].count) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		bx := boxes[best]
		seg := buckets[bx.lo:bx.hi]
		// The channel with the widest range.
		ch, span := 0, -1
		for c := 0; c < 3; c++ {
			lo, hi := 31, 0
			for _, b := range seg {
				v := chanOf(b, c)
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			if hi-lo > span {
				ch, span = c, hi-lo
			}
		}
		gifSortBuckets(seg, func(b gifBucket) int { return chanOf(b, ch) })
		half, acc, cut := bx.count/2, 0, 1
		for i, b := range seg {
			acc += b.count
			if acc >= half {
				cut = i + 1
				break
			}
		}
		if cut >= len(seg) {
			cut = len(seg) - 1
		}
		left := gifBox{lo: bx.lo, hi: bx.lo + cut}
		right := gifBox{lo: bx.lo + cut, hi: bx.hi}
		for _, b := range buckets[left.lo:left.hi] {
			left.count += b.count
		}
		right.count = bx.count - left.count
		boxes[best] = left
		boxes = append(boxes, right)
	}
	pal := make([][3]uint8, 0, len(boxes))
	for _, bx := range boxes {
		var n, r, g, b int
		for _, e := range buckets[bx.lo:bx.hi] {
			n += e.count
			r += e.sumR
			g += e.sumG
			b += e.sB
		}
		if n == 0 {
			continue
		}
		pal = append(pal, [3]uint8{uint8((r + n/2) / n), uint8((g + n/2) / n), uint8((b + n/2) / n)})
	}
	return pal
}

// gifSortBuckets sorts by one channel (an insertion-free counting sort over
// the 32 channel values, stable).
func gifSortBuckets(seg []gifBucket, key func(gifBucket) int) {
	var counts [33]int
	for _, b := range seg {
		counts[key(b)+1]++
	}
	for i := 1; i < 33; i++ {
		counts[i] += counts[i-1]
	}
	tmp := make([]gifBucket, len(seg))
	for _, b := range seg {
		k := key(b)
		tmp[counts[k]] = b
		counts[k]++
	}
	copy(seg, tmp)
}

// gifDither maps src onto pal with Floyd-Steinberg error diffusion over the
// opaque pixels; a pixel with alpha under 128 gets index `transparent`.
func gifDither(src *image.NRGBA, out *image.Paletted, pal [][3]uint8, transparent int) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	var cache [1 << 18]int16 // 6 bits per channel; 0 = not yet looked up
	nearest := func(r, g, b int) int {
		k := (r>>2)<<12 | (g>>2)<<6 | b>>2
		if v := cache[k]; v != 0 {
			return int(v) - 1
		}
		best, bestD := 0, 1<<30
		for i, c := range pal {
			dr, dg, db := r-int(c[0]), g-int(c[1]), b-int(c[2])
			d := 2*dr*dr + 4*dg*dg + 3*db*db
			if d < bestD {
				best, bestD = i, d
			}
		}
		cache[k] = int16(best + 1)
		return best
	}
	cur := make([][3]int32, w+2)
	next := make([][3]int32, w+2)
	clamp := func(v int32) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return int(v)
	}
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride:]
		for i := range next {
			next[i] = [3]int32{}
		}
		for x := 0; x < w; x++ {
			if row[4*x+3] < 128 {
				out.Pix[y*out.Stride+x] = uint8(transparent)
				continue
			}
			var v [3]int32
			for c := 0; c < 3; c++ {
				v[c] = int32(row[4*x+c]) + cur[x+1][c]/16
			}
			i := nearest(clamp(v[0]), clamp(v[1]), clamp(v[2]))
			out.Pix[y*out.Stride+x] = uint8(i)
			for c := 0; c < 3; c++ {
				e := v[c] - int32(pal[i][c])
				cur[x+2][c] += e * 7
				next[x][c] += e * 3
				next[x+1][c] += e * 5
				next[x+2][c] += e * 1
			}
		}
		cur, next = next, cur
	}
}

// gifStill writes one image as a GIF of one frame (no loop extension).
func gifStill(img image.Image) (string, error) {
	p, transparent := gifQuantise(img)
	g := newGifWriter(p.Rect.Dx(), p.Rect.Dy(), -1)
	if err := g.frame(p, transparent, 0, 0); err != nil {
		return "", err
	}
	return g.finish(), nil
}

// gifAnimate runs fn over every composed frame of a GIF and writes the
// results as a GIF with the source's delays, disposals and loop count.
// keep(i) chooses the frames to write (nil keeps all); a dropped frame's
// delay is added to the frame written before it. forceBackground writes every
// frame with disposal "background", for an output whose frames are complete
// pictures that may be transparent where an earlier frame was not.
func gifAnimate(op string, data []byte, s *gifScanResult,
	fn func(image.Image) (image.Image, any), keep func(i int) bool, forceBackground bool) any {
	var g *gifWriter
	var pending *image.Paletted
	pendingT, pendingDelay := -1, 0
	var pendingDisposal byte
	flush := func() any {
		if pending == nil {
			return nil
		}
		if err := g.frame(pending, pendingT, pendingDelay, pendingDisposal); err != nil {
			return Err[any, any](ErrFfi(op + ": gif encode: " + err.Error()))
		}
		pending = nil
		return nil
	}
	bad := gifEachFrame(op, data, s, func(i int, composed *image.NRGBA) any {
		f := s.frames[i]
		if keep != nil && !keep(i) {
			pendingDelay += f.delay
			return nil
		}
		if e := flush(); e != nil {
			return e
		}
		out, e := fn(composed)
		if e != nil {
			return e
		}
		if g == nil {
			b := out.Bounds()
			g = newGifWriter(b.Dx(), b.Dy(), s.loopCount)
		}
		pending, pendingT = gifQuantise(out)
		pendingDelay = f.delay
		pendingDisposal = f.disposal
		if forceBackground {
			pendingDisposal = gifDisposalBackground
		}
		return nil
	})
	if bad != nil {
		return bad
	}
	if e := flush(); e != nil {
		return e
	}
	if g == nil {
		return Err[any, any](ErrInvalidInput(op + ": not a decodable GIF: no image"))
	}
	return Ok[any, any](g.finish())
}
