package main

// ui.go - layout, state and rendering for the chat window.
//
// Like the desktop-pet, the whole interface is composed in software into one
// NRGBA frame per redraw: a header strip, a scrollable message list drawn
// into a clipped layer, and an input bar with the textarea and the SEND
// (submit) button. The palette reuses the pet's colors so both apps feel
// like one family.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strconv"
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

	// Settings modal (see drawSettings): the header's gear button plus the
	// widgets that live inside the modal.
	WSettings // gear button in the header
	WModal    // dim backdrop around the panel (absorbs outside clicks)
	WName     // character-name text field
	WDrop     // character-age dropdown box
	WDropFrom // sleep-time FROM dropdown box
	WDropTo   // sleep-time TO dropdown box
	WOption   // one row of an open dropdown list
	WSave     // modal SAVE button
	WCancel   // modal CANCEL button
	WPagePrev // prev page in a paginated chat bubble
	WPageNext // next page in a paginated chat bubble
)

// Msg is one chat entry.
type Msg struct {
	From  string // "you" or the bot's name
	Text  string
	Image image.Image // optional image to render inside the bubble
	Pages []string    // paragraphs; >1 turns the bubble into a pager (see Page)
	Page  int         // current page index into Pages (0 = first)
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
	colError       = color.RGBA{196, 60, 74, 255} // save-failure text in the modal
)

const (
	uiFontScale = 2
	cellW       = advW * uiFontScale         // glyph advance in px (12)
	lineH       = (glyphH + 3) * uiFontScale // text line pitch (24): generous
	// enough that descender tails (g, y, p, q) never touch the next line

	headerH = 44  // header strip height
	inputH  = 108 // input bar height: 3 lineH rows of text plus headroom so
	// descender tails (g, y, p, q) stay well clear of the textarea bottom
	inputPad  = 10
	btnW      = 84
	btnH      = 44
	padX      = 12
	btnGap    = 12
	hdrBtn    = 24 // header close-button square side
	inputRows = 3  // wrapped input lines visible in the textarea

	bubOutline = 2
	bubRadius  = 8
	bubPadX    = 10
	bubPadY    = 7
	bubGap     = 10 // vertical gap between messages
	labelH     = 10 // sender label strip above a bubble
	msgTopPad  = 8  // padding above the first message

	// Pagination: a reply with 2+ paragraphs becomes a paged bubble.
	pagStrip = 20 // height of the pager strip at the bubble's foot
	pagBtn   = 14 // prev/next page button side

	maxInput  = 280 // textarea rune cap
	winRadius = 12  // window shell corner rounding (transparent corners)

	// Settings modal layout (drawSettings).
	modalPad     = 20  // panel inner padding
	modalBtnW    = 90  // SAVE / CANCEL button width
	panelW       = 300 // modal panel width (clamped to the window)
	panelH       = 340 // modal panel height (name + age + sleep rows + buttons)
	dropH        = 32  // dropdown box height
	optH         = 24  // dropdown list row height
	minSettingsH = 440 // window height forced while the modal is open

	maxNameChars = 16 // character-name field rune cap

	minCharAge          = 7  // youngest character age in the dropdown
	maxCharAge          = 13 // oldest character age in the dropdown
	defaultCharacterAge = 10 // pre-selected age when none is configured

	numHours         = 24 // entries in a sleep-time dropdown (0:00 .. 23:00)
	visibleHourRows  = 5  // hour-list rows shown before it scrolls
	defaultSleepFrom = 22 // pre-selected sleep start when none is configured
	defaultSleepTo   = 7  // pre-selected sleep end when none is configured
)

// numAges is how many entries the age dropdown shows.
const numAges = maxCharAge - minCharAge + 1

// Which dropdown list is currently expanded (UI.openDrop).
const (
	dropNone = iota
	dropAge
	dropFrom
	dropTo
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

	// Settings modal state (see drawSettings). name / age / sleepFrom /
	// sleepTo are the committed values: unset until the first save, or
	// loaded from the config (age 0 = unset; sleep uses -1 because hour 0
	// is valid; name "" = unset).
	settingsOpen   bool
	nameFocused    bool   // the name field owns the keyboard
	nameDraft      []rune // name typed in the modal; committed on SAVE
	openDrop       int    // which dropdown list is expanded (dropNone/dropAge/...)
	hourScroll     int    // first visible row of the open hour list
	ageDraft       int    // age picked in the modal; committed on SAVE
	sleepFromDraft int    // sleep start hour picked in the modal
	sleepToDraft   int    // sleep end hour picked in the modal
	wasCollapsed   bool   // collapse state when the modal opened
	prevH          int    // window height before the modal forced minSettingsH
	name           string // committed character name ("" = not set yet)
	age            int    // committed character age (0 = not set yet)
	sleepFrom      int    // committed sleep-window start hour (-1 = unset)
	sleepTo        int    // committed sleep-window end hour (-1 = unset)
	savePath       string // INI file settings are written to ("" = ./chat-app.ini)
	saveErr        string // last save error, shown inside the modal
	optIdx         int    // dropdown row under the pointer (set by HitTest)
	pagerMsg       int    // msg index under the pointer in a paginated bubble (-1 = none)
	pagerDir       int    // -1 prev / +1 next, from the last pager hit-test
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
		sleepFrom: -1, // -1 = no sleep window configured yet
		sleepTo:   -1,
		pagerMsg:  -1, // no pager under the pointer yet
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

// settingsRect is the gear button in the header, left of the close button.
func (u *UI) settingsRect() image.Rectangle {
	x1 := u.closeRect().Min.X - btnGap
	y := (headerH - hdrBtn) / 2
	return image.Rect(x1-hdrBtn, y, x1, y+hdrBtn)
}

// modalPanel is the centred settings dialog rectangle.
func (u *UI) modalPanel() image.Rectangle {
	pw := min(panelW, u.W-2*modalPad)
	return image.Rect((u.W-pw)/2, (u.H-panelH)/2, (u.W+pw)/2, (u.H+panelH)/2)
}

// nameRect is the character-name text field, the first row of the panel.
func (u *UI) nameRect() image.Rectangle {
	p := u.modalPanel()
	return image.Rect(p.Min.X+modalPad, p.Min.Y+66, p.Max.X-modalPad, p.Min.Y+66+dropH)
}

// dropRect is the character-age dropdown box inside the panel.
func (u *UI) dropRect() image.Rectangle {
	p := u.modalPanel()
	return image.Rect(p.Min.X+modalPad, p.Min.Y+126, p.Max.X-modalPad, p.Min.Y+126+dropH)
}

// sleepFromRect / sleepToRect are the two half-width sleep-time dropdowns.
func (u *UI) sleepFromRect() image.Rectangle {
	p := u.modalPanel()
	w := (p.Dx() - 2*modalPad - 12) / 2
	y := p.Min.Y + 200
	return image.Rect(p.Min.X+modalPad, y, p.Min.X+modalPad+w, y+dropH)
}

func (u *UI) sleepToRect() image.Rectangle {
	f := u.sleepFromRect()
	dx := f.Dx() + 12
	return image.Rect(f.Min.X+dx, f.Min.Y, f.Max.X+dx, f.Max.Y)
}

// dropListRect is the expanded age list; empty unless the age list is open.
func (u *UI) dropListRect() image.Rectangle {
	if u.openDrop != dropAge {
		return image.Rectangle{}
	}
	d := u.dropRect()
	return image.Rect(d.Min.X, d.Max.Y+4, d.Max.X, d.Max.Y+4+numAges*optH)
}

// openHourBox is the FROM/TO box whose list is open; empty when none is.
func (u *UI) openHourBox() image.Rectangle {
	switch u.openDrop {
	case dropFrom:
		return u.sleepFromRect()
	case dropTo:
		return u.sleepToRect()
	}
	return image.Rectangle{}
}

// hourListRect is the expanded hour list: five visible rows below its box;
// the remaining hours are reached by scrolling. Empty when no hour list open.
func (u *UI) hourListRect() image.Rectangle {
	box := u.openHourBox()
	if box == (image.Rectangle{}) {
		return image.Rectangle{}
	}
	return image.Rect(box.Min.X, box.Max.Y+4, box.Max.X, box.Max.Y+4+visibleHourRows*optH)
}

// modalButtons returns the CANCEL and SAVE button rectangles.
func (u *UI) modalButtons() (cancel, save image.Rectangle) {
	p := u.modalPanel()
	total := 2*modalBtnW + btnGap
	sx := p.Min.X + (p.Dx()-total)/2
	by := p.Max.Y - 14 - btnH
	return image.Rect(sx, by, sx+modalBtnW, by+btnH),
		image.Rect(sx+modalBtnW+btnGap, by, sx+total, by+btnH)
}

// inRect reports whether the point is inside r.
func inRect(x, y int, r image.Rectangle) bool {
	return x >= r.Min.X && x < r.Max.X && y >= r.Min.Y && y < r.Max.Y
}

// HitTest maps window-relative pointer coordinates to a widget.
func (u *UI) HitTest(x, y int) Widget {
	if x < 0 || y < 0 || x >= u.W || y >= u.H {
		return WNone
	}
	if u.settingsOpen {
		// The modal owns the whole window while it is open. An open
		// dropdown list is an overlay: it wins over the widgets it covers.
		switch u.openDrop {
		case dropAge:
			if l := u.dropListRect(); inRect(x, y, l) {
				u.optIdx = clamp((y-l.Min.Y)/optH, 0, numAges-1)
				return WOption
			}
		case dropFrom, dropTo:
			if l := u.hourListRect(); inRect(x, y, l) {
				u.optIdx = clamp(u.hourScroll+(y-l.Min.Y)/optH, 0, numHours-1)
				return WOption
			}
		}
		if cancel, save := u.modalButtons(); inRect(x, y, save) {
			return WSave
		} else if inRect(x, y, cancel) {
			return WCancel
		}
		if r := u.nameRect(); inRect(x, y, r) {
			return WName
		}
		if d := u.dropRect(); inRect(x, y, d) {
			return WDrop
		}
		if r := u.sleepFromRect(); inRect(x, y, r) {
			return WDropFrom
		}
		if r := u.sleepToRect(); inRect(x, y, r) {
			return WDropTo
		}
		return WModal
	}
	if y < headerH {
		if inRect(x, y, u.closeRect()) {
			return WClose
		}
		if inRect(x, y, u.settingsRect()) {
			return WSettings
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
		if w := u.pagerAt(x, y); w != WNone {
			return w
		}
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
	if u.settingsOpen {
		u.H = max(u.H, minSettingsH) // the modal needs the taller window
	}
	if !u.collapsed {
		u.expandedH = u.H // remember the size to restore after collapsing
	}
	u.scroll = clamp(u.scroll, 0, u.maxScroll())
}

// AddMsg appends a text message and sticks the view to the newest entry.
func (u *UI) AddMsg(from, text string) {
	u.msgs = append(u.msgs, newMsg(from, text, nil))
	u.scroll = u.maxScroll()
}

// AddMsgWithImage appends a message that may include an image.
func (u *UI) AddMsgWithImage(from, text string, img image.Image) {
	u.msgs = append(u.msgs, newMsg(from, text, img))
	u.scroll = u.maxScroll()
}

// pagerAt checks whether (x,y) window coordinates fall on a paginated
// bubble's prev/next page button. When they do it records the target msg
// index (u.pagerMsg) and direction (u.pagerDir) and returns the widget.
func (u *UI) pagerAt(x, y int) Widget {
	areaY, areaH := u.msgArea()
	if areaH <= 0 {
		return WNone
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
	ly := y - areaY // layer-relative Y (the pager rects live in the msg layer)
	ty := msgTopPad - scroll
	for mi, b := range bs {
		if ty+b.h > 0 && ty < areaH && b.paginated {
			bx := padX
			if b.m.From == "you" {
				bx = u.W - padX - b.bubW
			}
			by := ty + labelH
			if b.img != nil {
				by += imgGapTop + b.img.Bounds().Dy() + imgGapBot
			}
			prev, next, _, _ := pagerRects(b, bx, by)
			if inRect(x, ly, next) && b.m.Page < b.pageCount-1 {
				u.pagerMsg, u.pagerDir = mi, +1
				return WPageNext
			}
			if inRect(x, ly, prev) && b.m.Page > 0 {
				u.pagerMsg, u.pagerDir = mi, -1
				return WPagePrev
			}
		}
		ty += b.h + bubGap
	}
	return WNone
}

// flipPage flips the last pager-hit bubble one page in the recorded direction,
// clamping at the firstand last page. The bubble resizes per page; drawMessages
// re-clamps the scroll on the next frame.

func (u *UI) flipPage() {
	if u.pagerMsg < 0 || u.pagerMsg >= len(u.msgs) {
		return
	}
	m := &u.msgs[u.pagerMsg]
	if len(m.Pages) <= 1 {
		return
	}
	np := m.Page + u.pagerDir
	if np < 0 || np >= len(m.Pages) {
		return
	}
	m.Page = np
}

// newMsg builds a chat entry, paginating the reply when it holds more
// than one paragraph (see splitPages): then the bubble shows one page at a
// time with a pager strip in its foot. The LLM separates paragraphs with
// newlines (see newlineToPageBreak) which become \f page breaks here.
func newMsg(from, text string, img image.Image) Msg {
	m := Msg{From: from, Text: text, Image: img}
	if ps := splitPages(newlineToPageBreak(text)); len(ps) > 1 {
		m.Pages = ps
	}
	return m
}

// splitPages splits text on \f (form feed) into trimmed, non-empty pages.
// A reply with 2+ pages becomes a paged bubble; a single page stays one
// page (Pages nil).
func splitPages(text string) []string {
	var ps []string
	for _, page := range strings.Split(text, "\f") {
		if t := strings.TrimSpace(page); t != "" {
			ps = append(ps, t)
		}
	}
	return ps
}

// ScrollBy moves the view by dy px (positive shows newer messages).
func (u *UI) ScrollBy(dy int) { u.scroll = clamp(u.scroll+dy, 0, u.maxScroll()) }

// Press records a mouse press on a widget (for the button's pressed look).
func (u *UI) Press(w Widget) { u.press = w }

// Release completes a click; the action fires only when press+release hit
// the same widget. Returns true when the header was clicked (collapse state
// toggled), so the caller can resize the window to match the new size.
func (u *UI) Release(w Widget) bool {
	if u.settingsOpen {
		// Clicking anywhere else in the modal moves focus off the name
		// field; only a click on the field itself keeps it.
		u.nameFocused = w == WName
	}
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
		case WSettings:
			if u.openSettings() {
				return true // the window grew to fit the modal
			}
		case WName:
			// Focus already moved above; typing now edits the name draft.
		case WDrop:
			u.toggleDrop(dropAge)
		case WDropFrom:
			u.toggleDrop(dropFrom)
		case WDropTo:
			u.toggleDrop(dropTo)
		case WOption:
			switch u.openDrop {
			case dropAge:
				if u.optIdx >= 0 && u.optIdx < numAges {
					u.ageDraft = minCharAge + u.optIdx
				}
			case dropFrom:
				if u.optIdx >= 0 && u.optIdx < numHours {
					u.sleepFromDraft = u.optIdx
				}
			case dropTo:
				if u.optIdx >= 0 && u.optIdx < numHours {
					u.sleepToDraft = u.optIdx
				}
			}
			u.openDrop = dropNone
		case WSave:
			u.saveSettings()
			if u.saveErr == "" {
				u.press = WNone
				return u.closeSettings()
			}
			// Save failed: the error is shown in the modal, which stays open.
		case WCancel:
			u.press = WNone
			return u.closeSettings()
		case WPagePrev, WPageNext:
			u.flipPage()
		case WModal:
			u.openDrop = dropNone // a click outside the widgets closes the list
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

// openSettings shows the settings modal. The conversation window is expanded
// (and grown if needed) so the panel and its dropdown fit; returns true when
// the window size changed, so the caller must resize.
func (u *UI) openSettings() bool {
	u.settingsOpen = true
	u.openDrop = dropNone
	u.hourScroll = 0
	u.saveErr = ""
	u.nameFocused = false
	u.nameDraft = []rune(u.name)
	u.ageDraft = u.age
	if u.ageDraft <= 0 {
		u.ageDraft = defaultCharacterAge
	}
	u.sleepFromDraft = u.sleepFrom
	if u.sleepFromDraft < 0 {
		u.sleepFromDraft = defaultSleepFrom
	}
	u.sleepToDraft = u.sleepTo
	if u.sleepToDraft < 0 {
		u.sleepToDraft = defaultSleepTo
	}
	u.wasCollapsed = u.collapsed
	u.hover, u.press = WNone, WNone
	changed := false
	if u.collapsed {
		u.collapsed = false
		u.H = max(u.expandedH, minSettingsH)
		changed = true
	}
	if u.H < minSettingsH {
		u.prevH = u.H
		u.H = minSettingsH
		changed = true
	}
	u.scroll = clamp(u.scroll, 0, u.maxScroll())
	return changed
}

// closeSettings hides the modal and restores the collapse state and window
// height from before it opened. Returns true when the window must be resized.
func (u *UI) closeSettings() bool {
	u.settingsOpen = false
	u.openDrop = dropNone
	u.nameFocused = false
	u.hover, u.press = WNone, WNone
	resized := false
	if u.wasCollapsed && !u.collapsed {
		u.collapsed = true
		u.H = headerH + inputH
		resized = true
	} else if u.prevH > 0 && !u.collapsed {
		u.H = u.prevH
		resized = true
	}
	u.prevH = 0
	return resized
}

// toggleDrop opens the given dropdown list, closing any other. Opening an
// hour list scrolls the selection into view.
func (u *UI) toggleDrop(which int) {
	if u.openDrop == which {
		u.openDrop = dropNone
		return
	}
	u.openDrop = which
	if which == dropFrom || which == dropTo {
		sel := u.sleepFromDraft
		if which == dropTo {
			sel = u.sleepToDraft
		}
		u.hourScroll = clamp(sel-2, 0, numHours-visibleHourRows)
	}
}

// ScrollHourList scrolls the open FROM/TO hour list; returns false when no
// hour list is open, so the caller scrolls the message history instead.
func (u *UI) ScrollHourList(dy int) bool {
	if !u.settingsOpen || (u.openDrop != dropFrom && u.openDrop != dropTo) {
		return false
	}
	u.hourScroll = clamp(u.hourScroll+dy, 0, numHours-visibleHourRows)
	if u.hover == WOption {
		// Keep the highlighted row inside the scrolled viewport.
		u.optIdx = clamp(u.optIdx, u.hourScroll, u.hourScroll+visibleHourRows-1)
	}
	return true
}

// saveSettings commits the modal's drafts: the persona picks them up live
// and character-age / sleep-time are rewritten in the INI file. Failures are
// reported in the modal, which then stays open.
func (u *UI) saveSettings() {
	path := u.savePath
	if path == "" {
		path = "chat-app.ini"
	}
	name := strings.TrimSpace(string(u.nameDraft))
	if err := SetConfigValue(path, "character", "character-name", name); err != nil {
		u.saveErr = err.Error()
		return
	}
	if err := SetConfigValue(path, "character", "character-age", strconv.Itoa(u.ageDraft)); err != nil {
		u.saveErr = err.Error()
		return
	}
	sleep := fmt.Sprintf("%02d:00-%02d:00", u.sleepFromDraft, u.sleepToDraft)
	if err := SetConfigValue(path, "character", "sleep-time", sleep); err != nil {
		u.saveErr = err.Error()
		return
	}
	u.saveErr = ""
	u.name = name
	u.age = u.ageDraft
	u.sleepFrom, u.sleepTo = u.sleepFromDraft, u.sleepToDraft
	if u.Bot != nil {
		if name != "" {
			u.Bot.Name = name // bubble sender label
		}
		u.Bot.CharacterName = name
		u.Bot.CharacterAge = u.ageDraft
		u.Bot.SleepSet = true
		u.Bot.SleepFrom, u.Bot.SleepTo = u.sleepFromDraft, u.sleepToDraft
	}
}

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
	if u.settingsOpen {
		// While the modal is up the textarea is dormant: Enter saves,
		// Escape cancels, and typing edits the name field (when focused).
		switch sym {
		case ksEscape:
			u.closeSettings()
			return true
		case ksReturn, ksKPEnter:
			u.saveSettings()
			if u.saveErr == "" {
				u.closeSettings()
			}
			return true
		case ksBackspace:
			if u.nameFocused && len(u.nameDraft) > 0 {
				u.nameDraft = u.nameDraft[:len(u.nameDraft)-1]
				return true
			}
			return false
		}
		if u.nameFocused && r >= 0x20 && r <= 0x7e && len(u.nameDraft) < maxNameChars {
			u.nameDraft = append(u.nameDraft, r)
			return true
		}
		return false
	}
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
	if u.settingsOpen {
		u.drawSettings(frame)
	}
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

	// Settings gear: a small square button left of the close button.
	sr := u.settingsRect()
	switch {
	case u.press == WSettings:
		fill, glyphCol = colPlum, colWhite
	case u.hover == WSettings:
		fill, glyphCol = colHairLight, colPlum
	default:
		fill, glyphCol = colTealShade, colWhite
	}
	drawRoundRect(frame, sr.Min.X, sr.Min.Y, hdrBtn, hdrBtn, 8, colTealShade)
	drawRoundRect(frame, sr.Min.X+2, sr.Min.Y+2, hdrBtn-4, hdrBtn-4, 6, fill)
	drawGear(frame, sr.Min.X+hdrBtn/2, sr.Min.Y+hdrBtn/2, glyphCol, fill)

	// Toggle icon: + (collapsed) / - (expanded), left of the gear button.
	icon := "+"
	if !u.collapsed {
		icon = "-"
	}
	iconW := textWidth(icon, 1)
	iconX := sr.Min.X - btnGap - iconW
	drawText(frame, iconX, (headerH-glyphH)/2, icon, 1, colWhite)

	sub := "AI HELPER"
	drawText(frame, iconX-padX-textWidth(sub, 1), (headerH-glyphH)/2, sub, 1,
		color.RGBA{255, 255, 255, 190})
}

// Settings modal -------------------------------------------------------------

// drawSettings renders the modal: a dim backdrop, the panel with the
// character-age dropdown and SAVE/CANCEL buttons, and - drawn last so it
// overlays the buttons it covers - the expanded option list.
func (u *UI) drawSettings(frame *image.NRGBA) {
	fillRect(frame, 0, 0, u.W, u.H, color.RGBA{40, 30, 55, 120}) // dim backdrop

	p := u.modalPanel()
	drawRoundRect(frame, p.Min.X, p.Min.Y, p.Dx(), p.Dy(), winRadius, colPlum)
	drawRoundRect(frame, p.Min.X+2, p.Min.Y+2, p.Dx()-4, p.Dy()-4, winRadius-2, colBubbleFill)

	drawText(frame, p.Min.X+modalPad, p.Min.Y+14, "SETTINGS", uiFontScale, colPlum)
	if u.saveErr != "" {
		msg := u.saveErr
		if cols := (p.Dx() - 2*modalPad) / cellW; len([]rune(msg)) > cols {
			rs := []rune(msg)
			msg = string(rs[:max(0, cols-3)]) + "..."
		}
		drawText(frame, p.Min.X+modalPad, p.Min.Y+36, msg, 1, colError)
	}

	drawText(frame, p.Min.X+modalPad, p.Min.Y+52, "NAME", 1, colMuted)
	u.drawNameBox(frame)

	drawText(frame, p.Min.X+modalPad, p.Min.Y+112, "CHARACTER AGE", 1, colMuted)
	u.drawSelectBox(frame, u.dropRect(), strconv.Itoa(u.ageDraft), u.openDrop == dropAge, WDrop)

	drawText(frame, p.Min.X+modalPad, p.Min.Y+172, "SLEEP TIME", 1, colMuted)
	fr, tr := u.sleepFromRect(), u.sleepToRect()
	drawText(frame, fr.Min.X, p.Min.Y+186, "FROM", 1, colMuted)
	drawText(frame, tr.Min.X, p.Min.Y+186, "TO", 1, colMuted)
	u.drawSelectBox(frame, fr, fmt.Sprintf("%02d:00", u.sleepFromDraft), u.openDrop == dropFrom, WDropFrom)
	u.drawSelectBox(frame, tr, fmt.Sprintf("%02d:00", u.sleepToDraft), u.openDrop == dropTo, WDropTo)

	u.drawModalButtons(frame)
	if u.openDrop == dropAge {
		u.drawDropList(frame)
	}
	if u.openDrop == dropFrom || u.openDrop == dropTo {
		u.drawHourList(frame)
	}
}

// drawNameBox paints the editable character-name field: white body, teal
// border while focused, placeholder when empty, caret after the last glyph.
func (u *UI) drawNameBox(frame *image.NRGBA) {
	r := u.nameRect()
	border := colPlum
	if u.nameFocused || u.hover == WName {
		border = colHeader
	}
	drawRoundRect(frame, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 8, border)
	drawRoundRect(frame, r.Min.X+2, r.Min.Y+2, r.Dx()-4, r.Dy()-4, 6, colWhite)

	text := string(u.nameDraft)
	dy := r.Min.Y + (dropH-glyphH*uiFontScale)/2
	if text == "" {
		drawText(frame, r.Min.X+12, dy, "type a name...", uiFontScale, colMuted)
	} else {
		drawText(frame, r.Min.X+12, dy, text, uiFontScale, colText)
	}
	if u.nameFocused && u.caret {
		fillRect(frame, r.Min.X+12+textWidth(text, uiFontScale), dy, 2,
			glyphH*uiFontScale, colPlum)
	}
}

// drawSelectBox paints one dropdown box: white body with the current value
// and a chevron that flips while the list is open.
func (u *UI) drawSelectBox(frame *image.NRGBA, r image.Rectangle, text string, open bool, w Widget) {
	border := colPlum
	if u.press == w {
		border = colTealShade
	} else if u.hover == w {
		border = colHeader
	}
	drawRoundRect(frame, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 8, border)
	drawRoundRect(frame, r.Min.X+2, r.Min.Y+2, r.Dx()-4, r.Dy()-4, 6, colWhite)

	dy := r.Min.Y + (dropH-glyphH*uiFontScale)/2
	drawText(frame, r.Min.X+12, dy, text, uiFontScale, colText)
	drawChevron(frame, r.Max.X-16, r.Min.Y+dropH/2-1, open, colMuted)
}

// drawDropList paints the expanded option list (7..13) over the panel. The
// selected age gets a dot marker; the hovered row is tinted.
func (u *UI) drawDropList(frame *image.NRGBA) {
	l := u.dropListRect()
	drawRoundRect(frame, l.Min.X, l.Min.Y, l.Dx(), l.Dy(), 8, colPlum)
	drawRoundRect(frame, l.Min.X+2, l.Min.Y+2, l.Dx()-4, l.Dy()-4, 6, colWhite)

	for i := 0; i < numAges; i++ {
		ry := l.Min.Y + i*optH
		if u.hover == WOption && u.optIdx == i {
			// Rounded ends on the first/last row so the highlight follows
			// the list's rounded corners.
			if i == 0 || i == numAges-1 {
				drawRoundRect(frame, l.Min.X+3, ry, l.Dx()-6, optH, 6, colHairLight)
			} else {
				fillRect(frame, l.Min.X+3, ry, l.Dx()-6, optH, colHairLight)
			}
		}
		ty := ry + (optH-glyphH*uiFontScale)/2
		val := minCharAge + i
		drawText(frame, l.Min.X+26, ty, strconv.Itoa(val), uiFontScale, colText)
		if val == u.ageDraft {
			fillDisc(frame, l.Min.X+15, ry+optH/2, 3, colHeader)
		}
	}
}

// drawHourList paints the expanded 0:00..23:00 list of the FROM/TO dropdowns:
// five rows visible at a time (wheel-scrolled, see ScrollHourList), the
// selected hour marked with a dot and a mini scrollbar on the right.
func (u *UI) drawHourList(frame *image.NRGBA) {
	box := u.openHourBox()
	if box == (image.Rectangle{}) {
		return
	}
	l := u.hourListRect()
	drawRoundRect(frame, l.Min.X, l.Min.Y, l.Dx(), l.Dy(), 8, colPlum)
	drawRoundRect(frame, l.Min.X+2, l.Min.Y+2, l.Dx()-4, l.Dy()-4, 6, colWhite)

	sel := u.sleepFromDraft
	if u.openDrop == dropTo {
		sel = u.sleepToDraft
	}
	for i := u.hourScroll; i < u.hourScroll+visibleHourRows && i < numHours; i++ {
		ry := l.Min.Y + (i-u.hourScroll)*optH
		if u.hover == WOption && u.optIdx == i {
			// Rounded ends wherever the highlight touches the list's
			// rounded corners (first/last row overall or of the viewport).
			if i == 0 || i == numHours-1 || i == u.hourScroll ||
				i == u.hourScroll+visibleHourRows-1 {
				drawRoundRect(frame, l.Min.X+3, ry, l.Dx()-6, optH, 6, colHairLight)
			} else {
				fillRect(frame, l.Min.X+3, ry, l.Dx()-6, optH, colHairLight)
			}
		}
		drawText(frame, l.Min.X+26, ry+(optH-glyphH*uiFontScale)/2,
			fmt.Sprintf("%02d:00", i), uiFontScale, colText)
		if i == sel {
			fillDisc(frame, l.Min.X+15, ry+optH/2, 3, colHeader)
		}
	}

	// Mini scrollbar: track on the right, thumb sized to the visible share.
	trackY0, trackY1 := l.Min.Y+4, l.Max.Y-4
	trackH := trackY1 - trackY0
	thumbH := max(trackH*visibleHourRows/numHours, 10)
	maxScroll := numHours - visibleHourRows
	thumbY := trackY0 + (trackH-thumbH)*u.hourScroll/max(maxScroll, 1)
	fillRect(frame, l.Max.X-8, trackY0, 3, trackH, colInputBorder)
	fillRect(frame, l.Max.X-8, thumbY, 3, thumbH, colMuted)
}

// drawModalButtons paints CANCEL (muted) and SAVE (teal, like SEND).
func (u *UI) drawModalButtons(frame *image.NRGBA) {
	cancel, save := u.modalButtons()
	u.drawModalButton(frame, cancel, WCancel, "CANCEL")
	u.drawModalButton(frame, save, WSave, "SAVE")
}

func (u *UI) drawModalButton(frame *image.NRGBA, r image.Rectangle, w Widget, lbl string) {
	fill, label, outline := colBtnOff, colMuted, colInputBorder
	if w == WSave {
		fill, label, outline = colBtn, colWhite, colPlum
	}
	switch {
	case u.press == w:
		fill = colTealShade
	case u.hover == w:
		fill, label = colHairLight, colPlum
	}
	drawRoundRect(frame, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 9, outline)
	drawRoundRect(frame, r.Min.X+2, r.Min.Y+2, r.Dx()-4, r.Dy()-4, 7, fill)
	lw := textWidth(lbl, uiFontScale)
	drawText(frame, r.Min.X+(r.Dx()-lw)/2,
		r.Min.Y+(r.Dy()-glyphH*uiFontScale)/2, lbl, uiFontScale, label)
}

// drawChevron paints the dropdown's small down/up arrow: three 2px rows that
// get narrower towards the point.
func drawChevron(img *image.NRGBA, cx, cy int, up bool, col color.RGBA) {
	for i := 0; i < 3; i++ {
		w := 3 + 2*i
		dy := 2 - 2*i // down: wide on top, narrow at the bottom
		if up {
			dy = -dy
		}
		fillRect(img, cx-w/2, cy+dy, w, 2, col)
	}
}

// drawGear paints a small cog centred at (cx,cy): a disc with eight teeth
// and a hole punched in the button's own fill colour.
func drawGear(img *image.NRGBA, cx, cy int, col, hole color.RGBA) {
	fillRect(img, cx-2, cy-7, 5, 3, col) // N
	fillRect(img, cx-2, cy+4, 5, 3, col) // S
	fillRect(img, cx-7, cy-2, 3, 5, col) // W
	fillRect(img, cx+4, cy-2, 3, 5, col) // E
	fillRect(img, cx+3, cy-6, 3, 3, col) // NE
	fillRect(img, cx+3, cy+3, 3, 3, col) // SE
	fillRect(img, cx-6, cy-6, 3, 3, col) // NW
	fillRect(img, cx-6, cy+3, 3, 3, col) // SW
	fillDisc(img, cx, cy, 5, col)
	fillDisc(img, cx, cy, 2, hole)
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
	m         Msg
	lines     []string
	bubW      int
	bubH      int         // bubble-only height, including the pager strip
	h         int         // total block height including the label strip + image
	img       image.Image // scaled image to draw above the bubble (may be nil)
	paginated bool        // bubble has a pager strip (m.Pages > 1)
	pageCount int         // number of pages (len m.Pages)
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
	text := m.Text
	paginated := len(m.Pages) > 1
	if paginated {
		text = m.Pages[clamp(m.Page, 0, len(m.Pages)-1)]
	}
	lines := wrapText(text, cols)
	textW := 0
	for _, l := range lines {
		textW = max(textW, textWidth(l, uiFontScale))
	}
	bubW := min(textW+2*bubOutline+2*bubPadX, maxW)
	bubH := len(lines)*lineH + 2*bubPadY
	if paginated {
		bubH += pagStrip // pager controls live in a strip at the bubble's foot
	}

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
	return msgBlock{m: m, lines: lines, bubW: bubW, bubH: bubH, h: h, img: img, paginated: paginated, pageCount: len(m.Pages)}
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
	if b.paginated {
		// Pager strip at the bubble's foot: separator hairline + < 1/3 >.
		stripY := by + b.bubH - pagStrip
		fillRect(layer, bx+bubOutline+4, stripY, b.bubW-2*bubOutline-8, 1, colInputBorder)
		u.drawPager(layer, b, bx, by)
	}
}

// drawPager paints the < prev / n/N label / next > controls in the bubble's
// pager strip. Disabled ends (first/last page) draw muted.
func (u *UI) drawPager(layer *image.NRGBA, b msgBlock, bx, by int) {
	prev, next, lblX, lbl := pagerRects(b, bx, by)
	u.drawPagBtn(layer, prev, "<", b.m.Page > 0)
	u.drawPagBtn(layer, next, ">", b.m.Page < b.pageCount-1)
	y := by + b.bubH - pagStrip + (pagStrip-pagBtn)/2
	drawText(layer, lblX, y+(pagBtn-glyphH)/2, lbl, 1, colMuted)
}

// pagerRects lays out the pager buttons and label right-aligned in the bubble's
// foot strip. Returns layer-relative rects, the label's left-x and the label.
// Single source of truth for hit-testing (pagerAt) and drawing (drawPager).
func pagerRects(b msgBlock, bx, by int) (prev, next image.Rectangle, lblX int, lbl string) {
	lbl = fmt.Sprintf("%d/%d", b.m.Page+1, b.pageCount)
	lw := textWidth(lbl, 1)
	x1 := bx + b.bubW - bubOutline - 4
	y := by + b.bubH - pagStrip + (pagStrip-pagBtn)/2
	x := x1
	next = image.Rect(x-pagBtn, y, x, y+pagBtn)
	x -= pagBtn + 3 // now: label right edge
	lblX = x - lw   // label left edge (drawn right-aligned at lblX)
	x = lblX - 3    // now: prev button right edge
	prev = image.Rect(x-pagBtn, y, x, y+pagBtn)
	return prev, next, lblX, lbl
}

// drawPagBtn paints one square pager chevron button ("<" or ">").
func (u *UI) drawPagBtn(layer *image.NRGBA, r image.Rectangle, glyph string, on bool) {
	fill, col, outline := colBubbleFill, colPlum, colPlum
	if !on {
		fill, col, outline = colBubbleFill, colMuted, colInputBorder
	}
	drawRoundRect(layer, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 4, outline)
	drawRoundRect(layer, r.Min.X+1, r.Min.Y+1, r.Dx()-2, r.Dy()-2, 3, fill)
	gw := textWidth(glyph, 1)
	drawText(layer, r.Min.X+(r.Dx()-gw)/2, r.Min.Y+(r.Dy()-glyphH)/2, glyph, 1, col)
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
