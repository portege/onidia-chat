package main

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCollapseDefault verifies the conversation history starts collapsed so
// only the prompt box is visible. NewUI keeps the passed window height (main
// opens the window at the collapsed size; previews set the size explicitly).
func TestCollapseDefault(t *testing.T) {
	u := NewUI(380, 520)
	if !u.Collapsed() {
		t.Fatal("NewUI should start collapsed")
	}
	if _, h := u.msgArea(); h != 0 {
		t.Errorf("msgArea height while collapsed: got %d want 0", h)
	}
	if u.expandedH != 520 {
		t.Errorf("expandedH: got %d want 520", u.expandedH)
	}
	// A prompt-only window (header + input bar, no message area) renders the
	// exact collapsed height and never draws the message bubbles.
	u.H = headerH + inputH
	frame := u.Render()
	if frame.Bounds().Dy() != headerH+inputH {
		t.Errorf("collapsed frame height: got %d want %d (header+input)",
			frame.Bounds().Dy(), headerH+inputH)
	}
}

// TestToggleCollapse exercises a header click: press+release on WHeader
// toggles the state and resizes H to the collapsed/expanded height.
func TestToggleCollapse(t *testing.T) {
	u := NewUI(380, 520)

	// Expand via header click.
	u.Press(WHeader)
	if !u.Release(WHeader) {
		t.Fatal("Release(WHeader) on a collapsed UI should return true (state toggled)")
	}
	if u.Collapsed() {
		t.Fatal("header click should expand the conversation")
	}
	if u.H != 520 {
		t.Errorf("expanded height: got %d want 520", u.H)
	}
	if _, h := u.msgArea(); h != 520-headerH-inputH {
		t.Errorf("msgArea height expanded: got %d", h)
	}

	// Collapse again; the expanded height must be remembered.
	u.H = 340 // simulate a user resize before collapsing
	u.Press(WHeader)
	if !u.Release(WHeader) {
		t.Fatal("Release(WHeader) on an expanded UI should return true")
	}
	if !u.Collapsed() {
		t.Fatal("header click should collapse the conversation")
	}
	if u.H != headerH+inputH {
		t.Errorf("collapsed height: got %d want %d", u.H, headerH+inputH)
	}
	if u.expandedH != 340 {
		t.Errorf("expandedH remembered: got %d want 340", u.expandedH)
	}
	if _, h := u.msgArea(); h != 0 {
		t.Errorf("msgArea height while collapsed: got %d want 0", h)
	}

	// And expand again restores the remembered height.
	u.Press(WHeader)
	u.Release(WHeader)
	if u.Collapsed() || u.H != 340 {
		t.Errorf("re-expand: collapsed=%v H=%d want H=340", u.Collapsed(), u.H)
	}
}

// TestHitTestHeader ensures the whole header strip is clickable and acts as
// the collapse toggle, and that non-header regions stay on their widgets.
func TestHitTestHeader(t *testing.T) {
	u := NewUI(380, 520)
	if wd := u.HitTest(10, headerH/2); wd != WHeader {
		t.Errorf("header hit: got %v want WHeader", wd)
	}
	// Simulate a click (press + release on the header) to expand.
	u.Press(u.HitTest(10, headerH/2))
	if !u.Release(u.HitTest(10, headerH/2)) {
		t.Fatal("header click should toggle the collapse state")
	}
	if wd := u.HitTest(10, headerH+10); wd != WMessages {
		t.Errorf("message area hit: got %v want WMessages", wd)
	}
	if wd := u.HitTest(10, u.H-20); wd != WInput {
		t.Errorf("input area hit: got %v want WInput", wd)
	}
}

// TestCloseButton verifies the header's close button: the far-right square
// hit-tests as WClose (not the collapse toggle), clicking it sets the close
// request, and clicks elsewhere in the header keep toggling.
func TestCloseButton(t *testing.T) {
	u := NewUI(380, 520)
	cr := u.closeRect()
	if cr.Max.X > u.W || cr.Min.X <= 0 || cr.Min.Y < 0 || cr.Max.Y > headerH {
		t.Fatalf("closeRect %v does not fit inside the header", cr)
	}
	if got := u.HitTest(cr.Max.X-1, headerH/2); got != WClose {
		t.Errorf("close button hit: got %v want WClose", got)
	}
	if got := u.HitTest(cr.Min.X-8, headerH/2); got != WHeader {
		t.Errorf("left of the close button: got %v want WHeader (toggle)", got)
	}
	u.Press(WClose)
	u.Release(WClose)
	if !u.WantClose() {
		t.Fatal("Release(WClose) should set the close request")
	}

	// The collapse toggle must never request a close.
	u2 := NewUI(380, 520)
	u2.Press(WHeader)
	if !u2.Release(WHeader) || u2.WantClose() {
		t.Fatal("header toggle should resize but not request a close")
	}
}

// TestRenderRoundedCorners verifies the window shell is not a hard rectangle:
// the four corner pixels of a rendered frame are fully transparent (the
// compositor rounds the window) while edge midpoints and the centre stay
// opaque.
func TestRenderRoundedCorners(t *testing.T) {
	u := NewUI(380, 520)
	frame := u.Render()
	w, h := frame.Bounds().Dx(), frame.Bounds().Dy()
	opaque := func(x, y int) bool { return frame.Pix[frame.PixOffset(x, y)+3] == 255 }
	for _, c := range [][2]int{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}} {
		if opaque(c[0], c[1]) {
			t.Errorf("corner (%d,%d) should be transparent", c[0], c[1])
		}
	}
	for _, p := range [][2]int{
		{w / 2, 0}, {w / 2, h - 1}, // top/bottom edge midpoints
		{0, h / 2}, {w - 1, h / 2}, // left/right edge midpoints
		{w / 2, h / 2}, {winRadius, winRadius}, // centre + just inside a corner
	} {
		if !opaque(p[0], p[1]) {
			t.Errorf("point (%d,%d) should be opaque", p[0], p[1])
		}
	}
}

// --- settings dialog (gear icon + character-age dropdown) ---

// TestSettingsGearHitTest verifies the gear button: it sits inside the header,
// does not overlap the close button, and hit-tests as WSettings.
func TestSettingsGearHitTest(t *testing.T) {
	u := NewUI(380, 520)
	sr := u.settingsRect()
	if sr.Min.X <= 0 || sr.Min.Y < 0 || sr.Max.X >= u.closeRect().Min.X || sr.Max.Y > headerH {
		t.Fatalf("settingsRect %v misplaced in the header", sr)
	}
	if got := u.HitTest(sr.Min.X+1, sr.Min.Y+1); got != WSettings {
		t.Errorf("gear hit: got %v want WSettings", got)
	}
	// The strip between gear and close stays the collapse toggle.
	if got := u.HitTest(sr.Max.X+2, headerH/2); got != WHeader {
		t.Errorf("between gear and close: got %v want WHeader", got)
	}
}

// TestSettingsOpenCancel verifies the gear opens the modal (expanding a
// collapsed window to fit it) and CANCEL closes it, restoring the collapse
// state, without touching the committed age.
func TestSettingsOpenCancel(t *testing.T) {
	u := NewUI(380, 520)
	u.age = 9

	// Open from the collapsed startup state.
	u.Press(WSettings)
	if !u.Release(WSettings) {
		t.Fatal("Release(WSettings) from collapsed should report a resize")
	}
	if !u.settingsOpen || u.collapsed {
		t.Fatalf("settings open=%v collapsed=%v, want true/false",
			u.settingsOpen, u.collapsed)
	}
	if u.ageDraft != 9 {
		t.Errorf("ageDraft: got %d want the committed age 9", u.ageDraft)
	}
	if u.H != 520 {
		t.Errorf("expanded height: got %d want 520", u.H)
	}

	// The modal absorbs the whole window: a point over the message area is
	// the backdrop now, not a message click.
	if got := u.HitTest(10, 300); got != WModal {
		t.Errorf("hit over the backdrop: got %v want WModal", got)
	}

	// Cancel: modal closes, window collapses back, age unchanged.
	u.Press(WCancel)
	if !u.Release(WCancel) {
		t.Fatal("Release(WCancel) should report the collapse resize")
	}
	if u.settingsOpen || !u.collapsed || u.H != headerH+inputH {
		t.Fatalf("after cancel: open=%v collapsed=%v H=%d",
			u.settingsOpen, u.collapsed, u.H)
	}
	if u.age != 9 {
		t.Errorf("age changed on cancel: got %d want 9", u.age)
	}
}

// TestSettingsDropdownSave walks the dropdown: open the list, pick a row,
// save, and verify the committed age plus the persisted INI file.
func TestSettingsDropdownSave(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.H = 520

	u.Press(WSettings)
	u.Release(WSettings) // open (window already tall enough: no resize)

	// The dropdown box hit-tests as WDrop and toggles the list.
	d := u.dropRect()
	cx, cy := (d.Min.X+d.Max.X)/2, (d.Min.Y+d.Max.Y)/2
	u.Press(u.HitTest(cx, cy))
	u.Release(u.HitTest(cx, cy))
	if u.openDrop != dropAge {
		t.Fatalf("clicking the age box: openDrop=%v, want dropAge", u.openDrop)
	}

	// Row for age 13 is index 6; the list overlays the buttons, so a point
	// inside the list must hit WOption, not WSave.
	l := u.dropListRect()
	px, py := (l.Min.X+l.Max.X)/2, l.Min.Y+6*optH+1
	if got := u.HitTest(px, py); got != WOption {
		t.Fatalf("list row hit: got %v want WOption", got)
	}
	if u.optIdx != 6 {
		t.Errorf("optIdx: got %d want 6 (age 13 row)", u.optIdx)
	}
	u.Press(WOption)
	u.Release(WOption)
	if u.openDrop != dropNone {
		t.Error("picking an option should close the list")
	}
	if u.ageDraft != 13 {
		t.Fatalf("ageDraft: got %d want 13", u.ageDraft)
	}

	// SAVE commits the draft and rewrites the INI in place.
	_, saveRect := u.modalButtons()
	sx, sy := (saveRect.Min.X+saveRect.Max.X)/2, (saveRect.Min.Y+saveRect.Max.Y)/2
	if got := u.HitTest(sx, sy); got != WSave {
		t.Fatalf("save button hit: got %v want WSave", got)
	}
	u.Press(WSave)
	u.Release(WSave) // no resize reported: the window was already tall enough
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if u.age != 13 || u.Bot.CharacterAge != 13 {
		t.Errorf("committed age: ui=%d bot=%d want 13/13", u.age, u.Bot.CharacterAge)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "character-age = 13") ||
		strings.Contains(string(b), "character-age = 7\n") {
		t.Errorf("INI not rewritten in place:\n%s", b)
	}
}

// TestSettingsSaveDefaultCreatesFile verifies the very first save (no
// character-age key yet) appends the section and stores the dialog's age.
func TestSettingsSaveDefaultCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.ini")
	u := NewUI(380, 520)
	u.savePath = path
	u.collapsed = false
	u.H = 520

	u.Press(WSettings)
	u.Release(WSettings)
	if u.ageDraft != defaultCharacterAge {
		t.Errorf("ageDraft with no configured age: got %d want %d",
			u.ageDraft, defaultCharacterAge)
	}
	_, saveRect := u.modalButtons()
	w := u.HitTest((saveRect.Min.X+saveRect.Max.X)/2, (saveRect.Min.Y+saveRect.Max.Y)/2)
	u.Press(w)
	u.Release(w)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("save did not create %s: %v", path, err)
	}
	if !strings.Contains(string(b), "character-age = 10") {
		t.Errorf("fresh file lacks the saved age:\n%s", b)
	}
}

// TestSettingsKeyShortcuts verifies Enter saves and Escape cancels while the
// modal is open, and that typing goes nowhere in either case.
func TestSettingsKeyShortcuts(t *testing.T) {
	const esc, ret = uint32(0xff1b), uint32(0xff0d)

	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.savePath = path
	u.collapsed = false
	u.H = 520
	u.openSettings()

	// Typing is swallowed by the modal.
	if u.Key('x', 0) {
		t.Error("Key while the modal is open should report no change")
	}
	if len(u.input) != 0 {
		t.Errorf("modal leaked keystrokes into the textarea: %q", string(u.input))
	}

	// Escape cancels without saving.
	if !u.Key(0, esc) {
		t.Error("Escape in the modal should report a change")
	}
	if u.settingsOpen {
		t.Fatal("Escape should close the modal")
	}

	// Re-open, then Enter saves the draft.
	u.openSettings()
	u.ageDraft = 8
	u.Key(0, ret)
	if u.settingsOpen || u.age != 8 {
		t.Fatalf("Enter save: open=%v age=%d, want false/8", u.settingsOpen, u.age)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "character-age = 8") {
		t.Errorf("Enter did not persist the age:\n%s", b)
	}
}

// TestSettingsMinHeightRestore verifies a short expanded window is grown to
// fit the modal and restored afterwards.
func TestSettingsMinHeightRestore(t *testing.T) {
	u := NewUI(380, 520)
	u.collapsed = false
	u.H = 280
	u.expandedH = 280

	if !u.openSettings() {
		t.Fatal("openSettings should report the height change")
	}
	if u.H != minSettingsH {
		t.Errorf("modal window height: got %d want %d", u.H, minSettingsH)
	}
	if !u.closeSettings() {
		t.Fatal("closeSettings should report the restore")
	}
	if u.H != 280 {
		t.Errorf("height after close: got %d want 280", u.H)
	}
}

// TestSettingsRenderSmoke verifies the modal renders over the conversation
// without changing the frame size, with every dropdown state.
func TestSettingsRenderSmoke(t *testing.T) {
	for _, open := range []int{dropNone, dropAge, dropFrom, dropTo} {
		u := NewUI(380, 520)
		u.collapsed = false
		u.openSettings()
		u.openDrop = open
		if open == dropFrom || open == dropTo {
			u.hourScroll = 10
			u.optIdx = 12
			u.hover = WOption
		}
		frame := u.Render()
		if frame.Bounds() != (image.Rect(0, 0, 380, 520)) {
			t.Fatalf("frame bounds %v with openDrop=%d", frame.Bounds(), open)
		}
		// The panel interior must be opaque: the backdrop dims the window,
		// but the panel is drawn on top of it.
		p := u.modalPanel()
		if a := frame.Pix[frame.PixOffset(p.Min.X+10, p.Min.Y+100)+3]; a != 255 {
			t.Errorf("panel interior alpha %d, want 255 (openDrop=%d)", a, open)
		}
	}
}

// TestSleepDropdowns walks the FROM/TO sleep-time dropdowns: scroll, pick
// hours in both, save, and verify the INI gets one "HH:00-HH:00" key.
func TestSleepDropdowns(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.H = 520
	u.openSettings()

	// Nothing configured yet: the dialogs pre-select the defaults.
	if u.sleepFromDraft != defaultSleepFrom || u.sleepToDraft != defaultSleepTo {
		t.Fatalf("drafts: from=%d to=%d, want defaults %d/%d",
			u.sleepFromDraft, u.sleepToDraft, defaultSleepFrom, defaultSleepTo)
	}
	if u.ScrollHourList(1) {
		t.Error("ScrollHourList should report false with no hour list open")
	}

	// Open the FROM list: it auto-scrolls the selection into view.
	from := u.sleepFromRect()
	fx := (from.Min.X + from.Max.X) / 2
	u.Press(u.HitTest(fx, (from.Min.Y+from.Max.Y)/2))
	u.Release(u.HitTest(fx, (from.Min.Y+from.Max.Y)/2))
	if u.openDrop != dropFrom {
		t.Fatalf("FROM box click: openDrop=%d, want dropFrom", u.openDrop)
	}
	if u.hourScroll != min(defaultSleepFrom-2, numHours-visibleHourRows) {
		t.Errorf("hourScroll: got %d want %d (clamped at the list bottom)",
			u.hourScroll, min(defaultSleepFrom-2, numHours-visibleHourRows))
	}

	// Wheel up 14 rows, then click hour 06:00.
	if !u.ScrollHourList(-14) {
		t.Fatal("ScrollHourList should report true while the FROM list is open")
	}
	wantScroll := min(defaultSleepFrom-2, numHours-visibleHourRows) - 14
	if u.hourScroll != wantScroll {
		t.Errorf("scrolled hourScroll: got %d want %d", u.hourScroll, wantScroll)
	}
	l := u.hourListRect()
	mx := (l.Min.X + l.Max.X) / 2
	if got := u.HitTest(mx, l.Min.Y+(6-u.hourScroll)*optH+1); got != WOption {
		t.Fatalf("hour row hit: got %v want WOption", got)
	}
	if u.optIdx != 6 {
		t.Errorf("optIdx: got %d want 6 (06:00)", u.optIdx)
	}
	u.Press(WOption)
	u.Release(WOption)
	if u.openDrop != dropNone || u.sleepFromDraft != 6 {
		t.Fatalf("pick: openDrop=%d from=%d, want dropNone/6", u.openDrop, u.sleepFromDraft)
	}

	// Open the TO list (auto-scrolled to 07:00) and pick 09:00 from it.
	to := u.sleepToRect()
	tx := (to.Min.X + to.Max.X) / 2
	u.Press(u.HitTest(tx, (to.Min.Y+to.Max.Y)/2))
	u.Release(u.HitTest(tx, (to.Min.Y+to.Max.Y)/2))
	if u.openDrop != dropTo {
		t.Fatalf("TO box click: openDrop=%d, want dropTo", u.openDrop)
	}
	l = u.hourListRect() // the TO list: different x-range than the FROM one
	mx = (l.Min.X + l.Max.X) / 2
	if u.HitTest(mx, l.Min.Y+(9-u.hourScroll)*optH+1) != WOption {
		t.Fatal("09:00 row should hit WOption in the TO list")
	}
	u.Press(WOption)
	u.Release(WOption)
	if u.sleepToDraft != 9 {
		t.Errorf("sleepToDraft: got %d want 9", u.sleepToDraft)
	}

	// SAVE persists one combined sleep-time key next to the age.
	_, saveRect := u.modalButtons()
	w := u.HitTest((saveRect.Min.X+saveRect.Max.X)/2, (saveRect.Min.Y+saveRect.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if u.sleepFrom != 6 || u.sleepTo != 9 || !u.Bot.SleepSet ||
		u.Bot.SleepFrom != 6 || u.Bot.SleepTo != 9 {
		t.Errorf("committed sleep: ui=%d/%d bot=%v %d/%d, want 6/9 true 6/9",
			u.sleepFrom, u.sleepTo, u.Bot.SleepSet, u.Bot.SleepFrom, u.Bot.SleepTo)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "sleep-time = 06:00-09:00") {
		t.Errorf("INI lacks the saved sleep window:\n%s", b)
	}
	if !strings.Contains(string(b), "character-age = 7") {
		t.Errorf("INI lost the age while saving the sleep window:\n%s", b)
	}
}

// TestSettingsNameField verifies the NAME input on the first row of the
// dialog: it hit-tests as WName, edits via the keyboard when focused, and
// SAVE persists character-name to the INI plus renames the bot.
func TestSettingsNameField(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7 // match the ini so save does not change it
	u.savePath = path
	u.collapsed = false
	u.H = 520
	u.openSettings()

	// The name box sits above the age dropdown and is its own widget.
	nr := u.nameRect()
	if nr.Min.Y >= u.dropRect().Min.Y {
		t.Fatalf("nameRect %v must sit above dropRect %v", nr, u.dropRect())
	}
	px, py := (nr.Min.X+nr.Max.X)/2, (nr.Min.Y+nr.Max.Y)/2
	if got := u.HitTest(px, py); got != WName {
		t.Fatalf("name box hit: got %v want WName", got)
	}
	if u.nameFocused {
		t.Fatal("name field should not be focused on open")
	}

	// Click to focus, then type; the caret blinks only while focused.
	u.Press(WName)
	u.Release(WName)
	if !u.nameFocused {
		t.Fatal("clicking the name box should focus it")
	}
	u.Key('O', 0)
	u.Key('n', 0)
	u.Key('i', 0)
	if got := string(u.nameDraft); got != "Oni" {
		t.Fatalf("typed draft: got %q want \"Oni\"", got)
	}

	// Backspace edits the draft, Escape-then-Enter still saves the draft.
	u.Key(0, uint32(0xff08)) // backspace
	if got := string(u.nameDraft); got != "On" {
		t.Fatalf("after backspace: got %q want \"On\"", got)
	}
	u.Key('i', 0)
	u.Key('d', 0)
	u.Key('i', 0)
	u.Key('a', 0)
	u.closeSettings() // cancel: name stays uncommitted
	if u.name != "" {
		t.Fatalf("cancel must not commit the name, got %q", u.name)
	}

	// Re-open (draft re-seeded from the committed name) and save.
	u.openSettings()
	u.nameFocused = true
	for _, c := range "Onidia" {
		u.Key(c, 0)
	}
	_, saveRect := u.modalButtons()
	w := u.HitTest((saveRect.Min.X+saveRect.Max.X)/2, (saveRect.Min.Y+saveRect.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if u.name != "Onidia" {
		t.Errorf("committed name: got %q want \"Onidia\"", u.name)
	}
	if u.Bot.Name != "Onidia" || u.Bot.CharacterName != "Onidia" {
		t.Errorf("bot name: Bot.Name=%q CharacterName=%q, want Onidia/Onidia",
			u.Bot.Name, u.Bot.CharacterName)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "character-name = Onidia") {
		t.Errorf("INI lacks the saved name:\n%s", b)
	}
	if !strings.Contains(string(b), "character-age = 7") {
		t.Errorf("INI lost the age while saving the name:\n%s", b)
	}
}
