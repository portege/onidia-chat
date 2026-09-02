package main

import "testing"

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
