package main

// about_art.go - the hand-drawn artwork the About modal shows: the round
// character badge (the pet's face) and the small four-point sparkles that dress
// up the word-art title.
//
// The chat UI is a separate binary and cannot import the pet's character
// package, so the portrait is composed from the same primitives as the rest of
// the interface (fillDisc/fillRect/fillRoundRect in font.go) instead of an
// asset: no image files, nothing to load, and it scales with the badge radius.

import (
	"image"
	"image/color"
)

// Portrait colours. The pet's palette (ui.go) already carries the hair, plum
// outline and bobble pinks; these two are what the face adds on top.
var (
	colSkin  = color.RGBA{255, 228, 206, 255} // chibi face
	colBlush = color.RGBA{246, 143, 168, 110} // translucent cheek blush
)

// drawFaceBadge paints the round character badge: a plum ring around a portrait
// of the pet - teal bob with her pink side bobbles, a zigzag fringe, happy
// closed eyes, blush and a small open smile - i.e. the happy face she rests
// with (see the pet's stateExpr). Everything is derived from r, so the art
// works at any badge size; below ~10px it would be mud, so it draws nothing.
func drawFaceBadge(dst *image.NRGBA, cx, cy, r int) {
	if r < 10 {
		return
	}
	// f converts a fraction of the radius into whole pixels.
	f := func(v float64) int { return int(v*float64(r) + 0.5) }

	// Plum ring with bubble-white inside: a tight frame around the portrait.
	fillDisc(dst, cx, cy, r, colPlum)
	fillDisc(dst, cx, cy, r-2, colBubbleFill)

	// Hair: the crown, a light sheen on its upper left, then the two bob locks
	// that hang past the cheeks.
	fillDisc(dst, cx, cy-f(0.12), f(0.70), colHeader)
	fillDisc(dst, cx-f(0.26), cy-f(0.34), f(0.22), colHairLight)
	fillDisc(dst, cx-f(0.58), cy+f(0.10), f(0.30), colHeader)
	fillDisc(dst, cx+f(0.58), cy+f(0.10), f(0.30), colHeader)

	// Face.
	fillDisc(dst, cx, cy+f(0.20), f(0.50), colSkin)

	// Zigzag fringe: three tapered strands over the forehead.
	for i := -1; i <= 1; i++ {
		fx := cx + f(0.30)*i
		for d := 0; d < f(0.22); d++ {
			w := f(0.19) - d
			if w < 1 {
				break
			}
			fillRect(dst, fx-w/2, cy-f(0.14)+d, w, 1, colHeader)
		}
	}

	// Her signature pink hair bobbles.
	fillDisc(dst, cx-f(0.62), cy-f(0.38), f(0.18), colHaiyaPink)
	fillDisc(dst, cx+f(0.62), cy-f(0.38), f(0.18), colHaiyaPink)

	// Happy closed eyes: a plum disc with a skin disc pushed down over it
	// leaves exactly the upward crescent (^) the pet's happy face is drawn
	// with - smoother than any stroke-by-stroke approximation.
	eye := func(ex int) {
		er := max(3, f(0.17))
		fillDisc(dst, ex, cy+f(0.22), er, colPlum)
		fillDisc(dst, ex, cy+f(0.22)+max(1, er/3), er, colSkin)
	}
	eye(cx - f(0.23))
	eye(cx + f(0.23))

	// Blush, then the small open smile with its tongue.
	fillDisc(dst, cx-f(0.36), cy+f(0.32), f(0.14), colBlush)
	fillDisc(dst, cx+f(0.36), cy+f(0.32), f(0.14), colBlush)
	fillDisc(dst, cx, cy+f(0.46), max(2, f(0.13)), colPlum)
	fillRect(dst, cx-max(1, f(0.06)), cy+f(0.50), max(2, f(0.12)), max(1, f(0.07)), colHaiyaPink)
}

// drawSparkle paints a four-point twinkle with a soft halo and a white core:
// a diamond body plus the two long thin spikes that make it read as a star
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
