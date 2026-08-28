package main

// x11draw.go - uploading composed UI frames to the X11 window.

import (
	"image"

	"github.com/jezek/xgb/xproto"
)

func (w *Win) ScreenSize() (int, int) {
	return w.scrW, w.scrH
}

// DrawFrame uploads one composed UI frame at (0,0). img must match the
// window's current size; main keeps them in sync via ConfigureNotify events.
// Chat frames are opaque except the four rounded-shell corners, so the
// frame's own alpha is passed through; it is binary (0 or 255), so no alpha
// premultiplication is needed - just an RGBA -> BGRA channel shuffle.
func (w *Win) DrawFrame(img *image.NRGBA) {
	if w.closed {
		return
	}
	pw, ph := img.Bounds().Dx(), img.Bounds().Dy()
	if pw < 1 || ph < 1 {
		return
	}
	if cap(w.regionBGRA) < pw*ph*4 {
		w.regionBGRA = make([]byte, pw*ph*4)
	}
	bgra := w.regionBGRA[:pw*ph*4]
	pix := img.Pix
	for i := 0; i < pw*ph; i++ {
		off := i * 4
		bgra[off+0] = pix[off+2] // B
		bgra[off+1] = pix[off+1] // G
		bgra[off+2] = pix[off+0] // R
		bgra[off+3] = pix[off+3] // A (0 only in the rounded corners)
	}
	w.putImageZ(pw, ph, 0, 0, bgra)
}

// putImageZ uploads a ZPixmap strip, splitting into chunks that fit one X
// request.
func (w *Win) putImageZ(rw, rh, dx, dy int, data []byte) {
	maxReq := int(xproto.Setup(w.conn).MaximumRequestLength) * 4
	rowsPerChunk := max((maxReq-4096)/(rw*4), 1)
	y := 0
	for y < rh {
		rows := min(rowsPerChunk, rh-y)
		chunk := data[y*rw*4 : (y+rows)*rw*4]
		xproto.PutImage(w.conn, xproto.ImageFormatZPixmap, xproto.Drawable(w.win),
			w.gc, uint16(rw), uint16(rows), int16(dx), int16(dy+y), 0, w.depth, chunk)
		y += rows
	}
}

func (w *Win) Events() <-chan Event { return w.evch }

// Close tears everything down.
func (w *Win) Close() {
	if w.closed {
		return
	}
	w.closed = true
	xproto.DestroyWindow(w.conn, w.win)
	xproto.FreeGC(w.conn, w.gc)
	if w.cmap != w.screen.DefaultColormap {
		xproto.FreeColormap(w.conn, w.cmap)
	}
	w.conn.Close()
}
