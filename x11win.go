package main

// x11win.go - one WM-managed X11 window with a 32-bit ARGB visual, rendered
// entirely in software (same approach as the desktop-pet: no cgo, no GUI
// toolkit, just X protocol PutImage uploads). Unlike the pet this is a
// regular managed window: the user can move, resize and close it normally.

import (
	"fmt"
	"log"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// Win wraps the chat window.
type Win struct {
	conn   *xgb.Conn
	screen *xproto.ScreenInfo

	win   xproto.Window
	gc    xproto.Gcontext
	cmap  xproto.Colormap
	depth byte
	scrW  int
	scrH  int

	// Reused upload buffer (no steady-state allocations).
	regionBGRA []byte

	cursorDefault xproto.Cursor // XC_left_ptr
	cursorHand    xproto.Cursor // XC_hand2 over the button
	cursorText    xproto.Cursor // XC_xterm over the textarea

	atomDeleteWindow xproto.Atom
	atomMoveResize   xproto.Atom // _NET_WM_MOVERESIZE (frameless header drag)

	// Logical window size (tracks the WM's ConfigureNotify requests).
	winW, winH int

	// Keyboard mapping for keycode -> keysym decoding.
	minKC xproto.Keycode
	maxKC xproto.Keycode
	km    *xproto.GetKeyboardMappingReply

	closed bool

	evch chan Event
}

// Open creates the chat window and maps it.
func Open(w, h int) (*Win, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("cannot open X display (are you running Xorg/XWayland?): %w", err)
	}
	scr := xproto.Setup(conn).DefaultScreen(conn)
	win := &Win{conn: conn, screen: scr,
		scrW: int(scr.WidthInPixels), scrH: int(scr.HeightInPixels)}

	// Pick a 32-bit TrueColor (ARGB) visual if one is advertised.
	vid, depth := scr.RootVisual, scr.RootDepth
	for _, d := range scr.AllowedDepths {
		if d.Depth != 32 {
			continue
		}
		for _, v := range d.Visuals {
			if v.RedMask == 0xff0000 && v.GreenMask == 0xff00 && v.BlueMask == 0xff {
				vid, depth = v.VisualId, 32
			}
		}
	}
	win.depth = depth
	win.winW, win.winH = w, h

	if vid != scr.RootVisual {
		win.cmap, err = xproto.NewColormapId(conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("colormap id: %w", err)
		}
		if err := xproto.CreateColormapChecked(conn, xproto.ColormapAllocNone, win.cmap, scr.Root, vid).Check(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("create colormap: %w", err)
		}
	} else {
		win.cmap = scr.DefaultColormap
	}

	win.win, err = xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("window id: %w", err)
	}
	// X requires value-list entries in strictly ascending order of their Cw
	// bit: BackPixel(2) < BorderPixel(8) < EventMask(2048) < Colormap(8192).
	eventMask := uint32(xproto.EventMaskKeyPress |
		xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease |
		xproto.EventMaskPointerMotion | xproto.EventMaskExposure |
		xproto.EventMaskStructureNotify)
	vals := []uint32{
		0xf6f3fa, // CwBackPixel -> colBg (visible only before the first frame)
		0,        // CwBorderPixel
		eventMask,
		uint32(win.cmap),
	}
	maskv := xproto.CwBackPixel | xproto.CwBorderPixel | xproto.CwEventMask | xproto.CwColormap
	if err := xproto.CreateWindowChecked(conn, depth, win.win, scr.Root,
		0, 0, uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, vid, uint32(maskv), vals).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create window: %w", err)
	}

	// Graphics context for PutImage uploads.
	win.gc, err = xproto.NewGcontextId(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("gc id: %w", err)
	}
	if err := xproto.CreateGCChecked(conn, win.gc, xproto.Drawable(win.win),
		xproto.GcGraphicsExposures, []uint32{0}).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create gc: %w", err)
	}

	win.setupHints()
	win.loadKeymap()

	win.cursorDefault = win.glyphCursor(68) // XC_left_ptr
	win.cursorHand = win.glyphCursor(60)    // XC_hand2
	win.cursorText = win.glyphCursor(152)   // XC_xterm
	xproto.ChangeWindowAttributes(conn, win.win, xproto.CwCursor,
		[]uint32{uint32(win.cursorDefault)})

	if err := xproto.MapWindowChecked(conn, win.win).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("map window: %w", err)
	}

	win.evch = make(chan Event, 128)
	go win.pumpEvents()

	log.Printf("open: %dx%d window, screen %dx%d depth=%d", w, h, win.scrW, win.scrH, depth)
	return win, nil
}

// setupHints gives the window a title and a close button.
func (w *Win) setupHints() {
	utf8 := mustInternAtom(w.conn, "UTF8_STRING")
	w.atomDeleteWindow = mustInternAtom(w.conn, "WM_DELETE_WINDOW")
	w.atomMoveResize = mustInternAtom(w.conn, "_NET_WM_MOVERESIZE")
	w.setStrProp(mustInternAtom(w.conn, "WM_NAME"), utf8, "chat - ai-helper")
	w.setStrProp(mustInternAtom(w.conn, "_NET_WM_NAME"), utf8, "chat - ai-helper")
	w.setStrProp(mustInternAtom(w.conn, "WM_CLASS"), xproto.AtomString, "chat-app\x00ChatApp\x00")
	setAtomProp(w.conn, w.win, mustInternAtom(w.conn, "WM_PROTOCOLS"),
		xproto.AtomAtom, []xproto.Atom{w.atomDeleteWindow})
	// Frameless: without this the WM's own titlebar/border squares off the
	// rounded ARGB shell.
	w.removeDecorations()
}

// removeDecorations sets _MOTIF_WM_HINTS (honoured by every mainstream WM:
// Mutter, Muffin, Marco, KWin, xfwm4, Openbox, ...) to keep move/resize/
// close functions but zero decorations, so the compositor shows our rounded
// corners instead of wrapping them in a rectangular frame. Closing still
// works via WM_DELETE_WINDOW (Alt+F4) and the header's own close button.
func (w *Win) removeDecorations() {
	atom := mustInternAtom(w.conn, "_MOTIF_WM_HINTS")
	const ( // MwmHints bits
		mwmHintsFunctions   = 1 << 0
		mwmHintsDecorations = 1 << 1
		mwmFuncResize       = 1 << 1
		mwmFuncMove         = 1 << 2
		mwmFuncClose        = 1 << 5
	)
	fields := []uint32{
		mwmHintsFunctions | mwmHintsDecorations, // which fields are valid
		mwmFuncResize | mwmFuncMove | mwmFuncClose,
		0, // decorations: none
		0, 0,
	}
	buf := make([]byte, 4*len(fields))
	for i, v := range fields {
		xgb.Put32(buf[i*4:], v)
	}
	xproto.ChangeProperty(w.conn, xproto.PropModeReplace, w.win, atom, atom,
		32, uint32(len(fields)), buf)
}

// loadKeymap fetches the server keyboard mapping once; keysym() uses it to
// decode KeyPress events.
func (w *Win) loadKeymap() {
	setup := xproto.Setup(w.conn)
	w.minKC, w.maxKC = setup.MinKeycode, setup.MaxKeycode
	km, err := xproto.GetKeyboardMapping(w.conn, setup.MinKeycode,
		byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		log.Printf("warning: keyboard mapping unavailable: %v", err)
		return
	}
	w.km = km
}

// windowSize returns the current logical window size (updated via
// ConfigureNotify by main).
func (w *Win) windowSize() (int, int) { return w.winW, w.winH }

// Resize requests a new window size from the window manager.
func (w *Win) Resize(width, height int) {
	if w.closed {
		return
	}
	xproto.ConfigureWindow(w.conn, w.win,
		xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{uint32(width), uint32(height)})
}

// StartMove hands control to the window manager so it can drag the window
// (the window is frameless, so the header is our only drag handle). rootX/Y
// are the pointer position at button-press. The WM grabs the pointer itself,
// so our own button-grab is released first or the WM's grab cannot arm.
func (w *Win) StartMove(rootX, rootY int) {
	if w.closed {
		return
	}
	xproto.UngrabPointer(w.conn, xproto.TimeCurrentTime)
	const netWMMoveResizeMove = 8 // _NET_WM_MOVERESIZE_MOVE
	u := xproto.ClientMessageDataUnionData32New([]uint32{
		uint32(rootX), uint32(rootY),
		netWMMoveResizeMove, 1, 1, // direction=move, button=left, source=application
	})
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: w.win,
		Type:   w.atomMoveResize,
		Data:   u,
	}
	b := ev.Bytes()
	if len(b) < 32 {
		buf := make([]byte, 32)
		copy(buf, b)
		b = buf
	}
	xproto.SendEvent(w.conn, false, w.screen.Root,
		xproto.EventMaskSubstructureNotify|xproto.EventMaskSubstructureRedirect, string(b))
	log.Printf("drag: header grabbed, _NET_WM_MOVERESIZE (%d,%d)", rootX, rootY)
}

// SetCursor swaps the pointer shape (text caret / hand / default).
func (w *Win) SetCursor(c xproto.Cursor) {
	if w.closed {
		return
	}
	xproto.ChangeWindowAttributes(w.conn, w.win, xproto.CwCursor, []uint32{uint32(c)})
}

func mustInternAtom(conn *xgb.Conn, name string) xproto.Atom {
	r, err := xproto.InternAtom(conn, true, uint16(len(name)), name).Reply()
	if err != nil || r.Atom == 0 {
		r2, err2 := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err2 != nil || r2.Atom == 0 {
			log.Fatalf("intern atom %q: %v %v", name, err, err2)
		}
		return r2.Atom
	}
	return r.Atom
}

func (w *Win) setStrProp(prop xproto.Atom, typ xproto.Atom, s string) {
	xproto.ChangeProperty(w.conn, xproto.PropModeReplace, w.win, prop, typ, 8,
		uint32(len(s)), []byte(s))
}

func setAtomProp(conn *xgb.Conn, win xproto.Window, prop, typ xproto.Atom, atoms []xproto.Atom) {
	buf := make([]byte, 4*len(atoms))
	for i, at := range atoms {
		xgb.Put32(buf[i*4:], uint32(at))
	}
	xproto.ChangeProperty(conn, xproto.PropModeReplace, win, prop, typ, 32,
		uint32(len(atoms)), buf)
}

func (w *Win) glyphCursor(glyph uint16) xproto.Cursor {
	fid, err := xproto.NewFontId(w.conn)
	if err != nil {
		log.Printf("warning: new font id: %v", err)
		return 0
	}
	if err := xproto.OpenFontChecked(w.conn, fid, uint16(len("cursor")), "cursor").Check(); err != nil {
		log.Printf("warning: cannot load cursor font: %v", err)
		return 0
	}
	cid, err := xproto.NewCursorId(w.conn)
	if err != nil {
		log.Printf("warning: new cursor id: %v", err)
		xproto.CloseFont(w.conn, fid)
		return 0
	}
	if err := xproto.CreateGlyphCursorChecked(w.conn, cid, fid, fid, glyph, glyph+1,
		0, 0, 0, 0xffff, 0xffff, 0xffff).Check(); err != nil {
		log.Printf("warning: cannot create cursor: %v", err)
		xproto.CloseFont(w.conn, fid)
		return 0
	}
	xproto.CloseFont(w.conn, fid)
	return cid
}
