package main

// pet_test.go - the pet say-pipe bridge: path auto-detection, flag/config
// resolution precedence (including the "auto" keyword), and the say-line
// composition for text + image replies.

import (
	"bufio"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolvePetPipe(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	const want = "/tmp/desktop-pet--0.say" // ":0" maps to "-0" like the pet

	cases := []struct {
		name            string
		flagVal, cfgVal string
		want            string
	}{
		{"config auto keyword", "", "auto", want},
		{"config auto case-insensitive", "", "AUTO", want},
		{"both empty auto-detects", "", "", want},
		{"whitespace trimmed", "  ", " auto ", want},
		{"flag off wins over config", "off", "auto", ""},
		{"config off", "", "off", ""},
		{"config off case-insensitive", "", "Off", ""},
		{"flag path wins over config auto", "/tmp/custom.say", "auto", "/tmp/custom.say"},
		{"config absolute path", "", "/tmp/from-ini.say", "/tmp/from-ini.say"},
		{"flag beats config path", "/tmp/custom.say", "/tmp/from-ini.say", "/tmp/custom.say"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePetPipe(tc.flagVal, tc.cfgVal); got != tc.want {
				t.Errorf("resolvePetPipe(%q, %q) = %q, want %q", tc.flagVal, tc.cfgVal, got, tc.want)
			}
		})
	}
}

func TestPetPipePath(t *testing.T) {
	cases := []struct{ display, want string }{
		{":0", "/tmp/desktop-pet--0.say"},
		{":1", "/tmp/desktop-pet--1.say"},
		{"", ""}, // no DISPLAY -> forwarding disabled
	}
	for _, tc := range cases {
		t.Setenv("DISPLAY", tc.display)
		if got := petPipePath(); got != tc.want {
			t.Errorf("petPipePath(DISPLAY=%q) = %q, want %q", tc.display, got, tc.want)
		}
	}
}

func TestPetCmdPathFor(t *testing.T) {
	cases := []struct{ sayPath, want string }{
		{"/tmp/desktop-pet--0.say", "/tmp/desktop-pet--0.cmd"},
		{"/tmp/custom.say", "/tmp/custom.cmd"},
		{"/tmp/custom.say/something.say", "/tmp/custom.say/something.cmd"},
		{"", ""}, // no say pipe -> no cmd pipe
	}
	for _, tc := range cases {
		if got := petCmdPathFor(tc.sayPath); got != tc.want {
			t.Errorf("petCmdPathFor(%q) = %q, want %q", tc.sayPath, got, tc.want)
		}
	}
}

// TestPetLaunchArgs checks the launch-argument mapping: the girl default
// launches flagless (the binary's own default), a boy passes -character kama,
// and only demo mode = true adds -demo=true (demo off is the binary default,
// so it stays off the command line).
func TestPetLaunchArgs(t *testing.T) {
	if got := petLaunchArgs("", false); len(got) != 0 {
		t.Errorf("empty gender, demo off: args %v, want none", got)
	}
	if got := petLaunchArgs("onidia", false); len(got) != 0 {
		t.Errorf("girl/onidia, demo off: args %v, want none (binary default)", got)
	}
	if got := petLaunchArgs("kama", false); len(got) != 2 || got[0] != "-character" || got[1] != "kama" {
		t.Errorf("boy/kama, demo off: args %v, want [-character kama]", got)
	}
	if got := petLaunchArgs("onidia", true); len(got) != 1 || got[0] != "-demo=true" {
		t.Errorf("girl/onidia, demo on: args %v, want [-demo=true]", got)
	}
	if got := petLaunchArgs("kama", true); len(got) != 3 ||
		got[0] != "-character" || got[1] != "kama" || got[2] != "-demo=true" {
		t.Errorf("boy/kama, demo on: args %v, want [-character kama -demo=true]", got)
	}
}

// TestPetCmdDeliversCommand checks that petCmd writes the exact command line to
// the pet's cmd FIFO (action/event forward from the LLM reply tags).
func TestPetCmdDeliversCommand(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "test.cmd")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			lines <- ""
			return
		}
		defer f.Close()
		l, err := bufio.NewReader(f).ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- l
	}()
	time.Sleep(50 * time.Millisecond) // let the reader open first

	petCmd(fifo, "action dance")

	select {
	case line := <-lines:
		if strings.TrimSpace(line) != "action dance" {
			t.Errorf("petCmd delivered %q, want %q", line, "action dance")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the cmd line")
	}
}

// TestQuitPet checks that QuitPet delivers the bare "quit" line to the pet's
// cmd FIFO - the command the pet answers with its poof-out animation - and
// reports `gone` once the FIFO's reader disappears (i.e. the pet exited).
func TestQuitPet(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "test.cmd")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			lines <- ""
			return
		}
		defer f.Close() // after this, petPipeReady reports no listener
		l, err := bufio.NewReader(f).ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- l
	}()
	time.Sleep(50 * time.Millisecond) // let the reader open first

	gone := make(chan struct{}, 1)
	QuitPet(fifo, gone)

	select {
	case line := <-lines:
		if strings.TrimSpace(line) != "quit" {
			t.Errorf("QuitPet delivered %q, want %q", line, "quit")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the quit line")
	}
	select {
	case <-gone:
		// exit confirmed by the watcher (readerless FIFO)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the gone signal")
	}
}

func TestBuildSayLine(t *testing.T) {
	cases := []struct {
		name, mood, text, imgPath, want string
	}{
		{"text only", "", "hello", "", "hello"},
		{"mood first", "happy", "hello", "", "[happy] hello"},
		{"image then caption", "wink", "look!", "/tmp/p.png", "[wink] [image /tmp/p.png] look!"},
		{"image with empty caption", "", "", "/tmp/p.png", "[image /tmp/p.png] "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildSayLine(tc.mood, tc.text, tc.imgPath); got != tc.want {
				t.Errorf("buildSayLine(%q, %q, %q) = %q, want %q", tc.mood, tc.text, tc.imgPath, got, tc.want)
			}
		})
	}
}

func TestSaveTempPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	path, err := saveTempPNG(img)
	if err != nil {
		t.Fatalf("saveTempPNG: %v", err)
	}
	defer os.Remove(path)

	if ok, _ := filepath.Match("chat-app-say-*.png", filepath.Base(path)); !ok {
		t.Errorf("temp file %q does not match pattern %q", path, sayImagePattern)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open saved png: %v", err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode saved png: %v", err)
	}
	if b := got.Bounds(); b.Dx() != 4 || b.Dy() != 3 {
		t.Errorf("decoded bounds %v, want 4x3", b)
	}
}

func TestPetSayFIFODeliversImageLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir) // say-image temp files land in the test dir
	fifo := filepath.Join(dir, "test.say")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		// Blocking open parks until petSay's writer opens the other end.
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			lines <- ""
			return
		}
		defer f.Close()
		l, err := bufio.NewReader(f).ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- l
	}()
	time.Sleep(50 * time.Millisecond) // let the reader open first (petSay retries ENXIO anyway)

	petSay(fifo, "happy", "look at this", image.NewRGBA(image.Rect(0, 0, 3, 2)))

	select {
	case line := <-lines:
		if line == "" {
			t.Fatal("reader goroutine failed")
		}
		if !strings.Contains(line, "[happy] ") ||
			!strings.Contains(line, "[image ") ||
			!strings.Contains(line, "look at this") {
			t.Errorf("say-line %q missing mood/image/caption", line)
		}
		m := sayImagePath.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("say-line %q has no [image path] tag", line)
		}
		f, err := os.Open(m[1])
		if err != nil {
			t.Fatalf("referenced image %s unreadable: %v", m[1], err)
		}
		defer f.Close()
		if _, err := png.Decode(f); err != nil {
			t.Errorf("referenced image %s is not a decodable png: %v", m[1], err)
		}
		os.Remove(m[1])
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for say-line")
	}
}

func TestPetSayFailureCleansTempImage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	count := func() int {
		m, _ := filepath.Glob(filepath.Join(dir, sayImagePattern))
		return len(m)
	}
	before := count()
	// No FIFO at that path -> open fails -> the temp image must not linger.
	petSay(filepath.Join(dir, "missing.say"), "happy", "hi", image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if after := count(); after > before {
		t.Errorf("failed say write leaked %d temp image(s)", after-before)
	}
}

// TestPetClearWritesToken checks that petClear pushes the reserved token line
// so the pet can dismiss its bubble exactly when audio playback ends.
func TestPetClearWritesToken(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "clear.say")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			lines <- ""
			return
		}
		defer f.Close()
		l, err := bufio.NewReader(f).ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- l
	}()
	time.Sleep(50 * time.Millisecond) // let the reader open first

	petClear(fifo)

	select {
	case line := <-lines:
		if strings.TrimSpace(line) != petSayClearToken {
			t.Errorf("petClear wrote %q, want the clear token %q", line, petSayClearToken)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the clear token")
	}
}

// TestBuildPetSayLineEmpty verifies nothing is built when forwarding is off or
// there is no content to show ("" text without an image).
func TestBuildPetSayLineEmpty(t *testing.T) {
	if got := buildPetSayLine("", "happy", "hi", image.NewRGBA(image.Rect(0, 0, 2, 2))); got != "" {
		t.Errorf("buildPetSayLine with empty pipe returned %q, want \"\"", got)
	}
	if got := buildPetSayLine("/tmp/x.say", "happy", "", nil); got != "" {
		t.Errorf("buildPetSayLine with empty text returned %q, want \"\"", got)
	}
	// Whitespace-only text is still a mood-only line (the pet shows the face
	// without a bubble) - kept for compatibility with petSay.
	if got := buildPetSayLine("/tmp/x.say", "happy", "  ", nil); got != "[happy]   " {
		t.Errorf("buildPetSayLine with blank text returned %q, want a mood-only line", got)
	}
}

// TestPetSayLineKeepsSayImage checks the raw-line writer delivers its content
// unchanged, so the TTS-synchronised path can push pre-built bubble lines.
func TestPetSayLineKeepsSayImage(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "raw.say")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			lines <- ""
			return
		}
		defer f.Close()
		l, err := bufio.NewReader(f).ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- l
	}()
	time.Sleep(50 * time.Millisecond)

	petSayLine(fifo, "[wink] [image /tmp/p.png] look at this")

	select {
	case line := <-lines:
		if strings.TrimSpace(line) != "[wink] [image /tmp/p.png] look at this" {
			t.Errorf("petSayLine delivered %q, want the raw line untouched", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the say-line")
	}
}
