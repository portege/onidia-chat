package main

// Tests for the AUTO SUBMIT setting: with it off the spoken prompt waits for
// the user (the behaviour before this feature existed), with it on the prompt
// goes out on its own one second after the transcript lands - and every route
// the user has to interrupt that is wired to actually cancel it.

import (
	"strings"
	"testing"
	"time"
)

// deliverTake runs a whole take through the fake backend and leaves the
// transcript in the textarea, exactly as a real mic click pair would. It
// deliberately does NOT wait out the grace window.
func deliverTake(t *testing.T, u *UI, text string) {
	t.Helper()
	drainUntilSettled(t, u)
	if got := strings.TrimSpace(string(u.input)); got != text {
		t.Fatalf("transcript landed as %q, want %q", got, text)
	}
}

// takeAndDeliver does the two mic clicks and waits for the transcript.
func takeAndDeliver(t *testing.T, u *UI, text string) {
	t.Helper()
	u.ToggleMic() // start
	u.ToggleMic() // stop -> transcribe
	deliverTake(t, u, text)
}

// TestAutoSubmitOffLeavesItWaiting is the default that must not change: the
// transcript sits in the textarea and the user sends it.
func TestAutoSubmitOffLeavesItWaiting(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake", text: "hello buddy"})
	if u.autoSubmit {
		t.Fatal("auto-submit should default to off")
	}
	takeAndDeliver(t, u, "hello buddy")
	if u.autoSubmitPending() {
		t.Fatal("a send is counting down with auto-submit off")
	}
	// Well past the grace window, polled as the main loop would.
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond)
		u.DrainSTT()
	}
	if len(u.msgs) != 1 { // only the opening greeting
		t.Fatalf("messages = %v, want nothing sent", u.msgs)
	}
	if strings.TrimSpace(string(u.input)) != "hello buddy" {
		t.Error("the transcript should still be in the textarea")
	}
}

// TestAutoSubmitSendsAfterTheDelay is the new workflow: nothing is sent during
// the grace window, and the prompt goes out on its own once it passes.
func TestAutoSubmitSendsAfterTheDelay(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake", text: "hello buddy"})
	u.autoSubmit = true
	takeAndDeliver(t, u, "hello buddy")

	// Inside the window: the words are on screen but nothing has gone out.
	if !u.autoSubmitPending() {
		t.Fatal("a transcript should have armed the auto-submit")
	}
	if !strings.Contains(u.sttNote, "Esc") {
		t.Errorf("note = %q, want it to say how to cancel", u.sttNote)
	}
	if len(u.msgs) != 1 {
		t.Fatalf("messages = %d, want only the greeting before the delay passes", len(u.msgs))
	}
	u.DrainSTT() // still inside the window
	if len(u.msgs) != 1 {
		t.Fatal("the prompt was sent before the grace window ended")
	}

	// Put the deadline in the past rather than sleeping through it: the test is
	// about the wiring, not about wall-clock timing.
	u.sttAutoAt = time.Now().Add(-time.Millisecond)
	u.DrainSTT()
	if u.autoSubmitPending() {
		t.Error("the countdown should be cleared once it has fired")
	}
	if len(u.msgs) != 2 {
		t.Fatalf("messages = %d, want the prompt sent (2 with the greeting)", len(u.msgs))
	}
	if got := u.msgs[1].Text; got != "hello buddy" {
		t.Errorf("sent %q, want %q", got, "hello buddy")
	}
	if strings.TrimSpace(string(u.input)) != "" {
		t.Error("the textarea should be empty after sending")
	}
}

// TestAutoSubmitNoteFits guards the one thing about the note that is easy to
// break silently: fitCols cuts it to the width of the input bar, and what it
// cuts is always the tail - which is where the key to press lives. A longer
// phrasing would still pass every other test here and still tell the user
// nothing about how to stop it.
func TestAutoSubmitNoteFits(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake", text: "hello buddy"})
	u.autoSubmit = true
	takeAndDeliver(t, u, "hello buddy")
	if u.sttNote == "" {
		t.Fatal("no note shown while a send is counting down")
	}
	// The width the real window gives the note, not a test-sized one.
	u.W, u.H = defaultWinW, defaultWinH
	cols := (u.inputRect().Dx() - 20) / (advW * uiFontScale)
	if got := fitCols(u.sttNote, cols); got != u.sttNote {
		t.Errorf("note %q does not fit in %d columns at width %d, renders as %q",
			u.sttNote, cols, defaultWinW, got)
	}
	if !strings.Contains(u.sttNote, "Esc") {
		t.Errorf("note %q should name the key that stops the send", u.sttNote)
	}
}

// TestAutoSubmitArmedOnlyWithWords guards the empty-transcript case: silence
// must not fire a countdown that would show "sending" and then send nothing.
func TestAutoSubmitArmedOnlyWithWords(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake", text: "   "})
	u.autoSubmit = true
	takeAndDeliver(t, u, "")
	if u.autoSubmitPending() {
		t.Error("an empty transcript should not arm the auto-submit")
	}
	if u.sttNote != "" {
		t.Errorf("note = %q, want none for an empty take", u.sttNote)
	}
}

// TestAutoSubmitCancelRoutes walks every way the user can take the prompt back
// during the grace window. Each subtest arms a pending send, performs one
// interaction, and requires the countdown to be gone.
func TestAutoSubmitCancelRoutes(t *testing.T) {
	arm := func(t *testing.T) *UI {
		t.Helper()
		u := newSTTTestUI(t, &fakeSTT{name: "fake", text: "hello buddy"})
		u.autoSubmit = true
		takeAndDeliver(t, u, "hello buddy")
		if !u.autoSubmitPending() {
			t.Fatal("nothing armed to cancel")
		}
		return u
	}
	// keepsText is whether the transcript should survive the interaction.
	cases := []struct {
		name      string
		act       func(u *UI)
		keepsText bool
		sends     bool // whether the interaction itself sends the prompt
	}{
		{"typing a character", func(u *UI) { u.Key('!', 0) }, true, false},
		{"backspace", func(u *UI) { u.Key(0, ksBackspace) }, true, false},
		{"escape", func(u *UI) { u.Key(0, ksEscape) }, false, false},
		{"enter", func(u *UI) { u.Key(0, ksReturn) }, false, true},
		{"the mic again", func(u *UI) { u.ToggleMic() }, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := arm(t)
			before := len(u.msgs)
			// Expire the deadline BEFORE interrupting. A cancel that only works
			// while the countdown is still running is not a cancel: the real
			// race is the user's keystroke landing in the same frame the timer
			// fires, and this is the only way to reproduce that.
			u.sttAutoAt = time.Now().Add(-time.Hour)
			c.act(u)
			if u.autoSubmitPending() {
				t.Errorf("%s left the send counting down", c.name)
			}
			u.DrainSTT()
			switch {
			case c.sends && len(u.msgs) != before+1:
				t.Errorf("%s: messages %d -> %d, want the prompt sent once",
					c.name, before, len(u.msgs))
			case !c.sends && len(u.msgs) != before:
				t.Errorf("%s: messages %d -> %d, want nothing sent",
					c.name, before, len(u.msgs))
			}
			if got := strings.TrimSpace(string(u.input)); (got != "") != c.keepsText {
				t.Errorf("%s left the textarea as %q, want text kept: %v", c.name, got, c.keepsText)
			}
		})
	}
}

// TestAutoSubmitOffMidCountdown: switching the checkbox off while a send is
// pending must stop that send too, not just the next one.
func TestAutoSubmitOffMidCountdown(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake", text: "hello buddy"})
	u.autoSubmit = true
	takeAndDeliver(t, u, "hello buddy")
	if !u.autoSubmitPending() {
		t.Fatal("nothing armed")
	}
	// The committed flag, as saveSettings would set it.
	u.autoSubmit = false
	u.cancelAutoSubmit()
	if u.autoSubmitPending() {
		t.Error("the pending send survived switching the setting off")
	}
	u.DrainSTT()
	if len(u.msgs) != 1 {
		t.Error("the prompt went out after the setting was turned off")
	}
}
