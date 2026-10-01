package main

// ui.go - layout, state and rendering for the chat window.
//
// Like the desktop-pet, the whole interface is composed in software into one
// NRGBA frame per redraw: a header strip, a scrollable message list drawn
// into a clipped layer, and an input bar with the textarea and the SEND
// (submit) button. The palette reuses the pet's colors so both apps feel
// like one family.

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
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
	WThink      // thinking-bubble checkbox row (shares the mute row)
	WDemo       // demo-mode checkbox row (below mute)
	WAutoSubmit // auto-submit checkbox row (shares the demo row)
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

	// Transport strip under the messages (see drawMediaBar): the play/pause
	// toggle and the stop button of whatever a media agent is playing.
	WMediaPlay
	WMediaStop

	WMic // input-bar microphone button (drawn only when speech input is on)
)

// Msg is one chat entry.
type Msg struct {
	From  string // "you" or the bot's name
	Text  string
	Image image.Image // optional image to render inside the bubble
	Pages []string    // paragraphs; >1 turns the bubble into a pager (see Page)
	Page  int         // current page index into Pages (0 = first)
	// Calls / Results carry the non-text half of a native tool-calling
	// exchange (Phase 3, see toolcalls.go): a bot turn can report the abilities
	// the model asked for, and the user turn after it the outcomes handed back.
	// Both stay nil for ordinary chat messages; the renderer ignores them.
	Calls   []ToolCall
	Results []ToolResult
	// UsedAbility marks a bot turn that ran at least one ability. The renderer
	// ignores it; the copy of history built for the model appends a short
	// "an ability did this" note (agentbridge.historyEvidence) so the model
	// can see the ability worked. Without it, the only evidence in history is
	// the model's own past refusals, and it stops offering the ability at all.
	UsedAbility bool

	// Thinking is the model's reasoning, drawn as a thought cloud ABOVE the
	// speech bubble. Kept out of Text on purpose: Text is what gets copied,
	// paginated and sent onward, and none of that should carry the reasoning.
	Thinking string
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

	// Thought cloud (the model's <THINKING> reasoning). Deliberately muted
	// against the speech bubble so the two never read as the same thing: the
	// cloud is a quiet aside, the bubble is what the character actually says.
	colCloudFill = color.RGBA{236, 232, 246, 255} // pale lilac
	colCloudEdge = color.RGBA{186, 178, 208, 255} // soft grey-lilac rim

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

	// Thought-cloud metrics. The reasoning is drawn at scale 1 (half the
	// speech bubble's size) so a long ramble cannot crowd out the answer, and
	// capped to a few lines so one turn's thinking can never fill the window.
	thinkScale  = 1
	thinkLineH  = (glyphH + 2) * thinkScale // tighter pitch than the bubble's
	thinkPadX   = 10
	thinkPadY   = 8
	thinkStroke = 2 // outline width, the line-art look
	thinkGap    = 6 // vertical gap between the cloud and the block below it
	thinkMaxW   = 3 // the cloud is narrower than a bubble: 3/4 of the same max
	thinkMaxLn  = 6 // lines kept before the reasoning is elided

	// The cloud is never flatter than this width:height ratio, so a long ramble
	// cannot flatten the outline into a lozenge.
	thinkMaxAspect = 190 // width*100 / height, i.e. 1.9:1

	// The two dots trailing away below the cloud: the detail that makes it read
	// as "thinking" rather than as a small speech balloon.
	thinkDotR     = 4
	thinkDotGap   = 4
	thinkDotDrift = 8
	pagBtn        = 14 // prev/next page button side

	// "Copy" pill on a bubble's sender-label row.
	copyBtnPad   = 4 // padding around the Copy label inside its pill
	copyLbl      = "Copy"
	copyFlashDur = 1200 * time.Millisecond // lit after a successful copy

	// Transport strip (drawMediaBar): the "now playing" row with the
	// play/pause and stop buttons, shown only while an agent's player runs.
	mediaH    = 34 // strip height (between the messages and the input bar)
	mediaBtn  = 26 // transport button square
	mediaGap  = 8  // gap between the two buttons
	mediaLbl  = "NOW PLAYING"
	mediaMaxT = 64 // title rune cap (the strip is narrow)

	maxInput = 280 // textarea rune cap

	// Window shell corner rounding: 0 keeps the shell a square rectangle.
	// Rounding was tried two ways - zeroing the alpha in the corners (the
	// compositor alone decides whether that shows) and cutting a SHAPE
	// outline - and on a labwc/Xwayland desktop both left the corners as
	// visible blocks instead of clean see-through arcs. A square shell has no
	// such seam, so it is the default; raise it to re-enable the rounding.
	winRadius = 0

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

	checkSide   = 20  // checkbox square side (mute / thinking bubble / demo mode)
	checkColGap = 24  // gap between the two checkboxes sharing the mute row
	muteRowY    = 370 // mute-checkbox row top inside the panel (below the character picker)
	demoRowY    = 398 // demo-mode checkbox row top inside the panel (below mute)

	// About modal layout (drawAbout): a small informational panel shown by
	// the header's About button.
	aboutPanelW = 340 // panel width
	aboutPanelH = 280 // panel height (hero art + tagline + status + credit + OK)
	minAboutH   = 300 // window height forced while the About modal is open
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
	Stream   chan string      // accumulated partial bot text while SSE streams in
	Thinking bool             // true while a Gemini call is in flight

	msgs        []Msg
	streamText  string // preview shown in the synthetic bubble ("" = "...")
	streamThink string // <THINKING> preview for the synthetic thought cloud
	input       []rune
	scroll      int // scrollTop in content px (clamped; 0 = oldest visible)

	focused   bool // the textarea owns the keyboard
	caret     bool // caret blink phase
	collapsed bool // true hides the conversation history (prompt-only mode)

	expandedH int // last non-collapsed height; restored when expanding

	// Speech input (see stt.go). STT is the configured backend, nil when
	// speech input is off. STTSess is the take in flight, sttBusy the
	// transcription that follows it, and sttErr/sttNote the message shown in
	// the input bar - the note persists as "the transcript is ready" until
	// the user edits or sends it.
	STT       STT
	STTSess   *STTSession
	sttBusy   bool
	sttErr    string
	sttNote   string
	sttSince  time.Time
	sttDevice string // capture device ("" = system default)
	// sttAutoAt is when a pending auto-submit fires, or zero when none is
	// pending. It is armed only once the transcript has actually landed, so it
	// is never set while a take or a transcription is still in flight.
	sttAutoAt time.Time

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
	thinkDraft        bool      // thinking-bubble checkbox in the modal; committed on SAVE
	demo              bool      // committed demo mode: true = pet roams & chatters (default off)
	autoSubmit        bool      // committed: a finished transcript is sent on its own (INI "auto-submit"; default off)
	demoDraft         bool      // demo-mode checkbox in the modal; committed on SAVE
	autoSubmitDraft   bool      // auto-submit checkbox in the modal; committed on SAVE
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
	think             bool      // committed: draw the reasoning cloud above a reply (INI "thinking")
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

	// Transport strip (see media.go and drawMediaBar). mediaActive is the
	// mirror of "a player an agent started is still running"; the strip is
	// only drawn - and only takes part in the layout - while it is true.
	mediaActive bool
	mediaPaused bool
	mediaTitle  string
	wantMedia   string // one-shot transport command a button click queued ("" = none)
}

// NewUI creates a UI sized w x h with a welcome message from the bot.
// The conversation history starts collapsed so only the prompt box is visible.
func NewUI(w, h int) *UI {
	u := &UI{
		W: w, H: h,
		Bot:       NewBot(),
		Replies:   make(chan ReplyResult, 4),
		Stream:    make(chan string, 16),
		focused:   true,
		caret:     true,
		collapsed: true,
		expandedH: max(h, 260),
		gender:    "girl", // GIRL picker active until the INI says boy
		think:     true,   // reasoning cloud on unless the INI says thinking = off
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

// mediaBarH is the transport strip's height right now: mediaH while a player
// runs, 0 otherwise - so the strip only ever takes part in the layout (and
// grows the collapsed window) when it is actually shown.
func (u *UI) mediaBarH() int {
	if u.mediaActive {
		return mediaH
	}
	return 0
}

// mediaBar is the strip's row: directly above the input bar, full width. The
// zero rectangle means "hidden", which makes inRect report no hits.
func (u *UI) mediaBar() image.Rectangle {
	if h := u.mediaBarH(); h > 0 {
		return image.Rect(0, u.H-inputH-h, u.W, u.H-inputH)
	}
	return image.Rectangle{}
}

// mediaPlayRect is the play/pause toggle, the right-most button; mediaStopRect
// the stop button to its left.
func (u *UI) mediaPlayRect() image.Rectangle {
	b := u.mediaBar()
	if b.Empty() {
		return image.Rectangle{}
	}
	x := b.Max.X - padX - mediaBtn
	y := b.Min.Y + (mediaH-mediaBtn)/2
	return image.Rect(x, y, x+mediaBtn, y+mediaBtn)
}

func (u *UI) mediaStopRect() image.Rectangle {
	p := u.mediaPlayRect()
	if p.Empty() {
		return image.Rectangle{}
	}
	x := p.Min.X - mediaGap - mediaBtn
	return image.Rect(x, p.Min.Y, x+mediaBtn, p.Max.Y)
}

func (u *UI) msgArea() (y, h int) {
	if u.collapsed {
		return headerH, 0
	}
	return headerH, u.H - headerH - inputH - u.mediaBarH()
}

// micW is the side of the square microphone button in the input bar. It only
// takes space when speech input is enabled, so a default install looks
// exactly as before.
const micW = 44

// micEnabled reports whether the microphone button is shown at all.
func (u *UI) micEnabled() bool { return u.STT != nil }

// micRect is the microphone button, parked to the left of the SEND button.
// The textarea and the SEND button both shrink by micW to make room.
func (u *UI) micRect() image.Rectangle {
	top := u.H - inputH + (inputH-btnH)/2
	return image.Rect(u.W-btnW-btnGap-micW-padX, top, u.W-btnW-btnGap-padX, top+btnH)
}

// micSpace is the width the input bar reserves for the mic, 0 when off.
func (u *UI) micSpace() int {
	if u.micEnabled() {
		return micW + btnGap
	}
	return 0
}

func (u *UI) inputRect() image.Rectangle {
	return image.Rect(padX, u.H-inputH+inputPad, u.W-btnW-btnGap-u.micSpace()-padX, u.H-inputPad)
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

// autoRect is the auto-submit checkbox row: the box plus its label, so clicking
// either toggles the draft. It shares the DEMO MODE row to that row's right, and
// is derived from it for the same reason thinkRect derives from muteRect - the
// two can never drift apart.
func (u *UI) autoRect() image.Rectangle {
	d := u.demoRect()
	w := checkSide + 10 + textWidth("AUTO SUBMIT", 1)
	return image.Rect(d.Max.X+checkColGap, d.Min.Y,
		d.Max.X+checkColGap+w, d.Min.Y+checkSide)
}

// thinkRect is the thinking-bubble checkbox row: the box plus its label, so
// clicking either toggles the draft. It shares the MUTE SPEECH row, sitting to
// its right, and is derived from that row rather than pinned to its own Y so
// the two can never drift apart. Checked means a reply's reasoning is drawn in
// the cloud above its bubble; unchecked hides the cloud and lays the block out
// exactly as if the model had produced no reasoning at all.
func (u *UI) thinkRect() image.Rectangle {
	m := u.muteRect()
	w := checkSide + 10 + textWidth("THINKING BUBBLE", 1)
	return image.Rect(m.Max.X+checkColGap, m.Min.Y,
		m.Max.X+checkColGap+w, m.Min.Y+checkSide)
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
		if r := u.thinkRect(); inRect(x, y, r) {
			return WThink
		}
		if r := u.demoRect(); inRect(x, y, r) {
			return WDemo
		}
		if r := u.autoRect(); inRect(x, y, r) {
			return WAutoSubmit
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
	if b := u.mediaBar(); !b.Empty() && inRect(x, y, b) {
		// Transport strip: only its two buttons are live, the rest is the
		// (passive) "now playing" label.
		if inRect(x, y, u.mediaPlayRect()) {
			return WMediaPlay
		}
		if inRect(x, y, u.mediaStopRect()) {
			return WMediaStop
		}
		return WMediaPlay // whole strip toggles play/pause
	}
	if y >= u.H-inputH {
		br := u.buttonRect()
		if x >= br.Min.X && x < br.Max.X && y >= br.Min.Y && y < br.Max.Y {
			return WButton
		}
		// The mic sits to the left of SEND and is its own widget, so a click
		// on it must not fall through and focus the textarea.
		if u.micEnabled() && inRect(x, y, u.micRect()) {
			return WMic
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
		minH = headerH + inputH + u.mediaBarH()
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

// AddMsgUsed appends a plain text message and records whether an ability ran
// for it, so the next turn's model-facing history can say so.
func (u *UI) AddMsgUsed(from, text string, used bool) {
	m := newMsg(from, text, nil)
	m.UsedAbility = used
	u.msgs = append(u.msgs, m)
	u.scroll = u.maxScroll()
}

// AddThinking appends a bot message with a separate thought cloud above it.
func (u *UI) AddThinking(from, text, thinking string, img image.Image, used bool) {
	m := newMsg(from, text, img)
	m.UsedAbility = used
	m.Thinking = thinking
	u.msgs = append(u.msgs, m)
	u.scroll = u.maxScroll()
}

// AddMsgWithImageUsed appends a message that may include an image and records
// whether an ability ran for it.
func (u *UI) AddMsgWithImageUsed(from, text string, img image.Image, used bool) {
	m := newMsg(from, text, img)
	m.UsedAbility = used
	u.msgs = append(u.msgs, m)
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
		case WMic:
			u.ToggleMic()
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
		case WThink:
			u.thinkDraft = !u.thinkDraft // commits on SAVE, like the drafts
		case WDemo:
			u.demoDraft = !u.demoDraft // commits on SAVE, like the drafts
		case WAutoSubmit:
			u.autoSubmitDraft = !u.autoSubmitDraft // commits on SAVE, like the drafts
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
		case WMediaPlay:
			// Play/pause toggle. The queued command is consumed by main(),
			// which runs the media_control agent; the strip repaints from the
			// next state poll, so the glyph flips when the player really did.
			if u.mediaPaused {
				u.wantMedia = "resume"
			} else {
				u.wantMedia = "pause"
			}
		case WMediaStop:
			u.wantMedia = "stop"
		case WToggle:
			// The history show/hide button: the only collapse toggle -
			// a plain title-bar click just drags the window. Collapsing
			// keeps only the header + prompt box; expanding restores the
			// last non-collapsed height.
			if u.collapsed {
				u.collapsed = false
				u.H = max(u.expandedH, headerH+inputH+u.mediaBarH())
			} else {
				u.expandedH = u.H
				u.collapsed = true
				u.H = headerH + inputH + u.mediaBarH()
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

// SetMedia applies a fresh transport-strip state (media.go:currentMedia) and
// reports whether anything changed, so the caller only repaints on a real
// difference. The caller must also resize the window when MediaActive flips:
// the strip is part of the layout.
func (u *UI) SetMedia(st MediaState) bool {
	if u.mediaActive == st.Active && u.mediaPaused == st.Paused &&
		u.mediaTitle == st.Title {
		return false
	}
	oldBar := u.mediaBarH()
	u.mediaActive, u.mediaPaused, u.mediaTitle = st.Active, st.Paused, st.Title
	if u.collapsed && u.mediaBarH() != oldBar {
		// A collapsed window is exactly header + input bar, so the strip has
		// nowhere to go: grow (or shrink) the window with it. Expanded, the
		// strip just takes the height it needs from the message area.
		u.H = max(u.H+u.mediaBarH()-oldBar, headerH+inputH+u.mediaBarH())
	}
	return true
}

// MediaActive reports whether the transport strip is on screen.
func (u *UI) MediaActive() bool { return u.mediaActive }

// TakeMedia hands main() the transport command a button click queued, if any
// (one-shot, like WantPet/WantCopy: a second click before it is consumed wins
// the race, and an unconsumed command is never replayed).
func (u *UI) TakeMedia() string {
	cmd := u.wantMedia
	u.wantMedia = ""
	return cmd
}

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
	u.thinkDraft = u.think
	u.demoDraft = u.demo
	u.autoSubmitDraft = u.autoSubmit
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
	// Stored as on/off rather than true/false (the same convention as "tts")
	// because the cloud is on by default: a bool would make an INI that predates
	// this setting read as false and silently lose the reasoning.
	thinkVal := "off"
	if u.thinkDraft {
		thinkVal = "on"
	}
	if err := SetConfigValue(path, "character", "thinking", thinkVal); err != nil {
		u.saveErr = err.Error()
		return
	}
	if err := SetConfigValue(path, "character", "demo-mode", strconv.FormatBool(u.demoDraft)); err != nil {
		u.saveErr = err.Error()
		return
	}
	if err := SetConfigValue(path, "character", "auto-submit", strconv.FormatBool(u.autoSubmitDraft)); err != nil {
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
	// No pet restart is needed here, unlike demo mode: the transcript is laid
	// out from scratch every frame, so the clouds appear or vanish the moment
	// the checkbox is committed.
	u.think = u.thinkDraft
	// A demo-mode flip must reach a running pet immediately, so flag a
	// restart (the main loop quits + relaunches it) — the checkbox would
	// otherwise only take effect on the next manual Haiya! click.
	if u.demo != u.demoDraft && u.petRunning {
		u.wantPetRestart = true
	}
	u.demo = u.demoDraft
	// Unlike demo mode this needs no pet restart, and it must also drop any
	// send already counting down: the user has just said they do not want the
	// app sending on its own, so the one it is about to do is the last one.
	if !u.autoSubmitDraft {
		u.cancelAutoSubmit()
	}
	u.autoSubmit = u.autoSubmitDraft
	u.gender = u.genderDraft
	if u.Bot != nil {
		if name != "" {
			u.Bot.Name = name // bubble sender label
		}
		u.Bot.CharacterName = name
		// The pet draws her own cloud from the say-line's <THINKING> block, so
		// the same checkbox has to gate the line the Bot builds - otherwise
		// unchecking this row silences the window but not the character.
		u.Bot.PetThinkingOff = !u.thinkDraft
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
		// Editing the transcript is the clearest signal that the user wants to
		// keep control of it, so it cancels the pending send.
		if n := len(u.input); n > 0 {
			u.cancelAutoSubmit()
			u.input = u.input[:n-1]
			return true
		}
		return false
	case ksEscape:
		// Escape abandons a take in flight before it falls through to
		// clearing the textarea: a live recording is the more urgent thing
		// to stop.
		if u.STTSess != nil {
			u.CancelMic()
			return true
		}
		if len(u.input) > 0 {
			u.cancelAutoSubmit()
			u.input = nil
			u.sttNote = "" // a discarded transcript note is no longer true
			return true
		}
		return false
	}
	if r >= 0x20 && r <= 0x7e && len(u.input) < maxInput {
		u.cancelAutoSubmit() // the user is adding to it by hand
		u.input = append(u.input, r)
		return true
	}
	return false
}

// streamNewlines flattens every newline flavor a model reply might use (see
// newlineToPageBreak) into a single space for the streaming preview.
var streamNewlines = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", `\\n`, " ", "\f", " ")

// SetStreamText replaces the preview shown in the synthetic "..." bubble
// while an SSE reply is streaming in. Called from the main loop only - the
// reply goroutine hands text over via u.Stream and never touches UI state.
// Tags are stripped to match the final bubble, including the bracket markup
// scrubTags hides there (see finishReply) - every '[' is consumed with the
// group it opens, so half an arrived tag cannot flash into the bubble - and
// newlines collapse to spaces: the real page breaks are decided later by
// finishReply. The view follows the bottom only if the user was already there.
func (u *UI) SetStreamText(s string) {
	follow := u.scroll >= u.maxScroll()
	// Same split as the final bubble: reasoning goes to the cloud, the answer
	// to the speech bubble. A half-arrived <THINKING> block counts as open, so
	// mid-stream text lands in the cloud instead of flashing in the answer.
	think, answer := splitThinking(s)
	_, _, _, _, _, txt := stripTags(answer)
	txt, think = scrubTags(txt), scrubTags(think)
	oneLine := func(s string) string { return strings.TrimSpace(streamNewlines.Replace(s)) }
	u.streamText = oneLine(txt)
	u.streamThink = oneLine(think)
	if follow {
		u.scroll = u.maxScroll()
	}
}

// drawCircle fills a circle of radius rad centred on (cx, cy). The mic
// button is the only round widget, so this stays local to the UI file.
func drawCircle(frame *image.NRGBA, cx, cy, rad int, col color.RGBA) {
	for y := -rad; y <= rad; y++ {
		for x := -rad; x <= rad; x++ {
			if x*x+y*y <= rad*rad {
				px, py := cx+x, cy+y
				if image.Pt(px, py).In(frame.Bounds()) {
					frame.Set(px, py, col)
				}
			}
		}
	}
}

// ---- speech input -----------------------------------------------------------

// ToggleMic starts a take, or finishes the one in flight. It is the whole
// mic interaction: no mode flags beyond the session itself, which already
// knows whether it is recording.
func (u *UI) ToggleMic() {
	// Any mic interaction supersedes a send that is counting down: the user is
	// about to record again, so the words waiting to go out should just wait.
	u.cancelAutoSubmit()
	switch {
	case u.STT == nil:
		return
	case u.STTSess != nil:
		// Recording: stop and hand the audio to the backend.
		u.STTSess.Stop()
		u.sttBusy = true
		u.sttNote = "Transcribing…"
		u.sttErr = ""
		u.focused = true
	case u.sttBusy:
		// A transcription is already in flight; starting a second recorder
		// alongside it would talk over the first.
		return
	default:
		u.startMic()
	}
}

// startMic begins a fresh take, reporting a failure in the input bar rather
// than silently doing nothing.
func (u *UI) startMic() {
	sess, err := StartSTTSession(u.STT, findSTTRecorder(), u.sttDevice)
	if err != nil {
		u.sttErr, u.sttNote = err.Error(), ""
		return
	}
	if err := sess.Record(); err != nil {
		u.sttErr, u.sttNote = err.Error(), ""
		return
	}
	u.STTSess = sess
	u.sttErr, u.sttNote = "", "Recording… (press again to stop)"
	u.sttSince = time.Now()
}

// CancelMic abandons a take in flight (Escape). Safe to call when idle.
func (u *UI) CancelMic() {
	if u.STTSess != nil {
		u.STTSess.Cancel()
		u.STTSess = nil
		u.sttNote = ""
	}
}

// armAutoSubmit schedules the prompt to go out on its own, and says so in the
// input bar. Only ever called with words already in the textarea.
//
// The wording is held to what actually fits: at the default window width the
// note area is 21 columns, so anything longer is silently cut by fitCols and
// loses the part that matters - which key to press. TestAutoSubmitNoteFits
// pins that.
func (u *UI) armAutoSubmit() {
	u.sttAutoAt = time.Now().Add(sttAutoSubmitDelay)
	u.sttErr = ""
	u.sttNote = "Sending, Esc to stop"
}

// cancelAutoSubmit drops a pending send, leaving the text where it is. Every
// route out of the grace window goes through here: the user editing, sending,
// pressing Escape, starting another take, or switching the setting off.
func (u *UI) cancelAutoSubmit() { u.sttAutoAt = time.Time{} }

// autoSubmitPending reports whether a send is counting down.
func (u *UI) autoSubmitPending() bool { return !u.sttAutoAt.IsZero() }

// DrainSTT collects a finished take. It is non-blocking: a take that is still
// transcribing has nothing on the channel yet, so the next poll picks it up.
// Call it every frame from main.
func (u *UI) DrainSTT() {
	// The countdown is checked before the take is even looked at, because by the
	// time a transcript has landed sttBusy is already false and the guard below
	// would return first. Nothing else can be pending meanwhile: every mic
	// interaction cancels the countdown, so a new take and a pending send can
	// never overlap.
	if u.autoSubmitPending() {
		if time.Now().Before(u.sttAutoAt) {
			return // still inside the grace window
		}
		u.cancelAutoSubmit()
		u.sttNote = ""
		u.Submit()
		return
	}
	if u.STTSess == nil || !u.sttBusy {
		return
	}
	select {
	case res := <-u.STTSess.Done:
		u.STTSess = nil
		u.sttBusy = false
		switch {
		case errors.Is(res.Err, errSTTCanceled):
			// Abandoned on purpose: no message, no text.
		case res.Err != nil:
			u.sttErr, u.sttNote = res.Err.Error(), ""
		default:
			// The words go straight into the textarea, so there is nothing
			// to announce: the text appearing IS the confirmation, and a
			// "transcript ready" line would only sit on top of it.
			u.sttErr, u.sttNote = "", ""
			// Land the words in the textarea instead of sending them: a
			// misheard sentence is far cheaper to fix here than to re-record.
			if len(u.input) > 0 && !strings.HasSuffix(string(u.input), " ") {
				u.input = append(u.input, ' ')
			}
			u.input = append(u.input, []rune(res.Text)...)
			u.focused = true
			// With auto-submit on, the words still land in the textarea first
			// and the countdown starts from here - so the one second is a grace
			// window the user can read and interrupt, not a replacement for
			// showing them what was heard. An empty take arms nothing: there
			// would be nothing to send, and the note would flash for no reason.
			if u.autoSubmit && strings.TrimSpace(string(u.input)) != "" {
				u.armAutoSubmit()
			}
		}
	default:
		// Still transcribing.
	}
}

// Recording reports whether a take is in flight (for the live mic state).
func (u *UI) Recording() bool { return u.STTSess != nil && u.STTSess.Recording() }

// Busy reports whether a transcription is in flight. The main loop uses it to
// keep redrawing the animated mic while the backend works.
func (u *UI) Busy() bool { return u.sttBusy }

// Submit sends the current input: the message is appended to the history and
// the bot answers asynchronously (Gemini can take seconds; the UI shows a
// "..." bubble meanwhile and the reply arrives on u.Replies). Empty input is
// a no-op.
func (u *UI) Submit() {
	// Every send is final, including an automatic one, so a countdown that is
	// somehow still live must not fire a second time behind it.
	u.cancelAutoSubmit()
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
	u.streamText = ""        // no stale preview from the previous reply
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
	u.drawMediaBar(frame)
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

// drawAbout renders the About modal: a dim backdrop and a centred card with a
// teal hero strip carrying the word-art title (gradient drop shadow, plum
// outline, sparkles) beside the round character badge, then the tagline, a live
// status line naming whichever pet is up, and the engineering credit, plus an
// OK button (a backdrop click dismisses it too).
func (u *UI) drawAbout(frame *image.NRGBA) {
	fillRect(frame, 0, 0, u.W, u.H, color.RGBA{40, 30, 55, 120}) // dim backdrop

	p := u.aboutPanel()
	drawRoundRect(frame, p.Min.X, p.Min.Y, p.Dx(), p.Dy(), winRadius, colPlum)
	drawRoundRect(frame, p.Min.X+2, p.Min.Y+2, p.Dx()-4, p.Dy()-4, winRadius-2, colBubbleFill)

	// Hero strip in the app header's teal, so the card reads as part of the app
	// rather than a floating box.
	heroX, heroY := p.Min.X+10, p.Min.Y+10
	heroW, heroH := p.Dx()-20, 96
	drawRoundRect(frame, heroX, heroY, heroW, heroH, 12, colHeader)
	fillRect(frame, heroX+14, heroY+heroH-2, heroW-28, 2, colTealShade)

	// Word art: the biggest letter size that still fits next to the badge; on a
	// very narrow window the badge is dropped rather than squeezing the title.
	// 84 is the width the mark wants: the old hand-drawn portrait read fine at
	// 72, but the traced logo carries the fringe, the lashes and the tongue, and
	// at 72 those collapse into a band across the face. The strip is 96 tall,
	// so this is the largest diameter that still leaves a margin.
	const title = "ONIDIA"
	const badgeD = 84
	cy := heroY + heroH/2
	scale, withBadge := 2, false
	for _, s := range []int{4, 3, 2} {
		if textWidth(title, s)+badgeD+18 <= heroW-32 {
			scale, withBadge = s, true
			break
		}
		if textWidth(title, s) <= heroW-32 {
			scale = s
			break
		}
	}
	tw := textWidth(title, scale)
	titleX := heroX + (heroW-tw)/2
	if withBadge {
		titleX = heroX + (heroW-(tw+badgeD+18))/2
		drawFaceBadge(frame, titleX+badgeD/2, cy, badgeD/2)
		titleX += badgeD + 18
	}
	ty := cy - glyphH*scale/2
	od := max(1, scale/2) // outline thickness follows the letter size
	// Candy drop shadow (gradient), then the plum outline, then the solid white
	// face of the letters: that last pass keeps the glyphs crisp and is what the
	// About test looks for.
	drawTextGrad(frame, titleX+4, ty+4, title, scale, colHaiyaPinkLo, colTealShade)
	for _, d := range [][2]int{{-od, 0}, {od, 0}, {0, -od}, {0, od},
		{-od, -od}, {od, -od}, {-od, od}, {od, od}} {
		drawText(frame, titleX+d[0], ty+d[1], title, scale, colPlum)
	}
	drawText(frame, titleX, ty, title, scale, colWhite)
	// Twinkle trail beside the letters - only where the teal strip has room
	// for it, and always clear of the glyph ink. A strip nearly filled by
	// the word art (narrow window) gets no sparkles rather than one crowded
	// against the rounded edge.
	free := heroX + heroW - (titleX + tw)
	switch {
	case free >= 40:
		drawSparkle(frame, titleX+tw+12, heroY+26, 4, colHairLight)
		drawSparkle(frame, titleX+tw+26, cy+6, 3, colWhite)
		drawSparkle(frame, titleX+tw+12, heroY+heroH-26, 5, colWhite)
	case free >= 24:
		drawSparkle(frame, titleX+tw+12, heroY+30, 4, colHairLight)
		drawSparkle(frame, titleX+tw+12, heroY+heroH-30, 4, colWhite)
	}

	// Tagline and the app's one-liner, centred under the strip.
	lineY := heroY + heroH + 14
	center := func(s string, y int, col color.RGBA) {
		if textWidth(s, 1) > p.Dx()-2*modalPad {
			return // too wide for a narrow panel: drop it rather than spill
		}
		drawText(frame, p.Min.X+(p.Dx()-textWidth(s, 1))/2, y, s, 1, col)
	}
	center("OmNIpresent DIgital Amigo", lineY, colText)
	center("Sparking curiosity, one question at a time", lineY+16, colMuted)

	// Live status: the name from the settings' CHARACTER picker plus a
	// colour-coded dot, so the modal says whether anyone is home.
	name := strings.ToUpper(u.PetCharacter())
	status, dot := name+" is running", colBtn
	if !u.petRunning {
		status, dot = name+" is asleep - press Haiya! to call her", colMuted
	}
	if textWidth(status, 1) > p.Dx()-2*modalPad-12 { // narrow window: shorter
		status = name + " asleep"
		if u.petRunning {
			status = name + " running"
		}
	}
	sw := textWidth(status, 1)
	sx := p.Min.X + (p.Dx()-(sw+12))/2
	fillDisc(frame, sx+4, lineY+36, 3, dot)
	drawText(frame, sx+12, lineY+32, status, 1, colText)

	// Hairline divider between the status and the credit.
	fillRect(frame, p.Min.X+modalPad, lineY+72, p.Dx()-2*modalPad, 1, colInputBorder)

	// Engineering credit on one line, two-tone: muted label + teal link.
	// On a narrow panel the label is dropped so the link still fits.
	const label, link = "Engineered by ", "https://mas-mas.it"
	line := label + link
	if textWidth(line, 1) > p.Dx()-2*modalPad {
		line = link
	}
	cx := p.Min.X + (p.Dx()-textWidth(line, 1))/2
	if line == link {
		drawText(frame, cx, lineY+84, link, 1, colHeader)
	} else {
		drawText(frame, cx, lineY+84, label, 1, colMuted)
		drawText(frame, cx+textWidth(label, 1), lineY+84, link, 1, colHeader)
	}

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
	u.drawThinkRow(frame)

	u.drawDemoRow(frame)
	u.drawAutoRow(frame)

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

// drawMuteRow paints the MUTE SPEECH checkbox, drawThinkRow the THINKING
// BUBBLE one sharing that row, and drawDemoRow the DEMO MODE one below; all
// three share drawCheckRow.
func (u *UI) drawMuteRow(frame *image.NRGBA) {
	u.drawCheckRow(frame, u.muteRect(), WMute, u.muteDraft, "MUTE SPEECH")
}

// drawThinkRow paints the THINKING BUBBLE checkbox: checked draws a reply's
// reasoning in the cloud above its bubble, unchecked hides the cloud.
func (u *UI) drawThinkRow(frame *image.NRGBA) {
	u.drawCheckRow(frame, u.thinkRect(), WThink, u.thinkDraft, "THINKING BUBBLE")
}

// drawDemoRow paints the DEMO MODE checkbox: checked means the pet roams and
// chatters on its own; unchecked plants it at the screen edge (still reactive
// and still idly blinking).
func (u *UI) drawDemoRow(frame *image.NRGBA) {
	u.drawCheckRow(frame, u.demoRect(), WDemo, u.demoDraft, "DEMO MODE")
}

// drawAutoRow paints the AUTO SUBMIT checkbox beside DEMO MODE: checked sends a
// finished transcript on its own after a short grace window, unchecked leaves it
// in the textarea for the user to read and send.
func (u *UI) drawAutoRow(frame *image.NRGBA) {
	u.drawCheckRow(frame, u.autoRect(), WAutoSubmit, u.autoSubmitDraft, "AUTO SUBMIT")
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

	// Thought cloud (m.Thinking). Laid out above the label strip, as its own
	// little block: the cloud is the reasoning, the bubble is the answer, and
	// they are different things at different sizes.
	thinkLines []string
	thinkW     int
	thinkH     int
}

func (u *UI) blocks() []msgBlock {
	cols := u.bubbleCols()
	maxW := (u.W - 2*padX) * 3 / 4
	bs := make([]msgBlock, 0, len(u.msgs)+1)
	for _, m := range u.msgs {
		bs = append(bs, u.blockFor(m, cols, maxW))
	}
	if u.Thinking {
		// Synthetic bubble while the call is in flight: "..." until the first
		// SSE delta arrives, then the streaming preview. The <THINKING> half
		// goes to its own cloud, exactly where it lands in the final bubble.
		txt, think := "...", u.streamThink
		if u.streamText != "" {
			txt = u.streamText
		}
		bs = append(bs, u.blockFor(Msg{From: u.Bot.Name, Text: txt, Thinking: think}, cols, maxW))
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
	b := msgBlock{m: m, lines: lines, bubW: bubW, bubH: bubH, h: h, img: img, paginated: paginated, pageCount: len(m.Pages)}
	// The thought cloud is measured at the smaller font and stacks on top of
	// everything else, so the block's total height grows by the cloud's height
	// plus the gap that keeps it off the label strip. The THINKING BUBBLE
	// checkbox gates it right here, at measure time: a hidden cloud leaves the
	// block laid out exactly as if the model had produced no reasoning at all,
	// so the answer bubble simply moves up rather than leaving a hole. One gate
	// covers both the settled reasoning and the live streaming preview, since
	// both arrive here as Msg.Thinking.
	if t := strings.TrimSpace(m.Thinking); t != "" && u.think {
		// Wrap to the real body budget, then clamp each line to it in pixels.
		// fitThinkLines is the safety net for an unbreakable token - a word
		// longer than the body - not for the ordinary case: wrapping to the
		// body means no word is elided to make the cloud fit.
		bodyMax := maxW * thinkMaxW / 4
		b.thinkLines = capLines(fitThinkLines(wrapText(t, thinkCols(maxW)), bodyMax-2*thinkPadX), thinkMaxLn)
		tw := 0
		for _, l := range b.thinkLines {
			tw = max(tw, textWidth(l, thinkScale))
		}
		// The body is the text box, floored to keep the outline rounded rather
		// than a flat lozenge, plus room for the trailing dots.
		bw := tw + 2*thinkPadX
		bh := len(b.thinkLines)*thinkLineH + 2*thinkPadY
		if minH := bw * 100 / thinkMaxAspect; bh < minH {
			bh = minH
		}
		b.thinkW = min(bw, bodyMax)
		b.thinkH = bh + thinkDotsHeight()
		b.h += b.thinkH + thinkGap
	}
	return b
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

// thinkCols is the reasoning's wrap width in glyph cells for a bubble cap of
// maxW pixels: the cloud body's budget minus its padding, converted to cells.
// At scale 1 each glyph is half as wide as in a bubble, so this fits about
// twice the characters a bubble line does.
//
// Deriving it from the same maxW the body is sized with is the point. The body
// is a FRACTION of that cap (thinkMaxW/4 of it), and the old code did not wrap
// to the real window at all: it divided by thinkMaxW and multiplied by 3, which
// cancels, so every line was measured against the full bubble width and then
// centred straight through the outline and off the edge of the window.
func thinkCols(maxW int) int {
	body := maxW * thinkMaxW / 4
	return max(12, (body-2*thinkPadX)/(advW*thinkScale))
}

// fitThinkLines hard-splits any line wider than the cloud's text budget, so no
// line can be wider than the body it is centred in. Wrapping already targets
// that budget, so this only ever bites on an unbreakable token - a "word" longer
// than the body - but it is what makes the bound absolute rather than nominal.
func fitThinkLines(lines []string, budget int) []string {
	// textWidth is n*advW*scale-scale, so cells and pixels are a straight
	// conversion. Work in cells, convert once.
	cells := budget / (advW * thinkScale)
	if cells < 1 {
		cells = 1
	}
	// Room for the marker is only reserved on a line actually being cut: a line
	// that already fits keeps its full cell budget.
	cut := max(1, cells-len("..."))
	out := make([]string, len(lines))
	for i, l := range lines {
		r := []rune(l)
		if len(r) <= cells {
			out[i] = l
			continue
		}
		if len(r) > cut {
			r = r[:cut]
		}
		out[i] = strings.TrimRight(string(r), " .,;:") + "..."
	}
	return out
}

// capLines keeps at most n lines, marking the cut with an ellipsis on the last
// one. The reasoning is an aside; an unbounded monologue must not push the
// answer out of view.
func capLines(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	out := append([]string(nil), lines[:n]...)
	out[n-1] = strings.TrimRight(out[n-1], " .,;:") + "..."
	return out
}

// thinkBlob is one circle in the thought cloud's outline.
type thinkBlob struct{ cx, cy, r int }

// thinkBlobR is the nominal lobe radius for a text area of tw x th: about a
// third of the short side, so the lobes are big and round.
func thinkBlobR(tw, th int) int { return max(5, min(20, min(tw, th)/3)) }

// thinkLobes returns the circles whose union forms the cloud body.
//
// Each disc is placed so its OUTER edge is tangent to the ellipse: the centre
// sits one radius in along the outward normal. That is what makes the union a
// clean scalloped oval instead of discs floating on or inside it.
//
// The discs are spaced by ARC LENGTH, not by angle. On a wide ellipse, even
// angular steps bunch at the curved ends and stretch along the flats, which
// leaves a notch on one side.
//
// Radii vary a little (+/-20%), deterministically from the index: enough to
// read as a drawn cloud, not enough to open a gap. Deterministic because the
// cloud is redrawn every frame - a random radius would make it shimmer.
func thinkLobes(tw, th, base int) []thinkBlob {
	a, b := float64(tw)/2, float64(th)/2
	thetas := thinkArcAngles(a, b, base)
	out := make([]thinkBlob, 0, len(thetas))
	for i, t := range thetas {
		frac := int(uint32(i*2654435761) % 1000)
		r := base * (80 + frac*40/1000) / 100
		ct, st := math.Cos(t), math.Sin(t)
		nx, ny := b*ct, a*st // outward normal, normalised
		if L := math.Hypot(nx, ny); L > 0 {
			nx, ny = nx/L, ny/L
		}
		out = append(out, thinkBlob{
			cx: int(a*ct - float64(r)*nx),
			cy: int(b*st - float64(r)*ny),
			r:  r,
		})
	}
	return out
}

// thinkArcAngles returns angles spread evenly by ARC LENGTH around the ellipse,
// with the count chosen so neighbouring discs overlap by roughly a third of a
// radius.
func thinkArcAngles(a, b float64, r int) []float64 {
	const samples = 720
	p := math.Pi * (3*(a+b) - math.Sqrt((3*a+b)*(a+3*b))) // Ramanujan
	n := max(8, min(28, int(p/(1.25*float64(r)))))
	cum := make([]float64, samples+1)
	px, py := a, 0.0
	for i := 1; i <= samples; i++ {
		t := 2 * math.Pi * float64(i) / float64(samples)
		x, y := a*math.Cos(t), b*math.Sin(t)
		cum[i] = cum[i-1] + math.Hypot(x-px, y-py)
		px, py = x, y
	}
	total := cum[samples]
	out := make([]float64, 0, n)
	j := 1
	for i := 0; i < n; i++ {
		target := total * float64(i) / float64(n)
		for j < samples && cum[j] < target {
			j++
		}
		out = append(out, 2*math.Pi*float64(j)/float64(samples))
	}
	return out
}

// drawThinkBlob outlines one circle: rim, then fill inset by sw, so every circle
// in the cloud gets the same stroke weight as the body.
func drawThinkBlob(layer *image.NRGBA, cx, cy, r, sw int, rim, fill color.RGBA) {
	fillDisc(layer, cx, cy, r, rim)
	if r-sw > 0 {
		fillDisc(layer, cx, cy, r-sw, fill)
	}
}

// thinkFillEllipse paints a solid ellipse row by row. The discs draw the
// outline; this only has to make the middle solid.
func thinkFillEllipse(layer *image.NRGBA, cx, cy, a, b int, col color.RGBA) {
	if a <= 0 || b <= 0 {
		return
	}
	for dy := -b; dy <= b; dy++ {
		f := 1 - float64(dy*dy)/float64(b*b)
		if f < 0 {
			continue
		}
		hw := int(float64(a) * math.Sqrt(f))
		fillRect(layer, cx-hw, cy+dy, 2*hw, 1, col)
	}
}

// drawThinkCloud paints the thought cloud into the layer at (x,y): the scalloped
// oval body plus the two dots trailing below it. w/h are the OUTER bounds,
// including the dot trail, matching what blockFor reserved.
func drawThinkCloud(layer *image.NRGBA, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	dotH := thinkDotsHeight()
	bodyW, bodyH := w, h-dotH
	if bodyW <= 0 || bodyH <= 0 {
		return
	}
	ox, oy := x+bodyW/2, y+bodyH/2
	base := thinkBlobR(bodyW, bodyH)
	lobes := thinkLobes(bodyW, bodyH, base)

	// Rim pass: interior ellipse + every disc.
	thinkFillEllipse(layer, ox, oy, bodyW/2, bodyH/2, colCloudEdge)
	for _, l := range lobes {
		fillDisc(layer, ox+l.cx, oy+l.cy, l.r, colCloudEdge)
	}
	// Fill pass, inset by the stroke: the same shape, shrunk.
	thinkFillEllipse(layer, ox, oy, bodyW/2-thinkStroke, bodyH/2-thinkStroke, colCloudFill)
	for _, l := range lobes {
		fillDisc(layer, ox+l.cx, oy+l.cy, l.r-thinkStroke, colCloudFill)
	}
	// The trailing dots, stepping down-left and shrinking.
	d1x := ox - bodyW/10
	d1y := oy + bodyH/2 + thinkDotGap + thinkDotR
	drawThinkBlob(layer, d1x, d1y, thinkDotR, 1, colCloudEdge, colCloudFill)
	drawThinkBlob(layer, d1x-thinkDotDrift, d1y+thinkDotR+thinkDotGap,
		thinkDotR-1, 1, colCloudEdge, colCloudFill)
}

// thinkDotsHeight is the vertical room the trailing dots need below the body.
func thinkDotsHeight() int {
	return thinkDotGap + 3*thinkDotR + thinkDotGap - 1
}

func (u *UI) drawMsgBlock(layer *image.NRGBA, b msgBlock, y int, copyPill, copyFlash, copyHover, copyPress bool) {
	isBot := b.m.From != "you"
	imgH := 0
	if b.img != nil {
		imgH = b.img.Bounds().Dy()
	}
	// The cloud sits at the very top of the block, above the label strip. It is
	// left-aligned with the bubble (bot) or right-aligned (user) so the two read
	// as one stack rather than two unrelated shapes.
	if len(b.thinkLines) > 0 {
		var cx int
		if isBot {
			cx = padX
		} else {
			cx = u.W - padX - b.thinkW
		}
		drawThinkCloud(layer, cx, y, b.thinkW, b.thinkH)
		// Text centred in the cloud body.
		tw := 0
		for _, l := range b.thinkLines {
			tw = max(tw, textWidth(l, thinkScale))
		}
		tx := cx + (b.thinkW-tw)/2
		ty := y + (b.thinkH-thinkDotsHeight()-len(b.thinkLines)*thinkLineH)/2
		for _, l := range b.thinkLines {
			drawText(layer, tx, ty, l, thinkScale, colText)
			ty += thinkLineH
		}
		y += b.thinkH + thinkGap
	}

	bubH := b.h - labelH - b.thinkH - thinkGap
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

// drawMediaBar paints the transport strip: "NOW PLAYING <title>" on the left,
// a play/pause toggle and a stop button on the right. It is only called with
// the strip visible (Render guards on the same mediaActive state the geometry
// uses), and the glyphs are drawn as plain shapes: the bitmap font has no media
// symbols, and hand-drawn bars/squares/triangles match the other icons (see
// drawChevron, drawGear).
func (u *UI) drawMediaBar(frame *image.NRGBA) {
	b := u.mediaBar()
	if b.Empty() {
		return
	}
	fillRect(frame, b.Min.X, b.Min.Y, b.Dx(), 1, colInputBorder) // top hairline

	lbl, lblCol := mediaLbl, colMuted
	if u.mediaPaused {
		lbl, lblCol = "PAUSED", colPlum // readable without decoding the glyph
	}
	y := b.Min.Y + (mediaH-glyphH)/2
	drawText(frame, padX, y, lbl, 1, lblCol)

	// Title: clipped to the room left between the label and the buttons.
	tx := padX + textWidth(lbl, 1) + 6
	if avail := u.mediaStopRect().Min.X - btnGap - tx; avail > cellW {
		drawText(frame, tx, y, ellipsize(oneLine(u.mediaTitle), 1, avail), 1, colText)
	}

	u.drawMediaBtn(frame, u.mediaStopRect(), WMediaStop, false)
	u.drawMediaBtn(frame, u.mediaPlayRect(), WMediaPlay, u.mediaPaused)
}

// drawMediaBtn paints one transport button (a round square) plus its glyph,
// with the same hover/press feedback the other buttons use. play is the
// play/pause toggle: it draws the resume triangle while paused, the two pause
// bars while playing.
func (u *UI) drawMediaBtn(frame *image.NRGBA, r image.Rectangle, w Widget, resume bool) {
	if r.Empty() {
		return
	}
	fill, col := colBubbleFill, colPlum
	switch {
	case u.press == w:
		fill = colHairLight
	case u.hover == w:
		fill = colBtnOff
	}
	drawRoundRect(frame, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), 7, colInputBorder)
	drawRoundRect(frame, r.Min.X+1, r.Min.Y+1, r.Dx()-2, r.Dy()-2, 6, fill)

	cx, cy := r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2
	switch {
	case w == WMediaPlay && resume:
		fillTriangleRight(frame, cx-3, cy, cx+5, cy-5, col)
	case w == WMediaPlay:
		fillRect(frame, cx-4, cy-4, 3, 9, col) // two bars, 3px apart
		fillRect(frame, cx+2, cy-4, 3, 9, col)
	default: // stop: a filled square
		fillRect(frame, cx-4, cy-4, 9, 9, col)
	}
}

// fillTriangleRight draws the play/resume glyph pointing right. The three
// points are filled row by row (no anti-aliasing, like the other hand-drawn
// shapes); x1 is the tip, yTop the top base corner, cy the middle row.
func fillTriangleRight(img *image.NRGBA, x0, cy, x1, yTop int, col color.RGBA) {
	h := cy - yTop
	for dy := 0; dy <= h; dy++ {
		y := yTop + dy
		p := cy - y
		if p < 0 { // mirror row below the midline
			p = -p
		}
		if p > h {
			continue
		}
		fillRect(img, x0, y, (x1-x0)*(h-p)/h, 1, col)
	}
}

// ellipsize cuts s to fit maxW pixels at the given scale, appending an ellipsis
// when it had to cut (a one-line title, not a word-wrapped paragraph).
func ellipsize(s string, scale, maxW int) string {
	if maxW <= 0 || textWidth(s, scale) <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 1 && textWidth(string(r)+"...", scale) > maxW {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}

// oneLine flattens a string to a single line: the strip draws one row, and an
// agent-supplied title must not be able to paint over the rest of the window.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
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
	// The status line replaces the placeholder, so a recording or a finished
	// transcript is visible without stealing a chat row. Both lines are cut
	// to the textarea's column count: a fixed character cap would overflow
	// the border on a narrow window.
	cols := max(4, (ta.Dx()-20)/cellW)
	if u.sttNote != "" {
		drawText(frame, ta.Min.X+10, ta.Min.Y+7, fitCols(u.sttNote, cols), uiFontScale, colMuted)
	} else {
		u.drawInputText(frame, ta.Min.X+10, ta.Min.Y+7, ta.Dx()-20)
	}
	if u.sttErr != "" {
		drawText(frame, ta.Min.X+10, ta.Min.Y+ta.Dy()/2, fitCols("mic: "+u.sttErr, cols), uiFontScale, colError)
	}
	if u.sttErr != "" {
		drawText(frame, ta.Min.X+10, ta.Min.Y+ta.Dy()/2, truncate("mic: "+u.sttErr, 42), uiFontScale, colError)
	}

	u.drawButton(frame)
	u.drawMic(frame)
}

// fitCols trims s to at most n columns, marking the cut with an ellipsis, so
// a status line never spills past the textarea border.
func fitCols(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// drawMic paints the microphone button: a round mic glyph that turns into a
// stop square while recording, with a pulsing halo so a live take is obvious
// even when the app is not focused.
func (u *UI) drawMic(frame *image.NRGBA) {
	if !u.micEnabled() {
		return
	}
	r := u.micRect()
	cx, cy := r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2

	fill, label, outline := colBtn, colWhite, colPlum
	switch {
	case u.Recording():
		fill, label, outline = colError, colWhite, colPlum
	case u.sttBusy:
		fill, label, outline = colBtnOff, colMuted, colInputBorder
	case u.press == WMic:
		fill = colTealShade
	case u.hover == WMic:
		fill, label = colHairLight, colPlum
	}
	if u.Recording() {
		// A slow breathing halo, one full cycle per second.
		if phase := int(time.Since(u.sttSince).Milliseconds()/125) % 2; phase == 0 {
			drawCircle(frame, cx, cy, micW/2+3, colError)
		}
	}
	drawCircle(frame, cx, cy, micW/2, outline)
	drawCircle(frame, cx, cy, micW/2-2, fill)

	// Glyph: a capsule mic with its stand, or a stop square while recording.
	if u.Recording() {
		s := 6
		drawRoundRect(frame, cx-s/2, cy-s/2, s, s, 2, label)
	} else {
		w, h := 8, 13
		drawRoundRect(frame, cx-w/2, cy-h/2-2, w, h, 4, label)
		fillRect(frame, cx-1, cy+2, 2, 5, label)  // stand
		fillRect(frame, cx-5, cy+6, 10, 2, label) // base
	}
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
