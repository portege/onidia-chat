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
	"time"
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
	WHaiya  // header "Haiya!" button: launches the onidia pet application
	WAbout  // header "About" button: opens the About modal
	WToggle // header +/- button: shows/hides the conversation history

	// Settings modal (see drawSettings): the header's gear button plus the
	// widgets that live inside the modal.
	WSettings   // gear button in the header
	WModal      // dim backdrop around the panel (absorbs outside clicks)
	WName       // character-name text field
	WDrop       // FROM hour dropdown
	WDropFrom   // FROM hour dropdown (sleep start)
	WDropTo     // TO hour dropdown (sleep end)
	WDropFromM  // FROM minute dropdown 0/15/30/45
	WDropToM    // TO minute dropdown 0/15/30/45
	WDropFromB  // BUSY FROM hour dropdown
	WDropToB    // BUSY TO hour dropdown
	WDropFromBM // BUSY FROM minute dropdown 0/15/30/45
	WDropToBM   // BUSY TO minute dropdown 0/15/30/45
	WDropBad    // dropdown whose selected value is invalid (for validation prompt)
	WMute       // mute-speech checkbox row
	WDemo       // demo-mode checkbox row (below mute)
	WGirl       // gender picker: ONIDIA button (Haiya! launches the girl)
	WBoy        // gender picker: KAMA button (Haiya! launches the boy)
	WOption     // one row of an open dropdown list
	WSave       // modal SAVE button
	WCancel     // modal CANCEL button
	WPagePrev   // prev page in a paginated chat bubble
	WPageNext   // next page in a paginated chat bubble
	WCopy       // "Copy" pill on a chat bubble's sender-label row

	// About modal (see drawAbout): a small informational panel.
	WAboutOK // the About modal's OK button
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

	// Haiya! button while the onidia pet is running: bubblegum pink so the
	// state change is obvious at a glance (teal = launch, pink = click quits).
	colHaiyaPink   = color.RGBA{236, 96, 156, 255}  // base fill
	colHaiyaPinkHi = color.RGBA{252, 150, 194, 255} // hover (lighter)
	colHaiyaPinkLo = color.RGBA{186, 52, 116, 255}  // press / border (deeper)
)

const (
	uiFontScale = 2
	cellW       = advW * uiFontScale         // glyph advance in px (12)
	lineH       = (glyphH + 3) * uiFontScale // text line pitch (24): generous
	// enough that descender tails (g, y, p, q) never touch the next line

	// Default window size. The width is 20% wider than the original 380 so
	// chat bubbles and the wrapping textarea have room; every rect (and the
	// bubble wrap width) derives from u.W, so widening this scales the whole
	// layout, text area included.
	defaultWinW = 456
	defaultWinH = 520

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

	// "Copy" pill on a bubble's sender-label row.
	copyBtnPad   = 4 // padding around the Copy label inside its pill
	copyLbl      = "Copy"
	copyFlashDur = 1200 * time.Millisecond // lit after a successful copy

	maxInput  = 280 // textarea rune cap
	winRadius = 12  // window shell corner rounding (transparent corners)

	// Settings modal layout (drawSettings).
	modalPad  = 20  // panel inner padding
	modalBtnW = 90  // SAVE / CANCEL button width
	panelW    = 340 // modal panel width (clamped to the window; wide enough
	// that the four sleep-time dropdowns fit their labels, chevrons and
	// the expanded lists' dot + text)
	panelH = 504 // modal panel height (name + age + sleep rows + busy rows +
	// character picker + mute checkbox + demo-mode checkbox + buttons)
	dropH        = 32  // dropdown box height
	optH         = 24  // dropdown list row height
	genderRowY   = 314 // CHARACTER picker row top inside the panel (below busy time)
	minSettingsH = 574 // window height forced while the modal is open

	checkSide = 20  // checkbox square side (mute / demo mode)
	muteRowY  = 370 // mute-checkbox row top inside the panel (below the character picker)
	demoRowY  = 398 // demo-mode checkbox row top inside the panel (below mute)

	// About modal layout (drawAbout): a small informational panel shown by
	// the header's About button.
	aboutPanelW = 300 // panel width
	aboutPanelH = 214 // panel height (title + tagline + credit + OK button)
	minAboutH   = 260 // window height forced while the About modal is open
	aboutBtnW   = 90  // OK button width

	maxNameChars = 16 // character-name field rune cap

	minCharAge          = 7  // youngest character age in the dropdown
	maxCharAge          = 13 // oldest character age in the dropdown
	defaultCharacterAge = 10 // pre-selected age when none is configured

	// sleep dropdowns: the hour list is 24 entries (0..23), the minute list
	// offers the four quarter-hour steps (00 / 15 / 30 / 45) next to it.
	numHours          = 24 // hour entries in a sleep-time dropdown (0..23)
	numMinutes        = 4  // minute entries (00 / 15 / 30 / 45)
	visibleHourRows   = 5  // hour-list rows shown before it scrolls
	visibleMinuteRows = 4  // all four minute rows shown at once
	defaultSleepFrom  = 22 // pre-selected sleep start when none is configured
	defaultSleepTo    = 7  // pre-selected sleep end when none is configured
	defaultBusyFrom   = 9  // pre-selected busy start when none is configured
	defaultBusyTo     = 17 // pre-selected busy end when none is configured
)

// numAges is how many entries the age dropdown shows.
const numAges = maxCharAge - minCharAge + 1

// Which dropdown list is currently expanded (UI.openDrop).
const (
	dropNone = iota
	dropAge
	dropFrom
	dropTo
	dropFromM
	dropToM
	dropBusyFrom
	dropBusyTo
	dropBusyFromM
	dropBusyToM
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

	wantClose  bool // set by a click on the header's close button
	wantPet    bool // set by a click on the header's "Haiya!" button
	petRunning bool // onidia pet is running: the button is pink and quits it

	// About modal state (see drawAbout): a small informational panel with an
	// OK button, opened from the header's About button.
	aboutOpen  bool
	aboutPrevH int // window height before the About panel forced minAboutH

	// Settings modal state (see drawSettings). name / age / sleepFrom /
	// sleepTo / mute are the committed values: unset until the first save,
	// or loaded from the config (age 0 = unset; sleep uses -1 because hour 0
	// is valid; name "" = unset; mute false = speech on).
	settingsOpen      bool
	nameFocused       bool      // the name field owns the keyboard
	nameDraft         []rune    // name typed in the modal; committed on SAVE
	openDrop          int       // which dropdown list is expanded (dropNone/dropAge/...)
	hourScroll        int       // first visible row of the open hour list
	minuteScroll      int       // first visible row of the open minute list
	ageDraft          int       // age picked in the modal; committed on SAVE
	sleepFromDraft    int       // sleep start hour picked in the modal
	sleepFromMinDraft int       // sleep start minute picked in the modal (0/15/30/45)
	sleepToDraft      int       // sleep end hour picked in the modal
	sleepToMinDraft   int       // sleep end minute picked in the modal (0/15/30/45)
	busyFromDraft     int       // busy start hour picked in the modal
	busyToDraft       int       // busy end hour picked in the modal
	busyFromMinDraft  int       // busy start minute picked in the modal (0/15/30/45)
	busyToMinDraft    int       // busy end minute picked in the modal (0/15/30/45)
	muteDraft         bool      // mute-speech checkbox in the modal; committed on SAVE
	demo              bool      // committed demo mode: true = pet roams & chatters (default off)
	demoDraft         bool      // demo-mode checkbox in the modal; committed on SAVE
	wantPetRestart    bool      // SAVE changed demo mode while the pet runs: restart it
	gender            string    // committed pet gender: "girl" (Onidia) or "boy" (Kama)
	genderDraft       string    // gender picked in the modal; committed on SAVE
	prevH             int       // window height before the modal forced minSettingsH
	name              string    // committed character name ("" = not set yet)
	age               int       // committed character age (0 = not set yet)
	sleepFrom         int       // committed sleep-window start hour (-1 = unset)
	sleepFromMin      int       // committed sleep-window start minute (0/15/30/45)
	sleepTo           int       // committed sleep-window end hour (-1 = unset)
	sleepToMin        int       // committed sleep-window end minute (0/15/30/45)
	busyFrom          int       // committed busy-window start hour (-1 = unset)
	busyFromMin       int       // committed busy-window start minute (0/15/30/45)
	busyTo            int       // committed busy-window end hour (-1 = unset)
	busyToMin         int       // committed busy-window end minute (0/15/30/45)
	mute              bool      // committed: replies are not spoken aloud (INI "mute")
	savePath          string    // INI file settings are written to ("" = ./chat-app.ini)
	saveErr           string    // last save error, shown inside the modal
	optIdx            int       // dropdown row under the pointer (set by HitTest)
	optMIdx           int       // minute-list row under the pointer (set by HitTest)
	pagerMsg          int       // msg index under the pointer in a paginated bubble (-1 = none)
	pagerDir          int       // -1 prev / +1 next, from the last pager hit-test
	copyMsg           int       // msg index whose Copy pill is under the pointer (-1 = none)
	wantCopy          bool      // a Copy pill was clicked; main() pushes the text onto the clipboard
	copiedText        string    // the message text to copy
	copyFlash         time.Time // when the copy happened (pill lights up briefly)
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
		gender:    "girl", // GIRL picker active until the INI says boy
		sleepFrom: -1,     // -1 = no sleep window configured yet
		sleepTo:   -1,
		pagerMsg:  -1, // no pager under the pointer yet
		copyMsg:   -1, // no COPY pill under the pointer yet
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

// aboutRect is the header's About button, between the settings gear and the
// close button. Clicking it opens the About modal (see drawAbout).
func (u *UI) aboutRect() image.Rectangle {
	x1 := u.closeRect().Min.X - btnGap
	y := (headerH - hdrBtn) / 2
	return image.Rect(x1-hdrBtn, y, x1, y+hdrBtn)
}

// settingsRect is the gear button in the header, left of the About button.
func (u *UI) settingsRect() image.Rectangle {
	x1 := u.aboutRect().Min.X - btnGap
	y := (headerH - hdrBtn) / 2
	return image.Rect(x1-hdrBtn, y, x1, y+hdrBtn)
}

// toggleRect is the header's history show/hide button (+/-, left of the
// settings gear). It is the only way to expand or collapse the conversation
// list - a plain title-bar click just drags the window.
func (u *UI) toggleRect() image.Rectangle {
	x1 := u.settingsRect().Min.X - btnGap
	y := (headerH - hdrBtn) / 2
	return image.Rect(x1-hdrBtn, y, x1, y+hdrBtn)
}

// haiyaLabel is the text on the header button that launches the onidia pet.
const haiyaLabel = "Haiya!"

// aboutLabel is the text on the header button that opens the About modal.
const aboutLabel = "About"

// haiyaRect is the "Haiya!" button: a rounded pill right after the CHAT
// label in the header, deliberately larger than the close/gear squares so the
// pet toggle is easy to hit. Clicking it runs the pet application, or - when
// the pet is already running (PetRunning) - quits it with a poof-out.
func (u *UI) haiyaRect() image.Rectangle {
	x1 := padX + textWidth("ONIDIA", uiFontScale) + btnGap
	h := hdrBtn + 8 // taller than the close/gear squares
	y := (headerH - h) / 2
	w := textWidth(haiyaLabel, uiFontScale) + 28 // bigger label + side padding
	return image.Rect(x1, y, x1+w, y+h)
}

// modalPanel is the centred settings dialog rectangle, clamped to the current
// window. Opening the modal grows the window to minSettingsH (see
// openSettings) so the full panelH fits; the clamp only comes into play if the
// window is somehow smaller while the modal is open.
func (u *UI) modalPanel() image.Rectangle {
	ph := min(panelH, u.H-modalPad)
	pw := min(panelW, u.W-2*modalPad)
	return image.Rect((u.W-pw)/2, (u.H-ph)/2, (u.W+pw)/2, (u.H+ph)/2)
}

// aboutPanel is the centred About dialog rectangle, likewise clamped.
func (u *UI) aboutPanel() image.Rectangle {
	ph := min(aboutPanelH, u.H-modalPad)
	pw := min(aboutPanelW, u.W-2*modalPad)
	return image.Rect((u.W-pw)/2, (u.H-ph)/2, (u.W+pw)/2, (u.H+ph)/2)
}

// nameRect is the editable character-name field: full panel width, directly
// below the NAME label (a 14px label gap, same as the age/sleep/busy rows) and
// above the age dropdown, with the same height as the other boxes (dropH).
func (u *UI) nameRect() image.Rectangle {
	p := u.modalPanel()
	return image.Rect(p.Min.X+modalPad, p.Min.Y+66, p.Max.X-modalPad, p.Min.Y+66+dropH)
}

// dropRect is the character-age dropdown box inside the panel.
func (u *UI) dropRect() image.Rectangle {
	p := u.modalPanel()
	return image.Rect(p.Min.X+modalPad, p.Min.Y+126, p.Max.X-modalPad, p.Min.Y+126+dropH)
}

// sleepFromRect / sleepToRect are the hour dropdown boxes (left of the pair).
func (u *UI) sleepFromRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	y := p.Min.Y + 200
	// The boxes split the row ~50/50 (6px gap): the minute box needs the
	// room for its two-digit label, chevron and the expanded list's dot +
	// text — at the old 60/40 split both overlapped / spilled out.
	hw := rowW * 5 / 10
	return image.Rect(p.Min.X+modalPad, y, p.Min.X+modalPad+hw, y+dropH)
}

// sleepFromMinRect / sleepToMinRect are the minute dropdown boxes (00/15/30/45),
// snug to the right of the hour box. The 5/10 split must mirror
// sleepFromRect's hour width exactly or the two boxes drift apart/overlap.
func (u *UI) sleepFromMinRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	y := p.Min.Y + 200
	hw := rowW * 5 / 10
	return image.Rect(p.Min.X+modalPad+hw+6, y, p.Min.X+modalPad+rowW, y+dropH)
}

func (u *UI) sleepToRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	dx := rowW + 12
	f := u.sleepFromRect()
	return image.Rect(f.Min.X+dx, f.Min.Y, f.Max.X+dx, f.Max.Y)
}

func (u *UI) sleepToMinRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	dx := rowW + 12
	f := u.sleepFromMinRect()
	return image.Rect(f.Min.X+dx, f.Min.Y, f.Max.X+dx, f.Max.Y)
}

// busyFromRect / busyToRect are the busy-time hour dropdowns, one row below
// the sleep boxes; the minute boxes share the same 5/10 split as sleep so
// labels stay fully inside the boxes.
func (u *UI) busyFromRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	y := p.Min.Y + 270
	hw := rowW * 5 / 10
	return image.Rect(p.Min.X+modalPad, y, p.Min.X+modalPad+hw, y+dropH)
}

func (u *UI) busyFromMinRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	y := p.Min.Y + 270
	hw := rowW * 5 / 10
	return image.Rect(p.Min.X+modalPad+hw+6, y, p.Min.X+modalPad+rowW, y+dropH)
}

func (u *UI) busyToRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	dx := rowW + 12
	f := u.busyFromRect()
	return image.Rect(f.Min.X+dx, f.Min.Y, f.Max.X+dx, f.Max.Y)
}

func (u *UI) busyToMinRect() image.Rectangle {
	p := u.modalPanel()
	rowW := (p.Dx() - 2*modalPad - 12) / 2
	dx := rowW + 12
	f := u.busyFromMinRect()
	return image.Rect(f.Min.X+dx, f.Min.Y, f.Max.X+dx, f.Max.Y)
}

// muteRect is the mute-speech checkbox row: the box plus its label, so
// clicking either toggles the draft.
func (u *UI) muteRect() image.Rectangle {
	p := u.modalPanel()
	w := checkSide + 10 + textWidth("MUTE SPEECH", 1)
	return image.Rect(p.Min.X+modalPad, p.Min.Y+muteRowY,
		p.Min.X+modalPad+w, p.Min.Y+muteRowY+checkSide)
}

// demoRect is the demo-mode checkbox row: the box plus its label, so clicking
// either toggles the draft. It sits directly below the MUTE SPEECH row.
func (u *UI) demoRect() image.Rectangle {
	p := u.modalPanel()
	w := checkSide + 10 + textWidth("DEMO MODE", 1)
	return image.Rect(p.Min.X+modalPad, p.Min.Y+demoRowY,
		p.Min.X+modalPad+w, p.Min.Y+demoRowY+checkSide)
}

// genderRects returns the ONIDIA and KAMA picker buttons: a centred pair on
// their own row below the busy time (the MUTE SPEECH checkbox sits directly
// below them), sized like the dropdown boxes and laid out like the modal's
// SAVE/CANCEL pair.
func (u *UI) genderRects() (girl, boy image.Rectangle) {
	p := u.modalPanel()
	total := 2*modalBtnW + btnGap
	sx := p.Min.X + (p.Dx()-total)/2
	by := p.Min.Y + genderRowY + 16
	return image.Rect(sx, by, sx+modalBtnW, by+dropH),
		image.Rect(sx+modalBtnW+btnGap, by, sx+total, by+dropH)
}

// dropListRect is the expanded age list; empty unless the age list is open.
func (u *UI) dropListRect() image.Rectangle {
	if u.openDrop != dropAge {
		return image.Rectangle{}
	}
	d := u.dropRect()
	return image.Rect(d.Min.X, d.Max.Y+4, d.Max.X, d.Max.Y+4+numAges*optH)
}

// hourLabel is the readable label for one sleep-hour entry: 0..23.
func hourLabel(i int) string {
	if i < 0 || i >= numHours {
		return "00"
	}
	return fmt.Sprintf("%02d", i)
}

// minuteLabel is the readable label for one sleep-minute entry (no colon):
// 00 / 15 / 30 / 45.
func minuteLabel(i int) string {
	if i < 0 || i >= numMinutes {
		return "00"
	}
	return fmt.Sprintf("%02d", sleepMinutes[i])
}

// openHourBox is the FROM/TO hour box whose list is open; empty when none is.
func (u *UI) openHourBox() image.Rectangle {
	switch u.openDrop {
	case dropFrom:
		return u.sleepFromRect()
	case dropTo:
		return u.sleepToRect()
	case dropBusyFrom:
		return u.busyFromRect()
	case dropBusyTo:
		return u.busyToRect()
	}
	return image.Rectangle{}
}

// openMinuteBox is the FROM/TO minute box whose list is open; empty when none.
func (u *UI) openMinuteBox() image.Rectangle {
	switch u.openDrop {
	case dropFromM:
		return u.sleepFromMinRect()
	case dropToM:
		return u.sleepToMinRect()
	case dropBusyFromM:
		return u.busyFromMinRect()
	case dropBusyToM:
		return u.busyToMinRect()
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

// minuteListRect is the expanded minute list: four rows below its box.
// Empty when no minute list open.
func (u *UI) minuteListRect() image.Rectangle {
	box := u.openMinuteBox()
	if box == (image.Rectangle{}) {
		return image.Rectangle{}
	}
	return image.Rect(box.Min.X, box.Max.Y+4, box.Max.X, box.Max.Y+4+visibleMinuteRows*optH)
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

// aboutOKRect is the About modal's single OK button, centred in the panel foot.
func (u *UI) aboutOKRect() image.Rectangle {
	p := u.aboutPanel()
	sx := p.Min.X + (p.Dx()-aboutBtnW)/2
	by := p.Max.Y - 14 - btnH
	return image.Rect(sx, by, sx+aboutBtnW, by+btnH)
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
	if u.aboutOpen {
		// The About modal owns the whole window while it is open.
		if inRect(x, y, u.aboutOKRect()) {
			return WAboutOK
		}
		return WModal
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
		case dropFrom, dropTo, dropBusyFrom, dropBusyTo:
			if l := u.hourListRect(); inRect(x, y, l) {
				u.optIdx = clamp(u.hourScroll+(y-l.Min.Y)/optH, 0, numHours-1)
				return WOption
			}
		case dropFromM, dropToM, dropBusyFromM, dropBusyToM:
			if l := u.minuteListRect(); inRect(x, y, l) {
				u.optMIdx = clamp(u.minuteScroll+(y-l.Min.Y)/optH, 0, numMinutes-1)
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
		if r := u.sleepFromMinRect(); inRect(x, y, r) {
			return WDropFromM
		}
		if r := u.sleepToMinRect(); inRect(x, y, r) {
			return WDropToM
		}
		if r := u.busyFromRect(); inRect(x, y, r) {
			return WDropFromB
		}
		if r := u.busyFromMinRect(); inRect(x, y, r) {
			return WDropFromBM
		}
		if r := u.busyToRect(); inRect(x, y, r) {
			return WDropToB
		}
		if r := u.busyToMinRect(); inRect(x, y, r) {
			return WDropToBM
		}
		if r := u.muteRect(); inRect(x, y, r) {
			return WMute
		}
		if r := u.demoRect(); inRect(x, y, r) {
			return WDemo
		}
		if girl, boy := u.genderRects(); inRect(x, y, girl) {
			return WGirl
		} else if inRect(x, y, boy) {
			return WBoy
		}
		return WModal
	}
	if y < headerH {
		if inRect(x, y, u.closeRect()) {
			return WClose
		}
		if inRect(x, y, u.aboutRect()) {
			return WAbout
		}
		if inRect(x, y, u.settingsRect()) {
			return WSettings
		}
		if inRect(x, y, u.toggleRect()) {
			return WToggle
		}
		if inRect(x, y, u.haiyaRect()) {
			return WHaiya
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
		if w := u.copyAt(x, y); w != WNone {
			return w
		}
		if w := u.pagerAt(x, y); w != WNone {
			return w
		}
		return WMessages
	}
	return WNone
}

// State changes ------------------------------------------------------------

// Resize updates the window size and re-clamps the scroll position. While a
// modal is open the height also covers its panel, so a resize cannot shrink
// the window until the panel no longer fits.
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
	u.msgs = append(u.msgs, newMsg(from, text, nil))
	u.scroll = u.maxScroll()
}

// AddMsgWithImage appends a message that may include an image.
func (u *UI) AddMsgWithImage(from, text string, img image.Image) {
	u.msgs = append(u.msgs, newMsg(from, text, img))
	u.scroll = u.maxScroll()
}

// copyBtnRect returns the little "Copy" pill on a bubble's sender-label row,
// placed on the free end of the row (right end for bot bubbles, left end for
// user ones - the sender name sits on the other side).
func copyBtnRect(b msgBlock, bx, y int) image.Rectangle {
	w := textWidth(copyLbl, 1) + 2*copyBtnPad
	if b.m.From != "you" {
		return image.Rect(bx+b.bubW-w, y, bx+b.bubW, y+labelH)
	}
	return image.Rect(bx, y, bx+w, y+labelH)
}

// copyAt checks whether (x,y) window coordinates fall on a chat bubble's COPY
// pill. When they do it records the target message index (u.copyMsg) and
// returns WCopy; textless and synthetic ("...") bubbles have no pill.
func (u *UI) copyAt(x, y int) Widget {
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
	ly := y - areaY // layer-relative Y (the pill rects live in the msg layer)
	ty := msgTopPad - scroll
	for mi, b := range bs {
		if mi < len(u.msgs) && strings.TrimSpace(b.m.Text) != "" &&
			ty+labelH > 0 && ty < areaH {
			bx := padX
			if b.m.From == "you" {
				bx = u.W - padX - b.bubW
			}
			if inRect(x, ly, copyBtnRect(b, bx, ty)) {
				u.copyMsg = mi
				return WCopy
			}
		}
		ty += b.h + bubGap
	}
	return WNone
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
// the same widget. Returns true when the window's height changed (the
// history show/hide button or a modal needing a taller window), so the
// caller can resize the X window to match.
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
		case WHaiya:
			// Header "Haiya!" button: launches the onidia pet application,
			// or quits it with a poof-out when it is already running.
			// main() consumes the request via WantPet (one-shot), which
			// decides from PetRunning; later header clicks must not re-fire.
			u.wantPet = true
		case WSettings:
			if u.openSettings() {
				return true // the window grew to fit the modal
			}
		case WAbout:
			if u.openAbout() {
				return true // the window grew to fit the modal
			}
		case WAboutOK:
			u.press = WNone
			return u.closeAbout()
		case WName:
			// Focus already moved above; typing now edits the name draft.
		case WDrop:
			u.toggleDrop(dropAge)
		case WDropFrom:
			u.toggleDrop(dropFrom)
		case WDropTo:
			u.toggleDrop(dropTo)
		case WDropFromM:
			u.toggleDrop(dropFromM)
		case WDropToM:
			u.toggleDrop(dropToM)
		case WDropFromB:
			u.toggleDrop(dropBusyFrom)
		case WDropToB:
			u.toggleDrop(dropBusyTo)
		case WDropFromBM:
			u.toggleDrop(dropBusyFromM)
		case WDropToBM:
			u.toggleDrop(dropBusyToM)
		case WMute:
			u.muteDraft = !u.muteDraft // commits on SAVE, like the drafts
		case WDemo:
			u.demoDraft = !u.demoDraft // commits on SAVE, like the drafts
		case WGirl:
			u.genderDraft = "girl" // commits on SAVE, like the drafts
		case WBoy:
			u.genderDraft = "boy"
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
			case dropFromM:
				if u.optMIdx >= 0 && u.optMIdx < numMinutes {
					u.sleepFromMinDraft = u.optMIdx
				}
			case dropToM:
				if u.optMIdx >= 0 && u.optMIdx < numMinutes {
					u.sleepToMinDraft = u.optMIdx
				}
			case dropBusyFrom:
				if u.optIdx >= 0 && u.optIdx < numHours {
					u.busyFromDraft = u.optIdx
				}
			case dropBusyTo:
				if u.optIdx >= 0 && u.optIdx < numHours {
					u.busyToDraft = u.optIdx
				}
			case dropBusyFromM:
				if u.optMIdx >= 0 && u.optMIdx < numMinutes {
					u.busyFromMinDraft = u.optMIdx
				}
			case dropBusyToM:
				if u.optMIdx >= 0 && u.optMIdx < numMinutes {
					u.busyToMinDraft = u.optMIdx
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
		case WCopy:
			// COPY pill on a bubble's label row: stage the message text for
			// main() to put on the X11 clipboard, and light the pill briefly.
			if u.copyMsg >= 0 && u.copyMsg < len(u.msgs) {
				u.copiedText = u.msgs[u.copyMsg].Text
				u.wantCopy = true
				u.copyFlash = time.Now()
			}
		case WModal:
			if u.aboutOpen {
				u.press = WNone
				return u.closeAbout() // a backdrop click dismisses About
			}
			u.openDrop = dropNone // a click outside the widgets closes the list
		case WToggle:
			// The history show/hide button: the only collapse toggle -
			// a plain title-bar click just drags the window. Collapsing
			// keeps only the header + prompt box; expanding restores the
			// last non-collapsed height.
			if u.collapsed {
				u.collapsed = false
				u.H = max(u.expandedH, headerH+inputH)
			} else {
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

// Muted reports whether the settings dialog's mute checkbox is committed on,
// i.e. replies must not be spoken aloud. The main loop checks it right
// before handing a reply to the TTS engine.
func (u *UI) Muted() bool { return u.mute }

// WantClose reports whether the header's close button was clicked; the main
// loop exits when it is set.
func (u *UI) WantClose() bool { return u.wantClose }

// WantPet reports whether the header's "Haiya!" button was clicked, and
// consumes the flag (one-shot): a single click fires exactly one launch/quit
// action. Without the consume, the stale flag would re-fire on every later
// title-bar click - quitting the pet with a poof whenever the header was
// clicked to collapse/expand. The main loop decides what the click means
// from PetRunning: launch the onidia pet (button teal) or quit it with a
// poof-out (button pink).
func (u *UI) WantPet() bool {
	if !u.wantPet {
		return false
	}
	u.wantPet = false
	return true
}

// PetDemo reports whether the pet should run in demo mode (autonomous
// roaming + unsolicited chatter) per the settings dialog's DEMO MODE
// checkbox. False plants it at the screen edge, still reactive to app
// speech and still idly blinking.
func (u *UI) PetDemo() bool { return u.demo }

// WantPetRestart reports whether SAVE changed the demo mode of a pet that is
// currently running, and consumes the flag (one-shot): the main loop then
// quits and relaunches the pet so the new mode takes effect immediately.
func (u *UI) WantPetRestart() bool {
	if !u.wantPetRestart {
		return false
	}
	u.wantPetRestart = false
	return true
}

// SetPetRunning records whether the onidia pet application is running; the
// header button turns pink while it is, signalling that a click now quits it.
func (u *UI) SetPetRunning(running bool) { u.petRunning = running }

// PetRunning reports whether the onidia pet application is running.
func (u *UI) PetRunning() bool { return u.petRunning }

// PetCharacter reports which character the Haiya! button should launch for
// the gender chosen in the settings dialog: Kama for a boy, Onidia (the
// pet binary's own default) for a girl.
func (u *UI) PetCharacter() string {
	if normalizeGender(u.gender) == "boy" {
		return "kama"
	}
	return "onidia"
}

// WantCopy reports whether a message's COPY pill was clicked; main() then
// pushes the staged text onto the X11 clipboard.
func (u *UI) WantCopy() bool { return u.wantCopy }

// TakeCopiedText returns the copied message text and clears the want flag so
// the same click is not copied twice.
func (u *UI) TakeCopiedText() string {
	u.wantCopy = false
	return u.copiedText
}

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
	u.sleepFromMinDraft = minuteIndex(u.sleepFromMin)
	u.sleepToDraft = u.sleepTo
	if u.sleepToDraft < 0 {
		u.sleepToDraft = defaultSleepTo
	}
	u.sleepToMinDraft = minuteIndex(u.sleepToMin)
	u.busyFromDraft = u.busyFrom
	if u.busyFromDraft < 0 {
		u.busyFromDraft = defaultBusyFrom
	}
	u.busyFromMinDraft = minuteIndex(u.busyFromMin)
	u.busyToDraft = u.busyTo
	if u.busyToDraft < 0 {
		u.busyToDraft = defaultBusyTo
	}
	u.busyToMinDraft = minuteIndex(u.busyToMin)
	u.muteDraft = u.mute
	u.demoDraft = u.demo
	u.genderDraft = u.gender
	// The modal keeps its full designed size, so the window grows in height
	// when it is too short (prevH remembers the old height). collapsed is
	// deliberately left alone: the history shows exactly what it showed
	// before, and when it was collapsed the grown space is just the dim
	// backdrop behind the panel (Render draws no messages while collapsed).
	changed := false
	if u.H < minSettingsH {
		u.prevH = u.H
		u.H = minSettingsH
		changed = true
	}
	u.scroll = clamp(u.scroll, 0, u.maxScroll())
	return changed
}

// closeSettings hides the modal and restores the window height from before it
// opened. Returns true when the window must be resized.
func (u *UI) closeSettings() bool {
	u.settingsOpen = false
	u.openDrop = dropNone
	u.nameFocused = false
	u.hover, u.press = WNone, WNone
	u.scroll = clamp(u.scroll, 0, u.maxScroll())
	resized := false
	if u.prevH > 0 && u.H != u.prevH {
		u.H = u.prevH
		resized = true
	}
	u.prevH = 0
	return resized
}

// openAbout shows the About modal (see drawAbout). Like the settings modal it
// keeps its full designed size: the window grows in height when the panel does
// not fit (aboutPrevH remembers the old height), while collapsed is left alone
// so the conversation history is never expanded - the modal just overlays
// whatever was shown before it. Returns true when the window must be resized.
func (u *UI) openAbout() bool {
	u.aboutOpen = true
	u.hover, u.press = WNone, WNone
	changed := false
	if u.H < minAboutH {
		u.aboutPrevH = u.H
		u.H = minAboutH
		changed = true
	}
	return changed
}

// closeAbout hides the About modal and restores the window height from before
// it opened. Returns true when the window must be resized.
func (u *UI) closeAbout() bool {
	u.aboutOpen = false
	u.hover, u.press = WNone, WNone
	resized := false
	if u.aboutPrevH > 0 && u.H != u.aboutPrevH {
		u.H = u.aboutPrevH
		resized = true
	}
	u.aboutPrevH = 0
	return resized
}

// minuteIndex maps a committed minute (0/15/30/45) to its row index 0..3;
// values outside the step set clamp to the nearest step so legacy / hand-edited
// values keep working.
func minuteIndex(m int) int {
	idx := 0
	for i, step := range sleepMinutes {
		if step <= m {
			idx = i
		} else {
			break
		}
	}
	return idx
}

// toggleDrop opens the given dropdown list, closing any other. Opening a
// list scrolls the selection into view.
func (u *UI) toggleDrop(which int) {
	if u.openDrop == which {
		u.openDrop = dropNone
		return
	}
	u.openDrop = which
	switch which {
	case dropFrom, dropTo:
		sel := u.sleepFromDraft
		if which == dropTo {
			sel = u.sleepToDraft
		}
		u.hourScroll = clamp(sel-2, 0, numHours-visibleHourRows)
	case dropFromM, dropToM:
		sel := u.sleepFromMinDraft
		if which == dropToM {
			sel = u.sleepToMinDraft
		}
		u.minuteScroll = clamp(sel-1, 0, numMinutes-visibleMinuteRows)
	case dropBusyFrom, dropBusyTo:
		sel := u.busyFromDraft
		if which == dropBusyTo {
			sel = u.busyToDraft
		}
		u.hourScroll = clamp(sel-2, 0, numHours-visibleHourRows)
	case dropBusyFromM, dropBusyToM:
		sel := u.busyFromMinDraft
		if which == dropBusyToM {
			sel = u.busyToMinDraft
		}
		u.minuteScroll = clamp(sel-1, 0, numMinutes-visibleMinuteRows)
	}
}

// ScrollHourList scrolls the open FROM/TO hour list; returns false when no
// hour list is open, so the caller scrolls the message history instead.
func (u *UI) ScrollHourList(dy int) bool {
	if !u.settingsOpen || (u.openDrop != dropFrom && u.openDrop != dropTo && u.openDrop != dropBusyFrom && u.openDrop != dropBusyTo) {
		return false
	}
	u.hourScroll = clamp(u.hourScroll+dy, 0, numHours-visibleHourRows)
	if u.hover == WOption {
		// Keep the highlighted row inside the scrolled viewport.
		u.optIdx = clamp(u.optIdx, u.hourScroll, u.hourScroll+visibleHourRows-1)
	}
	return true
}

// ScrollMinuteList scrolls the open FROM/TO minute list; returns false when no
// minute list is open, so the caller scrolls the message history instead.
func (u *UI) ScrollMinuteList(dy int) bool {
	if !u.settingsOpen || (u.openDrop != dropFromM && u.openDrop != dropToM && u.openDrop != dropBusyFromM && u.openDrop != dropBusyToM) {
		return false
	}
	u.minuteScroll = clamp(u.minuteScroll+dy, 0, numMinutes-visibleMinuteRows)
	if u.hover == WOption {
		u.optMIdx = clamp(u.optMIdx, u.minuteScroll, u.minuteScroll+visibleMinuteRows-1)
	}
	return true
}

// saveSettings commits the modal's drafts: the persona picks them up live,
// character-name / character-age / sleep-time / mute / character-gender are
// rewritten in the INI file, and the stored system instruction is re-baked
// with the name and age (see bakeCharacterPrompt). Failures are reported in
// the modal, which then stays open.
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
	sleep := fmt.Sprintf("%02d:%02d-%02d:%02d", u.sleepFromDraft, sleepMinutes[u.sleepFromMinDraft], u.sleepToDraft, sleepMinutes[u.sleepToMinDraft])
	if err := SetConfigValue(path, "character", "sleep-time", sleep); err != nil {
		u.saveErr = err.Error()
		return
	}
	if err := SetConfigValue(path, "character", "mute", strconv.FormatBool(u.muteDraft)); err != nil {
		u.saveErr = err.Error()
		return
	}
	if err := SetConfigValue(path, "character", "demo-mode", strconv.FormatBool(u.demoDraft)); err != nil {
		u.saveErr = err.Error()
		return
	}
	if err := SetConfigValue(path, "character", "character-gender", u.genderDraft); err != nil {
		u.saveErr = err.Error()
		return
	}
	busy := fmt.Sprintf("%02d:%02d-%02d:%02d", u.busyFromDraft, sleepMinutes[u.busyFromMinDraft], u.busyToDraft, sleepMinutes[u.busyToMinDraft])
	if err := SetConfigValue(path, "character", "busy-time", busy); err != nil {
		u.saveErr = err.Error()
		return
	}
	// Write the name and age into the stored persona too: the INI's system
	// instruction has its "your name is ..." sentence rewritten, so the
	// character definition itself carries these values.
	if err := bakeCharacterPrompt(path, name, u.ageDraft); err != nil {
		u.saveErr = err.Error()
		return
	}
	u.saveErr = ""
	u.name = name
	u.age = u.ageDraft
	u.sleepFrom, u.sleepTo = u.sleepFromDraft, u.sleepToDraft
	u.sleepFromMin, u.sleepToMin = sleepMinutes[u.sleepFromMinDraft], sleepMinutes[u.sleepToMinDraft]
	u.busyFrom, u.busyTo = u.busyFromDraft, u.busyToDraft
	u.busyFromMin, u.busyToMin = sleepMinutes[u.busyFromMinDraft], sleepMinutes[u.busyToMinDraft]
	u.mute = u.muteDraft
	// A demo-mode flip must reach a running pet immediately, so flag a
	// restart (the main loop quits + relaunches it) — the checkbox would
	// otherwise only take effect on the next manual Haiya! click.
	if u.demo != u.demoDraft && u.petRunning {
		u.wantPetRestart = true
	}
	u.demo = u.demoDraft
	u.gender = u.genderDraft
	if u.Bot != nil {
		if name != "" {
			u.Bot.Name = name // bubble sender label
		}
		u.Bot.CharacterName = name
		u.Bot.CharacterAge = u.ageDraft
		u.Bot.SleepSet = true
		u.Bot.SleepFromH, u.Bot.SleepToH = u.sleepFromDraft, u.sleepToDraft
		u.Bot.SleepFromM, u.Bot.SleepToM = sleepMinutes[u.sleepFromMinDraft], sleepMinutes[u.sleepToMinDraft]
		u.Bot.SleepFrom, u.Bot.SleepTo = u.sleepFromDraft, u.sleepToDraft
		u.Bot.BusySet = true
		u.Bot.BusyFromH, u.Bot.BusyToH = u.busyFromDraft, u.busyToDraft
		u.Bot.BusyFromM, u.Bot.BusyToM = sleepMinutes[u.busyFromMinDraft], sleepMinutes[u.busyToMinDraft]
	}
	u.updateBusyState()
}

// updateBusyState checks if the current time falls within the busy window and
// updates the bot's display name accordingly: "Busy/Work" while busy, the
// configured character name otherwise. Returns true when the name changed.
func (u *UI) updateBusyState() bool {
	if u.Bot == nil || !u.Bot.BusySet {
		return false
	}
	now := time.Now()
	cur := now.Hour()*60 + now.Minute()
	from := u.busyFrom*60 + u.busyFromMin
	to := u.busyTo*60 + u.busyToMin
	busy := false
	if from <= to {
		busy = cur >= from && cur < to
	} else {
		// Window wraps past midnight (e.g. 22:00-07:00).
		busy = cur >= from || cur < to
	}
	want := u.name
	if busy {
		want = "Busy/Work"
	}
	if u.Bot.Name != want {
		u.Bot.Name = want
		return true
	}
	return false
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
	if u.aboutOpen {
		// While the About modal is up the textarea is dormant: Enter and
		// Escape both dismiss it (as does a click on OK or the backdrop).
		switch sym {
		case ksEscape, ksReturn, ksKPEnter:
			u.closeAbout()
			return true
		}
		return false
	}
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
	if u.aboutOpen {
		u.drawAbout(frame)
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
	// "ONIDIA" label is followed by the "Haiya!" launch button.
	drawText(frame, padX, (headerH-glyphH*uiFontScale)/2, "ONIDIA", uiFontScale, colWhite)

	// "Haiya!" button: runs the onidia pet application, or quits it (pink)
	// while the pet is running. Painted like the other header buttons.
	if hr := u.haiyaRect(); hr.Max.X < u.W { // keep it on-screen on tiny windows
		fill, glyphCol := colTealShade, colWhite
		border := colTealShade
		if u.petRunning {
			border = colHaiyaPinkLo
			fill, glyphCol = colHaiyaPink, colWhite
		}
		switch {
		case u.press == WHaiya:
			fill, glyphCol = colPlum, colWhite // press stays plum in both states
		case u.hover == WHaiya:
			if u.petRunning {
				fill, glyphCol = colHaiyaPinkHi, colWhite
			} else {
				fill, glyphCol = colHairLight, colPlum
			}
		}
		drawRoundRect(frame, hr.Min.X, hr.Min.Y, hr.Dx(), hr.Dy(), 8, border)
		drawRoundRect(frame, hr.Min.X+2, hr.Min.Y+2, hr.Dx()-4, hr.Dy()-4, 6, fill)
		drawText(frame, hr.Min.X+(hr.Dx()-textWidth(haiyaLabel, uiFontScale))/2,
			hr.Min.Y+(hr.Dy()-glyphH*uiFontScale)/2, haiyaLabel, uiFontScale, glyphCol)
	}

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

	// About button: a small square between the gear and the close button.
	ar := u.aboutRect()
	switch {
	case u.press == WAbout:
		fill, glyphCol = colPlum, colWhite
	case u.hover == WAbout:
		fill, glyphCol = colHairLight, colPlum
	default:
		fill, glyphCol = colTealShade, colWhite
	}
	drawRoundRect(frame, ar.Min.X, ar.Min.Y, hdrBtn, hdrBtn, 8, colTealShade)
	drawRoundRect(frame, ar.Min.X+2, ar.Min.Y+2, hdrBtn-4, hdrBtn-4, 6, fill)
	drawText(frame, ar.Min.X+(hdrBtn-textWidth("i", 1))/2,
		ar.Min.Y+(hdrBtn-glyphH)/2, "i", 1, glyphCol)

	// History toggle: a small square left of the gear button - the only
	// way to expand/collapse the conversation list (+ = hidden, - = shown).
	tr := u.toggleRect()
	switch {
	case u.press == WToggle:
		fill, glyphCol = colPlum, colWhite
	case u.hover == WToggle:
		fill, glyphCol = colHairLight, colPlum
	default:
		fill, glyphCol = colTealShade, colWhite
	}
	drawRoundRect(frame, tr.Min.X, tr.Min.Y, hdrBtn, hdrBtn, 8, colTealShade)
	drawRoundRect(frame, tr.Min.X+2, tr.Min.Y+2, hdrBtn-4, hdrBtn-4, 6, fill)
	icon := "+"
	if !u.collapsed {
		icon = "-"
	}
	drawText(frame, tr.Min.X+(hdrBtn-textWidth(icon, 1))/2,
		tr.Min.Y+(hdrBtn-glyphH)/2, icon, 1, glyphCol)

	sub := "AI HELPER"
	drawText(frame, tr.Min.X-padX-textWidth(sub, 1), (headerH-glyphH)/2, sub, 1,
		color.RGBA{255, 255, 255, 190})
}

// About modal ---------------------------------------------------------------

// drawAbout renders the About modal: a dim backdrop and a small centred panel
// with the app name, its tagline and the engineering credit, plus an OK
// button (a backdrop click dismisses it too).
func (u *UI) drawAbout(frame *image.NRGBA) {
	fillRect(frame, 0, 0, u.W, u.H, color.RGBA{40, 30, 55, 120}) // dim backdrop

	p := u.aboutPanel()
	drawRoundRect(frame, p.Min.X, p.Min.Y, p.Dx(), p.Dy(), winRadius, colPlum)
	drawRoundRect(frame, p.Min.X+2, p.Min.Y+2, p.Dx()-4, p.Dy()-4, winRadius-2, colBubbleFill)

	// Title: the app name in the header's larger scale, centred.
	center := func(s string, scale, y int, col color.RGBA) {
		drawText(frame, p.Min.X+(p.Dx()-textWidth(s, scale))/2, y, s, scale, col)
	}
	center("ONIDIA", uiFontScale, p.Min.Y+30, colPlum)

	// Tagline: "ONIDIA" initials spelling the phrase, plus the full wording.
	center("ONmIpresent DIgital Amigo", 1, p.Min.Y+62, colText)

	// Hairline divider between the tagline and the credit.
	fillRect(frame, p.Min.X+modalPad, p.Min.Y+86, p.Dx()-2*modalPad, 1, colInputBorder)

	// Engineering credit on one line, two-tone: muted label + teal link.
	credit := "Engineered by " + "https://mas-mas.it"
	cx := p.Min.X + (p.Dx()-textWidth(credit, 1))/2
	drawText(frame, cx, p.Min.Y+104, "Engineered by ", 1, colMuted)
	drawText(frame, cx+textWidth("Engineered by ", 1), p.Min.Y+104,
		"https://mas-mas.it", 1, colHeader)

	u.drawModalButton(frame, u.aboutOKRect(), WAboutOK, "OK")
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

	drawText(frame, p.Min.X+modalPad, p.Min.Y+170, "SLEEP TIME", uiFontScale, colPlum)
	fr, tr := u.sleepFromRect(), u.sleepToRect()
	fm, tm := u.sleepFromMinRect(), u.sleepToMinRect()
	drawText(frame, fr.Min.X, p.Min.Y+190, "FROM", 1, colMuted)
	drawText(frame, tr.Min.X, p.Min.Y+190, "TO", 1, colMuted)
	u.drawSelectBox(frame, fr, hourLabel(u.sleepFromDraft), u.openDrop == dropFrom, WDropFrom)
	u.drawSelectBox(frame, fm, minuteLabel(u.sleepFromMinDraft), u.openDrop == dropFromM, WDropFromM)
	u.drawSelectBox(frame, tr, hourLabel(u.sleepToDraft), u.openDrop == dropTo, WDropTo)
	u.drawSelectBox(frame, tm, minuteLabel(u.sleepToMinDraft), u.openDrop == dropToM, WDropToM)

	drawText(frame, p.Min.X+modalPad, p.Min.Y+240, "BUSY TIME", uiFontScale, colPlum)
	busyF, busyT := u.busyFromRect(), u.busyToRect()
	busyFM, busyTM := u.busyFromMinRect(), u.busyToMinRect()
	drawText(frame, busyF.Min.X, p.Min.Y+260, "FROM", 1, colMuted)
	drawText(frame, busyT.Min.X, p.Min.Y+260, "TO", 1, colMuted)
	u.drawSelectBox(frame, busyF, hourLabel(u.busyFromDraft), u.openDrop == dropBusyFrom, WDropFromB)
	u.drawSelectBox(frame, busyFM, minuteLabel(u.busyFromMinDraft), u.openDrop == dropBusyFromM, WDropFromBM)
	u.drawSelectBox(frame, busyT, hourLabel(u.busyToDraft), u.openDrop == dropBusyTo, WDropToB)
	u.drawSelectBox(frame, busyTM, minuteLabel(u.busyToMinDraft), u.openDrop == dropBusyToM, WDropToBM)

	u.drawMuteRow(frame)

	u.drawDemoRow(frame)

	u.drawGenderRow(frame)

	u.drawModalButtons(frame)
	if u.openDrop == dropAge {
		u.drawDropList(frame)
	}
	if u.openDrop == dropFrom || u.openDrop == dropTo ||
		u.openDrop == dropBusyFrom || u.openDrop == dropBusyTo {
		u.drawHourList(frame)
	}
	if u.openDrop == dropFromM || u.openDrop == dropToM ||
		u.openDrop == dropBusyFromM || u.openDrop == dropBusyToM {
		u.drawMinuteList(frame)
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

// drawHourList paints the expanded hour list (0..23) of the FROM/TO dropdowns:
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
	if u.openDrop == dropBusyFrom {
		sel = u.busyFromDraft
	}
	if u.openDrop == dropBusyTo {
		sel = u.busyToDraft
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
			hourLabel(i), uiFontScale, colText)
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

// drawMinuteList paints the expanded minute list (00/15/30/45) of the FROM/TO
// dropdowns: four rows visible at a time, the selected minute marked with a
// dot. Empty when no minute list is open.
func (u *UI) drawMinuteList(frame *image.NRGBA) {
	box := u.openMinuteBox()
	if box == (image.Rectangle{}) {
		return
	}
	l := u.minuteListRect()
	drawRoundRect(frame, l.Min.X, l.Min.Y, l.Dx(), l.Dy(), 8, colPlum)
	drawRoundRect(frame, l.Min.X+2, l.Min.Y+2, l.Dx()-4, l.Dy()-4, 6, colWhite)

	sel := u.sleepFromMinDraft
	if u.openDrop == dropToM {
		sel = u.sleepToMinDraft
	}
	if u.openDrop == dropBusyFromM {
		sel = u.busyFromMinDraft
	}
	if u.openDrop == dropBusyToM {
		sel = u.busyToMinDraft
	}
	for i := u.minuteScroll; i < u.minuteScroll+visibleMinuteRows && i < numMinutes; i++ {
		ry := l.Min.Y + (i-u.minuteScroll)*optH
		if u.hover == WOption && u.optMIdx == i {
			if i == 0 || i == numMinutes-1 || i == u.minuteScroll ||
				i == u.minuteScroll+visibleMinuteRows-1 {
				drawRoundRect(frame, l.Min.X+3, ry, l.Dx()-6, optH, 6, colHairLight)
			} else {
				fillRect(frame, l.Min.X+3, ry, l.Dx()-6, optH, colHairLight)
			}
		}
		drawText(frame, l.Min.X+26, ry+(optH-glyphH*uiFontScale)/2,
			minuteLabel(i), uiFontScale, colText)
		if i == sel {
			fillDisc(frame, l.Min.X+15, ry+optH/2, 3, colHeader)
		}
	}
}

// drawMuteRow paints the MUTE SPEECH checkbox and drawDemoRow the DEMO MODE
// one directly below it; both share drawCheckRow.
func (u *UI) drawMuteRow(frame *image.NRGBA) {
	u.drawCheckRow(frame, u.muteRect(), WMute, u.muteDraft, "MUTE SPEECH")
}

// drawDemoRow paints the DEMO MODE checkbox: checked means the pet roams and
// chatters on its own; unchecked plants it at the screen edge (still reactive
// and still idly blinking).
func (u *UI) drawDemoRow(frame *image.NRGBA) {
	u.drawCheckRow(frame, u.demoRect(), WDemo, u.demoDraft, "DEMO MODE")
}

// drawCheckRow paints one labelled checkbox: a rounded square that is white
// while unchecked and teal with a white tick while checked, next to its label.
// Hovering tints the border like the other modal controls.
func (u *UI) drawCheckRow(frame *image.NRGBA, r image.Rectangle, w Widget, on bool, label string) {
	border := colPlum
	if u.hover == w || u.press == w {
		border = colHeader
	}
	fill := colWhite
	if on {
		fill = colHeader
	}
	drawRoundRect(frame, r.Min.X, r.Min.Y, checkSide, checkSide, 6, border)
	drawRoundRect(frame, r.Min.X+2, r.Min.Y+2, checkSide-4, checkSide-4, 4, fill)
	if on {
		drawCheck(frame, r.Min.X, r.Min.Y, colWhite)
	}
	drawText(frame, r.Min.X+checkSide+10, r.Min.Y+(checkSide-glyphH)/2,
		label, 1, colMuted)
}

// drawCheck paints a chunky tick inside a checkSide-sized box at (x,y): two
// 3px-thick diagonal strokes meeting near the box's lower-left of centre.
func drawCheck(img *image.NRGBA, x, y int, col color.RGBA) {
	for i := 0; i < 5; i++ { // short arm: down-right to the vertex
		fillRect(img, x+3+i, y+8+i, 3, 3, col)
	}
	for i := 0; i < 8; i++ { // long arm: up-right from the vertex
		fillRect(img, x+8+i, y+12-i, 3, 3, col)
	}
}

// drawGenderRow paints the pet-character picker: a CHARACTER label above a
// centred ONIDIA/KAMA pair (the buttons carry the character names - Onidia is
// the girl, Kama the boy). The chosen side is filled like SAVE, the other
// stays an outlined button like CANCEL; the choice commits on SAVE and
// decides which character the Haiya! button launches. The MUTE SPEECH
// checkbox sits directly below this row.
func (u *UI) drawGenderRow(frame *image.NRGBA) {
	p := u.modalPanel()
	drawText(frame, p.Min.X+modalPad, p.Min.Y+genderRowY, "CHARACTER", 1, colMuted)
	girl, boy := u.genderRects()
	for _, b := range [...]struct {
		r   image.Rectangle
		w   Widget
		on  bool
		lbl string
	}{
		{girl, WGirl, u.genderDraft == "girl", "ONIDIA"},
		{boy, WBoy, u.genderDraft == "boy", "KAMA"},
	} {
		fill, label, outline := colBtnOff, colMuted, colInputBorder
		if b.on {
			fill, label, outline = colBtn, colWhite, colPlum
		}
		switch {
		case u.press == b.w:
			fill = colTealShade
		case u.hover == b.w:
			fill, label = colHairLight, colPlum
		}
		drawRoundRect(frame, b.r.Min.X, b.r.Min.Y, b.r.Dx(), b.r.Dy(), 9, outline)
		drawRoundRect(frame, b.r.Min.X+2, b.r.Min.Y+2, b.r.Dx()-4, b.r.Dy()-4, 7, fill)
		lw := textWidth(b.lbl, uiFontScale)
		drawText(frame, b.r.Min.X+(b.r.Dx()-lw)/2,
			b.r.Min.Y+(b.r.Dy()-glyphH*uiFontScale)/2, b.lbl, uiFontScale, label)
	}
}

// drawModalButtons paints CANCEL (muted) and SAVE (teal, like SEND).
func (u *UI) drawModalButtons(frame *image.NRGBA) {
	cancel, save := u.modalButtons()
	u.drawModalButton(frame, cancel, WCancel, "CANCEL")
	u.drawModalButton(frame, save, WSave, "SAVE")
}

func (u *UI) drawModalButton(frame *image.NRGBA, r image.Rectangle, w Widget, lbl string) {
	fill, label, outline := colBtnOff, colMuted, colInputBorder
	if w == WSave || w == WAboutOK {
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
	for i, b := range bs {
		if y+b.h > 0 && y < areaH {
			flash := i == u.copyMsg && time.Since(u.copyFlash) < copyFlashDur
			hover := u.hover == WCopy && i == u.copyMsg
			press := u.press == WCopy && i == u.copyMsg
			pill := i < len(u.msgs) && b.m.Text != "" // none on the synthetic "..." bubble
			u.drawMsgBlock(layer, b, y, pill, flash, hover, press)
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

func (u *UI) drawMsgBlock(layer *image.NRGBA, b msgBlock, y int, copyPill, copyFlash, copyHover, copyPress bool) {
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
	if copyPill { // textless and synthetic bubbles get no COPY pill
		u.drawCopyBtn(layer, copyBtnRect(b, bx, y), copyFlash, copyHover, copyPress)
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

// drawCopyBtn paints the COPY pill in a bubble's sender-label strip: muted
// while idle, teal on hover, plum while pressed, and it lights up teal for
// a moment after the click to confirm the text went to the clipboard.
func (u *UI) drawCopyBtn(layer *image.NRGBA, r image.Rectangle, flash, hover, press bool) {
	outline, col := colMuted, colMuted
	switch {
	case flash:
		outline, col = colHeader, colHeader
	case press:
		outline, col = colPlum, colPlum
	case hover:
		outline, col = colHeader, colHeader
	}
	drawRoundRect(layer, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 4, outline)
	lw := textWidth(copyLbl, 1)
	drawText(layer, r.Min.X+(r.Dx()-lw)/2, r.Min.Y+(r.Dy()-glyphH)/2, copyLbl, 1, col)
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
