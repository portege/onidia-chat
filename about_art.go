package main

// about_art.go - the hand-drawn artwork the About modal shows: the round
// character badge (the pet's face) and the small four-point sparkles that dress
// up the word-art title.
//
// The badge is the same mark as the web header's logo: Onidia's head, happy
// expression, taken from the square viewBox "10 10 91 91" cropped out of the
// pet's 112x160 sprite. Every number inside drawFaceBadge is that drawing's own
// coordinate, unconverted - the point of the rewrite was to stop hand-rolling a
// likeness of her and to use the geometry the pet actually draws.
//
// The chat UI is a separate binary and cannot import the pet's character
// package, so - as before - the portrait is composed from primitives instead of
// shipping as an asset: no image files, nothing to load, and it scales with the
// badge radius. logoMap is the bridge: it carries the mapping from the mark's
// units to device pixels and clips every shape to the badge's white disc, so
// the shapes below can be written exactly as the pet draws them.

import (
	"image"
	"image/color"
	"math"
	"sort"
)

// Portrait colours. The pet's palette (ui.go) already carries the hair, the
// tail fill, the outline plum and the face, and the logo uses exactly those
// values - hair #5fcfd6, tails #41aeb6, outline #2f223e, face #ffe4ce are
// colHeader, colTealShade, colPlum and colSkin - so the badge reuses them and
// only adds the marks the logo needs on top.
//
// The ties take colHaiyaPink rather than the logo's own #ff7a9e: the tie is the
// one place the About panel's own accent reads better, and it keeps the
// bobbles the same pink as the word art's drop shadow beside them.
var (
	colSkin   = color.RGBA{255, 228, 206, 255} // chibi face
	colBlush  = color.RGBA{246, 143, 168, 110} // translucent cheek blush
	colFaceLo = color.RGBA{198, 134, 100, 255} // jaw shadow: the face's outline
	colInk    = color.RGBA{40, 32, 50, 255}    // happy eyes, near-black rather than outline plum
	colMouth  = color.RGBA{176, 66, 90, 255}   // open mouth interior
	colTongue = color.RGBA{231, 126, 143, 255} // tongue, clipped to the mouth
)

// The mark's own coordinate space. The viewBox is 10..101 on both axes; the
// badge is centred on the head at (56,60), not on the viewBox's middle.
const (
	logoCX = 56.0
	logoCY = 60.0
	// logoFar is the distance from the head centre to the furthest ink, the
	// cowlick's tip. Sizing from the art rather than from the viewBox is what
	// keeps the cowlick inside the disc; the twin tails still overrun and are
	// clipped, which is how a framed portrait crops.
	logoFar = 45.5
)

// logoMap maps the mark's units onto the badge: where to draw about, the unit
// scale, and the radius of the white disc that every shape is clipped to.
type logoMap struct {
	cx, cy int
	s      float64
	r      float64
}

func newLogoMap(cx, cy, r int) logoMap {
	return logoMap{cx: cx, cy: cy, s: float64(r-2) / logoFar, r: float64(r - 2)}
}

func (m logoMap) x(u float64) float64 { return float64(m.cx) + (u-logoCX)*m.s }
func (m logoMap) y(v float64) float64 { return float64(m.cy) + (v-logoCY)*m.s }
func (m logoMap) l(w float64) float64 { return w * m.s }

// put blends one pixel, dropping it if it falls outside the badge's white disc.
func (m logoMap) put(dst *image.NRGBA, x, y float64, col color.RGBA) {
	dx, dy := x-float64(m.cx), y-float64(m.cy)
	if dx*dx+dy*dy > m.r*m.r+0.5 { // +0.5 matches fillDisc's own edge test
		return
	}
	fillRect(dst, int(math.Floor(x)), int(math.Floor(y)), 1, 1, col)
}

// disc fills a circle in mark units.
func (m logoMap) disc(dst *image.NRGBA, u, v, rad float64, col color.RGBA) {
	px, py, pr := m.x(u), m.y(v), m.l(rad)
	rr := pr*pr + 0.5
	n := int(pr) + 1
	for dy := -n; dy <= n; dy++ {
		for dx := -n; dx <= n; dx++ {
			fx, fy := float64(dx)+0.5, float64(dy)+0.5
			if fx*fx+fy*fy <= rr {
				m.put(dst, px+fx, py+fy, col)
			}
		}
	}
}

// ell fills an axis-aligned ellipse in mark units - the blush.
func (m logoMap) ell(dst *image.NRGBA, u, v, ru, rv float64, col color.RGBA) {
	px, py, a, b := m.x(u), m.y(v), m.l(ru), m.l(rv)
	if a <= 0 || b <= 0 {
		return
	}
	for dy := -int(b) - 1; dy <= int(b)+1; dy++ {
		f := 1 - float64(dy*dy)/(b*b)
		if f < 0 {
			continue
		}
		hw := a * math.Sqrt(f)
		for dx := -int(hw) - 1; dx <= int(hw)+1; dx++ {
			m.put(dst, px+float64(dx)+0.5, py+float64(dy)+0.5, col)
		}
	}
}

// seg strokes a thick line segment with round caps in mark units - the side
// locks, the cowlick and the happy eyes.
func (m logoMap) seg(dst *image.NRGBA, u0, v0, u1, v1, w float64, col color.RGBA) {
	x0, y0 := m.x(u0), m.y(v0)
	x1, y1 := m.x(u1), m.y(v1)
	hw := m.l(w) / 2
	rr := hw*hw + 0.5
	ex, ey := x1-x0, y1-y0
	den := ex*ex + ey*ey
	lo, hi := math.Min(x0, x1)-hw-1, math.Max(x0, x1)+hw+1
	top, bot := math.Min(y0, y1)-hw-1, math.Max(y0, y1)+hw+1
	for y := int(math.Floor(top)); y <= int(math.Ceil(bot)); y++ {
		for x := int(math.Floor(lo)); x <= int(math.Ceil(hi)); x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			t := 0.0
			if den > 0 {
				t = math.Max(0, math.Min(1, ((px-x0)*ex+(py-y0)*ey)/den))
			}
			dx, dy := px-(x0+t*ex), py-(y0+t*ey)
			if dx*dx+dy*dy <= rr {
				m.put(dst, px, py, col)
			}
		}
	}
}

// poly fills a closed polygon given as flat u,v pairs in mark units, even-odd.
func (m logoMap) poly(dst *image.NRGBA, uv []float64, col color.RGBA) {
	if len(uv) < 6 {
		return
	}
	xs, ys := make([]float64, len(uv)/2), make([]float64, len(uv)/2)
	top, bot := math.Inf(1), math.Inf(-1)
	for i := range xs {
		xs[i], ys[i] = m.x(uv[2*i]), m.y(uv[2*i+1])
		top, bot = math.Min(top, ys[i]), math.Max(bot, ys[i])
	}
	for y := int(math.Floor(top)); y <= int(math.Ceil(bot)); y++ {
		sy := float64(y) + 0.5
		var cross []float64
		for i := range xs {
			j := (i + 1) % len(xs)
			if (sy >= ys[i]) == (sy >= ys[j]) { // not a spanning edge
				continue
			}
			t := (sy - ys[i]) / (ys[j] - ys[i])
			cross = append(cross, xs[i]+t*(xs[j]-xs[i]))
		}
		sort.Float64s(cross)
		for k := 0; k+1 < len(cross); k += 2 {
			for x := int(math.Floor(cross[k])); x < int(math.Ceil(cross[k+1])); x++ {
				m.put(dst, float64(x)+0.5, sy, col)
			}
		}
	}
}

// arcPts appends a circle's arc, in mark units, to a polygon under construction.
func arcPts(dst []float64, u, v, rad, a0, a1 float64) []float64 {
	const steps = 28
	for i := 0; i <= steps; i++ {
		a := a0 + (a1-a0)*float64(i)/steps
		dst = append(dst, u+rad*math.Cos(a), v+rad*math.Sin(a))
	}
	return dst
}

// taper fills a TaperOutlined capsule: the convex hull of a wide base circle and
// a narrow tip circle, which is the pet's own stroke primitive. The tangent
// between the two sits at acos((r1-r2)/L) off the axis, and both end arcs are
// appended in one increasing sweep so the hull closes.
func (m logoMap) taper(dst *image.NRGBA, bu, bv, tu, tv, r1, r2 float64, col color.RGBA) {
	du, dv := tu-bu, tv-bv
	l := math.Hypot(du, dv)
	if l <= 0 || r1 <= r2 {
		return
	}
	th := math.Atan2(dv, du)
	a := math.Acos(math.Max(-1, math.Min(1, (r1-r2)/l)))
	pts := arcPts(nil, bu, bv, r1, th+a, th+2*math.Pi-a) // base, the long way round
	pts = arcPts(pts, tu, tv, r2, th-a, th+a)            // then the tip
	m.poly(dst, pts, col)
}

// hairCap draws the hair cap - the top of a circle, closed off by the chord on
// the hairline - in both its passes at once. The plum outline and the teal fill
// are the same circle at two radii about one centre and one pair of angles, so
// the rim is even the whole way round; the strip is the half of the pet's
// centred stroke that lands below the chord, without which the hair would meet
// the forehead on a bare line.
//
// Both radii go in because the chord half-width bounds the radius from below:
// shrinking a cap while recomputing its centre from the chord asks for the
// square root of a negative, which silently draws nothing.
func (m logoMap) hairCap(dst *image.NRGBA, outer, inner, strip float64) {
	const brow, half = 51.0, 34.59
	cy := brow + math.Sqrt(outer*outer-half*half)
	a0, a1 := math.Atan2(brow-cy, -half), math.Atan2(brow-cy, half)
	if a1 < a0 {
		a1 += 2 * math.Pi
	}
	m.poly(dst, []float64{
		logoCX - half, brow, logoCX + half, brow,
		logoCX + half, brow + strip, logoCX - half, brow + strip,
	}, colPlum)
	m.poly(dst, arcPts(nil, logoCX, cy, outer, a0, a1), colPlum)
	m.poly(dst, arcPts(nil, logoCX, cy, inner, a0, a1), colHeader)
}

// smile fills the open mouth: the lower semicircle of a small disc closed by its
// chord. The tongue is the same shape, smaller and lower, so it can only ever
// land inside the mouth.
func (m logoMap) smile(dst *image.NRGBA, v, rad float64, col color.RGBA) {
	m.poly(dst, arcPts(nil, logoCX, v, rad, 0, math.Pi), col)
}

// fringe fills one of the five hair strands: a triangle hanging off the
// hairline with its top edge from x to x+w and a point at (tipX, tipY). The
// plum pass is the same triangle pulled 16% in about its own centre, which
// leaves the rim the pet's 1.6-unit stroke does.
func (m logoMap) fringe(dst *image.NRGBA, x, w, tipX, tipY float64) {
	pts := []float64{x, 49, x + w, 49, tipX, tipY}
	m.poly(dst, pts, colPlum)
	mx := (pts[0] + pts[2] + pts[4]) / 3
	my := (pts[1] + pts[3] + pts[5]) / 3
	for i := 0; i < len(pts); i += 2 {
		pts[i] = mx + (pts[i]-mx)*0.84
		pts[i+1] = my + (pts[i+1]-my)*0.84
	}
	m.poly(dst, pts, colHeader)
}

// a diamond body plus the two long thin spikes that make it read as a star
// drawFaceBadge paints the round character badge: the header logo, Onidia's
// head with her happy expression, in a plum ring over bubble-white. It is the
// pet's own geometry (drawHead, drawFace, drawTwinTails, and eyeArcUp /
// mouthOpenSmile) rather than a likeness of it, so the About card and the web
// header show the same drawing. Everything is scaled through logoMap, so the
// art works at any badge size; below ~10px it would be mud, so it draws nothing.
func drawFaceBadge(dst *image.NRGBA, cx, cy, r int) {
	if r < 10 {
		return
	}
	// Plum ring with bubble-white inside: the frame the portrait sits in.
	fillDisc(dst, cx, cy, r, colPlum)
	fillDisc(dst, cx, cy, r-2, colBubbleFill)
	m := newLogoMap(cx, cy, r)

	// Twin tails, behind the head. Each is one tapered capsule running from a
	// wide base at the crown down past the jaw; the plum outline and the teal
	// fill are the same capsule with the radii inset, which is how the pet
	// strokes it. side is +1 for the tail on the mark's left.
	for _, side := range []float64{1, -1} {
		at := func(dx float64) float64 { return logoCX + side*dx }
		m.taper(dst, at(25), 55.004, at(39.238), 90.238, 10.2, 4.8, colPlum)
		m.taper(dst, at(25), 55.004, at(39.238), 90.238, 8, 2.6, colTealShade)
		m.seg(dst, at(28), 62, at(33.54), 76.14, 3, colHairLight)
	}

	// Head: the plum silhouette, then the teal fill inside it.
	m.disc(dst, logoCX, logoCY, 38.7, colPlum)
	m.disc(dst, logoCX, logoCY, 36.5, colHeader)

	// Face, dropped a little so the hair overhangs the brow, outlined in the
	// jaw shadow rather than the plum every other outline uses.
	m.disc(dst, logoCX, 63, 32.5, colFaceLo)
	m.disc(dst, logoCX, 63, 31.5, colSkin)

	// The hair cap, then the fringe over it. The cap is the circle the pet cuts
	// off at the hairline; the five strands are separate triangles sitting on
	// that line with alternating tip lengths, not a scalloped edge - a scallop
	// reads as one solid band across the forehead.
	m.hairCap(dst, 35.5, 34.4, 1.1)
	for _, f := range [][4]float64{ // top x, width, tip x, tip y
		{27, 10, 35, 66}, {39, 10, 41, 69}, {51, 10, 59, 66},
		{63, 10, 65, 69}, {75, 10, 83, 66},
	} {
		m.fringe(dst, f[0], f[1], f[2], f[3])
	}

	// Side locks: a thick round-capped stroke down each cheek, plum then teal.
	for _, l := range [][2]float64{{24, 28}, {88, 84}} { // top x, bottom x
		m.seg(dst, l[0], 48, l[1], 88, 14, colPlum)
		m.seg(dst, l[0], 48, l[1], 88, 10, colHeader)
	}

	// The cowlick, thinning as it goes over.
	m.seg(dst, 54, 27, 62, 15, 2.8, colHeader)
	m.seg(dst, 62, 15, 69, 19, 1.1, colHeader)

	// Her signature pink ties, on the side locks at the brow line.
	for _, x := range []float64{29, 83} {
		m.disc(dst, x, 52, 6.2, colPlum)
		m.disc(dst, x, 52, 5.4, colHaiyaPink)
	}

	// Happy closed eyes: the shallow ^ the pet's happy face is drawn with, plus
	// the small outward lash at each outer corner. The right eye is the left
	// one mirrored about the centre line.
	for _, side := range []float64{1, -1} {
		at := func(dx float64) float64 { return logoCX + side*dx }
		m.seg(dst, at(18.5), 73.6, at(15), 69.6, 4, colInk)
		m.seg(dst, at(15), 69.6, at(11), 69.6, 4, colInk)
		m.seg(dst, at(11), 69.6, at(7.5), 73.6, 4, colInk)
		m.seg(dst, at(18.2), 70.8, at(21.6), 68.4, 2.8, colInk)
	}

	// Blush, then the small open smile with its tongue.
	m.ell(dst, 35, 84, 7.43, 4.05, colBlush)
	m.ell(dst, 77, 84, 7.43, 4.05, colBlush)
	m.smile(dst, 87.5, 4.8, colMouth)
	m.smile(dst, 90.2, 2.64, colTongue)
}

// drawSparkle paints a four-point twinkle with a soft halo and a white core:
// rather than a dot. r is the diamond radius.
func drawSparkle(dst *image.NRGBA, cx, cy, r int, col color.RGBA) {
	if r < 2 {
		return
	}
	halo := col
	halo.A = 60
	fillDisc(dst, cx, cy, r+1, halo)
	for dy := -r; dy <= r; dy++ {
		d := dy
		if d < 0 {
			d = -d
		}
		hw := r - d
		fillRect(dst, cx-hw, cy+dy, 2*hw+1, 1, col)
	}
	fillRect(dst, cx-r-3, cy, 2*r+7, 1, col) // horizontal spike
	fillRect(dst, cx, cy-r-3, 1, 2*r+7, col) // vertical spike
	fillRect(dst, cx, cy, 2, 2, colWhite)    // bright core
}
