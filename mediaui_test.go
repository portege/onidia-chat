package main

// mediaui_test.go - the transport strip as the user sees it: when it appears,
// where its buttons are, what a click queues, and that it fits the window
// (including the collapsed one, where it is the only free row).

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestMediaBarHiddenWithoutSession(t *testing.T) {
	u := NewUI(defaultWinW, defaultWinH)
	if u.MediaActive() {
		t.Fatal("strip active with no player")
	}
	if !u.mediaBar().Empty() || !u.mediaPlayRect().Empty() || !u.mediaStopRect().Empty() {
		t.Error("strip geometry must be empty while nothing plays")
	}
	// The strip's rows then belong to the input bar, as before.
	br := u.buttonRect()
	if got := u.HitTest(br.Max.X-2, br.Min.Y+2); got != WButton {
		t.Errorf("HitTest over the SEND button = %v, want WButton", got)
	}
	// No state change -> nothing to repaint.
	if u.SetMedia(MediaState{}) {
		t.Error("SetMedia with an unchanged state reported a change")
	}
	if u.TakeMedia() != "" {
		t.Error("TakeMedia returned a command with no click")
	}
}

func TestMediaBarGeometryAndHitTest(t *testing.T) {
	u := NewUI(defaultWinW, defaultWinH)
	u.collapsed = false
	u.SetMedia(MediaState{Active: true, Title: "Havana"})
	play, stop := u.mediaPlayRect(), u.mediaStopRect()

	for name, r := range map[string]image.Rectangle{"play": play, "stop": stop} {
		if r.Empty() {
			t.Fatalf("%s rect is empty", name)
		}
		if r.Max.X > u.W || r.Max.Y > u.H {
			t.Errorf("%s rect %v outside the window %dx%d", name, r, u.W, u.H)
		}
		// The strip sits directly above the input bar, never over it.
		if r.Max.Y > u.H-inputH {
			t.Errorf("%s rect %v overlaps the input bar (top %d)", name, r, u.H-inputH)
		}
	}
	if play.Overlaps(stop) {
		t.Errorf("play %v overlaps stop %v", play, stop)
	}
	// Both buttons hit-test to their own widget; the label row toggles too.
	if got := u.HitTest(play.Min.X+1, play.Min.Y+1); got != WMediaPlay {
		t.Errorf("HitTest(play) = %v, want WMediaPlay", got)
	}
	if got := u.HitTest(stop.Min.X+1, stop.Min.Y+1); got != WMediaStop {
		t.Errorf("HitTest(stop) = %v, want WMediaStop", got)
	}
	if got := u.HitTest(padX+2, u.mediaBar().Min.Y+2); got != WMediaPlay {
		t.Errorf("HitTest(label row) = %v, want WMediaPlay (whole strip toggles)", got)
	}
	// A press released somewhere else is not a click.
	u.Press(WMediaPlay)
	u.Release(u.HitTest(10, 10))
	if u.TakeMedia() != "" {
		t.Error("a press+release on different widgets queued a command")
	}
}

func TestMediaBarClicksQueueTransportCommands(t *testing.T) {
	u := NewUI(defaultWinW, defaultWinH)
	u.collapsed = false
	u.SetMedia(MediaState{Active: true, Title: "Havana"})
	play, stop := u.mediaPlayRect(), u.mediaStopRect()

	// Playing -> the toggle pauses.
	u.Press(WMediaPlay)
	u.Release(u.HitTest(play.Min.X+2, play.Min.Y+2))
	if got := u.TakeMedia(); got != "pause" {
		t.Fatalf("toggle while playing queued %q, want pause", got)
	}
	if u.TakeMedia() != "" {
		t.Error("TakeMedia is not one-shot")
	}

	// Paused -> the toggle resumes.
	u.SetMedia(MediaState{Active: true, Paused: true, Title: "Havana"})
	u.Press(WMediaPlay)
	u.Release(u.HitTest(play.Min.X+2, play.Min.Y+2))
	if got := u.TakeMedia(); got != "resume" {
		t.Errorf("toggle while paused queued %q, want resume", got)
	}

	// Stop button.
	u.Press(WMediaStop)
	u.Release(u.HitTest(stop.Min.X+2, stop.Min.Y+2))
	if got := u.TakeMedia(); got != "stop" {
		t.Errorf("stop button queued %q, want stop", got)
	}
}

func TestMediaBarTakesItsOwnLayoutSpace(t *testing.T) {
	// Expanded: the message area gives up the strip's height.
	u := NewUI(defaultWinW, defaultWinH)
	u.collapsed = false
	_, before := u.msgArea()
	u.SetMedia(MediaState{Active: true, Title: "Havana"})
	_, after := u.msgArea()
	if before-after != mediaH {
		t.Errorf("message area shrank by %d, want %d", before-after, mediaH)
	}
	if u.H != defaultWinH {
		t.Errorf("expanded window height changed to %d", u.H)
	}

	// Collapsed: header + strip + input bar, exactly. The strip grows the
	// window when the player appears and gives the space back when it ends.
	c := NewUI(defaultWinW, defaultWinH)
	c.collapsed = true
	c.H = headerH + inputH
	if got := c.HitTest(c.W/2, headerH+2); got == WMediaPlay {
		t.Error("strip is hit-testable while hidden")
	}
	c.SetMedia(MediaState{Active: true, Title: "Havana"})
	if c.H != headerH+inputH+mediaH {
		t.Fatalf("collapsed height = %d, want %d", c.H, headerH+inputH+mediaH)
	}
	// The strip starts right under the header, and the header keeps its own row.
	if got := c.mediaBar().Min.Y; got != headerH {
		t.Errorf("strip top = %d, want %d (right under the header)", got, headerH)
	}
	if got := c.HitTest(c.W/2, headerH/2); got != WHeader {
		t.Errorf("HitTest in the header = %v, want WHeader", got)
	}
	play := c.mediaPlayRect()
	if play.Max.Y > c.H-inputH || play.Min.Y < headerH {
		t.Errorf("strip rect %v does not fit between header and input bar", play)
	}
	c.SetMedia(MediaState{})
	if c.H != headerH+inputH {
		t.Errorf("collapsed height after stop = %d, want %d", c.H, headerH+inputH)
	}

	// Resize must not squeeze the strip out of a collapsed window.
	c.SetMedia(MediaState{Active: true, Title: "Havana"})
	c.Resize(c.W, 10)
	if c.H < headerH+inputH+mediaH {
		t.Errorf("Resize squeezed the collapsed window to %d", c.H)
	}
}

func TestMediaBarRenderSmoke(t *testing.T) {
	u := NewUI(defaultWinW, defaultWinH)
	u.collapsed = false
	u.SetMedia(MediaState{Active: true, Title: "Havana - Camila Cabello"})
	frame := u.Render()
	if frame.Bounds() != (image.Rect(0, 0, u.W, u.H)) {
		t.Fatalf("frame bounds %v", frame.Bounds())
	}
	at := func(f *image.NRGBA, x, y int) color.RGBA {
		i := f.PixOffset(x, y)
		return color.RGBA{f.Pix[i], f.Pix[i+1], f.Pix[i+2], f.Pix[i+3]}
	}
	bar := u.mediaBar()
	if at(frame, bar.Min.X+2, bar.Min.Y) == at(frame, bar.Min.X+2, bar.Min.Y+6) {
		t.Error("strip hairline not painted")
	}
	glyphPixels := func(r image.Rectangle) int {
		n := 0
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if at(frame, x, y) == colPlum {
					n++
				}
			}
		}
		return n
	}
	if n := glyphPixels(u.mediaPlayRect()); n < 10 {
		t.Errorf("play glyph pixels = %d, want a drawn glyph", n)
	}
	if n := glyphPixels(u.mediaStopRect()); n < 10 {
		t.Errorf("stop glyph pixels = %d, want a drawn glyph", n)
	}
	// Paused flips the label: the strip must change, and stay one row tall.
	row := bar.Min.Y + (mediaH-glyphH)/2 + 2
	before := at(frame, padX, row)
	u.SetMedia(MediaState{Active: true, Paused: true, Title: "Havana - Camila Cabello"})
	p2 := u.Render()
	if at(p2, padX, row) == before {
		t.Error("PAUSED state did not change the strip")
	}
}

func TestMediaBarTitleIsOneLineAndFits(t *testing.T) {
	if got := oneLine("a\nb\tc\r\nd"); got != "a b c d" {
		t.Errorf("oneLine = %q", got)
	}
	if got := ellipsize("short", 1, 1000); got != "short" {
		t.Errorf("ellipsize kept cutting a fitting title: %q", got)
	}
	got := ellipsize(strings.Repeat("x", 200), 1, 60)
	if !strings.HasSuffix(got, "...") || textWidth(got, 1) > 60 {
		t.Errorf("ellipsize(long) = %q, want a truncated title within 60px", got)
	}
	// A title that cannot fit at all is simply not drawn (no panic, no spill).
	u := NewUI(200, defaultWinH)
	u.collapsed = false
	u.SetMedia(MediaState{Active: true, Title: strings.Repeat("y", 300)})
	if got := u.Render(); got.Bounds() != image.Rect(0, 0, 200, u.H) {
		t.Errorf("narrow render bounds %v", got.Bounds())
	}
}
