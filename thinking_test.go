package main

// thinking_test.go - the <THINKING> block is peeled off before anything else
// looks at the reply, so a tag written while the model is reasoning can never
// reach the pet, the pager or an ability. That ordering is the whole point of
// the feature and is easy to break by accident.

import (
	"context"
	"image"
	"strings"
	"testing"

	"github.com/portege/chat-app/agent"
)

func TestSplitThinkingPeelsTheBlock(t *testing.T) {
	cases := []struct{ raw, think, rest string }{
		{"<THINKING>hmm</THINKING>hello", "hmm", "hello"},
		{"<thinking>a</thinking>b", "a", "b"},
		{"<Thinking>Mixed</Thinking>ok", "Mixed", "ok"},
		{"before <THINKING>x</THINKING> after", "x", "before after"},
		{"no tags at all", "", "no tags at all"},
		{"", "", ""},
		// Two blocks: the first wins, the answer keeps what is left. The join is
		// verbatim, so "A" and "B" do not gain a page-break newline.
		{"<THINKING>one</THINKING>A<THINKING>two</THINKING>B", "one", "AB"},
		// A block with no gap either side must not invent one.
		{"pre<THINKING>x</THINKING>post", "x", "prepost"},
		// Multi-paragraph answers survive a leading block: collapseSpaces only
		// runs on text that had a block cut out of it.
		{"<THINKING>x</THINKING>line one\nline two", "x", "line one line two"},
		// Whitespace inside the block is the model's, not ours.
		{"<THINKING>\n  spaced  \n</THINKING>y", "spaced", "y"},
	}
	for _, c := range cases {
		gotThink, gotRest := splitThinking(c.raw)
		if gotThink != c.think || gotRest != c.rest {
			t.Errorf("splitThinking(%q) = (%q, %q), want (%q, %q)",
				c.raw, gotThink, gotRest, c.think, c.rest)
		}
	}
}

// A stream caught mid-block has no closing tag yet. The partial text must be
// treated as reasoning, not leaked into the answer bubble where it would be
// read aloud by the pet.
func TestSplitThinkingHandlesUnclosedBlock(t *testing.T) {
	think, rest := splitThinking("<THINKING>the user wants a song, maybe")
	if think != "the user wants a song, maybe" {
		t.Errorf("thinking = %q, want the partial reasoning", think)
	}
	if rest != "" {
		t.Errorf("answer = %q, want empty until the block closes", rest)
	}
	// Text BEFORE the open tag is still the answer.
	think, rest = splitThinking("[happy] hi <THINKING>partial")
	if rest != "[happy] hi" || think != "partial" {
		t.Errorf("got (%q, %q), want (%q, %q)", think, rest, "partial", "[happy] hi")
	}
	// A closed block is not "unclosed", even if a stray open tag precedes it.
	_, rest = splitThinking("<THINKING>a</THINKING>b <THINKING>")
	if !strings.Contains(rest, "b") {
		t.Errorf("rest = %q, want the trailing answer kept", rest)
	}
}

// The security-relevant case: a model that mentions an ability WHILE THINKING
// has not asked for it. Nothing may run, and the mood tag inside the reasoning
// must not be honoured either.
func TestTagsInsideThinkingCannotFireAnything(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	spy := &countingAgent{}
	if err := agent.Register(spy); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{canned: "[AGENT: spy text=\"boom\"]\n<THINKING>maybe I should " +
		"[AGENT: spy text=\"boom\"] or use [AGENT: spy text=\"boom\"]</THINKING>[happy] done"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
		ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
	res := bot.Reply([]Msg{{From: "you", Text: "do it"}}, "do it")

	if spy.calls != 0 {
		t.Errorf("the ability ran %d times from tags inside <THINKING>; want 0", spy.calls)
	}
	if res.Thinking == "" {
		t.Error("Thinking is empty, so the reasoning would not be shown")
	}
	// The mood tag in the ANSWER is real and must survive.
	if res.petLine == "" || !strings.Contains(res.petLine, "happy") {
		t.Errorf("petLine = %q, want the answer's [happy] mood", res.petLine)
	}
}

// Reasoning must not be SPOKEN or shown in the chat. It is now deliberately on
// the pet say-line - that is how the pet learns to draw it in her own cloud -
// so the assertion is about the caption and the TTS line, not the pipe payload.
func TestThinkingIsNotSpokenOrDisplayed(t *testing.T) {
	fp := &fakeProvider{canned: "<THINKING>secret internal reasoning here</THINKING>[happy] hi there"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
		ImageSource: "/tmp/desktop-pet--0.say", PetPipe: "/tmp/desktop-pet--0.say"}
	res := bot.Reply([]Msg{{From: "you", Text: "hi"}}, "hi")
	if res.Thinking != "secret internal reasoning here" {
		t.Errorf("Thinking = %q", res.Thinking)
	}
	if strings.Contains(res.Text, "secret internal") {
		t.Errorf("the reasoning leaked into Text: %q", res.Text)
	}
	// It IS forwarded, inside its own tag, so the pet can render a cloud. What
	// matters is that it is tagged as reasoning and not glued onto the caption
	// the pet speaks.
	if !strings.Contains(res.petLine, thinkTagOpen+"secret internal reasoning here"+thinkTagClose) {
		t.Errorf("petLine = %q, want the reasoning forwarded inside a <THINKING> tag", res.petLine)
	}
	if !strings.HasSuffix(res.petLine, "hi there") {
		t.Errorf("petLine = %q, want it to end with the spoken caption", res.petLine)
	}
}

// The cloud is a real, separate bubble: it has its own block, its own size and
// its own colour, and it never overlaps the answer. A long ramble is capped so
// it cannot push the answer off screen.
func TestThinkingRendersAsItsOwnBubble(t *testing.T) {
	u := thinkingTestUI()
	u.AddThinking("bot", "the answer", "a short thought", nil, false)
	bs := u.blocks()
	if len(bs) != 1 {
		t.Fatalf("got %d blocks, want 1 (cloud and answer share a block)", len(bs))
	}
	b := bs[0]
	if len(b.thinkLines) == 0 {
		t.Fatal("no cloud lines laid out")
	}
	if b.thinkH <= 0 {
		t.Errorf("cloud height = %d, want > 0", b.thinkH)
	}
	// The block must be taller than the answer alone would be.
	plain := u.blockFor(Msg{From: "bot", Text: "the answer"}, u.bubbleCols(), (u.W-2*padX)*3/4)
	if b.h <= plain.h {
		t.Errorf("block height with a cloud = %d, not taller than %d without one", b.h, plain.h)
	}
	// The cloud is narrower than a bubble and its font is half the size, so a
	// long thought cannot dominate the answer.
	if b.thinkW > b.bubW {
		t.Errorf("cloud width %d exceeds bubble width %d", b.thinkW, b.bubW)
	}

	// A very long ramble is capped.
	long := strings.Repeat("thinking about it ", 60)
	u2 := thinkingTestUI()
	u2.AddThinking("bot", "ok", long, nil, false)
	if got := len(u2.blocks()[0].thinkLines); got > thinkMaxLn {
		t.Errorf("cloud has %d lines, want at most %d", got, thinkMaxLn)
	}

	// No reasoning means no cloud and no extra height.
	u3 := thinkingTestUI()
	u3.AddMsg("bot", "plain answer")
	if len(u3.blocks()[0].thinkLines) != 0 {
		t.Error("a plain message grew a thought cloud")
	}
}

// The cloud must be drawn ABOVE the answer and inside the messages area, with
// real pixels of its own colour - not silently skipped.
func TestThinkingCloudIsActuallyDrawn(t *testing.T) {
	u := thinkingTestUI()
	u.AddThinking("bot", "the answer", "a thought worth showing", nil, false)
	frame := u.Render()
	if countColourIn(frame, image.Rect(0, 0, u.W, u.H), colCloudFill) < 20 {
		t.Error("no cloud fill drawn")
	}
	if countColourIn(frame, image.Rect(0, 0, u.W, u.H), colCloudEdge) < 10 {
		t.Error("no cloud rim drawn")
	}
	// The cloud sits above the bubble: compare the first row of each colour.
	cloudTop, bubbleTop := -1, -1
	for y := 0; y < u.H; y++ {
		if cloudTop < 0 && countColourIn(frame, image.Rect(0, y, u.W, y+1), colCloudFill) > 0 {
			cloudTop = y
		}
		if bubbleTop < 0 && countColourIn(frame, image.Rect(0, y, u.W, y+1), colBubbleFill) > 0 {
			bubbleTop = y
		}
	}
	if cloudTop < 0 || bubbleTop < 0 {
		t.Fatalf("cloud or bubble missing (cloudTop=%d bubbleTop=%d)", cloudTop, bubbleTop)
	}
	if cloudTop > bubbleTop {
		t.Errorf("cloud starts at y=%d, below the answer bubble at y=%d; it should sit above",
			cloudTop, bubbleTop)
	}
}

// Streaming: reasoning fills the cloud live, and the answer stays out of it
// until the block actually closes.
func TestThinkingCloudStreamsSeparately(t *testing.T) {
	u := thinkingTestUI()
	u.Thinking = true
	// Mid-block: no closing tag yet.
	u.SetStreamText("<THINKING>the user wants a song and I should")
	if u.streamThink == "" {
		t.Error("mid-stream reasoning did not reach the cloud")
	}
	if u.streamText != "" {
		t.Errorf("mid-stream reasoning leaked into the answer: %q", u.streamText)
	}
	// Once the block closes, the answer appears in its own bubble.
	u.SetStreamText("<THINKING>the user wants a song</THINKING>[happy] playing it now")
	if u.streamThink != "the user wants a song" {
		t.Errorf("streamThink = %q", u.streamThink)
	}
	if !strings.Contains(u.streamText, "playing it now") {
		t.Errorf("streamText = %q, want the answer", u.streamText)
	}
	b := u.blocks()[0]
	if len(b.thinkLines) == 0 || len(b.lines) == 0 {
		t.Error("the streaming bubble should have both a cloud and an answer")
	}
}

type countingAgent struct{ calls int }

func (a *countingAgent) ID() string            { return "spy" }
func (a *countingAgent) Description() string   { return "spy" }
func (a *countingAgent) Params() []agent.Param { return nil }
func (a *countingAgent) Run(ctx context.Context, args map[string]string) (agent.Result, error) {
	a.calls++
	return agent.Result{Message: "boom"}, nil
}

// thinkingTestUI is a window with NewUI's seeded greeting removed and the
// messages area expanded, so block indices and pixel positions are the real
// ones a reply is drawn into.
func thinkingTestUI() *UI {
	u := NewUI(defaultWinW, defaultWinH)
	u.msgs = nil
	u.collapsed = false
	u.H = max(defaultWinH, 520)
	return u
}
