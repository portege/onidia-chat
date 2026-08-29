package main

// x11events.go - raw X event pump + keycode->keysym decoding. The decode is
// intentionally minimal (what a text input needs: printable ASCII, Enter,
// Backspace, Escape); exotic layouts and IME support can be added later.

import (
	"log"

	"github.com/jezek/xgb/xproto"
)

type EventType int

const (
	EvQuit EventType = iota
	EvKey
	EvMouse
	EvMotion
	EvScroll
	EvResize
	EvExpose
)

// Event is a user interaction forwarded from the X event pump to the main loop.
type Event struct {
	Type    EventType
	X, Y    int // pointer position, window-relative
	RootX   int // pointer position, root-relative (for _NET_WM_MOVERESIZE)
	RootY   int
	W, H    int // new window size (EvResize)
	N       int // scroll notches: +1 toward newer, -1 toward older
	Button  uint8
	Pressed bool
	Key     rune   // printable rune, 0 for control keys
	Sym     uint32 // X keysym
}

// pumpEvents translates raw X events into our small event vocabulary. It runs
// on its own goroutine and exits when the connection closes.
func (w *Win) pumpEvents() {
	defer close(w.evch)
	send := func(ev Event) {
		select {
		case w.evch <- ev:
		default: // never block the X thread; main drains every iteration
		}
	}
	grabbed := false
	for {
		ev, xerr := w.conn.WaitForEvent()
		if ev == nil && xerr == nil {
			return // connection closed
		}
		if xerr != nil {
			log.Printf("x error: %v", xerr)
			continue
		}
		switch e := ev.(type) {
		case xproto.KeyPressEvent:
			sym := w.keysym(e.Detail, e.State)
			r := rune(0)
			if sym >= 0x20 && sym <= 0x7e {
				r = rune(sym)
			}
			send(Event{Type: EvKey, Key: r, Sym: uint32(sym)})

		case xproto.ButtonPressEvent:
			switch e.Detail {
			case 1: // left: begin a click (grab so releases outside still land)
				if rep, err := xproto.GrabPointer(w.conn, true, w.win,
					xproto.EventMaskButtonRelease|xproto.EventMaskPointerMotion,
					xproto.GrabModeAsync, xproto.GrabModeAsync,
					0, 0, xproto.TimeCurrentTime).Reply(); err == nil &&
					rep != nil && rep.Status == xproto.GrabStatusSuccess {
					grabbed = true
				}
				send(Event{Type: EvMouse, Button: 1, Pressed: true,
					X: int(e.EventX), Y: int(e.EventY),
					RootX: int(e.RootX), RootY: int(e.RootY)})
			case 4: // wheel up -> older messages
				send(Event{Type: EvScroll, N: -1})
			case 5: // wheel down -> newer messages
				send(Event{Type: EvScroll, N: 1})
			}

		case xproto.MotionNotifyEvent:
			send(Event{Type: EvMotion, X: int(e.EventX), Y: int(e.EventY)})

		case xproto.ButtonReleaseEvent:
			if e.Detail == 1 {
				if grabbed {
					xproto.UngrabPointer(w.conn, xproto.TimeCurrentTime)
					grabbed = false
				}
				send(Event{Type: EvMouse, Button: 1, Pressed: false,
					X: int(e.EventX), Y: int(e.EventY)})
			}

		case xproto.ConfigureNotifyEvent:
			if e.Window == w.win {
				send(Event{Type: EvResize, W: int(e.Width), H: int(e.Height)})
			}

		case xproto.ExposeEvent:
			send(Event{Type: EvExpose})

		case xproto.ClientMessageEvent:
			if e.Type == w.atomDeleteWindow && len(e.Data.Data32) > 0 &&
				e.Data.Data32[0] == uint32(w.atomDeleteWindow) {
				send(Event{Type: EvQuit})
			}

		case xproto.DestroyNotifyEvent:
			send(Event{Type: EvQuit})
		}
	}
}

// keysym resolves a keycode + modifier state to an X keysym using the
// keyboard mapping fetched at startup. Handles shift and caps-lock for the
// common two-column layout.
func (w *Win) keysym(kc xproto.Keycode, state uint16) xproto.Keysym {
	if w.km == nil || w.km.KeysymsPerKeycode == 0 {
		return 0
	}
	per := int(w.km.KeysymsPerKeycode)
	idx := int(kc) - int(w.minKC)
	if idx < 0 || (idx+1)*per > len(w.km.Keysyms) {
		return 0
	}
	base := w.km.Keysyms[idx*per]
	shift := state&xproto.ModMaskShift != 0
	caps := state&xproto.ModMaskLock != 0
	col := 0
	if shift && per > 1 {
		col = 1
	}
	alpha := (base >= 'a' && base <= 'z') || (base >= 'A' && base <= 'Z')
	if caps && alpha && per > 1 {
		col = 1 - col
	}
	ks := w.km.Keysyms[idx*per+col]
	if ks == 0 && col != 0 {
		ks = base
	}
	return ks
}
