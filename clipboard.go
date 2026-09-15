package main

// clipboard.go - X11 clipboard ownership. Copying a message makes this window
// the owner of both the CLIPBOARD (Ctrl+C/Ctrl+V convention) and the PRIMARY
// (middle-click) selections; paste requests arriving from other applications
// are answered from the stored text. No external helper (xclip & co) is
// involved: the window answers SelectionRequest events itself, which is also
// what keeps the offer alive for as long as chat-app runs.

import (
	"fmt"
	"log"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// SetClipboard takes ownership of CLIPBOARD and PRIMARY for text. Ownership
// lasts until the app exits or another application claims the selection
// (SelectionClear then drops the stored text). Returns the error from
// claiming ownership; the payload itself is delivered later, asynchronously,
// per paste request.
func (w *Win) SetClipboard(text string) error {
	w.clipMu.Lock()
	w.clipText = text
	w.clipMu.Unlock()
	for _, sel := range []xproto.Atom{w.atomClipboard, xproto.AtomPrimary} {
		if err := xproto.SetSelectionOwnerChecked(w.conn, w.win, sel,
			xproto.TimeCurrentTime).Check(); err != nil {
			return fmt.Errorf("claim selection %#x: %v", uint32(sel), err)
		}
	}
	return nil
}

// clipClear drops the stored text when ownership is lost (another
// application took the selection).
func (w *Win) clipClear(sel xproto.Atom) {
	w.clipMu.Lock()
	defer w.clipMu.Unlock()
	if sel == w.atomClipboard || sel == xproto.AtomPrimary {
		w.clipText = ""
	}
}

// answerSelection replies to one SelectionRequest: it writes the owned text
// (or the TARGETS list) onto the requestor's property and sends back the
// acknowledging SelectionNotify. Runs on the X event-pump goroutine.
func (w *Win) answerSelection(e xproto.SelectionRequestEvent) {
	prop := e.Property
	if prop == 0 {
		prop = e.Target // ICCCM: None means "store into the target's property"
	}
	w.clipMu.Lock()
	text := w.clipText
	w.clipMu.Unlock()

	var dataType xproto.Atom
	var format byte
	var data []byte
	switch e.Target {
	case w.atomTargets:
		dataType, format, data = xproto.AtomAtom, 32, atomsData([]xproto.Atom{
			w.atomTargets, w.atomUTF8, xproto.AtomString, w.atomText,
		})
	case w.atomUTF8, w.atomText:
		dataType, format, data = w.atomUTF8, 8, []byte(text)
	case xproto.AtomString:
		dataType, format, data = xproto.AtomString, 8, latin1(text)
	default:
		prop = 0 // unsupported target: refuse via an empty property
	}
	if prop != 0 {
		items := uint32(len(data))
		if format == 32 {
			items /= 4
		}
		xproto.ChangeProperty(w.conn, xproto.PropModeReplace, e.Requestor,
			prop, dataType, format, items, data)
	}
	// The acknowledgement is a SelectionNotify (which, unlike the request,
	// has NO Owner field: Time, Requestor, Selection, Target, Property).
	// xgb's own Bytes() serializes it, so the wire layout can't drift from
	// what clients parse (a hand-packed copy once shifted every field by a
	// slot, leaving paste clients waiting on a notify that never parsed).
	notify := xproto.SelectionNotifyEvent{
		Time:      e.Time,
		Requestor: e.Requestor,
		Selection: e.Selection,
		Target:    e.Target,
		Property:  prop,
	}
	xproto.SendEvent(w.conn, false, e.Requestor, 0, string(notify.Bytes()))
}

// atomsData packs atoms into the byte layout of a 32-bit-format property.
func atomsData(as []xproto.Atom) []byte {
	b := make([]byte, 4*len(as))
	for i, a := range as {
		xgb.Put32(b[i*4:], uint32(a))
	}
	return b
}

// latin1 converts UTF-8 text to ISO-8859-1, the STRING target's encoding,
// replacing runes outside Latin-1 with '?'.
func latin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xff {
			out = append(out, byte(r))
		} else {
			out = append(out, '?')
		}
	}
	return out
}

// initClipAtoms interns the selection atoms once at window setup. A failure
// is fatal only in the sense that copies log an error; the chat keeps working.
func (w *Win) initClipAtoms() {
	w.atomClipboard = mustInternAtom(w.conn, "CLIPBOARD")
	w.atomUTF8 = mustInternAtom(w.conn, "UTF8_STRING")
	w.atomTargets = mustInternAtom(w.conn, "TARGETS")
	w.atomText = mustInternAtom(w.conn, "TEXT")
	if w.atomClipboard == 0 || w.atomUTF8 == 0 {
		log.Printf("clipboard: atom setup failed - copying may not work")
	}
}
