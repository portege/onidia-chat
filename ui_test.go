package main

import (
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// frameHasString reports whether the frame contains s rendered in the bitmap
// font at the given scale in the given color: it paints s onto a scratch
// layer with drawText, then slides that template over the frame looking for
// an exact match of the inked pixels. Font color is exclusive - only one
// color is painted per call - so callers check each color of interest
// separately (e.g. a title in plum and a link in teal).
func frameHasString(frame *image.NRGBA, s string, scale int, col color.RGBA) bool {
	w, h := textWidth(s, scale), glyphH*scale
	if w <= 0 || w > frame.Bounds().Dx() || h > frame.Bounds().Dy() {
		return false
	}
	tmpl := image.NewNRGBA(image.Rect(0, 0, w, h))
	drawText(tmpl, 0, 0, s, scale, col)
	var ink [][2]int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if tmpl.NRGBAAt(x, y).A != 0 {
				ink = append(ink, [2]int{x, y})
			}
		}
	}
	if len(ink) == 0 {
		return false
	}
	fw, fh := frame.Bounds().Dx(), frame.Bounds().Dy()
	cr, cg, cb, ca := col.RGBA()
	for oy := 0; oy+h <= fh; oy++ {
		for ox := 0; ox+w <= fw; ox++ {
			hit := true
			for _, p := range ink {
				if r, g, b, a := frame.NRGBAAt(ox+p[0], oy+p[1]).RGBA(); r != cr || g != cg || b != cb || a != ca {
					hit = false
					break
				}
			}
			if hit {
				return true
			}
		}
	}
	return false
}

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

// TestToggleCollapse exercises the history show/hide button: press+release
// on WToggle toggles the state and resizes H to the collapsed/expanded
// height, while a plain title-bar click no longer toggles anything.
func TestToggleCollapse(t *testing.T) {
	u := NewUI(380, 520)

	// Expand via the toggle button.
	u.Press(WToggle)
	if !u.Release(WToggle) {
		t.Fatal("Release(WToggle) on a collapsed UI should return true (state toggled)")
	}
	if u.Collapsed() {
		t.Fatal("toggle button click should expand the conversation")
	}
	if u.H != 520 {
		t.Errorf("expanded height: got %d want 520", u.H)
	}
	if _, h := u.msgArea(); h != 520-headerH-inputH {
		t.Errorf("msgArea height expanded: got %d", h)
	}

	// Collapse again; the expanded height must be remembered.
	u.H = 340 // simulate a user resize before collapsing
	u.Press(WToggle)
	if !u.Release(WToggle) {
		t.Fatal("Release(WToggle) on an expanded UI should return true")
	}
	if !u.Collapsed() {
		t.Fatal("toggle button click should collapse the conversation")
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
	u.Press(WToggle)
	u.Release(WToggle)
	if u.Collapsed() || u.H != 340 {
		t.Errorf("re-expand: collapsed=%v H=%d want H=340", u.Collapsed(), u.H)
	}

	// A plain title-bar click must NOT toggle the history list anymore.
	u.Press(WHeader)
	if u.Release(WHeader) {
		t.Fatal("Release(WHeader) should do nothing (drag handle, not a toggle)")
	}
	if u.Collapsed() || u.H != 340 {
		t.Errorf("header click changed the collapse state: collapsed=%v H=%d", u.Collapsed(), u.H)
	}
}

// TestHitTestHeader ensures the whole header strip hit-tests as the drag
// handle (WHeader), that clicking it no longer toggles the history, and that
// the +/- toggle button is its own widget that does toggle.
func TestHitTestHeader(t *testing.T) {
	u := NewUI(380, 520)
	if wd := u.HitTest(10, headerH/2); wd != WHeader {
		t.Errorf("header hit: got %v want WHeader", wd)
	}
	// A header click does nothing now (no collapse change).
	u.Press(u.HitTest(10, headerH/2))
	if u.Release(u.HitTest(10, headerH/2)) {
		t.Fatal("header click should not toggle anything")
	}
	if !u.Collapsed() {
		t.Fatal("header click must not expand the conversation")
	}
	// The toggle button hit-tests as WToggle and toggles.
	tr := u.toggleRect()
	if wd := u.HitTest(tr.Min.X+2, headerH/2); wd != WToggle {
		t.Errorf("toggle button hit: got %v want WToggle", wd)
	}
	u.Press(WToggle)
	if !u.Release(WToggle) {
		t.Fatal("toggle button click should expand the conversation")
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

	// The history toggle must never request a close, and a plain header
	// click does nothing at all now.
	u2 := NewUI(380, 520)
	u2.Press(WToggle)
	if !u2.Release(WToggle) || u2.WantClose() {
		t.Fatal("toggle button should resize but not request a close")
	}
	u2.Press(WHeader)
	if u2.Release(WHeader) || u2.WantClose() {
		t.Fatal("plain header click should do nothing and never request a close")
	}
}

// TestHaiyaButton verifies the header's "Haiya!" pet-toggle button: it hit-
// tests as WHaiya (not the collapse toggle), is bigger than the other header
// buttons, clicking it flags WantPet, the running state flips its meaning
// (launch vs quit), and neighbouring header clicks keep toggling as before.
func TestHaiyaButton(t *testing.T) {
	u := NewUI(380, 520)
	hr := u.haiyaRect()
	if hr.Min.X <= 0 || hr.Max.X > u.W || hr.Min.Y < 0 || hr.Max.Y > headerH {
		t.Fatalf("haiyaRect %v does not fit inside the header", hr)
	}
	// Bigger than the close/gear squares (hdrBtn) so it is easy to hit.
	if hr.Dy() <= hdrBtn || hr.Dx() <= textWidth(haiyaLabel, 1) {
		t.Fatalf("haiyaRect %v is not bigger than the hdrBtn squares (Dy=%d, w=%d)",
			hr, hr.Dy(), hr.Dx())
	}
	// The button sits right after the CHAT label, left of the right-side
	// close/gear buttons, so all four corners of its rect hit-test as the
	// button itself.
	for _, pt := range [][2]int{{hr.Min.X, hr.Min.Y}, {hr.Max.X - 1, hr.Max.Y - 1}, {hr.Min.X + 2, headerH / 2}} {
		if got := u.HitTest(pt[0], pt[1]); got != WHaiya {
			t.Errorf("Haiya button corner (%d,%d): got %v want WHaiya", pt[0], pt[1], got)
		}
	}
	// Press+release on the button flags WantPet but never toggles collapse.
	wasCollapsed := u.Collapsed()
	if u.PetRunning() {
		t.Fatal("a fresh UI should not report the pet as running")
	}
	u.Press(WHaiya)
	if u.Release(WHaiya) {
		t.Fatal("Release(WHaiya) should not collapse/expand the window")
	}
	if !u.WantPet() {
		t.Fatal("Release(WHaiya) should set the pet-launch request")
	}
	if u.Collapsed() != wasCollapsed {
		t.Fatalf("Haiya click should not change the collapse state (was %v, now %v)",
			wasCollapsed, u.Collapsed())
	}

	// SetPetRunning drives the button state main() uses to decide launch vs
	// quit; it must round-trip.
	u.SetPetRunning(true)
	if !u.PetRunning() {
		t.Fatal("SetPetRunning(true) did not stick")
	}
	u.SetPetRunning(false)
	if u.PetRunning() {
		t.Fatal("SetPetRunning(false) did not stick")
	}

	// One-shot: the flag was consumed by the WantPet() read above. No later
	// click - toggle button or plain title bar - may re-fire the launch/quit
	// action; the stale flag used to poof the running pet whenever the
	// header was clicked to collapse/expand.
	u.Press(WToggle)
	if !u.Release(WToggle) || u.WantPet() {
		t.Fatal("toggle click after a consumed Haiya click must not re-fire the pet action")
	}
	u.Press(WHeader)
	if u.Release(WHeader) || u.WantPet() {
		t.Fatal("header click after a consumed Haiya click must not re-fire the pet action")
	}

	// The header area immediately around the button still hit-tests as the
	// drag handle, and clicking it does nothing (no toggle, no pet request).
	u2 := NewUI(380, 520)
	if u2.HitTest(u2.haiyaRect().Min.X-8, headerH/2) != WHeader {
		t.Error("left of the Haiya button should fall through to WHeader (drag handle)")
	}
	u2.Press(WHeader)
	if u2.Release(WHeader) || u2.WantPet() {
		t.Fatal("plain header click should do nothing and never request a pet launch")
	}
}

// TestAboutModal verifies the header's About button and its popup: the
// button sits between the settings gear and the close button, clicking it
// opens the About panel (aboutOpen) without toggling collapse, the OK
// button/backdrop/Escape dismiss it, and the modal owns hit-testing while
// it is open.
func TestAboutModal(t *testing.T) {
	u := NewUI(380, 520)
	ar := u.aboutRect()
	sr := u.settingsRect()
	cr := u.closeRect()
	// Button order: gear ... About ... close.
	if !(sr.Max.X <= ar.Min.X && ar.Max.X <= cr.Min.X) {
		t.Fatalf("About button %v not between gear %v and close %v", ar, sr, cr)
	}
	for _, pt := range [][2]int{{ar.Min.X, ar.Min.Y}, {ar.Max.X - 1, ar.Max.Y - 1}, {(ar.Min.X + ar.Max.X) / 2, headerH / 2}} {
		if got := u.HitTest(pt[0], pt[1]); got != WAbout {
			t.Errorf("About button (%d,%d): got %v want WAbout", pt[0], pt[1], got)
		}
	}
	// Press+release opens the modal; like Settings it expands the window to
	// fit the panel, so start from an expanded UI (via the history toggle
	// button) to assert collapse state is untouched.
	u.Press(WToggle)
	u.Release(WToggle)
	wasCollapsed := u.Collapsed()
	if wasCollapsed {
		t.Fatal("toggle click should expand the fresh UI")
	}
	u.Press(WAbout)
	u.Release(WAbout)
	if !u.aboutOpen {
		t.Fatal("Release(WAbout) should open the About modal")
	}
	if u.Collapsed() != wasCollapsed {
		t.Fatal("About click should not change the collapse state")
	}
	// While open the modal owns the window: OK hits, header spots fall to
	// the backdrop (dismiss, never the header's own actions).
	if got := u.HitTest(u.aboutOKRect().Min.X+2, u.aboutOKRect().Min.Y+2); got != WAboutOK {
		t.Errorf("About OK: got %v want WAboutOK", got)
	}
	if got := u.HitTest(10, headerH/2); got != WModal {
		t.Errorf("header spot while About open: got %v want WModal", got)
	}
	// The modal draws the exact credit text.
	frame := u.Render()
	if !frameHasString(frame, "ONIDIA", uiFontScale, colPlum) ||
		!frameHasString(frame, "mas-mas.it", 1, colHeader) {
		t.Error("About modal should render the ONIDIA name and mas-mas.it credit")
	}
	// OK button dismisses.
	u.Press(WAboutOK)
	u.Release(WAboutOK)
	if u.aboutOpen {
		t.Fatal("Release(WAboutOK) should dismiss the About modal")
	}
	// Backdrop click dismisses.
	u.Press(WAbout)
	u.Release(WAbout)
	if !u.aboutOpen {
		t.Fatal("Release(WAbout) should reopen the About modal")
	}
	u.Press(WModal)
	u.Release(WModal)
	if u.aboutOpen {
		t.Fatal("backdrop click should dismiss the About modal")
	}
	// Escape dismisses.
	u.Press(WAbout)
	u.Release(WAbout)
	if !u.Key(0, ksEscape) || u.aboutOpen {
		t.Fatal("Escape should dismiss the About modal")
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

// TestInputDescenderSpace guards the textarea height: typed letters that
// dip below the baseline (g, y, p, q) must render fully with comfortable
// clearance above the textarea's inner bottom, even when the input wraps to
// all inputRows lines. This test previously caught descender rows getting
// visually chopped at the bottom edge of the input.
func TestInputDescenderSpace(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"single line", "big happy gym"}, // y+p+g descend
		{"wrapped to all rows", "how are you doing today? give a quick summary please"},
	}
	for _, c := range cases {
		u := NewUI(380, headerH+inputH) // collapsed prompt-only window
		u.input = []rune(c.input)
		u.focused = true
		u.caret = false
		frame := u.Render()

		ta := u.inputRect()
		minY, maxY := ta.Max.Y, ta.Min.Y
		for y := ta.Min.Y; y < ta.Max.Y; y++ {
			for x := ta.Min.X; x < ta.Max.X; x++ {
				p := frame.NRGBAAt(x, y)
				if p.R < 128 && p.G < 128 && p.B < 128 {
					if y < minY {
						minY = y
					}
					if y > maxY {
						maxY = y
					}
				}
			}
		}
		innerBottom := ta.Max.Y - 2 // white fill inside the 2px border
		if d := innerBottom - maxY; d < 8 {
			t.Errorf("%s: only %dpx below the descender bottoms (bbox y[%d..%d], textarea inner bottom %d)",
				c.name, d, minY, maxY, innerBottom)
		}
	}
}

// TestGlyphDescendersDistinct guards the lowercase bitmap font: the
// descender letters g / j / p / q / y must each have a distinct glyph so a
// well-rendered "g" can never be mistaken for a "q". They used to share
// identical bitmaps (g == q), which is what made the input's "g" read as "q".
// It also enforces the 9-row cell contract: true descenders carry ink below
// the baseline (rows 7-8) while every other glyph stops at row 6.
func TestGlyphDescendersDistinct(t *testing.T) {
	desc := []rune{'g', 'j', 'p', 'q', 'y'}
	seen := map[[9]uint8]rune{}
	for _, r := range desc {
		g, ok := font5x7[r]
		if !ok {
			t.Fatalf("font missing descender glyph %q", r)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("glyph %q is identical to %q (%#v); descender tails must differ", r, prev, g)
		}
		seen[g] = r
		if g[7] == 0 && g[8] == 0 {
			t.Errorf("glyph %q has no ink in the rows 7-8 descender zone; its tail would be chopped at the baseline", r)
		}
	}
	// punctuation that dips below the baseline too
	for _, r := range []rune{',', ';'} {
		g := font5x7[r]
		if g[7] == 0 && g[8] == 0 {
			t.Errorf("glyph %q should descend below the baseline", r)
		}
	}
	// every other glyph must stop above the descender zone
	isDesc := map[rune]bool{'g': true, 'j': true, 'p': true, 'q': true, 'y': true, ',': true, ';': true, '_': true}
	for r, g := range font5x7 {
		if isDesc[r] {
			continue
		}
		if g[7] != 0 || g[8] != 0 {
			t.Errorf("glyph %q unexpectedly has ink below the baseline (rows 7-8: %#x %#x)", r, g[7], g[8])
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

// TestSettingsOpenCancel verifies the gear opens the modal (growing a
// collapsed window to fit it while the history list stays hidden) and CANCEL
// closes it, restoring the window height, without touching the committed age.
func TestSettingsOpenCancel(t *testing.T) {
	u := NewUI(380, 520)
	u.age = 9
	// NewUI leaves H=520; collapse to the real startup size (header+input
	// only), exactly like main.go opens the window.
	u.collapsed = true
	u.H = headerH + inputH

	// Open from the collapsed startup state: the window grows to fit the
	// panel, but the conversation history must stay collapsed (the modal
	// just overlays the prompt-only view).
	u.Press(WSettings)
	if !u.Release(WSettings) {
		t.Fatal("Release(WSettings) from collapsed should report a resize")
	}
	if !u.settingsOpen || !u.collapsed {
		t.Fatalf("settings open=%v collapsed=%v, want true/true (history stays hidden)",
			u.settingsOpen, u.collapsed)
	}
	if u.ageDraft != 9 {
		t.Errorf("ageDraft: got %d want the committed age 9", u.ageDraft)
	}
	if u.H != minSettingsH {
		t.Errorf("modal window height: got %d want %d", u.H, minSettingsH)
	}

	// The modal absorbs the whole window: a point over the (hidden) history
	// area is the backdrop now, not a message click.
	if got := u.HitTest(10, 300); got != WModal {
		t.Errorf("hit over the backdrop: got %v want WModal", got)
	}

	// Cancel: modal closes, window shrinks back to the collapsed height,
	// age unchanged.
	u.Press(WCancel)
	if !u.Release(WCancel) {
		t.Fatal("Release(WCancel) should report the height restore")
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

// TestModalPanelsStayFullSize is the regression guard for the two modal rules
// that used to fight each other: the Settings and About panels must always
// render at their full designed size (never squeezed into the short collapsed
// window, which spilled their rows), and opening them must never change
// whether the conversation history is shown. The window itself may grow, but
// only in height, and only while the modal is open.
func TestModalPanelsStayFullSize(t *testing.T) {
	// The short window main.go starts with: header + prompt box only.
	collapsedH := headerH + inputH

	check := func(name string, openM, closeM func(*UI) bool,
		panel func(*UI) image.Rectangle, wantW, wantH, wantWinH int) {
		t.Helper()
		u := NewUI(380, 520)
		u.collapsed = true // history hidden, exactly like startup
		u.H = collapsedH

		if !openM(u) {
			t.Errorf("%s: opening from a short window should report a resize", name)
		}
		if u.H != wantWinH {
			t.Errorf("%s: window height during modal: got %d want %d", name, u.H, wantWinH)
		}
		if !u.collapsed {
			t.Errorf("%s: opening must not reveal the history (collapsed became false)", name)
		}
		if p := panel(u); p.Dx() != wantW || p.Dy() != wantH {
			t.Errorf("%s: panel %dx%d, want the full %dx%d",
				name, p.Dx(), p.Dy(), wantW, wantH)
		}
		if !closeM(u) {
			t.Errorf("%s: closing should report the height restore", name)
		}
		if u.H != collapsedH {
			t.Errorf("%s: height after close: got %d want %d", name, u.H, collapsedH)
		}
		if !u.collapsed {
			t.Errorf("%s: closing must leave the history hidden", name)
		}
	}

	check("settings", func(u *UI) bool { return u.openSettings() }, func(u *UI) bool { return u.closeSettings() },
		func(u *UI) image.Rectangle { return u.modalPanel() }, panelW, panelH, minSettingsH)
	check("about", func(u *UI) bool { return u.openAbout() }, func(u *UI) bool { return u.closeAbout() },
		func(u *UI) image.Rectangle { return u.aboutPanel() }, aboutPanelW, aboutPanelH, minAboutH)

	// With the history already expanded, the window still grows for the panel;
	// the expanded state (and therefore the visible history) is untouched.
	u := NewUI(380, 520)
	u.collapsed = false
	u.H = 300 // a short expanded window
	if !u.openSettings() {
		t.Fatal("settings in a short expanded window should grow it")
	}
	if u.collapsed {
		t.Error("settings grown from an expanded window must not collapse it")
	}
	if p := u.modalPanel(); p.Dx() != panelW || p.Dy() != panelH {
		t.Errorf("expanded settings panel %dx%d, want %dx%d", p.Dx(), p.Dy(), panelW, panelH)
	}
	if !u.closeSettings() || u.H != 300 || u.collapsed {
		t.Errorf("after close: H=%d collapsed=%v, want 300/false", u.H, u.collapsed)
	}
}

// TestSettingsRenderSmoke verifies the modal renders over the conversation
// without changing the frame size, with every dropdown state. The window is
// sized to minSettingsH so the modal needs no growth (the constant tracks the
// panel height, so this stays true as the dialog gains rows).
func TestSettingsRenderSmoke(t *testing.T) {
	for _, open := range []int{dropNone, dropAge, dropFrom, dropTo, dropFromM, dropToM} {
		u := NewUI(380, minSettingsH)
		u.collapsed = false
		u.openSettings()
		u.openDrop = open
		if open == dropFrom || open == dropTo {
			u.hourScroll = 10
			u.optIdx = 12
			u.hover = WOption
		}
		if open == dropFromM || open == dropToM {
			u.minuteScroll = 0
			u.optMIdx = 1
			u.hover = WOption
		}
		frame := u.Render()
		if frame.Bounds() != (image.Rect(0, 0, 380, minSettingsH)) {
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

	// Open the FROM minute list and pick :15 (index 1 of 00/15/30/45).
	fm := u.sleepFromMinRect()
	fmx := (fm.Min.X + fm.Max.X) / 2
	u.Press(u.HitTest(fmx, (fm.Min.Y+fm.Max.Y)/2))
	u.Release(u.HitTest(fmx, (fm.Min.Y+fm.Max.Y)/2))
	if u.openDrop != dropFromM {
		t.Fatalf("FROM minute box click: openDrop=%d, want dropFromM", u.openDrop)
	}
	fl := u.minuteListRect()
	if u.HitTest((fl.Min.X+fl.Max.X)/2, fl.Min.Y+1*optH+1) != WOption {
		t.Fatal(":15 row should hit WOption in the FROM minute list")
	}
	u.Press(WOption)
	u.Release(WOption)
	if u.sleepFromMinDraft != 1 {
		t.Errorf("sleepFromMinDraft: got %d want 1 (:15)", u.sleepFromMinDraft)
	}

	// Open the TO minute list and pick :30 (index 2).
	tm := u.sleepToMinRect()
	tmx := (tm.Min.X + tm.Max.X) / 2
	u.Press(u.HitTest(tmx, (tm.Min.Y+tm.Max.Y)/2))
	u.Release(u.HitTest(tmx, (tm.Min.Y+tm.Max.Y)/2))
	if u.openDrop != dropToM {
		t.Fatalf("TO minute box click: openDrop=%d, want dropToM", u.openDrop)
	}
	tl := u.minuteListRect()
	if u.HitTest((tl.Min.X+tl.Max.X)/2, tl.Min.Y+2*optH+1) != WOption {
		t.Fatal(":30 row should hit WOption in the TO minute list")
	}
	u.Press(WOption)
	u.Release(WOption)
	if u.sleepToMinDraft != 2 {
		t.Errorf("sleepToMinDraft: got %d want 2 (:30)", u.sleepToMinDraft)
	}

	// SAVE persists one combined sleep-time key with minutes next to the age.
	_, saveRect := u.modalButtons()
	w := u.HitTest((saveRect.Min.X+saveRect.Max.X)/2, (saveRect.Min.Y+saveRect.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if u.sleepFrom != 6 || u.sleepTo != 9 || u.sleepFromMin != 15 || u.sleepToMin != 30 ||
		!u.Bot.SleepSet || u.Bot.SleepFromH != 6 || u.Bot.SleepToH != 9 ||
		u.Bot.SleepFromM != 15 || u.Bot.SleepToM != 30 {
		t.Errorf("committed sleep: ui=%d:%02d/%d:%02d bot=%v %d:%02d/%d:%02d, want 6:15/9:30",
			u.sleepFrom, u.sleepFromMin, u.sleepTo, u.sleepToMin, u.Bot.SleepSet,
			u.Bot.SleepFromH, u.Bot.SleepFromM, u.Bot.SleepToH, u.Bot.SleepToM)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "sleep-time = 06:15-09:30") {
		t.Errorf("INI lacks the saved sleep window:\n%s", b)
	}
	if !strings.Contains(string(b), "character-age = 7") {
		t.Errorf("INI lost the age while saving the sleep window:\n%s", b)
	}
}

// TestSleepMinuteBoxesFitContent pins the dropdown geometry: the minute
// select box's "00" label must clear the chevron, and the expanded list's
// selection dot + text must stay inside the box. The old 44px-wide boxes
// (300px panel, 60/40 split) clipped both.
func TestSleepMinuteBoxesFitContent(t *testing.T) {
	u := NewUI(380, 520)
	u.collapsed = false
	u.openSettings()
	lblW := textWidth("00", uiFontScale)
	for name, r := range map[string]image.Rectangle{
		"from": u.sleepFromMinRect(),
		"to":   u.sleepToMinRect(),
	} {
		// Select box: text starts at +12 and must end before the chevron
		// (7px wide, centred at Max-16).
		if end := r.Min.X + 12 + lblW; end > r.Max.X-16-4 {
			t.Errorf("%s minute box %v: label ends at %d, chevron starts at %d",
				name, r, end, r.Max.X-20)
		}
		// Expanded list: dot centred at +15, text at +26; keep a margin.
		if end := r.Min.X + 26 + lblW; end > r.Max.X-4 {
			t.Errorf("%s minute list %v: label ends at %d, box edge %d",
				name, r, end, r.Max.X)
		}
		// The neighbouring hour box must not overlap the minute box.
		var hour image.Rectangle
		if name == "from" {
			hour = u.sleepFromRect()
		} else {
			hour = u.sleepToRect()
		}
		if r.Min.X-hour.Max.X < 6 {
			t.Errorf("%s minute box %v overlaps hour box %v", name, r, hour)
		}
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

// TestSettingsGenderPicker verifies the GENDER row: the GIRL/BOY buttons
// hit-test as WGirl/WBoy, a click flips only the draft (committed on SAVE,
// discarded by CANCEL), SAVE writes "character-gender" to the INI, and the
// committed gender decides which character the Haiya! button launches.
func TestSettingsGenderPicker(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-gender = girl\n")
	u := NewUI(380, 560)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.H = 560
	u.openSettings()

	// Layout sanity: the picker sits below the busy rows and above the mute
	// checkbox, which in turn sits above the SAVE/CANCEL buttons.
	girl, boy := u.genderRects()
	_, save := u.modalButtons()
	if girl.Min.Y <= u.busyToMinRect().Max.Y || boy.Max.Y >= u.muteRect().Min.Y || u.muteRect().Max.Y >= save.Min.Y {
		t.Fatalf("picker %v/%v must sit between the busy rows and the mute row (%v)", girl, boy, u.muteRect())
	}
	if u.genderDraft != "girl" || u.gender != "girl" {
		t.Fatalf("INI said girl: draft=%q committed=%q", u.genderDraft, u.gender)
	}
	if u.PetCharacter() != "onidia" {
		t.Errorf("girl default: PetCharacter()=%q, want onidia", u.PetCharacter())
	}

	// The picker renders the character names, not GIRL/BOY. The label checks
	// run on copied button crops (frameHasString scans from (0,0), and a
	// SubImage keeps absolute coordinates, so the crop must be re-based);
	// over the whole frame the solid-white name box would make every
	// colWhite template "match" (frameHasString compares ink pixels only).
	frame := u.Render()
	crop := func(r image.Rectangle) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(img, img.Bounds(), frame, r.Min, draw.Src)
		return img
	}
	girlImg, boyImg := crop(girl), crop(boy)
	btnHas := func(img *image.NRGBA, s string) bool {
		return frameHasString(img, s, uiFontScale, colWhite) ||
			frameHasString(img, s, uiFontScale, colMuted)
	}
	if !btnHas(girlImg, "ONIDIA") {
		t.Error("GIRL button should render the ONIDIA label")
	}
	if !btnHas(boyImg, "KAMA") {
		t.Error("BOY button should render the KAMA label")
	}
	if btnHas(girlImg, "GIRL") || btnHas(girlImg, "BOY") ||
		btnHas(boyImg, "GIRL") || btnHas(boyImg, "BOY") {
		t.Error("gender picker still renders the old GIRL/BOY labels")
	}

	// Both buttons hit-test; clicking BOY flips only the draft.
	if w := u.HitTest((girl.Min.X+girl.Max.X)/2, (girl.Min.Y+girl.Max.Y)/2); w != WGirl {
		t.Fatalf("GIRL button hit: got %v want WGirl", w)
	}
	if w := u.HitTest((boy.Min.X+boy.Max.X)/2, (boy.Min.Y+boy.Max.Y)/2); w != WBoy {
		t.Fatalf("BOY button hit: got %v want WBoy", w)
	}
	u.Press(WBoy)
	u.Release(WBoy)
	if u.genderDraft != "boy" || u.gender != "girl" {
		t.Fatalf("BOY click: draft=%q committed=%q, want boy/girl", u.genderDraft, u.gender)
	}

	// CANCEL discards the draft.
	u.Press(WCancel)
	u.Release(WCancel)
	if u.settingsOpen || u.gender != "girl" {
		t.Fatalf("cancel: open=%v gender=%q, want closed/girl", u.settingsOpen, u.gender)
	}

	// Re-open (the draft re-seeds from the committed gender), pick BOY and
	// SAVE: the INI gains character-gender = boy, the committed gender flips
	// and Haiya! would now launch Kama.
	u.openSettings()
	if u.genderDraft != "girl" {
		t.Fatalf("reopen should re-seed the draft from the committed gender, got %q", u.genderDraft)
	}
	u.Press(WBoy)
	u.Release(WBoy)
	_, save = u.modalButtons()
	w := u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if u.gender != "boy" || u.PetCharacter() != "kama" {
		t.Errorf("after save: gender=%q PetCharacter()=%q, want boy/kama", u.gender, u.PetCharacter())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "character-gender = boy") {
		t.Errorf("INI lacks the saved gender key:\n%s", b)
	}
	if !strings.Contains(string(b), "character-age = 7") {
		t.Errorf("INI lost the age while saving the gender:\n%s", b)
	}
}

// TestSettingsMuteCheckbox verifies the MUTE SPEECH checkbox row: it sits
// between the sleep dropdowns and the buttons, hit-tests as WMute, toggles
// the draft on click (committing only on SAVE), re-seeds from the committed
// value when the dialog reopens, and SAVE persists "mute" to the INI.
func TestSettingsMuteCheckbox(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.H = 520
	u.openSettings()

	// Layout sanity: the row lives below the sleep dropdowns and above the
	// SAVE/CANCEL buttons.
	mr := u.muteRect()
	cancel, _ := u.modalButtons()
	if mr.Min.Y <= u.sleepToRect().Max.Y || mr.Max.Y >= cancel.Min.Y {
		t.Fatalf("muteRect %v must sit between the sleep dropdowns and the buttons", mr)
	}
	if u.muteDraft || u.mute {
		t.Fatal("checkbox should start unchecked when speech is on")
	}

	// Click the checkbox twice: the draft toggles, nothing commits.
	px, py := (mr.Min.X+mr.Max.X)/2, (mr.Min.Y+mr.Max.Y)/2
	if w := u.HitTest(px, py); w != WMute {
		t.Fatalf("mute checkbox hit: got %v want WMute", w)
	}
	u.Press(WMute)
	u.Release(WMute)
	if !u.muteDraft {
		t.Fatal("click should check the mute draft")
	}
	u.Press(WMute)
	u.Release(WMute)
	if u.muteDraft || u.mute {
		t.Fatal("second click should uncheck; nothing commits before SAVE")
	}

	// Check it again and SAVE: the INI gains mute = true, the committed
	// state flips and the modal closes.
	u.Press(WMute)
	u.Release(WMute)
	_, save := u.modalButtons()
	w := u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if !u.Muted() {
		t.Error("Muted() should report true after saving the checked box")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "mute = true") {
		t.Errorf("INI lacks the saved mute key:\n%s", b)
	}
	if !strings.Contains(string(b), "character-age = 7") {
		t.Errorf("INI lost the age while saving mute:\n%s", b)
	}

	// Re-open: the draft re-seeds from the committed value. Uncheck and
	// save again: the key is rewritten in place and speech is back on.
	u.openSettings()
	if !u.muteDraft {
		t.Fatal("reopen should seed the draft from the committed mute")
	}
	mr = u.muteRect()
	px, py = (mr.Min.X+mr.Max.X)/2, (mr.Min.Y+mr.Max.Y)/2
	u.Press(u.HitTest(px, py))
	u.Release(u.HitTest(px, py))
	if u.muteDraft {
		t.Fatal("click should uncheck the seeded draft")
	}
	_, save = u.modalButtons()
	w = u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.Muted() {
		t.Error("Muted() should report false after unchecking and saving")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "mute = false") {
		t.Errorf("INI lacks the rewritten mute key:\n%s", b)
	}
}

// TestSettingsDemoCheckbox verifies the DEMO MODE checkbox row (directly
// below MUTE SPEECH): it hit-tests as WDemo, toggles the draft on click
// (committing only on SAVE), re-seeds from the committed value when the
// dialog reopens, SAVE persists "demo-mode" to the INI, and flipping it
// while a pet is running flags the restart so the mode takes effect at
// once.
func TestSettingsDemoCheckbox(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.H = 520
	u.openSettings()

	// Layout sanity: the row lives below MUTE SPEECH and above the buttons.
	dr, mr := u.demoRect(), u.muteRect()
	cancel, _ := u.modalButtons()
	if dr.Min.Y <= mr.Max.Y || dr.Max.Y >= cancel.Min.Y {
		t.Fatalf("demoRect %v must sit between the mute row (%v) and the buttons", dr, mr)
	}
	if u.demoDraft || u.demo {
		t.Fatal("demo mode should default to off (planted)")
	}
	if u.PetDemo() {
		t.Fatal("demo mode should default to off (planted)")
	}

	// Click the checkbox: the draft toggles, nothing commits.
	px, py := (dr.Min.X+dr.Max.X)/2, (dr.Min.Y+dr.Max.Y)/2
	if w := u.HitTest(px, py); w != WDemo {
		t.Fatalf("demo checkbox hit: got %v want WDemo", w)
	}
	u.Press(WDemo)
	u.Release(WDemo)
	if !u.demoDraft {
		t.Fatal("click should check the demo draft")
	}
	if u.PetDemo() {
		t.Fatal("nothing should commit before SAVE")
	}

	// SAVE: the INI gains demo-mode = true and the committed state flips.
	_, save := u.modalButtons()
	w := u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	if !u.PetDemo() {
		t.Error("PetDemo() should report true after saving the checked box")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "demo-mode = true") {
		t.Errorf("INI lacks the saved demo-mode key:\n%s", b)
	}
	if !strings.Contains(string(b), "character-age = 7") {
		t.Errorf("INI lost the age while saving demo mode:\n%s", b)
	}

	// Re-open: the draft re-seeds from the committed value.
	u.openSettings()
	if !u.demoDraft {
		t.Fatal("reopen should seed the draft from the committed demo mode")
	}

	// Flipping it while the pet runs must schedule a restart, so the new
	// mode reaches the running pet immediately instead of on the next
	// manual Haiya! click.
	u.SetPetRunning(true)
	dr = u.demoRect()
	px, py = (dr.Min.X+dr.Max.X)/2, (dr.Min.Y+dr.Max.Y)/2
	u.Press(u.HitTest(px, py))
	u.Release(u.HitTest(px, py))
	if u.demoDraft {
		t.Fatal("click should uncheck the seeded draft")
	}
	_, save = u.modalButtons()
	w = u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.PetDemo() {
		t.Error("PetDemo() should report false after unchecking and saving")
	}
	if !u.WantPetRestart() {
		t.Error("a demo flip with a running pet should flag a restart")
	}
	if u.WantPetRestart() {
		t.Error("the restart flag should clear once consumed")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "demo-mode = false") {
		t.Errorf("INI lacks the rewritten demo-mode key:\n%s", b)
	}
}

// TestSettingsDemoCheckboxCancel verifies CANCEL discards a demo-mode flip:
// the draft is dropped on reopen and nothing reaches the INI.
func TestSettingsDemoCheckboxCancel(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.openSettings()

	dr := u.demoRect()
	px, py := (dr.Min.X+dr.Max.X)/2, (dr.Min.Y+dr.Max.Y)/2
	if w := u.HitTest(px, py); w != WDemo {
		t.Fatalf("demo row hit-test = %v, want WDemo", w)
	}
	u.Press(WDemo)
	u.Release(WDemo)
	if !u.demoDraft {
		t.Fatal("click should check the demo draft")
	}
	cancel, _ := u.modalButtons()
	w := u.HitTest((cancel.Min.X+cancel.Max.X)/2, (cancel.Min.Y+cancel.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("cancel should close the modal")
	}
	if u.PetDemo() {
		t.Error("cancel must not commit the demo draft")
	}
	if u.WantPetRestart() {
		t.Error("cancel must not schedule a pet restart")
	}
	u.openSettings()
	if u.demoDraft {
		t.Error("reopen should re-seed the draft, dropping the cancelled flip")
	}
}

// TestSettingsSaveBakesNameAge verifies SAVE writes the dialog's name and
// age into the stored persona's "your name is ..." sentence (a persona
// without the sentence grows one after the built-in default), and a second
// save with new values rewrites that same sentence.
func TestSettingsSaveBakesNameAge(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	u := NewUI(380, 520)
	u.age = 7
	u.savePath = path
	u.collapsed = false
	u.H = 520
	u.openSettings()

	u.nameFocused = true
	for _, c := range "Onidia" {
		u.Key(c, 0)
	}
	u.ageDraft = 11
	_, save := u.modalButtons()
	w := u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	if u.settingsOpen {
		t.Fatal("save should close the modal")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Your name is Onidia, 11 years old.") {
		t.Errorf("INI persona lacks the baked name/age:\n%s", b)
	}
	if !strings.Contains(string(b), botPersona) {
		t.Errorf("first save should grow the default persona:\n%s", b)
	}

	// A second save with a new age rewrites the same sentence.
	u.openSettings()
	if u.ageDraft != 11 || string(u.nameDraft) != "Onidia" {
		t.Fatalf("drafts not re-seeded: age=%d name=%q", u.ageDraft, string(u.nameDraft))
	}
	u.ageDraft = 9
	_, save = u.modalButtons()
	w = u.HitTest((save.Min.X+save.Max.X)/2, (save.Min.Y+save.Max.Y)/2)
	u.Press(w)
	u.Release(w)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "11 years old") || !strings.Contains(string(b), "9 years old") {
		t.Errorf("stale values survived the second save:\n%s", b)
	}
	if n := strings.Count(strings.ToLower(string(b)), "your name is"); n != 1 {
		t.Errorf("identity sentence appears %d times:\n%s", n, b)
	}
}

// --- paginated bubbles (multi-paragraph replies) ---

// TestSplitPages verifies paragraph splitting: blank lines are dropped,and
// a single paragraph stays unpaginated while 2+ become pages.

func TestSplitPages(t *testing.T) {
	got := splitPages("one line")
	if len(got) != 1 || got[0] != "one line" {
		t.Errorf("splitPages(one line) = %q, want [one line]", got)
	}
	got = splitPages("one\ftwo\fthree")
	if len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Errorf("splitPages = %q, want [one two three]", got)
	}
	got = splitPages("\f one \f two \f")
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("splitPages trimmed empties = %q, want [one two]", got)
	}
}

// TestAddMsgPaginates verifies AddMsg splits multi-paragraph replies into
// Pages (starting at page 0)and leaves single paragraphs unpaginated.

func TestAddMsgPaginates(t *testing.T) {
	u := NewUI(380, 520)
	before := len(u.msgs)
	u.AddMsg("bot", "one\ntwo\nthree")
	m := u.msgs[before]
	if len(m.Pages) != 3 || m.Page != 0 || m.Pages[1] != "two" {
		t.Errorf("paged msg: pages=%v page=%d, want 3 pages at page 0", m.Pages, m.Page)

	}
	u.AddMsg("bot", "just one paragraph")
	m2 := u.msgs[before+1]
	if len(m2.Pages) != 0 {
		t.Errorf("single paragraph got pages %v, want nil", m2.Pages)

	}
}

// TestPagerFlip verifies the < > buttons on a paginated bubble flip its page,
// clamping at the firstand last page.

// blockPoint returns window coordinates of the centre of a rectangle computed
// in the message layer (layer-relative Y like copyBtnRect/pagerRects output):
// the message area starts right below the header.
func blockPoint(u *UI, r image.Rectangle) (int, int) {
	return (r.Min.X + r.Max.X) / 2, headerH + (r.Min.Y+r.Max.Y)/2
}

func TestCopyButton(t *testing.T) {
	u := NewUI(380, 520)
	u.collapsed = false
	u.msgs = nil
	u.AddMsg("bot", "hello from the bot")  // block 0: bot, pill on the right
	u.AddMsg("you", "hello from the user") // block 1: user, pill on the left
	hitPill := func(mi int) Widget {
		b := u.blocks()[mi]
		bx := padX
		if b.m.From == "you" {
			bx = u.W - padX - b.bubW
		}
		ty := msgTopPad
		for i := 0; i < mi; i++ {
			ty += u.blocks()[i].h + bubGap
		}
		x, y := blockPoint(u, copyBtnRect(b, bx, ty))
		return u.HitTest(x, y)
	}
	if w := hitPill(0); w != WCopy {
		t.Fatalf("bot pill: HitTest = %v, want WCopy", w)
	}
	if u.copyMsg != 0 {
		t.Fatalf("copyMsg = %d, want 0", u.copyMsg)
	}
	if w := hitPill(1); w != WCopy {
		t.Fatalf("user pill: HitTest = %v, want WCopy", w)
	}
	if u.copyMsg != 1 {
		t.Fatalf("copyMsg = %d, want 1", u.copyMsg)
	}
	// Click through: press + release on the user pill stages its text.
	u.Press(WCopy)
	if u.Release(WCopy) {
		t.Error("Release reported a header toggle for WCopy")
	}
	if !u.WantCopy() {
		t.Fatal("WantCopy = false after the pill click")
	}
	if got, want := u.TakeCopiedText(), "hello from the user"; got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
	if u.WantCopy() { // the flag must clear so the click is not copied twice
		t.Error("WantCopy still set after TakeCopiedText")
	}
	// Clicking elsewhere in the message area is not a copy.
	if w := u.HitTest(u.W/2, headerH+40); w == WCopy {
		t.Error("middle of the message area hit WCopy")
	}
	// The synthetic "..." thinking bubble gets no pill: the block after the
	// last real message must not answer WCopy.
	u.Thinking = true
	b := u.blocks()[len(u.msgs)] // the "..." block
	ty := msgTopPad
	for _, pb := range u.blocks()[:len(u.msgs)] {
		ty += pb.h + bubGap
	}
	x, y := blockPoint(u, copyBtnRect(b, padX, ty))
	if w := u.HitTest(x, y); w == WCopy {
		t.Error("thinking bubble answered WCopy")
	}
	u.Thinking = false
}

func TestSelNotifyBytes(t *testing.T) {
	// answerSelection builds a SelectionNotifyEvent and sends its Bytes().
	// Round-trip those bytes through xgb's own parser: whatever a paste
	// client parses out must be exactly what we put in (this is layout-
	// agnostic, unlike the old hand-packed offsets which enshrined a bug).
	e := xproto.SelectionRequestEvent{
		Time: 1234, Owner: 0x0B0B0B0B, Requestor: 0x11111111,
		Selection: 0x22222222, Target: 0x33333333, Property: 0x44444444,
	}
	notify := xproto.SelectionNotifyEvent{
		Time:      e.Time,
		Requestor: e.Requestor,
		Selection: e.Selection,
		Target:    e.Target,
		Property:  0x55555555,
	}
	b := notify.Bytes()
	if len(b) != 32 {
		t.Fatalf("len = %d, want 32", len(b))
	}
	if b[0] != 31 {
		t.Errorf("type = %d, want 31 (SelectionNotify)", b[0])
	}
	p, ok := xproto.SelectionNotifyEventNew(b).(xproto.SelectionNotifyEvent)
	if !ok {
		t.Fatal("parser did not return a SelectionNotifyEvent")
	}
	if p.Time != notify.Time || p.Requestor != notify.Requestor ||
		p.Selection != notify.Selection || p.Target != notify.Target ||
		p.Property != notify.Property {
		t.Errorf("parser round-trip mismatch: got %+v, want %+v", p, notify)
	}
}

func TestLatin1(t *testing.T) {
	got := latin1("h\u00e9llo \u2603") // "héllo ☃"
	// STRING is ISO-8859-1: é is the single raw byte 0xE9, ☃ becomes '?'.
	want := []byte{'h', 0xe9, 'l', 'l', 'o', ' ', '?'}
	if string(got) != string(want) {
		t.Errorf("latin1 = %#v, want %#v", got, want)
	}
}

func TestPagerFlip(t *testing.T) {
	u := NewUI(380, 520)
	u.collapsed = false
	u.msgs = nil
	u.AddMsg("bot", "one\ntwo\nthree")
	b := u.blocks()[0]
	if !b.paginated || b.pageCount != 3 {
		t.Fatalf("block: paginated=%v pages=%d, want true/3", b.paginated, b.pageCount)
	}
	// Recompute the pager geometry from the bubble's current page each step:
	// the bubble width (and so the pager position) depends on the paragraph.
	hitNext := func() (int, int) {
		_, nr, _, _ := pagerRects(u.blocks()[0], padX, msgTopPad+labelH)
		return (nr.Min.X + nr.Max.X) / 2, headerH + (nr.Min.Y+nr.Max.Y)/2
	}
	hitPrev := func() (int, int) {
		pr, _, _, _ := pagerRects(u.blocks()[0], padX, msgTopPad+labelH)
		return (pr.Min.X + pr.Max.X) / 2, headerH + (pr.Min.Y+pr.Max.Y)/2
	}
	flip := func(x, y int, want Widget) {
		w := u.HitTest(x, y)
		if w != want {
			t.Errorf("pager hit at (%d,%d): got %v want %v", x, y, w, want)
			return
		}
		u.Press(w)
		u.Release(w)
	}

	// Next: 0 -> 1 -> 2 (last), then one more next is inert (disabled button).
	for want := 1; want <= 2; want++ {
		nx, ny := hitNext()
		flip(nx, ny, WPageNext)
		if m := u.msgs[0]; m.Page != want {
			t.Errorf("page after next: got %d want %d", m.Page, want)
		}
	}
	nx, ny := hitNext()
	flip(nx, ny, WMessages) // next is disabled at the last page
	if m := u.msgs[0]; m.Page != 2 {
		t.Errorf("next clamped: got %d want 2", m.Page)
	}

	// Prev: 2 -> 1 -> 0 (first), then one more prev is inert (disabled button).
	for want := 1; want >= 0; want-- {
		px, py := hitPrev()
		flip(px, py, WPagePrev)
		if m := u.msgs[0]; m.Page != want {
			t.Errorf("page after prev: got %d want %d", m.Page, want)
		}
	}
	px, py := hitPrev()
	flip(px, py, WMessages) // prev is disabled at the first page
	if m := u.msgs[0]; m.Page != 0 {
		t.Errorf("prev clamped: got %d want 0", m.Page)
	}
}
