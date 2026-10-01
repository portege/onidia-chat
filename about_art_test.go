package main

// Tests for the About modal's badge artwork: that it is the traced logo rather
// than the hand-drawn portrait it replaced, that it scales, and that it stays
// mirror-symmetric - every shape in it is placed by mirroring about the head's
// centre line, so an off-by-one in that helper is invisible in a screenshot but
// obvious here.

import (
	"image"
	"image/color"
	"testing"
)

// badgeAt renders drawFaceBadge on its own at radius r, over a white field so
// the plum ring is distinguishable from an unpainted pixel.
func badgeAt(r int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 2*r, 2*r))
	for y := range img.Pix {
		img.Pix[y] = 255
	}
	drawFaceBadge(img, r, r, r)
	return img
}

func countIn(img *image.NRGBA, col color.RGBA) int {
	cr, cg, cb, ca := col.RGBA()
	n := 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			pr, pg, pb, pa := img.NRGBAAt(x, y).RGBA()
			if pr == cr && pg == cg && pb == cb && pa == ca {
				n++
			}
		}
	}
	return n
}

// TestFaceBadgeIsTheLogo pins the colours that only the traced logo uses. The
// old portrait had no twin tails, no open mouth and plum eyes, so each of these
// is a marker that the real geometry is in place:
//
//	colTealShade - the tails' darker fill, and nothing else in the badge
//	colMouth     - the open mouth's interior; the old one used plum
//	colTongue    - the tongue; the old one used the tie pink
//	colInk       - the happy eyes; the old ones were plum too, so they read as
//	               the same colour as every outline
//	colHaiyaPink - the hair ties, the one colour deliberately taken from the
//	               app's own palette rather than the mark's
//
// The tails' sheen is not in this list, and cannot be: in the source drawing it
// too is laid down before the head, so the head all but covers it. The only
// part that escapes is a sliver where the far round cap clears the head's edge -
// 0.505 units wide by 0.021 thick, about 0.006 of a device pixel at badge size.
// The sheen is ported anyway, in case the head's geometry ever moves, but it is
// not worth an assertion.
func TestFaceBadgeIsTheLogo(t *testing.T) {
	img := badgeAt(42)
	for _, c := range []struct {
		col color.RGBA
		n   int
		why string
	}{
		{colTealShade, 60, "twin-tail fill"},
		{colMouth, 20, "open mouth"},
		{colTongue, 8, "tongue"},
		{colInk, 30, "happy eyes"},
		{colHaiyaPink, 100, "hair ties"},
		{colSkin, 500, "face"},
		{colHeader, 500, "hair"},
	} {
		if n := countIn(img, c.col); n < c.n {
			t.Errorf("%s: %d px of the mark's own colour, want >= %d", c.why, n, c.n)
		}
	}
	// The frame must still be a plum ring with bubble-white inside it, since
	// that is what separates the portrait from the teal hero strip.
	if n := countIn(img, colBubbleFill); n < 500 {
		t.Errorf("badge: %d bubble-white px, want >= 500 (the ring's interior)", n)
	}
}

// TestFaceBadgeSymmetric checks that the face half of the badge mirrors about
// its vertical centre line. Every shape there is placed by mirroring about the
// head's centre line, so an off-by-one in that helper is invisible in a
// screenshot but obvious here.
//
// Two things keep this from being a pixel-exact test, and both are properties
// of the drawing and of the rasteriser rather than mistakes in them:
//
//   - Only the lower part of the badge is checked. The mark is deliberately
//     asymmetric above it: the cowlick leans right, and all five fringe tips
//     lean inwards, so the outermost two triangles do not mirror each other even
//     in the source drawing.
//   - A mirrored pair of coordinates can land a pixel apart, because flooring X
//     and flooring (W-X) are not the same integer. The blush is a good example:
//     it fills [17,31] on the left and [53,67] on the right, where a true
//     mirror of an 84-wide badge would be [52,66].
//
// So the test is on the shape of the disagreement rather than its count:
// rounding leaves isolated pixels, while a shape drawn on only one side leaves
// a contiguous run as wide as the shape.
func TestFaceBadgeSymmetric(t *testing.T) {
	const r = 42
	img := badgeAt(r)
	top := 2 * r * 62 / 100 // clear of the fringe tips
	seen, bad, worst := 0, 0, 0
	for y := top; y < 2*r; y++ {
		run := 0
		for x := 0; x < r; x++ {
			l, rr := img.NRGBAAt(x, y), img.NRGBAAt(2*r-1-x, y)
			if l.A == 0 && rr.A == 0 {
				run = 0
				continue
			}
			seen++
			if l == rr {
				run = 0
				continue
			}
			bad++
			if run++; run > worst {
				worst = run
			}
		}
	}
	if seen == 0 {
		t.Fatal("badge drew nothing")
	}
	if pct := 100 * float64(bad) / float64(seen); pct > 3 {
		t.Errorf("%d of %d mirrored px pairs disagree (%.1f%%), want <= 3%%", bad, seen, pct)
	}
	if worst > 3 {
		t.Errorf("longest run of consecutive asymmetric pixels is %d, want <= 3: "+
			"that is what a shape drawn on one side only looks like", worst)
	}
}

// TestFaceBadgeTiePlacement pins one landmark's position, which the symmetry
// test above cannot do. Every shape here is placed through the same `at`
// helper, and moving a shape inwards moves both sides at once, so the badge
// stays perfectly symmetric and a mirror test sees nothing wrong - yet the
// drawing is now wrong. This measures where the hair ties actually land.
//
// The ties are the mark's only pure-pink feature, so their pixels are easy to
// isolate, and the pet puts them 27 units either side of the head's centre.
func TestFaceBadgeTiePlacement(t *testing.T) {
	const r = 42
	img := badgeAt(r)
	cr, cg, cb, ca := colHaiyaPink.RGBA()
	var sx, sy, n [2]int // index 0 = left of centre, 1 = right
	for y := 0; y < 2*r; y++ {
		for x := 0; x < 2*r; x++ {
			pr, pg, pb, pa := img.NRGBAAt(x, y).RGBA()
			if pr != cr || pg != cg || pb != cb || pa != ca {
				continue
			}
			i := 0
			if x >= r {
				i = 1
			}
			sx[i], sy[i], n[i] = sx[i]+x, sy[i]+y, n[i]+1
		}
	}
	if n[0] == 0 || n[1] == 0 {
		t.Fatalf("ties: %d and %d px, want both present", n[0], n[1])
	}
	// The pet puts the ties 27 units either side of the head's centre, and the
	// badge scales the mark by (r-2)/logoFar.
	want := 27.0 * float64(r-2) / logoFar
	for i, side := range []string{"left", "right"} {
		sign := -1.0
		if i == 1 {
			sign = 1
		}
		got, at := float64(sx[i])/float64(n[i]), float64(r)+sign*want
		if d := got - at; d < -1.5 || d > 1.5 {
			t.Errorf("%s tie centred at x=%.1f, want %.1f: off by %+.1fpx", side, got, at, d)
		}
	}
	// They also sit level with each other, which neither the mirror test nor the
	// horizontal check above would notice.
	if d := float64(sy[0])/float64(n[0]) - float64(sy[1])/float64(n[1]); d < -1 || d > 1 {
		t.Errorf("ties are not level: left y=%.1f, right y=%.1f",
			float64(sy[0])/float64(n[0]), float64(sy[1])/float64(n[1]))
	}
}

// TestFaceBadgeScales checks the art is a drawing, not a fixed-size bitmap: the
// ink grows with the square of the radius, and nothing spills outside the
// plum ring at any size.
func TestFaceBadgeScales(t *testing.T) {
	small, big := countIn(badgeAt(16), colHeader), countIn(badgeAt(32), colHeader)
	if big < small*3 {
		t.Errorf("teal px %d at r=32 vs %d at r=16: want roughly 4x, got %.2fx",
			big, small, float64(big)/float64(small))
	}
	for _, r := range []int{12, 20, 28, 36, 42, 64} {
		img := badgeAt(r)
		for y := 0; y < 2*r; y++ {
			for x := 0; x < 2*r; x++ {
				if img.NRGBAAt(x, y).A == 0 {
					t.Fatalf("r=%d: painted pixel at (%d,%d), outside the badge", r, x, y)
				}
			}
		}
	}
}

// TestFaceBadgeTooSmallToDraw keeps the original guard: below about 10px the
// detail turns to mud, so the badge is left empty rather than drawn badly.
func TestFaceBadgeTooSmallToDraw(t *testing.T) {
	img := badgeAt(9)
	if n := countIn(img, colHeader); n != 0 {
		t.Errorf("r=9: drew %d teal px, want none", n)
	}
	// And the boundary case still draws.
	if n := countIn(badgeAt(10), colHeader); n == 0 {
		t.Error("r=10: drew nothing, want the badge")
	}
}
