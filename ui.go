package main

// ui.go - layout, state and rendering for the chat window.
//
// Like the desktop-pet, the whole interface is composed in software into one
// NRGBA frame per redraw: a header strip, a scrollable message list drawn
// into a clipped layer, and an input bar with the textarea and the SEND
// (submit) button. The palette reuses the pet's colors so both apps feel
// like one family.

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
)

// Widget identifies which UI region a pointer event landed on.
type Widget int

const (
	WNone Widget = iota
	WHeader
	WMessages
	WInput
	WButton
	WClose
)

// Msg is one chat entry.
type Msg struct {
	From  string // "you" or the bot's name
	Text  string
	Image image.Image // optional image to render inside the bubble
}

// Palette - shared with the desktop-pet (plum outlines, pastel teal).
var (
	colHeader      = color.RGBA{95, 207, 214, 255}  // pastel teal (pet hair)
	colTealShade   = color.RGBA{65, 174, 182, 255}  // darker teal
	colHairLight   = color.RGBA{165, 236, 239, 255} // light teal (user bubbles)
	colPlum        = color.RGBA{47, 34, 62, 255}    // deep plum outlines
	colText        = color.RGBA{45, 38, 60, 255}    // bubble text
	colMuted       = color.RGBA{148, 138, 164, 255} // secondary text
	colBg          = color.RGBA{246, 243, 250, 255} // soft lilac-white
	colBubbleFill  = color.RGBA{252, 250, 255, 255} // pet speech-bubble white
	colBtn         = color.RGBA{95, 207, 214, 255}  // SEND button (teal)
	colBtnOff      = color.RGBA{221, 216, 230, 255} // disabled button
	colInputBorder = color.RGBA{216, 210, 226, 255}
	colWhite       = color.RGBA{255, 255, 255, 255}
)

const (
	uiFontScale = 2
	cellW       = advW * uiFontScale         // glyph advance in px (12)
	lineH       = (glyphH + 3) * uiFontScale // text line pitch (20)

	headerH   = 44 // header strip height
	inputH    = 74 // input bar height
	inputPad  = 10
	btnW      = 84
	btnH      = 44
	padX      = 12
	btnGap    = 12
	hdrBtn    = 24 // header close-button square side
	inputRows = 2  // wrapped input lines visible in the textarea

	bubOutline = 2
	bubRadius  = 8
	bubPadX    = 10
	bubPadY    = 7
	bubGap     = 10 // vertical gap between messages
	labelH     = 10 // sender label strip above a bubble
	msgTopPad  = 8  // padding above the first message

	maxInput  = 280 // textarea rune cap
	winRadius = 12  // window shell corner rounding (transparent corners)
)

// UI holds all mutable chat-window state.
type UI struct {
	W, H int

	Bot      *Bot             // the Gemini-powered brain (see chat.go)
	Replies  chan ReplyResult // bot answers + optional image land here
	Thinking bool             // true while a Gemini call is in flight

	msgs   []Msg
	input  []rune
	scroll int // scrollTop in content px (clamped; 0 = oldest visible)

	focused   bool // the textarea owns the keyboard
	caret     bool // caret blink phase
	collapsed bool // true hides the conversation history (prompt-only mode)

	expandedH int // last non-collapsed height; restored when expanding

	hover Widget
	press Widget

	wantClose bool // set by a click on the header's close button
}

// NewUI creates a UI sized w x h with a welcome message from the bot.
// The conversation history starts collapsed so only the prompt box is visible.
func NewUI(w, h int) *UI {
	u := &UI{
		W: w, H: h,
		Bot:       NewBot(),
		Replies:   make(chan ReplyResult, 4),
		focused:   true,
		caret:     true,
		collapsed: true,
		expandedH: max(h, 260),
	}
	u.AddMsg(u.Bot.Name,
		"hi! i am buddy. ask me anything - my answers come from google gemini, and onidia the desktop-pet says them out loud!")
	return u
}

// Geometry -----------------------------------------------------------------

func (u *UI) msgArea() (y, h int) {
	if u.collapsed {
		return headerH, 0
	}
	return headerH, u.H - headerH - inputH
}

func (u *UI) inputRect() image.Rectangle {
	return image.Rect(padX, u.H-inputH+inputPad, u.W-btnW-btnGap-padX, u.H-inputPad)
}

func (u *UI) buttonRect() image.Rectangle {
	top := u.H - inputH + (inputH-btnH)/2
	return image.Rect(u.W-btnW-padX, top, u.W-padX, top+btnH)
}

// closeRect is the little square in the header's far right corner that
// closes the app (the window itself is frameless - see removeDecorations).
func (u *UI) closeRect() image.Rectangle {
	y := (headerH - hdrBtn) / 2
	return image.Rect(u.W-padX-hdrBtn, y, u.W-padX, y+hdrBtn)
}

// HitTest maps window-relative pointer coordinates to a widget.
func (u *UI) HitTest(x, y int) Widget {
	if x < 0 || y < 0 || x >= u.W || y >= u.H {
		return WNone
	}
	if y < headerH {
		cr := u.closeRect()
		if x >= cr.Min.X && x < cr.Max.X && y >= cr.Min.Y && y < cr.Max.Y {
			return WClose
		}
		return WHeader
	}
	if y >= u.H-inputH {
		br := u.buttonRect()
		if x >= br.Min.X && x < br.Max.X && y >= br.Min.Y && y < br.Max.Y {
			return WButton
		}
		return WInput
	}
	if !u.collapsed {
		return WMessages
	}
	return WNone
}

// State changes ------------------------------------------------------------

// Resize updates the window size and re-clamps the scroll position.
func (u *UI) Resize(w, h int) {
	minH := 260
	if u.collapsed {
		minH = headerH + inputH
	}
	u.W, u.H = max(w, 200), max(h, minH)
	if !u.collapsed {
		u.expandedH = u.H // remember the size to restore after collapsing
	}
	u.scroll = clamp(u.scroll, 0, u.maxScroll())
}

// AddMsg appends a text message and sticks the view to the newest entry.
func (u *UI) AddMsg(from, text string) {
	u.msgs = append(u.msgs, Msg{From: from, Text: text})
	u.scroll = u.maxScroll()
}

// AddMsgWithImage appends a message that may include an image.
func (u *UI) AddMsgWithImage(from, text string, img image.Image) {
	u.msgs = append(u.msgs, Msg{From: from, Text: text, Image: img})
	u.scroll = u.maxScroll()
}

// ScrollBy moves the view by dy px (positive shows newer messages).
func (u *UI) ScrollBy(dy int) { u.scroll = clamp(u.scroll+dy, 0, u.maxScroll()) }

// Press records a mouse press on a widget (for the button's pressed look).
func (u *UI) Press(w Widget) { u.press = w }

// Release completes a click; the action fires only when press+release hit
// the same widget. Returns true when the header was clicked (collapse state
// toggled), so the caller can resize the window to match the new size.
func (u *UI) Release(w Widget) bool {
	if w != WNone && w == u.press {
		switch w {
		case WInput:
			u.focused = true
		case WButton:
			u.focused = true
			u.Submit()
		case WClose:
			// The app is frameless, so this button is the way out besides
			// Alt+F4; main() polls WantClose and exits.
			u.wantClose = true
		case WHeader:
			if u.collapsed {
				// Expand: restore the last non-collapsed height.
				u.collapsed = false
				u.H = max(u.expandedH, headerH+inputH)
			} else {
				// Collapse: only the header + prompt box remain.
				u.expandedH = u.H
				u.collapsed = true
				u.H = headerH + inputH
			}
			u.scroll = clamp(u.scroll, 0, u.maxScroll())
			u.press = WNone
			return true
		}
	}
	u.press = WNone
	return false
}

// Collapsed reports whether the conversation history is currently hidden.
func (u *UI) Collapsed() bool { return u.collapsed }

// WantClose reports whether the header's close button was clicked; the main
// loop exits when it is set.
func (u *UI) WantClose() bool { return u.wantClose }

// SetHover updates the hovered widget (drives cursor shape + button tint).
func (u *UI) SetHover(w Widget) { u.hover = w }

// Keysyms the textarea reacts to (X protocol values).
const (
	ksBackspace = 0xff08
	ksReturn    = 0xff0d
	ksEscape    = 0xff1b
	ksKPEnter   = 0xff8d
)

// Key applies one key event; returns true when the UI changed.
func (u *UI) Key(r rune, sym uint32) bool {
	switch sym {
	case ksReturn, ksKPEnter:
		u.Submit()
		return true
	case ksBackspace:
		if n := len(u.input); n > 0 {
			u.input = u.input[:n-1]
			return true
		}
		return false
	case ksEscape:
		if len(u.input) > 0 {
			u.input = nil
			return true
		}
		return false
	}
	if r >= 0x20 && r <= 0x7e && len(u.input) < maxInput {
		u.input = append(u.input, r)
		return true
	}
	return false
}

// Submit sends the current input: the message is appended to the history and
// the bot answers asynchronously (Gemini can take seconds; the UI shows a
// "..." bubble meanwhile and the reply arrives on u.Replies). Empty input is
// a no-op.
func (u *UI) Submit() {
	text := strings.TrimSpace(string(u.input))
	if text == "" {
		return
	}
	u.AddMsg("you", text)
	u.input = nil

	// Snapshot the history for the goroutine: u.msgs keeps growing on the
	// UI goroutine, so the call must not touch it afterwards.
	hist := append([]Msg(nil), u.msgs...)
	u.Thinking = true
	u.scroll = u.maxScroll() // re-pin: the "..." bubble must be visible
	bot := u.Bot
	go func() {
		result := bot.Reply(hist, text)
		u.Replies <- result
	}()
}

// Rendering ----------------------------------------------------------------

// Render composes the entire window into one NRGBA frame. The four shell
// corners are cleared to alpha 0 so the compositor draws the window with
// rounded corners instead of a hard rectangle.
func (u *UI) Render() *image.NRGBA {
	frame := image.NewNRGBA(image.Rect(0, 0, u.W, u.H))
	fillRect(frame, 0, 0, u.W, u.H, colBg)
	u.drawHeader(frame)
	if !u.collapsed {
		u.drawMessages(frame)
	}
	u.drawInputBar(frame)
	roundWindowCorners(frame, winRadius)
	return frame
}

// roundWindowCorners clears (alpha 0) the pixels inside the four r x r corner
// squares that fall outside the rounded-rect silhouette drawRoundRect would
// paint for the full frame - same disc geometry as fillDisc, so the shell's
// curvature matches the bubbles. Only alpha is touched: on a compositor the
// cleared pixels vanish, and on a 24-bit fallback (no ARGB visual) the
// original RGB still shows, i.e. exactly the previous square look.
func roundWindowCorners(img *image.NRGBA, r int) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	r = min(r, w/2, h/2)
	if r <= 0 {
		return
	}
	rr := float64(r)*float64(r) + 0.5    // same inside-test as fillDisc
	corner := func(cx, cy, x0, y0 int) { // disc centre + corner-square origin
		for dy := 0; dy < r; dy++ {
			py := float64(y0+dy-cy) + 0.5
			for dx := 0; dx < r; dx++ {
				px := float64(x0+dx-cx) + 0.5
				if px*px+py*py > rr {
					img.Pix[img.PixOffset(x0+dx, y0+dy)+3] = 0
				}
			}
		}
	}
	corner(r, r, 0, 0)             // top-left
	corner(w-r-1, r, w-r, 0)       // top-right
	corner(r, h-r-1, 0, h-r)       // bottom-left
	corner(w-r-1, h-r-1, w-r, h-r) // bottom-right
}

func (u *UI) drawHeader(frame *image.NRGBA) {
	fillRect(frame, 0, 0, u.W, headerH, colHeader)
	fillRect(frame, 0, headerH-2, u.W, 2, colTealShade)
	drawText(frame, padX, (headerH-glyphH*uiFontScale)/2, "CHAT", uiFontScale, colWhite)

	// Close button: a small square in the far right corner (hover/press
	// tint it like the SEND button).
	cr := u.closeRect()
	fill, glyphCol := colTealShade, colWhite
	switch {
	case u.press == WClose:
		fill, glyphCol = colPlum, colWhite
	case u.hover == WClose:
		fill, glyphCol = colHairLight, colPlum
	}
	drawRoundRect(frame, cr.Min.X, cr.Min.Y, hdrBtn, hdrBtn, 8, colTealShade)
	drawRoundRect(frame, cr.Min.X+2, cr.Min.Y+2, hdrBtn-4, hdrBtn-4, 6, fill)
	drawText(frame, cr.Min.X+(hdrBtn-textWidth("x", 1))/2,
		cr.Min.Y+(hdrBtn-glyphH)/2, "x", 1, glyphCol)

	// Toggle icon: + (collapsed) / - (expanded), left of the close button.
	icon := "+"
	if !u.collapsed {
		icon = "-"
	}
	iconW := textWidth(icon, 1)
	iconX := cr.Min.X - btnGap - iconW
	drawText(frame, iconX, (headerH-glyphH)/2, icon, 1, colWhite)

	sub := "AI HELPER"
	drawText(frame, iconX-padX-textWidth(sub, 1), (headerH-glyphH)/2, sub, 1,
		color.RGBA{255, 255, 255, 190})
}

func (u *UI) drawMessages(frame *image.NRGBA) {
	areaY, areaH := u.msgArea()
	if areaH <= 0 {
		return
	}
	bs := u.blocks()
	contentH := msgTopPad
	for i, b := range bs {
		if i > 0 {
			contentH += bubGap
		}
		contentH += b.h
	}
	scroll := clamp(u.scroll, 0, max(0, contentH-areaH))

	// Draw into a layer clipped exactly to the messages area, then blit.
	layer := image.NewNRGBA(image.Rect(0, 0, u.W, areaH))
	fillRect(layer, 0, 0, u.W, areaH, colBg) // opaque, or Src would punch holes
	y := msgTopPad - scroll                  // layer-relative top of the current block
	for _, b := range bs {
		if y+b.h > 0 && y < areaH {
			u.drawMsgBlock(layer, b, y)
		}
		y += b.h + bubGap
	}
	draw.Draw(frame, image.Rect(0, areaY, u.W, areaY+areaH),
		layer, image.Point{}, draw.Src)
}

// bubbleCols returns the wrap width (glyph cells) for chat bubbles: at most
// 3/4 of the messages area, so both bubbles never touch.
func (u *UI) bubbleCols() int {
	maxW := (u.W - 2*padX) * 3 / 4
	textW := maxW - 2*bubOutline - 2*bubPadX
	return max(10, textW/cellW)
}

const (
	imgGapTop = 4 // gap between label and image
	imgGapBot = 8 // gap between image and bubble
)

type msgBlock struct {
	m     Msg
	lines []string
	bubW  int
	h     int         // total block height including the label strip
	img   image.Image // scaled image to draw above the bubble (may be nil)
}

func (u *UI) blocks() []msgBlock {
	cols := u.bubbleCols()
	maxW := (u.W - 2*padX) * 3 / 4
	bs := make([]msgBlock, 0, len(u.msgs)+1)
	for _, m := range u.msgs {
		bs = append(bs, u.blockFor(m, cols, maxW))
	}
	if u.Thinking {
		// Synthetic "..." bubble while the Gemini call is in flight.
		bs = append(bs, u.blockFor(Msg{From: u.Bot.Name, Text: "..."}, cols, maxW))
	}
	return bs
}

func (u *UI) blockFor(m Msg, cols, maxW int) msgBlock {
	lines := wrapText(m.Text, cols)
	textW := 0
	for _, l := range lines {
		textW = max(textW, textWidth(l, uiFontScale))
	}
	bubW := min(textW+2*bubOutline+2*bubPadX, maxW)
	bubH := len(lines)*lineH + 2*bubPadY

	imgH := 0
	var img image.Image
	if m.Image != nil {
		img = scaleImage(m.Image)
		ib := img.Bounds()
		imgW := ib.Dx()
		imgH = ib.Dy()
		if imgW+2*bubOutline+2*bubPadX > bubW {
			bubW = imgW + 2*bubOutline + 2*bubPadX
		}
		if bubW > maxW {
			bubW = maxW
		}
	}
	h := labelH + bubH
	if imgH > 0 {
		h += imgGapTop + imgH + imgGapBot
	}
	return msgBlock{m: m, lines: lines, bubW: bubW, h: h, img: img}
}

func (u *UI) contentHeight() int {
	h := msgTopPad
	for i, b := range u.blocks() {
		if i > 0 {
			h += bubGap
		}
		h += b.h
	}
	return h
}

func (u *UI) maxScroll() int {
	_, areaH := u.msgArea()
	return max(0, u.contentHeight()-areaH)
}

func (u *UI) drawMsgBlock(layer *image.NRGBA, b msgBlock, y int) {
	isBot := b.m.From != "you"
	imgH := 0
	if b.img != nil {
		imgH = b.img.Bounds().Dy()
	}
	bubH := b.h - labelH
	if imgH > 0 {
		bubH -= imgGapTop + imgH + imgGapBot
	}

	var bx int
	if isBot {
		bx = padX
		drawText(layer, bx, y, strings.ToUpper(b.m.From), 1, colMuted)
	} else {
		bx = u.W - padX - b.bubW
		lw := textWidth(strings.ToUpper(b.m.From), 1)
		drawText(layer, bx+b.bubW-lw, y, strings.ToUpper(b.m.From), 1, colMuted)
	}

	iy := y + labelH + imgGapTop
	if b.img != nil {
		// Draw the image above the bubble, left-aligned with the bubble edge.
		ib := b.img.Bounds()
		imgW := ib.Dx()
		draw.Draw(layer, image.Rect(bx, iy, bx+imgW, iy+ib.Dy()),
			b.img, ib.Min, draw.Over)
	}

	by := y + labelH
	if imgH > 0 {
		by += imgGapTop + imgH + imgGapBot
	}

	outline, fill := colTealShade, colHairLight // user (right)
	if isBot {
		outline, fill = colPlum, colBubbleFill // bot (left): pet speech bubble
	}
	drawRoundRect(layer, bx, by, b.bubW, bubH, bubRadius+bubOutline, outline)
	drawRoundRect(layer, bx+bubOutline, by+bubOutline,
		b.bubW-2*bubOutline, bubH-2*bubOutline, bubRadius, fill)

	tx := bx + bubOutline + bubPadX
	ty := by + bubOutline + bubPadY
	for _, l := range b.lines {
		drawText(layer, tx, ty, l, uiFontScale, colText)
		ty += lineH
	}
}

func (u *UI) drawInputBar(frame *image.NRGBA) {
	fillRect(frame, 0, u.H-inputH, u.W, 1, colInputBorder)

	// Textarea: white body, border turns teal while focused.
	ta := u.inputRect()
	border := color.RGBA(colInputBorder)
	if u.focused {
		border = colHeader
	}
	drawRoundRect(frame, ta.Min.X, ta.Min.Y, ta.Dx(), ta.Dy(), 7, border)
	drawRoundRect(frame, ta.Min.X+2, ta.Min.Y+2, ta.Dx()-4, ta.Dy()-4, 5, colWhite)
	u.drawInputText(frame, ta.Min.X+10, ta.Min.Y+7, ta.Dx()-20)

	u.drawButton(frame)
}

func (u *UI) drawInputText(frame *image.NRGBA, x, y, w int) {
	cols := max(4, w/cellW)
	if len(u.input) == 0 {
		drawText(frame, x, y, "type a message...", uiFontScale, colMuted)
		if u.focused && u.caret {
			fillRect(frame, x, y, 2, glyphH*uiFontScale, colPlum)
		}
		return
	}
	lines := wrapText(string(u.input), cols)
	if len(lines) > inputRows {
		lines = lines[len(lines)-inputRows:] // show the newest input lines
	}
	for _, l := range lines {
		drawText(frame, x, y, l, uiFontScale, colText)
		y += lineH
	}
	if u.focused && u.caret {
		last := lines[len(lines)-1]
		fillRect(frame, x+textWidth(last, uiFontScale), y-lineH, 2,
			glyphH*uiFontScale, colPlum)
	}
}

func (u *UI) drawButton(frame *image.NRGBA) {
	r := u.buttonRect()
	enabled := len(u.input) > 0

	fill, label, outline := colBtn, colWhite, colPlum
	switch {
	case !enabled:
		fill, label, outline = colBtnOff, colMuted, colInputBorder
	case u.press == WButton:
		fill = colTealShade
	case u.hover == WButton:
		fill, label = colHairLight, colPlum
	}
	drawRoundRect(frame, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 9, outline)
	drawRoundRect(frame, r.Min.X+2, r.Min.Y+2, r.Dx()-4, r.Dy()-4, 7, fill)

	const lbl = "SEND"
	dy := 0
	if u.press == WButton && enabled {
		dy = 2 // pressed nudge
	}
	lw := textWidth(lbl, uiFontScale)
	drawText(frame, r.Min.X+(r.Dx()-lw)/2,
		r.Min.Y+(r.Dy()-glyphH*uiFontScale)/2+dy, lbl, uiFontScale, label)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
