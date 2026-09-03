package main

// preview.go - headless UI previews. `-preview` renders representative chat
// window states to chat_ui_*.png and exits (same idea as the desktop-pet's
// `-debug` frame dumper) so the interface can be reviewed without a display.

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// testImage returns a small striped placeholder for previewing image bubbles.
func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/20+y/20)%2 == 0 {
				img.Set(x, y, color.RGBA{95, 207, 214, 255})
			} else {
				img.Set(x, y, color.RGBA{165, 236, 239, 255})
			}
		}
	}
	return img
}

func dumpPreviews() {
	// Collapsed: just the header + prompt box (default startup state).
	u0 := NewUI(380, headerH+inputH)
	writePNG("chat_ui_collapsed.png", u0.Render())

	// Fresh window: welcome bubble, empty textarea with placeholder,
	// disabled SEND.
	u := NewUI(380, 520)
	u.collapsed = false
	writePNG("chat_ui_empty.png", u.Render())

	// Mid-conversation: bubbles on both sides, typed text, focused caret,
	// hovered SEND.
	u2 := NewUI(380, 520)
	u2.collapsed = false
	seedConvo(u2)
	u2.input = []rune("tell me a joke")
	u2.focused = true
	u2.caret = true
	u2.hover = WButton
	writePNG("chat_ui_convo.png", u2.Render())

	// After submit: the user's message and the bot's reply appended, input
	// cleared. Drain the reply channel so the render is deterministic.
	u3 := NewUI(380, 520)
	u3.collapsed = false
	seedConvo(u3)
	u3.input = []rune("tell me a joke")
	u3.Submit()
	res := <-u3.Replies
	u3.AddMsg(u3.Bot.Name, res.Text)
	u3.Thinking = false
	writePNG("chat_ui_sent.png", u3.Render())

	// Reply with an image: a bot bubble that includes a thumbnail.
	u6 := NewUI(380, 520)
	u6.collapsed = false
	seedConvo(u6)
	u6.AddMsgWithImage(u6.Bot.Name,
		"bali is a beautiful island in indonesia, famous for its temples, beaches and rice terraces.",
		testImage(260, 140))
	writePNG("chat_ui_image.png", u6.Render())

	// Waiting for Gemini: the "..." thinking bubble below the user's entry.
	u5 := NewUI(380, 520)
	u5.collapsed = false
	seedConvo(u5)
	u5.input = []rune("tell me a joke")
	u5.Submit()
	writePNG("chat_ui_thinking.png", u5.Render())

	// Settings modal over an expanded conversation: opened via the gear,
	// dropdowns closed, SAVE/CANCEL at the bottom.
	u7 := NewUI(380, 520)
	u7.collapsed = false
	seedConvo(u7)
	u7.name = "Onidia"
	u7.age = 10
	u7.sleepFrom, u7.sleepTo = 22, 7
	u7.openSettings()
	u7.hover = WDrop
	writePNG("chat_ui_settings.png", u7.Render())

	// Same modal with the age dropdown expanded (7-13); the option list
	// overlays the buttons and the pointer rests on the selected row.
	u8 := NewUI(380, 520)
	u8.collapsed = false
	seedConvo(u8)
	u8.name = "Onidia"
	u8.age = 10
	u8.sleepFrom, u8.sleepTo = 22, 7
	u8.openSettings()
	u8.openDrop = dropAge
	u8.hover = WOption
	u8.optIdx = 3 // row for age 10
	writePNG("chat_ui_settings_open.png", u8.Render())

	// Sleep-time dropdowns: the FROM list is expanded. Only five of the 24
	// hours fit, so the list is scrolled to the selection with a scrollbar.
	u9 := NewUI(380, 520)
	u9.collapsed = false
	seedConvo(u9)
	u9.name = "Onidia"
	u9.age = 10
	u9.sleepFrom, u9.sleepTo = 22, 7
	u9.openSettings()
	u9.openDrop = dropFrom
	u9.hourScroll = 19 // rows 19:00..23:00 visible, 22:00 highlighted
	u9.hover = WOption
	u9.optIdx = 22
	writePNG("chat_ui_settings_sleep.png", u9.Render())

	// Narrow window: the layout reflows (bubbles and textarea shrink).
	u4 := NewUI(280, 430)
	u4.collapsed = false
	seedConvo(u4)
	u4.input = []rune("narrow but happy")
	u4.focused = true
	u4.caret = true
	writePNG("chat_ui_narrow.png", u4.Render())
}

// seedConvo fills a UI with a small demo conversation.
func seedConvo(u *UI) {
	u.AddMsg("you", "hey! what can you do?")
	u.AddMsg("bot", "a lot - once my brain is wired in! for now i mostly show off this ui and its speech bubbles.")
	u.AddMsg("you", "fair enough. the bubbles look nice, where are they from?")
	u.AddMsg("bot", "the desktop-pet drew them first. same plum outlines, same tiny font!")
}

func writePNG(name string, img *image.NRGBA) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return err
	}
	fmt.Println("wrote", name)
	return nil
}
