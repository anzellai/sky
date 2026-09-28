//go:build !js

// Sky.Tui — Std.Ui.Canvas scenes in character cells.
//
// A scene (Std/Ui/Canvas.sky) is an <svg data-sky-scene> Raw node. The web
// renderers draw the SVG; a terminal cannot, so this file rasterises the same
// node onto a Braille dot grid: each cell holds 2×4 dots (U+2800..U+28FF), so a
// 40×10-cell box resolves 80×40 dots. Shapes are flattened to polygons and
// polylines in scene units, mapped through their transforms (translate,
// rotate, scale, matrix, nested groups), then filled (even-odd, sampled at dot
// centres) and stroked (a line through the dots). A cell takes the colour of
// the last shape that set a dot in it. `text` is written on the cell grid at
// its anchor, over the dots. What a terminal cannot show is dropped: opacity
// below 0.2 hides a shape, other opacity is ignored, stroke width is one dot.
//
// Only the SVG subset Std.Ui.Canvas emits is read: rect, circle, ellipse,
// line, polyline, polygon, path (M L H V Q C A Z, absolute or relative), text
// and g, with fill / stroke / opacity / text-anchor / transform attributes.

package rt

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// tuiSceneNode returns the scene <svg> of a Raw node's Std.Html value.
func tuiSceneNode(node any) (VNode, bool) {
	vn := HtmlToVNode(node)
	if vn.Kind == "element" && vn.Tag == "svg" {
		if _, ok := vn.Attrs["data-sky-scene"]; ok {
			return vn, true
		}
	}
	return VNode{}, false
}

// tuiSceneLayout sizes a scene box: its width and height (CSS px) in cells,
// scaled down to fit maxW / maxH with the aspect ratio kept.
func tuiSceneLayout(vn VNode, ctx tuiLayoutCtx, maxW, maxH int) layoutBox {
	w := pxToCellsX(atoiOr(vn.Attrs["width"], 300), ctx)
	h := pxToCellsY(atoiOr(vn.Attrs["height"], 150), ctx)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if maxW > 0 && w > maxW {
		h = int(math.Max(1, math.Round(float64(h)*float64(maxW)/float64(w))))
		w = maxW
	}
	if maxH > 0 && h > maxH {
		w = int(math.Max(1, math.Round(float64(w)*float64(maxH)/float64(h))))
		h = maxH
	}
	v := vn
	return layoutBox{kind: "scene", scene: &v, width: w, height: h}
}

func atoiOr(s string, d int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return d
}

// tuiSceneCell is one rasterised cell: its glyph and colour ("" = empty).
type tuiSceneCell struct {
	ch string
	fg tuiColor
}

// paintScene rasterises a scene into grid at (col, row), w×h cells. Empty
// cells are left as they are, so a background shows through.
func paintScene(grid [][]tuiCell, vn *VNode, col, row, w, h int) {
	if vn == nil {
		return
	}
	cells := rasterScene(*vn, w, h)
	for r := 0; r < len(cells); r++ {
		gr := row + r
		if gr < 0 || gr >= len(grid) {
			continue
		}
		for c := 0; c < len(cells[r]); c++ {
			gc := col + c
			if gc < 0 || gc >= len(grid[gr]) || cells[r][c].ch == "" {
				continue
			}
			grid[gr][gc].ch = cells[r][c].ch
			if cells[r][c].fg.set {
				grid[gr][gc].fg = cells[r][c].fg
			}
		}
	}
}

// ─── Rasteriser ──────────────────────────────────────────────────────

type sceneMatrix [6]float64 // a b c d e f: x' = a x + c y + e, y' = b x + d y + f

var sceneIdentity = sceneMatrix{1, 0, 0, 1, 0, 0}

func (m sceneMatrix) mul(n sceneMatrix) sceneMatrix {
	return sceneMatrix{
		m[0]*n[0] + m[2]*n[1],
		m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3],
		m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4],
		m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

func (m sceneMatrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

type scenePaint struct {
	fill, stroke string
	opacity      float64
	anchor       string
}

type sceneRaster struct {
	cols, rows int
	dotW, dotH int
	dots       []bool
	dotColor   []tuiColor
	dotSet     []bool
	text       [][]tuiSceneCell
}

// rasterScene draws the scene into cols×rows cells.
func rasterScene(vn VNode, cols, rows int) [][]tuiSceneCell {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	r := &sceneRaster{cols: cols, rows: rows, dotW: cols * 2, dotH: rows * 4}
	r.dots = make([]bool, r.dotW*r.dotH)
	r.dotColor = make([]tuiColor, r.dotW*r.dotH)
	r.dotSet = make([]bool, r.dotW*r.dotH)
	r.text = make([][]tuiSceneCell, rows)
	for i := range r.text {
		r.text[i] = make([]tuiSceneCell, cols)
	}
	vw, vh := sceneViewBox(vn)
	base := sceneMatrix{float64(r.dotW) / vw, 0, 0, float64(r.dotH) / vh, 0, 0}
	for _, c := range vn.Children {
		r.draw(c, base, scenePaint{opacity: 1})
	}
	return r.cells()
}

// sceneViewBox is the scene's coordinate size (viewBox, else width/height).
func sceneViewBox(vn VNode) (float64, float64) {
	f := strings.Fields(strings.ReplaceAll(vn.Attrs["viewBox"], ",", " "))
	if len(f) == 4 {
		w, e1 := strconv.ParseFloat(f[2], 64)
		h, e2 := strconv.ParseFloat(f[3], 64)
		if e1 == nil && e2 == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	return float64(atoiOr(vn.Attrs["width"], 300)), float64(atoiOr(vn.Attrs["height"], 150))
}

func (r *sceneRaster) draw(n VNode, m sceneMatrix, inherited scenePaint) {
	if n.Kind != "element" {
		return
	}
	p := inherited
	if v, ok := n.Attrs["fill"]; ok {
		p.fill = v
	}
	if v, ok := n.Attrs["stroke"]; ok {
		p.stroke = v
	}
	if v, ok := n.Attrs["text-anchor"]; ok {
		p.anchor = v
	}
	if v, ok := n.Attrs["opacity"]; ok {
		if o, err := strconv.ParseFloat(v, 64); err == nil {
			p.opacity *= o
		}
	}
	if t, ok := n.Attrs["transform"]; ok {
		m = m.mul(parseSceneTransform(t))
	}
	if p.opacity < 0.2 {
		return
	}
	a := func(k string) float64 { return sceneNum(n.Attrs[k]) }
	switch n.Tag {
	case "title", "desc":
		return
	case "g":
		for _, c := range n.Children {
			r.draw(c, m, p)
		}
	case "rect":
		x, y, w, h := a("x"), a("y"), a("width"), a("height")
		r.shape([][][2]float64{{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}}, true, m, p)
	case "circle":
		r.shape([][][2]float64{ellipsePoints(a("cx"), a("cy"), a("r"), a("r"))}, true, m, p)
	case "ellipse":
		r.shape([][][2]float64{ellipsePoints(a("cx"), a("cy"), a("rx"), a("ry"))}, true, m, p)
	case "line":
		r.shape([][][2]float64{{{a("x1"), a("y1")}, {a("x2"), a("y2")}}}, false, m, p)
	case "polyline":
		r.shape([][][2]float64{parseScenePoints(n.Attrs["points"])}, false, m, p)
	case "polygon":
		r.shape([][][2]float64{parseScenePoints(n.Attrs["points"])}, true, m, p)
	case "path":
		subs, closed := parseScenePath(n.Attrs["d"])
		r.path(subs, closed, m, p)
	case "text":
		var sb strings.Builder
		for _, c := range n.Children {
			if c.Kind == "text" {
				sb.WriteString(c.Text)
			}
		}
		r.textAt(sb.String(), a("x"), a("y"), m, p)
	}
}

// shape fills (when closed) then strokes a list of point runs.
func (r *sceneRaster) shape(runs [][][2]float64, closed bool, m sceneMatrix, p scenePaint) {
	flags := make([]bool, len(runs))
	for i := range flags {
		flags[i] = closed
	}
	r.path(runs, flags, m, p)
}

func (r *sceneRaster) path(runs [][][2]float64, closed []bool, m sceneMatrix, p scenePaint) {
	dev := make([][][2]float64, len(runs))
	for i, run := range runs {
		for _, pt := range run {
			x, y := m.apply(pt[0], pt[1])
			dev[i] = append(dev[i], [2]float64{x, y})
		}
	}
	if fc, ok := scenePaintColor(p.fill, true); ok {
		r.fillPolys(dev, fc)
	}
	if sc, ok := scenePaintColor(p.stroke, false); ok {
		for i, run := range dev {
			for j := 1; j < len(run); j++ {
				r.line(run[j-1], run[j], sc)
			}
			if i < len(closed) && closed[i] && len(run) > 2 {
				r.line(run[len(run)-1], run[0], sc)
			}
		}
	}
}

// fillPolys fills every run as a closed polygon, even-odd, sampling each
// dot at its centre.
func (r *sceneRaster) fillPolys(polys [][][2]float64, c tuiColor) {
	for y := 0; y < r.dotH; y++ {
		cy := float64(y) + 0.5
		var xs []float64
		for _, poly := range polys {
			n := len(poly)
			if n < 3 {
				continue
			}
			for i := 0; i < n; i++ {
				a, b := poly[i], poly[(i+1)%n]
				if (a[1] <= cy && b[1] > cy) || (b[1] <= cy && a[1] > cy) {
					xs = append(xs, a[0]+(cy-a[1])*(b[0]-a[0])/(b[1]-a[1]))
				}
			}
		}
		sortFloats(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			x0 := int(math.Ceil(xs[i] - 0.5))
			x1 := int(math.Floor(xs[i+1] - 0.5))
			for x := x0; x <= x1; x++ {
				r.set(x, y, c)
			}
		}
	}
}

func sortFloats(xs []float64) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// line sets the dots along a segment (device dot coordinates).
func (r *sceneRaster) line(a, b [2]float64, c tuiColor) {
	dx, dy := b[0]-a[0], b[1]-a[1]
	steps := int(math.Ceil(math.Max(math.Abs(dx), math.Abs(dy))))
	if steps < 1 {
		steps = 1
	}
	if steps > 100000 {
		steps = 100000
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		r.set(int(math.Floor(a[0]+dx*t)), int(math.Floor(a[1]+dy*t)), c)
	}
}

func (r *sceneRaster) set(x, y int, c tuiColor) {
	if x < 0 || y < 0 || x >= r.dotW || y >= r.dotH {
		return
	}
	i := y*r.dotW + x
	r.dots[i] = true
	r.dotColor[i] = c
	r.dotSet[i] = true
}

func (r *sceneRaster) textAt(s string, x, y float64, m sceneMatrix, p scenePaint) {
	s = sanitiseString(strings.TrimSpace(s))
	if s == "" {
		return
	}
	dx, dy := m.apply(x, y)
	col := int(math.Floor(dx / 2))
	row := int(math.Floor((dy - 1) / 4))
	runes := []rune(s)
	switch p.anchor {
	case "middle":
		col -= len(runes) / 2
	case "end":
		col -= len(runes)
	}
	fg, _ := scenePaintColor(p.fill, true)
	if row < 0 || row >= r.rows {
		return
	}
	for i, ch := range runes {
		cc := col + i
		if cc < 0 || cc >= r.cols {
			continue
		}
		r.text[row][cc] = tuiSceneCell{ch: string(ch), fg: fg}
	}
}

// Braille dot bits, indexed [dy][dx] within a cell.
var brailleBits = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

func (r *sceneRaster) cells() [][]tuiSceneCell {
	out := make([][]tuiSceneCell, r.rows)
	for cy := 0; cy < r.rows; cy++ {
		out[cy] = make([]tuiSceneCell, r.cols)
		for cx := 0; cx < r.cols; cx++ {
			if t := r.text[cy][cx]; t.ch != "" {
				out[cy][cx] = t
				continue
			}
			var bits rune
			var col tuiColor
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					i := (cy*4+dy)*r.dotW + cx*2 + dx
					if r.dots[i] {
						bits |= brailleBits[dy][dx]
						if r.dotSet[i] {
							col = r.dotColor[i]
						}
					}
				}
			}
			if bits != 0 {
				out[cy][cx] = tuiSceneCell{ch: string(rune(0x2800) + bits), fg: col}
			}
		}
	}
	return out
}

// ─── Parsing ─────────────────────────────────────────────────────────

func sceneNum(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

func ellipsePoints(cx, cy, rx, ry float64) [][2]float64 {
	const n = 48
	pts := make([][2]float64, 0, n)
	for i := 0; i < n; i++ {
		t := 2 * math.Pi * float64(i) / n
		pts = append(pts, [2]float64{cx + rx*math.Cos(t), cy + ry*math.Sin(t)})
	}
	return pts
}

func sceneNumbers(s string) []float64 {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	out := make([]float64, 0, len(f))
	for _, x := range f {
		out = append(out, sceneNum(x))
	}
	return out
}

func parseScenePoints(s string) [][2]float64 {
	nums := sceneNumbers(s)
	var pts [][2]float64
	for i := 0; i+1 < len(nums); i += 2 {
		pts = append(pts, [2]float64{nums[i], nums[i+1]})
	}
	return pts
}

// parseSceneTransform reads an SVG transform list into one matrix; the
// transforms apply right to left to a point, as in SVG.
func parseSceneTransform(s string) sceneMatrix {
	m := sceneIdentity
	for {
		open := strings.IndexByte(s, '(')
		if open < 0 {
			return m
		}
		end := strings.IndexByte(s[open:], ')')
		if end < 0 {
			return m
		}
		name := strings.TrimSpace(strings.TrimLeft(s[:open], ", "))
		args := sceneNumbers(s[open+1 : open+end])
		s = s[open+end+1:]
		arg := func(i int, d float64) float64 {
			if i < len(args) {
				return args[i]
			}
			return d
		}
		var t sceneMatrix
		switch name {
		case "translate":
			t = sceneMatrix{1, 0, 0, 1, arg(0, 0), arg(1, 0)}
		case "scale":
			sx := arg(0, 1)
			t = sceneMatrix{sx, 0, 0, arg(1, sx), 0, 0}
		case "rotate":
			rad := arg(0, 0) * math.Pi / 180
			cs, sn := math.Cos(rad), math.Sin(rad)
			t = sceneMatrix{cs, sn, -sn, cs, 0, 0}
			if len(args) >= 3 {
				cx, cy := args[1], args[2]
				t = sceneMatrix{1, 0, 0, 1, cx, cy}.mul(t).mul(sceneMatrix{1, 0, 0, 1, -cx, -cy})
			}
		case "matrix":
			if len(args) == 6 {
				t = sceneMatrix{args[0], args[1], args[2], args[3], args[4], args[5]}
			} else {
				t = sceneIdentity
			}
		default:
			t = sceneIdentity
		}
		m = m.mul(t)
	}
}

// parseScenePath flattens a path to point runs (curves and arcs sampled),
// with each run's closed flag.
func parseScenePath(d string) ([][][2]float64, []bool) {
	var runs [][][2]float64
	var closed []bool
	var cur [][2]float64
	var x, y, sx, sy float64
	flush := func(c bool) {
		if len(cur) > 1 {
			runs = append(runs, cur)
			closed = append(closed, c)
		}
		cur = nil
	}
	toks := scenePathTokens(d)
	i := 0
	var cmd byte
	next := func() float64 {
		if i >= len(toks) {
			return 0
		}
		v := sceneNum(toks[i])
		i++
		return v
	}
	for i < len(toks) {
		if len(toks[i]) == 1 && isPathLetter(toks[i][0]) {
			cmd = toks[i][0]
			i++
		} else if cmd == 0 {
			i++
			continue
		}
		rel := cmd >= 'a' && cmd <= 'z'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = x, y
		}
		switch cmd | 0x20 {
		case 'm':
			flush(false)
			x, y = ox+next(), oy+next()
			sx, sy = x, y
			cur = [][2]float64{{x, y}}
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			x, y = ox+next(), oy+next()
			cur = append(cur, [2]float64{x, y})
		case 'h':
			x = ox + next()
			cur = append(cur, [2]float64{x, y})
		case 'v':
			y = oy + next()
			cur = append(cur, [2]float64{x, y})
		case 'q':
			cx, cy := ox+next(), oy+next()
			ex, ey := ox+next(), oy+next()
			for k := 1; k <= 16; k++ {
				t := float64(k) / 16
				u := 1 - t
				cur = append(cur, [2]float64{u*u*x + 2*u*t*cx + t*t*ex, u*u*y + 2*u*t*cy + t*t*ey})
			}
			x, y = ex, ey
		case 'c':
			c1x, c1y := ox+next(), oy+next()
			c2x, c2y := ox+next(), oy+next()
			ex, ey := ox+next(), oy+next()
			for k := 1; k <= 20; k++ {
				t := float64(k) / 20
				u := 1 - t
				cur = append(cur, [2]float64{
					u*u*u*x + 3*u*u*t*c1x + 3*u*t*t*c2x + t*t*t*ex,
					u*u*u*y + 3*u*u*t*c1y + 3*u*t*t*c2y + t*t*t*ey,
				})
			}
			x, y = ex, ey
		case 'a':
			rx, ry, rot := next(), next(), next()
			large, sweep := next() != 0, next() != 0
			ex, ey := ox+next(), oy+next()
			cur = append(cur, sceneArc(x, y, rx, ry, rot, large, sweep, ex, ey)...)
			x, y = ex, ey
		case 'z':
			if len(cur) > 0 {
				cur = append(cur, [2]float64{sx, sy})
			}
			flush(true)
			x, y = sx, sy
			cur = [][2]float64{{x, y}}
			cmd = 0
			continue
		default:
			i++
			continue
		}
	}
	flush(false)
	return runs, closed
}

func isPathLetter(c byte) bool {
	return strings.IndexByte("MmLlHhVvQqCcAaZz", c) >= 0
}

func scenePathTokens(d string) []string {
	var out []string
	var b strings.Builder
	emit := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case isPathLetter(c):
			emit()
			out = append(out, string(c))
		case c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r':
			emit()
		case c == '-' && b.Len() > 0 && !strings.HasSuffix(b.String(), "e"):
			emit()
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	emit()
	return out
}

// sceneArc samples an SVG elliptical arc (endpoint parameterisation,
// SVG 1.1 Appendix F.6) as points after the start point.
func sceneArc(x1, y1, rx, ry, rotDeg float64, large, sweep bool, x2, y2 float64) [][2]float64 {
	if rx == 0 || ry == 0 || (x1 == x2 && y1 == y2) {
		return [][2]float64{{x2, y2}}
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := rotDeg * math.Pi / 180
	cp, sp := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p := cp*dx + sp*dy
	y1p := -sp*dx + cp*dy
	lam := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry)
	if lam > 1 {
		s := math.Sqrt(lam)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := 0.0
	if den != 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx := cp*cxp - sp*cyp + (x1+x2)/2
	cy := sp*cxp + cp*cyp + (y1+y2)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	const n = 24
	pts := make([][2]float64, 0, n)
	for k := 1; k <= n; k++ {
		t := t1 + dt*float64(k)/n
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		pts = append(pts, [2]float64{cp*ex - sp*ey + cx, sp*ex + cp*ey + cy})
	}
	return pts
}

// scenePaintColor resolves a fill or stroke. An absent fill is SVG's
// default black, drawn in the terminal's default colour (black on a dark
// terminal would hide it); an absent stroke draws nothing. "none" and
// "transparent" draw nothing; "currentColor" is the default colour.
func scenePaintColor(v string, isFill bool) (tuiColor, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "":
		return tuiColor{}, isFill
	case "none", "transparent":
		return tuiColor{}, false
	case "currentcolor", "black":
		return tuiColor{}, true
	case "white":
		return tuiColor{set: true, r: 255, g: 255, b: 255}, true
	}
	if strings.HasPrefix(v, "#") {
		h := v[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) >= 6 {
			n, err := strconv.ParseUint(h[:6], 16, 32)
			if err == nil {
				return tuiColor{set: true, r: uint8(n >> 16), g: uint8(n >> 8), b: uint8(n)}, true
			}
		}
		return tuiColor{}, true
	}
	if open := strings.IndexByte(v, '('); open > 0 && strings.HasPrefix(v, "rgb") {
		nums := sceneNumbers(strings.TrimSuffix(v[open+1:], ")"))
		if len(nums) >= 3 {
			if len(nums) >= 4 && nums[3] <= 0 {
				return tuiColor{}, false
			}
			return tuiColor{set: true, r: clampByte(nums[0]), g: clampByte(nums[1]), b: clampByte(nums[2])}, true
		}
	}
	return tuiColor{}, true
}

func clampByte(f float64) uint8 {
	if f < 0 {
		return 0
	}
	if f > 255 {
		return 255
	}
	return uint8(math.Round(f))
}
